package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	PromptBlacklistOptionKey           = "PromptBlacklist"
	SecureContentLogEnabledOptionKey   = "SecureContentLogEnabled"
	SecureContentLogRetentionOptionKey = "SecureContentLogRetentionHours"
	SecureContentLogMaxBytesOptionKey = "SecureContentLogMaxBytes"
	DefaultSecureContentLogRetention   = 24 * time.Hour
	MaxSecureContentLogBytes           = model.MaxPromptContentLogBytes
)

var promptFold = cases.Fold()
var promptBlacklistState struct {
	sync.RWMutex
	raw      string
	patterns []string
}

// NormalizePromptText applies compatibility normalization and Unicode case
// folding before security matching. NFKC handles composed/decomposed and
// compatibility forms while cases.Fold handles more than ASCII lowercasing.
func NormalizePromptText(text string) string {
	if text == "" {
		return ""
	}
	return promptFold.String(norm.NFKC.String(text))
}

func normalizePromptPatterns(patterns []string) []string {
	seen := make(map[string]struct{}, len(patterns))
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = NormalizePromptText(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if len([]byte(pattern)) > 4096 {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		result = append(result, pattern)
	}
	sort.Strings(result)
	return result
}

// ParsePromptBlacklist accepts the JSON array used by the administrator
// option, while retaining newline-separated compatibility with SensitiveWords.
func ParsePromptBlacklist(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var words []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &words); err != nil {
			return nil, fmt.Errorf("prompt blacklist must be a JSON string array: %w", err)
		}
	} else if strings.HasPrefix(raw, "{") {
		var payload struct {
			Words []string `json:"words"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, fmt.Errorf("prompt blacklist must be a JSON object: %w", err)
		}
		words = payload.Words
	} else {
		words = strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	}
	for _, word := range words {
		if !utf8.ValidString(word) {
			return nil, errors.New("prompt blacklist contains invalid UTF-8")
		}
	}
	return normalizePromptPatterns(words), nil
}

func currentPromptBlacklistRaw() string {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[PromptBlacklistOptionKey]
	common.OptionMapRWMutex.RUnlock()
	return raw
}

func PromptBlacklist() []string {
	raw := currentPromptBlacklistRaw()
	promptBlacklistState.RLock()
	if raw == promptBlacklistState.raw {
		patterns := append([]string(nil), promptBlacklistState.patterns...)
		promptBlacklistState.RUnlock()
		return patterns
	}
	promptBlacklistState.RUnlock()

	patterns, err := ParsePromptBlacklist(raw)
	if err != nil {
		return nil
	}
	promptBlacklistState.Lock()
	promptBlacklistState.raw = raw
	promptBlacklistState.patterns = append([]string(nil), patterns...)
	promptBlacklistState.Unlock()
	return patterns
}

// SetPromptBlacklist persists an administrator option and refreshes the
// process-local matcher only after model.UpdateOption has succeeded.
func SetPromptBlacklist(raw string) error {
	if _, err := ParsePromptBlacklist(raw); err != nil {
		return err
	}
	return model.UpdateOption(PromptBlacklistOptionKey, raw)
}

func PromptBlacklistEnabled() bool {
	return len(PromptBlacklist()) > 0
}

func PromptBlacklistContains(text string) (bool, []string) {
	text = NormalizePromptText(text)
	patterns := PromptBlacklist()
	if text == "" || len(patterns) == 0 {
		return false, nil
	}
	// stopImmediately=false：报告全部命中的黑名单关键词，便于管理员排查。
	return AcSearch(text, patterns, false)
}

func CheckTokenCountMetaBlacklist(meta *types.TokenCountMeta) (bool, []string) {
	if meta == nil {
		return false, nil
	}
	return PromptBlacklistContains(meta.CombineText)
}

// ExtractPromptText recursively extracts string leaves from JSON-compatible
// OpenAI, Claude, Gemini, and generic request/response values. TokenCountMeta
// is preferred because its CombineText already follows provider semantics.
func ExtractPromptText(value any) string {
	if value == nil {
		return ""
	}
	if meta, ok := value.(*types.TokenCountMeta); ok {
		if meta == nil {
			return ""
		}
		return meta.CombineText
	}
	if meta, ok := value.(types.TokenCountMeta); ok {
		return meta.CombineText
	}
	if text, ok := value.(string); ok {
		return text
	}
	if raw, ok := value.([]byte); ok {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			return ExtractPromptText(decoded)
		}
		return string(raw)
	}
	if raw, ok := value.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			return ExtractPromptText(decoded)
		}
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return ""
	}
	return extractJSONText(decoded)
}

func extractJSONText(value any) string {
	var parts []string
	var walk func(any)
	walk = func(current any) {
		switch v := current.(type) {
		case string:
			if v != "" && !isLikelyEncodedMedia(v) {
				parts = append(parts, v)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key])
			}
		}
	}
	walk(value)
	return strings.Join(parts, "\n")
}

func isLikelyEncodedMedia(value string) bool {
	if strings.HasPrefix(strings.ToLower(value), "data:") && strings.Contains(value, ";base64,") {
		return true
	}
	return len(value) > 2048 && !strings.ContainsAny(value, " \t\r\n")
}

// responseTextKeys 列出各协议响应中承载正文的字段；命中后收集其下所有字符串叶子。
var responseTextKeys = map[string]struct{}{
	"content":           {},
	"text":              {},
	"delta":             {},
	"thinking":          {},
	"reasoning_content": {},
	"refusal":           {},
	"output_text":       {},
	"input_text":        {},
	"summary":           {},
	"transcript":        {},
	"caption":           {},
	"completion":        {},
}

func extractResponseJSONText(value any, inContent bool) []string {
	var parts []string
	var walk func(any, bool)
	walk = func(current any, insideContent bool) {
		switch v := current.(type) {
		case string:
			if insideContent && v != "" && !isLikelyEncodedMedia(v) {
				parts = append(parts, v)
			}
		case []any:
			for _, item := range v {
				walk(item, insideContent)
			}
		case map[string]any:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				_, isContentKey := responseTextKeys[key]
				walk(v[key], insideContent || isContentKey)
			}
		}
	}
	walk(value, inContent)
	return parts
}

// ExtractResponseText 从非流式 JSON 响应或 SSE 流（data: 行）中提取正文文本，
// 用于安全内容日志。无法解析时原样返回（仍受 16 KiB 截断与脱敏约束）。
func ExtractResponseText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var parts []string
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var decoded any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			parts = extractResponseJSONText(decoded, false)
		}
	} else {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				continue
			}
			parts = append(parts, extractResponseJSONText(decoded, false)...)
		}
	}
	if len(parts) == 0 {
		return raw
	}
	return strings.Join(parts, "\n")
}

var sensitiveContentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(\"?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|credential)\"?\s*[:=]\s*\"?)[^\"&,\s}]+`),
	regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{8})[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)([A-Za-z0-9._%+-]+)@([A-Za-z0-9.-]+\.[A-Za-z]{2,})`),
	regexp.MustCompile(`(?m)(\+?\d[\d ()-]{7,}\d)`),
}

// RedactSecureContent removes credentials and common direct identifiers, then
// caps the UTF-8 byte representation at 16 KiB without splitting a rune.
func RedactSecureContent(content string) string {
	for _, pattern := range sensitiveContentPatterns {
		content = pattern.ReplaceAllStringFunc(content, func(match string) string {
			if strings.Contains(match, "@") && !strings.Contains(strings.ToLower(match), "email") {
				parts := strings.SplitN(match, "@", 2)
				if len(parts) == 2 {
					return maskToken(parts[0]) + "@" + parts[1]
				}
			}
			if len(match) > 8 {
				return match[:min(4, len(match))] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return truncateUTF8(content, model.PromptContentLogMaxBytes())
}

func maskToken(value string) string {
	if value == "" {
		return "[REDACTED]"
	}
	return value[:min(1, len(value))] + "[REDACTED]"
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func SecureContentLogRetention() time.Duration {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[SecureContentLogRetentionOptionKey]
	common.OptionMapRWMutex.RUnlock()
	hours, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || hours <= 0 {
		return DefaultSecureContentLogRetention
	}
	return time.Duration(hours) * time.Hour
}

func SecureContentLogEnabled() bool {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[SecureContentLogEnabledOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(raw), "true")
}

type SecureContentLogParams struct {
	UserID    int
	Username  string
	TokenID   int
	ChannelID int
	RequestID string
	ModelName string
	Direction string
	Content   string
	Now       time.Time
}

func RecordSecureContentLog(params SecureContentLogParams) error {
	if !SecureContentLogEnabled() {
		return nil
	}
	if params.Direction != model.PromptContentDirectionRequest && params.Direction != model.PromptContentDirectionResponse {
		return fmt.Errorf("invalid secure content direction %q", params.Direction)
	}
	if params.Now.IsZero() {
		params.Now = time.Now()
	}
	created := params.Now.Unix()
	entry := &model.PromptContentLog{
		CreatedAt: created,
		ExpiresAt: params.Now.Add(SecureContentLogRetention()).Unix(),
		UserID:    params.UserID,
		Username:  params.Username,
		TokenID:   params.TokenID,
		ChannelID: params.ChannelID,
		RequestID: params.RequestID,
		ModelName: params.ModelName,
		Direction: params.Direction,
		Content:   RedactSecureContent(params.Content),
	}
	return model.CreatePromptContentLog(entry)
}

// RecordSecureRequestResponse is the integration entry point for relay or
// middleware code that already has provider-specific request/response values.
// Passing TokenCountMeta avoids reparsing provider requests when available.
func RecordSecureRequestResponse(params SecureContentLogParams, requestValue, responseValue any, requestMeta *types.TokenCountMeta) error {
	if requestMeta != nil {
		params.Direction = model.PromptContentDirectionRequest
		params.Content = ExtractPromptText(requestMeta)
	} else {
		params.Direction = model.PromptContentDirectionRequest
		params.Content = ExtractPromptText(requestValue)
	}
	if err := RecordSecureContentLog(params); err != nil {
		return err
	}
	params.Direction = model.PromptContentDirectionResponse
	params.Content = ExtractPromptText(responseValue)
	if strings.TrimSpace(params.Content) == "" {
		return nil
	}
	return RecordSecureContentLog(params)
}

const promptContentLogCleanupInterval = time.Hour

// StartPromptContentLogCleanup periodically removes expired prompt content
// log rows. Cleanup runs even when logging is disabled so rows recorded
// before the switch are still purged. Only the master instance runs it.
func StartPromptContentLogCleanup() {
	if !common.IsMasterNode {
		return
	}
	go func() {
		ticker := time.NewTicker(promptContentLogCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			removed, err := model.DeleteExpiredPromptContentLogs(context.Background(), time.Now().Unix(), 500)
			if err != nil {
				common.SysError("prompt content log cleanup failed: " + err.Error())
				continue
			}
			if removed > 0 {
				common.SysLog(fmt.Sprintf("prompt content log cleanup removed %d expired rows", removed))
			}
		}
	}()
}
