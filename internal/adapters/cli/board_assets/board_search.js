(function(){
  class BoardSearchClient {
    constructor(root, options){
      options = options || {};
      this.root = root;
      this.fetchImpl = options.fetch || fetch;
      this.documentImpl = options.document || document;
      this.windowImpl = options.window || window;
      this.historyImpl = options.history || this.windowImpl.history;
      this.requestId = 0;
      this.activeController = null;
      this.currentQuery = "";
      this.currentEntityType = "";
      this.results = [];
      this.nextCursor = null;
      this.hasMore = false;
      this.bind();
      this.initializeFromLocation();
    }

    bind(){
      if (!this.root) {
        return;
      }
      const form = this.root.querySelector("form[data-board-search-form]");
      if (form) {
        form.addEventListener("submit", (event) => {
          event.preventDefault();
          this.submitForm();
        });
      }
      if (this.windowImpl && typeof this.windowImpl.addEventListener === "function") {
        this.windowImpl.addEventListener("popstate", () => this.initializeFromLocation());
      }
    }

    initializeFromLocation(){
      if (!this.windowImpl || !this.windowImpl.location) {
        return;
      }
      const params = new URLSearchParams(this.windowImpl.location.search || "");
      const query = (params.get("q") || "").trim();
      const entityType = params.get("entity_type") || "";
      if (!query) {
        this.resetState();
        this.renderInitialState();
        return;
      }
      this.runSearch(query, {entityType: entityType || "", append: false, replace: true});
    }

    submitForm(){
      if (!this.root) {
        return;
      }
      const input = this.root.querySelector("input[name=search]");
      const select = this.root.querySelector("select[name=entity_type]");
      const query = input ? (input.value || "").trim() : "";
      const entityType = select ? (select.value || "") : "";
      this.applyLocationState(query, entityType);
      this.runSearch(query, {entityType, append: false});
    }

    loadMore(){
      if (!this.hasMore || !this.currentQuery) {
        return;
      }
      this.runSearch(this.currentQuery, {entityType: this.currentEntityType, append: true, cursor: this.nextCursor});
    }

    applyLocationState(query, entityType){
      const params = new URLSearchParams();
      if (query) {
        params.set("q", query);
      }
      if (entityType && entityType !== "all") {
        params.set("entity_type", entityType);
      }
      const suffix = params.toString();
      const nextUrl = "/search" + (suffix ? "?" + suffix : "");
      if (this.historyImpl && typeof this.historyImpl.pushState === "function") {
        this.historyImpl.pushState({query, entityType}, "", nextUrl);
      }
      if (this.windowImpl && this.windowImpl.location) {
        this.windowImpl.location.search = suffix ? "?" + suffix : "";
      }
    }

    resetState(){
      this.currentQuery = "";
      this.currentEntityType = "";
      this.results = [];
      this.nextCursor = null;
      this.hasMore = false;
      this.activeCursor = null;
      this.clearLoading();
    }

    async runSearch(query, options){
      options = options || {};
      const trimmedQuery = (query || "").trim();
      const entityType = options.entityType || "";
      if (!trimmedQuery) {
        this.resetState();
        this.renderInitialState();
        return;
      }
      if (!options.append) {
        this.results = [];
        this.nextCursor = null;
        this.hasMore = false;
      }
      this.currentQuery = trimmedQuery;
      this.currentEntityType = entityType;
      this.activeCursor = options.append ? (options.cursor || null) : null;
      this.renderLoadingState(trimmedQuery, entityType);
      this.requestId += 1;
      const requestId = this.requestId;
      if (this.activeController) {
        try {
          this.activeController.abort();
        } catch (error) {
          // ignore abort errors
        }
      }
      const controller = typeof AbortController !== "undefined" ? new AbortController() : null;
      this.activeController = controller;
      const params = new URLSearchParams({q: trimmedQuery});
      if (entityType && entityType !== "all") {
        params.set("entity_type", entityType);
      }
      if (options.append && options.cursor) {
        params.set("cursor", options.cursor);
      }
      const requestURL = "/api/search?" + params.toString();
      try {
        const response = await this.fetchImpl(requestURL, {cache: "no-store", signal: controller ? controller.signal : undefined});
        if (requestId !== this.requestId) {
          return;
        }
        if (!response || !response.ok) {
          this.renderErrorState();
          return;
        }
        const payload = response.json ? await response.json() : null;
        if (requestId !== this.requestId) {
          return;
        }
        this.results = options.append ? this.mergeResults(this.results, payload && payload.results ? payload.results : []) : (payload && payload.results ? payload.results : []);
        this.nextCursor = payload && payload.next_cursor ? payload.next_cursor : null;
        this.hasMore = !!(payload && payload.has_more);
        this.activeCursor = options.append ? (options.cursor || null) : null;
        this.renderResults(this.results);
      } catch (error) {
        if (requestId !== this.requestId) {
          return;
        }
        this.renderErrorState();
      }
    }

    mergeResults(existing, incoming){
      const merged = existing.slice();
      const seen = new Set(merged.map((item) => item.entity_type + "::" + item.entity_id));
      for (const item of incoming || []) {
        const key = item.entity_type + "::" + item.entity_id;
        if (!seen.has(key)) {
          seen.add(key);
          merged.push(item);
        }
      }
      return merged;
    }

    renderInitialState(){
      this.renderResults([]);
      this.renderStatus("Initial search: enter a query to find issues, comments, decisions, reviews, and attempt notes.");
    }

    renderLoadingState(query, entityType){
      this.renderStatus("Searching for \"" + query + "\"...");
      this.renderResults([]);
      this.setLoading(true);
    }

    renderErrorState(){
      this.setLoading(false);
      this.renderStatus("Search error. Please try again.");
      this.renderResults([]);
    }

    renderResults(results){
      this.setLoading(false);
      const container = this.root ? this.root.querySelector("[data-board-search-results]") : null;
      if (!container) {
        return;
      }
      if (typeof container.replaceChildren === "function") {
        container.replaceChildren();
      } else {
        container.children = [];
      }
      container.textContent = "";
      if (!results || results.length === 0) {
        const empty = this.documentImpl.createElement("p");
        empty.className = "empty";
        empty.textContent = this.currentQuery ? "No results found." : "Enter a search query to find issues, comments, decisions, reviews, and attempt notes.";
        container.appendChild(empty);
        return;
      }
      const list = this.documentImpl.createElement("ul");
      for (const result of results) {
        const item = this.documentImpl.createElement("li");
        item.setAttribute("data-board-search-result", "");
        const title = this.documentImpl.createElement("strong");
        title.textContent = result.title || "";
        item.appendChild(title);
        const entity = this.documentImpl.createElement("div");
        entity.textContent = (result.entity_type || "").replace(/_/g, " ") + " • " + (result.entity_id || "");
        item.appendChild(entity);
        const issueLink = this.documentImpl.createElement("div");
        const issueID = (result.issue_id || "").trim();
        if (issueID) {
          const link = this.documentImpl.createElement("a");
          link.setAttribute("href", "/issues/" + encodeURIComponent(issueID));
          link.textContent = issueID;
          issueLink.appendChild(link);
        } else {
          const orphan = this.documentImpl.createElement("span");
          orphan.textContent = "No owning issue";
          issueLink.appendChild(orphan);
        }
        item.appendChild(issueLink);
        const snippet = this.documentImpl.createElement("div");
        this.renderSnippet(snippet, result.snippet || "");
        item.appendChild(snippet);
        list.appendChild(item);
      }
      container.appendChild(list);
      if (this.hasMore) {
        const button = this.documentImpl.createElement("button");
        button.setAttribute("type", "button");
        button.setAttribute("data-board-search-load-more", "");
        button.textContent = "Load more";
        button.addEventListener("click", () => this.loadMore());
        container.appendChild(button);
      }
    }

    renderSnippet(target, value){
      if (!target) {
        return;
      }
      const text = String(value || "");
      if (text.indexOf("[") === -1 || text.indexOf("]") === -1) {
        target.textContent = text;
        return;
      }
      let cursor = 0;
      let valid = true;
      let inMarker = false;
      let markerStart = -1;
      const appendText = (segment) => {
        if (!segment) {
          return;
        }
        const span = this.documentImpl.createElement("span");
        span.textContent = segment;
        target.appendChild(span);
      };
      for (let index = 0; index < text.length; index += 1) {
        const char = text[index];
        if (char === "[") {
          if (inMarker) {
            valid = false;
            break;
          }
          if (cursor < index) {
            appendText(text.slice(cursor, index));
          }
          inMarker = true;
          markerStart = index;
          cursor = index + 1;
          continue;
        }
        if (char === "]") {
          if (!inMarker) {
            valid = false;
            break;
          }
          const markerText = text.slice(markerStart + 1, index);
          if (markerText.length === 0) {
            valid = false;
            break;
          }
          const mark = this.documentImpl.createElement("mark");
          mark.textContent = markerText;
          target.appendChild(mark);
          inMarker = false;
          markerStart = -1;
          cursor = index + 1;
          continue;
        }
      }
      if (!valid || inMarker) {
        target.textContent = text;
        return;
      }
      if (cursor < text.length) {
        appendText(text.slice(cursor));
      }
    }

    renderStatus(message){
      const status = this.root ? this.root.querySelector("[data-board-search-status]") : null;
      if (!status) {
        return;
      }
      status.textContent = message || "";
      status.setAttribute("aria-live", "polite");
      status.setAttribute("role", "status");
    }

    setLoading(loading){
      const button = this.root ? this.root.querySelector("button[type=submit]") : null;
      if (button) {
        button.disabled = loading;
      }
    }

    clearLoading(){
      this.setLoading(false);
    }
  }

  const root = document.querySelector("main[data-board-main]");
  if (window.__rhizomeBoardSearchTestHooks) {
    window.__rhizomeBoardSearchTestHooks.BoardSearchClient = BoardSearchClient;
  }
  if (root) {
    const client = new BoardSearchClient(root, {fetch: fetch, document: document, window: window, history: window.history});
    window.__rhizomeBoardSearchClient = client;
  }
})();
