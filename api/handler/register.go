// Package handler - register 处理器
package handler

import (
	"net/http"

	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// RegisterHandler 处理公开注册请求。
// 路由：POST /public/register（无需认证）
type RegisterHandler struct {
	store *store.Store
}

// NewRegisterHandler 构造 RegisterHandler。
func NewRegisterHandler(s *store.Store) *RegisterHandler {
	return &RegisterHandler{store: s}
}

// Register 处理用户自助注册。
// POST /public/register
//
// 行为：
//  1. 检查 app_settings 中的 registration_open 开关，关闭时返回 403
//  2. 校验用户名（2~64 字符）
//  3. 创建账户并返回 API Key（仅此时返回，之后无法再次查看）
//
// 注意：此接口无需认证，需防止滥用（如机器人批量注册）。
// 生产环境建议在关闭注册后通过管理员接口手动创建账户。
func (h *RegisterHandler) Register(c *gin.Context) {
	// 检查注册开关
	regOpen, err := h.store.GetSetting(c.Request.Context(), "registration_open")
	if err != nil || regOpen != "true" {
		c.JSON(http.StatusForbidden, gin.H{"error": "registration is currently closed"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required,min=2,max=64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	account, err := h.store.CreateAccount(c.Request.Context(), req.Username)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":       account.ID,
		"username": account.Username,
		"api_key":  account.APIKey,
		"message":  "registration successful — save your API key, it won't be shown again",
	})
}
