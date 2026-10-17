package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectClientAgent(t *testing.T) {
	tests := []struct {
		name        string
		userAgent   string
		wantName    string
		wantVersion string
	}{
		{name: "claude code", userAgent: "claude-cli/1.2.3 (external, cli)", wantName: "Claude Code", wantVersion: "1.2.3"},
		{name: "cursor", userAgent: "Cursor/0.48.7 (darwin arm64)", wantName: "Cursor", wantVersion: "0.48.7"},
		{name: "cline", userAgent: "Cline/3.4.0", wantName: "Cline", wantVersion: "3.4.0"},
		{name: "roo code", userAgent: "Roo-Code/2.1.0", wantName: "Roo Code", wantVersion: "2.1.0"},
		{name: "openai python sdk", userAgent: "OpenAI/Python 1.63.2", wantName: "OpenAI Python SDK", wantVersion: "1.63.2"},
		{name: "openai node sdk", userAgent: "openai-node/4.75.0", wantName: "OpenAI Node SDK", wantVersion: "4.75.0"},
		{name: "anthropic sdk", userAgent: "Anthropic/Python 0.45.0", wantName: "Anthropic Python SDK", wantVersion: "0.45.0"},
		{name: "curl", userAgent: "curl/8.7.1", wantName: "curl", wantVersion: "8.7.1"},
		{name: "go client", userAgent: "Go-http-client/1.1", wantName: "Go HTTP Client", wantVersion: "1.1"},
		{name: "codex cli", userAgent: "codex_cli_rs/0.20.0 (macos)", wantName: "Codex CLI", wantVersion: "0.20.0"},
		{name: "case insensitive", userAgent: "CURL/8.0.0", wantName: "curl", wantVersion: "8.0.0"},
		{name: "no version", userAgent: "PostmanRuntime", wantName: "Postman", wantVersion: ""},
		{name: "browser is not an agent", userAgent: "Mozilla/5.0 (Windows NT 10.0) Chrome/124.0", wantName: "", wantVersion: ""},
		{name: "empty", userAgent: "   ", wantName: "", wantVersion: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, version := DetectClientAgent(test.userAgent)
			assert.Equal(t, test.wantName, name)
			assert.Equal(t, test.wantVersion, version)
		})
	}
}

func TestClientAgentLabel(t *testing.T) {
	assert.Equal(t, "Cline 3.4.0", ClientAgentLabel("Cline/3.4.0"))
	assert.Equal(t, "Postman", ClientAgentLabel("PostmanRuntime"))
	assert.Equal(t, "", ClientAgentLabel("Mozilla/5.0"))
	assert.Equal(t, "Claude Code 1.2.3", ClientAgentLabel("claude-cli/1.2.3"))
}

func TestAppendRelayLogAdminInfoRecordsClientAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("User-Agent", "Cursor/0.48.7")

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Cursor 0.48.7", adminInfo["client_agent"])
	assert.Equal(t, "Cursor/0.48.7", adminInfo["user_agent"])
}

func TestAppendRelayLogAdminInfoKeepsUnknownUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("User-Agent", "SomeCustomClient/9.9")

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, adminInfo, "client_agent", "an unrecognized client has no normalized label")
	assert.Equal(t, "SomeCustomClient/9.9", adminInfo["user_agent"])
}

func TestAppendRelayLogAdminInfoTruncatesLongUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Set("User-Agent", strings.Repeat("a", clientAgentMaxUserAgentLength+100))

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)
	adminInfo, ok := other.Snapshot()["admin_info"].(map[string]any)
	require.True(t, ok)
	rawUserAgent, ok := adminInfo["user_agent"].(string)
	require.True(t, ok)
	assert.Len(t, rawUserAgent, clientAgentMaxUserAgentLength)
}

func TestAppendRelayLogAdminInfoOmitsMissingUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Request.Header.Del("User-Agent")

	other := model.NewLogOther()
	AppendRelayLogAdminInfo(ctx, nil, other)
	adminInfo, _ := other.Snapshot()["admin_info"].(map[string]any)
	assert.NotContains(t, adminInfo, "client_agent")
	assert.NotContains(t, adminInfo, "user_agent")
}
