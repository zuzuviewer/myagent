// coding-agent 命令行入口。当前进度：M1（单轮模型调用），尚无工具能力。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/myagent/coding-agent/internal/cli"
)

func main() {
	// 把 Ctrl-C / SIGTERM 转成 ctx 取消：取消必须能打断进行中的模型请求（R-15），
	// 而不是等请求跑完才生效。CLI 将其映射为退出码 130。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}
