package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientAgentRateLimitDefaultDisabled(t *testing.T) {
	state := NewClientAgentRateLimitState()
	allowed, remaining, retryAfter := state.Allow("Cursor 0.48.7", DefaultClientAgentRateLimit(), time.Now())
	assert.True(t, allowed)
	assert.Equal(t, int64(60), remaining)
	assert.Equal(t, int64(0), retryAfter)
}

func TestClientAgentRateLimitEnforcesWindow(t *testing.T) {
	state := NewClientAgentRateLimitState()
	config := ClientAgentRateLimitConfig{
		Enabled:      true,
		MaxRequests:  3,
		DurationSec:  60,
		BlockSeconds: 60,
	}
	now := time.Now()

	for i := 0; i < 3; i++ {
		allowed, remaining, _ := state.Allow("Cursor 0.48.7", config, now)
		assert.True(t, allowed, "request %d should be allowed", i)
		assert.Equal(t, int64(2-i), remaining)
	}

	allowed, remaining, retryAfter := state.Allow("Cursor 0.48.7", config, now)
	assert.False(t, allowed)
	assert.Equal(t, int64(0), remaining)
	assert.GreaterOrEqual(t, retryAfter, int64(0))
	assert.LessOrEqual(t, retryAfter, int64(60))
}

func TestClientAgentRateLimitWindowRollsOver(t *testing.T) {
	state := NewClientAgentRateLimitState()
	config := ClientAgentRateLimitConfig{
		Enabled:      true,
		MaxRequests:  1,
		DurationSec:  1,
		BlockSeconds: 1,
	}
	now := time.Now()

	allowed, _, _ := state.Allow("Cursor 0.48.7", config, now)
	assert.True(t, allowed)

	allowed, _, _ = state.Allow("Cursor 0.48.7", config, now)
	assert.False(t, allowed)

	allowed, _, _ = state.Allow("Cursor 0.48.7", config, now.Add(2*time.Second))
	assert.True(t, allowed)
}

func TestClientAgentRateLimitIndependentAgents(t *testing.T) {
	state := NewClientAgentRateLimitState()
	config := ClientAgentRateLimitConfig{
		Enabled:      true,
		MaxRequests:  1,
		DurationSec:  60,
		BlockSeconds: 60,
	}
	now := time.Now()

	allowed, _, _ := state.Allow("Cursor 0.48.7", config, now)
	assert.True(t, allowed)

	allowed, _, _ = state.Allow("Cursor 0.48.7", config, now)
	assert.False(t, allowed)

	allowed, _, _ = state.Allow("Cline 3.4.0", config, now)
	assert.True(t, allowed)
}

func TestParseClientAgentRateLimits(t *testing.T) {
	raw := `{"_global":{"enabled":true,"max_requests":10,"duration_sec":60,"block_seconds":30},"Cursor 0.48.7":{"enabled":true,"max_requests":5,"duration_sec":60,"block_seconds":10}}`
	limits := ParseClientAgentRateLimits(raw)
	require.Len(t, limits, 2)

	global, ok := limits["_global"]
	require.True(t, ok)
	assert.True(t, global.Enabled)
	assert.Equal(t, 10, global.MaxRequests)
	assert.Equal(t, int64(60), global.DurationSec)
	assert.Equal(t, int64(30), global.BlockSeconds)

	cursor, ok := limits["Cursor 0.48.7"]
	require.True(t, ok)
	assert.True(t, cursor.Enabled)
	assert.Equal(t, 5, cursor.MaxRequests)
	assert.Equal(t, int64(10), cursor.BlockSeconds)
}

func TestParseClientAgentRateLimitsInvalidJSON(t *testing.T) {
	limits := ParseClientAgentRateLimits(`{not json`)
	assert.Empty(t, limits)
}

func TestMarshalClientAgentRateLimits(t *testing.T) {
	limits := map[string]ClientAgentRateLimitConfig{
		"_global": {
			Enabled:      true,
			MaxRequests:  5,
			DurationSec:  30,
			BlockSeconds: 10,
		},
	}
	marshaled := MarshalClientAgentRateLimits(limits)
	assert.Contains(t, marshaled, `"max_requests":5`)
	assert.Contains(t, marshaled, `"duration_sec":30`)

	empty := MarshalClientAgentRateLimits(nil)
	assert.Equal(t, "{}", empty)
}
