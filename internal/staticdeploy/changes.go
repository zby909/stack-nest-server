package staticdeploy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

type fingerprint struct {
	bytes int64
	hash  string
}

// fingerprints 对受限目录中的有界文件计算摘要，避免同大小文件被误认为未修改。
func (d *Deployer) fingerprints(ctx context.Context, slug, slot string) (map[string]fingerprint, error) {
	result := map[string]fingerprint{}
	if slot == "" {
		return result, nil
	}
	files, err := d.FilesAt(ctx, slug, slot)
	if err != nil {
		return nil, err
	}
	root, err := d.OpenVersion(slug, slot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, file := range files {
		input, err := root.Open(file.Path)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		count, copyErr := storage.Copy(ctx, nil, hash, io.LimitReader(input, file.Bytes+1), 0)
		closeErr := input.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if count != file.Bytes {
			return nil, project.ErrVersionConflict
		}
		result[file.Path] = fingerprint{bytes: file.Bytes, hash: fmt.Sprintf("%x", hash.Sum(nil))}
	}
	return result, nil
}

// Changes 返回按路径排序的新增、删除和内容修改文件，不读取压缩包或系统目录。
func (d *Deployer) Changes(ctx context.Context, slug, base, target string) ([]project.FileChange, error) {
	before, err := d.fingerprints(ctx, slug, base)
	if err != nil {
		return nil, err
	}
	after, err := d.fingerprints(ctx, slug, target)
	if err != nil {
		return nil, err
	}
	changes := []project.FileChange{}
	for name, next := range after {
		old, exists := before[name]
		kind := "added"
		if exists {
			kind = "modified"
		}
		if !exists || next != old {
			changes = append(changes, project.FileChange{Path: name, Kind: kind, BeforeBytes: old.bytes, AfterBytes: next.bytes})
		}
		delete(before, name)
	}
	for name, old := range before {
		changes = append(changes, project.FileChange{Path: name, Kind: "deleted", BeforeBytes: old.bytes})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}
