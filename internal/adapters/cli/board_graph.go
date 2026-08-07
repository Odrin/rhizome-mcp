package cli

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"rhizome-mcp/internal/domain"
)

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
