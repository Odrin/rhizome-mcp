package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
	projectruntime "rhizome-mcp/internal/runtime"
)

var projectRouterMaxEntries = 16

var errProjectRouterClosed = errors.New("project router is closed")

type projectRouter struct {
	dataRoot      string
	clock         clock.Clock
	sqliteOptions sqlite.Options
	composeFn     func(context.Context, string, string, clock.Clock, sqlite.Options) (*composedServices, *projectruntime.Project, error)

	defaultBundle *composedServices
	defaultRef    string
	defaultRoot   string

	mu         sync.Mutex
	cond       *sync.Cond
	entries    map[string]*projectRouterEntry
	closed     bool
	closing    bool
	closingErr error
	closeOnce  sync.Once

	openingCount int
	activeCount  int
}

type projectRouterEntry struct {
	ref      string
	pinned   bool
	state    string
	bundle   *composedServices
	active   int
	lastUsed int64
	done     chan struct{}
	result   projectRouterResult
	removed  bool
}

type projectRouterResult struct {
	bundle *composedServices
	err    error
}

func newProjectRouter(dataRoot string, source clock.Clock, sqliteOptions sqlite.Options, defaultBundle *composedServices) *projectRouter {
	if source == nil {
		source = clock.RealClock{}
	}
	router := &projectRouter{
		dataRoot:      dataRoot,
		clock:         source,
		sqliteOptions: sqliteOptions,
		entries:       make(map[string]*projectRouterEntry),
		composeFn:     composeServicesFromExistingProject,
	}
	router.cond = sync.NewCond(&router.mu)
	if defaultBundle != nil && defaultBundle.ProjectRef() != "" {
		router.defaultBundle = defaultBundle
		router.defaultRef = defaultBundle.ProjectRef()
		if defaultBundle.project != nil {
			router.defaultRoot = defaultBundle.project.Root
		}
		router.entries[router.defaultRef] = &projectRouterEntry{
			ref:      router.defaultRef,
			pinned:   true,
			state:    "ready",
			bundle:   defaultBundle,
			lastUsed: time.Now().UnixNano(),
		}
	}
	return router
}

func (router *projectRouter) Acquire(ctx context.Context, explicitRef *string) (mcpadapter.ProjectLease, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return nil, errProjectRouterClosed
	}

	ref, err := router.resolveRef(explicitRef)
	if err != nil {
		router.mu.Unlock()
		return nil, err
	}
	if ref == "" {
		router.mu.Unlock()
		return nil, mcpadapter.NewProjectRequiredError()
	}

	if entry, ok := router.entries[ref]; ok {
		if entry.state == "opening" {
			entry.active++
			entry.lastUsed = time.Now().UnixNano()
			router.activeCount++
			router.mu.Unlock()
			<-entry.done
			router.mu.Lock()
			defer router.mu.Unlock()
			if entry.result.err != nil {
				router.releaseEntryLocked(entry)
				return nil, entry.result.err
			}
			if entry.result.bundle == nil {
				router.releaseEntryLocked(entry)
				return nil, errProjectRouterClosed
			}
			return router.wrapLease(entry), nil
		}
		if entry.state == "ready" {
			entry.active++
			entry.lastUsed = time.Now().UnixNano()
			router.activeCount++
			router.mu.Unlock()
			return router.wrapLease(entry), nil
		}
	}

	var evicted *projectRouterEntry
	if len(router.entries) >= projectRouterMaxEntries {
		if evicted = router.evictIdleEntryLocked(); evicted == nil {
			router.mu.Unlock()
			return nil, mcpadapter.NewProjectCapacityExceededError()
		}
	}

	entry := &projectRouterEntry{ref: ref, state: "opening", done: make(chan struct{}), lastUsed: time.Now().UnixNano()}
	router.entries[ref] = entry
	router.openingCount++
	router.mu.Unlock()
	if evicted != nil && evicted.bundle != nil {
		_ = evicted.bundle.Close(context.Background())
	}

	go func() {
		result := router.openEntry(ctx, entry)
		router.mu.Lock()
		defer router.mu.Unlock()
		if entry.removed {
			router.cond.Broadcast()
			return
		}
		entry.result = result
		if result.err != nil {
			delete(router.entries, ref)
			entry.removed = true
			router.openingCount--
			close(entry.done)
			router.cond.Broadcast()
			return
		}
		entry.state = "ready"
		entry.bundle = result.bundle
		entry.lastUsed = time.Now().UnixNano()
		router.openingCount--
		close(entry.done)
		router.cond.Broadcast()
	}()

	<-entry.done
	router.mu.Lock()
	defer router.mu.Unlock()
	if entry.result.err != nil {
		return nil, entry.result.err
	}
	if entry.result.bundle == nil {
		return nil, errProjectRouterClosed
	}
	entry.active++
	entry.lastUsed = time.Now().UnixNano()
	router.activeCount++
	return router.wrapLease(entry), nil
}

func (router *projectRouter) OpenProject(ctx context.Context, absoluteRoot string) (mcpadapter.ProjectLease, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if absoluteRoot == "" || !filepath.IsAbs(absoluteRoot) {
		return nil, projectRootArgumentError(absoluteRoot)
	}
	project, err := projectconfig.LoadProjectRoot(absoluteRoot)
	if err != nil {
		return nil, err
	}
	projectRef := project.Identity.ProjectID
	return router.Acquire(ctx, &projectRef)
}

func (router *projectRouter) resolveRef(explicitRef *string) (string, error) {
	if explicitRef != nil {
		if err := mcpadapter.ValidateProjectRef(*explicitRef); err != nil {
			return "", err
		}
		return *explicitRef, nil
	}
	if router.defaultRef != "" {
		return router.defaultRef, nil
	}
	return "", mcpadapter.NewProjectRequiredError()
}

func (router *projectRouter) openEntry(ctx context.Context, entry *projectRouterEntry) projectRouterResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if entry == nil {
		return projectRouterResult{err: fmt.Errorf("project router entry is nil")}
	}
	if router.composeFn == nil {
		return projectRouterResult{err: fmt.Errorf("project router composition function is nil")}
	}
	bundle, project, err := router.composeFn(ctx, entry.ref, router.dataRoot, router.clock, router.sqliteOptions)
	if err != nil {
		return projectRouterResult{err: err}
	}
	if bundle == nil || project == nil {
		return projectRouterResult{err: fmt.Errorf("project bundle for %q was nil", entry.ref)}
	}
	entry.bundle = bundle
	return projectRouterResult{bundle: bundle}
}

func (router *projectRouter) wrapLease(entry *projectRouterEntry) mcpadapter.ProjectLease {
	if entry == nil || entry.bundle == nil {
		return nil
	}
	baseLease := mcpadapter.NewStaticLease(entry.ref, entry.bundle.ProjectServices())
	return &projectRouterLease{baseLease: baseLease, router: router, entry: entry}
}

func (router *projectRouter) evictIdleEntryLocked() *projectRouterEntry {
	var victim *projectRouterEntry
	var victimKey string
	for key, entry := range router.entries {
		if entry == nil || entry.pinned || entry.state != "ready" || entry.removed || entry.active != 0 {
			continue
		}
		if victim == nil || entry.lastUsed < victim.lastUsed {
			victim = entry
			victimKey = key
		}
	}
	if victim == nil {
		return nil
	}
	delete(router.entries, victimKey)
	victim.removed = true
	return victim
}

func (router *projectRouter) Close(ctx context.Context) error {
	router.closeOnce.Do(func() {
		router.mu.Lock()
		router.closed = true
		router.closing = true
		router.mu.Unlock()

		deadline := time.Now().Add(5 * time.Second)
		if ctx != nil {
			if _, ok := ctx.Deadline(); !ok {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
			}
			if dl, ok := ctx.Deadline(); ok {
				deadline = dl
			}
		}
		for time.Now().Before(deadline) {
			router.mu.Lock()
			opening := router.openingCount
			active := router.activeCount
			router.mu.Unlock()
			if opening == 0 && active == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		router.mu.Lock()
		if router.openingCount != 0 || router.activeCount != 0 {
			router.closingErr = context.DeadlineExceeded
			router.mu.Unlock()
			return
		}
		remaining := make([]*projectRouterEntry, 0, len(router.entries))
		for _, entry := range router.entries {
			if entry != nil {
				remaining = append(remaining, entry)
			}
		}
		router.entries = make(map[string]*projectRouterEntry)
		router.mu.Unlock()

		var errs []error
		for _, entry := range remaining {
			if entry == nil || entry.bundle == nil {
				continue
			}
			if err := entry.bundle.Close(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		router.mu.Lock()
		router.closingErr = errors.Join(errs...)
		router.mu.Unlock()
	})
	return router.closingErr
}

// ExpireAttempts sweeps each currently ready project without retaining router
// locks while project-local expiry performs storage work.
func (router *projectRouter) ExpireAttempts(ctx context.Context) (ports.ExpireAttemptsResult, error) {
	if router == nil {
		return ports.ExpireAttemptsResult{}, nil
	}
	router.mu.Lock()
	refs := make([]string, 0, len(router.entries))
	for ref, entry := range router.entries {
		if entry != nil && entry.state == "ready" && !entry.removed {
			refs = append(refs, ref)
		}
	}
	router.mu.Unlock()

	var result ports.ExpireAttemptsResult
	var errs []error
	for _, ref := range refs {
		lease, err := router.Acquire(ctx, &ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		projectResult, projectErr := lease.AttemptService().ExpireAttempts(ctx)
		releaseErr := lease.Release()
		result.ExpiredAttemptCount += projectResult.ExpiredAttemptCount
		if projectErr != nil {
			errs = append(errs, projectErr)
		}
		if releaseErr != nil {
			errs = append(errs, releaseErr)
		}
	}
	return result, errors.Join(errs...)
}

type projectRouterLease struct {
	baseLease   mcpadapter.ProjectLease
	router      *projectRouter
	entry       *projectRouterEntry
	releaseOnce sync.Once
	releaseErr  error
}

func (lease *projectRouterLease) ProjectRef() string {
	if lease == nil || lease.baseLease == nil {
		return ""
	}
	return lease.baseLease.ProjectRef()
}

func (lease *projectRouterLease) IssueService() *application.IssueService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.IssueService()
}

func (lease *projectRouterLease) ProjectService() *application.ProjectService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.ProjectService()
}

func (lease *projectRouterLease) RelationService() *application.RelationService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.RelationService()
}

func (lease *projectRouterLease) GraphService() *application.GraphService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.GraphService()
}

func (lease *projectRouterLease) PlanningService() *application.PlanningService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.PlanningService()
}

func (lease *projectRouterLease) CommentService() *application.CommentService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.CommentService()
}

func (lease *projectRouterLease) DecisionService() *application.DecisionService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.DecisionService()
}

func (lease *projectRouterLease) ActivityService() *application.ActivityService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.ActivityService()
}

func (lease *projectRouterLease) SearchService() *application.SearchService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.SearchService()
}

func (lease *projectRouterLease) ReviewService() *application.ReviewService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.ReviewService()
}

func (lease *projectRouterLease) AttemptService() *application.AttemptService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.AttemptService()
}

func (lease *projectRouterLease) SessionService() *application.AgentSessionService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.SessionService()
}

func (lease *projectRouterLease) WorkContextService() *application.WorkContextService {
	if lease == nil || lease.baseLease == nil {
		return nil
	}
	return lease.baseLease.WorkContextService()
}

func (lease *projectRouterLease) Release() error {
	if lease == nil {
		return nil
	}
	lease.releaseOnce.Do(func() {
		if lease.entry == nil || lease.router == nil {
			lease.releaseErr = nil
			return
		}
		lease.router.mu.Lock()
		lease.router.releaseEntryLocked(lease.entry)
		lease.router.mu.Unlock()
		lease.releaseErr = nil
	})
	return lease.releaseErr
}

func (router *projectRouter) releaseEntryLocked(entry *projectRouterEntry) {
	if entry != nil && entry.active > 0 {
		entry.active--
	}
	if router.activeCount > 0 {
		router.activeCount--
	}
	router.cond.Broadcast()
}

func projectRootArgumentError(root string) error {
	if root == "" {
		return domain.NewError(domain.CodeInvalidArgument, "project_root is required", false,
			domain.Detail{Field: "project_root", Code: "REQUIRED", Message: "project_root is required"})
	}
	return domain.NewError(domain.CodeInvalidArgument, "project_root must be an absolute path", false,
		domain.Detail{Field: "project_root", Code: "ABSOLUTE_PATH_REQUIRED", Message: "project_root must be an absolute path"})
}
