// Package handler - email 处理器
package handler

import (
	"net/http"
	"strconv"

	"tempmail/middleware"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

// EmailHandler 处理邮件的读取和删除请求。
// 路由（均需认证，URL 嵌套在邮箱 ID 下）：
//   GET    /api/mailboxes/:id/emails             — List（邮件摘要列表，不含正文）
//   GET    /api/mailboxes/:id/emails/:email_id   — Get（邮件完整内容含 HTML）
//   DELETE /api/mailboxes/:id/emails/:email_id   — Delete（删除单封邮件）
//
// 所有操作均先通过 GetMailbox(mailboxID, accountID) 验证邮箱归属，
// 确保用户只能操作属于自己的邮箱中的邮件（双重 ID 绑定防越权）。
type EmailHandler struct {
	store *store.Store
}

// NewEmailHandler 构造 EmailHandler。
func NewEmailHandler(s *store.Store) *EmailHandler {
	return &EmailHandler{store: s}
}

// List 返回邮箱中的邮件摘要列表（不含正文，节省带宽）。
// GET /api/mailboxes/:id/emails?page=1&size=20
// 按收件时间倒序排列，最新邮件在最前。
func (h *EmailHandler) List(c *gin.Context) {
	account := middleware.GetAccount(c)
	mailboxID, err := parseUUID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mailbox id"})
		return
	}

	// 验证邮箱归属
	_, err = h.store.GetMailbox(c.Request.Context(), mailboxID, account.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 { page = 1 }
	if size < 1 || size > 100 { size = 20 }

	emails, total, err := h.store.ListEmails(c.Request.Context(), mailboxID, page, size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  emails,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// Get 返回单封邮件的完整内容（含 body_text / body_html / raw_message）。
// GET /api/mailboxes/:id/emails/:email_id
func (h *EmailHandler) Get(c *gin.Context) {
	account := middleware.GetAccount(c)
	mailboxID, err := parseUUID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mailbox id"})
		return
	}

	// 验证邮箱归属
	_, err = h.store.GetMailbox(c.Request.Context(), mailboxID, account.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}

	emailID, err := parseUUID(c.Param("email_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email id"})
		return
	}

	email, err := h.store.GetEmail(c.Request.Context(), emailID, mailboxID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"email": email})
}

// Delete 删除单封邮件。
// DELETE /api/mailboxes/:id/emails/:email_id
func (h *EmailHandler) Delete(c *gin.Context) {
	account := middleware.GetAccount(c)
	mailboxID, err := parseUUID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mailbox id"})
		return
	}

	// 验证邮箱归属
	_, err = h.store.GetMailbox(c.Request.Context(), mailboxID, account.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mailbox not found"})
		return
	}

	emailID, err := parseUUID(c.Param("email_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email id"})
		return
	}

	if err := h.store.DeleteEmail(c.Request.Context(), emailID, mailboxID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "email not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "email deleted"})
}
