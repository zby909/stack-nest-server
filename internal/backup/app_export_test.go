package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestExportSnapshot 校验完成时间、不同应用隔离以及已轮换的下载不能打开新副本。
func TestExportSnapshot(t *testing.T) {
	dir := t.TempDir()
	at := "2026-11-20T03:00:00Z"
	status := filepath.Join(dir, "status.json")
	content := fmt.Sprintf(`{"version":1,"state":"succeeded","lastAttempt":{"startedAt":%q,"finishedAt":%q,"stage":"completed"},"lastSuccess":{"createdAt":%q,"databaseBytes":1,"filesBytes":4,"totalBytes":5},"retainedCount":1,"retainedBytes":5}`, at, at, at)
	if err := os.WriteFile(status, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-4111-8111-111111111111"
	const generation = "app-export-example1"
	if err := os.Mkdir(filepath.Join(dir, generation), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, generation, id+".tar.gz")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, "app-exports.json")
	write := func(ts string) {
		t.Helper()
		if err := os.WriteFile(index, []byte(fmt.Sprintf(`{"directory":%q,"createdAt":%q,"apps":{%q:{"state":"ready","bytes":4}}}`, generation, ts, id)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-11-19T03:00:00Z")
	stale, err := LookupAppExport(status, id)
	if err != nil || stale.State != "preparing" {
		t.Fatalf("%+v %v", stale, err)
	}
	write(at)
	other, err := LookupAppExport(status, "22222222-2222-4222-8222-222222222222")
	if err != nil || other.State != "no_data" {
		t.Fatal(other, err)
	}
	value, err := LookupAppExport(status, id)
	if err != nil || value.Snapshot != generation {
		t.Fatal(value, err)
	}
	f, err := value.Open()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "data" {
		t.Fatal(err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err = value.Open(); err == nil {
		t.Fatal("missing snapshot remains downloadable")
	}
}
