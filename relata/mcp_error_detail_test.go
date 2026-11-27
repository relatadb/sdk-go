package relata

// #6779: an MCP tool error keeps the server's code and reason. Bodies are
// recorded from POST /v1/mcp/tools/call.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ruleDetail = "invalid condition_sql: parse error: expected '=', found 'SELECT'"

// Current server: detail, code and the HTTP problem in structuredContent.
const createRuleErrorBody = `{"content":[{"type":"text","text":"` + ruleDetail + `"}],` +
	`"isError":true,"code":"rule-condition-invalid","detail":"` + ruleDetail + `",` +
	`"structuredContent":{"type":"https://relatadb.dev/errors/rule-condition-invalid",` +
	`"title":"Bad Request","status":400,"detail":"` + ruleDetail + `","code":"rule-condition-invalid"}}`

// v3.0.0-rc.4 server: the reason only in content[0].text.
const recognizeText = "field 'id' has wrong type (expected string, got object)"
const rc4RecognizeErrorBody = `{"content":[{"type":"text","text":"` + recognizeText + `"}],` +
	`"isError":true,"code":"invalid_params"}`

func mcpErrorClient(t *testing.T, body string) *McpClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewMcpClient(newTestClient(srv, nil))
}

func mcpRelataError(t *testing.T, err error) *RelataError {
	t.Helper()
	var re *RelataError
	if !errors.As(err, &re) {
		t.Fatalf("want *RelataError, got %T: %v", err, err)
	}
	return re
}

func TestMcpClient_CreateRuleError_KeepsCodeAndDetail(t *testing.T) {
	mcp := mcpErrorClient(t, createRuleErrorBody)
	_, err := mcp.CreateRule(context.Background(), "r1", "SELECT * FROM Person WHERE name = 'x'", "Person", nil)
	re := mcpRelataError(t, err)
	if re.StatusCode != http.StatusBadRequest || re.Code != "rule-condition-invalid" || re.Message != ruleDetail {
		t.Fatalf("got status=%d code=%q message=%q", re.StatusCode, re.Code, re.Message)
	}
}

func TestMcpClient_ErrorEnvelopeTextIsTheFallbackDetail(t *testing.T) {
	mcp := mcpErrorClient(t, rc4RecognizeErrorBody)
	_, err := mcp.Recognize(context.Background(), "m1", "")
	re := mcpRelataError(t, err)
	if re.Code != "invalid_params" || re.Message != recognizeText {
		t.Fatalf("got code=%q message=%q", re.Code, re.Message)
	}
	if strings.Contains(re.Message, `"isError"`) {
		t.Fatalf("message is the raw body: %q", re.Message)
	}
}
