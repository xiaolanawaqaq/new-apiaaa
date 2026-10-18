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
