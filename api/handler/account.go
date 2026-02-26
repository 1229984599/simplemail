// Package handler 实现所有 HTTP 请求处理器（Controller 层）。
//
// 每个 Handler 结构体通过依赖注入持有 *store.Store，
// 负责：请求参数绑定与校验 → 调用 store 层 → 构造 JSON 响应。
// 认证和速率限制已在中间件层完成，handler 层无需重复处理。
package handler

import (
	"net/http"
	"strconv"

	"tempmail/middleware"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// AccountHandler 处理账户相关的 HTTP 请求。
// 路由挂载：
//   POST   /api/admin/accounts       — Create（管理员创建账户）
//   GET    /api/admin/accounts       — List（管理员列出所有账户）
//   DELETE /api/admin/accounts/:id   — Delete（管理员删除账户）
//   GET    /api/me                   — Me（查看当前登录账户信息）
type AccountHandler struct {
	store *store.Store
}

// NewAccountHandler 构造 AccountHandler 并注入 Store 依赖。
func NewAccountHandler(s *store.Store) *AccountHandler {
	return &AccountHandler{store: s}
}

// Create 创建新账户并返回 API Key（管理员接口）。
// POST /api/admin/accounts
// 请求体：{"username": "foo"}（2~64 字符）
// 响应：{"id", "username", "api_key"}
// API Key 仅在此处返回一次，请妥善保存。
func (h *AccountHandler) Create(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required,min=2,max=64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account, err := h.store.CreateAccount(c.Request.Context(), req.Username)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "username already exists or db error: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":       account.ID,
		"username": account.Username,
		"api_key":  account.APIKey,
	})
}

// List 返回分页账户列表（管理员接口）。
// GET /api/admin/accounts?page=1&size=20
// 响应包含 data/total/page/size 四个字段。
func (h *AccountHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 { page = 1 }
	if size < 1 || size > 100 { size = 20 }

	accounts, total, err := h.store.ListAccounts(c.Request.Context(), page, size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  accounts,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// Delete 按 UUID 删除指定账户（管理员接口）。
// DELETE /api/admin/accounts/:id
// 注意：删除账户会级联删除其所有邮箱和邮件。
// 不能删除自己或其他管理员（此约束由前端实现，后端不检查）。
func (h *AccountHandler) Delete(c *gin.Context) {
	id, err := parseUUID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account id"})
		return
	}

	if err := h.store.DeleteAccount(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "account deleted"})
}

// Me 返回当前已认证账户的基本信息（不含 API Key）。
// GET /api/me
// 账户信息已由 Auth 中间件注入上下文，此处直接读取，无需额外查询。
func (h *AccountHandler) Me(c *gin.Context) {
	account := middleware.GetAccount(c)
	c.JSON(http.StatusOK, gin.H{
		"id":         account.ID,
		"username":   account.Username,
		"is_admin":   account.IsAdmin,
		"created_at": account.CreatedAt,
	})
}
