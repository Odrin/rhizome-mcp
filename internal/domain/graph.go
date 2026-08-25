package domain

import (
	"sort"
)

const (
	defaultGraphDepth = 2
	defaultGraphNodes = 100
)

// GraphDirection controls which stored relation directions are traversed.
type GraphDirection string

const (
	GraphDirectionOutgoing GraphDirection = "outgoing"
	GraphDirectionIncoming GraphDirection = "incoming"
	GraphDirectionBoth     GraphDirection = "both"
)

func (direction GraphDirection) Valid() bool {
	return direction == GraphDirectionOutgoing || direction == GraphDirectionIncoming || direction == GraphDirectionBoth
}

// GetIssueGraphInput requests a compact normalized issue graph.
type GetIssueGraphInput struct {
	RootIssueID      string
	Depth            *int
	Direction        GraphDirection
	RelationTypes    []RelationType
	IncludeHierarchy *bool
	IncludeTerminal  *bool
	MaxNodes         *int
	View             string
}

// GetPlanningGraphInput requests the planning projection of the shared graph.
type GetPlanningGraphInput struct {
	RootIssueID     *string
	Depth           *int
	MaxNodes        *int
	IncludeReview   *bool
	IncludeRelated  *bool
	IncludeTerminal *bool
}

// Validate validates graph-local limits and returns defaults as explicit values.
func (input GetIssueGraphInput) Validate() (GetIssueGraphInput, error) {
	root, err := graphIdentifier("root_issue_id", input.RootIssueID)
	if err != nil {
		return GetIssueGraphInput{}, err
	}
	depth, nodes, err := graphBounds(input.Depth, input.MaxNodes, defaultGraphDepth)
	if err != nil {
		return GetIssueGraphInput{}, err
	}
	direction := input.Direction
	if direction == "" {
		direction = GraphDirectionBoth
	}
	if !direction.Valid() {
		return GetIssueGraphInput{}, invalidEnum("direction", string(direction))
	}
	if input.View != "" && input.View != "compact" {
		return GetIssueGraphInput{}, validationError("view", "UNSUPPORTED", "only compact is supported")
	}
	relationTypes, err := graphRelationTypes(input.RelationTypes)
	if err != nil {
		return GetIssueGraphInput{}, err
	}
	return GetIssueGraphInput{
		RootIssueID: root.Value, Depth: &depth, Direction: direction, RelationTypes: relationTypes,
		IncludeHierarchy: graphBool(input.IncludeHierarchy, true), IncludeTerminal: graphBool(input.IncludeTerminal, true),
		MaxNodes: &nodes, View: "compact",
	}, nil
}

// Validate validates planning limits and normalizes an optional root identifier.
func (input GetPlanningGraphInput) Validate() (GetPlanningGraphInput, error) {
	depth, nodes, err := graphBounds(input.Depth, input.MaxNodes, 3)
	if err != nil {
		return GetPlanningGraphInput{}, err
	}
	var root *string
	if input.RootIssueID != nil {
		identifier, err := graphIdentifier("root_issue_id", *input.RootIssueID)
		if err != nil {
			return GetPlanningGraphInput{}, err
		}
		value := identifier.Value
		root = &value
	}
	return GetPlanningGraphInput{
		RootIssueID: root, Depth: &depth, MaxNodes: &nodes,
		IncludeReview: graphBool(input.IncludeReview, true), IncludeRelated: graphBool(input.IncludeRelated, false),
		IncludeTerminal: graphBool(input.IncludeTerminal, true),
	}, nil
}

func graphIdentifier(field, value string) (IssueIdentifier, error) {
	if err := ValidateText(field, value, -1); err != nil {
		return IssueIdentifier{}, err
	}
	identifier, err := ParseIssueIdentifier(value)
	if err != nil {
		return IssueIdentifier{}, validationError(field, "INVALID_IDENTIFIER", "must be a canonical ULID or ISSUE-N")
	}
	return identifier, nil
}

func graphBounds(depth, nodes *int, defaultDepth int) (int, int, error) {
	resolvedDepth := defaultDepth
	if depth != nil {
		resolvedDepth = *depth
	}
	resolvedNodes := defaultGraphNodes
	if nodes != nil {
		resolvedNodes = *nodes
	}
	if resolvedDepth < 0 || resolvedDepth > MaxGraphDepth {
		return 0, 0, validationError("depth", "OUT_OF_RANGE", "must be between 0 and 5")
	}
	if resolvedNodes < 1 || resolvedNodes > MaxGraphNodes {
		return 0, 0, validationError("max_nodes", "OUT_OF_RANGE", "must be between 1 and 500")
	}
	return resolvedDepth, resolvedNodes, nil
}

func graphRelationTypes(values []RelationType) ([]RelationType, error) {
	if len(values) == 0 {
		return []RelationType{RelationTypeBlocks, RelationTypeRelatedTo, RelationTypeDuplicates}, nil
	}
	result := append([]RelationType(nil), values...)
	seen := make(map[RelationType]struct{}, len(result))
	for _, value := range result {
		if !value.Valid() {
			return nil, invalidEnum("relation_types", string(value))
		}
		if _, exists := seen[value]; exists {
			return nil, validationError("relation_types", "DUPLICATE", "must not contain duplicate values")
		}
		seen[value] = struct{}{}
	}
	return result, nil
}

func graphBool(value *bool, fallback bool) *bool {
	if value == nil {
		result := fallback
		return &result
	}
	result := *value
	return &result
}

// GraphEdge is one normalized directed edge. Stored relation edges retain their
// canonical source and target; contains is a derived epic-to-child edge.
type GraphEdge struct {
	SourceIssueID string `json:"source_issue_id"`
	TargetIssueID string `json:"target_issue_id"`
	Type          string `json:"type"`
}

// GraphSnapshot is the consistent candidate projection supplied by storage.
type GraphSnapshot struct {
	RootIssueID      *string
	TopLevelIssueIDs []string
	Nodes            []IssueProjection
	Edges            []GraphEdge
}

// GraphSummary is the compact deterministic count summary shared by graph views.
type GraphSummary struct {
	NodeCount         int `json:"node_count"`
	EdgeCount         int `json:"edge_count"`
	EntryPointCount   int `json:"entry_point_count"`
	BlockingNodeCount int `json:"blocking_node_count"`
}

// GraphResult is a normalized graph projection.
type GraphResult struct {
	RootIssueID      *string           `json:"root_issue_id,omitempty"`
	Nodes            []IssueProjection `json:"nodes"`
	Edges            []GraphEdge       `json:"edges"`
	EntryPoints      []string          `json:"entry_points"`
	BlockingNodes    []string          `json:"blocking_nodes"`
	Summary          GraphSummary      `json:"summary"`
	Warnings         []string          `json:"warnings"`
	Truncated        bool              `json:"truncated"`
	TruncationReason *string           `json:"truncation_reason,omitempty"`
}

// GraphTraversal configures the one shared bounded graph engine.
type GraphTraversal struct {
	RootIssueIDs     []string
	ExplicitRootID   string
	Depth            int
	MaxNodes         int
	Direction        GraphDirection
	RelationTypes    []RelationType
	IncludeHierarchy bool
	IncludeTerminal  bool
	ExcludeReview    bool
	// PreferNonTerminal spends the MaxNodes budget on non-terminal nodes
	// first, admitting done/cancelled nodes only afterwards and only as
	// relation endpoints of retained nodes, and computes EntryPoints over the
	// whole snapshot rather than the retained set. The planning graph sets it;
	// the rooted issue graph does not, because there order of discovery from
	// the requested root is the meaningful answer.
	PreferNonTerminal bool
}

// BuildGraph performs deterministic bounded breadth-first traversal over a
// storage snapshot. It intentionally has no persistence dependency.
func BuildGraph(snapshot GraphSnapshot, traversal GraphTraversal) GraphResult {
	nodesByID := make(map[string]IssueProjection, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		nodesByID[node.ID] = node
	}
	allowedTypes := make(map[string]bool, len(traversal.RelationTypes)+1)
	for _, relationType := range traversal.RelationTypes {
		allowedTypes[string(relationType)] = true
	}
	if traversal.IncludeHierarchy {
		allowedTypes["contains"] = true
	}
	adjacency := make(map[string][]GraphEdge, len(nodesByID))
	for _, edge := range snapshot.Edges {
		if !allowedTypes[edge.Type] {
			continue
		}
		if _, sourceFound := nodesByID[edge.SourceIssueID]; !sourceFound {
			continue
		}
		if _, targetFound := nodesByID[edge.TargetIssueID]; !targetFound {
			continue
		}
		adjacency[edge.SourceIssueID] = append(adjacency[edge.SourceIssueID], edge)
		if edge.TargetIssueID != edge.SourceIssueID {
			adjacency[edge.TargetIssueID] = append(adjacency[edge.TargetIssueID], edge)
		}
	}

	result := GraphResult{RootIssueID: snapshot.RootIssueID, Nodes: []IssueProjection{}, Edges: []GraphEdge{},
		EntryPoints: []string{}, BlockingNodes: []string{}, Warnings: []string{}}
	visited := make(map[string]bool, len(nodesByID))
	type queued struct {
		id    string
		depth int
	}
	queue := make([]queued, 0, traversal.MaxNodes)
	truncated := false

	nodeExcluded := func(id string) bool {
		node, found := nodesByID[id]
		if !found {
			return true
		}
		if !traversal.IncludeTerminal && (node.Status == StatusDone || node.Status == StatusCancelled) {
			return true
		}
		return traversal.ExcludeReview && node.Status == StatusReview
	}
	// deferred preserves discovery order so the second pass stays deterministic.
	deferred := make([]string, 0)
	isTerminalNode := func(id string) bool {
		node, found := nodesByID[id]
		return found && (node.Status == StatusDone || node.Status == StatusCancelled)
	}
	isDeferrable := func(id string) bool {
		return traversal.PreferNonTerminal && id != traversal.ExplicitRootID && isTerminalNode(id)
	}
	allowedNode := func(id string, root bool) bool {
		if _, found := nodesByID[id]; !found {
			return false
		}
		return (root && id == traversal.ExplicitRootID) || !nodeExcluded(id)
	}
	deferredSeen := make(map[string]bool)
	// budgetExhausted separates "this node was deferred to the second pass"
	// from "there was no room left", because the root loop must keep going in
	// the first case and stop in the second.
	budgetExhausted := false
	addNode := func(id string, depth int, root bool) bool {
		if visited[id] {
			return true
		}
		if _, found := nodesByID[id]; !found {
			return false
		}
		if isDeferrable(id) {
			// Terminal work never competes with open work for the budget, but
			// it is still traversed *through* (enqueued without being emitted),
			// so work reachable only via a finished parent or blocker stays
			// discoverable. The node itself gets a second chance below only if
			// it is includable at all and an allowed edge attaches it to a
			// pass-1 retained node.
			if !deferredSeen[id] {
				deferredSeen[id] = true
				queue = append(queue, queued{id: id, depth: depth})
				if allowedNode(id, root) {
					deferred = append(deferred, id)
				}
			}
			return false
		}
		if !allowedNode(id, root) {
			return false
		}
		if len(result.Nodes) >= traversal.MaxNodes {
			truncated = true
			budgetExhausted = true
			return false
		}
		visited[id] = true
		result.Nodes = append(result.Nodes, nodesByID[id])
		queue = append(queue, queued{id: id, depth: depth})
		return true
	}

	for _, root := range traversal.RootIssueIDs {
		if visited[root] {
			continue
		}
		if !allowedNode(root, root == traversal.ExplicitRootID) && !isDeferrable(root) {
			continue
		}
		if !addNode(root, 0, root == traversal.ExplicitRootID) && budgetExhausted {
			break
		}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if current.depth >= traversal.Depth {
				continue
			}
			// Deferred terminal nodes expand even though they are excluded or
			// not yet admitted; other exclusions (review) stay opaque.
			if nodeExcluded(current.id) && !isDeferrable(current.id) {
				continue
			}
			candidates := graphNeighbors(current.id, adjacency[current.id], nodesByID, traversal.Direction)
			for _, candidate := range candidates {
				if !allowedNode(candidate.neighborID, false) && !isDeferrable(candidate.neighborID) {
					continue
				}
				if !visited[candidate.neighborID] {
					if !addNode(candidate.neighborID, current.depth+1, false) {
						continue
					}
				}
				// Both endpoints must be emitted nodes; edges touching a
				// deferred node are added in the second pass if it is admitted.
				if visited[current.id] && visited[candidate.neighborID] {
					result.Edges = appendGraphEdge(result.Edges, candidate.edge)
				}
			}
		}
	}

	// Second pass: whatever budget survived pass 1 goes to deferred terminal
	// nodes, and only to those an allowed edge attaches to a node retained by
	// pass 1. Attachment is judged against the pass-1 set, not the growing
	// visited set, so admission cannot depend on snapshot order.
	retainedByPassOne := make(map[string]bool, len(visited))
	for id := range visited {
		retainedByPassOne[id] = true
	}
	for _, id := range deferred {
		if len(result.Nodes) >= traversal.MaxNodes {
			truncated = true
			break
		}
		attached := false
		for _, edge := range adjacency[id] {
			other := edge.SourceIssueID
			if other == id {
				other = edge.TargetIssueID
			}
			if other != id && retainedByPassOne[other] {
				attached = true
				break
			}
		}
		if !attached {
			// ISSUE-225 Q1: a policy drop is not truncation. This node is
			// includable but the endpoints-only rule attaches it to nothing
			// pass 1 retained, which is a deterministic selection outcome no
			// MaxNodes value changes. Flagging it sent clients into an
			// unwinnable raise-and-retry loop and gave every project holding
			// one relation-free finished issue a permanent truncated=true.
			// include_terminal=false exclusions already read this way.
			continue
		}
		visited[id] = true
		result.Nodes = append(result.Nodes, nodesByID[id])
		for _, edge := range adjacency[id] {
			other := edge.SourceIssueID
			if other == id {
				other = edge.TargetIssueID
			}
			if visited[other] {
				result.Edges = appendGraphEdge(result.Edges, edge)
			}
		}
	}

	result.Edges = uniqueSortedGraphEdges(result.Edges, nodesByID)
	if traversal.PreferNonTerminal {
		// Entry points answer "what can I claim", so truncation must not shrink
		// them: they are computed over the whole snapshot, which means a
		// truncated graph can report an entry point that is not in Nodes. A
		// status the caller excluded (review) is still excluded here — the
		// snapshot-wide scan must not resurrect filtered-out work.
		for _, node := range snapshot.Nodes {
			if node.IsClaimable && node.Status != StatusDone && node.Status != StatusCancelled && !nodeExcluded(node.ID) {
				result.EntryPoints = append(result.EntryPoints, node.ID)
			}
		}
	} else {
		for _, node := range result.Nodes {
			if node.IsClaimable && node.Status != StatusDone && node.Status != StatusCancelled {
				result.EntryPoints = append(result.EntryPoints, node.ID)
			}
		}
	}
	// ISSUE-225 Q2: entry points are still computed over the whole snapshot,
	// so the count never shrinks with the budget, but the serialized list is
	// bounded by the same MaxNodes that bounds Nodes -- otherwise a project
	// with thousands of claimable issues ships thousands of ULIDs on every
	// call and board poll, and the documented response budget is false
	// exactly where it matters. The kept prefix is the existing deterministic
	// snapshot order, so it is stable across calls and ETag-friendly, and
	// EntryPointCount > len(EntryPoints) plus the reason below tells a client
	// exactly what happened. The rooted graph derives entry points from
	// Nodes, which is already within budget, so the cap is a no-op there.
	entryPointCount := len(result.EntryPoints)
	entryPointsCapped := false
	if traversal.MaxNodes > 0 && len(result.EntryPoints) > traversal.MaxNodes {
		result.EntryPoints = result.EntryPoints[:traversal.MaxNodes]
		entryPointsCapped = true
	}

	blocking := make(map[string]bool)
	for _, edge := range result.Edges {
		if edge.Type == string(RelationTypeBlocks) {
			if node, found := nodesByID[edge.SourceIssueID]; found && node.Status != StatusDone && node.Status != StatusCancelled {
				blocking[edge.SourceIssueID] = true
			}
		}
	}
	for _, node := range result.Nodes {
		if blocking[node.ID] {
			result.BlockingNodes = append(result.BlockingNodes, node.ID)
		}
	}
	result.Summary = GraphSummary{NodeCount: len(result.Nodes), EdgeCount: len(result.Edges),
		EntryPointCount: entryPointCount, BlockingNodeCount: len(result.BlockingNodes)}
	// truncated now means exactly "a budget dropped something a larger budget
	// would have kept", which is true of both the node budget and the entry
	// point cap. The reason is single-valued, so node_limit wins when both
	// happened: it is the one that also cost the caller nodes and edges.
	result.Truncated = truncated || entryPointsCapped
	switch {
	case truncated:
		reason := "node_limit"
		result.TruncationReason = &reason
	case entryPointsCapped:
		reason := "entry_point_limit"
		result.TruncationReason = &reason
	}
	return result
}

type graphNeighbor struct {
	edge       GraphEdge
	neighborID string
	sequence   int64
}

func graphNeighbors(current string, edges []GraphEdge, nodes map[string]IssueProjection, direction GraphDirection) []graphNeighbor {
	result := make([]graphNeighbor, 0, len(edges))
	for _, edge := range edges {
		var neighbor string
		if edge.Type == string(RelationTypeRelatedTo) {
			if edge.SourceIssueID == current {
				neighbor = edge.TargetIssueID
			} else if edge.TargetIssueID == current {
				neighbor = edge.SourceIssueID
			}
		} else {
			if (direction == GraphDirectionOutgoing || direction == GraphDirectionBoth) && edge.SourceIssueID == current {
				neighbor = edge.TargetIssueID
			}
			if (direction == GraphDirectionIncoming || direction == GraphDirectionBoth) && edge.TargetIssueID == current {
				neighbor = edge.SourceIssueID
			}
		}
		if neighbor == "" {
			continue
		}
		node, found := nodes[neighbor]
		if !found {
			continue
		}
		result = append(result, graphNeighbor{edge: edge, neighborID: neighbor, sequence: node.SequenceNo})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].edge.Type != result[j].edge.Type {
			return result[i].edge.Type < result[j].edge.Type
		}
		if result[i].sequence != result[j].sequence {
			return result[i].sequence < result[j].sequence
		}
		return result[i].neighborID < result[j].neighborID
	})
	return result
}

func appendGraphEdge(edges []GraphEdge, edge GraphEdge) []GraphEdge {
	for _, existing := range edges {
		if existing == edge {
			return edges
		}
	}
	return append(edges, edge)
}

func uniqueSortedGraphEdges(edges []GraphEdge, nodes map[string]IssueProjection) []GraphEdge {
	sort.Slice(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if nodes[left.SourceIssueID].SequenceNo != nodes[right.SourceIssueID].SequenceNo {
			return nodes[left.SourceIssueID].SequenceNo < nodes[right.SourceIssueID].SequenceNo
		}
		if left.SourceIssueID != right.SourceIssueID {
			return left.SourceIssueID < right.SourceIssueID
		}
		if nodes[left.TargetIssueID].SequenceNo != nodes[right.TargetIssueID].SequenceNo {
			return nodes[left.TargetIssueID].SequenceNo < nodes[right.TargetIssueID].SequenceNo
		}
		return left.TargetIssueID < right.TargetIssueID
	})
	return edges
}
