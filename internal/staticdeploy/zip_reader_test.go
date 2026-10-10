package staticdeploy

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type measuredZIPSource struct {
	io.ReaderAt
	read int64
}

// ReadAt 统计实际读取量，验证拒绝发生在目录解析预算内。
func (r *measuredZIPSource) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, offset)
	r.read += int64(n)
	return n, err
}

// TestZIPIndexBudget 构造条目数正常但目录注释过大的有效归档，避免元数据绕过解压体积规则。
func TestZIPIndexBudget(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for i := 0; i < 140; i++ {
		if _, err := writer.CreateHeader(&zip.FileHeader{Name: "index.html", Comment: strings.Repeat("x", 65535)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	source := &measuredZIPSource{ReaderAt: bytes.NewReader(data.Bytes())}
	if _, err := newZIPReader(context.Background(), source, int64(data.Len())); !errors.Is(err, errZIPIndexTooLarge) {
		t.Fatalf("expected metadata budget rejection, got %v", err)
	}
	if source.read > maxZIPIndexBytes {
		t.Fatalf("metadata read exceeded budget: %d", source.read)
	}
}

// TestZIPBodyHasSeparateBudget 目录读取限额不能误拒绝合法的大文件正文。
func TestZIPBodyHasSeparateBudget(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.CreateHeader(&zip.FileHeader{Name: "index.html", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("a"), int(maxZIPIndexBytes)+1)
	if _, err := file.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := newZIPReader(context.Background(), bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	input, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	actual, err := io.ReadAll(input)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("valid body changed or rejected: %v", err)
	}
}

// TestZIPIndexCancellation 目录解析阶段也响应取消，尚未读取归档时即可退出。
func TestZIPIndexCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &measuredZIPSource{ReaderAt: bytes.NewReader(make([]byte, 64))}
	if _, err := newZIPReader(ctx, source, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if source.read != 0 {
		t.Fatal("canceled request read archive data")
	}
}
