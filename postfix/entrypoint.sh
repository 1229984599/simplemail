#!/bin/bash
# ============================================================
# Postfix 容器入口脚本
# ============================================================
# 职责：
#   1. 生成初始虚拟域名文件（virtual_domains）
#   2. 使用环境变量覆盖 main.cf 中的主机名配置
#   3. 启动后台循环：每 60 秒从 Go API 同步活跃域名列表
#   4. 前台启动 Postfix
#
# 依赖的环境变量（来自 docker-compose.yml → postfix.environment）：
#   SMTP_HOSTNAME — 邮件服务器主机名（来自 .env）
#                   用于设置 Postfix 的 myhostname
#
# ============================================================
# 【端口联动说明】
# ============================================================
# sync-domains.sh 脚本中硬编码了 Go API 的地址：
#   http://api:8080/internal/domains
# ⚠️  如果修改了 API 端口（默认 8080），必须同步修改下方
#     sync-domains.sh 脚本中的 http://api:8080 为新端口。
#     同时确保 .env → API_PORT 和 docker-compose.yml → api.ports 也已修改。
#
# "api" 是 Docker Compose 服务名（固定，不要修改）。
# ============================================================
set -e

echo "==> Starting Postfix mail receiver..."

# 设置 mail-receiver.py 的可执行权限
chmod +x /usr/local/bin/mail-receiver

# ── 步骤 1：生成初始虚拟域名文件 ─────────────────────────────
# 写入占位域名，防止 Postfix 启动时找不到 virtual_domains 文件报错
# 实际域名列表由下面的 sync-domains.sh 在启动后 60 秒内同步
echo "${SMTP_HOSTNAME:-mail.example.com}     OK" > /etc/postfix/virtual_domains

# ── 步骤 2：创建域名同步脚本 ──────────────────────────────────
# 此脚本每 60 秒运行一次，从 Go API 拉取活跃域名，更新 Postfix 配置
cat > /usr/local/bin/sync-domains.sh << 'SCRIPT'
#!/bin/bash
# 从 API 获取活跃域名列表并更新 Postfix 虚拟域名文件
# ⚠️  【端口联动】http://api:8080 中的 8080 必须与以下保持一致：
#   - .env → API_PORT
#   - docker-compose.yml → api.ports 右边的端口
DOMAINS=$(curl -sf http://api:8080/internal/domains 2>/dev/null || echo "")
if [ -n "$DOMAINS" ]; then
    # 解析 JSON 响应，提取所有 is_active=true 的域名写入临时文件
    echo "$DOMAINS" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for d in data.get('domains', []):
    if d.get('is_active', False):
        print(f\"{d['domain']}     OK\")
" > /etc/postfix/virtual_domains.new

    # 只有新文件非空时才替换（防止 API 短暂不可用时清空域名列表）
    if [ -s /etc/postfix/virtual_domains.new ]; then
        mv /etc/postfix/virtual_domains.new /etc/postfix/virtual_domains
        postmap /etc/postfix/virtual_domains  # 重建 hash 索引
        postfix reload 2>/dev/null || true    # 热重载配置（不中断当前连接）
    fi
fi
SCRIPT
chmod +x /usr/local/bin/sync-domains.sh

# ── 步骤 3：生成 hash 索引（Postfix 需要 .db 文件）──────────
postmap /etc/postfix/virtual_domains

# ── 步骤 4：启动后台域名同步循环（每 60 秒同步一次）──────────
# 使用子 shell 后台运行，不阻塞主进程
(while true; do sleep 60; /usr/local/bin/sync-domains.sh; done) &

# ── 步骤 5：用环境变量覆盖 main.cf 中的主机名 ────────────────
# ⚠️  这里的 SMTP_HOSTNAME 来自 .env（通过 docker-compose.yml 传入）
#     修改 .env 中的 SMTP_HOSTNAME 即可，无需修改 postfix/main.cf
postconf -e "myhostname=${SMTP_HOSTNAME:-mail.example.com}"
postconf -e "virtual_mailbox_domains=hash:/etc/postfix/virtual_domains"
postconf -e "virtual_transport=mailreceiver:"

echo "==> Postfix hostname set to: ${SMTP_HOSTNAME:-mail.example.com}"
echo "==> Domain sync started (every 60s from http://api:8080/internal/domains)"

# ── 步骤 6：前台启动 Postfix（容器保持运行）─────────────────
exec postfix start-fg
