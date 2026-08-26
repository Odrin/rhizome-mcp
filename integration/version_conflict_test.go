//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestIntegrationVersionConflict(t *testing.T) {
	t.Parallel()
	env := newIntegrationEnvironment(t)
	attached := env.attach()

	serverA := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
	t.Cleanup(func() { stopIntegrationHTTPServer(t, serverA) })
	serverB := launchIntegrationHTTPServer(t, attached, "127.0.0.1:0")
	t.Cleanup(func() { killIntegrationHTTPServer(t, serverB) })

	endpointA := "http://" + serverA.waitForEndpoint(t) + "/mcp"
	endpointB := "http://" + serverB.waitForEndpoint(t) + "/mcp"

	_, sessionIDA, err := communicateThroughHTTP(t, endpointA, "version-conflict-a")
	if err != nil {
		t.Fatalf("initialize server A session: %v\nstderr:\n%s", err, serverA.output.String())
	}
	_, sessionIDB, err := communicateThroughHTTP(t, endpointB, "version-conflict-b")
	if err != nil {
		t.Fatalf("initialize server B session: %v\nstderr:\n%s", err, serverB.output.String())
	}

	httpClient := &http.Client{Timeout: integrationTimeout}

	createResult, err := postJSONRPC(httpClient, endpointA, sessionIDA, 10, "tools/call", map[string]any{
		"name":      "create_issue",
		"arguments": map[string]any{"type": "task", "title": "version conflict test"},
	})
	if err != nil {
		t.Fatalf("create_issue on server A: %v", err)
	}

	var created struct {
		StructuredContent struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(createResult.result, &created); err != nil {
		t.Fatalf("decode create_issue result: %v", err)
	}
	if created.StructuredContent.ID == "" {
		t.Fatalf("create_issue returned no id: %s", createResult.result)
	}

	expectedVersion := created.StructuredContent.Version

	var resultA, resultB *jsonRPCResponse
	var resultsMu sync.Mutex
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-startCh

		result, callErr := postJSONRPC(httpClient, endpointA, sessionIDA, 20, "tools/call", map[string]any{
			"name": "update_issue",
			"arguments": map[string]any{
				"issue_id":         created.StructuredContent.ID,
				"expected_version": expectedVersion,
				"changes": map[string]any{
					"title": "updated by server A",
				},
			},
		})
		if callErr != nil {
			t.Errorf("update_issue on server A: %v", callErr)
			return
		}

		resultsMu.Lock()
		defer resultsMu.Unlock()
		resultA = result
	}()

	go func() {
		defer wg.Done()
		<-startCh

		result, callErr := postJSONRPC(httpClient, endpointB, sessionIDB, 21, "tools/call", map[string]any{
			"name": "update_issue",
			"arguments": map[string]any{
				"issue_id":         created.StructuredContent.ID,
				"expected_version": expectedVersion,
				"changes": map[string]any{
					"title": "updated by server B",
				},
			},
		})
		if callErr != nil {
			t.Errorf("update_issue on server B: %v", callErr)
			return
		}

		resultsMu.Lock()
		defer resultsMu.Unlock()
		resultB = result
	}()

	close(startCh)
	wg.Wait()

	if resultA == nil || resultB == nil {
		t.Fatalf("failed to execute both update_issue calls")
	}

	var resultADecoded map[string]any
	var resultBDecoded map[string]any

	if err := json.Unmarshal(resultA.result, &resultADecoded); err != nil {
		t.Fatalf("decode result A: %v", err)
	}
	if err := json.Unmarshal(resultB.result, &resultBDecoded); err != nil {
		t.Fatalf("decode result B: %v", err)
	}

	getStringField := func(data map[string]any, field string) string {
		if sc, ok := data["structuredContent"].(map[string]any); ok {
			if val, ok := sc[field].(string); ok {
				return val
			}
		}
		return ""
	}

	getVersionFromIssue := func(data map[string]any) int64 {
		if sc, ok := data["structuredContent"].(map[string]any); ok {
			if issue, ok := sc["issue"].(map[string]any); ok {
				if ver, ok := issue["version"].(float64); ok {
					return int64(ver)
				}
			}
		}
		return 0
	}

	codeA := getStringField(resultADecoded, "code")
	codeB := getStringField(resultBDecoded, "code")
	versionA := getVersionFromIssue(resultADecoded)
	versionB := getVersionFromIssue(resultBDecoded)

	var loserTitle, loserEndpoint, loserSession string

	if codeA == "VERSION_CONFLICT" {
		if versionB != expectedVersion+1 {
			t.Fatalf("server B version = %d, want %d", versionB, expectedVersion+1)
		}
		loserTitle = "updated by server A"
		loserEndpoint = endpointA
		loserSession = sessionIDA
	} else if codeB == "VERSION_CONFLICT" {
		if versionA != expectedVersion+1 {
			t.Fatalf("server A version = %d, want %d", versionA, expectedVersion+1)
		}
		loserTitle = "updated by server B"
		loserEndpoint = endpointB
		loserSession = sessionIDB
	} else {
		t.Fatalf("neither result was a VERSION_CONFLICT; resultA code=%q, resultB code=%q", codeA, codeB)
	}

	getBoolField := func(data map[string]any, field string) bool {
		if sc, ok := data["structuredContent"].(map[string]any); ok {
			if val, ok := sc[field].(bool); ok {
				return val
			}
		}
		return false
	}

	var loserResult map[string]any
	if codeA == "VERSION_CONFLICT" {
		loserResult = resultADecoded
	} else {
		loserResult = resultBDecoded
	}

	retryable := getBoolField(loserResult, "retryable")
	if !retryable {
		t.Fatalf("loser error retryable = %v, want true", retryable)
	}

	getLoserEndpoint := loserEndpoint
	getLoserSession := loserSession

	getResult, err := postJSONRPC(httpClient, getLoserEndpoint, getLoserSession, 30, "tools/call", map[string]any{
		"name":      "get_issue",
		"arguments": map[string]any{"issue_id": created.StructuredContent.ID, "view": "full"},
	})
	if err != nil {
		t.Fatalf("get_issue on loser endpoint: %v", err)
	}

	var getIssueDecoded map[string]any
	if err := json.Unmarshal(getResult.result, &getIssueDecoded); err != nil {
		t.Fatalf("decode get_issue result: %v", err)
	}

	getVersionDirect := func(data map[string]any) int64 {
		if sc, ok := data["structuredContent"].(map[string]any); ok {
			if ver, ok := sc["version"].(float64); ok {
				return int64(ver)
			}
		}
		return 0
	}

	currentVersion := getVersionDirect(getIssueDecoded)
	if currentVersion != expectedVersion+1 {
		t.Fatalf("current version after winner's update = %d, want %d", currentVersion, expectedVersion+1)
	}

	retryResult, err := postJSONRPC(httpClient, getLoserEndpoint, getLoserSession, 40, "tools/call", map[string]any{
		"name": "update_issue",
		"arguments": map[string]any{
			"issue_id":         created.StructuredContent.ID,
			"expected_version": currentVersion,
			"changes": map[string]any{
				"title": loserTitle,
			},
		},
	})
	if err != nil {
		t.Fatalf("retry update_issue on loser endpoint: %v", err)
	}

	var retriedDecoded map[string]any
	if err := json.Unmarshal(retryResult.result, &retriedDecoded); err != nil {
		t.Fatalf("decode retry result: %v", err)
	}

	retriedVersion := getVersionFromIssue(retriedDecoded)
	if retriedVersion != expectedVersion+2 {
		t.Fatalf("version after retry = %d, want %d", retriedVersion, expectedVersion+2)
	}

	finalGetResult, err := postJSONRPC(httpClient, endpointA, sessionIDA, 50, "tools/call", map[string]any{
		"name":      "get_issue",
		"arguments": map[string]any{"issue_id": created.StructuredContent.ID, "view": "full"},
	})
	if err != nil {
		t.Fatalf("final get_issue: %v", err)
	}

	var finalIssueDecoded map[string]any
	if err := json.Unmarshal(finalGetResult.result, &finalIssueDecoded); err != nil {
		t.Fatalf("decode final get_issue result: %v", err)
	}

	finalVersion := getVersionDirect(finalIssueDecoded)
	if finalVersion != expectedVersion+2 {
		t.Fatalf("final version = %d, want %d", finalVersion, expectedVersion+2)
	}

	getFinalTitle := func(data map[string]any) string {
		if sc, ok := data["structuredContent"].(map[string]any); ok {
			if title, ok := sc["title"].(string); ok {
				return title
			}
		}
		return ""
	}

	finalTitle := getFinalTitle(finalIssueDecoded)
	if finalTitle != loserTitle {
		t.Fatalf("final title = %q, want %q (from retry)", finalTitle, loserTitle)
	}
}
