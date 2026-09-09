package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizePromptTextAndBlacklist(t *testing.T) {
	previous := common.OptionMap
	common.OptionMap = map[string]string{PromptBlacklistOptionKey: `["Straße","ＡＰＩ"]`}
	t.Cleanup(func() { common.OptionMap = previous })

	assert.Equal(t, "strasse", NormalizePromptText("STRASSE"))
	assert.Equal(t, "api", NormalizePromptText("ＡＰＩ"))
	matched, words := PromptBlacklistContains("request contains STRASSE and api")
	assert.True(t, matched)
	assert.ElementsMatch(t, []string{"strasse", "api"}, words)
}

func TestExtractPromptTextRecursesAndUsesTokenMeta(t *testing.T) {
	payload := map[string]any{
		"contents": []any{
			map[string]any{"parts": []any{map[string]any{"text": "gemini text"}}},
		},
		"messages": []any{
			map[string]any{"content": []any{map[string]any{"type": "text", "text": "claude text"}}},
		},
	}
	text := ExtractPromptText(payload)
	assert.Contains(t, text, "gemini text")
	assert.Contains(t, text, "claude text")

	meta := &types.TokenCountMeta{CombineText: "already combined provider text"}
	assert.Equal(t, meta.CombineText, ExtractPromptText(meta))

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, ExtractPromptText(body), "gemini text")
}

func TestRedactAndLimitSecureContent(t *testing.T) {
	content := `Authorization: Bearer super-secret-token password="hunter2" mail=user@example.com ` + strings.Repeat("中", 20000)
	redacted := RedactSecureContent(content)
	assert.Equal(t, model.MaxPromptContentLogBytes, MaxSecureContentLogBytes)
	assert.LessOrEqual(t, len([]byte(redacted)), MaxSecureContentLogBytes)
	assert.NotContains(t, redacted, "super-secret-token")
	assert.NotContains(t, redacted, `password="hunter2"`)
	assert.True(t, strings.Contains(redacted, "[REDACTED]") || len([]byte(redacted)) == MaxSecureContentLogBytes)
}

func TestSecureContentLogRetentionDefaultsAndDisabled(t *testing.T) {
	previous := common.OptionMap
	common.OptionMap = map[string]string{}
	assert.Equal(t, DefaultSecureContentLogRetention, SecureContentLogRetention())
	assert.False(t, SecureContentLogEnabled())
	common.OptionMap = map[string]string{SecureContentLogRetentionOptionKey: "2", SecureContentLogEnabledOptionKey: "TRUE"}
	assert.Equal(t, 2*time.Hour, SecureContentLogRetention())
	assert.True(t, SecureContentLogEnabled())
	common.OptionMap = previous
}

func TestRecordSecureContentLogAndCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousOptions := common.OptionMap
	model.DB, model.LOG_DB = db, db
	common.OptionMap = map[string]string{SecureContentLogEnabledOptionKey: "true", SecureContentLogRetentionOptionKey: "24"}
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.OptionMap = previousOptions
	})
	require.NoError(t, model.MigratePromptContentLogs())
	now := time.Now().Truncate(time.Second)
	require.NoError(t, RecordSecureContentLog(SecureContentLogParams{
		UserID: 1, Username: "admin", RequestID: "req-1", ModelName: "gpt-test",
		Direction: model.PromptContentDirectionRequest, Content: `{"password":"secret"}`, Now: now,
	}))
	oldCreatedAt := now.Add(-48 * time.Hour).Unix()
	oldExpiresAt := now.Add(-time.Hour).Unix()
	require.NoError(t, model.LOG_DB.Create(&model.PromptContentLog{
		CreatedAt: oldCreatedAt, ExpiresAt: oldExpiresAt, Direction: model.PromptContentDirectionResponse,
	}).Error)
	entries, total, err := model.GetPromptContentLogs(model.PromptContentLogFilter{RequestID: "req-1"}, 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Content, "[REDACTED]")
	removed, err := model.DeleteExpiredPromptContentLogs(context.Background(), now.Unix(), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)
}

func TestExtractResponseTextFromNonStreamJSON(t *testing.T) {
	raw := `{"id":"resp-1","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello world","reasoning_content":"thinking hard"}}],"usage":{"total_tokens":42}}`
	text := ExtractResponseText(raw)
	assert.Contains(t, text, "hello world")
	assert.Contains(t, text, "thinking hard")
	// id/model/usage 等非正文字段不得混入。
	assert.NotContains(t, text, "resp-1")
	assert.NotContains(t, text, "gpt-test")
	assert.NotContains(t, text, "42")
}

func TestExtractResponseTextFromSSEStream(t *testing.T) {
	raw := "event: message\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	text := ExtractResponseText(raw)
	assert.Equal(t, "he\nllo", text)
}

func TestExtractResponseTextSkipsEncodedMedia(t *testing.T) {
	raw := `{"choices":[{"message":{"content":"data:image/png;base64,AAAA"}}]}`
	// 仅含 base64 媒体内容时没有可提取的正文,回退返回原文(入库前仍受脱敏与截断约束)。
	assert.Equal(t, raw, ExtractResponseText(raw))
}

func TestExtractResponseTextFallsBackToRaw(t *testing.T) {
	assert.Equal(t, "plain text response", ExtractResponseText("plain text response"))
	assert.Empty(t, ExtractResponseText("   \n  "))
}
