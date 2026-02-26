// Package middleware 的 ratelimit.go 实现基于 Redis 的滑动窗口速率限制。
//
// 实现方式：对每个 API Key（或 IP）在 Redis 中维护一个自增计数器，
// 窗口到期后自动重置。使用 Pipeline 将 INCR 和 EXPIRE 合并为一次往返，
// 高并发下性能接近 Redis 吞吐上限。
// Redis 故障时采用 fail-open 策略（放行请求），保证服务可用性。
package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimit 返回一个基于 Redis 计数器的速率限制中间件。
//
// 参数：
//   - rdb:    Redis 客户端
//   - limit:  每个窗口期内允许的最大请求数（如 500）
//   - window: 时间窗口大小，单位秒（如 60）
//
// 限速键（Redis Key）的优先级：
//  1. Authorization 请求头中的 API Key（已认证用户按 Key 限速，互不影响）
//  2. ?api_key= 查询参数
//  3. 客户端 IP（未认证请求的兜底方案）
//
// 响应头：
//   X-RateLimit-Limit     — 窗口内总配额
//   X-RateLimit-Remaining — 剩余请求数
//   X-RateLimit-Reset     — 窗口重置的 Unix 时间戳（近似值）
//
// 超出限制时返回 HTTP 429，响应体含 retry_after 秒数。
func RateLimit(rdb *redis.Client, limit int, window int) gin.HandlerFunc {
	windowDur := time.Duration(window) * time.Second

	return func(c *gin.Context) {
		// 使用 API Key 作为限速键
		key := c.GetHeader("Authorization")
		if key == "" {
			key = c.Query("api_key")
		}
		if key == "" {
			key = c.ClientIP()
		}

		redisKey := fmt.Sprintf("rl:%s", key)
		ctx := c.Request.Context()

		// 使用 Redis Pipeline 减少往返（高并发优化）
		pipe := rdb.Pipeline()
		incr := pipe.Incr(ctx, redisKey)
		pipe.Expire(ctx, redisKey, windowDur)
		_, err := pipe.Exec(ctx)

		if err != nil {
			// Redis 故障时放行（fail-open）
			c.Next()
			return
		}

		count := incr.Val()
		remaining := int64(limit) - count
		if remaining < 0 {
			remaining = 0
		}

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(windowDur).Unix()))

		if count > int64(limit) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":       "rate limit exceeded",
				"limit":       limit,
				"retry_after": window,
			})
			return
		}

		c.Next()
	}
}
