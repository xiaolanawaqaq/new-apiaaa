package controller

import (
	"net/http"
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

// Unwrap lets http.ResponseController reach the underlying writer so the
// streaming write deadline still applies through this wrapper.
func (w *secureContentWriter) Unwrap() http.ResponseWriter {
	if unwrapper, ok := w.ResponseWriter.(interface {
		Unwrap() http.ResponseWriter
	}); ok {
		return unwrapper.Unwrap()
	}
	return w.ResponseWriter
}

// secureContentCapture 保存一次 relay 请求的捕获状态：包装后的响应写入器、
// 请求正文文本，以及落库所需的调用方信息。
type secureContentCapture struct {
	writer      *secureContentWriter
	requestText string
	// rawBody 是原始请求体（JSON），仅在原始模式（训练数据）下采集。
	rawBody []byte
	rawMode bool
	params  service.SecureContentLogParams
}

// captureBufferSize 返回本次请求需要缓存的最大响应字节数。原始模式要保留
// 完整响应体，因此使用更高的上限。
func captureBufferSize(rawMode bool) int {
	if rawMode {
		return model.MaxRawContentLogBytes
	}
	return model.PromptContentLogMaxBytes()
}

// beginSecureContentCapture 在 relay 入口包装 c.Writer。仅在安全内容日志开启
// 且非 WebSocket 实时协议时生效；返回 nil 表示本次请求不做捕获。
func beginSecureContentCapture(c *gin.Context, relayFormat types.RelayFormat) *secureContentCapture {
	if relayFormat == types.RelayFormatOpenAIRealtime || !service.SecureContentLogEnabled() {
		return nil
	}
	rawMode := service.SecureContentLogRawMode()
	writer := &secureContentWriter{
		ResponseWriter: c.Writer,
		maxSize:        captureBufferSize(rawMode),
	}
	c.Writer = writer
	capture := &secureContentCapture{
		writer:  writer,
		rawMode: rawMode,
		params: service.SecureContentLogParams{
			UserID:    common.GetContextKeyInt(c, constant.ContextKeyUserId),
			Username:  common.GetContextKeyString(c, constant.ContextKeyUserName),
			RequestID: c.GetString(common.RequestIdKey),
		},
	}
	if rawMode {
		capture.rawBody = readRawRequestBody(c)
	}
	return capture
}

// readRawRequestBody 读取原始请求体。body storage 由中间件提前缓存，读取失败
// 不影响转发，只是本条日志缺少原始请求体。
func readRawRequestBody(c *gin.Context) []byte {
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage == nil {
		return nil
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return nil
	}
	if len(body) > model.MaxRawContentLogBytes {
		body = body[:model.MaxRawContentLogBytes]
	}
	return body
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
	if c != nil && c.Request != nil && c.Request.URL != nil {
		capture.params.RequestPath = c.Request.URL.Path
	}

	capture.params.Direction = model.PromptContentDirectionRequest
	if capture.rawMode {
		capture.params.Content = string(capture.rawBody)
	} else {
		capture.params.Content = capture.requestText
	}
	if strings.TrimSpace(capture.params.Content) == "" {
		return
	}
	if err := service.RecordSecureContentLog(capture.params); err != nil {
		logger.LogWarn(c, "secure content log request record failed: "+err.Error())
	}

	capture.params.Direction = model.PromptContentDirectionResponse
	if capture.rawMode {
		// 原始模式保留完整响应体（流式即原始 SSE 文本），便于后续解析成训练样本。
		capture.params.Content = string(capture.writer.body)
	} else {
		capture.params.Content = service.ExtractResponseText(string(capture.writer.body))
	}
	if strings.TrimSpace(capture.params.Content) == "" {
		return
	}
	if err := service.RecordSecureContentLog(capture.params); err != nil {
		logger.LogWarn(c, "secure content log response record failed: "+err.Error())
	}
}
