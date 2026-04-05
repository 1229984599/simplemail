package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tempmail/cf"
	"tempmail/middleware"
	"tempmail/model"
	"tempmail/store"

	"github.com/gin-gonic/gin"
)

type DomainHandler struct {
	store *store.Store
	cfgIP string // SMTP_SERVER_IP env
}

func NewDomainHandler(s *store.Store, smtpIP string) *DomainHandler {
	return &DomainHandler{store: s, cfgIP: smtpIP}
}

func (h *DomainHandler) getServerIP(ctx context.Context) string {
	if ip, err := h.store.GetSetting(ctx, "smtp_server_ip"); err == nil && ip != "" {
		return ip
	}
	return h.cfgIP
}

func (h *DomainHandler) GetServerIP() string {
	return h.getServerIP(context.Background())
}

func (h *DomainHandler) UpdateConfig(serverIP string) {
	h.cfgIP = serverIP
}

func buildDNSRecords(domain, hostname, serverIP string) []gin.H {
	if hostname != "" {
		return []gin.H{
			{"type": "MX", "host": "@", "value": hostname, "priority": 10},
			{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
		}
	}
	mailSub := fmt.Sprintf("mail.%s", domain)
	return []gin.H{
		{"type": "MX", "host": "@", "value": mailSub, "priority": 10},
		{"type": "A", "host": mailSub, "value": serverIP},
		{"type": "TXT", "host": "@", "value": fmt.Sprintf("v=spf1 ip4:%s ~all", serverIP)},
	}
}

// POST /api/admin/domains - 添加域名到池（管理员）
func (h *DomainHandler) Add(c *gin.Context) {
	var req struct {
		Domain   string `json:"domain" binding:"required"`
		Hostname string `json:"hostname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	domain, err := h.store.AddDomain(c.Request.Context(), req.Domain, req.Hostname)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "domain already exists: " + err.Error()})
		return
	}

	serverIP := h.getServerIP(c.Request.Context())
	dnsRecords := buildDNSRecords(req.Domain, req.Hostname, serverIP)

	// 返回 DNS 配置指引
	c.JSON(http.StatusCreated, gin.H{
		"domain":      domain,
		"dns_records": dnsRecords,
		"instructions": fmt.Sprintf(
			"请在域名 %s 的 DNS 管理面板中添加以上记录。添加后约 5-30 分钟生效。",
			req.Domain),
	})
}

// GET /api/domains - 列出所有域名（共享域名池）
func (h *DomainHandler) List(c *gin.Context) {
	_ = middleware.GetAccount(c) // 确保已认证

	domains, err := h.store.ListDomains(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"domains": domains})
}

// DELETE /api/admin/domains/:id - 删除域名（管理员）
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

// PUT /api/admin/domains/:id/toggle - 启用/禁用域名（管理员）
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

// POST /api/admin/domains/mx-import - MX快捷接入（DNS检测并自动导入）
// body: {"domain":"example.com", "hostname":"mail.xxx.yyy", "force":false}
func (h *DomainHandler) MXImport(c *gin.Context) {
	var req struct {
		Domain   string `json:"domain" binding:"required"`
		Hostname string `json:"hostname"`
		Force    bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))

	serverIP := h.getServerIP(c.Request.Context())
	hostname := req.Hostname

	// DNS MX 检测
	matched, mxHosts, mxStatus := store.CheckDomainMX(req.Domain, serverIP)

	if !matched && !req.Force {
		dnsHint := buildDNSRecords(req.Domain, hostname, serverIP)
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
	domain, err := h.store.AddDomain(c.Request.Context(), req.Domain, req.Hostname)
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

// POST /api/admin/domains/mx-register - 提交域名等待自动MX验证（无需手动确认）
// body: {"domain":"example.com", "hostname":"mail.xxx.yyy"}
func (h *DomainHandler) MXRegister(c *gin.Context) {
	var req struct {
		Domain   string `json:"domain" binding:"required"`
		Hostname string `json:"hostname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))

	serverIP := h.getServerIP(c.Request.Context())
	dnsRequired := buildDNSRecords(req.Domain, req.Hostname, serverIP)

	// 先尝试立即检测；通过则直接激活
	matched, _, mxStatus := store.CheckDomainMX(req.Domain, serverIP)
	if matched {
		domain, err := h.store.AddDomain(c.Request.Context(), req.Domain, req.Hostname)
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
	domain, err := h.store.AddDomainPending(c.Request.Context(), req.Domain, req.Hostname)
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

// POST /api/domains/submit — 任意已登录用户提交域名进行 MX 自动验证
// 与 MXRegister 逻辑相同，但不需要管理员权限
func (h *DomainHandler) Submit(c *gin.Context) {
	h.MXRegister(c) // 复用相同逻辑
}

// GET /api/admin/domains/:id/status - 查询域名状态（用于前端轮询）
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
		"hostname":      domain.Hostname,
		"mx_checked_at": domain.MxCheckedAt,
	})
}

// PUT /api/admin/domains/:id/hostname — 更新域名的 MX 目标主机名
func (h *DomainHandler) UpdateHostname(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain id"})
		return
	}

	var req struct {
		Hostname string `json:"hostname" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.store.UpdateDomainHostname(c.Request.Context(), id, strings.TrimSpace(req.Hostname)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "hostname updated"})
}

// POST /api/admin/domains/cf-create — 通过 Cloudflare API 自动创建子域名 MX 解析并加入域名池
//
// 请求示例: {"domain":"vet.nightunderfly.online", "hostname":"mail.xxx.yyy"}
//
// 完整流程:
//  1. 校验系统设置中已配置 cf_api_token（需要 Zone:DNS:Edit 权限的 CF API Token）
//  2. 校验域名格式：至少包含两段（子域名.主域名，如 vet.nightunderfly.online）
//  3. 使用请求中的 hostname 作为 MX 记录的目标值
//  4. 调用 CF API 根据"主域名"部分查找对应的 Zone ID
//  5. 调用 CF API 在该 Zone 下创建 MX 记录（subdomain → hostname）
//  6. 将域名以 pending 状态写入本地域名池，等待后台 MX 验证通过后自动激活
//
// 前置条件:
//   - 系统设置中已配置 cf_api_token（Cloudflare API Token，需 Zone:DNS:Edit 权限）
//   - 输入的域名必须使用 Cloudflare 托管的 DNS
//
// 错误码:
//
//	400 — CF Token 未配置 / 域名格式不合法 / hostname 未提供 / Zone 未找到
//	409 — 域名已存在于本地域名池
//	502 — CF API 创建 DNS 记录失败
func (h *DomainHandler) CFCreate(c *gin.Context) {
	var req struct {
		Domain   string `json:"domain" binding:"required"`
		Hostname string `json:"hostname"`
		Zone     string `json:"zone"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))

	// 检查 CF API Token 是否已在系统设置中配置
	cfToken, err := h.store.GetSetting(c.Request.Context(), "cf_api_token")
	if err != nil || cfToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "未配置 Cloudflare API Token，请在系统设置中添加 cf_api_token（需要 DNS 编辑权限）",
		})
		return
	}

	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "请提供 hostname（MX 记录目标，如 mail.xxx.yyy）",
		})
		return
	}

	var zoneName string
	if req.Zone = strings.TrimSpace(req.Zone); req.Zone != "" {
		zoneName = req.Zone
	} else {
		var err error
		zoneName, err = cf.ExtractBaseDomain(req.Domain)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "域名格式不合法，至少需要子域名.主域名（如 sub.example.com）: " + err.Error(),
				"domain": req.Domain,
			})
			return
		}
	}

	client := cf.NewClient(cfToken)
	zone, err := client.FindZoneByName(zoneName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "查找 Cloudflare Zone 失败: " + err.Error(),
			"domain": req.Domain,
			"zone":   zoneName,
		})
		return
	}

	subdomain := strings.TrimSuffix(req.Domain, "."+zone.Name)

	var created *cf.DNSRecord
	existing, findErr := client.FindMXRecord(zone.ID, subdomain, zone.Name, hostname)
	if findErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":     "查询 Cloudflare DNS 记录失败: " + findErr.Error(),
			"zone":      zone.Name,
			"subdomain": subdomain,
		})
		return
	}

	skippedCF := existing != nil
	if skippedCF {
		created = existing
	} else {
		var createErr error
		created, createErr = client.CreateMXRecord(zone.ID, subdomain, hostname)
		if createErr != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error":     "创建 Cloudflare DNS 记录失败: " + createErr.Error(),
				"zone":      zone.Name,
				"subdomain": subdomain,
				"mx_target": hostname,
			})
			return
		}
	}

	var domain *model.Domain
	if skippedCF {
		domain, err = h.store.AddDomain(c.Request.Context(), req.Domain, req.Hostname)
	} else {
		domain, err = h.store.AddDomainPending(c.Request.Context(), req.Domain, req.Hostname)
	}
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "域名已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if skippedCF {
		c.JSON(http.StatusCreated, gin.H{
			"domain":    domain,
			"cf_record": created,
			"zone":      zone.Name,
			"mx_target": hostname,
			"message": fmt.Sprintf(
				"Cloudflare Zone %s 中已存在 %s 的 MX 记录（→ %s），域名已直接激活",
				zone.Name, req.Domain, hostname,
			),
		})
	} else {
		c.JSON(http.StatusCreated, gin.H{
			"domain":    domain,
			"cf_record": created,
			"zone":      zone.Name,
			"mx_target": hostname,
			"message": fmt.Sprintf(
				"已在 Cloudflare Zone %s 中为 %s 创建 MX 记录（→ %s），域名已加入验证队列",
				zone.Name, req.Domain, hostname,
			),
		})
	}
}

// DELETE /api/admin/domains/:id/cf — 通过 Cloudflare API 删除 MX 记录并从本地域名池移除
func (h *DomainHandler) CFDelete(c *gin.Context) {
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

	cfToken, err := h.store.GetSetting(c.Request.Context(), "cf_api_token")
	if err != nil || cfToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Cloudflare API Token，请在系统设置中添加 cf_api_token"})
		return
	}

	hostname := domain.Hostname

	zoneName, err := cf.ExtractBaseDomain(domain.Domain)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "域名格式不合法: " + err.Error(), "domain": domain.Domain})
		return
	}

	client := cf.NewClient(cfToken)
	zone, err := client.FindZoneByName(zoneName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "查找 Cloudflare Zone 失败: " + err.Error(), "domain": domain.Domain})
		return
	}

	subdomain := strings.TrimSuffix(domain.Domain, "."+zone.Name)
	record, err := client.FindMXRecord(zone.ID, subdomain, zone.Name, hostname)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查找 MX 记录失败: " + err.Error(), "zone": zone.Name, "subdomain": subdomain})
		return
	}

	deletedCF := false
	if record != nil {
		if err := client.DeleteDNSRecord(zone.ID, record.ID); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "删除 Cloudflare DNS 记录失败: " + err.Error(), "record_id": record.ID})
			return
		}
		deletedCF = true
	}

	if err := h.store.DeleteDomain(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除本地域名失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           "域名已删除",
		"domain":            domain.Domain,
		"zone":              zone.Name,
		"cf_record_deleted": deletedCF,
	})
}

// PUT /api/admin/domains/batch/toggle — 批量启用/禁用域名
func (h *DomainHandler) BatchToggle(c *gin.Context) {
	var req struct {
		IDs    []int `json:"ids" binding:"required"`
		Active bool  `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated := 0
	for _, id := range req.IDs {
		if err := h.store.ToggleDomain(c.Request.Context(), id, req.Active); err == nil {
			updated++
		}
	}

	c.JSON(http.StatusOK, gin.H{"updated": updated, "total": len(req.IDs)})
}

// PUT /api/admin/domains/batch/delete — 批量删除域名（仅本地）
func (h *DomainHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deleted := 0
	for _, id := range req.IDs {
		if err := h.store.DeleteDomain(c.Request.Context(), id); err == nil {
			deleted++
		}
	}

	c.JSON(http.StatusOK, gin.H{"deleted": deleted, "total": len(req.IDs)})
}

// PUT /api/admin/domains/batch/cf-delete — 批量通过 CF API 删除 MX 记录并移除域名
func (h *DomainHandler) BatchCFDelete(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfToken, err := h.store.GetSetting(c.Request.Context(), "cf_api_token")
	if err != nil || cfToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Cloudflare API Token"})
		return
	}

	client := cf.NewClient(cfToken)

	type batchResult struct {
		ID     int    `json:"id"`
		Domain string `json:"domain"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}

	var results []batchResult
	for _, id := range req.IDs {
		domain, err := h.store.GetDomainByID(c.Request.Context(), id)
		if err != nil {
			results = append(results, batchResult{ID: id, Status: "error", Error: "domain not found"})
			continue
		}

		zoneName, zoneErr := cf.ExtractBaseDomain(domain.Domain)
		if zoneErr != nil {
			_ = h.store.DeleteDomain(c.Request.Context(), id)
			results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "deleted_local", Error: "invalid domain: " + zoneErr.Error()})
			continue
		}

		zone, zoneErr := client.FindZoneByName(zoneName)
		if zoneErr != nil {
			_ = h.store.DeleteDomain(c.Request.Context(), id)
			results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "deleted_local", Error: "zone not found: " + zoneErr.Error()})
			continue
		}

		subdomain := strings.TrimSuffix(domain.Domain, "."+zone.Name)
		record, findErr := client.FindMXRecord(zone.ID, subdomain, zone.Name, domain.Hostname)
		if findErr != nil {
			_ = h.store.DeleteDomain(c.Request.Context(), id)
			results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "deleted_local", Error: "find MX failed: " + findErr.Error()})
			continue
		}

		if record != nil {
			if delErr := client.DeleteDNSRecord(zone.ID, record.ID); delErr != nil {
				results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "error", Error: "delete CF record failed: " + delErr.Error()})
				continue
			}
		}

		if err := h.store.DeleteDomain(c.Request.Context(), id); err != nil {
			results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "error", Error: "delete local failed: " + err.Error()})
			continue
		}

		results = append(results, batchResult{ID: id, Domain: domain.Domain, Status: "success"})
	}

	c.JSON(http.StatusOK, gin.H{"results": results, "total": len(req.IDs)})
}
