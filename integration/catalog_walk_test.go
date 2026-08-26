//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rhizome-mcp/internal/testutil"
)

func TestIntegrationEveryAdvertisedToolIsCallable(t *testing.T) {
	t.Parallel()
	// Tools that were never previously exercised over a real transport
	requiredTools := []string{
		"apply_issue_plan",
		"cancel_review_request",
		"get_review_request",
		"list_decisions",
		"list_labels",
		"list_review_requests",
		"replace_review_request",
	}

	t.Run("stdio", func(t *testing.T) {
		env := newIntegrationEnvironment(t)
		session := env.connect(t)
		testAllToolsCallableWithSession(t, session, requiredTools)
	})

	t.Run("http", func(t *testing.T) {
		env := newIntegrationEnvironment(t)
		server := launchIntegrationHTTPServer(t, env, "127.0.0.1:0")
		t.Cleanup(func() { stopIntegrationHTTPServer(t, server) })

		endpoint := "http://" + server.waitForEndpoint(t) + "/mcp"
		httpClient := &http.Client{Timeout: integrationTimeout}
		testAllToolsCallableWithHTTP(t, endpoint, httpClient, requiredTools)
	})
}

func testAllToolsCallableWithSession(t *testing.T, session *mcp.ClientSession, requiredTools []string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Get the advertised tool list
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools.Tools) == 0 {
		t.Fatal("no tools advertised")
	}

	// Build a set of advertised tool names
	advertisedToolNames := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		advertisedToolNames[tool.Name] = tool
	}

	// Verify all required tools exist
	for _, requiredName := range requiredTools {
		if _, exists := advertisedToolNames[requiredName]; !exists {
			t.Fatalf("required tool %q not found in advertised tools", requiredName)
		}
	}

	// Walk every tool and call it with minimal valid input
	var counter int
	for _, tool := range tools.Tools {
		tool := tool
		t.Run(tool.Name, func(t *testing.T) {
			schema, err := testutil.DecodeInputSchema(tool)
			if err != nil {
				t.Fatalf("decode input schema: %v", err)
			}

			// Build minimal input: required properties only
			input := make(map[string]any)
			if schema != nil && len(schema.Properties) > 0 {
				for _, name := range schema.Required {
					input[name] = testutil.PlaceholderValue(schema.Properties[name], &counter)
				}
			}

			// Call the tool
			callCtx, callCancel := context.WithTimeout(context.Background(), integrationTimeout)
			defer callCancel()

			result, err := session.CallTool(callCtx, &mcp.CallToolParams{
				Name:      tool.Name,
				Arguments: input,
			})

			// Protocol-level errors are failures
			if err != nil {
				t.Fatalf("CallTool protocol error: %v", err)
			}

			// Verify result is well-formed
			if result == nil {
				t.Fatalf("CallTool returned nil result")
			}

			// If it's an error, verify the structured content is properly formatted
			if result.IsError {
				if result.StructuredContent == nil {
					t.Fatalf("error result has no structuredContent: %#v", result)
				}

				// Verify it's a well-formed domain error with code and message
				errorContent, ok := result.StructuredContent.(map[string]any)
				if !ok {
					t.Fatalf("error structuredContent is not a map: %#v", result.StructuredContent)
				}

				code, hasCode := errorContent["code"]
				message, hasMessage := errorContent["message"]

				if !hasCode || !hasMessage {
					t.Fatalf("error missing code or message: %#v", errorContent)
				}

				codeStr, codeOk := code.(string)
				messageStr, msgOk := message.(string)

				if !codeOk || !msgOk {
					t.Fatalf("error code/message not strings: code=%#v, message=%#v", code, message)
				}

				if codeStr == "" {
					t.Fatalf("error code is empty")
				}
				if messageStr == "" {
					t.Fatalf("error message is empty")
				}
			}
		})
	}
}

func testAllToolsCallableWithHTTP(t *testing.T, endpoint string, client *http.Client, requiredTools []string) {
	t.Helper()

	// List tools via HTTP JSON-RPC
	resp, err := postJSONRPC(client, endpoint, "", 1, "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}

	var toolsResponse struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(resp.result, &toolsResponse); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}

	if len(toolsResponse.Tools) == 0 {
		t.Fatal("no tools advertised")
	}

	// Build a set of advertised tool names
	advertisedToolNames := make(map[string]map[string]any, len(toolsResponse.Tools))
	for _, tool := range toolsResponse.Tools {
		if name, ok := tool["name"].(string); ok {
			advertisedToolNames[name] = tool
		}
	}

	// Verify all required tools exist
	for _, requiredName := range requiredTools {
		if _, exists := advertisedToolNames[requiredName]; !exists {
			t.Fatalf("required tool %q not found in advertised tools", requiredName)
		}
	}

	// Walk every tool and call it with minimal valid input
	httpCounter := 0
	for _, toolData := range toolsResponse.Tools {
		toolName, ok := toolData["name"].(string)
		if !ok {
			t.Fatalf("advertised tool has a non-string name: %#v", toolData)
		}

		t.Run(toolName, func(t *testing.T) {
			// Decoded through the same helper the stdio walk uses, so both
			// transports derive arguments from identical type, enum and
			// pattern information. A stringly-typed placeholder would be
			// rejected by the SDK's schema-validation layer before the
			// handler ever ran, and this walk exists to reach the handlers.
			schema, err := testutil.DecodeSchema(toolData["inputSchema"])
			if err != nil {
				t.Fatalf("decode input schema: %v", err)
			}

			// Build minimal input: required properties only
			input := make(map[string]any)
			if schema != nil && len(schema.Properties) > 0 {
				for _, name := range schema.Required {
					input[name] = testutil.PlaceholderValue(schema.Properties[name], &httpCounter)
				}
			}

			// Call the tool via HTTP JSON-RPC
			resp, err := postJSONRPC(client, endpoint, "", 2, "tools/call", map[string]any{
				"name":      toolName,
				"arguments": input,
			})
			if err != nil {
				t.Fatalf("tools/call protocol error: %v", err)
			}

			// Decode the response - the result is nested in the JSON-RPC envelope
			var callResult struct {
				IsError           bool           `json:"isError"`
				StructuredContent map[string]any `json:"structuredContent"`
				Content           []any          `json:"content"`
			}
			if err := json.Unmarshal(resp.result, &callResult); err != nil {
				t.Fatalf("decode tools/call result: %v", err)
			}

			// If it's an error, verify the structured content is properly formatted
			if callResult.IsError {
				// No content-array fallback: every handler error path must
				// reach the structured MCP error envelope, which is exactly
				// the regression this walk is here to catch.
				if callResult.StructuredContent == nil {
					t.Fatalf("error result has no structuredContent: content=%#v", callResult.Content)
				}

				code, hasCode := callResult.StructuredContent["code"]
				message, hasMessage := callResult.StructuredContent["message"]

				if !hasCode || !hasMessage {
					t.Fatalf("error missing code or message: %#v", callResult.StructuredContent)
				}

				codeStr, codeOk := code.(string)
				messageStr, msgOk := message.(string)

				if !codeOk || !msgOk {
					t.Fatalf("error code/message not strings: code=%#v, message=%#v", code, message)
				}

				if codeStr == "" {
					t.Fatalf("error code is empty")
				}
				if messageStr == "" {
					t.Fatalf("error message is empty")
				}
			}
		})
	}
}
