package deployclient

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const appHelp = `StackNest Docker 应用自动部署

  stacknest-deploy app status [--generation N]
  stacknest-deploy app deploy --image 仓库:标签或仓库@sha256:摘要 [--no-wait]
  stacknest-deploy app wait --generation N

使用应用专属 Token：STACKNEST_DEPLOY_TOKEN 或 --token-file；地址：STACKNEST_URL 或 --url。
deploy 更新正式应用，沿用后台已有配置，仅允许同一镜像仓库。推荐使用不可变摘要。
默认等待最多 75 分钟，--timeout 可调整；超时或 Ctrl+C 不会取消已提交任务。
断线后用 app wait --generation N 继续等待。--no-wait 只提交，返回排队回执，不表示成功。
重试未确认的提交可同时指定 --request-id UUID 和 --expected-generation 原基线。
`

// configuredClient 共用凭据文件读取，禁止在命令行参数中直接提供明文 Token。
func configuredClient(base, tokenFile string) (*Client, error) {
	token := strings.TrimSpace(os.Getenv("STACKNEST_DEPLOY_TOKEN"))
	if tokenFile != "" {
		file, err := os.Open(tokenFile)
		if err != nil {
			return nil, errors.New("无法读取 Token 文件")
		}
		data, err := io.ReadAll(io.LimitReader(file, 1025))
		file.Close()
		if err != nil || len(data) > 1024 {
			return nil, errors.New("Token 文件无效或过大")
		}
		token = strings.TrimSpace(string(data))
	}
	return New(base, token)
}

// appMain 提供镜像提交、查询和等待；任何失败都使 CI 返回非零退出码。
func appMain(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, appHelp)
		return 0
	}
	action := args[0]
	fs := flag.NewFlagSet("stacknest-deploy app "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("url", os.Getenv("STACKNEST_URL"), "管理端地址")
	tokenFile := fs.String("token-file", "", "私有 Token 文件")
	var generation *int64
	var image, requestID *string
	var expected *int64
	var noWait *bool
	var timeout *time.Duration
	switch action {
	case "deploy":
		image = fs.String("image", "", "同一仓库的镜像标签或摘要")
		expected = fs.Int64("expected-generation", 0, "原应用代次，默认读取一次当前代次")
		requestID = fs.String("request-id", "", "重试未确认提交的原请求 UUID")
		noWait = fs.Bool("no-wait", false, "只提交，不等待部署完成")
	case "status", "wait":
		generation = fs.Int64("generation", 0, "确切任务代次")
	default:
		fmt.Fprintln(stderr, "未知应用命令；使用 app --help 查看说明")
		return 2
	}
	if action != "status" {
		timeout = fs.Duration("timeout", 75*time.Minute, "等待上限")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "不接受未命名参数")
		return 2
	}
	if generation != nil && (*generation < 0 || action == "wait" && *generation == 0) {
		fmt.Fprintln(stderr, "请提供有效的 --generation")
		return 2
	}
	if action == "deploy" {
		if strings.TrimSpace(*image) == "" || *expected < 0 {
			fmt.Fprintln(stderr, "请提供 --image 和有效基线")
			return 2
		}
		if *requestID != "" {
			parsed, err := uuid.Parse(*requestID)
			if err != nil || parsed.String() != *requestID || *expected < 1 {
				fmt.Fprintln(stderr, "重试须同时提供规范 --request-id 和原 --expected-generation")
				return 2
			}
		}
	}
	if timeout != nil {
		if *timeout < time.Second || *timeout > 24*time.Hour {
			fmt.Fprintln(stderr, "--timeout 须在 1 秒至 24 小时之间")
			return 2
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	c, err := configuredClient(*base, *tokenFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var result Application
	if action == "deploy" {
		result, err = c.DeployApplication(ctx, *image, *expected, *requestID)
		if err == nil {
			fmt.Fprintf(stderr, "已提交应用 %s · 代次 %d；断线可用 app wait --generation %d 继续等待。\n", result.Slug, result.Generation, result.Generation)
			if !*noWait {
				result, err = c.WaitApplication(ctx, result.AppID, result.Generation, stderr)
			}
		}
	} else {
		result, err = c.Application(ctx, *generation)
		if err == nil && action == "wait" {
			result, err = c.WaitApplication(ctx, result.AppID, *generation, stderr)
		}
	}
	if result.AppID != "" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if outputErr := encoder.Encode(result); outputErr != nil {
			fmt.Fprintln(stderr, "无法输出回执，请在后台核实任务")
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, strings.ReplaceAll(err.Error(), c.Token, "[redacted]"))
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "等待已结束，后台任务可能仍在执行；请查询原代次，不要盲目重新部署。")
		}
		return 1
	}
	return 0
}
