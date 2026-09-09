package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// secureContentWriter 在不改变原始响应的前提下，把响应体复制一份到有限大小
// 的缓冲区，用于安全内容日志。缓冲区到达上限后不再缓存，原响应不受影响。
type secureContentWriter struct {
	gin.ResponseWriter
	body    []byte
	maxSize int
}

func (w *secureContentWriter) Write(b []byte) (int, error) {
	if remain := w.maxSize - len(w.body); remain > 0 {
		if remain >= len(b) {
			w.body = append(w.body, b...)
		} else {
			w.body = append(w.body, b[:remain]...)
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *secureContentWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// secureContentCapture 保存一次 relay 请求的捕获状态：包装后的响应写入器、
// 请求正文文本，以及落库所需的调用方信息。
type secureContentCapture struct {
	writer      *secureContentWriter
	requestText string
	params      service.SecureContentLogParams
}

// beginSecureContentCapture 在 relay 入口包装 c.Writer。仅在安全内容日志开启
// 且非 WebSocket 实时协议时生效；返回 nil 表示本次请求不做捕获。
func beginSecureContentCapture(c *gin.Context, relayFormat types.RelayFormat) *secureContentCapture {
	if relayFormat == types.RelayFormatOpenAIRealtime || !service.SecureContentLogEnabled() {
		return nil
	}
	writer := &secureContentWriter{
		ResponseWriter: c.Writer,
		maxSize:        model.PromptContentLogMaxBytes(),
	}
	c.Writer = writer
	return &secureContentCapture{
		writer: writer,
		params: service.SecureContentLogParams{
			UserID:    common.GetContextKeyInt(c, constant.ContextKeyUserId),
			Username:  common.GetContextKeyString(c, constant.ContextKeyUserName),
			RequestID: c.GetString(common.RequestIdKey),
		},
	}
}

// finishSecureContentCapture 在响应完全写出后记录请求与响应内容。relayInfo
// 可能为 nil（请求在早期失败），此时仍记录已捕获的请求文本与错误响应。
func finishSecureContentCapture(c *gin.Context, capture *secureContentCapture, relayInfo *relaycommon.RelayInfo) {
	if capture == nil {
		return
	}
	if relayInfo != nil {
		capture.params.TokenID = relayInfo.TokenId
		capture.params.ChannelID = relayInfo.ChannelId
		capture.params.ModelName = relayInfo.OriginModelName
	}
	capture.params.Direction = model.PromptContentDirectionRequest
	capture.params.Content = capture.requestText
	if err := service.RecordSecureContentLog(capture.params); err != nil {
		logger.LogWarn(c, "secure content log request record failed: "+err.Error())
	}
	capture.params.Direction = model.PromptContentDirectionResponse
	capture.params.Content = service.ExtractResponseText(string(capture.writer.body))
	if strings.TrimSpace(capture.params.Content) == "" {
		return
	}
	if err := service.RecordSecureContentLog(capture.params); err != nil {
		logger.LogWarn(c, "secure content log response record failed: "+err.Error())
	}
}
