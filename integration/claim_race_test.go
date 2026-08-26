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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
	"rhizome-mcp/internal/domain"
)

// TestIntegrationClaimRace is the ISSUE-103 proof test: two independent OS
// processes (one HTTP server, one stdio server) attached to the same
// repository and data root race claim_issue on the same issue. The in-process
// TestAttemptSimultaneousClaimsHaveOneWinner
// (internal/adapters/sqlite/attempts_test.go) already proves one-winner
// semantics with both racers sharing a connection pool; this test proves it
// still holds when the SQLite WAL writer lock is contended across process
// boundaries, and that a loser observes a stable ACTIVE_ATTEMPT_EXISTS domain
// error rather than a raw SQLITE_BUSY/"database is locked" failure.
func TestIntegrationClaimRace(t *testing.T) {
	t.Parallel()
	const raceIterations = 5

	env := newIntegrationEnvironment(t)
	attached := env.attach()

	// Seed every iteration's issue up front, through one session, before any
	// racer process exists: once an iteration's issue is claimed the issue is
	// no longer claimable, so reusing a single issue across iterations would
	// make every iteration after the first a guaranteed double-loss instead
	// of a race.
	seedSession := env.connect(t)
	issueIDs := make([]string, raceIterations)
	for i := range issueIDs {
		created := callIntegrationTool(t, seedSession, "create_issue", map[string]any{
			"type":   "task",
			"title":  fmt.Sprintf("claim race issue %d", i),
			"status": "ready",
		})
		var issue struct {
			ID string `json:"id"`
		}
		decodeIntegrationResult(t, created, &issue)
		if created.IsError || issue.ID == "" {
			t.Fatalf("create_issue result = %#v, decoded = %#v", created, issue)
		}
		issueIDs[i] = issue.ID
	}

	serverA := launchIntegrationHTTPServer(t, attached, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, serverA) })
	endpointA := "http://" + serverA.waitForEndpoint(t) + "/mcp"

	httpClient := &http.Client{Timeout: integrationTimeout}
	_, httpSessionID, err := communicateThroughHTTP(t, endpointA, "claim-race-http")
	if err != nil {
		t.Fatalf("initialize HTTP racer session: %v\nstderr:\n%s", err, serverA.output.String())
	}

	stdioSession := attached.connect(t)

	for i, issueID := range issueIDs {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			httpOutcome, stdioOutcome := raceClaimIssue(httpClient, endpointA, httpSessionID, 100+i, stdioSession, issueID)
			assertClaimRaceHasOneWinner(t, httpOutcome, stdioOutcome)
			assertExactlyOneActiveAttempt(t, attached, issueID)
		})
	}
}

// claimOutcome is the result of one racer's claim_issue call. protocolErr
// distinguishes a transport-level failure (unexpected: the race should never
// produce one) from a domain-level result (isError plus code/message, or a
// successful claim).
type claimOutcome struct {
	transport   string
	protocolErr error
	isError     bool
	attemptID   string
	leaseToken  string
	code        string
	message     string
}

// raceClaimIssue fires both racers' claim_issue calls as close to
// simultaneously as the harness allows: each goroutine signals readyWG once
// it has nothing left to do but issue the call, and both are released
// together only after readyWG.Wait returns, so the two RPCs actually overlap
// instead of serializing behind goroutine startup.
func raceClaimIssue(httpClient *http.Client, endpoint, httpSessionID string, requestID int, stdioSession *mcp.ClientSession, issueID string) (httpOutcome, stdioOutcome claimOutcome) {
	var readyWG sync.WaitGroup
	readyWG.Add(2)
	start := make(chan struct{})
	httpDone := make(chan claimOutcome, 1)
	stdioDone := make(chan claimOutcome, 1)

	go func() {
		readyWG.Done()
		<-start
		httpDone <- claimIssueOverHTTP(httpClient, endpoint, httpSessionID, requestID, issueID)
	}()
	go func() {
		readyWG.Done()
		<-start
		stdioDone <- claimIssueOverStdio(stdioSession, issueID)
	}()

	readyWG.Wait()
	close(start)

	return <-httpDone, <-stdioDone
}

func claimIssueOverStdio(session *mcp.ClientSession, issueID string) claimOutcome {
	outcome := claimOutcome{transport: "stdio"}
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "claim_issue",
		Arguments: map[string]any{"issue_id": issueID, "lease_seconds": 300},
	})
	if err != nil {
		outcome.protocolErr = fmt.Errorf("stdio claim_issue protocol error: %w", err)
		return outcome
	}
	outcome.isError = result.IsError
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		outcome.protocolErr = fmt.Errorf("marshal stdio structured content: %w", err)
		return outcome
	}
	if result.IsError {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &failure); err != nil {
			outcome.protocolErr = fmt.Errorf("decode stdio error payload: %w", err)
			return outcome
		}
		outcome.code = failure.Code
		outcome.message = failure.Message
		return outcome
	}
	var success struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	if err := json.Unmarshal(data, &success); err != nil {
		outcome.protocolErr = fmt.Errorf("decode stdio success payload: %w", err)
		return outcome
	}
	outcome.attemptID = success.Attempt.ID
	outcome.leaseToken = success.LeaseToken
	return outcome
}

func claimIssueOverHTTP(httpClient *http.Client, endpoint, sessionID string, requestID int, issueID string) claimOutcome {
	outcome := claimOutcome{transport: "http"}
	response, err := postJSONRPC(httpClient, endpoint, sessionID, requestID, "tools/call", map[string]any{
		"name":      "claim_issue",
		"arguments": map[string]any{"issue_id": issueID, "lease_seconds": 300},
	})
	if err != nil {
		outcome.protocolErr = fmt.Errorf("http claim_issue protocol error: %w", err)
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
	if envelope.IsError {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.StructuredContent, &failure); err != nil {
			outcome.protocolErr = fmt.Errorf("decode http error payload: %w", err)
			return outcome
		}
		outcome.code = failure.Code
		outcome.message = failure.Message
		return outcome
	}
	var success struct {
		Attempt struct {
			ID string `json:"id"`
		} `json:"attempt"`
		LeaseToken string `json:"lease_token"`
	}
	if err := json.Unmarshal(envelope.StructuredContent, &success); err != nil {
		outcome.protocolErr = fmt.Errorf("decode http success payload: %w", err)
		return outcome
	}
	outcome.attemptID = success.Attempt.ID
	outcome.leaseToken = success.LeaseToken
	return outcome
}

// assertClaimRaceHasOneWinner is the point of this test: exactly one racer
// must succeed with a non-empty attempt_id and lease_token, and the other
// must fail with the stable ACTIVE_ATTEMPT_EXISTS domain code — never a
// transport-level error, and never a message that leaks the underlying raw
// SQLite busy/locked failure.
func assertClaimRaceHasOneWinner(t *testing.T, outcomes ...claimOutcome) {
	t.Helper()
	var winners, losers int
	for _, outcome := range outcomes {
		if outcome.protocolErr != nil {
			t.Fatalf("%s racer failed with a transport-level error instead of a domain result: %v", outcome.transport, outcome.protocolErr)
		}
		if outcome.isError {
			losers++
			if outcome.code != domain.CodeActiveAttemptExists {
				t.Fatalf("%s racer's loss reported code %q, want stable domain error %q (message: %q)",
					outcome.transport, outcome.code, domain.CodeActiveAttemptExists, outcome.message)
			}
			lowerMessage := strings.ToLower(outcome.message)
			if strings.Contains(lowerMessage, "database is locked") || strings.Contains(lowerMessage, "sqlite_busy") {
				t.Fatalf("%s racer's domain error leaked a raw SQLite message instead of a clean domain error: %q",
					outcome.transport, outcome.message)
			}
			continue
		}
		winners++
		if outcome.attemptID == "" || outcome.leaseToken == "" {
			t.Fatalf("%s racer reported success but is missing attempt_id/lease_token: %#v", outcome.transport, outcome)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("claim race produced %d winner(s) and %d loser(s), want exactly one of each: %#v", winners, losers, outcomes)
	}
}

// assertExactlyOneActiveAttempt reads through a third SQLite connection —
// neither racer process's own pool — confirming the storage layer itself
// settled on a single writer for issueID rather than two processes each
// believing they hold the lease.
func assertExactlyOneActiveAttempt(t *testing.T, env integrationEnvironment, issueID string) {
	t.Helper()
	thirdConnection, err := sqlite.Open(context.Background(), mustProjectDatabasePath(t, env), sqlite.Options{})
	if err != nil {
		t.Fatalf("open third connection to project database: %v", err)
	}
	defer func() {
		if closeErr := thirdConnection.Close(context.Background()); closeErr != nil {
			t.Errorf("close third connection: %v", closeErr)
		}
	}()
	var activeCount int
	err = thirdConnection.Read(context.Background(), func(ctx context.Context, query sqlite.Queryer) error {
		return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_attempts WHERE issue_id = ? AND status = 'active'`, issueID).Scan(&activeCount)
	})
	if err != nil {
		t.Fatalf("read active attempt count through third connection: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active attempts for issue %s = %d, want exactly 1", issueID, activeCount)
	}
}
