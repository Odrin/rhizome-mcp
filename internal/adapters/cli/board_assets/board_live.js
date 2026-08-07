(function(){
  class BoardLiveClient {
    constructor(root, options){
      options = options || {};
      this.root = root;
      this.documentImpl = options.document || document;
      this.windowImpl = options.window || window;
      this.fetchImpl = options.fetch
        || (this.windowImpl && typeof this.windowImpl.fetch === "function" ? this.windowImpl.fetch.bind(this.windowImpl) : fetch);
      this.setTimeoutImpl = options.setTimeout
        || (this.windowImpl && typeof this.windowImpl.setTimeout === "function" ? this.windowImpl.setTimeout.bind(this.windowImpl) : setTimeout);
      this.clearTimeoutImpl = options.clearTimeout
        || (this.windowImpl && typeof this.windowImpl.clearTimeout === "function" ? this.windowImpl.clearTimeout.bind(this.windowImpl) : clearTimeout);
      this.randomImpl = options.random || Math.random;
      this.nowImpl = options.now || (() => new Date());
      this.domParserImpl = options.DOMParser || DOMParser;
      this.endpoint = options.endpoint || "/api/board";
      this.pageRoute = options.pageRoute || "/";
      this.intervalMs = options.intervalMs || 15000;
      this.backoffMs = options.backoffMs || 1000;
      this.maxBackoffMs = options.maxBackoffMs || 30000;
      this.timer = null;
      this.etag = options.etag || "";
      this.lastSuccessfulRefreshAt = null;
      this.staleStatus = null;
      this.isHidden = this.documentImpl.visibilityState === "hidden";
      this.statusElement = null;
      this.start();
    }

    start(){
      this.stop();
      this.schedule();
      this.documentImpl.addEventListener("visibilitychange", this.handleVisibilityChange);
      this.windowImpl.addEventListener("pagehide", this.handlePageHide);
    }

    stop(){
      if (this.timer !== null) {
        this.clearTimeoutImpl(this.timer);
        this.timer = null;
      }
    }

    schedule(){
      if (this.isHidden) {
        return;
      }
      this.timer = this.setTimeoutImpl(() => this.refresh(false), this.intervalMs);
    }

    handleVisibilityChange = () => {
      if (this.documentImpl.visibilityState === "hidden") {
        this.isHidden = true;
        this.stop();
        return;
      }
      this.isHidden = false;
      this.refresh(true);
    };

    handlePageHide = () => {
      this.isHidden = true;
      this.stop();
    };

    async refresh(immediate){
      if (this.isHidden && !immediate) {
        return;
      }
      try {
        const headers = this.etag ? {"If-None-Match": this.etag} : {};
        const response = await this.fetchImpl(this.endpoint, {headers, cache: "no-store"});
        if (response.status === 304) {
          this.lastSuccessfulRefreshAt = this.nowImpl();
          this.clearStale();
          this.backoffMs = 1000;
          this.schedule();
          return;
        }
        if (!response.ok) {
          throw new Error("request failed");
        }
        this.etag = response.headers.get("ETag") || this.etag;
        const pageHTML = await this.fetchPageHTML();
        this.refreshContent(pageHTML);
        this.clearStale();
        this.backoffMs = 1000;
        this.schedule();
      } catch (error) {
        this.markStale();
        this.scheduleWithBackoff();
      }
    }

    async fetchPageHTML(){
      const response = await this.fetchImpl(this.pageRoute, {cache: "no-store"});
      if (!response.ok) {
        throw new Error("page fetch failed");
      }
      if (typeof response.text === "function") {
        return response.text();
      }
      return response.body;
    }

    refreshContent(body){
      const parser = new this.domParserImpl();
      const doc = parser.parseFromString(body, "text/html");
      const newMain = doc.querySelector("main[data-board-main]");
      if (!newMain || !this.root) {
        return;
      }
      const previousOpenDetails = [];
      this.root.querySelectorAll("details[id]").forEach((detail) => {
        if (detail.open) {
          previousOpenDetails.push(detail.id);
        }
      });
      const activeElement = this.documentImpl.activeElement;
      const focusedID = activeElement && activeElement.id ? activeElement.id : "";
      const previousScrollY = this.windowImpl.scrollY;
      this.root.innerHTML = newMain.innerHTML;
      this.root.querySelectorAll("details[id]").forEach((detail) => {
        if (previousOpenDetails.includes(detail.id)) {
          detail.open = true;
        }
      });
      if (focusedID) {
        const nextFocusTarget = this.root.querySelector("#" + focusedID);
        if (nextFocusTarget) {
          nextFocusTarget.focus();
        }
      }
      this.windowImpl.scrollTo(0, previousScrollY);
      this.lastSuccessfulRefreshAt = this.nowImpl();
      this.renderStaleStatus();
    }

    markStale(){
      this.staleStatus = "stale";
      this.renderStaleStatus();
    }

    clearStale(){
      this.staleStatus = null;
      this.renderStaleStatus();
    }

    scheduleWithBackoff(){
      const baseDelay = this.backoffMs;
      const nextBackoff = Math.min(baseDelay * 2, this.maxBackoffMs);
      const jitter = Math.max(0, Math.min(1, this.randomImpl()));
      const delay = nextBackoff * (0.75 + jitter * 0.5);
      this.backoffMs = nextBackoff;
      this.timer = this.setTimeoutImpl(() => this.refresh(false), delay);
    }

    renderStaleStatus(){
      if (!this.root) {
        return;
      }
      if (!this.statusElement) {
        this.statusElement = this.documentImpl.createElement("div");
        this.statusElement.id = "board-refresh-status";
        this.statusElement.setAttribute("role", "status");
        this.statusElement.setAttribute("aria-live", "polite");
        this.statusElement.style.cssText = "margin-bottom: 0.75rem; color: #b45309; font-size: 0.875rem;";
        if (this.root.parentNode) {
          this.root.parentNode.insertBefore(this.statusElement, this.root);
        }
      }
      if (this.staleStatus) {
        const stamp = this.lastSuccessfulRefreshAt ? this.lastSuccessfulRefreshAt.toLocaleTimeString() : "unknown";
        this.statusElement.textContent = "stale • last success " + stamp;
        this.statusElement.hidden = false;
        return;
      }
      this.statusElement.textContent = "";
      this.statusElement.hidden = true;
    }
  }

  const root = document.querySelector("main[data-board-main]");
  if (root) {
    const endpoint = root.getAttribute("data-board-endpoint") || "/api/board";
    const pageRoute = root.getAttribute("data-board-route") || "/";
    const client = new BoardLiveClient(root, {endpoint: endpoint, pageRoute: pageRoute, intervalMs: 15000});
    window.__rhizomeBoardLiveClient = client;
  }

  if (window.__rhizomeBoardLiveTestHooks) {
    window.__rhizomeBoardLiveTestHooks.BoardLiveClient = BoardLiveClient;
  }
})();
