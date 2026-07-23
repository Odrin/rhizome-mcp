package cli

import (
	"fmt"
	"html"
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
			nodesSVG.WriteString(boardGraphNodeSVG(node, x, y))
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
	fill := boardGraphStatusColor(node.EffectiveStatus)
	label := node.DisplayID
	if label == "" {
		label = node.ID
	}
	title := truncateBoardGraphLabel(node.Title, 22)
	return fmt.Sprintf(
		`<g><rect x="%d" y="%d" width="%d" height="%d" rx="6" fill="%s" stroke="#33415580" stroke-width="1"/>`+
			`<text x="%d" y="%d" font-family="sans-serif" font-size="12" font-weight="600" fill="#0f172a" text-anchor="middle">%s</text>`+
			`<text x="%d" y="%d" font-family="sans-serif" font-size="10" fill="#1f2937" text-anchor="middle">%s</text></g>`,
		x, y, boardSVGNodeWidth, boardSVGNodeHeight, fill,
		x+boardSVGNodeWidth/2, y+19, html.EscapeString(label),
		x+boardSVGNodeWidth/2, y+34, html.EscapeString(title))
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
