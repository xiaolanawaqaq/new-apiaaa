package service

import (
	"encoding/json"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ClientAgentRateLimitConfig holds per-agent rate limit configuration.
type ClientAgentRateLimitConfig struct {
	Enabled      bool  `json:"enabled"`
	MaxRequests  int   `json:"max_requests"`
	DurationSec  int64 `json:"duration_sec"`
	BlockSeconds int64 `json:"block_seconds,omitempty"`
}

// DefaultClientAgentRateLimit returns a conservative default limit for agents
// that have no explicit per-agent configuration.
func DefaultClientAgentRateLimit() ClientAgentRateLimitConfig {
	return ClientAgentRateLimitConfig{
		Enabled:      false,
		MaxRequests:  60,
		DurationSec:  60,
		BlockSeconds: 60,
	}
}

// ParseClientAgentRateLimits parses a JSON map of agent name to config.
func ParseClientAgentRateLimits(raw string) map[string]ClientAgentRateLimitConfig {
	limits := make(map[string]ClientAgentRateLimitConfig)
	if raw == "" {
		return limits
	}
	if err := common.UnmarshalJsonStr(raw, &limits); err != nil {
		common.SysLog(common.LogText("client agent rate limits parse failed: %s", err.Error()))
		return limits
	}
	return limits
}

// MarshalClientAgentRateLimits serializes per-agent rate limits back to JSON.
func MarshalClientAgentRateLimits(limits map[string]ClientAgentRateLimitConfig) string {
	if limits == nil {
		return "{}"
	}
	data, err := common.Marshal(limits)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// ClientAgentRateLimitState tracks per-agent, per-window request counts.
type ClientAgentRateLimitState struct {
	Counts map[string]int64 `json:"counts"`
	// Windows map agent name to the window start time (unix seconds).
	Windows map[string]int64 `json:"windows"`
}

// NewClientAgentRateLimitState creates an empty state.
func NewClientAgentRateLimitState() *ClientAgentRateLimitState {
	return &ClientAgentRateLimitState{
		Counts:  make(map[string]int64),
		Windows: make(map[string]int64),
	}
}

// Allow checks whether a request for the given agent is allowed under the
// provided fixed-window configuration. It returns (allowed, remaining, retryAfterSeconds).
func (s *ClientAgentRateLimitState) Allow(agent string, config ClientAgentRateLimitConfig, now time.Time) (bool, int64, int64) {
	if !config.Enabled || config.MaxRequests <= 0 || config.DurationSec <= 0 {
		return true, int64(config.MaxRequests), 0
	}

	windowStart := now.Unix() / config.DurationSec * config.DurationSec
	key := agent

	window, ok := s.Windows[key]
	if !ok || window != windowStart {
		s.Windows[key] = windowStart
		s.Counts[key] = 0
		window = windowStart
	}

	count := s.Counts[key]
	if count >= int64(config.MaxRequests) {
		retryAfter := windowStart + config.DurationSec - now.Unix()
		if retryAfter < 0 {
			retryAfter = 0
		}
		return false, 0, retryAfter
	}

	s.Counts[key] = count + 1
	return true, int64(config.MaxRequests) - count - 1, 0
}

// MarshalJSON serializes only non-empty windows for compact storage.
func (s *ClientAgentRateLimitState) MarshalJSON() ([]byte, error) {
	counts := make(map[string]int64, len(s.Counts))
	for k, v := range s.Counts {
		if v > 0 {
			counts[k] = v
		}
	}
	windows := make(map[string]int64, len(s.Windows))
	for k, v := range s.Windows {
		if v > 0 {
			windows[k] = v
		}
	}
	type alias ClientAgentRateLimitState
	return json.Marshal(&alias{
		Counts:  counts,
		Windows: windows,
	})
}
