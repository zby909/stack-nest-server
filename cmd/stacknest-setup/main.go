package main

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"
	"stacknest/server/internal/setup"
)

// main 只接受交互式终端，避免密码进入命令行参数、重定向日志或终端回显。
func main() {
	dir := flag.String("dir", ".", "包含 .env.compose.example 的部署目录")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "不接受额外参数；使用 --help 查看用法")
		os.Exit(1)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "请在交互式终端运行，或按照配置参考手动创建 .env")
		os.Exit(1)
	}
	err := setup.Run(*dir, os.Stdin, os.Stdout, func() (string, error) {
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		return string(value), err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
