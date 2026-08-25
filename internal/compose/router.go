package compose

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/application"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	"rhizome-mcp/internal/ports"
	"rhizome-mcp/internal/projectconfig"
	"rhizome-mcp/internal/projectrouting"
	"rhizome-mcp/internal/runtime"
)

var RouterMaxEntries = 16

var ErrRouterClosed = errors.New("project router is closed")

type Router struct {
	dataRoot      string
	clock         clock.Clock
	sqliteOptions sqlite.Options
	composeFn     func(context.Context, string, string, clock.Clock, sqlite.Options) (*Services, *runtime.Project, error)

	defaultBundle *Services
	defaultRef    string
	defaultRoot   string

	mu                  sync.Mutex
	cond                *sync.Cond
	entries             map[string]*routerEntry
	closed              bool
	closing             bool
	closingErr          error
	closeStarted        chan struct{}
	closeStartedClosed  bool
	closeCleanupStarted bool
	closeComplete       bool

	openingCount int
	activeCount  int
	useCounter   int64
}

type routerEntry struct {
	ref      string
	pinned   bool
	state    string
	bundle   *Services
	active   int
	lastUsed int64
	done     chan struct{}
	result   routerResult
	removed  bool
}

type routerResult struct {
	bundle *Services
	err    error
}

func NewRouter(dataRoot string, source clock.Clock, sqliteOptions sqlite.Options, defaultBundle *Services) *Router {
	if source == nil {
		source = clock.RealClock{}
	}
	router := &Router{
		dataRoot:      dataRoot,
		clock:         source,
		sqliteOptions: sqliteOptions,
		entries:       make(map[string]*routerEntry),
		composeFn:     OpenExisting,
		closeStarted:  make(chan struct{}),
	}
	router.cond = sync.NewCond(&router.mu)
	if defaultBundle != nil && defaultBundle.ProjectRef() != "" {
		router.defaultBundle = defaultBundle
		router.defaultRef = defaultBundle.ProjectRef()
		if defaultBundle.project != nil {
			router.defaultRoot = defaultBundle.project.Root
		}
		router.useCounter = 1
		router.entries[router.defaultRef] = &routerEntry{
			ref:      router.defaultRef,
			pinned:   true,
			state:    "ready",
			bundle:   defaultBundle,
			lastUsed: router.useCounter,
		}
	}
	return router
}

func (router *Router) DataRoot() string {
	return router.dataRoot
}

func (router *Router) DefaultProjectRef() string {
	if router == nil {
		return ""
	}
	return router.defaultRef
}

func (router *Router) Acquire(ctx context.Context, explicitRef *string) (projectrouting.ProjectLease, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}

	router.mu.Lock()
	if router.closed {
		router.mu.Unlock()
		return nil, ErrRouterClosed
	}

	ref, err := router.resolveRef(explicitRef)
	if err != nil {
		router.mu.Unlock()
		return nil, err
	}
	if ref == "" {
		router.mu.Unlock()
		return nil, projectrouting.NewProjectRequiredError()
	}

	if entry, ok := router.entries[ref]; ok {
		if entry.state == "opening" {
			entry.active++
			entry.lastUsed = router.nextUseLocked()
			router.activeCount++
			router.mu.Unlock()
			select {
			case <-entry.done:
			case <-waitCtx.Done():
				router.mu.Lock()
				router.releaseEntryLocked(entry)
				router.mu.Unlock()
				return nil, waitCtx.Err()
			}
			router.mu.Lock()
			defer router.mu.Unlock()
			if entry.result.err != nil {
				router.releaseEntryLocked(entry)
				return nil, entry.result.err
			}
			if entry.result.bundle == nil {
				router.releaseEntryLocked(entry)
				return nil, ErrRouterClosed
			}
			return router.wrapLease(entry), nil
		}
		if entry.state == "ready" {
			entry.active++
			entry.lastUsed = router.nextUseLocked()
			router.activeCount++
			router.mu.Unlock()
			return router.wrapLease(entry), nil
		}
	}

	var evicted *routerEntry
	if len(router.entries) >= RouterMaxEntries {
		if evicted = router.evictIdleEntryLocked(); evicted == nil {
			router.mu.Unlock()
			return nil, projectrouting.NewProjectCapacityExceededError()
		}
	}

	entry := &routerEntry{ref: ref, state: "opening", done: make(chan struct{}), lastUsed: router.nextUseLocked()}
	router.entries[ref] = entry
	router.openingCount++
	entry.active++
	entry.lastUsed = router.nextUseLocked()
	router.activeCount++
	router.mu.Unlock()
	if evicted != nil && evicted.bundle != nil {
		_ = evicted.bundle.Close(context.Background())
	}

	go func() {
		result := router.openEntry(context.WithoutCancel(waitCtx), entry)
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
		entry.lastUsed = router.nextUseLocked()
		router.openingCount--
		close(entry.done)
		router.cond.Broadcast()
	}()

	select {
	case <-entry.done:
	case <-waitCtx.Done():
		router.mu.Lock()
		router.releaseEntryLocked(entry)
		router.mu.Unlock()
		return nil, waitCtx.Err()
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if entry.result.err != nil {
		router.releaseEntryLocked(entry)
		return nil, entry.result.err
	}
	if entry.result.bundle == nil {
		router.releaseEntryLocked(entry)
		return nil, ErrRouterClosed
	}
	return router.wrapLease(entry), nil
}

func (router *Router) OpenProject(ctx context.Context, absoluteRoot string) (projectrouting.ProjectLease, error) {
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

func (router *Router) resolveRef(explicitRef *string) (string, error) {
	if explicitRef != nil {
		if err := projectrouting.ValidateProjectRef(*explicitRef); err != nil {
			return "", err
		}
		return *explicitRef, nil
	}
	if router.defaultRef != "" {
		return router.defaultRef, nil
	}
	return "", projectrouting.NewProjectRequiredError()
}

func (router *Router) openEntry(ctx context.Context, entry *routerEntry) routerResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if entry == nil {
		return routerResult{err: fmt.Errorf("project router entry is nil")}
	}
	if router.composeFn == nil {
		return routerResult{err: fmt.Errorf("project router composition function is nil")}
	}
	bundle, project, err := router.composeFn(ctx, entry.ref, router.dataRoot, router.clock, router.sqliteOptions)
	if err != nil {
		return routerResult{err: err}
	}
	if bundle == nil || project == nil {
		return routerResult{err: fmt.Errorf("project bundle for %q was nil", entry.ref)}
	}
	entry.bundle = bundle
	return routerResult{bundle: bundle}
}

func (router *Router) wrapLease(entry *routerEntry) projectrouting.ProjectLease {
	if entry == nil || entry.bundle == nil {
		return nil
	}
	baseLease := projectrouting.NewStaticLease(entry.ref, entry.bundle.Bundle())
	return &routerLease{baseLease: baseLease, router: router, entry: entry}
}

func (router *Router) nextUseLocked() int64 {
	router.useCounter++
	return router.useCounter
}

func (router *Router) evictIdleEntryLocked() *routerEntry {
	var victim *routerEntry
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

func (router *Router) Close(ctx context.Context) error {
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	var cancel context.CancelFunc
	if _, ok := waitCtx.Deadline(); !ok {
		waitCtx, cancel = context.WithTimeout(waitCtx, 5*time.Second)
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	if waitCtx.Done() != nil {
		go func() {
			<-waitCtx.Done()
			router.mu.Lock()
			router.cond.Broadcast()
			router.mu.Unlock()
		}()
	}

	router.mu.Lock()
	if !router.closed {
		router.closed = true
		router.closing = true
		if router.closeStarted != nil && !router.closeStartedClosed {
			close(router.closeStarted)
			router.closeStartedClosed = true
		}
	}
	if router.closeComplete {
		result := router.closingErr
		router.mu.Unlock()
		return result
	}
	for {
		if router.closeComplete {
			result := router.closingErr
			router.mu.Unlock()
			return result
		}
		if err := waitCtx.Err(); err != nil {
			router.mu.Unlock()
			return err
		}
		if !router.closeCleanupStarted {
			if router.openingCount == 0 && router.activeCount == 0 {
				router.closeCleanupStarted = true
				router.mu.Unlock()
				return router.runCleanup(waitCtx)
			}
		}
		router.cond.Wait()
	}
}

func (router *Router) runCleanup(ctx context.Context) error {
	router.mu.Lock()
	if router.openingCount != 0 || router.activeCount != 0 {
		router.mu.Unlock()
		return context.DeadlineExceeded
	}
	remaining := make([]*routerEntry, 0, len(router.entries))
	for _, entry := range router.entries {
		if entry != nil {
			remaining = append(remaining, entry)
		}
	}
	router.entries = make(map[string]*routerEntry)
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
	router.closeComplete = true
	router.cond.Broadcast()
	router.mu.Unlock()
	return router.closingErr
}

// ExpireAttempts sweeps each currently ready project without retaining router
// locks while project-local expiry performs storage work. It pins entries
// through pinReadyEntry rather than Acquire: Acquire unconditionally stamps
// lastUsed and, on a miss, reopens the project from disk, both of which
// would let this periodic maintenance sweep defeat client-driven LRU
// eviction (by refreshing every ready entry's recency once a minute) and
// reopen a project a client had already evicted for capacity. A ref that no
// longer names a ready, non-removed entry by the time this reaches it
// (evicted or closed between the snapshot above and this loop) is skipped
// instead of being reopened.
func (router *Router) ExpireAttempts(ctx context.Context) (ports.ExpireAttemptsResult, error) {
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
		lease := router.pinReadyEntry(ref)
		if lease == nil {
			continue
		}
		projectResult, projectErr := lease.Services().AttemptService.ExpireAttempts(ctx)
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

// pinReadyEntry pins ref for a maintenance operation if it currently names a
// ready, non-removed entry: it increments the active counts exactly like
// Acquire's ready-entry fast path, but deliberately does not touch lastUsed,
// so the caller's use doesn't count as client activity for LRU purposes.
// Returns nil if ref no longer names such an entry (never Acquire's
// reopen-from-disk fallback). The caller must Release the returned lease.
func (router *Router) pinReadyEntry(ref string) projectrouting.ProjectLease {
	router.mu.Lock()
	defer router.mu.Unlock()
	entry, ok := router.entries[ref]
	if !ok || entry == nil || entry.state != "ready" || entry.removed {
		return nil
	}
	entry.active++
	router.activeCount++
	return router.wrapLease(entry)
}

type routerLease struct {
	baseLease   projectrouting.ProjectLease
	router      *Router
	entry       *routerEntry
	releaseOnce sync.Once
	releaseErr  error
}

func (lease *routerLease) ProjectRef() string {
	if lease == nil || lease.baseLease == nil {
		return ""
	}
	return lease.baseLease.ProjectRef()
}

func (lease *routerLease) Services() application.Bundle {
	if lease == nil || lease.baseLease == nil {
		return application.Bundle{}
	}
	return lease.baseLease.Services()
}

func (lease *routerLease) Release() error {
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

func (router *Router) releaseEntryLocked(entry *routerEntry) {
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
