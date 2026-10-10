package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadBackupStates 验证成功记录在后续失败时保留，并区分任务中断和过期备份。
func TestReadBackupStates(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-30T08:00:00Z")
	path := filepath.Join(t.TempDir(), "status.json")
	for _, sample := range []struct {
		body, state string
		overdue     bool
	}{
		{`{"version":1,"state":"succeeded","lastAttempt":{"startedAt":"2026-09-30T07:50:00Z","finishedAt":"2026-09-30T07:51:00Z","stage":"completed"},"lastSuccess":{"createdAt":"2026-09-30T07:51:00Z","databaseBytes":10,"filesBytes":20,"totalBytes":30},"retainedCount":2,"retainedBytes":60}`, "succeeded", false},
		{`{"version":1,"state":"failed","lastAttempt":{"startedAt":"2026-09-30T07:50:00Z","finishedAt":"2026-09-30T07:51:00Z","stage":"database"},"lastSuccess":{"createdAt":"2026-09-27T07:51:00Z","databaseBytes":10,"filesBytes":20,"totalBytes":30},"retainedCount":1,"retainedBytes":30}`, "failed", true},
		{`{"version":1,"state":"failed","lastAttempt":{"startedAt":"2026-09-30T07:50:00Z","finishedAt":"2026-09-30T07:51:00Z","stage":"apps-freeze"},"lastSuccess":{"createdAt":"2026-09-30T07:49:00Z","databaseBytes":10,"filesBytes":20,"totalBytes":30},"retainedCount":1,"retainedBytes":30}`, "failed", false},
		{`{"version":1,"state":"failed","lastAttempt":{"startedAt":"2026-09-30T07:50:00Z","finishedAt":"2026-09-30T07:51:00Z","stage":"apps-restore"},"lastSuccess":{"createdAt":"2026-09-30T07:49:00Z","databaseBytes":10,"filesBytes":20,"totalBytes":30},"retainedCount":1,"retainedBytes":30}`, "failed", false},
		{`{"version":1,"state":"running","lastAttempt":{"startedAt":"2026-09-30T07:59:00Z","stage":"initialize"},"retainedCount":0,"retainedBytes":0}`, "running", false},
		{`{"version":1,"state":"running","lastAttempt":{"startedAt":"2026-09-30T06:59:00Z","stage":"initialize"},"retainedCount":0,"retainedBytes":0}`, "interrupted", false},
	} {
		if err := os.WriteFile(path, []byte(sample.body), 0o600); err != nil {
			t.Fatal(err)
		}
		value, err := Read(path, now)
		if err != nil || value.State != sample.state || value.Overdue != sample.overdue {
			t.Fatalf("wrong backup state: %+v %v", value, err)
		}
	}
	if value, err := Read("", now); err != nil || value.State != "not_configured" {
		t.Fatalf("unconfigured: %+v %v", value, err)
	}
	if value, err := Read(path+"missing", now); err != nil || value.State != "never" {
		t.Fatalf("missing: %+v %v", value, err)
	}
}

// TestReadRejectsCorruptBackupStatus 损坏、额外字段、超限或不完整 JSON 均不能假报成功。
func TestReadRejectsCorruptBackupStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	for _, body := range []string{`{`, `{}`, `{"version":1,"state":"succeeded"}`, `{"version":1,"state":"failed","secret":"must not expose"}`, `{}` + strings.Repeat(" ", 65<<10), `{} {}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path, time.Now()); err == nil {
			t.Fatalf("accepted corrupt backup status: %.80s", body)
		}
	}
}
