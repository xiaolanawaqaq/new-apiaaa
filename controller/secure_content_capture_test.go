package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSecureCaptureTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousOptions := common.OptionMap
	model.DB, model.LOG_DB = db, db
	common.OptionMap = map[string]string{
		service.SecureContentLogEnabledOptionKey:   "true",
		service.SecureContentLogRetentionOptionKey: "24",
	}
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.OptionMap = previousOptions
	})
	require.NoError(t, model.MigratePromptContentLogs())
}

func TestSecureContentWriterTruncatesAndPassesThrough(t *testing.T) {
	setupSecureCaptureTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	base := c.Writer

	writer := &secureContentWriter{ResponseWriter: base, maxSize: 10}
	n, err := writer.Write([]byte("0123456789ABCDEF"))
	require.NoError(t, err)
	assert.Equal(t, 16, n)
	assert.Equal(t, "0123456789ABCDEF", recorder.Body.String())
	assert.Equal(t, "0123456789", string(writer.body))

	n, err = writer.WriteString("XY")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "0123456789ABCDEFXY", recorder.Body.String())
	// 缓冲区已满,不再缓存新写入。
	assert.Equal(t, "0123456789", string(writer.body))
}

func TestBeginSecureContentCaptureRespectsFormatAndOption(t *testing.T) {
	setupSecureCaptureTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	originalWriter := c.Writer
	capture := beginSecureContentCapture(c, types.RelayFormatOpenAI)
	require.NotNil(t, capture)
	assert.NotSame(t, originalWriter, c.Writer)

	// WebSocket 实时协议不做捕获。
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.Nil(t, beginSecureContentCapture(c2, types.RelayFormatOpenAIRealtime))

	// 功能关闭时返回 nil。
	common.OptionMap = map[string]string{}
	defer func() { common.OptionMap = map[string]string{service.SecureContentLogEnabledOptionKey: "true"} }()
	c3, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.Nil(t, beginSecureContentCapture(c3, types.RelayFormatOpenAI))
}

func TestFinishSecureContentCaptureRecordsBothDirections(t *testing.T) {
	setupSecureCaptureTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	capture := beginSecureContentCapture(c, types.RelayFormatOpenAI)
	require.NotNil(t, capture)

	capture.requestText = `{"messages":[{"role":"user","content":"hello api"}]}`
	_, err := c.Writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi there\"}}]}\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)

	finishSecureContentCapture(c, capture, &relaycommon.RelayInfo{
		TokenId: 5, OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7},
	})

	entries, total, err := model.GetPromptContentLogs(model.PromptContentLogFilter{}, 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	requests := 0
	responses := 0
	for _, entry := range entries {
		assert.Equal(t, 5, entry.TokenID)
		assert.Equal(t, 7, entry.ChannelID)
		assert.Equal(t, "gpt-test", entry.ModelName)
		if entry.Direction == model.PromptContentDirectionRequest {
			requests++
			assert.Contains(t, entry.Content, "hello api")
		} else {
			responses++
			assert.Equal(t, "hi there", entry.Content)
		}
	}
	assert.Equal(t, 1, requests)
	assert.Equal(t, 1, responses)
}

func TestFinishSecureContentCaptureSkipsEmptyResponse(t *testing.T) {
	setupSecureCaptureTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	capture := beginSecureContentCapture(c, types.RelayFormatClaude)
	require.NotNil(t, capture)
	capture.requestText = "user prompt"

	finishSecureContentCapture(c, capture, nil)

	entries, total, err := model.GetPromptContentLogs(model.PromptContentLogFilter{}, 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	assert.Equal(t, model.PromptContentDirectionRequest, entries[0].Direction)
	assert.Contains(t, entries[0].Content, "user prompt")

	// capture 为 nil 时是安全的 no-op。
	finishSecureContentCapture(c, nil, nil)
}
