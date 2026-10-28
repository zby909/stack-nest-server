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

	"stacknest/server/internal/buildinfo"
)

const help = `StackNest 静态项目自动部署

  stacknest-deploy upload --file guide.html [--notes "更新行程"] [--state .stacknest-upload.json]
  stacknest-deploy publish --draft-id dep-... --expected-id dep-...
  stacknest-deploy status

配置：STACKNEST_URL=https://管理端域名
凭据：STACKNEST_DEPLOY_TOKEN，或 --token-file 指向只含 Token 的本机文件。
通用参数：--url 可覆盖管理端地址；不接受命令行明文 Token。
upload 只创建预览。publish 必须明确指定版本；首次发布的 expected-id 填 none。
断线后使用相同文件和参数重跑即可续传。默认按目标和文件生成独立状态文件，新文件自动开始新上传。
标准输出为 JSON，进度写入标准错误；请勿将 Token、状态文件提交到仓库。
`

// Main 统一解析本机和 CI 命令，只有明确 publish 子命令才能正式发布。
func Main(ctx context.Context, args []string, out, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		if err := json.NewEncoder(out).Encode(buildinfo.Current()); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, help)
		return 0
	}
	fs := flag.NewFlagSet("stacknest-deploy "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("url", os.Getenv("STACKNEST_URL"), "StackNest 管理端 URL")
	tokenFile := fs.String("token-file", "", "只含 Token 的凭据文件")
	var file, state, notes, draft, expected *string
	switch args[0] {
	case "upload":
		file = fs.String("file", "", "HTML / HTM / ZIP / RAR 文件")
		state = fs.String("state", "", "续传收据文件，默认按文件和项目自动生成")
		notes = fs.String("notes", "", "更新说明")
	case "publish":
		draft = fs.String("draft-id", "", "确切草稿编号")
		expected = fs.String("expected-id", "", "当前正式版本编号，首次发布填 none")
	case "status":
	default:
		fmt.Fprintln(stderr, "未知命令；使用 --help 查看说明")
		return 2
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
	token := strings.TrimSpace(os.Getenv("STACKNEST_DEPLOY_TOKEN"))
	if *tokenFile != "" {
		f, err := os.Open(*tokenFile)
		if err != nil {
			fmt.Fprintln(stderr, "无法读取 Token 文件")
			return 2
		}
		data, err := io.ReadAll(io.LimitReader(f, 1025))
		f.Close()
		if err != nil || len(data) > 1024 {
			fmt.Fprintln(stderr, "Token 文件无效或过大")
			return 2
		}
		token = strings.TrimSpace(string(data))
	}
	c, err := New(*base, token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var result any
	switch args[0] {
	case "upload":
		if *file == "" {
			fmt.Fprintln(stderr, "必须提供 --file")
			return 2
		}
		result, err = c.Upload(ctx, *file, *state, *notes, stderr)
	case "publish":
		if *draft == "" || *expected == "" {
			fmt.Fprintln(stderr, "必须明确指定 --draft-id 和 --expected-id；首次发布填 none")
			return 2
		}
		if *expected == "none" {
			*expected = ""
		}
		result, err = c.Publish(ctx, *draft, *expected)
	case "status":
		result, err = c.Project(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, strings.ReplaceAll(err.Error(), token, "[redacted]"))
		return 1
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "无法输出部署结果，请重跑相同命令确认")
		return 1
	}
	return 0
}
