// Package middleware 提供 Gin 中间件：API Key 认证和基于 Redis 的速率限制。
package middleware

import (
	"net/http"
	"strings"

	"tempmail/model"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// AccountKey 是在 Gin 上下文中存储当前账户对象的键名。
// handler 层通过 GetAccount(c) 读取，无需重复查询数据库。
const AccountKey = "account"

// Auth 是 API Key 认证中间件，适用于所有 /api/* 路由。
//
// Key 的获取优先级：
//  1. Authorization 请求头（支持 "Bearer <key>" 格式）
//  2. ?api_key= 查询参数（方便调试/脚本调用）
//
// 验证通过后，将 *model.Account 存入 Gin 上下文（键名 AccountKey），
// 供后续 handler 通过 GetAccount(c) 读取，避免重复数据库查询。
func Auth(s *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("Authorization")
		if apiKey == "" {
			apiKey = c.Query("api_key")
		}

		// 支持 Bearer token 格式
		apiKey = strings.TrimPrefix(apiKey, "Bearer ")
		apiKey = strings.TrimSpace(apiKey)

		if apiKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing api_key: use Authorization header or ?api_key= query param",
			})
			return
		}

		account, err := s.GetAccountByAPIKey(c.Request.Context(), apiKey)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid api_key",
			})
			return
		}

		c.Set(AccountKey, account)
		c.Next()
	}
}

// AdminOnly 是管理员权限中间件，必须在 Auth 中间件之后挂载。
// 检查 Gin 上下文中的账户是否具有 is_admin=true 权限，
// 否则返回 403 Forbidden。
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		account := GetAccount(c)
		if account == nil || !account.IsAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "admin access required",
			})
			return
		}
		c.Next()
	}
}

// GetAccount 从 Gin 上下文中取出当前已认证账户。
// 若上下文中无账户（未经过 Auth 中间件），返回 nil。
// handler 层调用时无需处理 nil（被 Auth 中间件保证了已登录状态）。
func GetAccount(c *gin.Context) *model.Account {
	val, exists := c.Get(AccountKey)
	if !exists {
		return nil
	}
	a, ok := val.(*model.Account)
	if !ok {
		return nil
	}
	return a
}
