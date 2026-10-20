package model

import (
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	clientAgentUsageDefaultLimit = 20000
	clientAgentUsageMaxLimit     = 50000
	clientAgentUsageMaxRows      = 50
	clientAgentUsageUAMaxLength  = 160
)

// ClientAgentUsageQuery bounds a client-agent aggregation to the same filters
// the usage-log list accepts. Timestamps are unix seconds.
type ClientAgentUsageQuery struct {
	StartTimestamp int64
	EndTimestamp   int64
	Username       string
	ModelName      string
	TokenName      string
	Channel        int
	Group          string
	Limit          int
}

// ClientAgentUsageRow is one aggregated calling client. Rows without a
// recognized agent are grouped by their normalized raw User-Agent so newly
// appearing tools stay visible until a detection rule is added.
type ClientAgentUsageRow struct {
	ClientAgent      string `json:"client_agent"`
	RawUserAgent     string `json:"user_agent,omitempty"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	Quota            int64  `json:"quota"`
	LastSeen         int64  `json:"last_seen"`
}

// ClientAgentUsageResult carries the aggregated rows plus the scan bound that
// produced them, so callers can tell an exhaustive answer from a truncated one.
type ClientAgentUsageResult struct {
	Items       []ClientAgentUsageRow `json:"items"`
	Unknown     []ClientAgentUsageRow `json:"unknown_user_agents"`
	Scanned     int                   `json:"scanned"`
	Truncated   bool                  `json:"truncated"`
	GeneratedAt int64                 `json:"generated_at"`
}

type clientAgentUsageAccumulator struct {
	row ClientAgentUsageRow
}

// SumClientAgentUsage aggregates consume and error logs by the client identity
// recorded in other.admin_info. Aggregation runs in Go because the identity
// lives inside a JSON column, which cannot be grouped portably across SQLite,
// MySQL, PostgreSQL, and ClickHouse.
func SumClientAgentUsage(query ClientAgentUsageQuery) (ClientAgentUsageResult, error) {
	result := ClientAgentUsageResult{GeneratedAt: time.Now().Unix()}
	limit := query.Limit
	if limit <= 0 {
		limit = clientAgentUsageDefaultLimit
	}
	if limit > clientAgentUsageMaxLimit {
		limit = clientAgentUsageMaxLimit
	}

	tx := LOG_DB.Table("logs").
		Select("created_at, type, prompt_tokens, completion_tokens, quota, other").
		Where("type IN ?", []int{LogTypeConsume, LogTypeError})
	if query.StartTimestamp != 0 {
		tx = tx.Where("created_at >= ?", query.StartTimestamp)
	}
	if query.EndTimestamp != 0 {
		tx = tx.Where("created_at <= ?", query.EndTimestamp)
	}
	if query.Username != "" {
		tx = tx.Where("username = ?", query.Username)
	}
	if query.ModelName != "" {
		tx = tx.Where("model_name = ?", query.ModelName)
	}
	if query.TokenName != "" {
		tx = tx.Where("token_name = ?", query.TokenName)
	}
	if query.Channel != 0 {
		tx = tx.Where("channel_id = ?", query.Channel)
	}
	if query.Group != "" {
		tx = tx.Where(logGroupCol+" = ?", query.Group)
	}

	var rows struct {
		CreatedAt        int64
		Type             int
		PromptTokens     int64
		CompletionTokens int64
		Quota            int64
		Other            string
	}
	cursor, err := tx.Order("created_at DESC").Limit(limit).Rows()
	if err != nil {
		return result, err
	}
	defer cursor.Close()

	known := make(map[string]*clientAgentUsageAccumulator)
	unknown := make(map[string]*clientAgentUsageAccumulator)
	for cursor.Next() {
		if err := LOG_DB.ScanRows(cursor, &rows); err != nil {
			return result, err
		}
		result.Scanned++
		agent, rawUserAgent := clientAgentFromLogOther(rows.Other)
		isUnknown := agent == ""
		bucket := known
		key := agent
		if isUnknown {
			bucket = unknown
			key = normalizeUnknownUserAgent(rawUserAgent)
		}
		accumulator := bucket[key]
		if accumulator == nil {
			accumulator = &clientAgentUsageAccumulator{row: ClientAgentUsageRow{ClientAgent: agent}}
			if isUnknown {
				accumulator.row.RawUserAgent = key
			}
			bucket[key] = accumulator
		}
		if rows.Type == LogTypeConsume {
			accumulator.row.Requests++
			accumulator.row.PromptTokens += rows.PromptTokens
			accumulator.row.CompletionTokens += rows.CompletionTokens
			accumulator.row.Quota += rows.Quota
		} else {
			accumulator.row.Errors++
		}
		if rows.CreatedAt > accumulator.row.LastSeen {
			accumulator.row.LastSeen = rows.CreatedAt
		}
	}
	if err := cursor.Err(); err != nil {
		return result, err
	}
	result.Truncated = result.Scanned >= limit
	result.Items = sortedClientAgentUsageRows(known)
	result.Unknown = sortedClientAgentUsageRows(unknown)
	return result, nil
}

func sortedClientAgentUsageRows(bucket map[string]*clientAgentUsageAccumulator) []ClientAgentUsageRow {
	rows := make([]ClientAgentUsageRow, 0, len(bucket))
	for _, accumulator := range bucket {
		rows = append(rows, accumulator.row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Requests != right.Requests {
			return left.Requests > right.Requests
		}
		if left.Errors != right.Errors {
			return left.Errors > right.Errors
		}
		return left.LastSeen > right.LastSeen
	})
	if len(rows) > clientAgentUsageMaxRows {
		rows = rows[:clientAgentUsageMaxRows]
	}
	return rows
}

// clientAgentFromLogOther extracts the admin-only client identity. Older logs
// never wrote these fields, so both values are optional.
func clientAgentFromLogOther(other string) (agent string, rawUserAgent string) {
	if strings.TrimSpace(other) == "" {
		return "", ""
	}
	var parsed map[string]any
	if err := common.UnmarshalJsonStr(other, &parsed); err != nil {
		return "", ""
	}
	adminInfo, _ := parsed["admin_info"].(map[string]any)
	if adminInfo == nil {
		return "", ""
	}
	agent, _ = adminInfo["client_agent"].(string)
	rawUserAgent, _ = adminInfo["user_agent"].(string)
	return strings.TrimSpace(agent), strings.TrimSpace(rawUserAgent)
}

// normalizeUnknownUserAgent keeps unrecognized callers distinguishable without
// letting one enormous or binary header fragment dominate the result.
func normalizeUnknownUserAgent(rawUserAgent string) string {
	rawUserAgent = strings.TrimSpace(rawUserAgent)
	if rawUserAgent == "" {
		return ""
	}
	if len(rawUserAgent) > clientAgentUsageUAMaxLength {
		rawUserAgent = strings.ToValidUTF8(rawUserAgent[:clientAgentUsageUAMaxLength], "")
	}
	return rawUserAgent
}

const (
	clientAgentLogsDefaultPageSize = 20
	clientAgentLogsMaxPageSize     = 100
	clientAgentLogsScanLimit       = 50000
)

// ClientAgentLogQuery selects the individual logs behind one aggregated row.
// ClientAgent identifies a recognized agent; otherwise RawUserAgent selects an
// unrecognized bucket (empty matches logs with no client identity at all).
type ClientAgentLogQuery struct {
	ClientAgent    string
	RawUserAgent   string
	Unknown        bool
	StartTimestamp int64
	EndTimestamp   int64
	Username       string
	ModelName      string
	TokenName      string
	Channel        int
	Group          string
	Page           int
	PageSize       int
}

// ClientAgentLogItem is the request-level detail shown when an administrator
// drills into a client. It carries no credentials and no admin-only routing
// metadata beyond the identifiers already visible in the log list.
type ClientAgentLogItem struct {
	Id               int    `json:"id"`
	CreatedAt        int64  `json:"created_at"`
	Type             int    `json:"type"`
	ModelName        string `json:"model_name"`
	TokenName        string `json:"token_name"`
	Username         string `json:"username"`
	ChannelId        int    `json:"channel"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Quota            int    `json:"quota"`
	UseTime          int    `json:"use_time"`
	IsStream         bool   `json:"is_stream"`
	Content          string `json:"content"`
	RequestId        string `json:"request_id"`
}

// ClientAgentLogsResult is bounded by the same scan window as the aggregation,
// so Total counts matches within the examined logs rather than all history.
type ClientAgentLogsResult struct {
	Items     []ClientAgentLogItem `json:"items"`
	Total     int                  `json:"total"`
	Page      int                  `json:"page"`
	PageSize  int                  `json:"page_size"`
	Scanned   int                  `json:"scanned"`
	Truncated bool                 `json:"truncated"`
}

// ListClientAgentLogs returns one page of logs for a single client bucket.
func ListClientAgentLogs(query ClientAgentLogQuery) (ClientAgentLogsResult, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = clientAgentLogsDefaultPageSize
	}
	if pageSize > clientAgentLogsMaxPageSize {
		pageSize = clientAgentLogsMaxPageSize
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	result := ClientAgentLogsResult{Page: page, PageSize: pageSize}

	tx := LOG_DB.Table("logs").
		Select("id, created_at, type, model_name, token_name, username, channel_id, prompt_tokens, completion_tokens, quota, use_time, is_stream, content, request_id, other").
		Where("type IN ?", []int{LogTypeConsume, LogTypeError})
	if query.StartTimestamp != 0 {
		tx = tx.Where("created_at >= ?", query.StartTimestamp)
	}
	if query.EndTimestamp != 0 {
		tx = tx.Where("created_at <= ?", query.EndTimestamp)
	}
	if query.Username != "" {
		tx = tx.Where("username = ?", query.Username)
	}
	if query.ModelName != "" {
		tx = tx.Where("model_name = ?", query.ModelName)
	}
	if query.TokenName != "" {
		tx = tx.Where("token_name = ?", query.TokenName)
	}
	if query.Channel != 0 {
		tx = tx.Where("channel_id = ?", query.Channel)
	}
	if query.Group != "" {
		tx = tx.Where(logGroupCol+" = ?", query.Group)
	}

	cursor, err := tx.Order("created_at DESC, id DESC").Limit(clientAgentLogsScanLimit).Rows()
	if err != nil {
		return result, err
	}
	defer cursor.Close()

	skip := (page - 1) * pageSize
	result.Items = make([]ClientAgentLogItem, 0, pageSize)
	for cursor.Next() {
		var row struct {
			Id               int    `gorm:"column:id"`
			CreatedAt        int64  `gorm:"column:created_at"`
			Type             int    `gorm:"column:type"`
			ModelName        string `gorm:"column:model_name"`
			TokenName        string `gorm:"column:token_name"`
			Username         string `gorm:"column:username"`
			ChannelId        int    `gorm:"column:channel_id"`
			PromptTokens     int    `gorm:"column:prompt_tokens"`
			CompletionTokens int    `gorm:"column:completion_tokens"`
			Quota            int    `gorm:"column:quota"`
			UseTime          int    `gorm:"column:use_time"`
			IsStream         bool   `gorm:"column:is_stream"`
			Content          string `gorm:"column:content"`
			RequestId        string `gorm:"column:request_id"`
			Other            string `gorm:"column:other"`
		}
		if err := LOG_DB.ScanRows(cursor, &row); err != nil {
			return result, err
		}
		result.Scanned++
		agent, rawUserAgent := clientAgentFromLogOther(row.Other)
		if query.Unknown {
			if agent != "" || normalizeUnknownUserAgent(rawUserAgent) != query.RawUserAgent {
				continue
			}
		} else if agent != query.ClientAgent {
			continue
		}
		result.Total++
		if skip > 0 {
			skip--
			continue
		}
		if len(result.Items) < pageSize {
			result.Items = append(result.Items, ClientAgentLogItem{
				Id:               row.Id,
				CreatedAt:        row.CreatedAt,
				Type:             row.Type,
				ModelName:        row.ModelName,
				TokenName:        row.TokenName,
				Username:         row.Username,
				ChannelId:        row.ChannelId,
				PromptTokens:     row.PromptTokens,
				CompletionTokens: row.CompletionTokens,
				Quota:            row.Quota,
				UseTime:          row.UseTime,
				IsStream:         row.IsStream,
				Content:          row.Content,
				RequestId:        row.RequestId,
			})
		}
	}
	if err := cursor.Err(); err != nil {
		return result, err
	}
	result.Truncated = result.Scanned >= clientAgentLogsScanLimit
	return result, nil
}

// ClientAgentUsageTrendPoint is one time-bucket of client-agent usage trend data.
type ClientAgentUsageTrendPoint struct {
	WindowStart int64 `json:"window_start"` // unix seconds
	Requests    int64 `json:"requests"`
	Errors      int64 `json:"errors"`
	Quota       int64 `json:"quota"`
}

// ClientAgentUsageTrendQuery bounds a client-agent usage trend query.
type ClientAgentUsageTrendQuery struct {
	StartTimestamp int64
	EndTimestamp   int64
	Username       string
	ModelName      string
	TokenName      string
	Channel        int
	Group          string
	ClientAgent    string
	RawUserAgent   string
	Unknown        bool
	// BucketSeconds is the time bucket width in seconds (e.g. 3600 for hourly).
	BucketSeconds int64
}

// SumClientAgentUsageTrend aggregates consume and error logs into time buckets
// for client-agent trend charts. Aggregation runs in Go because the client
// identity lives inside a JSON column.
func SumClientAgentUsageTrend(query ClientAgentUsageTrendQuery) ([]ClientAgentUsageTrendPoint, error) {
	bucketSeconds := query.BucketSeconds
	if bucketSeconds <= 0 {
		bucketSeconds = 3600
	}

	tx := LOG_DB.Table("logs").
		Select("created_at, type, quota, other").
		Where("type IN ?", []int{LogTypeConsume, LogTypeError})
	if query.StartTimestamp != 0 {
		tx = tx.Where("created_at >= ?", query.StartTimestamp)
	}
	if query.EndTimestamp != 0 {
		tx = tx.Where("created_at <= ?", query.EndTimestamp)
	}
	if query.Username != "" {
		tx = tx.Where("username = ?", query.Username)
	}
	if query.ModelName != "" {
		tx = tx.Where("model_name = ?", query.ModelName)
	}
	if query.TokenName != "" {
		tx = tx.Where("token_name = ?", query.TokenName)
	}
	if query.Channel != 0 {
		tx = tx.Where("channel_id = ?", query.Channel)
	}
	if query.Group != "" {
		tx = tx.Where(logGroupCol+" = ?", query.Group)
	}

	var rows struct {
		CreatedAt int64
		Type      int
		Quota     int64
		Other     string
	}
	cursor, err := tx.Order("created_at ASC").Rows()
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	buckets := make(map[int64]*ClientAgentUsageTrendPoint)
	for cursor.Next() {
		if err := LOG_DB.ScanRows(cursor, &rows); err != nil {
			return nil, err
		}
		agent, rawUserAgent := clientAgentFromLogOther(rows.Other)
		if query.ClientAgent != "" && agent != query.ClientAgent {
			continue
		}
		if query.Unknown {
			if agent != "" || normalizeUnknownUserAgent(rawUserAgent) != query.RawUserAgent {
				continue
			}
		}

		window := rows.CreatedAt / bucketSeconds * bucketSeconds
		point := buckets[window]
		if point == nil {
			point = &ClientAgentUsageTrendPoint{WindowStart: window}
			buckets[window] = point
		}
		point.Requests++
		if rows.Type == LogTypeError {
			point.Errors++
		}
		point.Quota += rows.Quota
	}

	points := make([]ClientAgentUsageTrendPoint, 0, len(buckets))
	for _, point := range buckets {
		points = append(points, *point)
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].WindowStart < points[j].WindowStart
	})
	return points, nil
}
