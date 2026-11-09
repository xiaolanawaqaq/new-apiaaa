package model

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTrainingSamples(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	input := `{"messages":[{"role":"user","content":"Hello AI"}]}`
	output := strings.Repeat("你", 80)

	seedPromptContentLog(t, "t1", PromptContentDirectionRequest, input, true, now-10, now+3600)
	seedPromptContentLog(t, "t1", PromptContentDirectionResponse, "older response", true, now-9, now+3600)
	seedPromptContentLog(t, "t1", PromptContentDirectionResponse, output, true, now-9, now+3600)
	seedPromptContentLog(t, "t1", PromptContentDirectionResponse, "older timestamp inserted later", true, now-10, now+3600)
	seedPromptContentLog(t, "t1", PromptContentDirectionResponse, "redacted replacement", false, now-8, now+3600)
	seedPromptContentLog(t, "t1", PromptContentDirectionResponse, "expired replacement", true, now-7, now)
	seedPromptContentLog(t, "t2", PromptContentDirectionRequest, `{"messages":[{"role":"user","content":"Second request"}]}`, true, now-5, now+3600)
	seedPromptContentLog(t, "t3", PromptContentDirectionRequest, "redacted", false, now-3, now+3600)
	seedPromptContentLog(t, "t3", PromptContentDirectionResponse, "raw response without raw request", true, now-2, now+3600)
	seedPromptContentLog(t, "expired", PromptContentDirectionRequest, "expired request", true, now-20, now)
	seedPromptContentLog(t, "expired", PromptContentDirectionResponse, "still live response", true, now-19, now+3600)
	seedPromptContentLog(t, "orphan", PromptContentDirectionResponse, "response only", true, now-1, now+3600)
	seedPromptContentLog(t, "redacted-response", PromptContentDirectionRequest, "request", true, now-4, now+3600)
	seedPromptContentLog(t, "redacted-response", PromptContentDirectionResponse, "summary", false, now-3, now+3600)
	seedPromptContentLog(t, "expired-response", PromptContentDirectionRequest, "request", true, now-3, now+3600)
	seedPromptContentLog(t, "expired-response", PromptContentDirectionResponse, "stale", true, now-2, now)
	seedPromptContentLog(t, "empty-response", PromptContentDirectionRequest, output, true, now-2, now+3600)
	seedPromptContentLog(t, "empty-response", PromptContentDirectionResponse, "", true, now-1, now+3600)

	var version string
	require.NoError(t, LOG_DB.Raw("SELECT sqlite_version()").Scan(&version).Error)
	t.Logf("SQLite version: %s", version)

	samples, total, err := ListTrainingSamples(PromptContentLogFilter{}, 0, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total)
	require.Len(t, samples, 5)
	assert.Equal(t, []string{"empty-response", "expired-response", "redacted-response", "t2", "t1"}, []string{
		samples[0].RequestID, samples[1].RequestID, samples[2].RequestID, samples[3].RequestID, samples[4].RequestID,
	})
	assert.True(t, samples[0].HasResponse, "an empty raw response row is still a captured response")
	assert.Empty(t, samples[0].OutputPreview)
	assert.Zero(t, samples[0].OutputBytes)
	assert.Equal(t, strings.Repeat("你", 66), samples[0].InputPreview)
	assert.Equal(t, len(output), samples[0].InputBytes)
	for _, sample := range samples[1:4] {
		assert.False(t, sample.HasResponse, sample.RequestID)
		assert.Empty(t, sample.OutputPreview, sample.RequestID)
		assert.Zero(t, sample.OutputBytes, sample.RequestID)
	}
	assert.Equal(t, TrainingSample{
		RequestID: "t1", CreatedAt: now - 10, ModelName: "gpt-4o", RequestPath: "/v1/chat/completions",
		InputPreview: input, OutputPreview: strings.Repeat("你", 66), HasResponse: true,
		InputBytes: len(input), OutputBytes: len(output),
	}, samples[4])
	assert.True(t, utf8.ValidString(samples[4].OutputPreview))
}

func TestListTrainingSamplesFiltersAndPagination(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	rows := []PromptContentLog{
		{RequestID: "t1", CreatedAt: now - 10, UserID: 1, Username: "alice", TokenID: 11, ChannelID: 21, ModelName: "gpt-4o"},
		{RequestID: "t2", CreatedAt: now - 5, UserID: 2, Username: "bob", TokenID: 12, ChannelID: 22, ModelName: "claude"},
		{RequestID: "t3", CreatedAt: now - 5, UserID: 1, Username: "alice", TokenID: 12, ChannelID: 21, ModelName: "claude"},
	}
	for i := range rows {
		rows[i].Raw = true
		rows[i].Direction = PromptContentDirectionRequest
		rows[i].ExpiresAt = now + 3600
		rows[i].Content = "request"
	}
	require.NoError(t, LOG_DB.Create(&rows).Error)
	response := rows[0]
	response.ID = 0
	response.CreatedAt = now
	response.Direction = PromptContentDirectionResponse
	response.Content = "response after the request time window"
	require.NoError(t, LOG_DB.Create(&response).Error)

	for _, tc := range []struct {
		name   string
		filter PromptContentLogFilter
		start  int
		limit  int
		ids    []string
		total  int64
	}{
		{name: "all", ids: []string{"t3", "t2", "t1"}, total: 3},
		{name: "user", filter: PromptContentLogFilter{UserID: 1}, ids: []string{"t3", "t1"}, total: 2},
		{name: "token", filter: PromptContentLogFilter{TokenID: 11}, ids: []string{"t1"}, total: 1},
		{name: "channel", filter: PromptContentLogFilter{ChannelID: 22}, ids: []string{"t2"}, total: 1},
		{name: "request", filter: PromptContentLogFilter{RequestID: "t1"}, ids: []string{"t1"}, total: 1},
		{name: "model", filter: PromptContentLogFilter{ModelName: "claude"}, ids: []string{"t3", "t2"}, total: 2},
		{name: "start inclusive", filter: PromptContentLogFilter{StartAt: now - 5}, ids: []string{"t3", "t2"}, total: 2},
		{name: "end inclusive", filter: PromptContentLogFilter{EndAt: now - 10}, ids: []string{"t1"}, total: 1},
		{name: "time window", filter: PromptContentLogFilter{StartAt: now - 10, EndAt: now - 10}, ids: []string{"t1"}, total: 1},
		{name: "combined", filter: PromptContentLogFilter{UserID: 1, TokenID: 12, ChannelID: 21, RequestID: "t3", ModelName: "claude", StartAt: now - 5, EndAt: now - 5}, ids: []string{"t3"}, total: 1},
		{name: "combined mismatch", filter: PromptContentLogFilter{UserID: 1, ChannelID: 22}, ids: []string{}},
		{name: "no match", filter: PromptContentLogFilter{RequestID: "missing"}, ids: []string{}},
		{name: "reversed window", filter: PromptContentLogFilter{StartAt: now, EndAt: now - 10}, ids: []string{}},
		{name: "first page", limit: 1, ids: []string{"t3"}, total: 3},
		{name: "second page", start: 1, limit: 1, ids: []string{"t2"}, total: 3},
		{name: "last page", start: 2, limit: 1, ids: []string{"t1"}, total: 3},
		{name: "past last page", start: 3, limit: 1, ids: []string{}, total: 3},
		{name: "filtered page", filter: PromptContentLogFilter{UserID: 1}, start: 1, limit: 1, ids: []string{"t1"}, total: 2},
		{name: "negative pagination", start: -1, limit: -1, ids: []string{"t3", "t2", "t1"}, total: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, total, err := ListTrainingSamples(tc.filter, tc.start, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.total, total)
			require.NotNil(t, samples, "empty results must serialize as []")
			ids := make([]string, 0, len(samples))
			for _, sample := range samples {
				ids = append(ids, sample.RequestID)
				if sample.RequestID == "t1" {
					assert.Equal(t, "alice", sample.Username)
					assert.True(t, sample.HasResponse)
					assert.Equal(t, string(response.Content), sample.OutputPreview)
				}
			}
			assert.Equal(t, tc.ids, ids)
		})
	}
}

func TestListTrainingSamplesDatabaseErrors(t *testing.T) {
	previous := LOG_DB
	t.Cleanup(func() { LOG_DB = previous })

	LOG_DB = nil
	samples, total, err := ListTrainingSamples(PromptContentLogFilter{}, 0, 20)
	require.EqualError(t, err, "log database is unavailable")
	assert.Nil(t, samples)
	assert.Zero(t, total)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	LOG_DB = previous.WithContext(ctx)
	samples, total, err = ListTrainingSamples(PromptContentLogFilter{}, 0, 20)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, samples)
	assert.Zero(t, total)
}
