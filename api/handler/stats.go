// Package handler - stats 处理器
package handler

import (
	"net/http"

	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// StatsHandler 处理统计数据查询请求。
// 路由：
//   GET /public/stats — 无需认证，供首页/访客展示
//   GET /api/stats    — 需认证，供已登录用户 Dashboard 展示
type StatsHandler struct {
	store *store.Store
}

// NewStatsHandler 构造 StatsHandler。
func NewStatsHandler(s *store.Store) *StatsHandler {
	return &StatsHandler{store: s}
}

// Get 返回全平台统计快照（邮箱数、邮件数、域名数、账户数等）。
// 通过单条多子查询 SQL 一次性获取所有统计项，避免多次数据库往返。
func (h *StatsHandler) Get(c *gin.Context) {
	stats, err := h.store.GetStats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}
