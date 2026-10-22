package model

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

// PromptContentLog stores a short-lived copy of request or response text.
// Rows are either redacted troubleshooting summaries or, in raw mode, the
// verbatim request/response bodies used to build training data. It lives
// outside logs so its retention policy cannot affect usage and audit logs.
type PromptContentLog struct {
	ID          uint64 `json:"id" gorm:"primaryKey"`
	CreatedAt   int64  `json:"created_at" gorm:"index:idx_prompt_content_expires,priority:1;index:idx_prompt_content_created,priority:1"`
	ExpiresAt   int64  `json:"expires_at" gorm:"index:idx_prompt_content_expires,priority:2"`
	UserID      int    `json:"user_id" gorm:"index"`
	Username    string `json:"username" gorm:"type:varchar(64);index"`
	TokenID     int    `json:"token_id" gorm:"index"`
	ChannelID   int    `json:"channel_id" gorm:"index"`
	RequestID   string `json:"request_id" gorm:"type:varchar(64);index"`
	ModelName   string `json:"model_name" gorm:"type:varchar(128);index"`
	RequestPath string `json:"request_path" gorm:"type:varchar(255);index"`
	Direction   string `json:"direction" gorm:"type:varchar(16);index"`
	// Raw marks a row whose content is an unmodified request/response body.
	Raw     bool     `json:"raw" gorm:"index"`
	Content LongText `json:"content"`
}

func (PromptContentLog) TableName() string {
	return "prompt_content_logs"
}

type PromptContentLogFilter struct {
	UserID    int
	TokenID   int
	ChannelID int
	RequestID string
	ModelName string
	Direction string
	StartAt   int64
	EndAt     int64
	// RawOnly restricts the query to verbatim bodies captured in raw mode.
	RawOnly bool
}

const (
	PromptContentDirectionRequest  = "request"
	PromptContentDirectionResponse = "response"
	// MaxPromptContentLogBytes is the per-entry cap for redacted troubleshooting
	// summaries. Raw training-data rows may use up to MaxRawContentLogBytes.
	MaxPromptContentLogBytes = 256 * 1024
	// MaxRawContentLogBytes caps a verbatim request/response body kept for
	// training-data export. The column is LongText, so this is a policy limit
	// rather than a storage limit.
	MaxRawContentLogBytes = 2 * 1024 * 1024
)

// MaxSecureContentLogBytesLimit bounds the configurable per-entry size so a
// misconfigured option cannot bloat the log database.
const MaxSecureContentLogBytesLimit = 2 * 1024 * 1024

// PromptContentLogMaxBytes returns the configured per-entry byte cap. The
// option is clamped to [1, MaxSecureContentLogBytesLimit]; the constant
// default applies when the option is missing or invalid.
func PromptContentLogMaxBytes() int {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["SecureContentLogMaxBytes"]
	common.OptionMapRWMutex.RUnlock()
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed <= 0 {
		return MaxPromptContentLogBytes
	}
	if parsed > MaxSecureContentLogBytesLimit {
		return MaxSecureContentLogBytesLimit
	}
	return parsed
}

func MigratePromptContentLogs() error {
	if LOG_DB == nil {
		return errors.New("log database is unavailable")
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return LOG_DB.Exec(`CREATE TABLE IF NOT EXISTS prompt_content_logs (
			id UInt64 DEFAULT 0,
			created_at Int64,
			expires_at Int64,
				user_id Int32 DEFAULT 0,
				username String DEFAULT '',
				token_id Int32 DEFAULT 0,
				channel_id Int32 DEFAULT 0,
				request_id String DEFAULT '',

			model_name String DEFAULT '',
			request_path String DEFAULT '',
			direction String DEFAULT '',
			raw Bool DEFAULT false,
			content String DEFAULT ''
		) ENGINE = MergeTree()
		PARTITION BY toYYYYMM(toDateTime(created_at))
		ORDER BY (created_at, id)`).Error
	}
	return LOG_DB.AutoMigrate(&PromptContentLog{})
}

func CreatePromptContentLog(entry *PromptContentLog) error {
	if entry == nil {
		return errors.New("prompt content log is nil")
	}
	if LOG_DB == nil {
		return errors.New("log database is unavailable")
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	if entry.ExpiresAt <= entry.CreatedAt {
		return errors.New("prompt content log expiration must be after creation")
	}
	if entry.Direction != PromptContentDirectionRequest && entry.Direction != PromptContentDirectionResponse {
		return fmt.Errorf("invalid prompt content log direction %q", entry.Direction)
	}
	maxBytes := PromptContentLogMaxBytes()
	if entry.Raw {
		if limit := MaxRawContentLogBytes; maxBytes < limit {
			maxBytes = limit
		}
	}
	entry.Content = LongText(truncateUTF8Bytes(string(entry.Content), maxBytes))
	return LOG_DB.Create(entry).Error
}

func truncatePromptContentLog(value string) string {
	return truncateUTF8Bytes(value, PromptContentLogMaxBytes())
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func GetPromptContentLogs(filter PromptContentLogFilter, start, limit int) ([]*PromptContentLog, int64, error) {
	if LOG_DB == nil {
		return nil, 0, errors.New("log database is unavailable")
	}
	if start < 0 {
		start = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := LOG_DB.Model(&PromptContentLog{}).Where("expires_at > ?", time.Now().Unix())
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.TokenID > 0 {
		query = query.Where("token_id = ?", filter.TokenID)
	}
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.Direction != "" {
		query = query.Where("direction = ?", filter.Direction)
	}
	if filter.StartAt > 0 {
		query = query.Where("created_at >= ?", filter.StartAt)
	}
	if filter.EndAt > 0 {
		query = query.Where("created_at <= ?", filter.EndAt)
	}
	if filter.RawOnly {
		query = query.Where("raw = ?", true)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	entries := make([]*PromptContentLog, 0)
	if err := query.Order("created_at DESC").Order("id DESC").Offset(start).Limit(limit).Find(&entries).Error; err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// PromptContentPair is one request/response pair as captured in raw mode. It
// is the export shape for training data: both bodies verbatim, plus the
// routing metadata needed to group or filter samples later.
type PromptContentPair struct {
	RequestID   string `json:"request_id"`
	CreatedAt   int64  `json:"created_at"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	TokenID     int    `json:"token_id"`
	ChannelID   int    `json:"channel_id"`
	ModelName   string `json:"model_name"`
	RequestPath string `json:"request_path"`
	Request     string `json:"request,omitempty"`
	Response    string `json:"response,omitempty"`
}

// StreamRawPromptContentPairs walks raw-mode rows newest first and calls fn with
// each complete request/response pair. Pairs are assembled per request_id in
// memory: a request row and its response row are written by the same handler,
// so they are adjacent in practice, but a bounded map keeps the result correct
// when they are not. Rows without a response are still emitted with Request
// set, so failed or streaming-aborted samples remain visible to the exporter.
func StreamRawPromptContentPairs(
	ctx context.Context,
	filter PromptContentLogFilter,
	fn func(PromptContentPair) error,
) error {
	if LOG_DB == nil {
		return errors.New("log database is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	query := LOG_DB.WithContext(ctx).Model(&PromptContentLog{}).
		Where("raw = ?", true).
		Where("expires_at > ?", time.Now().Unix())
	if filter.StartAt > 0 {
		query = query.Where("created_at >= ?", filter.StartAt)
	}
	if filter.EndAt > 0 {
		query = query.Where("created_at <= ?", filter.EndAt)
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.TokenID > 0 {
		query = query.Where("token_id = ?", filter.TokenID)
	}
	if filter.ChannelID > 0 {
		query = query.Where("channel_id = ?", filter.ChannelID)
	}
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}

	rows, err := query.
		Select("request_id, created_at, user_id, username, token_id, channel_id, model_name, request_path, direction, content").
		Order("created_at ASC").
		Order("id ASC").
		Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	pending := make(map[string]*PromptContentPair)
	// A response row normally follows its request row immediately, so the map
	// stays small. The cap only guards against a producer that writes requests
	// without responses (for example an aborted stream).
	const maxPendingPairs = 2000
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row struct {
			RequestID   string   `gorm:"column:request_id"`
			CreatedAt   int64    `gorm:"column:created_at"`
			UserID      int      `gorm:"column:user_id"`
			Username    string   `gorm:"column:username"`
			TokenID     int      `gorm:"column:token_id"`
			ChannelID   int      `gorm:"column:channel_id"`
			ModelName   string   `gorm:"column:model_name"`
			RequestPath string   `gorm:"column:request_path"`
			Direction   string   `gorm:"column:direction"`
			Content     LongText `gorm:"column:content"`
		}
		if err := LOG_DB.ScanRows(rows, &row); err != nil {
			return err
		}
		pair := pending[row.RequestID]
		if pair == nil {
			pair = &PromptContentPair{
				RequestID:   row.RequestID,
				CreatedAt:   row.CreatedAt,
				UserID:      row.UserID,
				Username:    row.Username,
				TokenID:     row.TokenID,
				ChannelID:   row.ChannelID,
				ModelName:   row.ModelName,
				RequestPath: row.RequestPath,
			}
			pending[row.RequestID] = pair
		}
		if row.Direction == PromptContentDirectionResponse {
			pair.Response = string(row.Content)
		} else {
			pair.Request = string(row.Content)
		}
		// Emit as soon as both halves are present, so memory stays bounded on
		// large exports. Requests without a response are flushed at the end.
		if pair.Request != "" && pair.Response != "" {
			if err := fn(*pair); err != nil {
				return err
			}
			delete(pending, row.RequestID)
		}
		if len(pending) >= maxPendingPairs {
			for id, stale := range pending {
				if stale.Request == "" {
					continue
				}
				if err := fn(*stale); err != nil {
					return err
				}
				delete(pending, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, pair := range pending {
		if pair.Request == "" {
			continue
		}
		if err := fn(*pair); err != nil {
			return err
		}
	}
	return nil
}

// DeleteExpiredPromptContentLogs deletes at most batch rows and returns the
// number removed. Callers can invoke it periodically from the existing task
// scheduler; no independent goroutine is started by this module.
func DeleteExpiredPromptContentLogs(ctx context.Context, now int64, batch int) (int64, error) {
	if LOG_DB == nil {
		return 0, errors.New("log database is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	if batch <= 0 {
		batch = 500
	}
	if batch > 5000 {
		batch = 5000
	}

	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		var total int64
		if err := LOG_DB.WithContext(ctx).Model(&PromptContentLog{}).Where("expires_at <= ?", now).Count(&total).Error; err != nil {
			return 0, err
		}
		if total == 0 {
			return 0, nil
		}
		if err := LOG_DB.WithContext(ctx).Exec("ALTER TABLE prompt_content_logs DELETE WHERE expires_at <= ? SETTINGS mutations_sync = 1", now).Error; err != nil {
			return 0, err
		}
		return total, nil
	}

	var ids []uint64
	if err := LOG_DB.WithContext(ctx).Model(&PromptContentLog{}).
		Where("expires_at <= ?", now).
		Order("expires_at ASC").Limit(batch).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := LOG_DB.WithContext(ctx).Where("id IN ?", ids).Delete(&PromptContentLog{})
	return result.RowsAffected, result.Error
}
