// Package handler - domain 处理器
package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tempmail/middleware"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// DomainHandler 处理域名池相关的 HTTP 请求。
//
// 路由挂载（部分需要管理员权限）：
//   GET    /api/domains                    — List（列出所有域名）
//   GET    /api/domains/:id/status         — GetStatus（轮询 MX 验证进度）
//   POST   /api/domains/submit             — Submit（任意用户提交域名验证）
//   POST   /api/admin/domains              — Add（管理员直接添加）
//   DELETE /api/admin/domains/:id          — Delete
//   PUT    /api/admin/domains/:id/toggle   — Toggle（启用/停用）
//   POST   /api/admin/domains/mx-import   — MXImport（带检测的导入）
//   POST   /api/admin/domains/mx-register — MXRegister（提交等待自动验证）
//   GET    /api/admin/domains/:id/status  — GetStatus（管理员版，同非管理员版）
type DomainHandler struct {
	store        *store.Store
	cfgIP        string // 启动时从 SMTP_SERVER_IP 环境变量读取的备用 IP
	cfgHostname  string // 启动时从 SMTP_HOSTNAME 环境变量读取的备用主机名
}

// NewDomainHandler 构造 DomainHandler，注入 Store 和启动配置。
func NewDomainHandler(s *store.Store, smtpIP, smtpHostname string) *DomainHandler {
	return &DomainHandler{store: s, cfgIP: smtpIP, cfgHostname: smtpHostname}
}

// getServerIP 返回服务器公网 IP，优先读 DB 配置（可运行时修改），
// 其次 fallback 到启动时传入的环境变量值。
func (h *DomainHandler) getServerIP(ctx context.Context) string {
	if ip, err := h.store.GetSetting(ctx, "smtp_server_ip"); err == nil && ip != "" {
		return ip
	}
	return h.cfgIP
}

// getServerHostname 返回 MX 记录应指向的邮件服务器主机名。
// 优先级：DB 设置 smtp_hostname → 环境变量 SMTP_HOSTNAME → 空串
// 若为空串，则 DNS 配置提示中会建议用户创建 mail.<domain> 的 A 记录。
func (h *DomainHandler) getServerHostname(ctx context.Context) string {
	if hn, err := h.store.GetSetting(ctx, "smtp_hostname"); err == nil && hn != "" {
		return hn
	}
	return h.cfgHostname
}

// Add 直接将域名以 active 状态加入域名池（管理员，跳过 MX 验证）。
// POST /api/admin/domains
// 响应额外包含 dns_records 字段，提示管理员需要配置的 DNS 记录。
func (h *DomainHandler) Add(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	domain, err := h.store.AddDomain(c.Request.Context(), req.Domain)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "domain already exists: " + err.Error()})
		return
	}

	// 获取服务器 IP 和 hostname（来自 DB 设置或环境变量）
	serverIP := h.getServerIP(c.Request.Context())
	hostname := h.getServerHostname(c.Request.Context())

	// 构建 DNS 指引
	var dnsRecords []gin.H
	if hostname != "" {
		dnsRecords = []gin.H{
			{"type": "MX",  "host": "@", "value": hostname, "priority": 10, "description": "邮件交换记录，指向本服务器"},
			{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP), "description": "SPF 记录（可选）"},
		}
	} else {
		dnsRecords = []gin.H{
			{"type": "MX",  "host": "@", "value": fmt.Sprintf("mail.%s", req.Domain), "priority": 10, "description": "邮件交换记录"},
			{"type": "A",   "host": fmt.Sprintf("mail.%s", req.Domain), "value": serverIP, "description": "邮件服务器 A 记录"},
			{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP), "description": "SPF 记录（可选）"},
		}
	}

	// 返回 DNS 配置指引
	c.JSON(http.StatusCreated, gin.H{
		"domain":      domain,
		"dns_records": dnsRecords,
		"instructions": fmt.Sprintf(
			"请在域名 %s 的 DNS 管理面板中添加以上记录。添加后约 5-30 分钟生效。",
			req.Domain),
	})
}

// List 返回所有域名（含状态），供已认证用户查看域名池。
// GET /api/domains
// 包含 pending/disabled 状态的域名，前端可据此展示验证进度。
func (h *DomainHandler) List(c *gin.Context) {
	_ = middleware.GetAccount(c) // 确保已认证

	domains, err := h.store.ListDomains(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"domains": domains})
}

// Delete 从域名池中永久删除域名（管理员）。
// DELETE /api/admin/domains/:id
func (h *DomainHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain id"})
		return
	}

	if err := h.store.DeleteDomain(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "domain deleted"})
}

// Toggle 手动切换域名的启用/停用状态（管理员）。
// PUT /api/admin/domains/:id/toggle
// 请求体：{"active": true|false}
func (h *DomainHandler) Toggle(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain id"})
		return
	}

	var req struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.store.ToggleDomain(c.Request.Context(), id, req.Active); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "domain updated"})
}

// MXImport 先执行 DNS MX 检测，通过则导入（管理员）。
// POST /api/admin/domains/mx-import
// 请求体：{"domain": "example.com", "force": false}
//
// 流程：
//   - force=false：先做 MX 检测，通过则激活加入，未通过返回 422 + DNS 配置提示
//   - force=true：跳过 MX 检测，直接以 active 状态导入（适用于已确认 DNS 正确的情况）
func (h *DomainHandler) MXImport(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" binding:"required"`
		Force  bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))

	// 获取服务器 IP / hostname（来自 DB 设置或环境变量，不内置硬编码）
	serverIP := h.getServerIP(c.Request.Context())
	hostname := h.getServerHostname(c.Request.Context())

	// DNS MX 检测
	matched, mxHosts, mxStatus := store.CheckDomainMX(req.Domain, serverIP)

	if !matched && !req.Force {
		var dnsHint []gin.H
		if hostname != "" {
			dnsHint = []gin.H{
				{"type": "MX", "host": "@", "value": hostname, "priority": 10},
				{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
			}
		} else {
			mailSub := fmt.Sprintf("mail.%s", req.Domain)
			dnsHint = []gin.H{
				{"type": "MX", "host": "@", "value": mailSub, "priority": 10},
				{"type": "A", "host": mailSub, "value": serverIP},
				{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
			}
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":     "MX检测未通过，如确定要导入请加 force:true",
			"mx_status": mxStatus,
			"mx_hosts":  mxHosts,
			"server_ip": serverIP,
			"domain":    req.Domain,
			"dns_hint":  dnsHint,
		})
		return
	}

	// 导入到域名池
	domain, err := h.store.AddDomain(c.Request.Context(), req.Domain)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "域名已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"domain":     domain,
		"mx_status":  mxStatus,
		"mx_matched": matched,
		"message":    fmt.Sprintf("域名 %s 已导入域名池，Postfix 将在 60 秒内自动同步", req.Domain),
	})
}

// MXRegister 提交域名进入自动 MX 验证流程（无需手动确认）。
// POST /api/admin/domains/mx-register（管理员）
// POST /api/domains/submit（任意已登录用户，通过 Submit 代理调用）
//
// 流程：
//  1. 立即做一次 MX 检测
//     - 通过 → 直接以 active 状态激活，返回 201
//     - 未通过 → 写入 pending 状态，返回 202（Accepted）
//  2. pending 状态的域名由后台 goroutine 每 30 秒自动重检，通过后自动激活
//  3. 响应中包含 dns_required 字段，告知用户需要配置的 DNS 记录
func (h *DomainHandler) MXRegister(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))

	serverIP  := h.getServerIP(c.Request.Context())
	hostname  := h.getServerHostname(c.Request.Context())

	// MX 目标: 优先用服务器自己的 hostname，否则用用户域名的 mail 子域
	mxTarget := fmt.Sprintf("mail.%s", req.Domain)
	dnsRequired := []gin.H{
		{"type": "MX", "host": "@", "value": mxTarget, "priority": 10},
		{"type": "A",  "host": mxTarget, "value": serverIP},
		{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
	}
	if hostname != "" {
		mxTarget = hostname
		dnsRequired = []gin.H{
			{"type": "MX", "host": "@", "value": hostname, "priority": 10},
			{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
		}
	}

	// 先尝试立即检测；通过则直接激活
	matched, _, mxStatus := store.CheckDomainMX(req.Domain, serverIP)
	if matched {
		domain, err := h.store.AddDomain(c.Request.Context(), req.Domain)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
				// 已存在则直接返回
				domains, _ := h.store.ListDomains(c.Request.Context())
				for _, d := range domains {
					if d.Domain == req.Domain {
						c.JSON(http.StatusOK, gin.H{
							"domain":    d,
							"status":    d.Status,
							"mx_status": mxStatus,
							"message":   "域名已存在且处于激活状态",
						})
						return
					}
				}
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"domain":  domain,
			"status":  "active",
			"message": "MX验证通过，域名已立即加入域名池",
		})
		return
	}

	// MX未通过 → 加入 pending，等待后台自动轮询
	domain, err := h.store.AddDomainPending(c.Request.Context(), req.Domain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"domain":       domain,
		"status":       domain.Status,
		"server_ip":    serverIP,
		"mx_status":    mxStatus,
		"message":      fmt.Sprintf("域名 %s 已进入待验证队列，后台每30秒自动检测MX记录，通过后自动加入域名池", req.Domain),
		"dns_required": dnsRequired,
	})
}

// Submit 是 MXRegister 的别名，供任意已登录用户（非管理员）调用。
// POST /api/domains/submit
// 与 MXRegister 逻辑完全相同，仅路由权限不同（无需 AdminOnly 中间件）。
func (h *DomainHandler) Submit(c *gin.Context) {
	h.MXRegister(c) // 复用相同逻辑
}

// GetStatus 查询指定域名的当前状态，供前端轮询 MX 验证进度。
// GET /api/domains/:id/status（任意已登录用户）
// GET /api/admin/domains/:id/status（管理员）
// 返回 id / domain / status / is_active / mx_checked_at。
func (h *DomainHandler) GetStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain id"})
		return
	}

	domain, err := h.store.GetDomainByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":            domain.ID,
		"domain":        domain.Domain,
		"status":        domain.Status,
		"is_active":     domain.IsActive,
		"mx_checked_at": domain.MxCheckedAt,
	})
}

