// Package handler - mailbox 处理器
package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tempmail/middleware"
	"tempmail/model"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// MailboxHandler 处理临时邮箱的创建、列表和删除。
// 路由（均需认证）：
//   POST   /api/mailboxes      — Create（创建临时邮箱）
//   GET    /api/mailboxes      — List（列出当前用户的邮箱）
//   DELETE /api/mailboxes/:id  — Delete（删除邮箱及其所有邮件）
type MailboxHandler struct {
	store *store.Store
}

// NewMailboxHandler 构造 MailboxHandler。
func NewMailboxHandler(s *store.Store) *MailboxHandler {
	return &MailboxHandler{store: s}
}

// Create 创建一个新的临时邮箱。
// POST /api/mailboxes
//
// 请求体（所有字段均为可选）：
//
//	{}
//	  → 随机地址 + 随机域名
//	{"address": "mybox"}
//	  → mybox@<随机活跃域名>
//	{"domain": "example.com"}
//	  → <随机地址>@example.com（domain 必须是已激活域名）
//	{"address": "mybox", "domain": "example.com"}
//	  → mybox@example.com
//
// 流程：
//  1. 若 address 为空，调用 GenerateRandomAddress() 生成 10 位随机串
//  2. 从 app_settings 读取 mailbox_ttl_minutes（默认 30 分钟）
//  3. 若请求中指定了 domain：查询该域名是否存在且处于活跃状态（is_active=true）
//     若域名不存在或未激活，返回 400
//  4. 若未指定 domain：从活跃域名池随机选择一个（ORDER BY RANDOM()）
//  5. 组合 address@domain，写入数据库（全局唯一约束）
//
// 若地址冲突（极小概率），返回 409，前端可重试。
func (h *MailboxHandler) Create(c *gin.Context) {
	account := middleware.GetAccount(c)

	var req struct {
		Address string `json:"address"` // 可选，为空则随机生成
		Domain  string `json:"domain"`  // 可选，指定域名；为空则从活跃域名池随机选取
	}
	c.ShouldBindJSON(&req)

	// 生成邮箱地址（本地部分）
	address := strings.TrimSpace(req.Address)
	if address == "" {
		address = store.GenerateRandomAddress()
	}
	address = strings.ToLower(address)

	// 读取 TTL 设置
	ttlMinutes := 30
	if ttlStr, err := h.store.GetSetting(c.Request.Context(), "mailbox_ttl_minutes"); err == nil {
		if n, err := strconv.Atoi(ttlStr); err == nil && n > 0 {
			ttlMinutes = n
		}
	}

	// 确定使用的域名：指定域名 or 随机活跃域名
	var domain *model.Domain
	if domainName := strings.TrimSpace(strings.ToLower(req.Domain)); domainName != "" {
		// 用户指定了域名，验证其是否存在且活跃
		d, err := h.store.GetDomainByName(c.Request.Context(), domainName)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "domain not found or not active: " + domainName})
			return
		}
		domain = d
	} else {
		// 未指定域名，随机选取一个活跃域名
		d, err := h.store.GetRandomActiveDomain(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no active domains available"})
			return
		}
		domain = d
	}

	fullAddress := fmt.Sprintf("%s@%s", address, domain.Domain)

	mailbox, err := h.store.CreateMailbox(c.Request.Context(), account.ID, address, domain.ID, fullAddress, ttlMinutes)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "address already taken, try again"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"mailbox": mailbox})
}

// List 返回当前认证用户的分页邮箱列表（含已过期邮箱）。
// GET /api/mailboxes?page=1&size=20
func (h *MailboxHandler) List(c *gin.Context) {
	account := middleware.GetAccount(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 { page = 1 }
	if size < 1 || size > 100 { size = 20 }

	mailboxes, total, err := h.store.ListMailboxes(c.Request.Context(), account.ID, page, size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  mailboxes,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// Delete 删除指定邮箱及其所有邮件（通过 ON DELETE CASCADE 实现）。
// DELETE /api/mailboxes/:id
// 只能删除属于当前用户的邮箱（store 层验证 accountID）。
func (h *MailboxHandler) Delete(c *gin.Context) {
	account := middleware.GetAccount(c)
	id, err := parseUUID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mailbox id"})
		return
	}

	if err := h.store.DeleteMailbox(c.Request.Context(), id, account.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "mailbox deleted"})
}
