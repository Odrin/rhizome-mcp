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
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Rhizome status board</title>\n<style>")
	b.WriteString(boardHTMLStyle)
	b.WriteString("</style>\n</head>\n<body>\n")
	b.WriteString("<main data-board-main data-board-endpoint=\"/api/board\" data-board-route=\"/\">\n")
	b.WriteString("<h1>Rhizome status board</h1>\n")
	b.WriteString(fmt.Sprintf("<p class=\"generated\">Generated %s</p>\n", html.EscapeString(result.GeneratedAt.Format(time.RFC3339))))

	writeServedBoardStatusCountsHTML(&b, result.StatusCounts)
	writeServedBoardActiveAttemptsHTML(&b, result.ActiveAttempts)
	writeServedBoardBlockedIssuesHTML(&b, result.BlockedIssues)
	writeServedBoardReviewRequestsHTML(&b, result.ReviewRequests, issueDisplayIDMap(result.PlanningGraph.Nodes))
	writeServedBoardPlanningGraphHTML(&b, result.PlanningGraph)

	b.WriteString("</main>\n")
	b.WriteString("<script>")
	b.WriteString(boardLiveRefreshScript)
	b.WriteString("</script>\n")
	b.WriteString("<footer>Generated locally by <code>rhizome-mcp board --serve</code>.</footer>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
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

func issueDisplayIDMap(nodes []domain.IssueProjection) map[string]string {
	if len(nodes) == 0 {
		return nil
	}
	mapping := make(map[string]string, len(nodes))
	for _, node := range nodes {
		label := strings.TrimSpace(node.DisplayID)
		if label == "" {
			label = strings.TrimSpace(node.Issue.DisplayID)
		}
		if label != "" {
			mapping[node.ID] = label
		}
	}
	return mapping
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

// Node box and layer spacing constants for the hand-built inline SVG layout.
const (
	boardSVGNodeWidth  = 172
	boardSVGNodeHeight = 46
	boardSVGHGap       = 24
	boardSVGVGap       = 54
	boardSVGMargin     = 24
	// boardSVGMaxColumns bounds how many node boxes share one visual row
	// before wrapping onto another row, keeping wide layers legible.
	boardSVGMaxColumns = 8
)

// renderBoardGraphSVG computes a simple, deterministic layered layout (a
// bounded longest-path/Kahn topological layering over "blocks" and "contains"
// edges) and renders it as plain inline SVG: rectangles for nodes labelled
// with their display ID and title, and lines with arrowheads for edges. This
// intentionally is not a polished force-directed graph; it only needs to be a
// legible, self-contained visual with zero JavaScript.
//
// The xmlns attribute is deliberately omitted: this SVG is always embedded
// inline in an HTML5 document (which implicitly namespaces svg/foreignObject
// content), and omitting it keeps the generated file free of any "http://"
// substring so it passes a naive network-dependency scan.
func renderBoardGraphSVG(graph domain.GraphResult) string {
	return renderBoardGraphSVGWithLinks(graph, false)
}

func renderServedBoardGraphSVG(graph domain.GraphResult) string {
	return renderBoardGraphSVGWithLinks(graph, true)
}

func renderBoardGraphSVGWithLinks(graph domain.GraphResult, linkable bool) string {
	mapping := issueDisplayIDMap(graph.Nodes)
	if len(graph.Nodes) == 0 {
		return `<svg viewBox="0 0 420 90" width="420" height="90" role="img" aria-label="Empty planning graph">` +
			`<rect x="0" y="0" width="420" height="90" fill="#f8fafc"/>` +
			`<text x="16" y="50" font-family="sans-serif" font-size="14" fill="#475569">No planning graph nodes.</text></svg>`
	}

	layer := boardGraphLayers(graph)
	maxLayer := 0
	nodesByLayer := make(map[int][]domain.IssueProjection, len(graph.Nodes))
	for _, node := range graph.Nodes {
		l := layer[node.ID]
		nodesByLayer[l] = append(nodesByLayer[l], node)
		if l > maxLayer {
			maxLayer = l
		}
	}

	// Wrap any layer wider than boardSVGMaxColumns onto additional visual
	// rows. Backlogs commonly have many unrelated done/cancelled issues that
	// all land on layer 0 with no edges between them; without wrapping, that
	// single row would stretch arbitrarily wide and become illegible.
	rows := make([][]domain.IssueProjection, 0, maxLayer+1)
	for l := 0; l <= maxLayer; l++ {
		remaining := nodesByLayer[l]
		for len(remaining) > 0 {
			chunkSize := len(remaining)
			if chunkSize > boardSVGMaxColumns {
				chunkSize = boardSVGMaxColumns
			}
			rows = append(rows, remaining[:chunkSize])
			remaining = remaining[chunkSize:]
		}
	}

	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	width := boardSVGMargin*2 + maxCols*(boardSVGNodeWidth+boardSVGHGap) - boardSVGHGap
	if width < 320 {
		width = 320
	}
	height := boardSVGMargin*2 + len(rows)*(boardSVGNodeHeight+boardSVGVGap) - boardSVGVGap

	type point struct{ x, y int }
	centers := make(map[string]point, len(graph.Nodes))

	var nodesSVG strings.Builder
	for rowIndex, row := range rows {
		y := boardSVGMargin + rowIndex*(boardSVGNodeHeight+boardSVGVGap)
		rowWidth := len(row)*(boardSVGNodeWidth+boardSVGHGap) - boardSVGHGap
		startX := boardSVGMargin + (width-boardSVGMargin*2-rowWidth)/2
		if startX < boardSVGMargin {
			startX = boardSVGMargin
		}
		for index, node := range row {
			x := startX + index*(boardSVGNodeWidth+boardSVGHGap)
			centers[node.ID] = point{x: x + boardSVGNodeWidth/2, y: y + boardSVGNodeHeight/2}
			nodesSVG.WriteString(boardGraphNodeSVGWithLink(node, x, y, linkable, mapping))
		}
	}

	var edgesSVG strings.Builder
	for _, edge := range graph.Edges {
		source, sourceOK := centers[edge.SourceIssueID]
		target, targetOK := centers[edge.TargetIssueID]
		if !sourceOK || !targetOK || edge.SourceIssueID == edge.TargetIssueID {
			continue
		}
		edgesSVG.WriteString(fmt.Sprintf(
			`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="1.5" marker-end="url(#board-arrow)"/>`,
			source.x, source.y, target.x, target.y, boardGraphEdgeColor(edge.Type)))
	}

	return fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Planning graph">`+
			`<defs><marker id="board-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">`+
			`<path d="M0,0 L10,5 L0,10 z" fill="#64748b"/></marker></defs>`+
			`<rect x="0" y="0" width="%d" height="%d" fill="#f8fafc"/>`+
			`%s%s</svg>`,
		width, height, width, height, width, height, edgesSVG.String(), nodesSVG.String())
}

func boardGraphNodeSVG(node domain.IssueProjection, x, y int) string {
	return boardGraphNodeSVGWithLink(node, x, y, false, nil)
}

func boardGraphNodeSVGWithLink(node domain.IssueProjection, x, y int, linkable bool, mapping map[string]string) string {
	fill := boardGraphStatusColor(node.EffectiveStatus)
	label := boardGraphNodeLabel(node)
	displayID := issueDisplayIDForProjection(node, mapping)
	title := truncateBoardGraphLabel(node.Title, 22)
	content := fmt.Sprintf(
		`<g><rect x="%d" y="%d" width="%d" height="%d" rx="6" fill="%s" stroke="#33415580" stroke-width="1"/>`+
			`<text x="%d" y="%d" font-family="sans-serif" font-size="12" font-weight="600" fill="#0f172a" text-anchor="middle">%s</text>`+
			`<text x="%d" y="%d" font-family="sans-serif" font-size="10" fill="#1f2937" text-anchor="middle">%s</text></g>`,
		x, y, boardSVGNodeWidth, boardSVGNodeHeight, fill,
		x+boardSVGNodeWidth/2, y+19, html.EscapeString(label),
		x+boardSVGNodeWidth/2, y+34, html.EscapeString(title))
	if !linkable {
		return content
	}
	return fmt.Sprintf(`<a href="%s" aria-label="%s">%s</a>`, boardIssuePath(node.ID, displayID), html.EscapeString(displayID), content)
}

func boardGraphNodeLabel(node domain.IssueProjection) string {
	if node.DisplayID != "" {
		return node.DisplayID
	}
	return node.ID
}

func boardIssueLink(identifier, display string) string {
	return boardIssueLinkForProjection(domain.IssueProjection{Issue: domain.Issue{ID: identifier, DisplayID: display}}, nil)
}

func boardIssueLinkForProjection(node domain.IssueProjection, mapping map[string]string) string {
	displayID := issueDisplayIDForProjection(node, mapping)
	label := displayID
	if label == "" {
		label = node.ID
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, boardIssuePath(node.ID, displayID), html.EscapeString(label))
}

func issueDisplayIDForProjection(node domain.IssueProjection, mapping map[string]string) string {
	if strings.TrimSpace(node.DisplayID) != "" {
		return strings.TrimSpace(node.DisplayID)
	}
	if strings.TrimSpace(node.Issue.DisplayID) != "" {
		return strings.TrimSpace(node.Issue.DisplayID)
	}
	if mapping != nil {
		if value, ok := mapping[node.ID]; ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func issueDisplayIDName(identifier string, mapping map[string]string) string {
	if mapping != nil {
		if value, ok := mapping[identifier]; ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boardIssuePath(identifier, display string) string {
	target := identifier
	if strings.TrimSpace(display) != "" {
		target = strings.TrimSpace(display)
	}
	return "/issues/" + url.PathEscape(target)
}

func boardGraphStatusColor(status domain.EffectiveStatus) string {
	switch status {
	case domain.EffectiveStatusDone:
		return "#bbf7d0"
	case domain.EffectiveStatusCancelled:
		return "#e5e7eb"
	case domain.EffectiveStatusBlocked:
		return "#fecaca"
	case domain.EffectiveStatusInProgress:
		return "#bfdbfe"
	case domain.EffectiveStatusReview:
		return "#fde68a"
	case domain.EffectiveStatusReady:
		return "#ddd6fe"
	default:
		return "#e2e8f0"
	}
}

func boardGraphEdgeColor(edgeType string) string {
	switch edgeType {
	case "blocks":
		return "#dc2626"
	case "contains":
		return "#64748b"
	default:
		return "#94a3b8"
	}
}

func truncateBoardGraphLabel(text string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

// boardGraphLayers assigns each node a deterministic layer number using a
// bounded Kahn's-algorithm longest-path layering over "blocks" and "contains"
// edges (both are directed: a blocker or parent should appear at or before its
// dependent). Symmetric "related_to" edges do not participate in layering.
// Any nodes left over after a cycle (which should not occur for well-formed
// data) are placed together on one trailing row so every node is still drawn.
func boardGraphLayers(graph domain.GraphResult) map[string]int {
	indegree := make(map[string]int, len(graph.Nodes))
	adjacency := make(map[string][]string, len(graph.Nodes))
	known := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		known[node.ID] = true
		indegree[node.ID] = 0
	}
	for _, edge := range graph.Edges {
		if edge.Type == string(domain.RelationTypeRelatedTo) {
			continue
		}
		if !known[edge.SourceIssueID] || !known[edge.TargetIssueID] || edge.SourceIssueID == edge.TargetIssueID {
			continue
		}
		adjacency[edge.SourceIssueID] = append(adjacency[edge.SourceIssueID], edge.TargetIssueID)
		indegree[edge.TargetIssueID]++
	}

	layer := make(map[string]int, len(graph.Nodes))
	visited := make(map[string]bool, len(graph.Nodes))
	queue := make([]string, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if indegree[node.ID] == 0 {
			layer[node.ID] = 0
			visited[node.ID] = true
			queue = append(queue, node.ID)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, neighbor := range adjacency[current] {
			if layer[current]+1 > layer[neighbor] {
				layer[neighbor] = layer[current] + 1
			}
			indegree[neighbor]--
			if indegree[neighbor] <= 0 && !visited[neighbor] {
				visited[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}

	maxLayer := 0
	for _, value := range layer {
		if value > maxLayer {
			maxLayer = value
		}
	}
	leftover := false
	for _, node := range graph.Nodes {
		if !visited[node.ID] {
			leftover = true
			break
		}
	}
	if leftover {
		maxLayer++
		for _, node := range graph.Nodes {
			if !visited[node.ID] {
				layer[node.ID] = maxLayer
			}
		}
	}
	return layer
}
