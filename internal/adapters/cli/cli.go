package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"rhizome-mcp/internal/domain"
)

// ProjectService exposes current project metadata reads for CLI commands.
type ProjectService interface {
	GetProject(context.Context) (domain.Project, error)
}

// IssueService exposes issue list/show reads for CLI commands.
type IssueService interface {
	ListIssues(context.Context, domain.ListIssuesInput) (domain.IssueList, error)
	GetIssue(context.Context, string) (domain.Issue, error)
}

// SearchService exposes full-text search reads for CLI commands.
type SearchService interface {
	Search(context.Context, domain.SearchInput) (domain.SearchPage, error)
}

// GraphService exposes graph traversal reads for CLI commands.
type GraphService interface {
	GetIssueGraph(context.Context, domain.GetIssueGraphInput) (domain.GraphResult, error)
}

// Services packages the application-layer services used by the CLI adapter.
type Services struct {
	ProjectService ProjectService
	IssueService   IssueService
	SearchService  SearchService
	GraphService   GraphService
}

// InitHandler runs CLI init logic after the adapter parses the command.
type InitHandler func(context.Context, string) error

// ServeHandler runs CLI serve logic after the adapter parses the command.
type ServeHandler func(context.Context) error

// CLI adapts CLI command parsing and output rendering over application services.
type CLI struct {
	services     Services
	stdout       io.Writer
	stderr       io.Writer
	initHandler  InitHandler
	serveHandler ServeHandler
}

// New constructs a CLI adapter around application services and output writers.
func New(services Services, stdout, stderr io.Writer, initHandler InitHandler, serveHandler ServeHandler) *CLI {
	return &CLI{services: services, stdout: stdout, stderr: stderr, initHandler: initHandler, serveHandler: serveHandler}
}

// Run parses the supplied arguments and dispatches the matching subcommand.
func (c *CLI) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}

	switch args[0] {
	case "init":
		return c.runInit(ctx, args[1:])
	case "serve":
		return c.runServe(ctx, args[1:])
	case "project":
		return c.runProject(ctx, args[1:])
	case "issue":
		return c.runIssue(ctx, args[1:])
	case "search":
		return c.runSearch(ctx, args[1:])
	case "graph":
		return c.runGraph(ctx, args[1:])
	default:
		return c.usageError()
	}
}

func (c *CLI) runInit(ctx context.Context, args []string) error {
	if c.initHandler == nil {
		return fmt.Errorf("init handler is not configured")
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dataRoot := fs.String("data-root", "", "external project data root")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	return c.initHandler(ctx, *dataRoot)
}

func (c *CLI) runServe(ctx context.Context, args []string) error {
	if c.serveHandler == nil {
		return fmt.Errorf("serve handler is not configured")
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	return c.serveHandler(ctx)
}

func (c *CLI) runProject(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}
	if args[0] != "info" {
		return c.usageError()
	}
	if c.services.ProjectService == nil {
		return fmt.Errorf("project service is not configured")
	}
	fs := flag.NewFlagSet("project info", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args[1:])
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("unsupported format %q", *format)
	}
	project, err := c.services.ProjectService.GetProject(ctx)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), ProjectInfoResponse{Project: projectInfoFromDomain(project)})
	}
	return c.writeProjectInfoTable(project)
}

func (c *CLI) runIssue(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}
	if c.services.IssueService == nil {
		return fmt.Errorf("issue service is not configured")
	}

	switch args[0] {
	case "list":
		return c.runIssueList(ctx, args[1:])
	case "show":
		return c.runIssueShow(ctx, args[1:])
	default:
		return c.usageError()
	}
}

func (c *CLI) runIssueList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	limit := fs.Int("limit", 0, "maximum items to return")
	cursor := fs.String("cursor", "", "pagination cursor")
	types := newStringSliceValue()
	statuses := newStringSliceValue()
	effectiveStatuses := newStringSliceValue()
	priorities := newStringSliceValue()
	fs.Var(types, "type", "repeatable issue type")
	fs.Var(statuses, "status", "repeatable issue status")
	fs.Var(effectiveStatuses, "effective-status", "repeatable effective status")
	fs.Var(priorities, "priority", "repeatable issue priority")
	includeArchived := fs.Bool("include-archived", false, "include archived issues")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("unsupported format %q", *format)
	}

	input := domain.ListIssuesInput{
		Types:             toDomainTypes(types.values()),
		Statuses:          toDomainStatuses(statuses.values()),
		EffectiveStatuses: toDomainEffectiveStatuses(effectiveStatuses.values()),
		Priorities:        toDomainPriorities(priorities.values()),
		IncludeArchived:   *includeArchived,
		Limit:             *limit,
		Cursor:            *cursor,
	}
	page, err := c.services.IssueService.ListIssues(ctx, input)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), issueListResponseFromDomain(page))
	}
	return c.writeIssueListTable(page)
}

func (c *CLI) runIssueShow(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("issue show", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("unsupported format %q", *format)
	}
	issue, err := c.services.IssueService.GetIssue(ctx, positionals[0])
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), IssueResponse{Issue: issueFromDomain(issue)})
	}
	return c.writeIssueTable(issue)
}

func (c *CLI) runSearch(ctx context.Context, args []string) error {
	if c.services.SearchService == nil {
		return fmt.Errorf("search service is not configured")
	}
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	limit := fs.Int("limit", 0, "maximum items to return")
	cursor := fs.String("cursor", "", "pagination cursor")
	entityTypes := newStringSliceValue()
	issue := newOptionalStringValue()
	epic := newOptionalStringValue()
	statuses := newStringSliceValue()
	labels := newStringSliceValue()
	fs.Var(entityTypes, "entity-type", "repeatable entity type")
	fs.Var(issue, "issue", "issue ID filter")
	fs.Var(epic, "epic", "epic ID filter")
	fs.Var(statuses, "status", "repeatable issue status")
	fs.Var(labels, "label", "repeatable label")
	snippetLength := fs.Int("snippet-length", 0, "snippet length")
	includeArchived := fs.Bool("include-archived", false, "include archived items")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("unsupported format %q", *format)
	}

	input := domain.SearchInput{
		Query:           positionals[0],
		EntityTypes:     toDomainSearchEntityTypes(entityTypes.values()),
		IssueID:         issue.value(),
		EpicID:          epic.value(),
		Statuses:        toDomainStatuses(statuses.values()),
		Labels:          labels.values(),
		IncludeArchived: *includeArchived,
		Limit:           *limit,
		Cursor:          *cursor,
		SnippetLength:   *snippetLength,
	}
	page, err := c.services.SearchService.Search(ctx, input)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), searchResponseFromDomain(page))
	}
	return c.writeSearchTable(page)
}

func (c *CLI) runGraph(ctx context.Context, args []string) error {
	if c.services.GraphService == nil {
		return fmt.Errorf("graph service is not configured")
	}
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	depth := newOptionalIntValue()
	maxNodes := newOptionalIntValue()
	relationTypes := newStringSliceValue()
	includeHierarchy := newOptionalBoolValue()
	includeTerminal := newOptionalBoolValue()
	fs.Var(depth, "depth", "graph traversal depth")
	fs.Var(maxNodes, "max-nodes", "maximum number of nodes")
	direction := fs.String("direction", "", "graph traversal direction")
	fs.Var(relationTypes, "relation-type", "repeatable relation type")
	fs.Var(includeHierarchy, "include-hierarchy", "include hierarchy relations")
	fs.Var(includeTerminal, "include-terminal", "include terminal nodes")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" && *format != "mermaid" {
		return fmt.Errorf("unsupported format %q", *format)
	}

	input := domain.GetIssueGraphInput{RootIssueID: positionals[0], Direction: domain.GraphDirection(*direction)}
	if depth.set {
		value := *depth.value
		input.Depth = &value
	}
	if maxNodes.set {
		value := *maxNodes.value
		input.MaxNodes = &value
	}
	if includeHierarchy.set {
		value := *includeHierarchy.value
		input.IncludeHierarchy = &value
	}
	if includeTerminal.set {
		value := *includeTerminal.value
		input.IncludeTerminal = &value
	}
	input.RelationTypes = toDomainRelationTypes(relationTypes.values())
	result, err := c.services.GraphService.GetIssueGraph(ctx, input)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), result)
	}
	if *format == "mermaid" {
		_, err := fmt.Fprint(c.stdoutWriter(), renderMermaid(result))
		return err
	}
	return c.writeGraphTable(result)
}

func (c *CLI) writeProjectInfoTable(project domain.Project) error {
	lines := []string{
		fmt.Sprintf("id\t%s", project.ID),
		fmt.Sprintf("name\t%s", formatOptionalString(project.Name)),
		fmt.Sprintf("next_issue_number\t%d", project.NextIssueNumber),
		fmt.Sprintf("schema_version\t%d", project.SchemaVersion),
		fmt.Sprintf("latest_event_id\t%d", project.LatestEventID),
	}
	_, err := fmt.Fprintln(c.stdoutWriter(), strings.Join(lines, "\n"))
	return err
}

func (c *CLI) writeIssueListTable(page domain.IssueList) error {
	var builder strings.Builder
	builder.WriteString("display_id\ttype\tstatus\tpriority\ttitle\n")
	for _, item := range page.Items {
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n", item.DisplayID, item.Type, item.Status, item.Priority, escapeTableValue(item.Title)))
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func (c *CLI) writeIssueTable(issue domain.Issue) error {
	lines := []string{
		fmt.Sprintf("display_id\t%s", issue.DisplayID),
		fmt.Sprintf("type\t%s", issue.Type),
		fmt.Sprintf("status\t%s", issue.Status),
		fmt.Sprintf("priority\t%s", issue.Priority),
		fmt.Sprintf("title\t%s", escapeTableValue(issue.Title)),
	}
	_, err := fmt.Fprintln(c.stdoutWriter(), strings.Join(lines, "\n"))
	return err
}

func (c *CLI) writeSearchTable(page domain.SearchPage) error {
	var builder strings.Builder
	builder.WriteString("entity_type\tentity_id\tissue_id\ttitle\n")
	for _, item := range page.Results {
		var issueID string
		if item.IssueID != nil {
			issueID = *item.IssueID
		}
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", item.EntityType, item.EntityID, issueID, escapeTableValue(item.Title)))
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func (c *CLI) writeGraphTable(result domain.GraphResult) error {
	var builder strings.Builder
	builder.WriteString("node\tstate\ttitle\n")
	for _, node := range result.Nodes {
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\n", node.DisplayID, node.Status, escapeTableValue(node.Title)))
	}
	builder.WriteString("edges\n")
	for _, edge := range result.Edges {
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\n", edge.SourceIssueID, edge.TargetIssueID, edge.Type))
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func (c *CLI) parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	flagArgs, positionals := splitFlagArgs(fs, args)
	fs.SetOutput(c.stderrWriter())
	fs.Usage = func() {
		fmt.Fprint(c.stderrWriter(), c.usage())
	}
	if err := fs.Parse(flagArgs); err != nil {
		if err != flag.ErrHelp {
			fmt.Fprintln(c.stderrWriter())
			fs.Usage()
		}
		return nil, err
	}
	return positionals, nil
}

func splitFlagArgs(fs *flag.FlagSet, args []string) ([]string, []string) {
	flagArgs := make([]string, 0, len(args))
	positionals := make([]string, 0)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if name == "" {
			continue
		}
		if flagExpectsValue(fs, name) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flagArgs = append(flagArgs, args[i+1])
			i++
		}
	}
	return flagArgs, positionals
}

func flagExpectsValue(fs *flag.FlagSet, name string) bool {
	flag := fs.Lookup(name)
	if flag == nil {
		return true
	}
	if boolFlag, ok := flag.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
		return false
	}
	return true
}

func (c *CLI) stderrWriter() io.Writer {
	if c.stderr == nil {
		return io.Discard
	}
	return c.stderr
}

func (c *CLI) stdoutWriter() io.Writer {
	if c.stdout == nil {
		return io.Discard
	}
	return c.stdout
}

func (c *CLI) usage() string {
	return `Usage:
  rhizome-mcp [--data-root PATH] init
  rhizome-mcp [--data-root PATH] serve
  rhizome-mcp [--data-root PATH] project info [--format table|json]
  rhizome-mcp [--data-root PATH] issue list [--format table|json] [--limit N] [--cursor CURSOR] [--type TYPE ...] [--status STATUS ...] [--effective-status STATUS ...] [--priority PRIORITY ...] [--include-archived]
  rhizome-mcp [--data-root PATH] issue show ISSUE-ID [--format table|json]
  rhizome-mcp [--data-root PATH] search QUERY [--format table|json] [--limit N] [--cursor CURSOR] [--entity-type TYPE ...] [--issue ISSUE-ID] [--epic EPIC-ID] [--status STATUS ...] [--label LABEL ...] [--include-archived] [--snippet-length N]
  rhizome-mcp [--data-root PATH] graph ISSUE-ID [--format table|json|mermaid] [--depth N] [--max-nodes N] [--direction outgoing|incoming|both] [--relation-type TYPE ...] [--include-hierarchy] [--include-terminal]
`
}

func (c *CLI) usageError() error {
	fmt.Fprint(c.stderrWriter(), c.usage())
	return fmt.Errorf("usage error")
}

// InitResponse is the JSON payload emitted by the init command.
type InitResponse struct {
	Root         string `json:"root"`
	ProjectID    string `json:"project_id"`
	DatabasePath string `json:"database_path"`
}

// ProjectInfoResponse is the JSON payload emitted by project info.
type ProjectInfoResponse struct {
	Project ProjectInfo `json:"project"`
}

// ProjectInfo is a stable CLI projection of project metadata.
type ProjectInfo struct {
	ID              string    `json:"id"`
	Name            *string   `json:"name,omitempty"`
	Instructions    *string   `json:"instructions,omitempty"`
	NextIssueNumber int64     `json:"next_issue_number"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	SchemaVersion   int       `json:"schema_version"`
	LatestEventID   int64     `json:"latest_event_id"`
}

func projectInfoFromDomain(project domain.Project) ProjectInfo {
	return ProjectInfo{
		ID:              project.ID,
		Name:            copyOptionalString(project.Name),
		Instructions:    copyOptionalString(project.Instructions),
		NextIssueNumber: project.NextIssueNumber,
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
		SchemaVersion:   project.SchemaVersion,
		LatestEventID:   project.LatestEventID,
	}
}

// IssueResponse is the JSON payload emitted by issue show.
type IssueResponse struct {
	Issue IssueSummary `json:"issue"`
}

// IssueListResponse is the JSON payload emitted by issue list.
type IssueListResponse struct {
	Items      []IssueSummary `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

// IssueSummary is a stable CLI projection of an issue.
type IssueSummary struct {
	ID                     string     `json:"id"`
	DisplayID              string     `json:"display_id"`
	SequenceNo             int64      `json:"sequence_no"`
	Type                   string     `json:"type"`
	Title                  string     `json:"title"`
	Description            *string    `json:"description,omitempty"`
	AcceptanceCriteria     *string    `json:"acceptance_criteria,omitempty"`
	Status                 string     `json:"status"`
	Priority               string     `json:"priority"`
	ParentID               *string    `json:"parent_id,omitempty"`
	BlockedReason          *string    `json:"blocked_reason,omitempty"`
	Version                int64      `json:"version"`
	CreatedBySessionID     *string    `json:"created_by_session_id,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	ClosedAt               *time.Time `json:"closed_at,omitempty"`
	ArchivedAt             *time.Time `json:"archived_at,omitempty"`
	ArchivedBySessionID    *string    `json:"archived_by_session_id,omitempty"`
	Labels                 []string   `json:"labels"`
	EffectiveStatus        string     `json:"effective_status"`
	UnresolvedBlockerCount int64      `json:"unresolved_blocker_count"`
	IsBlocked              bool       `json:"is_blocked"`
	IsClaimable            bool       `json:"is_claimable"`
	ActiveAttemptID        *string    `json:"active_attempt_id,omitempty"`
}

func issueListResponseFromDomain(page domain.IssueList) IssueListResponse {
	items := make([]IssueSummary, len(page.Items))
	for i, item := range page.Items {
		items[i] = issueFromDomainProjection(item)
	}
	var nextCursor *string
	if page.NextCursor != nil {
		value := *page.NextCursor
		nextCursor = &value
	}
	return IssueListResponse{Items: items, NextCursor: nextCursor, HasMore: page.HasMore}
}

func issueFromDomain(issue domain.Issue) IssueSummary {
	labels := make([]string, len(issue.Labels))
	for i, label := range issue.Labels {
		labels[i] = label.Name
	}
	return IssueSummary{
		ID:                  issue.ID,
		DisplayID:           issue.DisplayID,
		SequenceNo:          issue.SequenceNo,
		Type:                string(issue.Type),
		Title:               issue.Title,
		Description:         copyOptionalString(issue.Description),
		AcceptanceCriteria:  copyOptionalString(issue.AcceptanceCriteria),
		Status:              string(issue.Status),
		Priority:            string(issue.Priority),
		ParentID:            copyOptionalString(issue.ParentID),
		BlockedReason:       copyOptionalString(issue.BlockedReason),
		Version:             issue.Version,
		CreatedBySessionID:  copyOptionalString(issue.CreatedBySessionID),
		CreatedAt:           issue.CreatedAt,
		UpdatedAt:           issue.UpdatedAt,
		ClosedAt:            copyOptionalTime(issue.ClosedAt),
		ArchivedAt:          copyOptionalTime(issue.ArchivedAt),
		ArchivedBySessionID: copyOptionalString(issue.ArchivedBySessionID),
		Labels:              labels,
	}
}

func issueFromDomainProjection(projection domain.IssueProjection) IssueSummary {
	summary := issueFromDomain(projection.Issue)
	summary.EffectiveStatus = string(projection.EffectiveStatus)
	summary.UnresolvedBlockerCount = projection.UnresolvedBlockerCount
	summary.IsBlocked = projection.IsBlocked
	summary.IsClaimable = projection.IsClaimable
	summary.ActiveAttemptID = copyOptionalString(projection.ActiveAttemptID)
	return summary
}

// SearchResponse is the JSON payload emitted by search.
type SearchResponse struct {
	Results    []SearchResultSummary `json:"results"`
	NextCursor *string               `json:"next_cursor"`
	HasMore    bool                  `json:"has_more"`
}

// SearchResultSummary is a stable CLI projection of a search result.
type SearchResultSummary struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	IssueID    *string `json:"issue_id,omitempty"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

func searchResponseFromDomain(page domain.SearchPage) SearchResponse {
	results := make([]SearchResultSummary, len(page.Results))
	for i, item := range page.Results {
		results[i] = SearchResultSummary{
			EntityType: string(item.EntityType),
			EntityID:   item.EntityID,
			IssueID:    copyOptionalString(item.IssueID),
			Title:      item.Title,
			Snippet:    item.Snippet,
			Score:      item.Score,
		}
	}
	var nextCursor *string
	if page.NextCursor != nil {
		value := *page.NextCursor
		nextCursor = &value
	}
	return SearchResponse{Results: results, NextCursor: nextCursor, HasMore: page.HasMore}
}

func writeJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func toDomainTypes(values []string) []domain.Type {
	result := make([]domain.Type, len(values))
	for i, value := range values {
		result[i] = domain.Type(value)
	}
	return result
}

func toDomainStatuses(values []string) []domain.Status {
	result := make([]domain.Status, len(values))
	for i, value := range values {
		result[i] = domain.Status(value)
	}
	return result
}

func toDomainEffectiveStatuses(values []string) []domain.EffectiveStatus {
	result := make([]domain.EffectiveStatus, len(values))
	for i, value := range values {
		result[i] = domain.EffectiveStatus(value)
	}
	return result
}

func toDomainPriorities(values []string) []domain.Priority {
	result := make([]domain.Priority, len(values))
	for i, value := range values {
		result[i] = domain.Priority(value)
	}
	return result
}

func toDomainSearchEntityTypes(values []string) []domain.SearchEntityType {
	result := make([]domain.SearchEntityType, len(values))
	for i, value := range values {
		result[i] = domain.SearchEntityType(value)
	}
	return result
}

func toDomainRelationTypes(values []string) []domain.RelationType {
	result := make([]domain.RelationType, len(values))
	for i, value := range values {
		result[i] = domain.RelationType(value)
	}
	return result
}

func copyOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func formatOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func escapeTableValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return value
}

func renderMermaid(result domain.GraphResult) string {
	var builder strings.Builder
	builder.WriteString("```mermaid\n")
	builder.WriteString("graph TD\n")
	nodeIDs := make(map[string]string, len(result.Nodes))
	for index, node := range result.Nodes {
		id := mermaidNodeID(node, index)
		nodeIDs[node.ID] = id
		label := node.DisplayID
		if label == "" {
			label = node.ID
		}
		builder.WriteString(fmt.Sprintf("  %s[\"%s\"]\n", id, escapeMermaidValue(label)))
	}
	for _, edge := range result.Edges {
		sourceID, sourceOK := nodeIDs[edge.SourceIssueID]
		targetID, targetOK := nodeIDs[edge.TargetIssueID]
		if !sourceOK || !targetOK {
			continue
		}
		builder.WriteString(fmt.Sprintf("  %s -->|%s| %s\n", sourceID, escapeMermaidValue(edge.Type), targetID))
	}
	builder.WriteString("```\n")
	return builder.String()
}

func mermaidNodeID(node domain.IssueProjection, index int) string {
	base := node.DisplayID
	if base == "" {
		base = node.ID
	}
	if base == "" {
		return fmt.Sprintf("node%d", index+1)
	}
	var builder strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	identifier := builder.String()
	if identifier == "" {
		identifier = fmt.Sprintf("node%d", index+1)
	}
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "n" + identifier
	}
	return identifier
}

func escapeMermaidValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

type stringSliceValue struct {
	items []string
}

func newStringSliceValue() *stringSliceValue {
	return &stringSliceValue{}
}

func (v *stringSliceValue) String() string {
	return strings.Join(v.items, ",")
}

func (v *stringSliceValue) Set(value string) error {
	v.items = append(v.items, value)
	return nil
}

func (v *stringSliceValue) values() []string {
	return append([]string(nil), v.items...)
}

type optionalStringValue struct {
	item *string
}

func newOptionalStringValue() *optionalStringValue {
	return &optionalStringValue{}
}

func (v *optionalStringValue) String() string {
	if v.item == nil {
		return ""
	}
	return *v.item
}

func (v *optionalStringValue) Set(value string) error {
	if v.item != nil {
		return errors.New("may only be specified once")
	}
	v.item = &value
	return nil
}

func (v *optionalStringValue) value() *string {
	return copyOptionalString(v.item)
}

type optionalIntValue struct {
	value *int
	set   bool
}

func newOptionalIntValue() *optionalIntValue {
	return &optionalIntValue{}
}

func (v *optionalIntValue) String() string {
	if v.value == nil {
		return ""
	}
	return strconv.Itoa(*v.value)
}

func (v *optionalIntValue) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	v.value = &parsed
	v.set = true
	return nil
}

type optionalBoolValue struct {
	value *bool
	set   bool
}

func newOptionalBoolValue() *optionalBoolValue {
	return &optionalBoolValue{}
}

func (v *optionalBoolValue) String() string {
	if v.value == nil {
		return ""
	}
	return strconv.FormatBool(*v.value)
}

func (v *optionalBoolValue) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	v.value = &parsed
	v.set = true
	return nil
}

func (v *optionalBoolValue) IsBoolFlag() bool {
	return true
}
