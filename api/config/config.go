package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port         string
	DBDSN        string
	RateLimit    int
	RateWindow   int // seconds
	SMTPServerIP string
	EnvFilePath  string // .env 文件路径，用于回写环境变量
}

func Load() *Config {
	rl, _ := strconv.Atoi(getEnv("RATE_LIMIT", "500"))
	rw, _ := strconv.Atoi(getEnv("RATE_WINDOW", "60"))

	return &Config{
		Port:         getEnv("PORT", "8081"),
		DBDSN:        getEnv("DB_DSN", ""),
		RateLimit:    rl,
		RateWindow:   rw,
		SMTPServerIP: os.Getenv("SMTP_SERVER_IP"),
		EnvFilePath:  getEnv("ENV_FILE", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
