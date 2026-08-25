//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

// TestIntegrationReservationRace is the ISSUE-183 acquire/acquire proof:
// two independent OS processes (one HTTP server, one stdio server) attached
// to the same repository and data root race reserve_resources on overlapping
// resource sets. The in-process mutation tests
// (internal/adapters/sqlite/reservation_mutations_test.go) already prove
// all-or-nothing acquisition within one connection pool; this test proves the
// same holds when the SQLite WAL writer lock is contended across process
// boundaries: exactly one racer acquires, the loser sees a stable
// RESOURCE_RESERVATION_CONFLICT domain error rather than a raw
// SQLITE_BUSY/"database is locked" failure, and the loser's non-conflicting
// resource is never partially written.
func TestIntegrationReservationRace(t *testing.T) {
	// Every case reserves under its own top-level directory (or logical
	// namespace): reservations acquired by a winning subtest stay active for
	// the rest of the run, so sharing one path across cases would make later
	// cases conflict with an earlier winner instead of with each other.
	overlapCases := []struct {
		name string
		// httpContended and stdioContended are the pair whose resource
		// languages intersect; each racer also asks for a resource only it
		// wants, so a partial write is detectable.
		httpContended  map[string]any
		stdioContended map[string]any
	}{
		{
			name:           "exact_file",
			httpContended:  map[string]any{"kind": "file", "path": "case0/shared/config.go"},
			stdioContended: map[string]any{"kind": "file", "path": "case0/shared/config.go"},
		},
		{
			name:           "ascii_case_folded_file",
			httpContended:  map[string]any{"kind": "file", "path": "case1/Shared/Config.go"},
			stdioContended: map[string]any{"kind": "file", "path": "case1/shared/config.go"},
		},
		{
			name:           "ancestor_directory_over_file",
			httpContended:  map[string]any{"kind": "directory", "path": "case2/shared"},
			stdioContended: map[string]any{"kind": "file", "path": "case2/shared/config.go"},
		},
		{
			name:           "glob_over_file",
			httpContended:  map[string]any{"kind": "glob", "path": "case3/shared/**"},
			stdioContended: map[string]any{"kind": "file", "path": "case3/shared/config.go"},
		},
		{
			name:           "glob_over_glob",
			httpContended:  map[string]any{"kind": "glob", "path": "case4/*/config.go"},
			stdioContended: map[string]any{"kind": "glob", "path": "case4/shared/**"},
		},
		{
			// Logical names are compared exactly, unlike ASCII-folded paths,
			// so the intersecting pair here is the identical name.
			name:           "logical",
			httpContended:  map[string]any{"kind": "logical", "namespace": "schema", "name": "migrations"},
			stdioContended: map[string]any{"kind": "logical", "namespace": "schema", "name": "migrations"},
		},
	}

	env := newIntegrationEnvironment(t)
	attached := env.attach()

	serverA := launchIntegrationHTTPServer(t, attached, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, serverA) })
	endpointA := "http://" + serverA.waitForEndpoint(t) + "/mcp"

	httpClient := &http.Client{Timeout: integrationTimeout}
	_, httpSessionID, err := communicateThroughHTTP(t, endpointA, "reservation-race-http")
	if err != nil {
		t.Fatalf("initialize HTTP racer session: %v\nstderr:\n%s", err, serverA.output.String())
	}
	stdioSession := attached.connect(t)

	for index, overlapCase := range overlapCases {
		t.Run(overlapCase.name, func(t *testing.T) {
			// Each racer owns its own issue and attempt: reservations are
			// held per attempt, so racing on one issue would be an
			// active-attempt race (already covered by TestIntegrationClaimRace)
			// instead of a reservation race.
			httpIssueID := seedReadyIssue(t, stdioSession, fmt.Sprintf("reservation race http %d", index))
			stdioIssueID := seedReadyIssue(t, stdioSession, fmt.Sprintf("reservation race stdio %d", index))

			httpLease := claimOverHTTPForReservation(t, httpClient, endpointA, httpSessionID, 200+index*2, httpIssueID)
			stdioLease := claimOverStdioForReservation(t, stdioSession, stdioIssueID)

			// Every racer asks for two resources: one that collides with the
			// other racer's set, and one that is unique to itself. The unique
			// resource is what proves all-or-nothing: a loser that wrote its
			// non-conflicting half would leave a partial reservation set.
			httpUnique := map[string]any{"kind": "file", "path": fmt.Sprintf("case%d/http-only.go", index)}
			stdioUnique := map[string]any{"kind": "file", "path": fmt.Sprintf("case%d/stdio-only.go", index)}

			httpOutcome, stdioOutcome := raceReserveResources(
				httpClient, endpointA, httpSessionID, 300+index*2, stdioSession,
				reservationRequest{lease: httpLease, resources: []map[string]any{overlapCase.httpContended, httpUnique}},
				reservationRequest{lease: stdioLease, resources: []map[string]any{overlapCase.stdioContended, stdioUnique}},
			)

			winner, loser := assertReservationRaceHasOneWinner(t, httpOutcome, stdioOutcome)
			assertActiveReservationCount(t, attached, winner.attemptID, 2)
			assertActiveReservationCount(t, attached, loser.attemptID, 0)
		})
	}
}

// TestIntegrationReservationExpiryRecovery is the ISSUE-183 acquire/expire
// proof and the canonical two-agent scenario documented in
// docs/12-resource-reservations.md: agent A claims an issue and atomically
// reserves a file, agent B is refused the same file while A's lease is live,
// agent A's process is killed without a clean shutdown, and once A's lease
// has passed agent B reclaims the same issue and reacquires the same resource
// in one atomic call. The reservation A held must end up released with reason
// "expired" rather than permanently stuck.
func TestIntegrationReservationExpiryRecovery(t *testing.T) {
	const contendedPath = "internal/reservation/expiry.go"

	env := newIntegrationEnvironment(t)
	attached := env.attach()

	stdioSession := attached.connect(t)
	sharedIssueID := seedReadyIssue(t, stdioSession, "reservation expiry shared issue")
	otherIssueID := seedReadyIssue(t, stdioSession, "reservation expiry other issue")

	serverA := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { killIntegrationHTTPServer(t, serverA) })
	endpointA := "http://" + serverA.waitForEndpoint(t) + "/mcp"
	httpClient := &http.Client{Timeout: integrationTimeout}
	_, httpSessionID, err := communicateThroughHTTP(t, endpointA, "reservation-expiry-http")
	if err != nil {
		t.Fatalf("initialize agent A session: %v\nstderr:\n%s", err, serverA.output.String())
	}

	// Agent A: claim + reserve atomically, in the other process.
	claimA, err := postJSONRPC(httpClient, endpointA, httpSessionID, 400, "tools/call", map[string]any{
		"name": "claim_issue",
		"arguments": map[string]any{
			"issue_id":      sharedIssueID,
			"lease_seconds": 300,
			"resources":     []map[string]any{{"kind": "file", "path": contendedPath}},
		},
	})
	if err != nil {
		t.Fatalf("agent A claim_issue: %v", err)
	}
	attemptA := decodeClaimEnvelope(t, "agent A", claimA.result)
	if attemptA.attemptID == "" {
		t.Fatalf("agent A claim returned no attempt id: %s", claimA.result)
	}
	assertActiveReservationCount(t, attached, attemptA.attemptID, 1)

	// Agent B, in a different process, sees A's reservation and is refused
	// the whole claim: an atomic claim+reserve that conflicts must not leave
	// a half-claimed issue behind.
	conflicted := callIntegrationTool(t, stdioSession, "claim_issue", map[string]any{
		"issue_id":      otherIssueID,
		"lease_seconds": 300,
		"resources":     []map[string]any{{"kind": "file", "path": contendedPath}},
	})
	if !conflicted.IsError {
		t.Fatalf("agent B claim_issue succeeded while agent A held %q, want a conflict", contendedPath)
	}
	code, message := decodeIntegrationFailure(t, conflicted)
	if code != domain.CodeResourceReservationConflict {
		t.Fatalf("agent B conflict code = %q, want %q (message: %q)", code, domain.CodeResourceReservationConflict, message)
	}
	assertActiveAttemptCount(t, attached, otherIssueID, 0)

	// Agent A's process dies without running shutdown handlers, then its
	// lease passes. Backdating through a third connection is what a real
	// wall-clock wait would produce; the claim path expires the stale attempt
	// lazily on the next claim of the same issue.
	killIntegrationHTTPServer(t, serverA)
	backdateAttemptLease(t, attached, attemptA.attemptID)

	recovered := callIntegrationTool(t, stdioSession, "claim_issue", map[string]any{
		"issue_id":      sharedIssueID,
		"lease_seconds": 300,
		"resources":     []map[string]any{{"kind": "file", "path": contendedPath}},
	})
	if recovered.IsError {
		code, message := decodeIntegrationFailure(t, recovered)
		t.Fatalf("agent B reacquisition after expiry failed: %s: %s", code, message)
	}
	var recoveredClaim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, recovered, &recoveredClaim)
	if recoveredClaim.Attempt.ID == "" || recoveredClaim.LeaseToken == "" {
		t.Fatalf("agent B reacquisition returned no attempt/lease: %#v", recoveredClaim)
	}
	if recoveredClaim.Attempt.ID == attemptA.attemptID {
		t.Fatalf("agent B reacquisition reused agent A's expired attempt %s", attemptA.attemptID)
	}
	assertActiveReservationCount(t, attached, attemptA.attemptID, 0)
	assertActiveReservationCount(t, attached, recoveredClaim.Attempt.ID, 1)
	assertReservationsReleasedAs(t, attached, attemptA.attemptID, "expired")
	assertAttemptStatus(t, attached, attemptA.attemptID, "expired")
}

// reservationRequest is one racer's reserve_resources payload: the attempt it
// owns and the resource set it will try to acquire all-or-nothing.
type reservationRequest struct {
	lease     leaseHandle
	resources []map[string]any
}

// leaseHandle is the claim result a reservation racer needs: which attempt it
// owns and the opaque token proving it.
type leaseHandle struct {
	attemptID  string
	leaseToken string
}

// reservationOutcome is one racer's reserve_resources result. protocolErr
// distinguishes a transport-level failure (unexpected: the race should never
// produce one) from a domain-level result.
type reservationOutcome struct {
	transport    string
	attemptID    string
	protocolErr  error
	isError      bool
	reservations int
	code         string
	message      string
}

// raceReserveResources fires both racers' reserve_resources calls as close to
// simultaneously as the harness allows, using the same ready-then-release
// barrier as raceClaimIssue so the two RPCs actually overlap instead of
// serializing behind goroutine startup.
func raceReserveResources(
	httpClient *http.Client, endpoint, httpSessionID string, requestID int,
	stdioSession *mcp.ClientSession, httpRequest, stdioRequest reservationRequest,
) (httpOutcome, stdioOutcome reservationOutcome) {
	var readyWG sync.WaitGroup
	readyWG.Add(2)
	start := make(chan struct{})
	httpDone := make(chan reservationOutcome, 1)
	stdioDone := make(chan reservationOutcome, 1)

	go func() {
		readyWG.Done()
		<-start
		httpDone <- reserveOverHTTP(httpClient, endpoint, httpSessionID, requestID, httpRequest)
	}()
	go func() {
		readyWG.Done()
		<-start
		stdioDone <- reserveOverStdio(stdioSession, stdioRequest)
	}()

	readyWG.Wait()
	close(start)

	return <-httpDone, <-stdioDone
}

func reserveOverStdio(session *mcp.ClientSession, request reservationRequest) reservationOutcome {
	outcome := reservationOutcome{transport: "stdio", attemptID: request.lease.attemptID}
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reserve_resources",
		Arguments: map[string]any{
			"attempt_id":  request.lease.attemptID,
			"lease_token": request.lease.leaseToken,
			"resources":   request.resources,
		},
	})
	if err != nil {
		outcome.protocolErr = fmt.Errorf("stdio reserve_resources protocol error: %w", err)
		return outcome
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		outcome.protocolErr = fmt.Errorf("marshal stdio structured content: %w", err)
		return outcome
	}
	outcome.isError = result.IsError
	applyReservationPayload(&outcome, data)
	return outcome
}

func reserveOverHTTP(httpClient *http.Client, endpoint, sessionID string, requestID int, request reservationRequest) reservationOutcome {
	outcome := reservationOutcome{transport: "http", attemptID: request.lease.attemptID}
	response, err := postJSONRPC(httpClient, endpoint, sessionID, requestID, "tools/call", map[string]any{
		"name": "reserve_resources",
		"arguments": map[string]any{
			"attempt_id":  request.lease.attemptID,
			"lease_token": request.lease.leaseToken,
			"resources":   request.resources,
		},
	})
	if err != nil {
		outcome.protocolErr = fmt.Errorf("http reserve_resources protocol error: %w", err)
		return outcome
	}
	var envelope struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(response.result, &envelope); err != nil {
		outcome.protocolErr = fmt.Errorf("decode http tools/call envelope: %w", err)
		return outcome
	}
	outcome.isError = envelope.IsError
	applyReservationPayload(&outcome, envelope.StructuredContent)
	return outcome
}

// applyReservationPayload decodes either shape of a reserve_resources result
// -- the acquired reservation list, or the domain error the loser sees --
// into outcome.
func applyReservationPayload(outcome *reservationOutcome, payload []byte) {
	if outcome.isError {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &failure); err != nil {
			outcome.protocolErr = fmt.Errorf("decode %s error payload: %w", outcome.transport, err)
			return
		}
		outcome.code = failure.Code
		outcome.message = failure.Message
		return
	}
	var success struct {
		Reservations []struct {
			ID string `json:"id"`
		} `json:"reservations"`
	}
	if err := json.Unmarshal(payload, &success); err != nil {
		outcome.protocolErr = fmt.Errorf("decode %s success payload: %w", outcome.transport, err)
		return
	}
	outcome.reservations = len(success.Reservations)
}

// assertReservationRaceHasOneWinner is the point of the acquire/acquire test:
// exactly one racer acquires its full set, and the other fails with the
// stable RESOURCE_RESERVATION_CONFLICT domain code -- never a transport-level
// error, and never a message leaking the underlying raw SQLite busy/locked
// failure.
func assertReservationRaceHasOneWinner(t *testing.T, outcomes ...reservationOutcome) (winner, loser reservationOutcome) {
	t.Helper()
	var winners, losers int
	for _, outcome := range outcomes {
		if outcome.protocolErr != nil {
			t.Fatalf("%s racer failed with a transport-level error instead of a domain result: %v", outcome.transport, outcome.protocolErr)
		}
		if outcome.isError {
			losers++
			loser = outcome
			if outcome.code != domain.CodeResourceReservationConflict {
				t.Fatalf("%s racer's loss reported code %q, want stable domain error %q (message: %q)",
					outcome.transport, outcome.code, domain.CodeResourceReservationConflict, outcome.message)
			}
			lowerMessage := strings.ToLower(outcome.message)
			if strings.Contains(lowerMessage, "database is locked") || strings.Contains(lowerMessage, "sqlite_busy") {
				t.Fatalf("%s racer's domain error leaked a raw SQLite message instead of a clean domain error: %q",
					outcome.transport, outcome.message)
			}
			continue
		}
		winners++
		winner = outcome
		if outcome.reservations != 2 {
			t.Fatalf("%s racer acquired %d reservation(s), want the full requested set of 2", outcome.transport, outcome.reservations)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("reservation race produced %d winner(s) and %d loser(s), want exactly one of each: %#v", winners, losers, outcomes)
	}
	return winner, loser
}

func seedReadyIssue(t *testing.T, session *mcp.ClientSession, title string) string {
	t.Helper()
	created := callIntegrationTool(t, session, "create_issue", map[string]any{
		"type": "task", "title": title, "status": "ready",
	})
	var issue struct {
		ID string `json:"id"`
	}
	decodeIntegrationResult(t, created, &issue)
	if created.IsError || issue.ID == "" {
		t.Fatalf("create_issue %q result = %#v, decoded = %#v", title, created, issue)
	}
	return issue.ID
}

func claimOverStdioForReservation(t *testing.T, session *mcp.ClientSession, issueID string) leaseHandle {
	t.Helper()
	result := callIntegrationTool(t, session, "claim_issue", map[string]any{
		"issue_id": issueID, "lease_seconds": 300,
	})
	if result.IsError {
		code, message := decodeIntegrationFailure(t, result)
		t.Fatalf("stdio claim_issue on %s failed: %s: %s", issueID, code, message)
	}
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	decodeIntegrationResult(t, result, &claim)
	if claim.Attempt.ID == "" || claim.LeaseToken == "" {
		t.Fatalf("stdio claim_issue on %s returned no attempt/lease: %#v", issueID, claim)
	}
	return leaseHandle{attemptID: claim.Attempt.ID, leaseToken: claim.LeaseToken}
}

func claimOverHTTPForReservation(t *testing.T, httpClient *http.Client, endpoint, sessionID string, requestID int, issueID string) leaseHandle {
	t.Helper()
	response, err := postJSONRPC(httpClient, endpoint, sessionID, requestID, "tools/call", map[string]any{
		"name":      "claim_issue",
		"arguments": map[string]any{"issue_id": issueID, "lease_seconds": 300},
	})
	if err != nil {
		t.Fatalf("http claim_issue on %s: %v", issueID, err)
	}
	claim := decodeClaimEnvelope(t, "http", response.result)
	if claim.attemptID == "" || claim.leaseToken == "" {
		t.Fatalf("http claim_issue on %s returned no attempt/lease: %s", issueID, response.result)
	}
	return claim
}

// decodeClaimEnvelope decodes a successful claim_issue tools/call envelope
// received over HTTP, failing the test when the call reported a domain error.
func decodeClaimEnvelope(t *testing.T, label string, result json.RawMessage) leaseHandle {
	t.Helper()
	var envelope struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatalf("decode %s tools/call envelope: %v", label, err)
	}
	if envelope.IsError {
		t.Fatalf("%s claim_issue reported an error: %s", label, envelope.StructuredContent)
	}
	var claim struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	if err := json.Unmarshal(envelope.StructuredContent, &claim); err != nil {
		t.Fatalf("decode %s claim payload: %v", label, err)
	}
	return leaseHandle{attemptID: claim.Attempt.ID, leaseToken: claim.LeaseToken}
}

func decodeIntegrationFailure(t *testing.T, result *mcp.CallToolResult) (code, message string) {
	t.Helper()
	var failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	decodeIntegrationResult(t, result, &failure)
	return failure.Code, failure.Message
}

// openThirdConnection opens a connection owned by neither racer process, so
// every assertion below reads the storage layer's own settled state rather
// than either participant's view of it.
func openThirdConnection(t *testing.T, env integrationEnvironment) *sqlite.DB {
	t.Helper()
	connection, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, env), sqlite.Options{})
	if err != nil {
		t.Fatalf("open third connection to project database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := connection.Close(context.Background()); closeErr != nil {
			t.Errorf("close third connection: %v", closeErr)
		}
	})
	return connection
}

func assertActiveReservationCount(t *testing.T, env integrationEnvironment, attemptID string, want int) {
	t.Helper()
	connection := openThirdConnection(t, env)
	var got int
	err := connection.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM resource_reservations WHERE attempt_id = ? AND status = 'active'`, attemptID).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read active reservation count through third connection: %v", err)
	}
	if got != want {
		t.Fatalf("active reservations for attempt %s = %d, want %d", attemptID, got, want)
	}
}

func assertActiveAttemptCount(t *testing.T, env integrationEnvironment, issueID string, want int) {
	t.Helper()
	connection := openThirdConnection(t, env)
	var got int
	err := connection.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM work_attempts WHERE issue_id = ? AND status = 'active'`, issueID).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read active attempt count through third connection: %v", err)
	}
	if got != want {
		t.Fatalf("active attempts for issue %s = %d, want %d", issueID, got, want)
	}
}

func assertReservationsReleasedAs(t *testing.T, env integrationEnvironment, attemptID, wantReason string) {
	t.Helper()
	connection := openThirdConnection(t, env)
	var total, matching int
	err := connection.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		if err := query.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM resource_reservations WHERE attempt_id = ?`, attemptID).Scan(&total); err != nil {
			return err
		}
		return query.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM resource_reservations WHERE attempt_id = ? AND status = 'released' AND release_reason = ?`,
			attemptID, wantReason).Scan(&matching)
	})
	if err != nil {
		t.Fatalf("read reservation release state through third connection: %v", err)
	}
	if total == 0 {
		t.Fatalf("attempt %s holds no reservation rows at all, want at least one released as %q", attemptID, wantReason)
	}
	if matching != total {
		t.Fatalf("attempt %s has %d of %d reservation(s) released with reason %q, want all of them",
			attemptID, matching, total, wantReason)
	}
}

func assertAttemptStatus(t *testing.T, env integrationEnvironment, attemptID, want string) {
	t.Helper()
	connection := openThirdConnection(t, env)
	var got string
	err := connection.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT status FROM work_attempts WHERE id = ?`, attemptID).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read attempt status through third connection: %v", err)
	}
	if got != want {
		t.Fatalf("attempt %s status = %q, want %q", attemptID, got, want)
	}
}

// backdateAttemptLease moves an attempt's lease expiry into the past through
// a connection owned by neither participant, standing in for the wall-clock
// wait a real expired lease would need. The attempt row itself stays active:
// expiry is lazy, so the next claim of the same issue is what must notice.
func backdateAttemptLease(t *testing.T, env integrationEnvironment, attemptID string) {
	t.Helper()
	connection := openThirdConnection(t, env)
	past := sqlite.FormatStorageTime(time.Now().Add(-time.Hour))
	err := connection.Write(context.Background(), func(ctx context.Context, tx sqlite.Executor) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE work_attempts SET lease_expires_at = ? WHERE id = ? AND status = 'active'`, past, attemptID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("backdating lease for attempt %s updated %d rows, want 1", attemptID, affected)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("backdate attempt lease through third connection: %v", err)
	}
}
