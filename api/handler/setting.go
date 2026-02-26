// Package handler - setting 处理器
package handler

import (
	"net/http"

	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// SettingHandler 处理系统配置的读取和更新。
// 路由：
//   GET /public/settings       — 公开配置（无需认证）
//   GET /api/admin/settings    — 所有配置（管理员）
//   PUT /api/admin/settings    — 更新配置（管理员）
type SettingHandler struct {
	store *store.Store
}

// NewSettingHandler 构造 SettingHandler。
func NewSettingHandler(s *store.Store) *SettingHandler {
	return &SettingHandler{store: s}
}

// GetPublic 返回前端登录页和访客所需的公开配置，无需认证。
// GET /public/settings
// 返回字段：registration_open / site_title / smtp_server_ip / smtp_hostname / announcement
func (h *SettingHandler) GetPublic(c *gin.Context) {
	regOpen, err := h.store.GetSetting(c.Request.Context(), "registration_open")
	if err != nil {
		regOpen = "false"
	}
	siteTitle, _ := h.store.GetSetting(c.Request.Context(), "site_title")
	smtpIP, _    := h.store.GetSetting(c.Request.Context(), "smtp_server_ip")
	smtpHostname, _ := h.store.GetSetting(c.Request.Context(), "smtp_hostname")
	announce, _  := h.store.GetSetting(c.Request.Context(), "announcement")
	c.JSON(http.StatusOK, gin.H{
		"registration_open": regOpen == "true",
		"site_title":        siteTitle,
		"smtp_server_ip":    smtpIP,
		"smtp_hostname":     smtpHostname,
		"announcement":      announce,
	})
}

// AdminGetAll 返回所有配置项的键值对（管理员专用）。
// GET /api/admin/settings
// 响应为 map[string]string，包含所有 app_settings 记录。
func (h *SettingHandler) AdminGetAll(c *gin.Context) {
	settings, err := h.store.GetAllSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// AdminUpdate 批量更新配置项（管理员专用）。
// PUT /api/admin/settings
// 请求体：map[string]string，如 {"mailbox_ttl_minutes": "60"}
// 支持的配置键（白名单）：
//   registration_open / rate_limit_enabled / max_mailboxes_per_user /
//   smtp_server_ip / smtp_hostname / site_title / announcement /
//   default_domain / mailbox_ttl_minutes
// 不在白名单内的键直接返回 400，防止意外写入未知配置。
func (h *SettingHandler) AdminUpdate(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 白名单：已知配置项
	allowed := map[string]bool{
		"registration_open":      true,
		"rate_limit_enabled":     true,
		"max_mailboxes_per_user": true,
		"smtp_server_ip":         true,
		"smtp_hostname":          true,
		"site_title":             true,
		"announcement":           true,
		"default_domain":         true,
		"mailbox_ttl_minutes":    true,
	}

	for k, v := range req {
		if !allowed[k] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown setting key: " + k})
			return
		}
		if err := h.store.SetSetting(c.Request.Context(), k, v); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "settings updated"})
}
