package skills

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestArchive 验证实际分发包可解压、路径稳定且只含通用说明及完整许可证。
func TestArchive(t *testing.T) {
	data, err := Archive()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"stacknest-deploy/SKILL.md":                   true,
		"stacknest-deploy/agents/openai.yaml":         true,
		"stacknest-deploy/references/local-config.md": true,
		"stacknest-deploy/LICENSE":                    true,
		"stacknest-deploy/NOTICE":                     true,
	}
	for _, file := range reader.File {
		if !wanted[file.Name] || !file.Mode().IsRegular() {
			t.Fatalf("unexpected or duplicate archive entry: %s", file.Name)
		}
		delete(wanted, file.Name)
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		stream.Close()
		if err != nil || len(content) == 0 {
			t.Fatalf("unreadable archive entry: %s (%v)", file.Name, err)
		}
		for _, private := range []string{"snd_", "://stack-nest.", "D:\\", "C:\\"} {
			if bytes.Contains(content, []byte(private)) {
				t.Fatalf("instance settings or credentials in %s", file.Name)
			}
		}
		if file.Name == "stacknest-deploy/LICENSE" || file.Name == "stacknest-deploy/NOTICE" {
			original, err := os.ReadFile("../" + strings.TrimPrefix(file.Name, "stacknest-deploy/"))
			if err != nil || string(original) != string(content) {
				t.Fatalf("distributed license differs from repository: %s", file.Name)
			}
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("missing archive entries: %v", wanted)
	}
}
