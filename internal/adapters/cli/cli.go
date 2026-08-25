package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/domain"
)

const (
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiReset = "\x1b[0m"
)

// ProjectService exposes current project metadata reads and exports for CLI commands.
type ProjectService interface {
	GetProject(context.Context) (domain.Project, error)
	ExportLogicalProject(context.Context) ([]byte, error)
	ValidateLogicalProjectImport(context.Context, []byte) (domain.LogicalProjectImportDryRun, error)
	ApplyLogicalProjectImport(context.Context, []byte) (domain.LogicalProjectImportApplyResult, error)
}

// IssueService exposes issue list/show reads for CLI commands.
type IssueService interface {
	ListIssues(context.Context, domain.ListIssuesInput) (domain.IssueList, error)
	GetIssue(context.Context, string) (domain.Issue, error)
	GetIssueProjection(context.Context, string) (domain.IssueProjection, error)
}

// SearchService exposes full-text search reads for CLI commands.
type SearchService interface {
	Search(context.Context, domain.SearchInput) (domain.SearchPage, error)
}

// GraphService exposes graph traversal reads for CLI commands.
type GraphService interface {
	GetIssueGraph(context.Context, domain.GetIssueGraphInput) (domain.GraphResult, error)
}

// MaintenanceService exposes maintenance-only operations for CLI commands.
type MaintenanceService interface {
	ForceReleaseAttempt(context.Context, string) (application.ForceReleaseAttemptResult, error)
	RebuildSearchIndex(context.Context) error
}

// BoardService exposes the bounded, read-only project status board for CLI
// commands.
type BoardService interface {
	GetBoard(context.Context) (domain.BoardResult, error)
}

// Services packages the application-layer services used by the CLI adapter.
type Services struct {
	ProjectService     ProjectService
	IssueService       IssueService
	SearchService      SearchService
	GraphService       GraphService
	MaintenanceService MaintenanceService
	BoardService       BoardService
}

// InitHandler runs CLI init logic after the adapter parses the command.
type InitHandler func(context.Context, string) error

// ServeHandler runs CLI serve logic after the adapter parses the command.
// The second parameter is the requested HTTP address (blank for stdio); the
// third is the requested tool exposure profile (blank keeps the caller's
// configured default).
type ServeHandler func(context.Context, string, string) error

// BoardServeHandler runs CLI board serve logic after the adapter parses the command.
type BoardServeHandler func(context.Context, string, io.Writer) error

// BackupReport summarizes a validated backup database artifact for CLI output.
type BackupReport struct {
	OutputPath    string
	SchemaVersion int
}

// BackupHandler runs a project backup after the adapter parses the command.
type BackupHandler func(context.Context, string) (BackupReport, error)

// DoctorReport summarizes a runtime doctor report for CLI output.
type DoctorReport struct {
	Full       bool          `json:"full"`
	AppVersion string        `json:"app_version,omitempty"`
	Checks     []DoctorCheck `json:"checks"`
}

// DoctorCheck is one doctor verification result.
type DoctorCheck struct {
	Check   string `json:"check"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// MarshalJSON writes doctor report JSON with a computed healthy field.
func (report DoctorReport) MarshalJSON() ([]byte, error) {
	type alias struct {
		Full       bool          `json:"full"`
		Healthy    bool          `json:"healthy"`
		AppVersion string        `json:"app_version,omitempty"`
		Checks     []DoctorCheck `json:"checks"`
	}
	return json.Marshal(alias{Full: report.Full, Healthy: report.Healthy(), AppVersion: report.AppVersion, Checks: report.Checks})
}

// Healthy reports whether every named check passed.
func (report DoctorReport) Healthy() bool {
	for _, check := range report.Checks {
		if !check.Healthy {
			return false
		}
	}
	return true
}

// DoctorHandler runs a project doctor check after the adapter parses the command.
type DoctorHandler func(context.Context, bool) (DoctorReport, error)

// ConnectHandler sets up MCP client configuration after the adapter parses
// the command: target, printOnly, and bareCommand (the --command opt-in for
// a portable, shareable config that names "rhizome-mcp" on PATH instead of
// this resolved binary's absolute, machine-specific path).
type ConnectHandler func(context.Context, string, bool, bool) error

// CLI adapts CLI command parsing and output rendering over application services.
type CLI struct {
	services          Services
	stdout            io.Writer
	stderr            io.Writer
	initHandler       InitHandler
	serveHandler      ServeHandler
	boardServeHandler BoardServeHandler
	backupHandler     BackupHandler
	doctorHandler     DoctorHandler
	connectHandler    ConnectHandler
	appVersion        string
}

// New constructs a CLI adapter around application services and output writers.
func New(services Services, stdout, stderr io.Writer, initHandler InitHandler, serveHandler ServeHandler) *CLI {
	return &CLI{services: services, stdout: stdout, stderr: stderr, initHandler: initHandler, serveHandler: serveHandler}
}

// SetBoardServeHandler installs a handler for the board serve command.
func (c *CLI) SetBoardServeHandler(handler BoardServeHandler) {
	c.boardServeHandler = handler
}

// SetBackupHandler installs a handler for the backup command.
func (c *CLI) SetBackupHandler(handler BackupHandler) {
	c.backupHandler = handler
}

// SetDoctorHandler installs a handler for the doctor command.
func (c *CLI) SetDoctorHandler(handler DoctorHandler) {
	c.doctorHandler = handler
}

// SetConnectHandler installs a handler for the connect command.
func (c *CLI) SetConnectHandler(handler ConnectHandler) {
	c.connectHandler = handler
}

// SetAppVersion sets the application version string for display in CLI outputs.
func (c *CLI) SetAppVersion(version string) {
	c.appVersion = version
}

// Run parses the supplied arguments and dispatches the matching subcommand.
func (c *CLI) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}

	found, ok := lookupCommand(args[0])
	if !ok {
		return c.usageError()
	}
	return found.run(c, ctx, args[1:])
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
	httpAddress := fs.String("http-address", "", "serve over loopback HTTP instead of stdio")
	profile := fs.String("profile", "", "tool exposure profile: full, agent, read-only, or migration (default full)")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if _, err := domain.ParseToolProfile(*profile); err != nil {
		return err
	}
	return c.serveHandler(ctx, *httpAddress, *profile)
}

func (c *CLI) runBackup(ctx context.Context, args []string) error {
	if c.backupHandler == nil {
		return fmt.Errorf("backup handler is not configured")
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := fs.String("output", "", "backup output path")
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *output == "" {
		return fmt.Errorf("output is required")
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	report, err := c.backupHandler(ctx, *output)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), BackupResponse{Output: report.OutputPath, SchemaVersion: report.SchemaVersion, Validated: true})
	}
	return c.writeBackupTable(report)
}

func (c *CLI) runDoctor(ctx context.Context, args []string) error {
	if c.doctorHandler == nil {
		return fmt.Errorf("doctor handler is not configured")
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	full := fs.Bool("full", false, "run the full integrity check")
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	report, err := c.doctorHandler(ctx, *full)
	// An unhealthy verdict is reported even when the handler also errored:
	// the report is the more actionable answer, and TestRunDoctor pins that
	// ordering. The handler error is only surfaced when there is no unhealthy
	// verdict to report, which is what used to be dropped silently.
	if !report.Healthy() {
		if *format == "json" {
			if writeErr := writeJSON(c.stdoutWriter(), report); writeErr != nil {
				return writeErr
			}
		} else if writeErr := c.writeDoctorTable(report); writeErr != nil {
			return writeErr
		}
		return NewDoctorUnhealthyError(failedCheckCount(report))
	}
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), report)
	}
	return c.writeDoctorTable(report)
}

func (c *CLI) runConnect(ctx context.Context, args []string) error {
	if c.connectHandler == nil {
		return fmt.Errorf("connect handler is not configured")
	}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	printFlag := fs.Bool("print", false, "print configuration instead of writing")
	commandFlag := fs.Bool("command", false, "emit a bare \"rhizome-mcp\" command instead of this binary's absolute path, for a config shareable across machines that have it on PATH")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return c.usageError()
	}

	target := positionals[0]
	validTargets := []string{"claude", "codex", "vscode", "json"}
	isValid := false
	for _, valid := range validTargets {
		if target == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		return NewUsageError(fmt.Sprintf("unsupported target %q (valid targets: %s)", target, strings.Join(validTargets, ", ")))
	}

	return c.connectHandler(ctx, target, *printFlag, *commandFlag)
}

func (c *CLI) runProject(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}
	switch args[0] {
	case "info":
		return c.runProjectInfo(ctx, args[1:])
	case "export":
		return c.runProjectExport(ctx, args[1:])
	case "import":
		return c.runProjectImport(ctx, args[1:])
	default:
		return c.usageError()
	}
}

func (c *CLI) runProjectInfo(ctx context.Context, args []string) error {
	if c.services.ProjectService == nil {
		return fmt.Errorf("project service is not configured")
	}
	fs := flag.NewFlagSet("project info", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	project, err := c.services.ProjectService.GetProject(ctx)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), ProjectInfoResponse{Project: projectInfoFromDomain(project, c.appVersion)})
	}
	return c.writeProjectInfoTable(project, c.appVersion)
}

func (c *CLI) runProjectExport(ctx context.Context, args []string) error {
	if c.services.ProjectService == nil {
		return fmt.Errorf("project service is not configured")
	}
	fs := flag.NewFlagSet("project export", flag.ContinueOnError)
	output := fs.String("output", "", "export output path or '-' for stdout")
	overwrite := fs.Bool("overwrite", false, "replace an existing output file")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *output == "" {
		return fmt.Errorf("output is required")
	}
	data, err := c.services.ProjectService.ExportLogicalProject(ctx)
	if err != nil {
		return err
	}
	if *output == "-" {
		payload := append([]byte{}, data...)
		payload = append(payload, '\n')
		if c.stdout != nil {
			_, err = c.stdout.Write(payload)
			return err
		}
		_, err = io.Discard.Write(payload)
		return err
	}
	return c.writeProjectExportFile(*output, data, *overwrite)
}

func (c *CLI) runProjectImport(ctx context.Context, args []string) error {
	if c.services.ProjectService == nil {
		return fmt.Errorf("project service is not configured")
	}
	fs := flag.NewFlagSet("project import", flag.ContinueOnError)
	inputPath := fs.String("input", "", "input path or '-' for stdin")
	dryRun := fs.Bool("dry-run", false, "validate without applying imports")
	apply := fs.Bool("apply", false, "apply a validated import into an empty destination")
	// Defaults to json, not table: this command was JSON-only before it gained
	// a --format flag, so defaulting to table would silently change what
	// existing scripts read from stdout.
	format := fs.String("format", "json", "output format: table|json")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *inputPath == "" {
		return fmt.Errorf("input is required")
	}
	if *dryRun && *apply {
		return fmt.Errorf("--dry-run and --apply are mutually exclusive")
	}
	if !*dryRun && !*apply {
		return fmt.Errorf("--dry-run or --apply is required")
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	data, err := readProjectImportInput(*inputPath, os.Stdin)
	if err != nil {
		return err
	}
	if *apply {
		result, err := c.services.ProjectService.ApplyLogicalProjectImport(ctx, data)
		if err != nil {
			return err
		}
		if *format == "table" {
			return c.writeProjectImportResultTable(result)
		}
		return writeJSON(c.stdoutWriter(), result)
	}
	result, err := c.services.ProjectService.ValidateLogicalProjectImport(ctx, data)
	if err != nil {
		return err
	}
	if *format == "table" {
		return c.writeProjectImportDryRunTable(result)
	}
	return writeJSON(c.stdoutWriter(), result)
}
func (c *CLI) writeProjectImportDryRunTable(result domain.LogicalProjectImportDryRun) error {
	var builder strings.Builder
	builder.WriteString("entity_type\tcount\n")
	builder.WriteString(fmt.Sprintf("project\t%d\n", result.Counts.Project))
	builder.WriteString(fmt.Sprintf("issues\t%d\n", result.Counts.Issues))
	builder.WriteString(fmt.Sprintf("labels\t%d\n", result.Counts.Labels))
	builder.WriteString(fmt.Sprintf("issue_labels\t%d\n", result.Counts.IssueLabels))
	builder.WriteString(fmt.Sprintf("relations\t%d\n", result.Counts.Relations))
	builder.WriteString(fmt.Sprintf("comments\t%d\n", result.Counts.Comments))
	builder.WriteString(fmt.Sprintf("decisions\t%d\n", result.Counts.Decisions))
	builder.WriteString(fmt.Sprintf("attempts\t%d\n", result.Counts.Attempts))
	builder.WriteString(fmt.Sprintf("attempt_notes\t%d\n", result.Counts.AttemptNotes))
	builder.WriteString(fmt.Sprintf("artifacts\t%d\n", result.Counts.Artifacts))
	builder.WriteString(fmt.Sprintf("events\t%d\n", result.Counts.Events))
	if result.Counts.ReviewTargets > 0 || result.Counts.ReviewRequests > 0 || result.Counts.ReviewOutcomes > 0 || result.Counts.ReviewEvents > 0 {
		builder.WriteString(fmt.Sprintf("review_targets\t%d\n", result.Counts.ReviewTargets))
		builder.WriteString(fmt.Sprintf("review_requests\t%d\n", result.Counts.ReviewRequests))
		builder.WriteString(fmt.Sprintf("review_outcomes\t%d\n", result.Counts.ReviewOutcomes))
		builder.WriteString(fmt.Sprintf("review_events\t%d\n", result.Counts.ReviewEvents))
	}
	if result.Counts.Reservations > 0 {
		builder.WriteString(fmt.Sprintf("reservations\t%d\n", result.Counts.Reservations))
	}
	builder.WriteString(fmt.Sprintf("writes\t%d\n", result.Writes.Count))
	if len(result.Conflicts) > 0 {
		builder.WriteString("\nconflicts:\n")
		builder.WriteString("code\tmessage\tfield\n")
		for _, conflict := range result.Conflicts {
			builder.WriteString(fmt.Sprintf("%s\t%s\t%s\n", conflict.Code, conflict.Message, conflict.Field))
		}
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func (c *CLI) writeProjectImportResultTable(result domain.LogicalProjectImportApplyResult) error {
	var builder strings.Builder
	builder.WriteString("entity_type\tcount\n")
	builder.WriteString(fmt.Sprintf("project\t%d\n", result.Counts.Project))
	builder.WriteString(fmt.Sprintf("issues\t%d\n", result.Counts.Issues))
	builder.WriteString(fmt.Sprintf("labels\t%d\n", result.Counts.Labels))
	builder.WriteString(fmt.Sprintf("issue_labels\t%d\n", result.Counts.IssueLabels))
	builder.WriteString(fmt.Sprintf("relations\t%d\n", result.Counts.Relations))
	builder.WriteString(fmt.Sprintf("comments\t%d\n", result.Counts.Comments))
	builder.WriteString(fmt.Sprintf("decisions\t%d\n", result.Counts.Decisions))
	builder.WriteString(fmt.Sprintf("attempts\t%d\n", result.Counts.Attempts))
	builder.WriteString(fmt.Sprintf("attempt_notes\t%d\n", result.Counts.AttemptNotes))
	builder.WriteString(fmt.Sprintf("artifacts\t%d\n", result.Counts.Artifacts))
	builder.WriteString(fmt.Sprintf("events\t%d\n", result.Counts.Events))
	if result.Counts.ReviewTargets > 0 || result.Counts.ReviewRequests > 0 || result.Counts.ReviewOutcomes > 0 || result.Counts.ReviewEvents > 0 {
		builder.WriteString(fmt.Sprintf("review_targets\t%d\n", result.Counts.ReviewTargets))
		builder.WriteString(fmt.Sprintf("review_requests\t%d\n", result.Counts.ReviewRequests))
		builder.WriteString(fmt.Sprintf("review_outcomes\t%d\n", result.Counts.ReviewOutcomes))
		builder.WriteString(fmt.Sprintf("review_events\t%d\n", result.Counts.ReviewEvents))
	}
	if result.Counts.Reservations > 0 {
		builder.WriteString(fmt.Sprintf("reservations\t%d\n", result.Counts.Reservations))
	}
	builder.WriteString(fmt.Sprintf("latest_event_id\t%d\n", result.LatestEventID))
	if len(result.Conflicts) > 0 {
		builder.WriteString("\nconflicts:\n")
		builder.WriteString("code\tmessage\tfield\n")
		for _, conflict := range result.Conflicts {
			builder.WriteString(fmt.Sprintf("%s\t%s\t%s\n", conflict.Code, conflict.Message, conflict.Field))
		}
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func readProjectImportInput(path string, stdin io.Reader) ([]byte, error) {
	const maxProjectImportBytes = 1 << 20
	if path == "-" {
		if stdin == nil {
			return nil, fmt.Errorf("stdin is not available")
		}
		var buf bytes.Buffer
		_, err := io.Copy(&buf, io.LimitReader(stdin, maxProjectImportBytes+1))
		if err != nil {
			return nil, err
		}
		if buf.Len() > maxProjectImportBytes {
			return nil, fmt.Errorf("input exceeds the maximum size of 1048576 bytes")
		}
		return buf.Bytes(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxProjectImportBytes {
		return nil, fmt.Errorf("input exceeds the maximum size of 1048576 bytes")
	}
	return data, nil
}

func (c *CLI) writeProjectExportFile(path string, data []byte, overwrite bool) error {
	if _, err := os.Stat(path); err == nil {
		if !overwrite {
			return fmt.Errorf("refusing to overwrite existing path %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	temp, err := os.CreateTemp(dir, ".rhizome-export-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	payload := append([]byte{}, data...)
	payload = append(payload, '\n')
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
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
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
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
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	projection, err := c.services.IssueService.GetIssueProjection(ctx, positionals[0])
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), IssueResponse{Issue: issueFromDomainProjection(projection)})
	}
	return c.writeIssueTable(projection.Issue)
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
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
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
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
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

func (c *CLI) runBoard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	output := fs.String("output", "", "write a fully self-contained HTML status board to this path")
	serve := fs.Bool("serve", false, "serve the board over loopback HTTP")
	httpAddress := fs.String("http-address", "127.0.0.1:0", "loopback HTTP address for served board mode")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	if *serve && *output != "" {
		return fmt.Errorf("--serve and --output are mutually exclusive")
	}
	if *serve {
		if c.boardServeHandler == nil {
			return fmt.Errorf("board serve handler is not configured")
		}
		return c.boardServeHandler(ctx, *httpAddress, c.stdoutWriter())
	}
	if c.services.BoardService == nil {
		return fmt.Errorf("board service is not configured")
	}

	result, err := c.services.BoardService.GetBoard(ctx)
	if err != nil {
		return err
	}
	if *format == "json" {
		if err := writeJSON(c.stdoutWriter(), boardResponseFromDomain(result)); err != nil {
			return err
		}
	} else if err := c.writeBoardTable(result); err != nil {
		return err
	}
	if *output != "" {
		html, err := renderBoardHTML(result)
		if err != nil {
			return fmt.Errorf("render board HTML: %w", err)
		}
		if err := os.WriteFile(*output, []byte(html), 0o644); err != nil {
			return fmt.Errorf("write board HTML: %w", err)
		}
	}
	return nil
}

func (c *CLI) runMaintenance(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return c.usageError()
	}
	if c.services.MaintenanceService == nil {
		return fmt.Errorf("maintenance service is not configured")
	}

	switch args[0] {
	case "release-attempt":
		return c.runMaintenanceReleaseAttempt(ctx, args[1:])
	case "rebuild-search-index":
		return c.runMaintenanceRebuildSearchIndex(ctx, args[1:])
	default:
		return c.usageError()
	}
}

func (c *CLI) runMaintenanceReleaseAttempt(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("maintenance release-attempt", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	result, err := c.services.MaintenanceService.ForceReleaseAttempt(ctx, positionals[0])
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), maintenanceReleaseAttemptResponseFromDomain(result))
	}
	return c.writeMaintenanceReleaseAttemptTable(result)
}

func (c *CLI) runMaintenanceRebuildSearchIndex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("maintenance rebuild-search-index", flag.ContinueOnError)
	format := fs.String("format", "table", "output format")
	positionals, err := c.parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return c.usageError()
	}
	if *format != "table" && *format != "json" {
		return NewUsageError(fmt.Sprintf("unsupported format %q", *format))
	}
	if err := c.services.MaintenanceService.RebuildSearchIndex(ctx); err != nil {
		return err
	}
	if *format == "json" {
		return writeJSON(c.stdoutWriter(), MaintenanceRebuildResponse{Rebuilt: true})
	}
	_, err = fmt.Fprintln(c.stdoutWriter(), "search index rebuilt")
	return err
}

func (c *CLI) writeMaintenanceReleaseAttemptTable(result application.ForceReleaseAttemptResult) error {
	var builder strings.Builder
	builder.WriteString("attempt_id\tstatus\tinterruption_reason\tfinished_at\tlatest_event_id\n")
	interruptionReason := ""
	if result.Attempt.InterruptionReasonCode != nil {
		interruptionReason = string(*result.Attempt.InterruptionReasonCode)
	}
	finishedAt := ""
	if result.Attempt.FinishedAt != nil {
		finishedAt = result.Attempt.FinishedAt.Format(time.RFC3339Nano)
	}
	builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%d\n", result.Attempt.ID, result.Attempt.Status, interruptionReason, finishedAt, result.LatestEventID))
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func (c *CLI) writeProjectInfoTable(project domain.Project, appVersion string) error {
	lines := []string{
		fmt.Sprintf("id\t%s", project.ID),
		fmt.Sprintf("name\t%s", formatOptionalString(project.Name)),
		fmt.Sprintf("next_issue_number\t%d", project.NextIssueNumber),
		fmt.Sprintf("schema_version\t%d", project.SchemaVersion),
	}
	if appVersion != "" {
		lines = append(lines, fmt.Sprintf("app_version\t%s", appVersion))
	}
	lines = append(lines, fmt.Sprintf("latest_event_id\t%d", project.LatestEventID))
	_, err := fmt.Fprintln(c.stdoutWriter(), strings.Join(lines, "\n"))
	return err
}

func (c *CLI) writeDoctorTable(report DoctorReport) error {
	var builder strings.Builder
	checkWidth := len("overall_health")
	for _, check := range report.Checks {
		if len(check.Check) > checkWidth {
			checkWidth = len(check.Check)
		}
	}
	statusWidth := len("healthy")
	builder.WriteString(fmt.Sprintf("%-*s  %-*s\n", checkWidth, "mode", statusWidth, strconv.FormatBool(report.Full)))
	if report.AppVersion != "" {
		builder.WriteString(fmt.Sprintf("%-*s  %s\n", checkWidth, "app_version", report.AppVersion))
	}
	builder.WriteString(fmt.Sprintf("%-*s  %s\n", checkWidth, "overall_health", colorizeDoctorStatus(padStatus(report.Healthy()), report.Healthy())))
	builder.WriteString(fmt.Sprintf("%-*s  %-*s  %s\n", checkWidth, "check", statusWidth, "healthy", "message"))
	for _, check := range report.Checks {
		builder.WriteString(fmt.Sprintf("%-*s  %s  %s\n", checkWidth, check.Check, colorizeDoctorStatus(padStatus(check.Healthy), check.Healthy), check.Message))
	}
	_, err := fmt.Fprint(c.stdoutWriter(), builder.String())
	return err
}

func padStatus(healthy bool) string {
	return fmt.Sprintf("%-*s", len("healthy"), strconv.FormatBool(healthy))
}

func colorizeDoctorStatus(value string, healthy bool) string {
	if healthy {
		return ansiGreen + value + ansiReset
	}
	return ansiRed + value + ansiReset
}

func (c *CLI) writeBackupTable(report BackupReport) error {
	lines := []string{
		fmt.Sprintf("output\t%s", report.OutputPath),
		fmt.Sprintf("schema_version\t%d", report.SchemaVersion),
		"validated\ttrue",
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

func (c *CLI) writeBoardTable(result domain.BoardResult) error {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("generated_at\t%s\n", result.GeneratedAt.Format(time.RFC3339Nano)))

	builder.WriteString("\nstatus_counts\n")
	builder.WriteString("effective_status\tcount\n")
	for _, count := range result.StatusCounts {
		builder.WriteString(fmt.Sprintf("%s\t%d\n", count.EffectiveStatus, count.Count))
	}

	builder.WriteString("\nactive_attempts\n")
	builder.WriteString("attempt_id\tissue\tkind\tsession_label\tlease_expires_at\n")
	for _, attempt := range result.ActiveAttempts {
		label := ""
		if attempt.SessionLabel != nil {
			label = *attempt.SessionLabel
		}
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
			attempt.AttemptID, attempt.IssueDisplayID, attempt.Kind, escapeTableValue(label), attempt.LeaseExpiresAt.Format(time.RFC3339Nano)))
	}

	reservationIssueDisplayIDByAttempt := make(map[string]string, len(result.ActiveAttempts))
	for _, attempt := range result.ActiveAttempts {
		reservationIssueDisplayIDByAttempt[attempt.AttemptID] = attempt.IssueDisplayID
	}
	builder.WriteString("\nactive_reservations\n")
	builder.WriteString("id\tissue\tattempt_id\tkind\tdisplay_value\n")
	for _, reservation := range result.ActiveReservations {
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
			reservation.ID, reservationIssueDisplayIDByAttempt[reservation.AttemptID], reservation.AttemptID, reservation.Kind, escapeTableValue(reservation.DisplayValue)))
	}

	builder.WriteString("\nblocked_issues\n")
	builder.WriteString("display_id\ttitle\tblocked_reason\n")
	for _, issue := range result.BlockedIssues {
		reason := ""
		if issue.BlockedReason != nil {
			reason = *issue.BlockedReason
		}
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\n", issue.DisplayID, escapeTableValue(issue.Title), escapeTableValue(reason)))
	}

	builder.WriteString("\nreview_requests\n")
	builder.WriteString("id\tissue_id\tstatus\tcreated_at\n")
	for _, request := range result.ReviewRequests {
		builder.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", request.ID, request.IssueID, request.Status, request.CreatedAt.Format(time.RFC3339Nano)))
	}

	builder.WriteString("\nplanning_graph\n")
	builder.WriteString(fmt.Sprintf("nodes\t%d\n", result.PlanningGraph.Summary.NodeCount))
	builder.WriteString(fmt.Sprintf("edges\t%d\n", result.PlanningGraph.Summary.EdgeCount))
	builder.WriteString(fmt.Sprintf("entry_points\t%d\n", result.PlanningGraph.Summary.EntryPointCount))
	builder.WriteString(fmt.Sprintf("blocking_nodes\t%d\n", result.PlanningGraph.Summary.BlockingNodeCount))
	if result.PlanningGraph.Truncated {
		builder.WriteString(fmt.Sprintf("truncated\ttrue\t(%d retained)\n", len(result.PlanningGraph.Nodes)))
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
	var builder strings.Builder
	builder.WriteString("Usage:\n")
	for _, candidate := range commands() {
		for _, line := range candidate.usageLines {
			builder.WriteString("  rhizome-mcp [--data-root PATH] ")
			builder.WriteString(line)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func failedCheckCount(report DoctorReport) int {
	failed := 0
	for _, check := range report.Checks {
		if !check.Healthy {
			failed++
		}
	}
	return failed
}

func (c *CLI) usageError() error {
	fmt.Fprint(c.stderrWriter(), c.usage())
	return NewUsageError("usage error")
}

type BackupResponse struct {
	Output        string `json:"output"`
	SchemaVersion int    `json:"schema_version"`
	Validated     bool   `json:"validated"`
}

type MaintenanceReleaseAttemptResponse struct {
	Attempt       domain.WorkAttempt `json:"attempt"`
	LatestEventID int64              `json:"latest_event_id"`
}

func maintenanceReleaseAttemptResponseFromDomain(result application.ForceReleaseAttemptResult) MaintenanceReleaseAttemptResponse {
	return MaintenanceReleaseAttemptResponse{Attempt: result.Attempt, LatestEventID: result.LatestEventID}
}

type MaintenanceRebuildResponse struct {
	Rebuilt bool `json:"rebuilt"`
}

// InitResponse is the JSON payload emitted by the init command.
type InitResponse struct {
	Root         string   `json:"root"`
	ProjectID    string   `json:"project_id"`
	DatabasePath string   `json:"database_path"`
	NextActions  []string `json:"next_actions,omitempty"`
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
	AppVersion      string    `json:"app_version,omitempty"`
	LatestEventID   int64     `json:"latest_event_id"`
}

func projectInfoFromDomain(project domain.Project, appVersion string) ProjectInfo {
	return ProjectInfo{
		ID:              project.ID,
		Name:            copyOptionalString(project.Name),
		Instructions:    copyOptionalString(project.Instructions),
		NextIssueNumber: project.NextIssueNumber,
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
		SchemaVersion:   project.SchemaVersion,
		AppVersion:      appVersion,
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
	ParentIssueID          *string    `json:"parent_issue_id,omitempty"`
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
		ParentIssueID:       copyOptionalString(issue.ParentID),
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

// BoardResponse is a stable CLI projection of the board's aggregate status
// summary.
type BoardResponse struct {
	GeneratedAt        time.Time            `json:"generated_at"`
	StatusCounts       []BoardStatusCount   `json:"status_counts"`
	ActiveAttempts     []BoardActiveAttempt `json:"active_attempts"`
	AttemptGates       []BoardAttemptGates  `json:"attempt_gates"`
	ActiveReservations []BoardReservation   `json:"active_reservations"`
	BlockedIssues      []IssueSummary       `json:"blocked_issues"`
	ReviewRequests     []BoardReviewRequest `json:"review_requests"`
	PlanningGraph      BoardGraph           `json:"planning_graph"`
}

// BoardStatusCount is one bounded aggregate count of issues in a single
// effective status.
type BoardStatusCount struct {
	EffectiveStatus string `json:"effective_status"`
	Count           int64  `json:"count"`
}

// BoardActiveAttempt is a stable CLI projection of one currently leased attempt.
type BoardActiveAttempt struct {
	AttemptID      string    `json:"attempt_id"`
	IssueID        string    `json:"issue_id"`
	IssueDisplayID string    `json:"issue_display_id"`
	IssueTitle     string    `json:"issue_title"`
	Kind           string    `json:"kind"`
	SessionID      *string   `json:"session_id,omitempty"`
	SessionLabel   *string   `json:"session_label,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

// BoardAttemptGates is a stable CLI projection of one active attempt's
// workflow-gate progress (ISSUE-175 AC2): the enforcement point the attempt
// holder will hit, the frozen snapshot fingerprint when one supplied the
// requirements, progress counts, and each unmet requirement with the same
// key and reason the WORKFLOW_GATE_UNSATISFIED error reports. One row per
// active attempt, joined to active_attempts by attempt_id. Next actions are
// deliberately not projected here; they live on the issue-detail page and in
// get_work_context.
type BoardAttemptGates struct {
	AttemptID           string                  `json:"attempt_id"`
	IssueID             string                  `json:"issue_id"`
	IssueDisplayID      string                  `json:"issue_display_id"`
	EnforcementPoint    string                  `json:"enforcement_point"`
	SnapshotFingerprint *string                 `json:"snapshot_fingerprint,omitempty"`
	RequirementCount    int64                   `json:"requirement_count"`
	SatisfiedCount      int64                   `json:"satisfied_count"`
	Unmet               []BoardUnmetRequirement `json:"unmet"`
}

// BoardUnmetRequirement is one failing gate requirement on a board row.
type BoardUnmetRequirement struct {
	PolicyID       string `json:"policy_id"`
	RequirementKey string `json:"requirement_key"`
	Reason         string `json:"reason"`
}

// BoardReservation is a stable CLI projection of one active resource
// reservation. Never carries a lease token; session label/lease-expiry are
// read from the matching BoardActiveAttempt (grouped by AttemptID) instead
// of duplicating that projection here (ISSUE-181). IssueDisplayID is looked
// up from that same matching attempt so this block can be read (and its
// issue column correlated with the active_attempts block) without a
// separate issue lookup, matching every other board collection's use of
// the human-readable ISSUE-N form over the raw internal ID.
type BoardReservation struct {
	ID             string `json:"id"`
	IssueID        string `json:"issue_id"`
	IssueDisplayID string `json:"issue_display_id"`
	AttemptID      string `json:"attempt_id"`
	Kind           string `json:"kind"`
	DisplayValue   string `json:"display_value"`
}

// BoardReviewRequest is a stable CLI projection of one open review request.
type BoardReviewRequest struct {
	ID                 string    `json:"id"`
	IssueID            string    `json:"issue_id"`
	Status             string    `json:"status"`
	TargetIssueVersion int64     `json:"target_issue_version"`
	CreatedAt          time.Time `json:"created_at"`
}

// BoardGraph is a stable CLI projection of the board's planning graph.
type BoardGraph struct {
	Nodes             []IssueSummary      `json:"nodes"`
	Edges             []domain.GraphEdge  `json:"edges"`
	EntryPoints       []string            `json:"entry_points"`
	BlockingNodes     []string            `json:"blocking_nodes"`
	Summary           domain.GraphSummary `json:"summary"`
	Truncated         bool                `json:"truncated"`
	RetainedNodeCount int                 `json:"retained_node_count"`
}

func boardResponseFromDomain(result domain.BoardResult) BoardResponse {
	counts := make([]BoardStatusCount, len(result.StatusCounts))
	for index, item := range result.StatusCounts {
		counts[index] = BoardStatusCount{EffectiveStatus: string(item.EffectiveStatus), Count: item.Count}
	}
	attempts := make([]BoardActiveAttempt, len(result.ActiveAttempts))
	for index, item := range result.ActiveAttempts {
		attempts[index] = BoardActiveAttempt{
			AttemptID: item.AttemptID, IssueID: item.IssueID, IssueDisplayID: item.IssueDisplayID, IssueTitle: item.IssueTitle,
			Kind: string(item.Kind), SessionID: copyOptionalString(item.SessionID), SessionLabel: copyOptionalString(item.SessionLabel),
			StartedAt: item.StartedAt.UTC(), LeaseExpiresAt: item.LeaseExpiresAt.UTC(),
		}
	}
	issueDisplayIDByAttempt := make(map[string]string, len(result.ActiveAttempts))
	for _, attempt := range result.ActiveAttempts {
		issueDisplayIDByAttempt[attempt.AttemptID] = attempt.IssueDisplayID
	}
	attemptGates := boardAttemptGatesFromDomain(result.AttemptGates)
	reservations := make([]BoardReservation, len(result.ActiveReservations))
	for index, item := range result.ActiveReservations {
		reservations[index] = BoardReservation{
			ID: item.ID, IssueID: item.IssueID, IssueDisplayID: issueDisplayIDByAttempt[item.AttemptID],
			AttemptID: item.AttemptID, Kind: string(item.Kind), DisplayValue: item.DisplayValue,
		}
	}
	blocked := make([]IssueSummary, len(result.BlockedIssues))
	for index, item := range result.BlockedIssues {
		blocked[index] = issueFromDomainProjection(item)
	}
	reviews := make([]BoardReviewRequest, len(result.ReviewRequests))
	for index, item := range result.ReviewRequests {
		reviews[index] = BoardReviewRequest{
			ID: item.ID, IssueID: item.IssueID, Status: string(item.Status),
			TargetIssueVersion: item.TargetIssueVersion, CreatedAt: item.CreatedAt.UTC(),
		}
	}
	nodes := make([]IssueSummary, len(result.PlanningGraph.Nodes))
	for index, item := range result.PlanningGraph.Nodes {
		nodes[index] = issueFromDomainProjection(item)
	}
	return BoardResponse{
		GeneratedAt:        result.GeneratedAt.UTC(),
		StatusCounts:       counts,
		ActiveAttempts:     attempts,
		AttemptGates:       attemptGates,
		ActiveReservations: reservations,
		BlockedIssues:      blocked,
		ReviewRequests:     reviews,
		PlanningGraph: BoardGraph{
			Nodes: nodes, Edges: result.PlanningGraph.Edges, EntryPoints: result.PlanningGraph.EntryPoints,
			BlockingNodes: result.PlanningGraph.BlockingNodes, Summary: result.PlanningGraph.Summary, Truncated: result.PlanningGraph.Truncated,
			RetainedNodeCount: len(result.PlanningGraph.Nodes),
		},
	}
}

// boardAttemptGatesFromDomain projects attempt gate-progress rows for the
// CLI JSON, the served board API, and the board's semantic ETag payload.
func boardAttemptGatesFromDomain(rows []domain.AttemptGateProgress) []BoardAttemptGates {
	attemptGates := make([]BoardAttemptGates, len(rows))
	for index, item := range rows {
		unmet := make([]BoardUnmetRequirement, len(item.Gates.Unmet))
		for unmetIndex, requirement := range item.Gates.Unmet {
			unmet[unmetIndex] = BoardUnmetRequirement{
				PolicyID: requirement.PolicyID, RequirementKey: requirement.RequirementKey, Reason: requirement.Reason,
			}
		}
		attemptGates[index] = BoardAttemptGates{
			AttemptID: item.AttemptID, IssueID: item.IssueID, IssueDisplayID: item.IssueDisplayID,
			EnforcementPoint:    string(item.Gates.Point),
			SnapshotFingerprint: copyOptionalString(item.Gates.SnapshotFingerprint),
			RequirementCount:    item.Gates.RequirementCount,
			SatisfiedCount:      item.Gates.SatisfiedCount,
			Unmet:               unmet,
		}
	}
	return attemptGates
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
