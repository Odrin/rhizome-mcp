package tools

import (
	"context"
	"testing"

	"rhizome-mcp/store"
)

func TestAddTaskHandler(t *testing.T) {
	store.Default().Reset()

	_, out, err := AddTaskHandler(context.Background(), nil, AddTaskInput{
		Title:    "Write MCP docs",
		Priority: "high",
		DueDate:  "2026-07-31",
	})
	if err != nil {
		t.Fatalf("AddTaskHandler returned error: %v", err)
	}

	if out.Task.ID != 1 {
		t.Fatalf("expected task ID 1, got %d", out.Task.ID)
	}
	if out.Task.Status != store.StatusPending {
		t.Fatalf("expected pending status, got %s", out.Task.Status)
	}
}

func TestCompleteTaskHandler(t *testing.T) {
	store.Default().Reset()

	_, created, err := AddTaskHandler(context.Background(), nil, AddTaskInput{Title: "Ship server"})
	if err != nil {
		t.Fatalf("setup AddTaskHandler returned error: %v", err)
	}

	_, out, err := CompleteTaskHandler(context.Background(), nil, CompleteTaskInput{ID: created.Task.ID})
	if err != nil {
		t.Fatalf("CompleteTaskHandler returned error: %v", err)
	}

	if out.Task.Status != store.StatusCompleted {
		t.Fatalf("expected completed status, got %s", out.Task.Status)
	}
}

func TestListTasksHandlerWithFilter(t *testing.T) {
	store.Default().Reset()

	_, first, err := AddTaskHandler(context.Background(), nil, AddTaskInput{Title: "A"})
	if err != nil {
		t.Fatalf("setup first AddTaskHandler returned error: %v", err)
	}
	if _, _, err := AddTaskHandler(context.Background(), nil, AddTaskInput{Title: "B"}); err != nil {
		t.Fatalf("setup second AddTaskHandler returned error: %v", err)
	}
	if _, _, err := CompleteTaskHandler(context.Background(), nil, CompleteTaskInput{ID: first.Task.ID}); err != nil {
		t.Fatalf("setup CompleteTaskHandler returned error: %v", err)
	}

	_, out, err := ListTasksHandler(context.Background(), nil, ListTasksInput{Status: "pending"})
	if err != nil {
		t.Fatalf("ListTasksHandler returned error: %v", err)
	}

	if out.Count != 1 {
		t.Fatalf("expected 1 pending task, got %d", out.Count)
	}
	if out.Tasks[0].Title != "B" {
		t.Fatalf("expected task B, got %s", out.Tasks[0].Title)
	}
}
