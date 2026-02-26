// Package store 的 settings.go 实现对 app_settings 表的读写操作。
//
// app_settings 是一张 key-value 表，存储可在运行时动态修改的系统配置，
// 例如注册开关、邮箱 TTL、站点标题、SMTP 服务器信息等。
// 这些配置优先级高于环境变量（handler 层先查 DB，再 fallback 到 env）。
package store

import (
	"context"
	"time"
)

// GetSetting 读取单个配置项
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM app_settings WHERE key = $1`, key,
	).Scan(&value)
	return value, err
}

// SetSetting 写入配置项（upsert）
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_settings (key, value, updated_at)
         VALUES ($1, $2, $3)
         ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3`,
		key, value, time.Now(),
	)
	return err
}

// GetAllSettings 读取所有配置项
func (s *Store) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}
