package cli

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

// boardHTMLStyle is the inline stylesheet embedded in every generated board
// HTML file. It is intentionally self-contained: no external stylesheets,
// fonts, or CDN references.
const boardHTMLStyle = `
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
  margin: 2rem auto; max-width: 980px; padding: 0 1rem; line-height: 1.5;
  color: #0f172a; background: #ffffff;
}
h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
h2 { font-size: 1.125rem; margin-top: 2rem; border-bottom: 1px solid #e2e8f0; padding-bottom: 0.25rem; }
.generated { color: #64748b; font-size: 0.875rem; margin-top: 0; }
table { border-collapse: collapse; width: 100%; margin-top: 0.75rem; font-size: 0.9rem; }
th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid #e2e8f0; vertical-align: top; }
th { color: #475569; font-weight: 600; }
.empty { color: #64748b; font-style: italic; }
.table-scroll { overflow-x: auto; }
.graph { overflow-x: auto; border: 1px solid #e2e8f0; border-radius: 8px; padding: 0.5rem; margin-top: 0.75rem; }
.graph svg { display: block; }
pre { background: #0f172a; color: #e2e8f0; padding: 1rem; border-radius: 8px; overflow-x: auto; font-size: 0.8rem; white-space: pre-wrap; word-break: break-word; }
details summary { cursor: pointer; color: #2563eb; margin-top: 0.5rem; }
footer { color: #94a3b8; font-size: 0.8rem; margin-top: 3rem; }

@media (prefers-color-scheme: dark) {
  body { background: #0b1220; color: #e2e8f0; }
  h2 { border-bottom-color: #1e293b; }
  th, td { border-bottom-color: #1e293b; }
  th { color: #94a3b8; }
  .graph { border-color: #1e293b; }
}
`

const boardLiveRefreshScript = `
(function(){
  class BoardLiveClient {
    constructor(root, options){
      options = options || {};
      this.root = root;
      this.fetchImpl = options.fetch || fetch;
      this.setTimeoutImpl = options.setTimeout || setTimeout;
      this.clearTimeoutImpl = options.clearTimeout || clearTimeout;
      this.randomImpl = options.random || Math.random;
      this.nowImpl = options.now || (() => new Date());
      this.documentImpl = options.document || document;
      this.windowImpl = options.window || window;
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
`

const boardSearchScript = `
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
`

// renderBoardHTML renders a fully self-contained HTML status board: no
// <script src=...>, no <link rel="stylesheet" href=...>, no CDN or network
// references of any kind. The dependency/planning graph is rendered as
// hand-built inline SVG (see renderBoardGraphSVG), and the same graph is also
// included as portable Mermaid source text for copying into any renderer.
func renderBoardHTML(result domain.BoardResult) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Rhizome status board</title>\n<style>")
	b.WriteString(boardHTMLStyle)
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString("<h1>Rhizome status board</h1>\n")
	b.WriteString(fmt.Sprintf("<p class=\"generated\">Generated %s</p>\n", html.EscapeString(result.GeneratedAt.Format(time.RFC3339))))

	writeBoardStatusCountsHTML(&b, result.StatusCounts)
	writeBoardActiveAttemptsHTML(&b, result.ActiveAttempts)
	writeBoardBlockedIssuesHTML(&b, result.BlockedIssues)
	writeBoardReviewRequestsHTML(&b, result.ReviewRequests)
	writeBoardPlanningGraphHTML(&b, result.PlanningGraph)

	b.WriteString("<footer>Generated locally by <code>rhizome-mcp board --output</code>. No network access is required to view this file.</footer>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func renderServedBoardHTML(result domain.BoardResult) string {
	return renderServedBoardHTMLWithSearchState(result, servedBoardSearchState{})
}

func renderServedBoardHTMLWithSearchState(result domain.BoardResult, state servedBoardSearchState) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Rhizome status board</title>\n<style>")
	b.WriteString(boardHTMLStyle)
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString("<main data-board-main data-board-endpoint=\"/api/board\" data-board-route=\"/\">\n")
	b.WriteString("<h1>Rhizome status board</h1>\n")
	b.WriteString(fmt.Sprintf("<p class=\"generated\">Generated %s</p>\n", html.EscapeString(result.GeneratedAt.Format(time.RFC3339))))
	b.WriteString("<section>\n")
	b.WriteString("<h2>Search</h2>\n")
	b.WriteString("<form data-board-search-form>\n")
	b.WriteString("<label for=\"board-search-query\">Search</label>\n")
	b.WriteString("<input id=\"board-search-query\" name=\"search\" type=\"search\" value=\"")
	b.WriteString(html.EscapeString(state.Query))
	b.WriteString("\" />\n")
	b.WriteString("<select name=\"entity_type\">\n<option value=\"all\">All</option>\n<option value=\"issue\"")
	if state.EntityType == "issue" {
		b.WriteString(" selected")
	}
	b.WriteString(">Issue</option>\n<option value=\"comment\"")
	if state.EntityType == "comment" {
		b.WriteString(" selected")
	}
	b.WriteString(">Comment</option>\n<option value=\"decision\"")
	if state.EntityType == "decision" {
		b.WriteString(" selected")
	}
	b.WriteString(">Decision</option>\n<option value=\"review\"")
	if state.EntityType == "review" {
		b.WriteString(" selected")
	}
	b.WriteString(">Review</option>\n<option value=\"attempt_note\"")
	if state.EntityType == "attempt_note" {
		b.WriteString(" selected")
	}
	b.WriteString(">Attempt note</option>\n</select>\n")
	b.WriteString("<button type=\"submit\">Search</button>\n")
	b.WriteString("</form>\n")
	b.WriteString("<div data-board-search-status role=\"status\" aria-live=\"polite\">")
	b.WriteString(escapeHTMLText(state.StatusMessage))
	b.WriteString("</div>\n")
	b.WriteString("<div data-board-search-results>\n")
	if len(state.Results) > 0 {
		b.WriteString("<ul>\n")
		for _, result := range state.Results {
			b.WriteString("<li><strong>")
			b.WriteString(escapeHTMLText(result.Title))
			b.WriteString("</strong><div>")
			b.WriteString(escapeHTMLText(string(result.EntityType)))
			b.WriteString(" • ")
			b.WriteString(escapeHTMLText(result.EntityID))
			b.WriteString("</div><div>")
			if issueID := strings.TrimSpace(ptrString(result.IssueID)); issueID != "" {
				b.WriteString("<a href=\"/issues/")
				b.WriteString(url.PathEscape(issueID))
				b.WriteString("\">")
				b.WriteString(escapeHTMLText(issueID))
				b.WriteString("</a>")
			} else {
				b.WriteString("<span class=\"empty\">No owning issue</span>")
			}
			b.WriteString("</div><div>")
			b.WriteString(escapeHTMLText(result.Snippet))
			b.WriteString("</div></li>\n")
		}
		b.WriteString("</ul>\n")
		if state.HasMore {
			b.WriteString("<button type=\"button\">Load more</button>\n")
		}
	} else if state.Query != "" {
		if state.Invalid {
			b.WriteString("<p class=\"empty\">Invalid search query.</p>\n")
		} else if state.Error {
			b.WriteString("<p class=\"empty\">Search temporarily unavailable.</p>\n")
		} else {
			b.WriteString("<p class=\"empty\">No results found.</p>\n")
		}
	} else {
		b.WriteString("<p class=\"empty\">Enter a search query to find issues, comments, decisions, reviews, and attempt notes.</p>\n")
	}
	b.WriteString("</div>\n")
	b.WriteString("</section>\n")

	writeServedBoardStatusCountsHTML(&b, result.StatusCounts)
	writeServedBoardActiveAttemptsHTML(&b, result.ActiveAttempts)
	writeServedBoardBlockedIssuesHTML(&b, result.BlockedIssues)
	writeServedBoardReviewRequestsHTML(&b, result.ReviewRequests, issueDisplayIDMap(result.PlanningGraph.Nodes))
	writeServedBoardPlanningGraphHTML(&b, result.PlanningGraph)

	b.WriteString("</main>\n")
	b.WriteString("<script>")
	b.WriteString(boardLiveRefreshScript)
	b.WriteString("</script>\n")
	b.WriteString("<script>")
	b.WriteString(boardSearchScript)
	b.WriteString("</script>\n")
	b.WriteString("<footer>Generated locally by <code>rhizome-mcp board --serve</code>.</footer>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

type servedBoardSearchState struct {
	Query         string
	EntityType    string
	StatusMessage string
	Results       []domain.SearchResult
	HasMore       bool
	Invalid       bool
	Error         bool
}

func escapeHTMLText(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(value)
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func renderIssueDetailHTML(detail domain.IssueDetail) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Rhizome issue detail</title>\n<style>")
	b.WriteString(boardHTMLStyle)
	b.WriteString("</style>\n</head>\n<body>\n")
	identifier := detail.Issue.DisplayID
	if identifier == "" {
		identifier = detail.Issue.ID
	}
	b.WriteString("<main data-board-main data-board-endpoint=\"/api/issues/")
	b.WriteString(html.EscapeString(identifier))
	b.WriteString("\" data-board-route=\"/issues/")
	b.WriteString(html.EscapeString(identifier))
	b.WriteString("\">\n")
	b.WriteString("<p><a href=\"/\">Return to board</a></p>\n")

	label := detail.Issue.DisplayID
	if label == "" {
		label = detail.Issue.ID
	}
	b.WriteString("<h1>")
	b.WriteString(html.EscapeString(label))
	if strings.TrimSpace(detail.Issue.Title) != "" {
		b.WriteString(" — ")
		b.WriteString(html.EscapeString(detail.Issue.Title))
	}
	b.WriteString("</h1>\n")
	b.WriteString(fmt.Sprintf("<p class=\"generated\">Stored status: %s · Effective status: %s · Type: %s · Priority: %s</p>\n",
		html.EscapeString(string(detail.Issue.Status)), html.EscapeString(string(EffectiveStatusForIssue(detail))), html.EscapeString(string(detail.Issue.Type)), html.EscapeString(string(detail.Issue.Priority))))

	b.WriteString("<section>\n<h2>Metadata</h2>\n")
	b.WriteString("<p><strong>Version:</strong> ")
	b.WriteString(html.EscapeString(fmt.Sprintf("%d", detail.Issue.Version)))
	b.WriteString("</p>\n")
	b.WriteString("<p><strong>Created:</strong> ")
	b.WriteString(html.EscapeString(formatIssueDetailTimestamp(detail.Issue.CreatedAt)))
	b.WriteString("</p>\n")
	b.WriteString("<p><strong>Updated:</strong> ")
	b.WriteString(html.EscapeString(formatIssueDetailTimestamp(detail.Issue.UpdatedAt)))
	b.WriteString("</p>\n")
	if detail.Issue.ArchivedAt != nil {
		b.WriteString("<p><strong>Archived:</strong> ")
		b.WriteString(html.EscapeString(formatIssueDetailTimestamp(*detail.Issue.ArchivedAt)))
		b.WriteString("</p>\n")
	} else {
		b.WriteString("<p class=\"empty\">Not archived.</p>\n")
	}
	b.WriteString("</section>\n")

	b.WriteString("<section>\n<h2>Labels</h2>\n")
	if len(detail.Issue.Labels) == 0 {
		b.WriteString("<p class=\"empty\">No labels assigned.</p>\n")
	} else {
		parts := make([]string, 0, len(detail.Issue.Labels))
		for _, label := range detail.Issue.Labels {
			parts = append(parts, label.Name)
		}
		b.WriteString("<p>")
		b.WriteString(html.EscapeString(strings.Join(parts, ", ")))
		b.WriteString("</p>\n")
	}
	b.WriteString("</section>\n")

	writeIssueDetailTextSection(&b, "Description", detail.Issue.Description)
	writeIssueDetailTextSection(&b, "Acceptance criteria", detail.Issue.AcceptanceCriteria)
	writeIssueDetailTextSection(&b, "Blocked reason", detail.Issue.BlockedReason)

	if detail.RootIssueProjection != nil {
		b.WriteString("<section>\n<h2>Root issue</h2>\n<p>")
		b.WriteString(boardIssueLinkForProjection(*detail.RootIssueProjection, issueDisplayIDMap(detail.Graph.Nodes)))
		b.WriteString("</p>\n</section>\n")
	}
	if detail.LatestAttempt != nil {
		b.WriteString("<section>\n<h2>Latest attempt</h2>\n<p>")
		b.WriteString(html.EscapeString(detail.LatestAttempt.ID))
		if !detail.LatestAttempt.StartedAt.IsZero() {
			b.WriteString(" — ")
			b.WriteString(html.EscapeString(formatIssueDetailTimestamp(detail.LatestAttempt.StartedAt)))
		}
		b.WriteString("</p>\n</section>\n")
	}
	if detail.OpenReview != nil {
		b.WriteString("<section>\n<h2>Open review</h2>\n<p>")
		b.WriteString(html.EscapeString(detail.OpenReview.ID))
		if detail.OpenReview.Status != "" {
			b.WriteString(" — ")
			b.WriteString(html.EscapeString(string(detail.OpenReview.Status)))
		}
		b.WriteString("</p>\n</section>\n")
	}
	if detail.LatestDecision != nil {
		b.WriteString("<section>\n<h2>Latest decision</h2>\n<p>")
		b.WriteString(html.EscapeString(detail.LatestDecision.Title))
		if detail.LatestDecision.Summary != "" {
			b.WriteString(" — ")
			b.WriteString(html.EscapeString(detail.LatestDecision.Summary))
		}
		b.WriteString("</p>\n</section>\n")
	}
	if len(detail.Graph.Nodes) > 0 || len(detail.Graph.Edges) > 0 {
		b.WriteString("<section>\n<h2>Related graph</h2>\n")
		b.WriteString(renderServedBoardGraphSVG(detail.Graph))
		b.WriteString("</section>\n")
	}
	if len(detail.Activity.Items) > 0 {
		b.WriteString("<section>\n<h2>Recent activity</h2>\n")
		if detail.Activity.HasMore {
			b.WriteString("<p class=\"empty\">Additional activity is available.</p>\n")
		}
		b.WriteString("<ul>\n")
		for _, item := range detail.Activity.Items {
			b.WriteString("<li>")
			b.WriteString(html.EscapeString(issueActivitySummary(item)))
			if !item.OccurredAt.IsZero() {
				b.WriteString(" <span class=\"generated\">")
				b.WriteString(html.EscapeString(item.OccurredAt.Format(time.RFC3339)))
				b.WriteString("</span>")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n</section>\n")
	} else {
		b.WriteString("<section>\n<h2>Recent activity</h2>\n<p class=\"empty\">No activity recorded yet.</p>\n</section>\n")
	}

	b.WriteString("</main>\n")
	b.WriteString("<script>")
	b.WriteString(boardLiveRefreshScript)
	b.WriteString("</script>\n")
	b.WriteString("<footer>Generated locally by <code>rhizome-mcp board --serve</code>.</footer>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func writeIssueDetailTextSection(b *strings.Builder, heading string, value *string) {
	b.WriteString("<section>\n<h2>")
	b.WriteString(html.EscapeString(heading))
	b.WriteString("</h2>\n")
	if value == nil || strings.TrimSpace(*value) == "" {
		b.WriteString("<p class=\"empty\">No ")
		b.WriteString(html.EscapeString(strings.ToLower(heading)))
		b.WriteString(" provided.</p>\n")
	} else {
		b.WriteString("<pre>")
		b.WriteString(html.EscapeString(*value))
		b.WriteString("</pre>\n")
	}
	b.WriteString("</section>\n")
}

func EffectiveStatusForIssue(detail domain.IssueDetail) domain.EffectiveStatus {
	status, err := domain.EffectiveStatusFor(detail.Issue.Status, detail.LatestAttempt != nil)
	if err != nil {
		return domain.EffectiveStatus(detail.Issue.Status)
	}
	return status
}

func effectiveStatusForIssue(detail domain.IssueDetail) domain.EffectiveStatus {
	status, err := domain.EffectiveStatusFor(detail.Issue.Status, detail.LatestAttempt != nil)
	if err != nil {
		return domain.EffectiveStatus(detail.Issue.Status)
	}
	return status
}

func formatIssueDetailTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func issueActivitySummary(item domain.ActivityItem) string {
	switch item.EntityType {
	case domain.ActivityEntityTypeComment:
		if item.Comment != nil && strings.TrimSpace(item.Comment.Content) != "" {
			return strings.TrimSpace(item.Comment.Content)
		}
	case domain.ActivityEntityTypeDecision:
		if item.Decision != nil {
			if strings.TrimSpace(item.Decision.Title) != "" {
				return item.Decision.Title
			}
			if strings.TrimSpace(item.Decision.Summary) != "" {
				return item.Decision.Summary
			}
		}
	case domain.ActivityEntityTypeAttempt:
		if item.Attempt != nil {
			if item.Attempt.ResultSummary != nil && strings.TrimSpace(*item.Attempt.ResultSummary) != "" {
				return *item.Attempt.ResultSummary
			}
			if item.Attempt.FailureReasonCode != nil {
				return string(*item.Attempt.FailureReasonCode)
			}
		}
	case domain.ActivityEntityTypeReview:
		if item.Review != nil {
			if strings.TrimSpace(string(item.Review.Status)) != "" {
				return string(item.Review.Status)
			}
		}
	case domain.ActivityEntityTypeEvent:
		if item.Event != nil && item.Event.EventType != "" {
			return item.Event.EventType
		}
	case domain.ActivityEntityTypeArtifact:
		if item.Artifact != nil && item.Artifact.Title != nil && strings.TrimSpace(*item.Artifact.Title) != "" {
			return *item.Artifact.Title
		}
	case domain.ActivityEntityTypeAttemptNote:
		if item.AttemptNote != nil && strings.TrimSpace(item.AttemptNote.Content) != "" {
			return item.AttemptNote.Content
		}
	}
	return string(item.EntityType)
}

func writeBoardStatusCountsHTML(b *strings.Builder, counts []domain.EffectiveStatusCount) {
	b.WriteString("<section>\n<h2>Status counts</h2>\n")
	if len(counts) == 0 {
		b.WriteString("<p class=\"empty\">No issues yet.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Effective status</th><th>Count</th></tr></thead>\n<tbody>\n")
	for _, count := range counts {
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%d</td></tr>\n", html.EscapeString(string(count.EffectiveStatus)), count.Count))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeBoardActiveAttemptsHTML(b *strings.Builder, attempts []domain.ActiveAttemptSummary) {
	b.WriteString("<section>\n<h2>Active (leased) attempts</h2>\n")
	if len(attempts) == 0 {
		b.WriteString("<p class=\"empty\">No active attempts.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Attempt</th><th>Issue</th><th>Title</th><th>Kind</th><th>Session label</th><th>Started</th><th>Lease expires</th></tr></thead>\n<tbody>\n")
	for _, attempt := range attempts {
		label := "—"
		if attempt.SessionLabel != nil && strings.TrimSpace(*attempt.SessionLabel) != "" {
			label = *attempt.SessionLabel
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(attempt.AttemptID), html.EscapeString(attempt.IssueDisplayID), html.EscapeString(attempt.IssueTitle), html.EscapeString(string(attempt.Kind)),
			html.EscapeString(label), html.EscapeString(attempt.StartedAt.Format(time.RFC3339)), html.EscapeString(attempt.LeaseExpiresAt.Format(time.RFC3339))))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeServedBoardStatusCountsHTML(b *strings.Builder, counts []domain.EffectiveStatusCount) {
	b.WriteString("<section>\n<h2>Status counts</h2>\n")
	if len(counts) == 0 {
		b.WriteString("<p class=\"empty\">No issues yet.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Effective status</th><th>Count</th></tr></thead>\n<tbody>\n")
	for _, count := range counts {
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%d</td></tr>\n", html.EscapeString(string(count.EffectiveStatus)), count.Count))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeServedBoardActiveAttemptsHTML(b *strings.Builder, attempts []domain.ActiveAttemptSummary) {
	b.WriteString("<section>\n<h2>Active (leased) attempts</h2>\n")
	if len(attempts) == 0 {
		b.WriteString("<p class=\"empty\">No active attempts.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Attempt</th><th>Issue</th><th>Title</th><th>Kind</th><th>Session label</th><th>Started</th><th>Lease expires</th></tr></thead>\n<tbody>\n")
	for _, attempt := range attempts {
		label := "—"
		if attempt.SessionLabel != nil && strings.TrimSpace(*attempt.SessionLabel) != "" {
			label = *attempt.SessionLabel
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(attempt.AttemptID), boardIssueLink(attempt.IssueID, attempt.IssueDisplayID), html.EscapeString(attempt.IssueTitle), html.EscapeString(string(attempt.Kind)),
			html.EscapeString(label), html.EscapeString(attempt.StartedAt.Format(time.RFC3339)), html.EscapeString(attempt.LeaseExpiresAt.Format(time.RFC3339))))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeBoardBlockedIssuesHTML(b *strings.Builder, issues []domain.IssueProjection) {
	b.WriteString("<section>\n<h2>Blocked issues</h2>\n")
	if len(issues) == 0 {
		b.WriteString("<p class=\"empty\">No blocked issues.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Issue</th><th>Title</th><th>Blocked reason</th></tr></thead>\n<tbody>\n")
	for _, issue := range issues {
		reason := ""
		if issue.BlockedReason != nil {
			reason = *issue.BlockedReason
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(issue.DisplayID), html.EscapeString(issue.Title), html.EscapeString(reason)))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeBoardReviewRequestsHTML(b *strings.Builder, requests []domain.ReviewRequest) {
	b.WriteString("<section>\n<h2>Open review requests</h2>\n")
	if len(requests) == 0 {
		b.WriteString("<p class=\"empty\">No open review requests.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Request</th><th>Issue</th><th>Status</th><th>Target version</th><th>Created</th></tr></thead>\n<tbody>\n")
	for _, request := range requests {
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>\n",
			html.EscapeString(request.ID), html.EscapeString(request.IssueID), html.EscapeString(string(request.Status)),
			request.TargetIssueVersion, html.EscapeString(request.CreatedAt.Format(time.RFC3339))))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeServedBoardBlockedIssuesHTML(b *strings.Builder, issues []domain.IssueProjection) {
	b.WriteString("<section>\n<h2>Blocked issues</h2>\n")
	if len(issues) == 0 {
		b.WriteString("<p class=\"empty\">No blocked issues.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Issue</th><th>Title</th><th>Blocked reason</th></tr></thead>\n<tbody>\n")
	for _, issue := range issues {
		reason := ""
		if issue.BlockedReason != nil {
			reason = *issue.BlockedReason
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			boardIssueLink(issue.ID, issue.DisplayID), html.EscapeString(issue.Title), html.EscapeString(reason)))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeServedBoardReviewRequestsHTML(b *strings.Builder, requests []domain.ReviewRequest, mapping map[string]string) {
	b.WriteString("<section>\n<h2>Open review requests</h2>\n")
	if len(requests) == 0 {
		b.WriteString("<p class=\"empty\">No open review requests.</p>\n</section>\n")
		return
	}
	b.WriteString("<div class=\"table-scroll\"><table>\n<thead><tr><th>Request</th><th>Issue</th><th>Status</th><th>Target version</th><th>Created</th></tr></thead>\n<tbody>\n")
	for _, request := range requests {
		issueCell := html.EscapeString(request.IssueID)
		if displayID := issueDisplayIDName(request.IssueID, mapping); displayID != "" {
			issueCell = fmt.Sprintf(`<a href="%s">%s</a>`, boardIssuePath(request.IssueID, displayID), html.EscapeString(displayID))
		}
		b.WriteString(fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>\n",
			html.EscapeString(request.ID), issueCell, html.EscapeString(string(request.Status)),
			request.TargetIssueVersion, html.EscapeString(request.CreatedAt.Format(time.RFC3339))))
	}
	b.WriteString("</tbody>\n</table></div>\n</section>\n")
}

func writeServedBoardPlanningGraphHTML(b *strings.Builder, graph domain.GraphResult) {
	b.WriteString("<section>\n<h2>Planning graph</h2>\n")
	truncatedNote := ""
	if graph.Truncated {
		truncatedNote = " (truncated)"
	}
	b.WriteString(fmt.Sprintf("<p>%d nodes, %d edges, %d entry points, %d blocking nodes%s.</p>\n",
		graph.Summary.NodeCount, graph.Summary.EdgeCount, graph.Summary.EntryPointCount, graph.Summary.BlockingNodeCount, truncatedNote))
	b.WriteString("<div class=\"graph\">\n")
	b.WriteString(renderServedBoardGraphSVG(graph))
	b.WriteString("\n</div>\n")
	b.WriteString("<details>\n<summary>Mermaid source (copy into any Mermaid renderer)</summary>\n<pre>")
	b.WriteString(html.EscapeString(renderMermaid(graph)))
	b.WriteString("</pre>\n</details>\n")
	b.WriteString("</section>\n")
}

func writeBoardPlanningGraphHTML(b *strings.Builder, graph domain.GraphResult) {
	b.WriteString("<section>\n<h2>Planning graph</h2>\n")
	truncatedNote := ""
	if graph.Truncated {
		truncatedNote = " (truncated)"
	}
	b.WriteString(fmt.Sprintf("<p>%d nodes, %d edges, %d entry points, %d blocking nodes%s.</p>\n",
		graph.Summary.NodeCount, graph.Summary.EdgeCount, graph.Summary.EntryPointCount, graph.Summary.BlockingNodeCount, truncatedNote))
	b.WriteString("<div class=\"graph\">\n")
	b.WriteString(renderBoardGraphSVG(graph))
	b.WriteString("\n</div>\n")
	b.WriteString("<details>\n<summary>Mermaid source (copy into any Mermaid renderer)</summary>\n<pre>")
	b.WriteString(html.EscapeString(renderMermaid(graph)))
	b.WriteString("</pre>\n</details>\n")
	b.WriteString("</section>\n")
}
