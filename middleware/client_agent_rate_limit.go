package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// clientAgentRateLimitNamespace is the Redis key namespace for per-agent rate limits.
const clientAgentRateLimitNamespace = "rateLimit:client_agent:v1"

// clientAgentRateLimitMark is the Redis key mark for per-agent rate limits.
const clientAgentRateLimitMark = "CA"

// ClientAgentRateLimit applies per-agent rate limits based on the User-Agent header.
// It must be placed after authentication middleware so that TokenAuth/TokenAuthReadOnly
// have already run; unauthenticated requests fall back to IP-based limiting.
func ClientAgentRateLimit() func(c *gin.Context) {
	limits := service.ParseClientAgentRateLimits(common.OptionMap["ClientAgentRateLimits"])
	if !limits["_global"].Enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	return func(c *gin.Context) {
		agent, _ := service.DetectClientAgent(c.Request.UserAgent())
		config := limits[agent]
		if !config.Enabled {
			config = limits["_global"]
		}
		if !config.Enabled || config.MaxRequests <= 0 || config.DurationSec <= 0 {
			c.Next()
			return
		}

		now := time.Now()
		windowStart := now.Unix() / config.DurationSec * config.DurationSec
		redisKey := fmt.Sprintf("%s:%s:%d", clientAgentRateLimitNamespace, agent, windowStart)

		// Try Redis first for cross-node coordination.
		if common.RDB != nil {
			allowed, _, ttlSeconds, err := redisFixedWindowTake(
				c.Request.Context(),
				redisKey,
				config.MaxRequests,
				config.DurationSec,
			)
			if err != nil {
				logger.LogError(c.Request.Context(), common.LogText("client agent rate limit check failed (agent=%s): %v", agent, err))
				c.Status(http.StatusInternalServerError)
				c.Abort()
				return
			}
			if !allowed {
				writeClientAgentRateLimited(c, config, ttlSeconds)
				return
			}
			c.Next()
			return
		}

		// Fall back to in-memory sliding window.
		key := agent
		if !clientAgentMemoryLimiter.Request(key, config.MaxRequests, config.DurationSec) {
			writeClientAgentRateLimited(c, config, config.DurationSec)
			return
		}
		c.Next()
	}
}

// writeClientAgentRateLimited rejects the request with 429 and a Retry-After hint.
func writeClientAgentRateLimited(c *gin.Context, config service.ClientAgentRateLimitConfig, retryAfterSeconds int64) {
	if retryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
	}
	c.Header("Content-Type", "application/json")
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"success": false,
		"message": i18n.T(c, i18n.MsgRateLimitReached, map[string]any{
			"Minutes": config.DurationSec / 60,
			"Max":     config.MaxRequests,
		}),
		"error": gin.H{
			"message": i18n.T(c, i18n.MsgRateLimitReached, map[string]any{
				"Minutes": config.DurationSec / 60,
				"Max":     config.MaxRequests,
			}),
			"type":  "rate_limit_error",
			"param": nil,
			"code":  "rate_limit_exceeded",
		},
	})
}

var clientAgentMemoryLimiter = common.NewInMemoryRateLimiter()
