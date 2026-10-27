package auth

import (
	"errors"
	"strings"
)

// ValidateSecret 拒绝空白、过短及公开示例密钥；有效密钥原样使用，避免破坏已有加密数据。
func ValidateSecret(secret string) error {
	value := strings.TrimSpace(secret)
	if len(secret) < 32 || value == "" || strings.EqualFold(value, "replace-with-at-least-32-random-characters") {
		return errors.New("STACKNEST_AUTH_SECRET must contain at least 32 bytes and must not use the example value")
	}
	return nil
}
