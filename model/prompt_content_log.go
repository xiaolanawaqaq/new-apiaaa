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

// PromptContentLog stores a short-lived, redacted copy of request or response
// text for administrator troubleshooting. It intentionally lives outside logs
// so its retention policy cannot affect usage and audit logs.
type PromptContentLog struct {
	ID        uint64 `json:"id" gorm:"primaryKey"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_prompt_content_expires,priority:1;index:idx_prompt_content_created,priority:1"`
	ExpiresAt int64  `json:"expires_at" gorm:"index:idx_prompt_content_expires,priority:2"`
	UserID    int    `json:"user_id" gorm:"index"`
	Username  string `json:"username" gorm:"type:varchar(64);index"`
	TokenID   int    `json:"token_id" gorm:"index"`
	ChannelID int    `json:"channel_id" gorm:"index"`
	RequestID string `json:"request_id" gorm:"type:varchar(64);index"`
	ModelName string `json:"model_name" gorm:"type:varchar(128);index"`
	Direction string `json:"direction" gorm:"type:varchar(16);index"`
	Content   string `json:"content" gorm:"type:text"`
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
}

const (
	PromptContentDirectionRequest  = "request"
	PromptContentDirectionResponse = "response"
	MaxPromptContentLogBytes       = 16 * 1024
)

// MaxSecureContentLogBytesLimit bounds the configurable per-entry size so a
// misconfigured option cannot bloat the log database.
const MaxSecureContentLogBytesLimit = 64 * 1024

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
			direction String DEFAULT '',
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
	entry.Content = truncatePromptContentLog(entry.Content)
	return LOG_DB.Create(entry).Error
}

func truncatePromptContentLog(value string) string {
	maxBytes := PromptContentLogMaxBytes()
	if len(value) <= maxBytes {
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
