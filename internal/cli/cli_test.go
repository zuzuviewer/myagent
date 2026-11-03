package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/myagent/coding-agent/internal/config"
	"github.com/myagent/coding-agent/internal/provider"
)

const testSecret = "sk-cli-secret-DO-NOT-LEAK"

// validConfigBody 刻意不含单价，用于验证"单价未配置"分支。
const validConfigBody = "model: test-model\n" +
	"timeout: 5s\n" +
	"max_context_tokens: 100000\n" +
	"max_output_tokens: 2000\n" +
	"base_url: https://api.example.com\n"

const pricedConfigBody = validConfigBody +
	"currency: CNY\n" +
	"input_price_per_mtok: 2.0\n" +
	"output_price_per_mtok: 8.0\n"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return path
}

// runWith 注入 Provider 跑一次 CLI。真实请求不会发生，因此测试不需要 API Key 之外的任何外部条件。
func runWith(t *testing.T, p provider.Provider, configBody string, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", testSecret)
	full := append([]string{"--config", writeConfig(t, configBody)}, args...)

	var stdout, stderr bytes.Buffer
	opts := &Options{NewProvider: func(*config.Config) provider.Provider { return p }}
	code := Run(context.Background(), full, &stdout, &stderr, opts)
	return stdout.String(), stderr.String(), code
}

func okFake(content string) *provider.Fake {
	return provider.NewFake(provider.ScriptText(content))
}

func TestCLISuccessWritesStdoutOnly(t *testing.T) {
	stdout, stderr, code := runWith(t, okFake("这是模型的回答"), validConfigBody, "总结这个项目")

	if code != ExitOK {
		t.Fatalf("退出码 = %d, 期望 %d (stderr: %s)", code, ExitOK, stderr)
	}
	if stdout != "这是模型的回答\n" {
		t.Errorf("stdout 应只有正文, got %q", stdout)
	}
	// 不带 --debug 时诊断信息不该污染 stderr。
	if strings.Contains(stderr, "[debug]") {
		t.Errorf("默认不应输出 debug: %s", stderr)
	}
}

func TestCLIDebugGoesToStderrAndMasksSecret(t *testing.T) {
	stdout, stderr, code := runWith(t, okFake("回答"), validConfigBody, "--debug", "总结这个项目")
	if code != ExitOK {
		t.Fatalf("退出码 = %d", code)
	}

	for _, want := range []string{"[debug] 配置:", "[debug] 请求:", "[debug] 响应:", "[debug] Token:", "[debug] 成本:"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("debug 输出缺少 %q 段:\n%s", want, stderr)
		}
	}
	// 正文仍然只在 stdout：debug 不能混进可管道化的输出。
	if strings.Contains(stdout, "[debug]") {
		t.Errorf("debug 不应写进 stdout: %s", stdout)
	}
	// §3.4 / §4 红线：调试输出不得记录密钥。
	if strings.Contains(stderr, testSecret) {
		t.Errorf("debug 输出泄露了 API Key:\n%s", stderr)
	}
	if !strings.Contains(stderr, "APIKey: ***") {
		t.Errorf("配置摘要应掩码 APIKey:\n%s", stderr)
	}
	// 未配置单价时必须显式说明，而不是算出 0.00。
	if !strings.Contains(stderr, "单价未配置") {
		t.Errorf("应标注单价未配置:\n%s", stderr)
	}
}

// 成本必须带币种（§3.1）：不带币种的数字在混合端点时毫无意义。
func TestCLIDebugCostCarriesCurrency(t *testing.T) {
	f := provider.NewFake(provider.TextWithUsage("回答", provider.Usage{PromptTokens: 1000, CompletionTokens: 500}))
	_, stderr, code := runWith(t, f, pricedConfigBody, "--debug", "任务")
	if code != ExitOK {
		t.Fatalf("退出码 = %d", code)
	}
	// 1000/1M * 2 + 500/1M * 8 = 0.002 + 0.004 = 0.006
	if !strings.Contains(stderr, "0.006000 CNY") {
		t.Errorf("成本应带币种且计算正确:\n%s", stderr)
	}
	// usage 实测存在时来源标注为 api，不能伪装成估算。
	if !strings.Contains(stderr, "(api)") {
		t.Errorf("有 usage 时应标注来源为 api:\n%s", stderr)
	}
	if !strings.Contains(stderr, "占比") {
		t.Errorf("应输出窗口占比:\n%s", stderr)
	}
}

// usage 缺失时退回估算并标注来源（§3.4）。
func TestCLIDebugMarksEstimatedUsage(t *testing.T) {
	_, stderr, _ := runWith(t, okFake("回答"), validConfigBody, "--debug", "任务")
	if !strings.Contains(stderr, "(estimated)") {
		t.Errorf("usage 缺失时应标注 estimated:\n%s", stderr)
	}
}

// 发给模型的请求内容属于验收范围：系统指令必须明确否认工具能力，
// 否则模型会顺着任务描述编造读取结果。
func TestCLIBuildsRequestWithHonestSystemPrompt(t *testing.T) {
	f := okFake("回答")
	_, _, code := runWith(t, f, validConfigBody, "读取 go.mod 并总结这个项目")
	if code != ExitOK {
		t.Fatalf("退出码 = %d", code)
	}

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("应只调用一次模型, got %d", len(calls))
	}
	req := calls[0]
	if len(req.Messages) != 2 {
		t.Fatalf("应为 system + user 两条消息, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != provider.RoleSystem || req.Messages[1].Role != provider.RoleUser {
		t.Errorf("消息角色顺序错误: %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, "没有可用工具") {
		t.Errorf("系统指令应声明无工具能力: %q", req.Messages[0].Content)
	}
	if req.Messages[1].Content != "读取 go.mod 并总结这个项目" {
		t.Errorf("任务文本应原样传递, got %q", req.Messages[1].Content)
	}
	// max_output_tokens 来自配置，不是硬编码。
	if req.MaxOutputTokens != 2000 {
		t.Errorf("MaxOutputTokens = %d, 期望配置值 2000", req.MaxOutputTokens)
	}
}

func TestCLIUsageErrorsExit2(t *testing.T) {
	t.Run("缺少任务描述", func(t *testing.T) {
		_, stderr, code := runWith(t, okFake("x"), validConfigBody)
		if code != ExitUsageError || !strings.Contains(stderr, "用法") {
			t.Fatalf("应报用法错误, code=%d stderr=%s", code, stderr)
		}
	})
	t.Run("plan 子命令未实现", func(t *testing.T) {
		_, stderr, code := runWith(t, okFake("x"), validConfigBody, "plan", "设计配置模块")
		if code != ExitUsageError || !strings.Contains(stderr, "M4") {
			t.Fatalf("plan 应提示 M4 提供, code=%d stderr=%s", code, stderr)
		}
	})
	t.Run("review 子命令未实现", func(t *testing.T) {
		_, _, code := runWith(t, okFake("x"), validConfigBody, "review", "./")
		if code != ExitUsageError {
			t.Fatalf("review 应返回 2, got %d", code)
		}
	})
	t.Run("未知 flag", func(t *testing.T) {
		_, _, code := runWith(t, okFake("x"), validConfigBody, "--nope", "任务")
		if code != ExitUsageError {
			t.Fatalf("未知 flag 应返回 2, got %d", code)
		}
	})
	t.Run("帮助不是错误", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"-h"}, &stdout, &stderr, nil); code != ExitOK {
			t.Fatalf("-h 应返回 0, got %d", code)
		}
	})
	t.Run("配置错误与参数错误同级", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", testSecret)
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(),
			[]string{"--config", filepath.Join(t.TempDir(), "missing.yml"), "任务"},
			&stdout, &stderr, nil)
		if code != ExitUsageError || !strings.Contains(stderr.String(), "配置错误") {
			t.Fatalf("配置缺失应返回 2, code=%d stderr=%s", code, stderr.String())
		}
	})
}

// --provider 拼错时必须拒绝，而不是静默退回真实 Provider：
// 使用者以为在跑 fake，实际发出了花钱的真实请求。
func TestCLIUnknownProviderNameIsUsageError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", testSecret)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		append([]string{"--config", writeConfig(t, validConfigBody), "--provider", "chatcompletion"}, "任务"),
		&stdout, &stderr, nil)

	if code != ExitUsageError {
		t.Fatalf("未知 provider 名应返回 2, got %d (输出: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "未知的 --provider") {
		t.Errorf("应指名未知取值, got %s", stderr.String())
	}
}

func TestCLIErrorToExitCode(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    int
		wantMsg string
	}{
		{
			name:    "鉴权失败是任务失败",
			err:     &provider.ProviderError{Category: provider.CategoryAuth, Message: "鉴权失败，请检查 OPENAI_API_KEY 是否有效"},
			want:    ExitTaskFailed,
			wantMsg: "auth",
		},
		{
			name:    "取消是 130",
			err:     &provider.ProviderError{Category: provider.CategoryCanceled, Message: "请求已被取消", Err: context.Canceled},
			want:    ExitCanceled,
			wantMsg: "已取消",
		},
		{
			name:    "被包装的裸取消仍判 130",
			err:     fmt.Errorf("模型调用中断: %w", context.Canceled),
			want:    ExitCanceled,
			wantMsg: "已取消",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := provider.NewFake(provider.ScriptError(tc.err))
			_, stderr, code := runWith(t, f, validConfigBody, "任务")
			if code != tc.want {
				t.Errorf("退出码 = %d, 期望 %d (stderr: %s)", code, tc.want, stderr)
			}
			if !strings.Contains(stderr, tc.wantMsg) {
				t.Errorf("stderr 应含 %q, got %s", tc.wantMsg, stderr)
			}
			if strings.Contains(stderr, testSecret) {
				t.Errorf("错误输出泄露密钥: %s", stderr)
			}
		})
	}
}

// 模型有返回不等于任务完成（§3.5）。以下四种收尾都必须以退出码 1 结束，
// 否则脚本化调用会把失败当成成功继续往下走。
func TestCLIIncompleteOutcomesAreNotSuccess(t *testing.T) {
	cases := []struct {
		name     string
		response provider.ModelResponse
		wantMsg  string
	}{
		{
			name:     "空响应",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "   "}, FinishReason: provider.FinishReasonStop},
			wantMsg:  "空响应",
		},
		{
			name:     "输出被截断",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "半句话"}, FinishReason: provider.FinishReasonLength},
			wantMsg:  "截断",
		},
		{
			name:     "内容过滤",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant}, FinishReason: provider.FinishReasonContentFilter},
			wantMsg:  "安全过滤",
		},
		{
			name:     "请求调用工具",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant}, FinishReason: provider.FinishReasonToolCalls},
			wantMsg:  "M2",
		},
		{
			name:     "缺少 finish_reason",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "回答"}},
			wantMsg:  "无法确认",
		},
		{
			name:     "未知 finish_reason",
			response: provider.ModelResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "回答"}, FinishReason: "abort"},
			wantMsg:  "未知",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := provider.NewFake(provider.Script{Response: tc.response})
			stdout, stderr, code := runWith(t, f, validConfigBody, "任务")
			if code != ExitTaskFailed {
				t.Errorf("退出码 = %d, 期望 1 (stderr: %s)", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantMsg) {
				t.Errorf("应给出诊断 %q, got %s", tc.wantMsg, stderr)
			}
			// 正文仍然输出给用户看（截断的内容有诊断价值），但退出码必须说真话。
			if stdout == "" {
				t.Error("部分输出仍应展示，便于判断为何未完成")
			}
		})
	}
}
