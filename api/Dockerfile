# ============================================================
# Go API 多阶段构建 - 最小化镜像
#
# 职责：
#   运行 TempMail 后端 API 服务（Go 语言编写），提供以下功能：
#   - 账号与 API Key 管理
#   - 域名 MX 验证与管理
#   - 邮箱（地址）创建与删除
#   - 邮件查询与内容读取
#   - 接收 Postfix 投递的原始邮件（/api/receive-email）
#   - 系统统计与全局设置
#
# 构建策略：多阶段构建（Multi-stage build）
#   - 阶段一（builder）：使用完整 Go 工具链编译，产出静态二进制
#   - 阶段二（runtime）：仅复制二进制到极小的 Alpine 镜像，大幅减小体积
#
# ⚠ 端口联动说明（API 端口 8080）：
#   若要修改 API 监听端口（默认 8080，由环境变量 API_PORT 控制），
#   需要同步修改以下位置：
#   1. .env → API_PORT=新端口
#   2. docker-compose.yml → api.ports 左侧数字（宿主机映射端口）
#   3. nginx/default.conf → 3 处 proxy_pass http://api:新端口
#   4. postfix/entrypoint.sh → sync-domains.sh 中的 API 地址端口
#   本文件的 EXPOSE 仅为文档声明，实际端口由 API_PORT 环境变量决定，
#   修改 EXPOSE 不影响功能，但建议保持与 API_PORT 一致便于阅读。
#
# 构建上下文说明：
#   docker-compose.yml 中 api 服务的 build.context 为 ./api 目录，
#   因此 COPY . . 复制的是 api/ 目录下的所有 Go 源码。
# ============================================================

# ==================== 阶段一：编译 ====================
# 使用官方 Go Alpine 镜像，包含完整编译工具链
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 复制全部源码（api/ 目录下的 go.mod、go.sum 和所有 .go 文件）
COPY . .

# 下载依赖（go mod tidy 清理多余依赖，download 预热模块缓存）
RUN go mod tidy && go mod download

# 编译为静态二进制（CGO_ENABLED=0 禁用 C 绑定，确保在 Alpine 中可运行）
# -ldflags="-s -w"：去除调试符号和 DWARF 信息，显著减小二进制体积
# GOOS=linux：明确目标平台（在 macOS/Windows 宿主构建时必须指定）
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/api-server .

# ==================== 阶段二：运行 ====================
# 使用极简 Alpine 镜像，仅包含运行时必要组件，镜像体积约 15MB
FROM alpine:3.20

# 安装运行时依赖：
#   ca-certificates — 让 Go HTTP 客户端信任 HTTPS 证书（调用外部 API 时需要）
#   tzdata          — 时区数据，配合 TZ 环境变量使日志时间正确显示
RUN apk --no-cache add ca-certificates tzdata

# 设置时区（影响日志时间戳；可改为 UTC 等其他时区）
ENV TZ=Asia/Shanghai

# 从编译阶段复制静态二进制（不复制源码，保持镜像最小化）
COPY --from=builder /build/api-server /usr/local/bin/api-server

# 声明容器监听端口（与 .env 中 API_PORT 保持一致，默认 8080）
# ⚠ 修改实际端口须在 .env 中改 API_PORT，并同步修改：
#   - docker-compose.yml api.ports 左侧数字
#   - nginx/default.conf 全部 3 处 proxy_pass 地址
#   - postfix/entrypoint.sh 中 API 调用地址
EXPOSE 8080

CMD ["api-server"]
