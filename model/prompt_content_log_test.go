package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedPromptContentLog(t *testing.T, reqID, direction, content string, raw bool, created, expires int64) {
	t.Helper()
	require.NoError(t, LOG_DB.Create(&PromptContentLog{
		CreatedAt:   created,
		ExpiresAt:   expires,
		RequestID:   reqID,
		Direction:   direction,
		Raw:         raw,
		Content:     LongText(content),
		ModelName:   "gpt-4o",
		RequestPath: "/v1/chat/completions",
	}).Error)
}

func TestStreamRawPromptContentPairsPairsRequestAndResponse(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()

	seedPromptContentLog(t, "r1", PromptContentDirectionRequest, `{"messages":[{"role":"user","content":"hi"}]}`, true, now-10, now+3600)
	seedPromptContentLog(t, "r1", PromptContentDirectionResponse, `{"choices":[{"message":{"content":"hello"}}]}`, true, now-9, now+3600)
	// A request without a response is still exported, so aborted streams stay visible.
	seedPromptContentLog(t, "r2", PromptContentDirectionRequest, `{"messages":[{"role":"user","content":"only request"}]}`, true, now-8, now+3600)
	// Redacted rows belong to troubleshooting, not training data.
	seedPromptContentLog(t, "r3", PromptContentDirectionRequest, "redacted summary", false, now-7, now+3600)
	seedPromptContentLog(t, "r3", PromptContentDirectionResponse, "redacted summary", false, now-6, now+3600)
	// Expired rows must not resurface.
	seedPromptContentLog(t, "old", PromptContentDirectionRequest, "stale", true, now-7200, now-3600)

	var pairs []PromptContentPair
	err := StreamRawPromptContentPairs(context.Background(), PromptContentLogFilter{RawOnly: true}, func(p PromptContentPair) error {
		pairs = append(pairs, p)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, pairs, 2)

	assert.Equal(t, "r1", pairs[0].RequestID)
	assert.Contains(t, pairs[0].Request, `"content":"hi"`)
	assert.Contains(t, pairs[0].Response, `"content":"hello"`)
	assert.Equal(t, "/v1/chat/completions", pairs[0].RequestPath)

	assert.Equal(t, "r2", pairs[1].RequestID)
	assert.Contains(t, pairs[1].Request, "only request")
	assert.Empty(t, pairs[1].Response)
}

func TestCreatePromptContentLogKeepsRawContentIntact(t *testing.T) {
	truncateTables(t)
	previous := common.OptionMap
	common.OptionMap = map[string]string{"SecureContentLogMaxBytes": "1024"}
	t.Cleanup(func() { common.OptionMap = previous })

	long := ""
	for range 5000 {
		long += "0123456789"
	}
	entry := &PromptContentLog{
		CreatedAt: 100,
		ExpiresAt: 200,
		RequestID: "raw-1",
		Direction: PromptContentDirectionRequest,
		Raw:       true,
		Content:   LongText(long),
	}
	require.NoError(t, CreatePromptContentLog(entry))
	// Raw rows use MaxRawContentLogBytes, not the smaller redacted-summary cap.
	assert.Greater(t, len(entry.Content), 1024)
	assert.LessOrEqual(t, len(entry.Content), MaxRawContentLogBytes)
}

func TestCreatePromptContentLogTruncatesSummaries(t *testing.T) {
	truncateTables(t)
	previous := common.OptionMap
	common.OptionMap = map[string]string{"SecureContentLogMaxBytes": "100"}
	t.Cleanup(func() { common.OptionMap = previous })

	long := ""
	for range 50 {
		long += "abcdefghij"
	}
	entry := &PromptContentLog{
		CreatedAt: 100,
		ExpiresAt: 200,
		RequestID: "sum-1",
		Direction: PromptContentDirectionRequest,
		Content:   LongText(long),
	}
	require.NoError(t, CreatePromptContentLog(entry))
	assert.Len(t, string(entry.Content), 100)
}
