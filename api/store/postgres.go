// Package store 封装所有数据库操作（PostgreSQL）和 DNS 工具函数。
//
// 使用 pgx/v5 连接池（pgxpool），最大 500 个连接。在生产环境中，
// 这些连接由前置的 PgBouncer（transaction 模式）进一步复用，
// 因此实际到达 PostgreSQL 的并发连接数远小于 500。
//
// 函数分组：
//   Account   — 账户 CRUD
//   Domain    — 域名池管理（含 MX 验证状态机）
//   Mailbox   — 临时邮箱 CRUD
//   Email     — 邮件存取删
//   Helpers   — generateAPIKey、GenerateRandomAddress
//   CheckDomainMX — 独立 DNS 检测函数（供 handler 和后台 goroutine 调用）
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"tempmail/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 是数据库访问层的核心结构体，持有 pgxpool 连接池。
// 所有 DB 操作均通过方法挂载在此结构体上，便于依赖注入和测试替换。
type Store struct {
	pool *pgxpool.Pool
}

// New 初始化连接池并返回 Store。
// 连接池参数：最大 500 连接、最小 20 空闲连接、30 分钟生命周期、
// 5 分钟空闲超时、30 秒健康检查。
// 在生产环境中，pgxpool 与前置的 PgBouncer 配合使用：
// pgxpool 管理到 PgBouncer 的连接，PgBouncer 再以 transaction 模式
// 复用到 PostgreSQL 的物理连接，最大承载万级并发。
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

// 连接池参数说明：
//   MaxConns=500    — 最大并发连接数（由 PgBouncer 进一步收束到 PG 端）
//   MinConns=20     — 预热连接数，避免突发流量时的冷启动延迟
//   MaxConnLifetime — 强制连接轮换，防止长连接问题
//   HealthCheck     — 定期 ping，及时释放失效连接
	cfg.MaxConns = 500
        cfg.MinConns = 20
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close 关闭连接池，释放所有数据库连接。应在服务优雅关闭时调用。
func (s *Store) Close() {
	s.pool.Close()
}

// ==================== Account ====================

// GetAccountByAPIKey 通过 API Key 查找账户，用于认证中间件的热路径。
// accounts.api_key 列有 B-tree 索引，查询复杂度 O(log n)。
// 只返回 is_active=TRUE 的账户，已停用账户视为不存在。
func (s *Store) GetAccountByAPIKey(ctx context.Context, apiKey string) (*model.Account, error) {
	var a model.Account
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, api_key, is_admin, is_active, created_at, updated_at
		 FROM accounts WHERE api_key = $1 AND is_active = TRUE`, apiKey,
	).Scan(&a.ID, &a.Username, &a.APIKey, &a.IsAdmin, &a.IsActive, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateAccount 创建新账户，自动生成 "tm_" 前缀的 API Key。
// 若 username 已存在（唯一约束），pgx 返回 unique_violation 错误。
func (s *Store) CreateAccount(ctx context.Context, username string) (*model.Account, error) {
	apiKey := generateAPIKey()
	var a model.Account
	err := s.pool.QueryRow(ctx,
		`INSERT INTO accounts (username, api_key) VALUES ($1, $2)
		 RETURNING id, username, api_key, is_admin, is_active, created_at, updated_at`,
		username, apiKey,
	).Scan(&a.ID, &a.Username, &a.APIKey, &a.IsAdmin, &a.IsActive, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteAccount 按 UUID 删除账户（级联删除其所有邮箱和邮件）。
func (s *Store) DeleteAccount(ctx context.Context, accountID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, accountID)
	return err
}

// ListAccounts 返回分页账户列表，按创建时间倒序排列。
// 先查总数再查数据（两次查询），适合管理后台低频使用场景。
func (s *Store) ListAccounts(ctx context.Context, page, size int) ([]model.Account, int, error) {
	var total int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, username, api_key, is_admin, is_active, created_at, updated_at
		 FROM accounts ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	accounts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[model.Account])
	if err != nil {
		return nil, 0, err
	}
	return accounts, total, nil
}

// GetAdminAPIKey 获取第一个管理员账号的 API Key（用于写入 admin.key 文件）
func (s *Store) GetAdminAPIKey(ctx context.Context) (string, error) {
	var apiKey string
	err := s.pool.QueryRow(ctx,
		`SELECT api_key FROM accounts WHERE is_admin = TRUE ORDER BY created_at LIMIT 1`,
	).Scan(&apiKey)
	return apiKey, err
}

// ==================== Domain ====================

// AddDomain 直接将域名以 active 状态加入域名池（跳过 MX 验证）。
// 适用于管理员手动添加已确认 DNS 正确的域名。
// 域名统一转为小写存储。
func (s *Store) AddDomain(ctx context.Context, domain string) (*model.Domain, error) {
	var d model.Domain
	err := s.pool.QueryRow(ctx,
		`INSERT INTO domains (domain, is_active, status) VALUES ($1, TRUE, 'active')
		 RETURNING id, domain, is_active, status, created_at, mx_checked_at`,
		strings.ToLower(domain),
	).Scan(&d.ID, &d.Domain, &d.IsActive, &d.Status, &d.CreatedAt, &d.MxCheckedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// AddDomainPending 将域名以 pending 状态写入，等待后台 goroutine 的 MX 轮询验证。
// 使用 ON CONFLICT DO UPDATE 实现幂等写入：
//   - 若域名已为 active，保持 active 不降级
//   - 若域名已为 disabled/pending，重置为 pending 重新触发验证
func (s *Store) AddDomainPending(ctx context.Context, domain string) (*model.Domain, error) {
	var d model.Domain
	err := s.pool.QueryRow(ctx,
		`INSERT INTO domains (domain, is_active, status) VALUES ($1, FALSE, 'pending')
		 ON CONFLICT (domain) DO UPDATE
		   SET status = CASE WHEN domains.status = 'active' THEN 'active' ELSE 'pending' END,
		       is_active = CASE WHEN domains.status = 'active' THEN TRUE ELSE FALSE END
		 RETURNING id, domain, is_active, status, created_at, mx_checked_at`,
		strings.ToLower(domain),
	).Scan(&d.ID, &d.Domain, &d.IsActive, &d.Status, &d.CreatedAt, &d.MxCheckedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDomains 返回所有域名（含 pending/disabled），供管理面板展示。
func (s *Store) ListDomains(ctx context.Context) ([]model.Domain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at FROM domains ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.Domain])
}

// GetActiveDomains 返回所有 is_active=TRUE 的域名，用于后台 MX 健康复检。
func (s *Store) GetActiveDomains(ctx context.Context) ([]model.Domain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at FROM domains WHERE is_active = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.Domain])
}

// GetRandomActiveDomain 从所有活跃域名中随机选取一个，用于创建邮箱时分配域名。
// ORDER BY RANDOM() 在域名数量较少（通常 < 100）时性能可接受。
func (s *Store) GetRandomActiveDomain(ctx context.Context) (*model.Domain, error) {
	var d model.Domain
	err := s.pool.QueryRow(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at FROM domains
		 WHERE is_active = TRUE ORDER BY RANDOM() LIMIT 1`,
	).Scan(&d.ID, &d.Domain, &d.IsActive, &d.Status, &d.CreatedAt, &d.MxCheckedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDomainByName 按域名字符串查询域名记录（大小写不敏感）。
// 若域名不存在或未处于 is_active=TRUE 状态，则返回错误。
// 供创建邮箱时用户指定域名使用，只允许使用活跃域名。
func (s *Store) GetDomainByName(ctx context.Context, domain string) (*model.Domain, error) {
	var d model.Domain
	err := s.pool.QueryRow(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at
		 FROM domains WHERE domain = $1 AND is_active = TRUE`,
		strings.ToLower(domain),
	).Scan(&d.ID, &d.Domain, &d.IsActive, &d.Status, &d.CreatedAt, &d.MxCheckedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDomainByID 按主键查询域名，供前端轮询 MX 验证状态时使用。
func (s *Store) GetDomainByID(ctx context.Context, domainID int) (*model.Domain, error) {
	var d model.Domain
	err := s.pool.QueryRow(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at FROM domains WHERE id = $1`,
		domainID,
	).Scan(&d.ID, &d.Domain, &d.IsActive, &d.Status, &d.CreatedAt, &d.MxCheckedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListPendingDomains 返回所有 status='pending' 的待验证域名，
// 供后台 goroutine 每 30 秒批量检测 MX 记录。
func (s *Store) ListPendingDomains(ctx context.Context) ([]model.Domain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, domain, is_active, status, created_at, mx_checked_at
		 FROM domains WHERE status = 'pending'
		 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.Domain])
}

// PromoteDomainToActive 将域名状态从 pending 升级为 active（MX 验证通过）。
// 同时记录 mx_checked_at 时间戳，用于前端展示"验证通过时间"。
func (s *Store) PromoteDomainToActive(ctx context.Context, domainID int) error {
	now := time.Now()
	_, err := s.pool.Exec(ctx,
		`UPDATE domains SET is_active = TRUE, status = 'active', mx_checked_at = $1 WHERE id = $2`,
		now, domainID)
	return err
}

// TouchDomainCheckTime 仅更新 mx_checked_at 为当前时间，
// 记录"最近一次检测时间"，无论检测结果是否通过都会调用。
func (s *Store) TouchDomainCheckTime(ctx context.Context, domainID int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE domains SET mx_checked_at = NOW() WHERE id = $1`, domainID)
	return err
}

// DisableDomainMX 将域名降级为 disabled（MX 健康复检失败时调用）。
// 停用后 Postfix 将在 60 秒内同步移除该域名，新邮件不再投递。
func (s *Store) DisableDomainMX(ctx context.Context, domainID int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE domains SET is_active = FALSE, status = 'disabled', mx_checked_at = NOW() WHERE id = $1`,
		domainID)
	return err
}

// DeleteDomain 从域名池中永久删除域名。
// 注意：若有邮箱使用此域名，需先删除邮箱或数据库会因外键约束报错。
func (s *Store) DeleteDomain(ctx context.Context, domainID int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM domains WHERE id = $1`, domainID)
	return err
}

// ToggleDomain 手动启用（active）或停用（disabled）域名。
// 同时更新 is_active 和 status 两个冗余字段，保持数据一致性。
func (s *Store) ToggleDomain(ctx context.Context, domainID int, active bool) error {
	status := "disabled"
	if active {
		status = "active"
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE domains SET is_active = $1, status = $2 WHERE id = $3`, active, status, domainID)
	return err
}

// GetStats 通过单条 SQL 的多个子查询返回全平台统计数据，避免多次往返。
// 该查询扫描 4 张表，在大数据量时可考虑缓存（目前直接查询，适合中小规模）。
func (s *Store) GetStats(ctx context.Context) (*model.Stats, error) {
	var st model.Stats
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM mailboxes)                         AS total_mailboxes,
		  (SELECT COUNT(*) FROM mailboxes WHERE expires_at > NOW()) AS active_mailboxes,
		  (SELECT COUNT(*) FROM emails)                            AS total_emails,
		  (SELECT COUNT(*) FROM domains WHERE is_active = TRUE)    AS active_domains,
		  (SELECT COUNT(*) FROM domains WHERE status = 'pending')  AS pending_domains,
		  (SELECT COUNT(*) FROM accounts WHERE is_active = TRUE)   AS total_accounts
	`).Scan(
		&st.TotalMailboxes, &st.ActiveMailboxes,
		&st.TotalEmails, &st.ActiveDomains,
		&st.PendingDomains, &st.TotalAccounts,
	)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// ==================== Mailbox ====================

// CreateMailbox 创建一个新的临时邮箱，计算并存储过期时间。
// ttlMinutes <= 0 时默认 30 分钟。
// full_address 列有唯一索引，若冲突返回 unique_violation 错误。
func (s *Store) CreateMailbox(ctx context.Context, accountID uuid.UUID, address string, domainID int, fullAddress string, ttlMinutes int) (*model.Mailbox, error) {
	if ttlMinutes <= 0 {
		ttlMinutes = 30
	}
	expiresAt := time.Now().Add(time.Duration(ttlMinutes) * time.Minute)
	var m model.Mailbox
	err := s.pool.QueryRow(ctx,
		`INSERT INTO mailboxes (account_id, address, domain_id, full_address, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, account_id, address, domain_id, full_address, created_at, expires_at`,
		accountID, address, domainID, fullAddress, expiresAt,
	).Scan(&m.ID, &m.AccountID, &m.Address, &m.DomainID, &m.FullAddress, &m.CreatedAt, &m.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMailboxes 返回指定账户的分页邮箱列表（含已过期的邮箱，前端可据 expires_at 展示状态）。
func (s *Store) ListMailboxes(ctx context.Context, accountID uuid.UUID, page, size int) ([]model.Mailbox, int, error) {
	var total int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM mailboxes WHERE account_id = $1`, accountID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, address, domain_id, full_address, created_at, expires_at
		 FROM mailboxes WHERE account_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		accountID, size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	mailboxes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[model.Mailbox])
	if err != nil {
		return nil, 0, err
	}
	return mailboxes, total, nil
}

// GetMailbox 按邮箱 ID 和账户 ID 查询邮箱（强制所有权验证）。
// 若邮箱不属于该账户，返回 pgx.ErrNoRows，handler 层统一返回 404。
func (s *Store) GetMailbox(ctx context.Context, mailboxID uuid.UUID, accountID uuid.UUID) (*model.Mailbox, error) {
	var m model.Mailbox
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, address, domain_id, full_address, created_at, expires_at
		 FROM mailboxes WHERE id = $1 AND account_id = $2`,
		mailboxID, accountID,
	).Scan(&m.ID, &m.AccountID, &m.Address, &m.DomainID, &m.FullAddress, &m.CreatedAt, &m.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteMailbox 删除邮箱（同时通过 ON DELETE CASCADE 删除其所有邮件）。
// 必须同时匹配 mailboxID 和 accountID，防止越权删除他人邮箱。
// 若影响行数为 0，返回 pgx.ErrNoRows（表示邮箱不存在或不属于该用户）。
func (s *Store) DeleteMailbox(ctx context.Context, mailboxID uuid.UUID, accountID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM mailboxes WHERE id = $1 AND account_id = $2`, mailboxID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetMailboxByFullAddress 按完整邮件地址查找邮箱，供 /internal/deliver 投递邮件时使用。
// 地址统一转小写后查询，与存储时的大小写处理一致。
func (s *Store) GetMailboxByFullAddress(ctx context.Context, fullAddress string) (*model.Mailbox, error) {
	var m model.Mailbox
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_id, address, domain_id, full_address, created_at, expires_at
		 FROM mailboxes WHERE full_address = $1`,
		strings.ToLower(fullAddress),
	).Scan(&m.ID, &m.AccountID, &m.Address, &m.DomainID, &m.FullAddress, &m.CreatedAt, &m.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteExpiredMailboxes 批量删除所有已过期邮箱（expires_at < NOW()），
// 由后台 goroutine 每 1 分钟调用一次。
// emails 表有 ON DELETE CASCADE，关联邮件会一并删除。
// 返回实际删除的行数，为 0 时不打印日志（避免无意义输出）。
func (s *Store) DeleteExpiredMailboxes(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mailboxes WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CheckDomainMX 是一个独立的 DNS 检测函数（包级，非 Store 方法），
// 供后台 goroutine 和 handler 层直接调用。
//
// 检测逻辑：
//  1. net.LookupMX 查询域名的 MX 记录
//  2. 对每条 MX 记录的主机名做 A 记录查询
//  3. 若任意一个 IP 匹配 serverIP，则认为验证通过
//
// 返回值：
//   matched  — 是否通过验证
//   mxHosts  — 解析到的 MX 主机名列表（用于调试/展示）
//   status   — 人类可读的检测状态描述
func CheckDomainMX(domain, serverIP string) (matched bool, mxHosts []string, status string) {
	mxRecords, err := net.LookupMX(domain)
	if err != nil {
		return false, nil, fmt.Sprintf("DNS查询失败: %v", err)
	}
	if len(mxRecords) == 0 {
		return false, nil, "未找到MX记录，请先配置MX记录"
	}
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")
		mxHosts = append(mxHosts, host)
		addrs, err := net.LookupHost(host)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if addr == serverIP {
				return true, mxHosts, fmt.Sprintf("✓ MX记录匹配：%s → %s", host, addr)
			}
		}
	}
	return false, mxHosts, fmt.Sprintf("MX记录(%s)未指向本服务器(%s)", strings.Join(mxHosts, ","), serverIP)
}

// ==================== Email ====================

// InsertEmail 将解析后的邮件内容写入数据库。
// size_bytes 自动计算为原始 MIME 的字节长度（len(raw)）。
// 由 /internal/deliver 接口调用，是邮件投递的最后一步。
func (s *Store) InsertEmail(ctx context.Context, mailboxID uuid.UUID, sender, subject, bodyText, bodyHTML, raw string) (*model.Email, error) {
	var e model.Email
	err := s.pool.QueryRow(ctx,
		`INSERT INTO emails (mailbox_id, sender, subject, body_text, body_html, raw_message, size_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, sender, subject, body_text, body_html, raw_message, size_bytes, received_at`,
		mailboxID, sender, subject, bodyText, bodyHTML, raw, len(raw),
	).Scan(&e.ID, &e.MailboxID, &e.Sender, &e.Subject, &e.BodyText, &e.BodyHTML, &e.RawMessage, &e.SizeBytes, &e.ReceivedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListEmails 返回邮件摘要列表（不含正文），按收件时间倒序分页。
// 注意：只查询 EmailSummary 字段（id/sender/subject/size/time），不查 body，节省带宽。
// emails 表在 (mailbox_id, received_at DESC) 上有复合索引，分页性能良好。
func (s *Store) ListEmails(ctx context.Context, mailboxID uuid.UUID, page, size int) ([]model.EmailSummary, int, error) {
	var total int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM emails WHERE mailbox_id = $1`, mailboxID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, sender, subject, size_bytes, received_at
		 FROM emails WHERE mailbox_id = $1
		 ORDER BY received_at DESC LIMIT $2 OFFSET $3`,
		mailboxID, size, (page-1)*size,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	emails, err := pgx.CollectRows(rows, pgx.RowToStructByPos[model.EmailSummary])
	if err != nil {
		return nil, 0, err
	}
	return emails, total, nil
}

// GetEmail 获取单封邮件的完整内容（含 HTML 正文和原始 MIME）。
// 必须同时匹配 emailID 和 mailboxID，确保只能读取自己邮箱中的邮件。
func (s *Store) GetEmail(ctx context.Context, emailID uuid.UUID, mailboxID uuid.UUID) (*model.Email, error) {
	var e model.Email
	err := s.pool.QueryRow(ctx,
		`SELECT id, mailbox_id, sender, subject, body_text, body_html, raw_message, size_bytes, received_at
		 FROM emails WHERE id = $1 AND mailbox_id = $2`,
		emailID, mailboxID,
	).Scan(&e.ID, &e.MailboxID, &e.Sender, &e.Subject, &e.BodyText, &e.BodyHTML, &e.RawMessage, &e.SizeBytes, &e.ReceivedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// DeleteEmail 删除单封邮件（同时匹配 emailID 和 mailboxID 防止越权）。
// 影响行数为 0 时返回 pgx.ErrNoRows，handler 层返回 404。
func (s *Store) DeleteEmail(ctx context.Context, emailID uuid.UUID, mailboxID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM emails WHERE id = $1 AND mailbox_id = $2`, emailID, mailboxID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ==================== Helpers ====================

// generateAPIKey 生成一个密码学安全的 API Key，格式为 "tm_" + 48 位十六进制字符。
// 使用 crypto/rand 保证随机性，不可预测。
// 此函数仅在 package 内调用（首字母小写），外部通过 CreateAccount 间接使用。
func generateAPIKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "tm_" + hex.EncodeToString(b)
}

// GenerateRandomAddress 生成一个 10 位随机小写字母+数字字符串，作为邮箱本地部分。
// 使用 crypto/rand 确保地址不可预测，降低被猜到的风险。
// 例如生成 "a3k9xmqz1r"，最终邮箱地址为 "a3k9xmqz1r@domain.com"。
func GenerateRandomAddress() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	length := 10
	result := make([]byte, length)
	for i := range result {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		result[i] = chars[n.Int64()]
	}
	return string(result)
}
