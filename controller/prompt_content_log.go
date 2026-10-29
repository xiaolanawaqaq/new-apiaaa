package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func requirePromptSecurityAdmin(c *gin.Context) bool {
	if c.GetInt("role") < common.RoleAdminUser {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "管理员权限不足",
		})
		return false
	}
	return true
}

// GetPromptSecurityOptions exposes the security options using the same Option
// shape as the existing administrator settings API.
func GetPromptSecurityOptions(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	common.OptionMapRWMutex.RLock()
	blacklist := common.OptionMap[service.PromptBlacklistOptionKey]
	enabled := common.OptionMap[service.SecureContentLogEnabledOptionKey]
	retention := common.OptionMap[service.SecureContentLogRetentionOptionKey]
	maxBytes := common.OptionMap[service.SecureContentLogMaxBytesOptionKey]
	rawMode := common.OptionMap[service.SecureContentLogRawModeOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if enabled == "" {
		enabled = "false"
	}
	if retention == "" {
		retention = "24"
	}
	if maxBytes == "" {
		maxBytes = strconv.Itoa(model.MaxPromptContentLogBytes)
	}
	if rawMode == "" {
		rawMode = "false"
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": []*model.Option{
			{Key: service.PromptBlacklistOptionKey, Value: blacklist},
			{Key: service.SecureContentLogEnabledOptionKey, Value: enabled},
			{Key: service.SecureContentLogRetentionOptionKey, Value: retention},
			{Key: service.SecureContentLogMaxBytesOptionKey, Value: maxBytes},
			{Key: service.SecureContentLogRawModeOptionKey, Value: rawMode},
		},
	})
}

type PromptSecurityOptionUpdateRequest struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func promptSecurityOptionString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// UpdatePromptSecurityOption persists only the security options owned by this
// module. Generic Option writes remain available through the existing root API.
func UpdatePromptSecurityOption(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	var request PromptSecurityOptionUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的参数"})
		return
	}
	value := promptSecurityOptionString(request.Value)
	switch request.Key {
	case service.PromptBlacklistOptionKey:
		if _, err := service.ParsePromptBlacklist(value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
			return
		}
		if err := service.SetPromptBlacklist(value); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogEnabledOptionKey:
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogEnabled 必须是布尔值"})
			return
		}
		if err := model.UpdateOption(request.Key, strings.ToLower(value)); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogRetentionOptionKey:
		hours, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || hours <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogRetentionHours 必须是正整数"})
			return
		}
		if err := model.UpdateOption(request.Key, strconv.Itoa(hours)); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogMaxBytesOptionKey:
		maxBytes, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || maxBytes <= 0 || maxBytes > model.MaxSecureContentLogBytesLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogMaxBytes 必须是 1 到 2097152 之间的整数"})
			return
		}
		if err := model.UpdateOption(request.Key, strconv.Itoa(maxBytes)); err != nil {
			common.ApiError(c, err)
			return
		}
	case service.SecureContentLogRawModeOptionKey:
		if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "SecureContentLogRawMode 必须是布尔值"})
			return
		}
		if err := model.UpdateOption(request.Key, strings.ToLower(value)); err != nil {
			common.ApiError(c, err)
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "不支持的安全配置项"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func GetPromptContentLogs(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	page := common.GetPageQuery(c)
	userID, _ := strconv.Atoi(c.Query("user_id"))
	tokenID, _ := strconv.Atoi(c.Query("token_id"))
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	startAt, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endAt, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	entries, total, err := model.GetPromptContentLogs(model.PromptContentLogFilter{
		UserID:    userID,
		TokenID:   tokenID,
		ChannelID: channelID,
		RequestID: c.Query("request_id"),
		ModelName: c.Query("model_name"),
		Direction: c.Query("direction"),
		StartAt:   startAt,
		EndAt:     endAt,
	}, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// 查询时顺带清理过期记录（小批量、尽力而为），配合每小时的定时清理任务。
	if _, err := model.DeleteExpiredPromptContentLogs(context.Background(), 0, 100); err != nil {
		logger.LogWarn(c, "prompt content log opportunistic cleanup failed: "+err.Error())
	}
	page.SetTotal(int(total))
	page.SetItems(entries)
	common.ApiSuccess(c, page)
}

func CleanupPromptContentLogs(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	removed, err := model.DeleteExpiredPromptContentLogs(context.Background(), 0, 5000)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"deleted": removed}})
}

// ExportPromptContentPairs streams raw-mode request/response pairs as NDJSON.
// Each line is one complete pair, which is the shape training-data pipelines
// expect; the trailing line is the usual API result object.
func ExportPromptContentPairs(c *gin.Context) {
	if !requirePromptSecurityAdmin(c) {
		return
	}
	startAt, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endAt, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	userID, _ := strconv.Atoi(c.Query("user_id"))
	tokenID, _ := strconv.Atoi(c.Query("token_id"))
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	modelName := c.Query("model_name")
	format := c.Query("format")
	requireResponse := c.Query("require_response") == "true"

	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	count := 0
	writePair := func(pair model.PromptContentPair) error {
		var line []byte
		var err error
		if requireResponse && pair.Response == "" {
			return nil
		}
		if format == "openai" {
			sample, buildErr := buildOpenAITrainingSample(pair)
			if buildErr != nil {
				return nil // skip malformed samples
			}
			line, err = common.Marshal(sample)
		} else {
			line, err = common.Marshal(pair)
		}
		if err != nil {
			return err
		}
		if _, err := c.Writer.Write(append(line, '\n')); err != nil {
			return err
		}
		count++
		return nil
	}
	ctx := c.Request.Context()
	err := model.StreamRawPromptContentPairs(ctx, model.PromptContentLogFilter{
		UserID:    userID,
		TokenID:   tokenID,
		ChannelID: channelID,
		ModelName: modelName,
		StartAt:   startAt,
		EndAt:     endAt,
		RawOnly:   true,
	}, writePair)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		common.SysError(common.LogText("failed to export prompt content: %s", err.Error()))
		common.ApiErrorT(c, "Failed to export content")
		return
	}
	common.ApiSuccess(c, gin.H{"count": count})
}

// buildOpenAITrainingSample converts a raw pair into OpenAI fine-tune format:
// the original messages plus the assistant's response appended as the final turn.
func buildOpenAITrainingSample(pair model.PromptContentPair) (map[string]any, error) {
	messages := normalizeTrainingMessages(pair.Request)
	if len(messages) == 0 {
		return nil, errors.New("request body is not a recognized chat format")
	}
	responseText := pair.Response
	if strings.Contains(responseText, "data:") {
		// 流式响应是 SSE 帧：按顺序拼接 delta.content（无分隔符），还原完整回复。
		responseText = assembleStreamingResponse(responseText)
	} else {
		responseText = extractJSONResponseText(responseText)
	}
	if strings.TrimSpace(responseText) == "" {
		return nil, errors.New("empty response")
	}
	messages = append(messages, map[string]any{"role": "assistant", "content": responseText})
	return map[string]any{"messages": messages}, nil
}

// extractJSONResponseText pulls the assistant text out of a non-streaming
// response body. Handles OpenAI, Claude, and Gemini response shapes.
func extractJSONResponseText(raw string) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw
	}

	// OpenAI: choices[].message.content
	if choicesRaw, ok := root["choices"]; ok {
		var choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(choicesRaw, &choices) == nil && len(choices) > 0 {
			return choices[0].Message.Content
		}
	}

	// Claude: content[].text
	if contentRaw, ok := root["content"]; ok {
		var blocks []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(contentRaw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "")
			}
		}
	}

	// Gemini: candidates[].content.parts[].text
	if candsRaw, ok := root["candidates"]; ok {
		var cands []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}
		if json.Unmarshal(candsRaw, &cands) == nil {
			for _, cand := range cands {
				for _, p := range cand.Content.Parts {
					if p.Text != "" {
						return p.Text
					}
				}
			}
		}
	}

	return raw
}

// assembleStreamingResponse parses SSE frames and concatenates the content
// tokens in order (no separator), reconstructing the full assistant reply.
func assembleStreamingResponse(sseBody string) string {
	var sb strings.Builder
	for _, line := range strings.Split(sseBody, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			continue
		}
		// OpenAI: choices[].delta.content
		if choicesRaw, ok := decoded["choices"]; ok {
			var choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			}
			if json.Unmarshal(choicesRaw, &choices) == nil {
				for _, c := range choices {
					sb.WriteString(c.Delta.Content)
				}
			}
		}
		// Claude: delta.text
		if deltaRaw, ok := decoded["delta"]; ok {
			var delta struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(deltaRaw, &delta) == nil {
				sb.WriteString(delta.Text)
			}
		}
		// Gemini: candidates[].content.parts[].text
		if candsRaw, ok := decoded["candidates"]; ok {
			var cands []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				}
			}
			if json.Unmarshal(candsRaw, &cands) == nil {
				for _, cand := range cands {
					for _, p := range cand.Content.Parts {
						sb.WriteString(p.Text)
					}
				}
			}
		}
	}
	return sb.String()
}

// normalizeTrainingMessages parses a raw request body (OpenAI, Claude, or
// Gemini format) into a uniform [{role, content}] messages array.
func normalizeTrainingMessages(body string) []map[string]any {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return nil
	}

	extractContent := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
		// Content blocks: [{"type":"text","text":"..."}, ...]
		var blocks []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil && len(blocks) > 0 {
			var parts []string
			for _, b := range blocks {
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
		return string(raw)
	}

	var result []map[string]any

	// OpenAI / Claude format: messages[] with string or block content.
	// Both formats use a "messages" array; Claude additionally has top-level system.
	if msgsRaw, ok := root["messages"]; ok {
		var messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(msgsRaw, &messages); err == nil && len(messages) > 0 {
			if system, hasSys := root["system"]; hasSys {
				if sys := extractContent(system); sys != "" {
					result = append(result, map[string]any{"role": "system", "content": sys})
				}
			}
			for _, m := range messages {
				result = append(result, map[string]any{"role": m.Role, "content": extractContent(m.Content)})
			}
			return result
		}
	}

	// Gemini format: contents[] with role + parts[].text
	if contentsRaw, ok := root["contents"]; ok {
		var geminiContents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}
		if err := json.Unmarshal(contentsRaw, &geminiContents); err == nil && len(geminiContents) > 0 {
			for _, gc := range geminiContents {
				var parts []string
				for _, p := range gc.Parts {
					if p.Text != "" {
						parts = append(parts, p.Text)
					}
				}
				result = append(result, map[string]any{"role": gc.Role, "content": strings.Join(parts, "\n")})
			}
			return result
		}
	}

	return nil
}
