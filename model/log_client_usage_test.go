package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedClientAgentUsageLog(t *testing.T, logType int, clientAgent, rawUserAgent string, quota int, promptTokens, completionTokens int, createdAt int64) {
	t.Helper()
	adminInfo := map[string]any{}
	if clientAgent != "" {
		adminInfo["client_agent"] = clientAgent
	}
	if rawUserAgent != "" {
		adminInfo["user_agent"] = rawUserAgent
	}
	other := ""
	if len(adminInfo) > 0 {
		other = common.MapToJsonStr(map[string]any{"admin_info": adminInfo})
	}
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           1,
		CreatedAt:        createdAt,
		Type:             logType,
		ModelName:        "gpt-4o",
		Quota:            quota,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Other:            other,
	}).Error)
}

func TestSumClientAgentUsageAggregatesKnownAndUnknownClients(t *testing.T) {
	truncateTables(t)

	seedClientAgentUsageLog(t, LogTypeConsume, "Cursor 0.48.7", "Cursor/0.48.7", 100, 10, 20, 1000)
	seedClientAgentUsageLog(t, LogTypeConsume, "Cursor 0.48.7", "Cursor/0.48.7", 50, 5, 5, 2000)
	seedClientAgentUsageLog(t, LogTypeError, "Cursor 0.48.7", "Cursor/0.48.7", 0, 0, 0, 3000)
	seedClientAgentUsageLog(t, LogTypeConsume, "Claude Code 1.2.3", "claude-cli/1.2.3", 25, 1, 2, 4000)
	seedClientAgentUsageLog(t, LogTypeConsume, "", "SomeCustomClient/9.9", 10, 0, 0, 5000)
	seedClientAgentUsageLog(t, LogTypeConsume, "", "SomeCustomClient/9.9", 10, 0, 0, 6000)
	seedClientAgentUsageLog(t, LogTypeConsume, "", "", 5, 0, 0, 7000)

	result, err := SumClientAgentUsage(ClientAgentUsageQuery{})
	require.NoError(t, err)
	require.False(t, result.Truncated)
	assert.Equal(t, 7, result.Scanned)

	require.Len(t, result.Items, 2)
	assert.Equal(t, "Cursor 0.48.7", result.Items[0].ClientAgent)
	assert.Equal(t, int64(2), result.Items[0].Requests)
	assert.Equal(t, int64(1), result.Items[0].Errors)
	assert.Equal(t, int64(150), result.Items[0].Quota)
	assert.Equal(t, int64(15), result.Items[0].PromptTokens)
	assert.Equal(t, int64(25), result.Items[0].CompletionTokens)
	assert.Equal(t, int64(3000), result.Items[0].LastSeen)
	assert.Equal(t, "Claude Code 1.2.3", result.Items[1].ClientAgent)
	assert.Equal(t, int64(1), result.Items[1].Requests)

	require.Len(t, result.Unknown, 2)
	assert.Equal(t, "SomeCustomClient/9.9", result.Unknown[0].RawUserAgent)
	assert.Equal(t, int64(2), result.Unknown[0].Requests)
	assert.Equal(t, "", result.Unknown[1].RawUserAgent, "logs without any client identity stay in one bucket")
}

func TestSumClientAgentUsageHonorsFiltersAndLimit(t *testing.T) {
	truncateTables(t)

	seedClientAgentUsageLog(t, LogTypeConsume, "Cursor 0.48.7", "Cursor/0.48.7", 100, 0, 0, 1000)
	seedClientAgentUsageLog(t, LogTypeConsume, "Cline 3.4.0", "Cline/3.4.0", 100, 0, 0, 2000)
	seedClientAgentUsageLog(t, LogTypeConsume, "Claude Code 1.2.3", "claude-cli/1.2.3", 100, 0, 0, 3000)

	result, err := SumClientAgentUsage(ClientAgentUsageQuery{StartTimestamp: 1500, EndTimestamp: 2500})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "Cline 3.4.0", result.Items[0].ClientAgent)

	limited, err := SumClientAgentUsage(ClientAgentUsageQuery{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, limited.Scanned)
	assert.True(t, limited.Truncated)
}

func TestSumClientAgentUsageIgnoresMalformedOther(t *testing.T) {
	truncateTables(t)

	require.NoError(t, LOG_DB.Create(&Log{UserId: 1, CreatedAt: 1000, Type: LogTypeConsume, Other: "{not-json"}).Error)
	require.NoError(t, LOG_DB.Create(&Log{UserId: 1, CreatedAt: 2000, Type: LogTypeConsume, Other: ""}).Error)

	result, err := SumClientAgentUsage(ClientAgentUsageQuery{})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Scanned)
	require.Len(t, result.Unknown, 1)
	assert.Equal(t, "", result.Unknown[0].RawUserAgent)
}

func seedClientAgentDetailLog(t *testing.T, clientAgent, rawUserAgent string, createdAt int64, modelName string) {
	t.Helper()
	adminInfo := map[string]any{}
	if clientAgent != "" {
		adminInfo["client_agent"] = clientAgent
	}
	if rawUserAgent != "" {
		adminInfo["user_agent"] = rawUserAgent
	}
	other := ""
	if len(adminInfo) > 0 {
		other = common.MapToJsonStr(map[string]any{"admin_info": adminInfo})
	}
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           1,
		CreatedAt:        createdAt,
		Type:             LogTypeConsume,
		ModelName:        modelName,
		TokenName:        "test-token",
		Quota:            10,
		RequestId:        "req-" + modelName,
		Content:          "consume " + modelName,
		Other:            other,
		PromptTokens:     1,
		CompletionTokens: 2,
	}).Error)
}

func TestListClientAgentLogsFiltersByBucketAndPaginates(t *testing.T) {
	truncateTables(t)

	seedClientAgentDetailLog(t, "Cursor 0.48.7", "Cursor/0.48.7", 1000, "model-a")
	seedClientAgentDetailLog(t, "Cursor 0.48.7", "Cursor/0.48.7", 2000, "model-b")
	seedClientAgentDetailLog(t, "Cursor 0.48.7", "Cursor/0.48.7", 3000, "model-c")
	seedClientAgentDetailLog(t, "Cline 3.4.0", "Cline/3.4.0", 4000, "model-d")
	seedClientAgentDetailLog(t, "", "UnknownTool/1.0", 5000, "model-e")

	first, err := ListClientAgentLogs(ClientAgentLogQuery{ClientAgent: "Cursor 0.48.7", Page: 1, PageSize: 2})
	require.NoError(t, err)
	assert.Equal(t, 3, first.Total)
	require.Len(t, first.Items, 2)
	assert.Equal(t, "model-c", first.Items[0].ModelName, "newest log comes first")
	assert.Equal(t, "model-b", first.Items[1].ModelName)

	second, err := ListClientAgentLogs(ClientAgentLogQuery{ClientAgent: "Cursor 0.48.7", Page: 2, PageSize: 2})
	require.NoError(t, err)
	assert.Equal(t, 3, second.Total)
	require.Len(t, second.Items, 1)
	assert.Equal(t, "model-a", second.Items[0].ModelName)

	unknown, err := ListClientAgentLogs(ClientAgentLogQuery{Unknown: true, RawUserAgent: "UnknownTool/1.0"})
	require.NoError(t, err)
	assert.Equal(t, 1, unknown.Total)
	require.Len(t, unknown.Items, 1)
	assert.Equal(t, "model-e", unknown.Items[0].ModelName)

	other, err := ListClientAgentLogs(ClientAgentLogQuery{ClientAgent: "Cline 3.4.0"})
	require.NoError(t, err)
	assert.Equal(t, 1, other.Total)
	assert.Equal(t, "model-d", other.Items[0].ModelName)
}

func TestListClientAgentLogsAttachesCapturedContent(t *testing.T) {
	truncateTables(t)

	seedClientAgentDetailLog(t, "Cursor 0.48.7", "Cursor/0.48.7", 1000, "model-a")
	seedClientAgentDetailLog(t, "Cursor 0.48.7", "Cursor/0.48.7", 2000, "model-b")

	now := time.Now().Unix()
	require.NoError(t, LOG_DB.Create(&PromptContentLog{
		CreatedAt: now,
		ExpiresAt: now + 3600,
		RequestID: "req-model-b",
		Direction: PromptContentDirectionRequest,
		Content:   "user asked something",
	}).Error)
	require.NoError(t, LOG_DB.Create(&PromptContentLog{
		CreatedAt: now,
		ExpiresAt: now + 3600,
		RequestID: "req-model-b",
		Direction: PromptContentDirectionResponse,
		Content:   "assistant answered",
	}).Error)
	// Expired entries must not resurface.
	require.NoError(t, LOG_DB.Create(&PromptContentLog{
		CreatedAt: now - 7200,
		ExpiresAt: now - 3600,
		RequestID: "req-model-a",
		Direction: PromptContentDirectionRequest,
		Content:   "stale request",
	}).Error)

	result, err := ListClientAgentLogs(ClientAgentLogQuery{ClientAgent: "Cursor 0.48.7"})
	require.NoError(t, err)
	require.Len(t, result.Items, 2)

	newest := result.Items[0]
	assert.Equal(t, "req-model-b", newest.RequestId)
	assert.Equal(t, "user asked something", newest.RequestContent)
	assert.Equal(t, "assistant answered", newest.ResponseContent)
	assert.True(t, newest.ContentCaptured)

	oldest := result.Items[1]
	assert.Equal(t, "req-model-a", oldest.RequestId)
	assert.Empty(t, oldest.RequestContent)
	assert.False(t, oldest.ContentCaptured)
}

func TestListClientAgentLogsReadsRequestPath(t *testing.T) {
	truncateTables(t)

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:    1,
		CreatedAt: 1000,
		Type:      LogTypeConsume,
		ModelName: "gpt-4o",
		Other:     common.MapToJsonStr(map[string]any{"request_path": "/v1/chat/completions"}),
	}).Error)

	result, err := ListClientAgentLogs(ClientAgentLogQuery{Unknown: true})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "/v1/chat/completions", result.Items[0].RequestPath)
}
