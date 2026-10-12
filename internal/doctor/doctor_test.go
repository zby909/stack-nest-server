package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"stacknest/server/internal/config"
	"strings"
	"testing"
)

// TestDoctorFailureDoesNotLeakSecrets 错误连接串与密码不会进入可分享的诊断输出。
func TestDoctorFailureDoesNotLeakSecrets(t *testing.T) {
	secret := "not-for-diagnostic-output"
	cfg := config.Config{DatabaseURL: "postgres://" + secret + ":%zz@invalid", AuthSecret: secret, SMTPPassword: secret, SMTPPort: 0, DataDir: t.TempDir(), WebDir: t.TempDir()}
	for _, args := range [][]string{nil, {"--json"}} {
		var output, errors bytes.Buffer
		if code := Main(context.Background(), cfg, args, &output, &errors); code != 1 {
			t.Fatal("expected diagnostic failure", code)
		}
		if strings.Contains(output.String(), secret) || strings.Contains(errors.String(), secret) {
			t.Fatal("secret in report")
		}
		if len(args) > 0 {
			var report Report
			if json.Unmarshal(output.Bytes(), &report) != nil || !report.Failed || len(report.Checks) < 5 {
				t.Fatal("invalid JSON report")
			}
		}
	}
}

// TestDoctorRejectsUnknownArguments 避免误拼参数被当作成功自检。
func TestDoctorRejectsUnknownArguments(t *testing.T) {
	var out, err bytes.Buffer
	if Main(context.Background(), config.Config{}, []string{"--password=unsafe"}, &out, &err) != 2 {
		t.Fatal("unknown option accepted")
	}
}
