package staticdeploy

import (
	"bytes"
	"context"
	"io"
	"os"
	"sort"

	"stacknest/server/internal/project"
)

// equalContent 只比较同名同大小文件，复用缓冲区，发现不同的字节就结束。
func equalContent(ctx context.Context, before, after *os.Root, file project.PublishedFile, buffers *[2][32 << 10]byte) (bool, error) {
	left, err := before.Open(file.Path)
	if err != nil {
		return false, err
	}
	defer left.Close()
	right, err := after.Open(file.Path)
	if err != nil {
		return false, err
	}
	defer right.Close()
	for remaining := file.Bytes; ; {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// 最后一块多读一个字节，检查列举文件后长度有无变化。
		limit := min(remaining+1, int64(len(buffers[0])))
		want := min(remaining, limit)
		for i, input := range [2]*os.File{left, right} {
			n, err := io.ReadFull(input, buffers[i][:limit])
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return false, err
			}
			if int64(n) != want {
				return false, project.ErrVersionConflict
			}
		}
		if !bytes.Equal(buffers[0][:want], buffers[1][:want]) {
			return false, nil
		}
		if want < limit {
			return true, nil
		}
		remaining -= want
	}
}

// Changes 返回按路径排序的新增、删除和内容修改文件，不读取压缩包或系统目录。
func (d *Deployer) Changes(ctx context.Context, slug, base, target string) ([]project.FileChange, error) {
	after, err := d.FilesAt(ctx, slug, target)
	if err != nil {
		return nil, err
	}
	changes := []project.FileChange{}
	if base == target {
		return changes, nil
	}
	before := map[string]int64{}
	var baseRoot, targetRoot *os.Root
	if base != "" {
		files, err := d.FilesAt(ctx, slug, base)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			before[file.Path] = file.Bytes
		}
		baseRoot, err = d.OpenVersion(slug, base)
		if err != nil {
			return nil, err
		}
		defer baseRoot.Close()
		targetRoot, err = d.OpenVersion(slug, target)
		if err != nil {
			return nil, err
		}
		defer targetRoot.Close()
	}
	var buffers [2][32 << 10]byte
	for _, next := range after {
		old, exists := before[next.Path]
		kind := "added"
		if exists {
			delete(before, next.Path)
			if old == next.Bytes {
				same, err := equalContent(ctx, baseRoot, targetRoot, next, &buffers)
				if err != nil {
					return nil, err
				}
				if same {
					continue
				}
			}
			kind = "modified"
		}
		changes = append(changes, project.FileChange{Path: next.Path, Kind: kind, BeforeBytes: old, AfterBytes: next.Bytes})
	}
	for name, old := range before {
		changes = append(changes, project.FileChange{Path: name, Kind: "deleted", BeforeBytes: old})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}
