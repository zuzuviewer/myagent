package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/myagent/coding-agent/internal/config"
)

const testAPIKey = "sk-test-secret-DO-NOT-LEAK"

// newTestProvider 直接构造，绕开 config 校验（httptest 是 http:// 而非 https://），
// 并把 backoff 压到 1ms，免得测试为退避真的等待。
func newTestProvider(srv *httptest.Server, timeout time.Duration) *ChatCompletions {
	return &ChatCompletions{
		client:  srv.Client(),
		apiKey:  testAPIKey,
		apiBase: srv.URL,
		model:   "test-model",
		timeout: timeout,
		backoff: time.Millisecond,
	}
}

func textRequest() ModelRequest {
	return ModelRequest{
		Messages:        []Message{{Role: RoleUser, Content: "读取 go.mod"}},
		MaxOutputTokens: 128,
	}
}

// okBody 是一个最小合法响应。
const okBody = `{"id":"c1","object":"chat.completion","model":"test-model",
"choices":[{"index":0,"message":{"role":"assistant","content":"完成"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`

// asProviderError 取出分类，取不出即视为测试失败。
func asProviderError(t *testing.T, err error) *ProviderError {
	t.Helper()
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("错误应为 *ProviderError 以便上层分类处理, got %T: %v", err, err)
	}
	return pe
}

func TestGenerateSuccess(t *testing.T) {
	var hits int32
	var gotPath, gotAuth, gotContentType, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		gotBody = string(body[:n])
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	got, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	if err != nil {
		t.Fatalf("合法请求应成功: %v", err)
	}
	if hits != 1 {
		t.Errorf("成功路径应只请求 1 次, got %d", hits)
	}
	if got.Message.Content != "完成" || got.Message.Role != RoleAssistant {
		t.Errorf("消息转换错误: %+v", got.Message)
	}
	if got.FinishReason != FinishReasonStop {
		t.Errorf("FinishReason = %q, 期望 stop", got.FinishReason)
	}
	if got.Usage.PromptTokens != 11 || got.Usage.CompletionTokens != 7 {
		t.Errorf("Usage 错误: %+v", got.Usage)
	}
	// 路径、鉴权与请求体形状属于协议契约，必须逐条核对。
	if gotPath != "/chat/completions" {
		t.Errorf("请求路径 = %q, 期望 /chat/completions", gotPath)
	}
	if gotAuth != "Bearer "+testAPIKey {
		t.Errorf("Authorization 头错误: %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type 错误: %q", gotContentType)
	}
	if !strings.Contains(gotBody, `"model":"test-model"`) || !strings.Contains(gotBody, `"parallel_tool_calls":false`) {
		t.Errorf("请求体缺少必需字段: %s", gotBody)
	}
}

// 重试必须能重放同一份 body：body Reader 被消费后不可复用，
// 这是"先 Marshal 成 []byte 再每次重新构造请求"的唯一理由。
func TestGenerateRetriesRateLimitReplayingBody(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"限流","type":"rate_limit_error"}}`, http.StatusTooManyRequests)
			return
		}
		// 第二次尝试必须拿到完整可解析的请求体，否则重试实现就是坏的。
		var probe openaiRequest
		if err := json.NewDecoder(r.Body).Decode(&probe); err != nil {
			t.Errorf("重试的请求体不可解析（body 未重放）: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if probe.Model != "test-model" || len(probe.Messages) != 1 {
			t.Errorf("重试请求体内容错误: %+v", probe)
		}
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	if _, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest()); err != nil {
		t.Fatalf("429 后重试应成功: %v", err)
	}
	if hits != 2 {
		t.Errorf("应重试 1 次共 2 次请求, got %d", hits)
	}
}

func TestGenerateRetriesServerError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	if _, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest()); err != nil {
		t.Fatalf("5xx 重试后应成功: %v", err)
	}
	if hits != maxAttempts {
		t.Errorf("尝试次数 = %d, 期望 %d", hits, maxAttempts)
	}
}

func TestGenerateStopsAfterMaxAttempts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if pe.Category != CategoryServer || !pe.Retryable {
		t.Errorf("5xx 应分类为 server 且标记可重试, got %+v", pe)
	}
	if pe.StatusCode != http.StatusInternalServerError {
		t.Errorf("应保留状态码, got %d", pe.StatusCode)
	}
	if hits != maxAttempts {
		t.Errorf("重试必须有限, 尝试 %d 次（上限 %d）", hits, maxAttempts)
	}
}

// 鉴权失败重试不会变好，必须一次即终止（§3.5 任务级终止条件）。
func TestGenerateAuthFailureNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if pe.Category != CategoryAuth || pe.Retryable {
		t.Errorf("401 应分类为 auth 且不可重试, got %+v", pe)
	}
	if hits != 1 {
		t.Errorf("鉴权失败不应重试, 尝试 %d 次", hits)
	}
}

// 错误信息要能用，但不能泄密：带出服务端 message，同时不含密钥。
func TestGenerateErrorMessageUsefulAndSecretFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"The model `+`deepseek-xxx`+` does not exist"}}`)
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	if err == nil {
		t.Fatal("400 应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not exist") {
		t.Errorf("应带出服务端诊断信息, got %q", msg)
	}
	if !strings.Contains(msg, "400") {
		t.Errorf("应带出状态码兜底, got %q", msg)
	}
	if strings.Contains(msg, testAPIKey) {
		t.Errorf("错误信息泄露了 API Key: %s", msg)
	}
	if pe := asProviderError(t, err); pe.Category != CategoryRequest || pe.Retryable {
		t.Errorf("其他 4xx 应分类为 request 且不可重试, got %+v", pe)
	}
}

// 网关返回非预期结构（HTML 错误页）时，诊断信息仍要可用。
func TestGenerateNonJSONErrorBodyFallsBackToStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "<html>502 Bad Gateway</html>")
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if pe.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("应保留状态码, got %+v", pe)
	}
	if pe.Category != CategoryServer {
		t.Errorf("503 应分类为 server, got %q", pe.Category)
	}
}

func TestGenerateParseErrorCategories(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantPart string
	}{
		{"200 但非 JSON", `not json at all`, "响应不是合法 JSON"},
		{"200 但 choices 为空", `{"choices":[]}`, "响应结构不符合协议"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
			pe := asProviderError(t, err)
			if pe.Category != CategoryParse {
				t.Errorf("应分类为 parse, got %q", pe.Category)
			}
			if !strings.Contains(err.Error(), tc.wantPart) {
				t.Errorf("错误应含 %q, got %v", tc.wantPart, err)
			}
		})
	}
}

func TestGenerateTimeoutNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 30*time.Millisecond).Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if pe.Category != CategoryTimeout {
		t.Errorf("超时应分类为 timeout, got %q (%v)", pe.Category, err)
	}
	if pe.Retryable {
		t.Error("单次请求超时不应重试：重放同一上下文等于重复花钱并放大预算")
	}
	if hits != 1 {
		t.Errorf("超时不应重试, 尝试 %d 次", hits)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("错误链应可被 errors.Is(DeadlineExceeded) 判定, got %v", err)
	}
}

// 用户取消必须能打断进行中的模型请求（R-15），并以 context.Canceled 暴露给 CLI（退出码 130）。
func TestGenerateHonorsCallerCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, okBody)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(30*time.Millisecond, cancel)

	_, err := newTestProvider(srv, 5*time.Second).Generate(ctx, textRequest())
	pe := asProviderError(t, err)
	if pe.Category != CategoryCanceled {
		t.Errorf("应分类为 canceled, got %q (%v)", pe.Category, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CLI 依赖 errors.Is(err, context.Canceled) 返回退出码 130, got %v", err)
	}
}

func TestGenerateRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer srv.Close()

	_, err := newTestProvider(srv, 2*time.Second).Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if !strings.Contains(err.Error(), "上限") {
		t.Errorf("应报响应体超限而不是伪装成解析失败, got %v", err)
	}
	if pe.Category != CategoryParse {
		t.Errorf("超限应归为 parse 类, got %q", pe.Category)
	}
}

// Provider 必须从已校验的配置派生，并保留两个关键行为：
// Client.Timeout 为 0（取消能打断）与 Transport 继承 DefaultTransport（代理环境变量不丢）。
func TestNewChatCompletionsDerivesFromConfig(t *testing.T) {
	c := &config.Config{
		APIKey:  testAPIKey,
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-flash",
		Timeout: 42 * time.Second,
	}
	p := NewChatCompletions(c)

	if p.apiKey != testAPIKey || p.apiBase != "https://api.deepseek.com" || p.model != "deepseek-flash" {
		t.Errorf("配置未正确派生: %+v", p)
	}
	if p.timeout != 42*time.Second {
		t.Errorf("timeout = %v, 期望 42s（Generate 的每请求超时依赖它）", p.timeout)
	}
	if p.client.Timeout != 0 {
		t.Errorf("http.Client.Timeout 必须为 0，超时走 request context, got %v", p.client.Timeout)
	}
	if p.backoff <= 0 {
		t.Error("backoff 未初始化，退避等待会立即返回")
	}
	tr, ok := p.client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Error("Transport 应继承 DefaultTransport，否则代理环境变量与连接阶段超时会全部丢失")
	}
}

// 传输层故障（连接被拒）与取消/超时性质不同，它是唯一应该重试的非 HTTP 错误。
func TestGenerateNetworkErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close() // 关闭后请求只会得到 connection refused

	p := &ChatCompletions{
		client:  &http.Client{},
		apiKey:  testAPIKey,
		apiBase: deadURL,
		model:   "test-model",
		timeout: 2 * time.Second,
		backoff: time.Millisecond,
	}

	_, err := p.Generate(context.Background(), textRequest())
	pe := asProviderError(t, err)
	if pe.Category != CategoryNetwork || !pe.Retryable {
		t.Errorf("传输层故障应分类为 network 且可重试, got %+v", pe)
	}
	if pe.StatusCode != 0 {
		t.Errorf("没有 HTTP 响应时 StatusCode 应为 0, got %d", pe.StatusCode)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("错误信息不应包含密钥: %s", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-5", 0},
		{"3", 3 * time.Second},
		{"999999", maxRetryAfter}, // 过长等待被夹到上限
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("Retry-After %q => %v, 期望 %v", tc.in, got, tc.want)
		}
	}
	// HTTP-date 写法：过去的时间等于"立刻可试"。
	if got := parseRetryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("过期的 HTTP-date 应为 0, got %v", got)
	}
}

func TestClipRunesKeepsShortText(t *testing.T) {
	if got := clipRunes("短文本", 10); got != "短文本" {
		t.Errorf("未超限时不应改动, got %q", got)
	}
	long := strings.Repeat("很长的错误信息", 100)
	got := clipRunes(long, 20)
	if !strings.HasSuffix(got, "…（已截断）") {
		t.Errorf("超限应带截断标记, got %q", got)
	}
	if len([]rune(got)) > 20+len([]rune("…（已截断）")) {
		t.Errorf("截断未生效, rune 数 %d", len([]rune(got)))
	}
}
