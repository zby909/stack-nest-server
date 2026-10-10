package config

import "testing"

// TestInvitationLoginURL 确认邀请地址取自可信配置，空配置不会使用本地请求 Host。
func TestInvitationLoginURL(t *testing.T) {
	for _, tt := range []struct{ name, domain, override, want string }{
		{"unconfigured", "", "", ""},
		{"admin domain", "stack.example.com", "", "https://stack.example.com/login"},
		{"explicit URL", "stack.example.com", " https://private.example.com/login ", "https://private.example.com/login"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("STACKNEST_ADMIN_DOMAIN", tt.domain)
			t.Setenv("STACKNEST_INVITATION_LOGIN_URL", tt.override)
			if got := Load().InvitationLoginURL; got != tt.want {
				t.Fatalf("login URL = %q, want %q", got, tt.want)
			}
		})
	}
}
