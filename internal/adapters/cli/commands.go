package cli

import "context"

// commandTable is the single source of truth for CLI dispatch. Before it
// existed, three places had to agree by hand and did not: the switch in Run,
// the usage string, and a literal command list in package main that decided
// which commands needed an open project (ISSUE-207). Adding a command now
// means adding one row here.
type command struct {
	// name is the first positional argument that selects this command.
	name string
	// needsProject reports whether the command needs an open project bundle
	// composed before it runs. Package main asks through NeedsProject.
	needsProject bool
	// usageLines are the usage lines for this command, rendered in table
	// order. Every FlagSet-registered flag must appear in one of them.
	usageLines []string
	// run dispatches the command with its remaining arguments.
	run func(*CLI, context.Context, []string) error
}

// commands returns the dispatch table. It is a function rather than a package
// variable because the table's handlers reach usage(), which reads the table:
// as a var that is an initialization cycle.
func commands() []command {
	return []command{
		{
			name:         "init",
			needsProject: false,
			usageLines:   []string{"init"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runInit(ctx, args) },
		},
		{
			name:         "serve",
			needsProject: false,
			usageLines:   []string{"serve [--http-address ADDR] [--profile full|agent|read-only|migration] [--toolsets GROUP[,GROUP...]] [--project-root PATH]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runServe(ctx, args) },
		},
		{
			name:         "connect",
			needsProject: false,
			usageLines:   []string{"connect TARGET [--print] [--command]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runConnect(ctx, args) },
		},
		{
			name:         "backup",
			needsProject: true,
			usageLines:   []string{"backup --output PATH [--format table|json]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runBackup(ctx, args) },
		},
		{
			name:         "doctor",
			needsProject: true,
			usageLines:   []string{"doctor [--full] [--format table|json]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runDoctor(ctx, args) },
		},
		{
			name:         "project",
			needsProject: true,
			usageLines: []string{
				"project info [--format table|json]",
				"project export --output PATH|- [--overwrite]",
				"project import --input PATH|- [--dry-run|--apply] [--format table|json]",
			},
			run: func(c *CLI, ctx context.Context, args []string) error { return c.runProject(ctx, args) },
		},
		{
			name:         "issue",
			needsProject: true,
			usageLines: []string{
				"issue list [--format table|json] [--limit N] [--cursor CURSOR] [--type TYPE ...] [--status STATUS ...] [--effective-status STATUS ...] [--priority PRIORITY ...] [--include-archived]",
				"issue show ISSUE-ID [--format table|json]",
			},
			run: func(c *CLI, ctx context.Context, args []string) error { return c.runIssue(ctx, args) },
		},
		{
			name:         "search",
			needsProject: true,
			usageLines:   []string{"search QUERY [--format table|json] [--limit N] [--cursor CURSOR] [--entity-type TYPE ...] [--issue ISSUE-ID] [--epic EPIC-ID] [--status STATUS ...] [--label LABEL ...] [--include-archived] [--snippet-length N]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runSearch(ctx, args) },
		},
		{
			name:         "graph",
			needsProject: true,
			usageLines:   []string{"graph ISSUE-ID [--format table|json|mermaid] [--depth N] [--max-nodes N] [--direction outgoing|incoming|both] [--relation-type TYPE ...] [--include-hierarchy] [--include-terminal]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runGraph(ctx, args) },
		},
		{
			name:         "board",
			needsProject: true,
			usageLines:   []string{"board [--format table|json] [--output PATH] [--serve [--http-address ADDR]]"},
			run:          func(c *CLI, ctx context.Context, args []string) error { return c.runBoard(ctx, args) },
		},
		{
			name:         "maintenance",
			needsProject: true,
			usageLines: []string{
				"maintenance release-attempt ATTEMPT-ID [--format table|json]",
				"maintenance rebuild-search-index [--format table|json]",
			},
			run: func(c *CLI, ctx context.Context, args []string) error { return c.runMaintenance(ctx, args) },
		},
	}
}

func lookupCommand(name string) (command, bool) {
	for _, candidate := range commands() {
		if candidate.name == name {
			return candidate, true
		}
	}
	return command{}, false
}

// NeedsProject reports whether the command named by args requires an open
// project. Package main calls this before composing the service bundle, so the
// decision lives with the command definition instead of in a parallel list
// that has to be kept in sync by hand.
func NeedsProject(args []string) bool {
	if len(args) == 0 {
		return false
	}
	found, ok := lookupCommand(args[0])
	return ok && found.needsProject
}

// CommandNames returns every dispatchable command name in table order, for
// tests and documentation generation.
func CommandNames() []string {
	table := commands()
	names := make([]string, len(table))
	for index, candidate := range table {
		names[index] = candidate.name
	}
	return names
}
