// Package model 定义 TempMail 服务所有数据模型和 HTTP 请求/响应结构体。
//
// 数据模型与数据库表的对应关系：
//   Account  → accounts 表
//   Domain   → domains 表（status: active|pending|disabled）
//   Mailbox  → mailboxes 表（含 TTL 字段 expires_at）
//   Email    → emails 表
//   Stats    → 多表聚合查询结果（仅读，无对应表）
package model

import (
	"time"

	"github.com/google/uuid"
)

// ==================== 数据模型 ====================

// Account 对应 accounts 表，代表一个服务用户。
// API Key 以 "tm_" 前缀 + 48 位十六进制字符构成，使用 crypto/rand 生成。
type Account struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	APIKey    string    `json:"api_key"`
	IsAdmin   bool      `json:"is_admin"`  // true 表示管理员，可访问 /api/admin/* 路由
	IsActive  bool      `json:"is_active"` // false 时该账户无法通过认证
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Domain 对应 domains 表，代表一个可接收邮件的域名。
// Status 状态机：pending → active（MX 验证通过）→ disabled（MX 失效或手动停用）。
type Domain struct {
	ID           int        `json:"id"`
	Domain       string     `json:"domain"`
	IsActive     bool       `json:"is_active"`                // true 表示此域名可用于创建邮箱
	Status       string     `json:"status"`                   // active | pending | disabled
	CreatedAt    time.Time  `json:"created_at"`
	MxCheckedAt  *time.Time `json:"mx_checked_at,omitempty"`  // 最后一次 DNS MX 检测时间，nil 表示从未检测
}

// Stats 是 /api/stats 接口返回的全平台统计快照，由多个子查询聚合而来。
type Stats struct {
	TotalMailboxes  int `json:"total_mailboxes"`  // 历史总邮箱数
	ActiveMailboxes int `json:"active_mailboxes"` // expires_at > NOW() 的有效邮箱数
	TotalEmails     int `json:"total_emails"`     // 全平台累计收到邮件数
	ActiveDomains   int `json:"active_domains"`   // is_active=true 的域名数
	PendingDomains  int `json:"pending_domains"`  // status='pending' 等待 MX 验证的域名数
	TotalAccounts   int `json:"total_accounts"`   // is_active=true 的账户数
}

// Mailbox 对应 mailboxes 表，代表一个临时收件箱。
// FullAddress = Address + "@" + Domain，全局唯一（数据库唯一索引保证）。
type Mailbox struct {
	ID          uuid.UUID `json:"id"`
	AccountID   uuid.UUID `json:"account_id"`  // 所属账户
	Address     string    `json:"address"`     // 邮箱本地部分（@ 之前），如 "abcd1234ef"
	DomainID    int       `json:"domain_id"`   // 关联的 domains.id
	FullAddress string    `json:"full_address"` // 完整邮件地址，如 "abcd1234ef@example.com"
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`  // 过期时间，到期后由清理 goroutine 自动删除
}

// Email 对应 emails 表，代表一封收到的邮件。
// RawMessage 存储原始 MIME 内容（body text/html 由 mail-receiver.py 预解析）。
// SizeBytes = len(raw_message)，在写入时自动计算。
type Email struct {
	ID         uuid.UUID `json:"id"`
	MailboxID  uuid.UUID `json:"mailbox_id"`
	Sender     string    `json:"sender"`
	Subject    string    `json:"subject"`
	BodyText   string    `json:"body_text"`              // 纯文本正文
	BodyHTML   string    `json:"body_html"`              // HTML 正文
	RawMessage string    `json:"raw_message,omitempty"` // 原始 MIME（列表接口不返回，节省带宽）
	SizeBytes  int       `json:"size_bytes"`
	ReceivedAt time.Time `json:"received_at"`
}

// ==================== 请求/响应 ====================

// CreateAccountReq 是 POST /api/admin/accounts 的请求体。
type CreateAccountReq struct {
	Username string `json:"username" binding:"required,min=2,max=64"`
}

// CreateAccountResp 是创建账户成功后返回的结构体。
// APIKey 只在此处返回一次，后续无法通过 API 再次获取明文。
type CreateAccountResp struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
	APIKey   string    `json:"api_key"`
}

// AddDomainReq 是 POST /api/admin/domains 的请求体。
// 使用 fqdn 验证标签确保是合法的完全限定域名。
type AddDomainReq struct {
	Domain string `json:"domain" binding:"required,fqdn"`
}

// DNSInstruction 描述一条需要在域名 DNS 管理面板中配置的记录。
type DNSInstruction struct {
	Type     string `json:"type"`              // 记录类型：MX / A / TXT
	Host     string `json:"host"`              // 主机名，@ 表示根域
	Value    string `json:"value"`             // 记录内容
	Priority int    `json:"priority,omitempty"` // MX 优先级（非 MX 记录时为 0，不输出）
}

// AddDomainResp 是添加域名成功后的响应，包含 DNS 配置指引。
type AddDomainResp struct {
	Domain       Domain           `json:"domain"`
	DNSRecords   []DNSInstruction `json:"dns_records"`  // 需要在 DNS 控制台配置的记录列表
	Instructions string           `json:"instructions"` // 人类可读的操作说明
}

// CreateMailboxReq 是 POST /api/mailboxes 的请求体。
//
// 字段说明：
//   - Address：可选，邮箱本地部分（@ 前），为空则随机生成 10 位字母数字字符串。
//   - Domain：可选，指定使用的域名（如 "example.com"）。
//     必须是当前已激活（is_active=true）的域名，否则返回 400/404。
//     为空时从活跃域名池随机选取一个。
//
// 示例请求体：
//
//	{}                                        → 随机地址 + 随机域名
//	{"address":"mybox"}                       → mybox@<随机域名>
//	{"domain":"example.com"}                  → <随机地址>@example.com
//	{"address":"mybox","domain":"example.com"} → mybox@example.com
type CreateMailboxReq struct {
	Address string `json:"address,omitempty"` // 可选，为空则随机生成
	Domain  string `json:"domain,omitempty"`  // 可选，指定域名；为空则随机选取活跃域名
}

// CreateMailboxResp 是创建邮箱成功后的响应。
type CreateMailboxResp struct {
	Mailbox Mailbox `json:"mailbox"`
}

// ListResp 是通用分页列表响应，使用泛型支持任意元素类型。
// 用于 /api/mailboxes、/api/admin/accounts 等分页接口。
type ListResp[T any] struct {
	Data  []T `json:"data"`  // 当前页数据
	Total int `json:"total"` // 总记录数（用于前端计算总页数）
	Page  int `json:"page"`  // 当前页码（从 1 开始）
	Size  int `json:"size"`  // 每页大小
}

// EmailSummary 是邮件列表接口返回的摘要信息，不包含正文（节省带宽）。
// 前端读取邮件列表时使用此结构，点击具体邮件后再调用 GetEmail 获取全文。
type EmailSummary struct {
	ID         uuid.UUID `json:"id"`
	Sender     string    `json:"sender"`
	Subject    string    `json:"subject"`
	SizeBytes  int       `json:"size_bytes"`
	ReceivedAt time.Time `json:"received_at"`
}
