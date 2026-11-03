// Package cli 负责参数解析、配置加载、结果渲染与退出码（§3.1）。
//
// 它不直接访问文件系统工具、不启动命令、也不做权限判断。
// M1 阶段还没有 Agent Core 与 Mode Controller，因此这里由 CLI 直接调用 Provider；
// 这是有意保留的临时形态，M2 起该调用改由 Agent Core 组织（见 §7 M1/M2 交付项）。
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/myagent/coding-agent/internal/config"
	"github.com/myagent/coding-agent/internal/provider"
)

// 退出码约定（§3.1，M1 固化）。
const (
	ExitOK         = 0
	ExitTaskFailed = 1
	ExitUsageError = 2
	ExitCanceled   = 130
)

const defaultConfigPath = "configs/config.yml"

// Provider 名称取值。
const (
	providerChat = "chat-completions"
	providerFake = "fake"
)

// systemPrompt 是 M1 的最小系统指令。
// 刻意声明"没有工具"：否则模型会顺着"读取 go.mod"的指令编造读取结果，
// 那正是 §3.5 要求区分"已完成 / 部分完成 / 未完成"要防的情形。
const systemPrompt = "你是一个编程助手。当前是单轮对话，你没有可用工具，" +
	"不能读取文件、搜索代码或执行命令。不要声称做过你没有能力做的事；" +
	"如果信息不足，直接说明缺少什么。"

// Options 允许测试注入 Provider 构造，避免单元测试产生真实模型请求。
type Options struct {
	NewProvider func(c *config.Config) provider.Provider
}

// Run 执行一次 CLI 调用并返回进程退出码。
// stdout 只放模型正文，诊断与调试一律走 stderr，保证 `coding-agent "..." > out.txt` 可用。
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, opts *Options) int {
	fs := flag.NewFlagSet("coding-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)

	configPath := fs.String("config", defaultConfigPath, "配置文件路径")
	debug := fs.Bool("debug", false, "把每轮请求与响应、Token 与窗口占比、成本写入 stderr（脱敏后）")
	providerName := fs.String("provider", providerChat, "Provider 实现：chat-completions 或 fake（本地联调，不产生真实请求）")

	if err := fs.Parse(args); err != nil {
		// -h/-help 由 flag 自己打印用法，属正常路径。
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsageError
	}

	rest := fs.Args()
	if len(rest) > 0 {
		switch strings.ToLower(rest[0]) {
		case "plan", "review":
			fmt.Fprintf(stderr, "coding-agent: %s 模式在 M4 提供，当前阶段请直接给出任务描述\n", rest[0])
			return ExitUsageError
		}
	}

	task := strings.TrimSpace(strings.Join(rest, " "))
	if task == "" {
		fmt.Fprintln(stderr, `用法: coding-agent [--debug] [--config 路径] [--provider chat-completions|fake] "任务描述"`)
		return ExitUsageError
	}

	cfg, err := config.NewConfig(*configPath)
	if err != nil {
		// 配置错误与参数错误同属"还没开始干活就失败"，退出码 2。
		fmt.Fprintf(stderr, "coding-agent: 配置错误: %v\n", err)
		return ExitUsageError
	}

	prov, code := buildProvider(*providerName, cfg, task, opts, stderr)
	if code != ExitOK {
		return code
	}

	req := buildRequest(task, cfg.MaxOutputTokens)
	if *debug {
		// Redact 保证密钥不外泄（§3.4 / §4：调试输出不得记录密钥）。
		fmt.Fprintf(stderr, "[debug] 配置: %s\n", cfg.Redact())
	}

	start := time.Now()
	resp, err := prov.Generate(ctx, req)
	elapsed := time.Since(start)
	if err != nil {
		return reportFailure(stderr, err)
	}

	fmt.Fprintln(stdout, strings.TrimSpace(resp.Message.Content))
	if *debug {
		writeDebug(stderr, req, resp, cfg, elapsed)
	}

	code, note := evaluateOutcome(resp)
	if note != "" {
		fmt.Fprintf(stderr, "coding-agent: %s\n", note)
	}
	return code
}

func buildProvider(name string, cfg *config.Config, task string, opts *Options, stderr io.Writer) (provider.Provider, int) {
	if opts != nil && opts.NewProvider != nil {
		return opts.NewProvider(cfg), ExitOK
	}
	switch name {
	case providerChat:
		return provider.NewChatCompletions(cfg), ExitOK
	case providerFake:
		return provider.FakeCannedReply(task), ExitOK
	default:
		// 未知的 Provider 名属参数错误，不能默默退回默认实现：
		// 拼错 --provider 的人很可能以为自己在跑 fake，结果发出真请求。
		fmt.Fprintf(stderr, "coding-agent: 未知的 --provider %q，可选值：%s / %s\n", name, providerChat, providerFake)
		return nil, ExitUsageError
	}
}

func buildRequest(task string, maxOutputTokens int) provider.ModelRequest {
	return provider.ModelRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: systemPrompt},
			{Role: provider.RoleUser, Content: task},
		},
		MaxOutputTokens: maxOutputTokens,
	}
}

// evaluateOutcome 把"模型有返回"与"任务完成"分开判定（§3.5）。
// 模型声称成功、或生成被截断后看起来像一段完整的话，都不构成成功依据。
func evaluateOutcome(resp provider.ModelResponse) (int, string) {
	switch resp.FinishReason {
	case "":
		return ExitTaskFailed, "响应缺少 finish_reason，无法确认本次生成完整"
	case provider.FinishReasonStop:
		if strings.TrimSpace(resp.Message.Content) == "" {
			return ExitTaskFailed, "模型返回空响应，不视为成功"
		}
		return ExitOK, ""
	case provider.FinishReasonLength:
		return ExitTaskFailed, "输出被 max_output_tokens 截断，本次生成未完成"
	case provider.FinishReasonContentFilter:
		return ExitTaskFailed, "内容被安全过滤中断，本次生成未完成"
	case provider.FinishReasonToolCalls:
		return ExitTaskFailed, "模型请求调用工具，而 M1 阶段没有工具执行能力（M2 提供）"
	default:
		return ExitTaskFailed, fmt.Sprintf("未知的 finish_reason %q，无法判定任务状态", resp.FinishReason)
	}
}

func reportFailure(w io.Writer, err error) int {
	code := ExitTaskFailed
	if errors.Is(err, context.Canceled) {
		// Ctrl-C：用户主动中止，与"任务失败"必须是不同的退出码。
		code = ExitCanceled
	}
	// ProviderError.Error() 已包含分类、状态码与被包装的原因，且实现层保证不含密钥
	// （openai_test.go 有断言）。这里不再逐项拆解，避免漏掉诊断信息。
	fmt.Fprintf(w, "coding-agent: %v\n", err)
	if code == ExitCanceled {
		fmt.Fprintln(w, "coding-agent: 已取消")
	}
	return code
}

func writeDebug(w io.Writer, req provider.ModelRequest, resp provider.ModelResponse, cfg *config.Config, elapsed time.Duration) {
	fmt.Fprintf(w, "[debug] 请求: model=%s messages=%d max_output_tokens=%d\n",
		cfg.Model, len(req.Messages), req.MaxOutputTokens)
	for i, m := range req.Messages {
		fmt.Fprintf(w, "[debug]   <- [%d] %s: %s\n", i, m.Role, m.Content)
	}

	fmt.Fprintf(w, "[debug] 响应: finish_reason=%s tool_calls=%d 耗时=%s\n",
		resp.FinishReason, len(resp.Message.ToolCalls), elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "[debug]   -> %s\n", resp.Message.Content)

	writeTokenDebug(w, req, resp, cfg)
}

func writeTokenDebug(w io.Writer, req provider.ModelRequest, resp provider.ModelResponse, cfg *config.Config) {
	prompt, completion := resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	source := "api"
	if prompt == 0 && completion == 0 {
		// 端点未回 usage：退回保守估算，并**标注来源**，
		// 不把估算值伪装成实测数字（§3.4）。
		source = "estimated"
		prompt, completion = estimateTokens(flatten(req)), estimateTokens(resp.Message.Content)
	}

	share := 0.0
	if cfg.MaxContextTokens > 0 {
		// 窗口约束的是 input 与 output 之和（§3.6）。
		share = float64(prompt+completion) / float64(cfg.MaxContextTokens) * 100
	}
	fmt.Fprintf(w, "[debug] Token: prompt=%d completion=%d (%s) 窗口=%d 占比=%.3f%%\n",
		prompt, completion, source, cfg.MaxContextTokens, share)

	fmt.Fprintf(w, "[debug] 成本: %s\n", formatCost(cfg, prompt, completion))
}

// formatCost 输出带币种的成本估算；单价未配置时明确说"不可估算"，
// 绝不输出 "0.00 CNY" 这种看起来精确的假数（§3.1）。
func formatCost(cfg *config.Config, promptTokens, completionTokens int) string {
	if !cfg.PriceConfigured() {
		return "单价未配置，无法估算成本"
	}
	in := float64(promptTokens) / 1e6 * cfg.InputPricePerMTok
	out := float64(completionTokens) / 1e6 * cfg.OutputPricePerMTok
	return fmt.Sprintf("%.6f %s（prompt %.6f + completion %.6f，按最贵一档单价估算）",
		in+out, cfg.Currency, in, out)
}

// estimateTokens 是 M1 的临时估算，仅服务于 debug 的窗口占比展示。
// 正式实现属 Context Manager（§3.6），M3 落地时迁移过去并按"宁可高估"的原则加测试。
// 非 ASCII 按每字 1 token 计（中文常见 tokenizer 接近或高于此值），ASCII 按 4 字符 1 token。
func estimateTokens(s string) int {
	var ascii, other int
	for _, r := range s {
		if r < 0x80 {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + other + 1
}

func flatten(req provider.ModelRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
	}
	return b.String()
}
