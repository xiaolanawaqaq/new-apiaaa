package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListTrainingSamplesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousLogDB := model.LOG_DB
	model.LOG_DB = db
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.PromptContentLog{}))
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PromptContentLog{
		CreatedAt: now - 10, ExpiresAt: now + 3600,
		UserID: 1, TokenID: 2, ChannelID: 3, ModelName: "gpt-4o", RequestID: "t1",
		Direction: model.PromptContentDirectionRequest, Raw: true, Content: "request body",
	}).Error)

	for _, tc := range []struct {
		name  string
		role  int
		query string
		total int
	}{
		{name: "anonymous"},
		{name: "ordinary user", role: common.RoleCommonUser},
		{name: "admin", role: common.RoleAdminUser, total: 1},
		{name: "root", role: common.RoleRootUser, total: 1},
		{name: "user filter", role: common.RoleAdminUser, query: "user_id=9"},
		{name: "token filter", role: common.RoleAdminUser, query: "token_id=9"},
		{name: "channel filter", role: common.RoleAdminUser, query: "channel_id=9"},
		{name: "request filter", role: common.RoleAdminUser, query: "request_id=missing"},
		{name: "model filter", role: common.RoleAdminUser, query: "model_name=missing"},
		{name: "start filter", role: common.RoleAdminUser, query: "start_timestamp=" + strconv.FormatInt(now, 10)},
		{name: "end filter", role: common.RoleAdminUser, query: "end_timestamp=" + strconv.FormatInt(now-20, 10)},
		{name: "matching filters", role: common.RoleAdminUser, query: "user_id=1&token_id=2&channel_id=3&request_id=t1&model_name=gpt-4o", total: 1},
		{name: "negative pagination", role: common.RoleAdminUser, query: "p=-1&page_size=-1", total: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/prompt-security/training-samples?"+tc.query, nil)
			c.Set("role", tc.role)
			ListTrainingSamples(c)

			var result struct {
				Success bool `json:"success"`
				Data    struct {
					Page     int                    `json:"page"`
					PageSize int                    `json:"page_size"`
					Total    int                    `json:"total"`
					Items    []model.TrainingSample `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
			if tc.role < common.RoleAdminUser {
				assert.Equal(t, http.StatusForbidden, recorder.Code)
				assert.False(t, result.Success)
				assert.Nil(t, result.Data.Items)
				return
			}
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.True(t, result.Success)
			assert.Equal(t, 1, result.Data.Page)
			assert.Positive(t, result.Data.PageSize)
			assert.Equal(t, tc.total, result.Data.Total)
			require.NotNil(t, result.Data.Items)
			require.Len(t, result.Data.Items, tc.total)
			if tc.total > 0 {
				assert.Equal(t, "t1", result.Data.Items[0].RequestID)
				assert.False(t, result.Data.Items[0].HasResponse)
			}
		})
	}
}

func TestZZBuildOpenAITrainingSample(t *testing.T) {
	pair := model.PromptContentPair{
		RequestID:   "test-1",
		ModelName:   "gpt-4o",
		RequestPath: "/v1/chat/completions",
		Request:     `{"model":"gpt-4o","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Hello!"}]}`,
		Response:    "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\ndata: [DONE]\n\n",
	}

	sample, err := buildOpenAITrainingSample(pair)
	require.NoError(t, err)

	msgs, ok := sample["messages"].([]map[string]any)
	require.True(t, ok, "messages should be []map[string]any")
	require.Len(t, msgs, 3)
	assert.Equal(t, "system", msgs[0]["role"])
	assert.Equal(t, "You are helpful.", msgs[0]["content"])
	assert.Equal(t, "user", msgs[1]["role"])
	assert.Equal(t, "Hello!", msgs[1]["content"])
	assert.Equal(t, "assistant", msgs[2]["role"])
	assert.Equal(t, "Hi there", msgs[2]["content"])
}

func TestZZBuildOpenAITrainingSampleClaudeFormat(t *testing.T) {
	pair := model.PromptContentPair{
		RequestID:   "test-2",
		ModelName:   "claude-3",
		RequestPath: "/v1/messages",
		Request:     `{"model":"claude-3","system":"Be concise.","messages":[{"role":"user","content":[{"type":"text","text":"What is 2+2?"}]}]}`,
		Response:    `{"content":[{"type":"text","text":"4"}]}`,
	}

	sample, err := buildOpenAITrainingSample(pair)
	require.NoError(t, err)

	msgs, ok := sample["messages"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, msgs, 3)
	assert.Equal(t, "system", msgs[0]["role"])
	assert.Equal(t, "Be concise.", msgs[0]["content"])
	assert.Equal(t, "user", msgs[1]["role"])
	assert.Equal(t, "What is 2+2?", msgs[1]["content"])
	assert.Equal(t, "assistant", msgs[2]["role"])
	assert.Equal(t, "4", msgs[2]["content"])
}

func TestZZExportOpenAIFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousOptions := common.OptionMap
	model.DB, model.LOG_DB = db, db
	common.OptionMap = map[string]string{"SecureContentLogEnabled": "true", "SecureContentLogRawMode": "true"}
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.OptionMap = previousOptions
	})
	require.NoError(t, db.AutoMigrate(&model.PromptContentLog{}, &model.Log{}))
	now := time.Now().Unix()

	require.NoError(t, model.LOG_DB.Create(&model.PromptContentLog{
		CreatedAt: now - 10, ExpiresAt: now + 3600,
		RequestID: "r1", Direction: model.PromptContentDirectionRequest, Raw: true,
		Content:   model.LongText(`{"model":"gpt-4o","messages":[{"role":"user","content":"What is AI?"}]}`),
		ModelName: "gpt-4o", RequestPath: "/v1/chat/completions",
	}).Error)
	require.NoError(t, model.LOG_DB.Create(&model.PromptContentLog{
		CreatedAt: now - 9, ExpiresAt: now + 3600,
		RequestID: "r1", Direction: model.PromptContentDirectionResponse, Raw: true,
		Content:   model.LongText(`{"choices":[{"message":{"content":"AI is artificial intelligence."}}]}`),
		ModelName: "gpt-4o", RequestPath: "/v1/chat/completions",
	}).Error)
	// Non-raw rows must be excluded from training export.
	require.NoError(t, model.LOG_DB.Create(&model.PromptContentLog{
		CreatedAt: now - 8, ExpiresAt: now + 3600,
		RequestID: "r2", Direction: model.PromptContentDirectionRequest, Raw: false,
		Content:   model.LongText("redacted summary"),
		ModelName: "gpt-4o",
	}).Error)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/prompt-security/content/export", func(c *gin.Context) {
		c.Set("role", common.RoleRootUser)
		ExportPromptContentPairs(c)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/prompt-security/content/export?format=openai&require_response=true", nil)
	router.ServeHTTP(recorder, request)

	assert.Equal(t, 200, recorder.Code)
	var samples []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(recorder.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if jsonErr := json.Unmarshal([]byte(line), &obj); jsonErr != nil {
			continue // trailing API result line
		}
		if _, has := obj["messages"]; has {
			samples = append(samples, obj)
		}
	}
	require.Len(t, samples, 1, "should have exactly one training sample")
	msgs := samples[0]["messages"].([]any)
	require.Len(t, msgs, 2)
	first := msgs[0].(map[string]any)
	assert.Equal(t, "user", first["role"])
	assert.Equal(t, "What is AI?", first["content"])
	second := msgs[1].(map[string]any)
	assert.Equal(t, "assistant", second["role"])
	assert.Equal(t, "AI is artificial intelligence.", second["content"])
}
