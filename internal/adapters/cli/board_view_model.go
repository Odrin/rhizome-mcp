package cli

import (
	"html/template"
	"strconv"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

type boardStaticPageViewModel struct {
	Title                       string
	GeneratedAt                 string
	Style                       template.CSS
	StatusCounts                []boardStatusCountViewModel
	ActiveAttempts              []boardActiveAttemptViewModel
	ActiveReservationCount      int
	BlockedIssues               []boardIssueRowViewModel
	ReviewRequests              []boardReviewRequestViewModel
	PlanningGraphSVG            template.HTML
	PlanningGraphSummary        string
	PlanningGraphMermaid        string
	ActiveAttemptsTruncated     bool
	ActiveReservationsTruncated bool
	BlockedIssuesTruncated      bool
	ReviewRequestsTruncated     bool
}

type boardServedPageViewModel struct {
	Title                       string
	GeneratedAt                 string
	Style                       template.CSS
	LiveRefreshScript           template.JS
	SearchScript                template.JS
	SearchQuery                 string
	SelectedEntityType          string
	SearchStatusMessage         string
	SearchResults               []boardSearchResultViewModel
	SearchHasResults            bool
	SearchHasMore               bool
	SearchInvalid               bool
	SearchError                 bool
	SearchIsInitial             bool
	StatusCounts                []boardStatusCountViewModel
	ActiveAttempts              []boardActiveAttemptViewModel
	ActiveReservationCount      int
	BlockedIssues               []boardIssueRowViewModel
	ReviewRequests              []boardReviewRequestViewModel
	PlanningGraphSVG            template.HTML
	PlanningGraphSummary        string
	PlanningGraphMermaid        string
	ActiveAttemptsTruncated     bool
	ActiveReservationsTruncated bool
	BlockedIssuesTruncated      bool
	ReviewRequestsTruncated     bool
}

type boardSearchResultViewModel struct {
	Title        string
	EntityType   string
	EntityID     string
	IssueLabel   string
	IssueHref    string
	HasIssueLink bool
	Snippet      string
}

type boardStatusCountViewModel struct {
	Status string
	Count  int
}

type boardActiveAttemptViewModel struct {
	AttemptID       string
	IssueLabel      string
	IssueHref       string
	HasIssueLink    bool
	IssueTitle      string
	Kind            string
	SessionLabel    string
	StartedAt       string
	LeaseExpiresAt  string
	Reservations    []boardReservationRowViewModel
	HasReservations bool
	// GateProgress is the attempt's workflow-gate progress as text ("2/3
	// satisfied", or "none apply" when no requirements match), with unmet
	// requirement keys listed beneath it (ISSUE-175 AC2). Text, not a
	// color-only indicator, so the state is perceivable without vision.
	GateProgress string
	GateUnmet    []string
	HasGateUnmet bool
}

// boardReservationRowViewModel is one active reservation nested under its
// owning attempt's boardActiveAttemptViewModel row -- kind and display
// value only, since owner/session/lease-expiry are already on the parent
// row (ISSUE-181's "group active reservations under active attempts").
type boardReservationRowViewModel struct {
	Kind         string
	DisplayValue string
}

type boardIssueRowViewModel struct {
	Label         string
	Title         string
	BlockedReason string
	IssueHref     string
	HasIssueLink  bool
}

type boardReviewRequestViewModel struct {
	ID            string
	IssueLabel    string
	IssueHref     string
	HasIssueLink  bool
	Status        string
	TargetVersion int
	CreatedAt     string
}

type issueDetailPageViewModel struct {
	Title               string
	Identifier          string
	BoardEndpoint       string
	BoardRoute          string
	ReturnHref          string
	IssueHeading        string
	StatusLine          string
	Metadata            []issueDetailMetadataViewModel
	Labels              []string
	HasLabels           bool
	Description         issueDetailTextSectionViewModel
	AcceptanceCriteria  issueDetailTextSectionViewModel
	BlockedReason       issueDetailTextSectionViewModel
	RootIssue           *issueDetailLinkViewModel
	LatestAttempt       *issueDetailAttemptViewModel
	OpenReview          *issueDetailReviewViewModel
	LatestDecision      *issueDetailDecisionViewModel
	GraphSVG            template.HTML
	Activity            issueDetailActivityViewModel
	Reservations        []issueDetailReservationViewModel
	HasReservations     bool
	HasMoreReservations bool
	Gates               issueDetailGatesViewModel
	Style               template.CSS
	LiveRefreshScript   template.JS
}

// issueDetailGatesViewModel is the issue page's workflow-gate section
// (ISSUE-175 AC2): a one-line summary of what was evaluated and how far
// along it is, plus one row per unmet requirement with its reason and the
// imperative next action that clears it.
type issueDetailGatesViewModel struct {
	StatusLine string
	NoneApply  bool
	Satisfied  bool
	Unmet      []issueDetailGateUnmetViewModel
	HasUnmet   bool
}

type issueDetailGateUnmetViewModel struct {
	RequirementKey string
	Reason         string
	NextAction     string
}

// issueDetailReservationViewModel is one current or historical reservation
// row on the issue detail page (ISSUE-181: "issue detail shows current and
// historical rows").
type issueDetailReservationViewModel struct {
	Kind         string
	DisplayValue string
	Status       string
	CreatedAt    string
	ReleasedAt   string
	HasReleased  bool
}

type issueDetailMetadataViewModel struct {
	Label string
	Value string
}

type issueDetailTextSectionViewModel struct {
	Heading      string
	Value        string
	IsEmpty      bool
	EmptyMessage string
}

type issueDetailLinkViewModel struct {
	Label string
	Href  string
}

type issueDetailAttemptViewModel struct {
	ID         string
	StartedAt  string
	HasStarted bool
}

type issueDetailReviewViewModel struct {
	ID        string
	Status    string
	HasStatus bool
}

type issueDetailDecisionViewModel struct {
	Title      string
	Summary    string
	HasSummary bool
}

type issueDetailActivityViewModel struct {
	HasMore bool
	Items   []issueDetailActivityItemViewModel
}

type issueDetailActivityItemViewModel struct {
	Summary    string
	OccurredAt string
	HasDate    bool
}

func newBoardStaticPageViewModel(result domain.BoardResult) boardStaticPageViewModel {
	mapping := issueDisplayIDMap(result.PlanningGraph.Nodes)
	vm := boardStaticPageViewModel{
		Title:                       "Rhizome status board",
		GeneratedAt:                 result.GeneratedAt.Format(time.RFC3339),
		Style:                       template.CSS(boardHTMLStyle),
		StatusCounts:                make([]boardStatusCountViewModel, 0, len(result.StatusCounts)),
		ActiveAttempts:              make([]boardActiveAttemptViewModel, 0, len(result.ActiveAttempts)),
		ActiveReservationCount:      len(result.ActiveReservations),
		BlockedIssues:               make([]boardIssueRowViewModel, 0, len(result.BlockedIssues)),
		ReviewRequests:              make([]boardReviewRequestViewModel, 0, len(result.ReviewRequests)),
		PlanningGraphSVG:            template.HTML(renderBoardGraphSVG(result.PlanningGraph)),
		PlanningGraphSummary:        buildPlanningGraphSummary(result.PlanningGraph),
		PlanningGraphMermaid:        renderMermaid(result.PlanningGraph),
		ActiveAttemptsTruncated:     result.Truncation.ActiveAttempts,
		ActiveReservationsTruncated: result.Truncation.ActiveReservations,
		BlockedIssuesTruncated:      result.Truncation.BlockedIssues,
		ReviewRequestsTruncated:     result.Truncation.ReviewRequests,
	}
	for _, count := range result.StatusCounts {
		vm.StatusCounts = append(vm.StatusCounts, boardStatusCountViewModel{Status: string(count.EffectiveStatus), Count: int(count.Count)})
	}
	reservationsByAttempt := boardReservationsByAttempt(result.ActiveReservations)
	gatesByAttempt := boardGatesByAttempt(result.AttemptGates)
	for _, attempt := range result.ActiveAttempts {
		reservations := reservationsByAttempt[attempt.AttemptID]
		gates, hasGates := gatesByAttempt[attempt.AttemptID]
		vm.ActiveAttempts = append(vm.ActiveAttempts, boardActiveAttemptViewModel{
			AttemptID:       attempt.AttemptID,
			IssueLabel:      issueDisplayLabel(attempt.IssueID, attempt.IssueDisplayID, mapping),
			IssueTitle:      attempt.IssueTitle,
			Kind:            string(attempt.Kind),
			SessionLabel:    sessionLabel(attempt.SessionLabel),
			StartedAt:       attempt.StartedAt.Format(time.RFC3339),
			LeaseExpiresAt:  attempt.LeaseExpiresAt.Format(time.RFC3339),
			Reservations:    reservations,
			HasReservations: len(reservations) > 0,
			GateProgress:    boardGateProgressText(gates, hasGates),
			GateUnmet:       boardGateUnmetLines(gates),
			HasGateUnmet:    len(gates.Unmet) > 0,
		})
	}
	for _, issue := range result.BlockedIssues {
		vm.BlockedIssues = append(vm.BlockedIssues, boardIssueRowViewModel{
			Label:         issueDisplayLabel(issue.Issue.ID, issue.Issue.DisplayID, mapping),
			Title:         issue.Title,
			BlockedReason: blockedReasonValue(issue.BlockedReason),
		})
	}
	for _, request := range result.ReviewRequests {
		vm.ReviewRequests = append(vm.ReviewRequests, boardReviewRequestViewModel{
			ID:            request.ID,
			IssueLabel:    issueDisplayLabel(request.IssueID, "", mapping),
			Status:        string(request.Status),
			TargetVersion: int(request.TargetIssueVersion),
			CreatedAt:     request.CreatedAt.Format(time.RFC3339),
		})
	}
	return vm
}

func newBoardServedPageViewModel(result domain.BoardResult, state servedBoardSearchState) boardServedPageViewModel {
	mapping := issueDisplayIDMap(result.PlanningGraph.Nodes)
	vm := boardServedPageViewModel{
		Title:                       "Rhizome status board",
		GeneratedAt:                 result.GeneratedAt.Format(time.RFC3339),
		Style:                       template.CSS(boardHTMLStyle),
		LiveRefreshScript:           template.JS(boardLiveRefreshScript),
		SearchScript:                template.JS(boardSearchScript),
		SearchQuery:                 state.Query,
		SelectedEntityType:          state.EntityType,
		SearchStatusMessage:         state.StatusMessage,
		SearchHasResults:            len(state.Results) > 0,
		SearchHasMore:               state.HasMore,
		SearchInvalid:               state.Invalid,
		SearchError:                 state.Error,
		SearchIsInitial:             state.Query == "",
		StatusCounts:                make([]boardStatusCountViewModel, 0, len(result.StatusCounts)),
		ActiveAttempts:              make([]boardActiveAttemptViewModel, 0, len(result.ActiveAttempts)),
		ActiveReservationCount:      len(result.ActiveReservations),
		BlockedIssues:               make([]boardIssueRowViewModel, 0, len(result.BlockedIssues)),
		ReviewRequests:              make([]boardReviewRequestViewModel, 0, len(result.ReviewRequests)),
		PlanningGraphSVG:            template.HTML(renderServedBoardGraphSVG(result.PlanningGraph)),
		PlanningGraphSummary:        buildPlanningGraphSummary(result.PlanningGraph),
		PlanningGraphMermaid:        renderMermaid(result.PlanningGraph),
		ActiveAttemptsTruncated:     result.Truncation.ActiveAttempts,
		ActiveReservationsTruncated: result.Truncation.ActiveReservations,
		BlockedIssuesTruncated:      result.Truncation.BlockedIssues,
		ReviewRequestsTruncated:     result.Truncation.ReviewRequests,
	}
	for _, count := range result.StatusCounts {
		vm.StatusCounts = append(vm.StatusCounts, boardStatusCountViewModel{Status: string(count.EffectiveStatus), Count: int(count.Count)})
	}
	reservationsByAttempt := boardReservationsByAttempt(result.ActiveReservations)
	gatesByAttempt := boardGatesByAttempt(result.AttemptGates)
	for _, attempt := range result.ActiveAttempts {
		reservations := reservationsByAttempt[attempt.AttemptID]
		gates, hasGates := gatesByAttempt[attempt.AttemptID]
		vm.ActiveAttempts = append(vm.ActiveAttempts, boardActiveAttemptViewModel{
			AttemptID:       attempt.AttemptID,
			IssueLabel:      issueDisplayLabel(attempt.IssueID, attempt.IssueDisplayID, mapping),
			IssueHref:       boardIssuePath(attempt.IssueID, issueDisplayLabel(attempt.IssueID, attempt.IssueDisplayID, mapping)),
			HasIssueLink:    true,
			IssueTitle:      attempt.IssueTitle,
			Kind:            string(attempt.Kind),
			SessionLabel:    sessionLabel(attempt.SessionLabel),
			StartedAt:       attempt.StartedAt.Format(time.RFC3339),
			LeaseExpiresAt:  attempt.LeaseExpiresAt.Format(time.RFC3339),
			Reservations:    reservations,
			HasReservations: len(reservations) > 0,
			GateProgress:    boardGateProgressText(gates, hasGates),
			GateUnmet:       boardGateUnmetLines(gates),
			HasGateUnmet:    len(gates.Unmet) > 0,
		})
	}
	for _, issue := range result.BlockedIssues {
		label := issueDisplayLabel(issue.Issue.ID, issue.Issue.DisplayID, mapping)
		vm.BlockedIssues = append(vm.BlockedIssues, boardIssueRowViewModel{
			Label:         label,
			Title:         issue.Title,
			BlockedReason: blockedReasonValue(issue.BlockedReason),
			IssueHref:     boardIssuePath(issue.Issue.ID, label),
			HasIssueLink:  true,
		})
	}
	for _, request := range result.ReviewRequests {
		label := issueDisplayLabel(request.IssueID, "", mapping)
		vm.ReviewRequests = append(vm.ReviewRequests, boardReviewRequestViewModel{
			ID:            request.ID,
			IssueLabel:    label,
			IssueHref:     boardIssuePath(request.IssueID, label),
			HasIssueLink:  true,
			Status:        string(request.Status),
			TargetVersion: int(request.TargetIssueVersion),
			CreatedAt:     request.CreatedAt.Format(time.RFC3339),
		})
	}
	vm.SearchResults = make([]boardSearchResultViewModel, 0, len(state.Results))
	for _, result := range state.Results {
		issueLabel := ""
		issueHref := ""
		hasIssueLink := false
		if result.IssueID != nil && strings.TrimSpace(*result.IssueID) != "" {
			issueLabel = strings.TrimSpace(*result.IssueID)
			issueHref = boardIssuePath(strings.TrimSpace(*result.IssueID), issueLabel)
			hasIssueLink = true
		}
		vm.SearchResults = append(vm.SearchResults, boardSearchResultViewModel{
			Title:        result.Title,
			EntityType:   string(result.EntityType),
			EntityID:     result.EntityID,
			IssueLabel:   issueLabel,
			IssueHref:    issueHref,
			HasIssueLink: hasIssueLink,
			Snippet:      result.Snippet,
		})
	}
	return vm
}

func newIssueDetailPageViewModel(detail domain.IssueDetail) issueDetailPageViewModel {
	identifier := detail.Issue.DisplayID
	if identifier == "" {
		identifier = detail.Issue.ID
	}
	vm := issueDetailPageViewModel{
		Title:              "Rhizome issue detail",
		Identifier:         identifier,
		BoardEndpoint:      "/api/issues/" + identifier,
		BoardRoute:         "/issues/" + identifier,
		ReturnHref:         "/",
		IssueHeading:       detail.Issue.DisplayID,
		StatusLine:         buildIssueStatusLine(detail),
		Metadata:           []issueDetailMetadataViewModel{},
		Labels:             make([]string, 0, len(detail.Issue.Labels)),
		Description:        issueDetailTextSectionViewModel{Heading: "Description", EmptyMessage: "No description provided."},
		AcceptanceCriteria: issueDetailTextSectionViewModel{Heading: "Acceptance criteria", EmptyMessage: "No acceptance criteria provided."},
		BlockedReason:      issueDetailTextSectionViewModel{Heading: "Blocked reason", EmptyMessage: "No blocked reason provided."},
		Style:              template.CSS(boardHTMLStyle),
		LiveRefreshScript:  template.JS(boardLiveRefreshScript),
	}
	if vm.IssueHeading == "" {
		vm.IssueHeading = detail.Issue.ID
	}
	if strings.TrimSpace(detail.Issue.Title) != "" {
		vm.IssueHeading = vm.IssueHeading + " — " + strings.TrimSpace(detail.Issue.Title)
	}
	vm.Metadata = append(vm.Metadata,
		issueDetailMetadataViewModel{Label: "Version", Value: stringFromInt(int(detail.Issue.Version))},
		issueDetailMetadataViewModel{Label: "Created", Value: formatIssueDetailTimestamp(detail.Issue.CreatedAt)},
		issueDetailMetadataViewModel{Label: "Updated", Value: formatIssueDetailTimestamp(detail.Issue.UpdatedAt)},
	)
	if detail.Issue.ArchivedAt != nil {
		vm.Metadata = append(vm.Metadata, issueDetailMetadataViewModel{Label: "Archived", Value: formatIssueDetailTimestamp(*detail.Issue.ArchivedAt)})
	} else {
		vm.Metadata = append(vm.Metadata, issueDetailMetadataViewModel{Label: "Archived", Value: "Not archived."})
	}
	for _, label := range detail.Issue.Labels {
		vm.Labels = append(vm.Labels, label.Name)
	}
	vm.HasLabels = len(vm.Labels) > 0
	if detail.Issue.Description != nil && strings.TrimSpace(*detail.Issue.Description) != "" {
		vm.Description.Value = strings.TrimSpace(*detail.Issue.Description)
	} else {
		vm.Description.IsEmpty = true
	}
	if detail.Issue.AcceptanceCriteria != nil && strings.TrimSpace(*detail.Issue.AcceptanceCriteria) != "" {
		vm.AcceptanceCriteria.Value = strings.TrimSpace(*detail.Issue.AcceptanceCriteria)
	} else {
		vm.AcceptanceCriteria.IsEmpty = true
	}
	if detail.Issue.BlockedReason != nil && strings.TrimSpace(*detail.Issue.BlockedReason) != "" {
		vm.BlockedReason.Value = strings.TrimSpace(*detail.Issue.BlockedReason)
	} else {
		vm.BlockedReason.IsEmpty = true
	}
	if detail.RootIssueProjection != nil && !sameIssueIdentity(detail.Issue, detail.RootIssueProjection.Issue) {
		mapping := issueDisplayIDMap(detail.Graph.Nodes)
		label := issueDisplayLabel(detail.RootIssueProjection.Issue.ID, detail.RootIssueProjection.Issue.DisplayID, mapping)
		vm.RootIssue = &issueDetailLinkViewModel{Label: label, Href: boardIssuePath(detail.RootIssueProjection.Issue.ID, label)}
	}
	if detail.LatestAttempt != nil {
		vm.LatestAttempt = &issueDetailAttemptViewModel{ID: detail.LatestAttempt.ID}
		if !detail.LatestAttempt.StartedAt.IsZero() {
			vm.LatestAttempt.StartedAt = formatIssueDetailTimestamp(detail.LatestAttempt.StartedAt)
			vm.LatestAttempt.HasStarted = true
		}
	}
	if detail.OpenReview != nil {
		vm.OpenReview = &issueDetailReviewViewModel{ID: detail.OpenReview.ID}
		if detail.OpenReview.Status != "" {
			vm.OpenReview.Status = string(detail.OpenReview.Status)
			vm.OpenReview.HasStatus = true
		}
	}
	if detail.LatestDecision != nil {
		vm.LatestDecision = &issueDetailDecisionViewModel{Title: detail.LatestDecision.Title}
		if detail.LatestDecision.Summary != "" {
			vm.LatestDecision.Summary = detail.LatestDecision.Summary
			vm.LatestDecision.HasSummary = true
		}
	}
	if len(detail.Graph.Nodes) > 0 || len(detail.Graph.Edges) > 0 {
		vm.GraphSVG = template.HTML(renderServedBoardGraphSVG(detail.Graph))
	}
	vm.Activity.Items = make([]issueDetailActivityItemViewModel, 0, len(detail.Activity.Items))
	for _, item := range detail.Activity.Items {
		vm.Activity.Items = append(vm.Activity.Items, issueDetailActivityItemViewModel{
			Summary:    issueActivitySummary(item),
			OccurredAt: item.OccurredAt.Format(time.RFC3339),
			HasDate:    !item.OccurredAt.IsZero(),
		})
	}
	vm.Activity.HasMore = detail.Activity.HasMore
	vm.Reservations = make([]issueDetailReservationViewModel, 0, len(detail.Reservations))
	for _, reservation := range detail.Reservations {
		row := issueDetailReservationViewModel{
			Kind: string(reservation.Kind), DisplayValue: reservation.DisplayValue, Status: string(reservation.Status),
			CreatedAt: reservation.CreatedAt.Format(time.RFC3339),
		}
		if reservation.ReleasedAt != nil {
			row.ReleasedAt = reservation.ReleasedAt.Format(time.RFC3339)
			row.HasReleased = true
		}
		vm.Reservations = append(vm.Reservations, row)
	}
	vm.HasReservations = len(vm.Reservations) > 0
	vm.HasMoreReservations = detail.HasMoreReservations
	vm.Gates = newIssueDetailGatesViewModel(detail.Gates)
	return vm
}

// boardGatesByAttempt indexes attempt gate-progress rows by attempt ID for
// joining against the already-rendered active attempts, the same join the
// reservation rows use.
func boardGatesByAttempt(rows []domain.AttemptGateProgress) map[string]domain.WorkContextGateSummary {
	byAttempt := make(map[string]domain.WorkContextGateSummary, len(rows))
	for _, row := range rows {
		byAttempt[row.AttemptID] = row.Gates
	}
	return byAttempt
}

// boardGateProgressText renders gate progress as text rather than a
// color-only indicator, so the state is perceivable without vision.
func boardGateProgressText(summary domain.WorkContextGateSummary, hasSummary bool) string {
	if !hasSummary {
		return "—"
	}
	if summary.RequirementCount == 0 {
		return "none apply"
	}
	return strconv.FormatInt(summary.SatisfiedCount, 10) + "/" + strconv.FormatInt(summary.RequirementCount, 10) + " satisfied"
}

func boardGateUnmetLines(summary domain.WorkContextGateSummary) []string {
	lines := make([]string, 0, len(summary.Unmet))
	for _, unmet := range summary.Unmet {
		lines = append(lines, unmet.RequirementKey+": "+unmet.Reason)
	}
	return lines
}

func newIssueDetailGatesViewModel(summary domain.WorkContextGateSummary) issueDetailGatesViewModel {
	vm := issueDetailGatesViewModel{}
	if summary.RequirementCount == 0 {
		vm.NoneApply = true
		vm.StatusLine = "No workflow gate requirements apply to this issue."
		return vm
	}
	source := "live policies"
	if summary.SnapshotFingerprint != nil {
		source = "the active attempt's frozen snapshot (fingerprint " + *summary.SnapshotFingerprint + ")"
	}
	vm.StatusLine = "Evaluated at " + string(summary.Point) + " against " + source + ": " +
		strconv.FormatInt(summary.SatisfiedCount, 10) + " of " + strconv.FormatInt(summary.RequirementCount, 10) + " requirements satisfied."
	vm.Satisfied = len(summary.Unmet) == 0
	vm.Unmet = make([]issueDetailGateUnmetViewModel, 0, len(summary.Unmet))
	for index, unmet := range summary.Unmet {
		nextAction := ""
		if index < len(summary.NextActions) {
			nextAction = summary.NextActions[index]
		}
		vm.Unmet = append(vm.Unmet, issueDetailGateUnmetViewModel{
			RequirementKey: unmet.RequirementKey,
			Reason:         unmet.Reason,
			NextAction:     nextAction,
		})
	}
	vm.HasUnmet = len(vm.Unmet) > 0
	return vm
}

func buildIssueStatusLine(detail domain.IssueDetail) string {
	return "Stored status: " + string(detail.Issue.Status) + " · Effective status: " + string(EffectiveStatusForIssue(detail)) + " · Type: " + string(detail.Issue.Type) + " · Priority: " + string(detail.Issue.Priority)
}

// boardReservationsByAttempt groups active reservations by their owning
// attempt ID, preserving id ordering within each group (the repository
// orders ListReservations by created_at DESC, id DESC). Every active
// reservation's owning attempt is, by definition, active, so grouping by
// AttemptID against the already-fetched ActiveAttempts needs no separate
// issue/session/lease-expiry lookup.
func boardReservationsByAttempt(reservations []domain.Reservation) map[string][]boardReservationRowViewModel {
	grouped := make(map[string][]boardReservationRowViewModel, len(reservations))
	for _, reservation := range reservations {
		grouped[reservation.AttemptID] = append(grouped[reservation.AttemptID], boardReservationRowViewModel{
			Kind: string(reservation.Kind), DisplayValue: reservation.DisplayValue,
		})
	}
	return grouped
}

func issueDisplayLabel(identifier string, displayID string, mapping map[string]string) string {
	candidate := strings.TrimSpace(displayID)
	if candidate == "" {
		candidate = strings.TrimSpace(identifier)
	}
	if candidate == "" && mapping != nil {
		if value, ok := mapping[identifier]; ok {
			candidate = strings.TrimSpace(value)
		}
	}
	return candidate
}

func blockedReasonValue(reason *string) string {
	if reason == nil {
		return ""
	}
	return *reason
}

func sessionLabel(value *string) string {
	if value == nil {
		return "—"
	}
	if strings.TrimSpace(*value) == "" {
		return "—"
	}
	return *value
}

func stringFromInt(value int) string {
	return strconv.Itoa(value)
}

func buildPlanningGraphSummary(graph domain.GraphResult) string {
	truncatedNote := ""
	if graph.Truncated {
		truncatedNote = " (truncated)"
	}
	return strconv.Itoa(graph.Summary.NodeCount) + " nodes, " + strconv.Itoa(graph.Summary.EdgeCount) + " edges, " + strconv.Itoa(graph.Summary.EntryPointCount) + " entry points, " + strconv.Itoa(graph.Summary.BlockingNodeCount) + " blocking nodes" + truncatedNote + "."
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameIssueIdentity(left domain.Issue, right domain.Issue) bool {
	leftID := strings.TrimSpace(left.ID)
	rightID := strings.TrimSpace(right.ID)
	if leftID != "" && rightID != "" {
		return leftID == rightID
	}
	leftDisplay := strings.TrimSpace(left.DisplayID)
	rightDisplay := strings.TrimSpace(right.DisplayID)
	return leftDisplay != "" && rightDisplay != "" && leftDisplay == rightDisplay
}
