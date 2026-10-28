package deployclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
	"stacknest/server/internal/storage"
)

// UploadState 不保存凭据或文件内容，完整绑定文件、目标、版本和上传编号。
type UploadState struct {
	Format    int               `json:"format"`
	BaseURL   string            `json:"baseUrl"`
	ProjectID string            `json:"projectId"`
	TokenHash string            `json:"tokenFingerprint"`
	Request   resumable.Request `json:"request"`
}

// prepareState 新建状态使用独占写入，已有状态必须与文件及目标完全一致才能继续。
func (c *Client) prepareState(path string, p Project, name, hash, notes string, size int64, replaceDraftID string) (UploadState, error) {
	fingerprint := sha256.Sum256([]byte(c.Token))
	state := UploadState{Format: 1, BaseURL: c.BaseURL, ProjectID: p.ID, TokenHash: hex.EncodeToString(fingerprint[:16]), Request: resumable.Request{ID: uuid.NewString(), Kind: "update", Slug: p.Slug, ExpectedID: currentID(p), Preview: true, Notes: notes, FileName: name, Size: size, SHA256: hash}}
	state.Request.ReplaceDraftID = replaceDraftID
	if input, err := os.Open(path); err == nil {
		defer input.Close()
		data, err := io.ReadAll(io.LimitReader(input, (128<<10)+1))
		if err != nil {
			return state, err
		}
		var previous UploadState
		if len(data) > 128<<10 || json.Unmarshal(data, &previous) != nil || previous.Format != 1 || previous.BaseURL != state.BaseURL || previous.ProjectID != p.ID || previous.TokenHash != state.TokenHash || previous.Request.Kind != "update" || !previous.Request.Preview || previous.Request.Slug != p.Slug || previous.Request.FileName != name || previous.Request.Size != size || previous.Request.SHA256 != hash || previous.Request.Notes != notes || previous.Request.ReplaceDraftID != replaceDraftID {
			return state, errors.New("续传状态与文件、Token 或目标不一致；请用原文件和参数继续，或指定另一个 --state 文件")
		}
		if _, err := uuid.Parse(previous.Request.ID); err != nil {
			return state, errors.New("续传编号无效")
		}
		return previous, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return state, err
	}
	if replaceDraftID != "" && (p.Draft == nil || p.Draft.ID != replaceDraftID) {
		return state, errors.New("待发布版本已变化，请核实原预览；不会自动改为替换最新草稿")
	}
	if replaceDraftID == "" && p.Draft != nil {
		return state, errors.New("项目已有待发布版本；更新它请明确使用 --replace-draft 草稿编号，或先在后台发布、丢弃")
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return state, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return state, err
}

// Upload 在失败时保留状态，重复执行只续传原文件；完成后输出指定草稿的地址。
func (c *Client) Upload(ctx context.Context, file, statePath, notes string, progress io.Writer) (Result, error) {
	return c.UploadReplacing(ctx, file, statePath, notes, "", progress)
}

// UploadReplacing 把替换目标固定在续传收据中，重试时不能改接另一草稿。
func (c *Client) UploadReplacing(ctx context.Context, file, statePath, notes, replaceDraftID string, progress io.Writer) (Result, error) {
	if replaceDraftID != "" {
		id := strings.TrimPrefix(replaceDraftID, "dep-")
		parsed, err := uuid.Parse(id)
		if !strings.HasPrefix(replaceDraftID, "dep-") || err != nil || parsed.String() != id {
			return Result{}, errors.New("--replace-draft 必须是要替换的完整草稿编号 dep-UUID")
		}
	}
	f, err := os.Open(file)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() || !project.SupportedUploadFile(filepath.Base(file)) || info.Size() < 1 || info.Size() > storage.MaxArchiveBytes || len(notes) > 4096 {
		return Result{}, errors.New("请选择不超过 90 MiB 的 HTML / HTM / ZIP / RAR 文件，更新说明不超过 4096 字节")
	}
	hasher := sha256.New()
	if _, err = storage.Copy(ctx, nil, hasher, f, 0); err != nil {
		return Result{}, err
	}
	p, err := c.Project(ctx)
	if err != nil {
		return Result{}, err
	}
	if statePath == "" {
		identity := c.BaseURL + "\x00" + p.ID + "\x00" + c.Token + "\x00" + filepath.Base(file) + "\x00" + hex.EncodeToString(hasher.Sum(nil)) + "\x00" + notes
		if replaceDraftID != "" {
			identity += "\x00" + replaceDraftID
		}
		key := sha256.Sum256([]byte(identity))
		statePath = fmt.Sprintf(".stacknest-upload-%x.json", key[:12])
	}
	if progress != nil {
		fmt.Fprintln(progress, "续传收据："+statePath)
	}
	state, err := c.prepareState(statePath, p, filepath.Base(file), hex.EncodeToString(hasher.Sum(nil)), notes, info.Size(), replaceDraftID)
	if err != nil {
		return Result{}, err
	}
	body, _ := json.Marshal(state.Request)
	var status Status
	if err = c.retry(ctx, func() error { return c.request(ctx, "POST", "uploads", body, nil, &status) }); err != nil {
		return Result{}, err
	}
	if err = c.sendChunks(ctx, f, state.Request, &status, progress); err != nil {
		return Result{}, err
	}
	var result Project
	if err = c.retry(ctx, func() error {
		return c.request(ctx, "POST", "uploads/"+state.Request.ID+"/complete", []byte("{}"), nil, &result)
	}); err != nil {
		return Result{}, err
	}
	versionID := "dep-" + state.Request.ID
	if result.ID != p.ID {
		return Result{}, errors.New("项目身份已变化，请到后台核实")
	}
	output := Result{ProjectID: p.ID, Slug: p.Slug, DraftID: versionID, ExpectedID: state.Request.ExpectedID, SiteURL: result.SiteURL, Status: "preview"}
	if result.Draft != nil && result.Draft.ID == versionID {
		output.PreviewURL = result.Draft.PreviewURL
	} else if currentID(result) == versionID {
		output.Status = "published"
	} else {
		return Result{}, errors.New("上传曾完成，但该版本已被替换或丢弃，请到后台核实；不会重新生成或发布")
	}
	// 保留成功收据，重跑相同命令仍返回同一版本；新文件须使用新的状态文件。
	return output, nil
}

// checkStatus 拒绝服务端异常偏移，客户端只认可已确认的有效字节范围。
func checkStatus(s Status, r resumable.Request) error {
	if s.ID != r.ID || s.Size != r.Size || s.Offset < 0 || s.Offset > s.Size || s.ChunkSize < resumable.MinChunkSize || s.MinChunkSize < 1 || s.MinChunkSize > s.ChunkSize || s.Completed && s.Offset != s.Size {
		return errors.New("服务端上传进度异常，已停止发送")
	}
	return nil
}

// sendChunks 从确认偏移逐片发送，网络失败缩小分片并有限重试，避免无休止自动重传。
func (c *Client) sendChunks(ctx context.Context, f *os.File, r resumable.Request, status *Status, progress io.Writer) error {
	chunkSize := min(int64(512<<10), status.ChunkSize)
	failures := 0
	for {
		if err := checkStatus(*status, r); err != nil {
			return err
		}
		if status.Offset == status.Size {
			return nil
		}
		offset := status.Offset
		data := make([]byte, min(chunkSize, status.Size-offset))
		if _, err := f.ReadAt(data, offset); err != nil {
			return err
		}
		var next Status
		err := c.request(ctx, "PATCH", "uploads/"+r.ID, data, &offset, &next)
		if err == nil {
			if checkStatus(next, r) != nil || next.Offset != offset+int64(len(data)) {
				return errors.New("分片确认偏移异常")
			}
			*status = next
			failures = 0
			if progress != nil {
				fmt.Fprintf(progress, "已确认 %d / %d 字节（%.0f%%）\n", next.Offset, next.Size, float64(next.Offset)*100/float64(next.Size))
			}
			continue
		}
		var api *APIError
		conflict := errors.As(err, &api) && api.Status == 409
		if (!retryable(err) && !conflict) || failures >= 3 {
			return err
		}
		failures++
		delay := time.Duration(1<<uint(failures-1)) * time.Second
		if api != nil {
			delay = max(delay, api.RetryAfter)
		}
		if err = c.Wait(ctx, delay); err != nil {
			return err
		}
		if api == nil || api.Status == 408 || api.Status >= 500 {
			chunkSize = max(status.MinChunkSize, chunkSize/2)
		}
		if err = c.retry(ctx, func() error { return c.request(ctx, "GET", "uploads/"+r.ID, nil, nil, &next) }); err != nil {
			return err
		}
		if checkStatus(next, r) != nil || next.Offset < offset {
			return errors.New("恢复时发现上传进度回退，请到后台核实")
		}
		// 响应丢失但分片已提交时继续；无进展失败仍共享同一重试预算。
		if next.Offset > offset {
			failures = 0
		}
		*status = next
	}
}
