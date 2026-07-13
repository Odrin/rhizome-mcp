package store

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusCompleted Status = "completed"
)

type Task struct {
	ID          int        `json:"id" jsonschema:"description=Task identifier"`
	Title       string     `json:"title" jsonschema:"description=Task title"`
	Priority    string     `json:"priority" jsonschema:"description=Task priority: low, medium, or high"`
	Status      Status     `json:"status" jsonschema:"description=Task status"`
	DueDate     string     `json:"dueDate,omitempty" jsonschema:"description=Optional due date in YYYY-MM-DD format"`
	CreatedAt   time.Time  `json:"createdAt" jsonschema:"description=Task creation timestamp"`
	CompletedAt *time.Time `json:"completedAt,omitempty" jsonschema:"description=Task completion timestamp"`
}

type Stats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Completed int `json:"completed"`
}

type Store struct {
	mu     sync.RWMutex
	nextID int
	tasks  []Task
}

func New() *Store {
	return &Store{
		nextID: 1,
		tasks:  make([]Task, 0, 16),
	}
}

func (s *Store) Add(title, priority, dueDate string) (Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Task{}, fmt.Errorf("title is required")
	}

	priority = normalizePriority(priority)
	if !isValidPriority(priority) {
		return Task{}, fmt.Errorf("invalid priority %q; expected low, medium, or high", priority)
	}

	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			return Task{}, fmt.Errorf("invalid dueDate %q; expected YYYY-MM-DD", dueDate)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task := Task{
		ID:        s.nextID,
		Title:     title,
		Priority:  priority,
		Status:    StatusPending,
		DueDate:   dueDate,
		CreatedAt: time.Now().UTC(),
	}
	s.nextID++
	s.tasks = append(s.tasks, task)

	return task, nil
}

func (s *Store) List(statusFilter string) []Task {
	statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))

	s.mu.RLock()
	defer s.mu.RUnlock()

	if statusFilter == "" || statusFilter == "all" {
		return append([]Task(nil), s.tasks...)
	}

	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		if string(t.Status) == statusFilter {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) Complete(id int) (Task, error) {
	if id <= 0 {
		return Task{}, fmt.Errorf("id must be greater than zero")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.tasks {
		if s.tasks[i].ID != id {
			continue
		}
		if s.tasks[i].Status == StatusCompleted {
			return Task{}, fmt.Errorf("task %d is already completed", id)
		}
		now := time.Now().UTC()
		s.tasks[i].Status = StatusCompleted
		s.tasks[i].CompletedAt = &now
		return s.tasks[i], nil
	}

	return Task{}, fmt.Errorf("task %d not found", id)
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := Stats{Total: len(s.tasks)}
	for _, t := range s.tasks {
		if t.Status == StatusCompleted {
			st.Completed++
		} else {
			st.Pending++
		}
	}
	return st
}

func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID = 1
	s.tasks = s.tasks[:0]
}

func normalizePriority(priority string) string {
	priority = strings.ToLower(strings.TrimSpace(priority))
	if priority == "" {
		return "medium"
	}
	return priority
}

func isValidPriority(priority string) bool {
	switch priority {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

var defaultStore = New()

func Default() *Store {
	return defaultStore
}
