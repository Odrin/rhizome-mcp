# rhizome-mcp

A Model Context Protocol (MCP) server in Go for lightweight task tracking.

## Features

- Type-safe MCP tools using the official Go SDK
- In-memory task store with validation and thread safety
- Resource endpoint for task overview data
- Graceful shutdown with signal handling
- Unit tests for core tool handlers

## Requirements

- Go 1.23+

## Installation

```bash
go mod tidy
go build -o rhizome-mcp
```

## Usage

Run with stdio transport (default for MCP integrations):

```bash
./rhizome-mcp
```

## Configuration

Environment variables:

- `SERVER_NAME`: MCP implementation name (default: `rhizome-mcp`)
- `VERSION`: MCP implementation version (default: `v1.0.0`)
- `LOG_LEVEL`: `debug`, `info`, `warn`, or `error` (default: `info`)

## Available Tools

### add_task
Create a new task.

Input:
- `title` (string, required)
- `priority` (string, optional: `low`, `medium`, `high`)
- `dueDate` (string, optional: `YYYY-MM-DD`)

Output:
- `task` (object)
- `message` (string)

### list_tasks
List tasks with optional status filtering.

Input:
- `status` (string, optional: `pending`, `completed`, `all`)

Output:
- `tasks` (array)
- `count` (number)
- `message` (string)

### complete_task
Mark a task as completed.

Input:
- `id` (number, required)

Output:
- `task` (object)
- `message` (string)

## Available Resources

### tasks://overview
Returns JSON with task statistics and the current list of pending tasks.

## Development

Run tests:

```bash
go test ./...
```

Run static checks:

```bash
go vet ./...
```
