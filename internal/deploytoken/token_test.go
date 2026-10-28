package deploytoken

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTokenSecret 检查随机凭据的规范形式及所有序列化响应不泄漏摘要。
func TestTokenSecret(t *testing.T) {
	a, plain, err := New("project", "CI", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	b, second, err := New("project", "CI", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || plain == second || a.Hash == plain || len(a.Hash) != 64 {
		t.Fatal("token entropy or storage representation")
	}
	if got, err := Hash(plain); err != nil || got != a.Hash {
		t.Fatal("hash mismatch")
	}
	data, _ := json.Marshal(a)
	if strings.Contains(string(data), plain) || strings.Contains(string(data), a.Hash) || strings.Contains(string(data), "projectId") {
		t.Fatal("credential metadata leaked")
	}
	for _, value := range []string{"", plain + "=", "Bearer " + plain, strings.ToUpper(plain[:4]) + plain[4:]} {
		if _, err := Hash(value); err == nil {
			t.Fatal("accepted malformed token")
		}
	}
	for _, days := range []int{-1, 0, 366} {
		if _, _, err := New("p", "CI", days, false); err == nil {
			t.Fatal("accepted invalid expiry")
		}
	}
	for _, name := range []string{" ", "CI\nlog", strings.Repeat("x", 61)} {
		if _, _, err := New("p", name, 30, false); err == nil {
			t.Fatal("accepted invalid name")
		}
	}
}
