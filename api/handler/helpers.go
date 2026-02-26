// Package handler - helpers 处理器
package handler

import "github.com/google/uuid"

// parseUUID 将路由参数字符串（如 c.Param("id")）解析为 uuid.UUID。
// 解析失败时返回 error，handler 层统一返回 400 Bad Request。
func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}
