// Package config 负责从环境变量加载服务配置。
//
// 所有配置项均通过环境变量注入，不在代码中硬编码任何默认 IP / 密码等敏感信息。
// 在 Docker Compose 部署中，这些变量通常来自 .env 文件或 compose 的 environment 字段。
package config

import (
	"os"
	"strconv"
)

// Config 保存服务运行所需的全部配置项。
// 字段与环境变量的对应关系见 Load() 函数。
type Config struct {
	Port          string // 监听端口，对应 PORT（默认 8080）
	DBDSN         string // PostgreSQL 连接串，对应 DB_DSN（通过 PgBouncer 代理）
	RedisAddr     string // Redis 地址，对应 REDIS_ADDR（默认 redis:6379）
	RedisPassword string // Redis 密码，对应 REDIS_PASSWORD
	RateLimit     int    // 每个窗口允许的最大请求数，对应 RATE_LIMIT（默认 500）
	RateWindow    int    // 速率限制窗口大小（秒），对应 RATE_WINDOW（默认 60）
	SMTPServerIP  string // 服务器公网 IP，用于 MX 验证和 SPF 提示，对应 SMTP_SERVER_IP
	SMTPHostname  string // 邮件服务器主机名（如 mail.example.com），用作 MX 记录目标，对应 SMTP_HOSTNAME
}

// Load 从环境变量读取配置并返回 *Config。
// 若环境变量未设置，使用括号内的默认值（SMTP_SERVER_IP / SMTP_HOSTNAME 无默认值）。
func Load() *Config {
	rl, _ := strconv.Atoi(getEnv("RATE_LIMIT", "500"))
	rw, _ := strconv.Atoi(getEnv("RATE_WINDOW", "60"))

	return &Config{
		Port:          getEnv("PORT", "8080"),
		DBDSN:         getEnv("DB_DSN", ""),
		RedisAddr:     getEnv("REDIS_ADDR", "redis:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RateLimit:     rl,
		RateWindow:    rw,
		SMTPServerIP:  os.Getenv("SMTP_SERVER_IP"),
		SMTPHostname:  os.Getenv("SMTP_HOSTNAME"),
	}
}

// getEnv 读取环境变量 key；若为空则返回 fallback 默认值。
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
