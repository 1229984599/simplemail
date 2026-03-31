# ============================================================
# TempMail 单容器构建 - Go API + 嵌入前端 + Postfix
# ============================================================

# ==================== Stage 1: Go 构建 ====================
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 复制 Go 源码
COPY api/ .

# 复制前端文件用于 go:embed（main.go 中 //go:embed all:frontend）
COPY frontend/ ./frontend/

RUN go mod tidy && go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/api-server .

# ==================== Stage 2: 运行镜像 ====================
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai

RUN apt-get update && apt-get install -y --no-install-recommends \
    postfix \
    python3 \
    curl \
    ca-certificates \
    tzdata \
    supervisor \
    && rm -rf /var/lib/apt/lists/*

# Go 二进制
COPY --from=builder /build/api-server /usr/local/bin/api-server

# Postfix 配置
COPY postfix/main.cf /etc/postfix/main.cf
COPY postfix/master.cf /etc/postfix/master.cf
COPY postfix/mail-receiver.py /usr/local/bin/mail-receiver
COPY postfix/entrypoint.sh /entrypoint.sh

# Supervisord 配置
COPY supervisord.conf /etc/supervisor/conf.d/supervisord.conf

# 权限 & 初始化
RUN chmod +x /usr/local/bin/api-server /usr/local/bin/mail-receiver /entrypoint.sh \
    && touch /etc/postfix/virtual_domains \
    && postmap /etc/postfix/virtual_domains \
    && mkdir -p /var/log/supervisor

# 数据卷
VOLUME /data

EXPOSE 8080 25

ENTRYPOINT ["/entrypoint.sh"]
