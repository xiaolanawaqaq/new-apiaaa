package model

import (
	"math"
	"sync"
	"time"
)

const (
	channelDispatchStatsTTL        = 30 * time.Minute
	channelDispatchMinStats        = 2
	channelDispatchLatencyBaseline = 1000.0
)

type ChannelDispatchStats struct {
	Attempts          uint64 `json:"attempts"`
	Successes         uint64 `json:"successes"`
	Failures          uint64 `json:"failures"`
	TemporaryFailures uint64 `json:"temporary_failures"`
	BalanceExhausted  uint64 `json:"balance_exhausted"`
	InFlight          int64  `json:"in_flight"`
	EWMALatencyMS     int64  `json:"ewma_latency_ms"`
	LastErrorStatus   int    `json:"last_error_status"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	LastErrorKind     string `json:"last_error_kind,omitempty"`
	LastSuccessAt     int64  `json:"last_success_at"`
	LastFailureAt     int64  `json:"last_failure_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

type channelDispatchStatsEntry struct {
	ChannelDispatchStats
	lastActivity time.Time
}

var channelDispatchStats = struct {
	sync.Mutex
	entries map[int]*channelDispatchStatsEntry
}{entries: make(map[int]*channelDispatchStatsEntry)}

func pruneChannelDispatchStatsLocked(now time.Time) {
	for channelID, entry := range channelDispatchStats.entries {
		if now.Sub(entry.lastActivity) > channelDispatchStatsTTL {
			delete(channelDispatchStats.entries, channelID)
		}
	}
}

func channelDispatchEntryLocked(channelID int, now time.Time) *channelDispatchStatsEntry {
	entry := channelDispatchStats.entries[channelID]
	if entry == nil {
		entry = &channelDispatchStatsEntry{}
		channelDispatchStats.entries[channelID] = entry
	}
	entry.lastActivity = now
	entry.UpdatedAt = now.UnixMilli()
	return entry
}

func RecordChannelDispatchAttempt(channelID int) {
	if channelID <= 0 {
		return
	}
	now := time.Now()
	channelDispatchStats.Lock()
	defer channelDispatchStats.Unlock()
	pruneChannelDispatchStatsLocked(now)
	entry := channelDispatchEntryLocked(channelID, now)
	entry.Attempts++
	entry.InFlight++
}

func RecordChannelDispatchSuccess(channelID int, latency time.Duration) {
	if channelID <= 0 {
		return
	}
	now := time.Now()
	channelDispatchStats.Lock()
	defer channelDispatchStats.Unlock()
	pruneChannelDispatchStatsLocked(now)
	entry := channelDispatchEntryLocked(channelID, now)
	if entry.InFlight > 0 {
		entry.InFlight--
	}
	entry.Successes++
	entry.LastSuccessAt = now.UnixMilli()
	latencyMS := max(latency.Milliseconds(), int64(0))
	if entry.EWMALatencyMS == 0 {
		entry.EWMALatencyMS = latencyMS
	} else {
		entry.EWMALatencyMS = (entry.EWMALatencyMS*7 + latencyMS*3) / 10
	}
}

func RecordChannelDispatchFailure(channelID int, status int, errorCode, kind string, latency time.Duration) {
	if channelID <= 0 {
		return
	}
	now := time.Now()
	channelDispatchStats.Lock()
	defer channelDispatchStats.Unlock()
	pruneChannelDispatchStatsLocked(now)
	entry := channelDispatchEntryLocked(channelID, now)
	if entry.InFlight > 0 {
		entry.InFlight--
	}
	entry.Failures++
	entry.LastErrorStatus = status
	entry.LastErrorCode = errorCode
	entry.LastErrorKind = kind
	entry.LastFailureAt = now.UnixMilli()
	switch kind {
	case "temporary":
		entry.TemporaryFailures++
	case "balance_exhausted":
		entry.BalanceExhausted++
	}
	latencyMS := max(latency.Milliseconds(), int64(0))
	if latencyMS > 0 {
		if entry.EWMALatencyMS == 0 {
			entry.EWMALatencyMS = latencyMS
		} else {
			entry.EWMALatencyMS = (entry.EWMALatencyMS*7 + latencyMS*3) / 10
		}
	}
}

func ChannelDispatchStatsFor(channelID int) ChannelDispatchStats {
	if channelID <= 0 {
		return ChannelDispatchStats{}
	}
	now := time.Now()
	channelDispatchStats.Lock()
	defer channelDispatchStats.Unlock()
	pruneChannelDispatchStatsLocked(now)
	if entry := channelDispatchStats.entries[channelID]; entry != nil {
		return entry.ChannelDispatchStats
	}
	return ChannelDispatchStats{}
}

func AllChannelDispatchStats() map[int]ChannelDispatchStats {
	now := time.Now()
	channelDispatchStats.Lock()
	defer channelDispatchStats.Unlock()
	pruneChannelDispatchStatsLocked(now)
	stats := make(map[int]ChannelDispatchStats, len(channelDispatchStats.entries))
	for channelID, entry := range channelDispatchStats.entries {
		stats[channelID] = entry.ChannelDispatchStats
	}
	return stats
}

func channelSelectionMultiplier(channelID int) float64 {
	stats := ChannelDispatchStatsFor(channelID)
	if stats.Attempts < channelDispatchMinStats {
		return 1
	}

	successRate := float64(stats.Successes) / float64(stats.Attempts)
	reliability := 0.5 + 0.5*successRate
	latencyFactor := 1.0
	if stats.EWMALatencyMS > 0 {
		latencyFactor = channelDispatchLatencyBaseline / (channelDispatchLatencyBaseline + float64(stats.EWMALatencyMS))
		latencyFactor = max(latencyFactor, 0.35)
	}
	recentFailureFactor := 1.0
	if stats.LastFailureAt > 0 && time.Since(time.UnixMilli(stats.LastFailureAt)) < time.Minute {
		recentFailureFactor = 0.65
		if stats.LastErrorKind == "balance_exhausted" {
			recentFailureFactor = 0.35
		}
	}
	return max(0.2, reliability*latencyFactor*recentFailureFactor)
}

func channelSelectionWeight(channel *Channel) int {
	if channel == nil {
		return 0
	}
	base := channel.GetWeight()
	if base <= 0 {
		base = 100
	}
	weighted := float64(base) * channelSelectionMultiplier(channel.Id)
	return max(1, int(math.Round(weighted)))
}
