package staticdeploy

import (
	"archive/zip"
	"context"
	"errors"
	"io"
)

const maxZIPIndexBytes int64 = 8 << 20

var errZIPIndexTooLarge = errors.New("ZIP directory metadata exceeds read budget")

type zipSource struct {
	reader         io.ReaderAt
	ctx            context.Context
	indexRemaining int64
	indexComplete  bool
}

// ReadAt 为 ZIP 目录解析设置固定读取预算，正文仍由解压字节和磁盘规则限制。
func (r *zipSource) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if !r.indexComplete && int64(len(data)) > r.indexRemaining {
		return 0, errZIPIndexTooLarge
	}
	n, err := r.reader.ReadAt(data, offset)
	if !r.indexComplete {
		r.indexRemaining -= int64(n)
	}
	return n, err
}

// newZIPReader 限制建立条目列表的资源消耗，避免只在全部元数据已分配后才检查条目数。
func newZIPReader(ctx context.Context, source io.ReaderAt, size int64) (*zip.Reader, error) {
	input := &zipSource{reader: source, ctx: ctx, indexRemaining: maxZIPIndexBytes}
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return nil, err
	}
	input.indexComplete = true
	return reader, nil
}
