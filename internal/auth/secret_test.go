package auth

import (
	"strings"
	"testing"
)

// TestServiceRejectsExampleSecrets 校验真正的认证服务拒绝公开模板密钥，且错误不泄露配置。
func TestServiceRejectsExampleSecrets(t *testing.T) {
	for _, secret := range []string{
		"", strings.Repeat(" ", 48), strings.Repeat("x", 31),
		"replace-with-at-least-32-random-characters",
		"  REPLACE-WITH-AT-LEAST-32-RANDOM-CHARACTERS  ",
	} {
		service, err := NewService(superAdminStore{}, &invitationMailer{}, secret)
		if err == nil || service != nil {
			t.Fatal("unsafe secret accepted")
		}
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("error disclosed the secret")
		}
	}
}

// TestServiceKeepsValidSecret 原有有效密钥保持逐字节不变，包括升级时已有的首尾空白。
func TestServiceKeepsValidSecret(t *testing.T) {
	for _, secret := range []string{
		strings.Repeat("x", 32),
		"  28fa6ce9ec2ef880c28ab9295862dcb2cdbb1d7065e1414bcdd98d8f6bbcfdf2  ",
	} {
		service, err := NewService(superAdminStore{}, &invitationMailer{}, secret)
		if err != nil || string(service.secret) != secret {
			t.Fatal("valid existing secret was changed or rejected")
		}
	}
}
