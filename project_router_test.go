package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpadapter "rhizome-mcp/internal/adapters/mcp"
	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/clock"
	"rhizome-mcp/internal/domain"
	projectruntime "rhizome-mcp/internal/runtime"
)

func TestProjectRouterUsesDefaultBundleAndSharedModeSelection(t *testing.T) {
	defaultBundle := newTestBundle("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, defaultBundle)
	lease, err := router.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire(nil) error = %v", err)
	}
	if lease.ProjectRef() != defaultBundle.ProjectRef() {
		t.Fatalf("Acquire(nil) project ref = %q, want %q", lease.ProjectRef(), defaultBundle.ProjectRef())
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	shared := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	_, err = shared.Acquire(context.Background(), nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectRequired {
		t.Fatalf("shared Acquire(nil) error = %v, want PROJECT_REQUIRED", err)
	}
}

func TestProjectRouterRejectsMalformedRefsBeforeLookup(t *testing.T) {
	calls := 0
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(context.Context, string, string, clock.Clock, sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		calls++
		return nil, nil, errors.New("unexpected load")
	}

	badRef := "bad-ref"
	_, err := router.Acquire(context.Background(), &badRef)
	if err == nil {
		t.Fatal("Acquire(bad ref) succeeded")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("Acquire(bad ref) error = %v, want domain.Error", err)
	}
	if domainErr.Code != domain.CodeInvalidArgument {
		t.Fatalf("Acquire(bad ref) code = %q, want %q", domainErr.Code, domain.CodeInvalidArgument)
	}
	if calls != 0 {
		t.Fatalf("loader calls = %d, want 0", calls)
	}
}

func TestProjectRouterCoalescesConcurrentOpens(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(context.Context, string, string, clock.Clock, sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return newTestBundle(ref), &projectruntime.Project{ProjectID: ref}, nil
	}

	ctx := context.Background()
	leases := make(chan mcpadapter.ProjectLease, 4)
	errCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			lease, err := router.Acquire(ctx, &ref)
			if err != nil {
				errCh <- err
				return
			}
			leases <- lease
		}()
	}
	<-started
	close(release)
	for i := 0; i < 4; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("Acquire() error = %v", err)
		case lease := <-leases:
			if lease.ProjectRef() != ref {
				t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), ref)
			}
			if err := lease.Release(); err != nil {
				t.Fatalf("Release() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for coalesced opens")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
}

func TestProjectRouterEvictsLeastRecentlyUsedReadyEntries(t *testing.T) {
	oldMax := projectRouterMaxEntries
	projectRouterMaxEntries = 2
	defer func() { projectRouterMaxEntries = oldMax }()

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	first := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	second := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	third := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	firstLease, err := router.Acquire(context.Background(), &first)
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	if err := firstLease.Release(); err != nil {
		t.Fatalf("Release(first) error = %v", err)
	}
	secondLease, err := router.Acquire(context.Background(), &second)
	if err != nil {
		t.Fatalf("Acquire(second) error = %v", err)
	}
	if err := secondLease.Release(); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
	secondLease, err = router.Acquire(context.Background(), &second)
	if err != nil {
		t.Fatalf("Acquire(second again) error = %v", err)
	}
	if err := secondLease.Release(); err != nil {
		t.Fatalf("Release(second again) error = %v", err)
	}
	thirdLease, err := router.Acquire(context.Background(), &third)
	if err != nil {
		t.Fatalf("Acquire(third) error = %v", err)
	}
	if err := thirdLease.Release(); err != nil {
		t.Fatalf("Release(third) error = %v", err)
	}
	if _, ok := router.entries[first]; ok {
		t.Fatalf("expected entry %q to be evicted", first)
	}
	if _, ok := router.entries[second]; !ok {
		t.Fatal("expected entry for second ref to remain")
	}
	if _, ok := router.entries[third]; !ok {
		t.Fatal("expected entry for third ref to remain")
	}
}

func TestProjectRouterDoesNotEvictActiveEntry(t *testing.T) {
	oldMax := projectRouterMaxEntries
	projectRouterMaxEntries = 1
	defer func() { projectRouterMaxEntries = oldMax }()

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	first := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	second := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	firstLease, err := router.Acquire(context.Background(), &first)
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	_, err = router.Acquire(context.Background(), &second)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeProjectCapacityExceeded || !domainErr.Retryable {
		t.Fatalf("Acquire(second) error = %v, want retryable PROJECT_CAPACITY_EXCEEDED", err)
	}
	if err := firstLease.Release(); err != nil {
		t.Fatalf("Release(first) error = %v", err)
	}
	secondLease, err := router.Acquire(context.Background(), &second)
	if err != nil {
		t.Fatalf("Acquire(second after release) error = %v", err)
	}
	if err := secondLease.Release(); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
}

func TestProjectRouterRetriesAfterFailedOpen(t *testing.T) {
	calls := 0
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		calls++
		if calls == 1 {
			return nil, nil, errors.New("open failed")
		}
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	ref := "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	if _, err := router.Acquire(context.Background(), &ref); err == nil {
		t.Fatal("first Acquire() succeeded, want error")
	}
	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if lease.ProjectRef() != ref {
		t.Fatalf("lease ref = %q, want %q", lease.ProjectRef(), ref)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestProjectRouterReleaseIsIdempotentAndCloseDrains(t *testing.T) {
	defaultBundle := newTestBundle("01ARZ3NDEKTSV4RRFFQ69G5FAZ")
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, defaultBundle)
	lease, err := router.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire(nil) error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("first Release() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}

	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB0"
	openStarted := make(chan struct{})
	allowOpen := make(chan struct{})
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		close(openStarted)
		<-allowOpen
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}
	started := make(chan mcpadapter.ProjectLease, 1)
	acquireErr := make(chan error, 1)
	go func() {
		lease, err := router.Acquire(context.Background(), &ref)
		if err != nil {
			acquireErr <- err
			return
		}
		started <- lease
	}()
	<-openStarted
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- router.Close(context.Background())
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before open completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowOpen)
	var openedLease mcpadapter.ProjectLease
	select {
	case err := <-acquireErr:
		t.Fatalf("Acquire() error after open = %v", err)
	case openedLease = <-started:
	}
	if err := openedLease.Release(); err != nil {
		t.Fatalf("Release() error after open = %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestProjectRouterCancelingInitiatingWaiterReleasesReservation(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB1"
	openStarted := make(chan struct{})
	allowOpen := make(chan struct{})
	acquireErr := make(chan error, 1)

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		close(openStarted)
		<-allowOpen
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := router.Acquire(ctx, &ref)
		acquireErr <- err
	}()

	select {
	case <-openStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for open to begin")
	}

	cancel()

	select {
	case err := <-acquireErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled acquire to return")
	}

	router.mu.Lock()
	entry, present := router.entries[ref]
	opening := router.openingCount
	active := router.activeCount
	router.mu.Unlock()
	if !present {
		t.Fatal("expected router entry to remain present while open is in flight")
	}
	if opening != 1 {
		t.Fatalf("openingCount = %d, want 1 while open is still in flight", opening)
	}
	if active != 0 {
		t.Fatalf("activeCount = %d, want 0 after canceled waiter releases reservation", active)
	}
	if entry == nil || entry.active != 0 {
		t.Fatalf("entry.active = %d, want 0 after canceled waiter releases reservation", entry.active)
	}

	close(allowOpen)
}

func TestProjectRouterCancelingCoalescedWaiterLeavesHealthyWaiterWorking(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB2"
	openStarted := make(chan struct{})
	allowOpen := make(chan struct{})
	healthyLeaseCh := make(chan mcpadapter.ProjectLease, 1)
	healthyErrCh := make(chan error, 1)
	coalescedErrCh := make(chan error, 1)

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		close(openStarted)
		<-allowOpen
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	healthyCtx := context.Background()
	go func() {
		lease, err := router.Acquire(healthyCtx, &ref)
		if err != nil {
			healthyErrCh <- err
			return
		}
		healthyLeaseCh <- lease
	}()

	select {
	case <-openStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for open to begin")
	}

	coalescedCtx, cancelCoalesced := context.WithCancel(context.Background())
	go func() {
		_, err := router.Acquire(coalescedCtx, &ref)
		coalescedErrCh <- err
	}()

	cancelCoalesced()

	select {
	case err := <-coalescedErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("coalesced Acquire() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled coalesced acquire to return")
	}

	router.mu.Lock()
	entry, present := router.entries[ref]
	opening := router.openingCount
	active := router.activeCount
	router.mu.Unlock()
	if !present {
		t.Fatal("expected router entry to remain present while healthy waiter is still waiting")
	}
	if opening != 1 {
		t.Fatalf("openingCount = %d, want 1 while open is still in flight", opening)
	}
	if active != 1 {
		t.Fatalf("activeCount = %d, want 1 after canceled coalesced waiter releases reservation", active)
	}
	if entry == nil || entry.active != 1 {
		t.Fatalf("entry.active = %d, want 1 after canceled coalesced waiter releases reservation", entry.active)
	}

	close(allowOpen)

	select {
	case err := <-healthyErrCh:
		t.Fatalf("healthy Acquire() error = %v", err)
	case lease := <-healthyLeaseCh:
		if lease == nil {
			t.Fatal("healthy Acquire() returned nil lease")
		}
		if lease.ProjectRef() != ref {
			t.Fatalf("healthy lease ref = %q, want %q", lease.ProjectRef(), ref)
		}
		if err := lease.Release(); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for healthy waiter to receive lease")
	}
}

func TestProjectRouterCloseWaitsForInitiatingLeaseRelease(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB1"
	openStarted := make(chan struct{})
	allowOpen := make(chan struct{})
	leaseDelivered := make(chan mcpadapter.ProjectLease, 1)
	acquireErr := make(chan error, 1)
	closeDone := make(chan error, 1)

	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		close(openStarted)
		<-allowOpen
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	go func() {
		lease, err := router.Acquire(context.Background(), &ref)
		if err != nil {
			acquireErr <- err
			return
		}
		leaseDelivered <- lease
	}()

	<-openStarted
	go func() {
		closeDone <- router.Close(context.Background())
	}()

	select {
	case <-router.closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Close to begin draining")
	}

	close(allowOpen)

	var lease mcpadapter.ProjectLease
	select {
	case err := <-acquireErr:
		t.Fatalf("Acquire() error after open = %v", err)
	case lease = <-leaseDelivered:
	}
	if lease == nil {
		t.Fatal("Acquire() returned nil lease")
	}

	router.mu.Lock()
	entry, present := router.entries[ref]
	opening := router.openingCount
	active := router.activeCount
	router.mu.Unlock()
	if !present {
		t.Fatal("expected router entry to remain present while active lease is outstanding")
	}
	if opening != 0 {
		t.Fatalf("openingCount = %d, want 0 after open completion", opening)
	}
	if active != 1 {
		t.Fatalf("activeCount = %d, want 1 while lease is active", active)
	}
	if entry == nil || entry.active != 1 {
		t.Fatalf("entry.active = %v, want 1 while lease is active", entry.active)
	}

	select {
	case err := <-closeDone:
		t.Fatalf("Close completed before initiating lease was released: %v", err)
	default:
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Close to finish after lease release")
	}
}

func TestProjectRouterCloseTimeoutDoesNotConsumeCleanupCapability(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB3"
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	closeErr := router.Close(deadlineCtx)
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want context.DeadlineExceeded", closeErr)
	}

	router.mu.Lock()
	_, ok := router.entries[ref]
	closed := router.closed
	opening := router.openingCount
	active := router.activeCount
	cleanupStarted := router.closeCleanupStarted
	router.mu.Unlock()
	if !ok {
		t.Fatal("expected router entry to remain present after timed-out Close")
	}
	if !closed {
		t.Fatal("expected router to be closed after first Close")
	}
	if opening != 0 || active != 1 {
		t.Fatalf("openingCount/activeCount = %d/%d, want 0/1", opening, active)
	}
	if cleanupStarted {
		t.Fatal("expected cleanup not to start before lease release")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := router.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}

	router.mu.Lock()
	_, ok = router.entries[ref]
	cleanupStarted = router.closeCleanupStarted
	closeComplete := router.closeComplete
	router.mu.Unlock()
	if ok {
		t.Fatal("expected router entry to be removed after retry Close")
	}
	if !cleanupStarted {
		t.Fatal("expected cleanup to start on retry Close")
	}
	if !closeComplete {
		t.Fatal("expected cleanup to complete on retry Close")
	}
	if _, err := router.Acquire(context.Background(), &ref); !errors.Is(err, errProjectRouterClosed) {
		t.Fatalf("Acquire() after timed-out Close error = %v, want %v", err, errProjectRouterClosed)
	}
}

func TestProjectRouterCloseWithCanceledContextClosesAdmissionImmediately(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB5"
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	closeErr := router.Close(canceledCtx)
	if !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", closeErr)
	}

	router.mu.Lock()
	closed := router.closed
	closeStarted := router.closeStartedClosed
	router.mu.Unlock()
	if !closed {
		t.Fatal("expected router to be closed after canceled Close")
	}
	if !closeStarted {
		t.Fatal("expected closeStarted to be signaled for canceled Close")
	}
	if _, err := router.Acquire(context.Background(), &ref); !errors.Is(err, errProjectRouterClosed) {
		t.Fatalf("Acquire() after canceled Close error = %v, want %v", err, errProjectRouterClosed)
	}
	if err := router.Close(context.Background()); err != nil {
		t.Fatalf("retry Close() error = %v", err)
	}
}

func TestProjectRouterConcurrentCloseCallsShareCleanup(t *testing.T) {
	ref := "01ARZ3NDEKTSV4RRFFQ69G5FB4"
	router := newProjectRouter("/tmp/data", clock.RealClock{}, sqlite.Options{}, nil)
	router.composeFn = func(_ context.Context, projectID string, _ string, _ clock.Clock, _ sqlite.Options) (*composedServices, *projectruntime.Project, error) {
		return newTestBundle(projectID), &projectruntime.Project{ProjectID: projectID}, nil
	}

	lease, err := router.Acquire(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	router.mu.Lock()
	entry := router.entries[ref]
	router.mu.Unlock()
	if entry == nil {
		t.Fatal("expected router entry to be present")
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			errs <- router.Close(context.Background())
		}()
	}
	close(start)

	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent Close calls")
		}
	}

	router.mu.Lock()
	cleanupStarted := router.closeCleanupStarted
	closeComplete := router.closeComplete
	_, ok := router.entries[ref]
	router.mu.Unlock()
	if !cleanupStarted {
		t.Fatal("expected cleanup to start for concurrent Close calls")
	}
	if !closeComplete {
		t.Fatal("expected cleanup to complete for concurrent Close calls")
	}
	if ok {
		t.Fatal("expected router entry to be removed after concurrent Close")
	}
}

func newTestBundle(projectID string) *composedServices {
	return &composedServices{project: &projectruntime.Project{ProjectID: projectID}}
}
