#!/usr/bin/env python3
"""
mail-receiver.py — Postfix pipe 传输程序

职责：
    作为 Postfix 的 pipe 传输代理，接收 Postfix 投递的原始邮件（MIME 格式），
    解析后通过 HTTP POST 转发给 Go API 的内部投递接口（/internal/deliver）。

调用方式（由 Postfix master.cf 中的 mailreceiver 服务定义）：
    /usr/local/bin/mail-receiver ${recipient}

    Postfix 通过 stdin 传入完整的 RFC 2822 邮件内容，
    收件人地址通过命令行参数 argv[1] 传入。

退出码语义（Postfix pipe 协议）：
    0  — 成功（包括"收件人不存在"的静默丢弃，避免不必要的退信）
    75 — 临时失败（EX_TEMPFAIL），Postfix 会将邮件放入重试队列，稍后重试
         仅在网络错误（无法连接 API）时使用，避免邮件永久丢失

为何使用 Python：
    Python 标准库的 email 模块对 MIME 解析最成熟，
    能正确处理 multipart、Content-Transfer-Encoding、字符集等复杂场景，
    且无需额外安装依赖（Ubuntu 22.04 自带 Python 3）。

环境变量：
    API_URL — Go API 的内部地址，默认 http://api:8080（Docker Compose 服务名解析）
"""
import sys
import os
import email
import email.policy
import json
import urllib.request
import urllib.error

API_URL = os.environ.get("API_URL", "http://api:8080")

def main():
    # ── 步骤 1：从命令行参数获取收件人地址 ──
    # Postfix 在调用 pipe 时将 ${recipient} 作为第一个参数传入
    if len(sys.argv) < 2:
        print("Usage: mail-receiver <recipient>", file=sys.stderr)
        sys.exit(1)

    recipient = sys.argv[1].lower().strip()

    # ── 步骤 2：从 stdin 读取完整的原始邮件（MIME 格式）──
    # Postfix 通过管道将邮件内容写入 stdin
    raw = sys.stdin.read()
    if not raw:
        sys.exit(0)

    # ── 步骤 3：解析 MIME 邮件 ──
    # 使用 email.policy.default 启用现代解析（支持 UTF-8、Header 解码等）
    msg = email.message_from_string(raw, policy=email.policy.default)

    sender = msg.get("From", "")
    subject = msg.get("Subject", "")
    body_text = ""
    body_html = ""

    if msg.is_multipart():
        for part in msg.walk():
            ct = part.get_content_type()
            if ct == "text/plain" and not body_text:
                body_text = part.get_content()
            elif ct == "text/html" and not body_html:
                body_html = part.get_content()
    else:
        ct = msg.get_content_type()
        content = msg.get_content()
        if ct == "text/html":
            body_html = content
        else:
            body_text = content

    # ── 步骤 4：构造请求体并 POST 到 Go API ──
    # /internal/deliver 是仅限内网的投递接口，不经过认证中间件
    payload = json.dumps({
        "recipient": recipient,
        "sender": sender,
        "subject": subject,
        "body_text": body_text if isinstance(body_text, str) else str(body_text),
        "body_html": body_html if isinstance(body_html, str) else str(body_html),
        "raw": raw,
    }).encode("utf-8")

    req = urllib.request.Request(
        f"{API_URL}/internal/deliver",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            result = json.loads(resp.read())
            if result.get("status") == "delivered":
                sys.exit(0)
            # status == 'discarded'：收件人不存在，静默丢弃
            # 仍然退出 0，告知 Postfix 处理完成（不要退信/bounce）
            sys.exit(0)
    except urllib.error.URLError as e:
        # 网络错误（API 宕机、连接超时等）
        # 返回 75 = EX_TEMPFAIL，Postfix 将邮件加入重试队列，避免永久丢失
        print(f"Error delivering mail: {e}", file=sys.stderr)
        sys.exit(75)

if __name__ == "__main__":
    main()
