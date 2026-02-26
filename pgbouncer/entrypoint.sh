#!/bin/bash
# ============================================================
# PgBouncer 容器入口脚本
# ============================================================
# 作用：
#   在 PgBouncer 启动前，根据环境变量动态生成 userlist.txt 认证文件，
#   然后启动 PgBouncer。
#
# 为什么需要此脚本：
#   PgBouncer 需要一个包含用户名和密码的 userlist.txt 文件来验证客户端身份。
#   密码来自 .env 环境变量（通过 Docker Compose 注入），
#   不能硬编码在配置文件中，因此通过此脚本在运行时生成。
#
# 依赖的环境变量（来自 docker-compose.yml → pgbouncer.environment）：
#   POSTGRES_USER     — 数据库用户名（与 .env POSTGRES_USER 一致）
#   POSTGRES_PASSWORD — 数据库密码（与 .env POSTGRES_PASSWORD 一致）
# ============================================================
set -e

# 生成 PgBouncer 认证文件（格式："用户名" "密码"）
# 此文件路径在 pgbouncer.ini 的 auth_file 中指定
PGBOUNCER_AUTH_FILE="/etc/pgbouncer/userlist.txt"
echo "\"${POSTGRES_USER}\" \"${POSTGRES_PASSWORD}\"" > "$PGBOUNCER_AUTH_FILE"
echo "==> Generated PgBouncer userlist for user: ${POSTGRES_USER}"

# 启动 PgBouncer（前台运行，配置文件路径来自 docker-compose.yml 的 volume 挂载）
exec pgbouncer /etc/pgbouncer/pgbouncer.ini
