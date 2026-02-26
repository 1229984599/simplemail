# ============================================================
# Postfix 收件容器
#
# 职责：
#   1. 运行 Postfix SMTP 服务，接收来自互联网的电子邮件
#   2. 通过 master.cf 中配置的 mailreceiver 管道，将每封邮件
#      交给 mail-receiver.py 脚本处理
#   3. mail-receiver.py 再调用 Go API 的 /api/receive-email 接口
#      将邮件存入数据库
#
# ⚠ 端口联动说明（SMTP 端口 25）：
#   若要修改 SMTP 监听端口（默认 25），需要同步修改以下位置：
#   1. 本文件 EXPOSE 行 → 改为新端口（仅文档用途，不影响实际监听）
#   2. docker-compose.yml → postfix.ports 左侧数字（宿主机映射端口）
#   3. postfix/master.cf → 第一行 "smtp inet n - n - - smtpd" 前的端口号
#      （若要改为非标准端口，需将 "smtp" 改为具体端口号，如 "2525"）
#   注意：端口 25 是 SMTP 标准端口，修改后外部 MX 记录投递将无法到达；
#         除非你的邮件服务商支持非标准端口，否则不建议修改。
#
# 构建上下文说明：
#   docker-compose.yml 中 postfix 服务的 build.context 为项目根目录，
#   build.dockerfile 指向 postfix/Dockerfile，
#   因此 COPY 路径以项目根目录为基准（如 postfix/main.cf）。
# ============================================================
FROM ubuntu:22.04

# 禁用 apt 交互式提示（避免构建时卡住）
ENV DEBIAN_FRONTEND=noninteractive

# 设置时区（影响日志时间戳；可改为 UTC 等其他时区）
ENV TZ=Asia/Shanghai

# 安装依赖：
#   postfix    — SMTP 服务主程序
#   python3    — 运行 mail-receiver.py 邮件处理脚本
#   curl       — entrypoint.sh 中用于调用 API 同步域名列表
#   ca-certificates — 让 curl/python3 信任 HTTPS 证书
#   tzdata     — 时区数据（配合 TZ 变量生效）
RUN apt-get update && apt-get install -y --no-install-recommends \
    postfix \
    python3 \
    curl \
    ca-certificates \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

# 复制 Postfix 配置文件（运行时 entrypoint.sh 会用 sed 动态替换部分变量）
COPY postfix/main.cf /etc/postfix/main.cf
# 复制 master.cf（定义 SMTP 服务和 mailreceiver 管道，端口在此文件中声明）
COPY postfix/master.cf /etc/postfix/master.cf
# 邮件处理脚本：解析 stdin 收到的原始邮件，调用 Go API 写库
COPY postfix/mail-receiver.py /usr/local/bin/mail-receiver
# 启动脚本：动态同步域名列表、替换主机名后启动 Postfix
COPY postfix/entrypoint.sh /entrypoint.sh

# 赋予脚本执行权限
RUN chmod +x /usr/local/bin/mail-receiver /entrypoint.sh

# 初始化 virtual_domains 文件（Postfix 用于判断哪些域名由本机接收）
# postmap 将文本文件编译为 Postfix 可直接查询的 hash 格式
# entrypoint.sh 每次启动时会动态更新此文件并重新 postmap
RUN touch /etc/postfix/virtual_domains && postmap /etc/postfix/virtual_domains

# 声明容器监听端口（SMTP 标准端口）
# ⚠ 修改此端口必须同步修改：
#   - docker-compose.yml postfix.ports 左侧数字
#   - postfix/master.cf 第一行端口声明
EXPOSE 25

CMD ["/entrypoint.sh"]
