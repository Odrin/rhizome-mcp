package mcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/adapters/sqlite"
)

// TestRawLeaseTokenNeverAppearsOutsideClaimIssueResponse is ISSUE-193's AC3/AC4
// cross-cutting audit, scoped to the surfaces where a raw secret can actually
// reach a marshaled response: it claims an issue, captures the real lease
// token, walks every catalog tool (except claim_issue itself, which
// legitimately returns a fresh token on every real claim) with placeholder
// arguments, and asserts the token value never appears in any response. It
// also checks export_project, get_changes, and the work_attempts and
// issue_events tables directly.
//
// Board HTTP and CLI issue show/list are deliberately not covered here: their
// response types (domain.BoardResult, domain.IssueDetail, domain.Issue,
// domain.IssueList, domain.WorkAttempt) never declare a lease-token-shaped
// field in the first place (verified directly against internal/domain --
// only the *Input structs for finish_attempt/renew_attempt/save_attempt_note
// carry LeaseToken, and those are request-side, never serialized back out),
// so a raw token cannot reach those surfaces structurally, not just by
// convention.
func TestRawLeaseTokenNeverAppearsOutsideClaimIssueResponse(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "lease-token-egress.db"))
	defer db.Close(ctx)
	options := composeServices(t, db, source)
	client, stop := newClient(t, options)
	defer stop()

	created := call(t, client, "create_issue", map[string]any{"type": "task", "title": "lease token egress", "status": "ready"})
	var createdIssue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, created, &createdIssue)

	claimed := call(t, client, "claim_issue", map[string]any{"issue_id": createdIssue.ID})
	var claimResult struct {
		LeaseToken string `json:"lease_token"`
		Attempt    struct {
			ID string `json:"id"`
		} `json:"attempt"`
	}
	decodeStructured(t, claimed, &claimResult)
	token := claimResult.LeaseToken
	if token == "" {
		t.Fatal("claim_issue did not return a lease token")
	}

	note := call(t, client, "save_attempt_note", map[string]any{
		"attempt_id": claimResult.Attempt.ID, "lease_token": token, "kind": "progress", "content": "audit note",
	})
	if note.IsError {
		t.Fatalf("save_attempt_note failed: %#v", note)
	}
	assertNoRawToken(t, "save_attempt_note", note, token)

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "claim_issue" {
			continue
		}
		tool := tool
		t.Run(tool.Name, func(t *testing.T) {
			schema := decodeInputSchema(t, tool)
			var counter int
			base := make(map[string]any, len(schema.Required))
			for _, name := range schema.Required {
				base[name] = placeholderValue(schema.Properties[name], &counter)
			}
			result := call(t, client, tool.Name, base)
			assertNoRawToken(t, tool.Name, result, token)
		})
	}

	export := call(t, client, "export_project", map[string]any{})
	assertNoRawToken(t, "export_project", export, token)

	changes := call(t, client, "get_changes", map[string]any{"since_event_id": 0})
	assertNoRawToken(t, "get_changes", changes, token)

	if err := db.Read(ctx, func(ctx context.Context, query sqlite.Queryer) error {
		for _, table := range []string{"work_attempts", "issue_events", "idempotency_records"} {
			rows, err := query.QueryContext(ctx, "SELECT * FROM "+table) //nolint:gosec // table is a fixed literal from the loop above, not user input
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for index := range values {
					pointers[index] = &values[index]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					return err
				}
				for index, value := range values {
					text, ok := value.(string)
					if !ok {
						if raw, ok := value.([]byte); ok {
							text = string(raw)
						} else {
							continue
						}
					}
					if strings.Contains(text, token) {
						rows.Close()
						t.Fatalf("table %s column %s contains the raw lease token", table, columns[index])
					}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		return nil
	}); err != nil {
		t.Fatalf("db.Read() error = %v", err)
	}
}

func TestLeaseTokenAndHandleSchemaErrorsDoNotReflectCredentials(t *testing.T) {
	ctx := context.Background()
	db, source := openDatabase(t, filepath.Join(t.TempDir(), "credential-schema-egress.db"))
	defer db.Close(ctx)
	client, stop := newClient(t, composeServices(t, db, source))
	defer stop()
	issued := call(t, client, "create_agent_session", map[string]any{"client_name": "credential-egress-test"})
	var session struct {
		Handle string `json:"agent_session_handle"`
	}
	decodeStructured(t, issued, &session)
	created := call(t, client, "create_issue", map[string]any{"type": "task", "title": "credential schema egress", "status": "ready"})
	var issue struct {
		ID string `json:"id"`
	}
	decodeStructured(t, created, &issue)
	claimed := call(t, client, "claim_issue", map[string]any{"issue_id": issue.ID})
	var claim struct {
		LeaseToken string `json:"lease_token"`
		Attempt    struct {
			ID string `json:"id"`
		} `json:"attempt"`
	}
	decodeStructured(t, claimed, &claim)
	if session.Handle == "" || claim.LeaseToken == "" {
		t.Fatal("fixture did not issue both credentials")
	}
	for _, credential := range []struct {
		field  string
		tool   string
		secret string
		limit  int
	}{
		{"agent_session_handle", "get_project", session.Handle, 256},
		{"agent_session_handle", "end_agent_session", session.Handle, 256},
		{"lease_token", "renew_attempt", claim.LeaseToken, 512},
	} {
		for _, malformed := range []struct {
			name  string
			value any
		}{
			{"array", []any{credential.secret}},
			{"object", map[string]any{"value": credential.secret}},
			{"oversize", strings.Repeat("x", credential.limit+1) + credential.secret},
			{"null", nil},
		} {
			if malformed.name == "null" && credential.tool == "get_project" {
				continue
			}
			t.Run(credential.tool+"/"+malformed.name, func(t *testing.T) {
				args := map[string]any{credential.field: malformed.value}
				if credential.field == "lease_token" {
					args["attempt_id"] = claim.Attempt.ID
				}
				result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: credential.tool, Arguments: args})
				assertNoCredentialFragments(t, result, err, session.Handle, claim.LeaseToken)
				if err != nil {
					t.Fatal("malformed credential returned a protocol error instead of a structured validation result")
				}
				if result == nil || !result.IsError {
					t.Fatal("malformed credential was accepted")
				}
				assertDomainError(t, result, "INVALID_ARGUMENT", false)
				var output struct {
					Details []struct {
						Field string `json:"field"`
						Code  string `json:"code"`
					} `json:"details"`
				}
				decodeStructured(t, result, &output)
				wantCode := "INVALID"
				if credential.field == "agent_session_handle" {
					wantCode = "INVALID_HANDLE"
				}
				if len(output.Details) != 1 || output.Details[0].Field != credential.field || output.Details[0].Code != wantCode {
					t.Fatal("malformed credential has incorrect structured error details")
				}
			})
		}
	}
	for _, valid := range []struct {
		tool string
		args map[string]any
	}{
		{"get_project", map[string]any{"agent_session_handle": session.Handle}},
		{"get_project", map[string]any{"agent_session_handle": nil}},
		{"renew_attempt", map[string]any{"attempt_id": claim.Attempt.ID, "lease_token": claim.LeaseToken}},
	} {
		result, err := client.CallTool(ctx, &sdkmcp.CallToolParams{Name: valid.tool, Arguments: valid.args})
		assertNoCredentialFragments(t, result, err, session.Handle, claim.LeaseToken)
		if err != nil || result == nil || result.IsError {
			t.Fatal("valid credential request failed")
		}
	}
}

func assertNoCredentialFragments(t *testing.T, result *sdkmcp.CallToolResult, callErr error, credentials ...string) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal("could not serialize tool result for secret-egress assertion")
	}
	output := string(raw)
	if callErr != nil {
		output += callErr.Error()
	}
	for _, credential := range credentials {
		for start := 0; start+8 <= len(credential); start++ {
			if strings.Contains(output, credential[start:start+8]) {
				t.Fatal("tool result or protocol error contains an issued credential fragment")
			}
		}
	}
}

func assertNoRawToken(t *testing.T, toolName string, result *sdkmcp.CallToolResult, token string) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal %s result: %v", toolName, err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("%s response contains the raw lease token: %s", toolName, raw)
	}
}
