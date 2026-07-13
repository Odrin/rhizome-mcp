# Product goals and scope

## 1. Purpose

`rhizome-mcp` is a task tracking and coordination server designed primarily for AI coding agents.

It provides a shared project memory and execution model that allows different agents to work on the same repository sequentially or concurrently without relying on a single chat context or a pair of mutable Markdown files.

The system must support environments where work can move between GitHub Copilot, Codex, Claude Code, Antigravity or other MCP-compatible clients.

## 2. Product goals

The first version must:

- let agents create, update, relate and search project issues;
- represent epics, tasks and bugs;
- expose dependency and planning graphs;
- let agents atomically claim work;
- recover automatically when an agent process disappears;
- preserve checkpoints, decisions and execution history;
- minimize repeated context loading and token usage;
- support multiple independent agent clients without permanent agent registration;
- run locally as a native binary;
- use a project-local identity but project-external database;
- remain simple enough for a small Go codebase and SQLite deployment.

## 3. Main user

The primary user is an AI agent connected through MCP.

A human developer is a secondary user and interacts through:

- the agent;
- a minimal CLI;
- JSON, table, Markdown or Mermaid output;
- backup and diagnostic commands.

A graphical user interface is not required for the first version.

## 4. Local-first deployment

The expected setup is:

```text
repository/
  .agent-tracker.json
  source files...

application data directory/
  projects/
    <project-id>/
      tasks.db
```

The repository configuration:

```json
{
  "version": 1,
  "project_id": "01J..."
}
```

The database is not committed to Git.

## 5. Technical baseline

- Go
- SQLite
- preferred SQLite driver: `modernc.org/sqlite`
- pure native binary
- primary MCP transport: `stdio`
- optional future transport: local HTTP
- embedded and automatic migrations
- application-layer services shared by MCP and CLI
- SQLite FTS5 for lexical search

## 6. Core terminology

### Project

A repository-associated workspace with one SQLite database.

### Issue

The primary unit of planning and work. Types:

- `epic`
- `task`
- `bug`

### Work attempt

One leased execution of an issue by an agent session.

### Agent session

A temporary record of one MCP client connection. It is not a permanent agent identity.

### Decision

A durable project or issue-level technical/product decision.

### Attempt note

An operational note written during a work attempt. Checkpoints are a special kind of attempt note.

### Relation

A semantic connection between two issues:

- `blocks`
- `related_to`
- `duplicates`

### Artifact

A link to a file, commit, branch, pull request, URL or other result. Binary data is not stored.

## 7. Autonomy principles

Agents may autonomously:

- create and decompose work;
- create epics, tasks and bugs;
- add labels and relations;
- move issues into `ready`;
- claim work;
- block work with a reason;
- record decisions;
- save checkpoints;
- complete work;
- request review;
- review work;
- reopen completed work.

Review is optional.

The system must not require a permanent human assignee or a permanent agent identity.

## 8. Token-efficiency principles

The MCP contract must follow these rules:

- list operations return compact projections;
- graph nodes are compact by default;
- long related content is opt-in;
- search returns snippets, not full documents;
- pagination is cursor-based;
- graph depth and node count are bounded;
- large results explicitly report truncation;
- structured results are not duplicated as full text;
- a dedicated work-context tool returns a bounded context package;
- delta queries return changes after an event ID;
- previous work is summarized through checkpoints and result summaries.

## 9. Non-goals for the first version

The first version does not include:

- web UI or desktop UI;
- authentication or user accounts;
- multi-user permissions;
- permanent agent identities;
- permanent assignees;
- custom issue statuses or workflows;
- nested epics;
- multiple assignees;
- estimates, milestones, due dates or manual rank;
- binary file attachments;
- vector or semantic search;
- automatic deletion of historical data;
- networked multi-node deployment;
- PostgreSQL support;
- Docker as a requirement.
