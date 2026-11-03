package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/myagent/coding-agent/internal/config"
)

const (
	// chatCompletionsPath 由 Provider 拼接：配置里的 BaseURL 只到"路径之前的部分"
	// （OpenAI 含 /v1，DeepSeek 官方基址不含，两者都符合这一约定）。
	chatCompletionsPath = "/chat/completions"

	// maxResponseBytes 限制单次响应体。正常 Chat Completions 响应远小于 4 MiB，
	// 超出即异常（网关返回整页 HTML、失控输出等）。超限**报错而不是截断**：
	// 截断后的 JSON 解析失败会把"响应过大"伪装成"协议不合法"，误导排查（R-17）。
	maxResponseBytes = 4 << 20

	// maxAttempts 是总尝试次数（含首次）。§3.4 要求"有限重试"，且工具执行
	// 永远不能因为模型请求重试而重复执行——重试范围严格限制在本函数内。
	maxAttempts = 3

	defaultBackoff = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second

	// maxRetryAfter 给服务端的 Retry-After 设上限：过长等待会静默吃掉任务预算，
	// 用户只看到"卡住"，不如按超时失败并把状态码交出去。
	maxRetryAfter = 30 * time.Second

	// maxAPIMessageRunes 限制从错误响应体里带出的文本长度。错误信息会进日志与
	// 终端，不封顶等于把任意大小的响应体搬到展示层。
	maxAPIMessageRunes = 200
)

// errResponseTooLarge 单独可判定，便于和"读取失败"给出不同的诊断信息。
var errResponseTooLarge = errors.New("响应体超过上限")

// ChatCompletions 是 OpenAI Chat Completions 协议的 Provider 实现。
// 按协议命名而不是按厂商命名：§1.2 路线里后续还要接入 OpenAI Responses API，
// 那同样是 OpenAI，两者不能都叫 OpenAI。
type ChatCompletions struct {
	client *http.Client
	// apiKey 只能进请求头，不得进入错误信息、日志或返回值（§3.4 / §4）。
	apiKey  string
	apiBase string
	model   string
	timeout time.Duration
	// backoff 可注入，避免单元测试为了退避等待真睡 500ms。
	backoff time.Duration
}

// NewChatCompletions 从已校验的配置构造 Provider。
// 不读环境变量、不重复校验：config 包是配置的唯一校验边界（§3.1）。
func NewChatCompletions(c *config.Config) *ChatCompletions {
	// Clone 而不是从零构造 Transport：DefaultTransport 带着代理环境变量支持、
	// Dial 与 TLS 握手超时等必要默认值，手写会把它们全丢掉。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.IdleConnTimeout = 60 * time.Second

	return &ChatCompletions{
		// 故意不设 http.Client.Timeout：它不响应 context 取消，会让 Ctrl-C
		// 等满整个超时。超时统一走 request context（见 do，R-15）。
		client:  &http.Client{Transport: transport},
		apiKey:  c.APIKey,
		apiBase: c.BaseURL,
		model:   c.Model,
		timeout: c.Timeout,
		backoff: defaultBackoff,
	}
}

// apiResult 是一次 HTTP 往来的**完整结果**。响应体在 do 内部就被读尽并关闭，
// 这样请求级 context 的 cancel 可以安全 defer——若把未读完的 Body 交给调用方，
// cancel 会在读取过程中中断它，表现为莫名的 "read: context canceled"。
type apiResult struct {
	statusCode int
	body       []byte
	retryAfter time.Duration
}

// Generate implements [Provider].
//
// 行为约定：
//   - 每次尝试都有独立的请求级超时（配置项 timeout），外层 ctx 取消可立刻中断；
//   - 只对 CategoryNetwork / CategoryRateLimit / CategoryServer 重试，最多 maxAttempts 次；
//     鉴权失败、4xx 请求非法、响应解析失败、超时与取消一律不重试；
//   - 失败返回 *ProviderError，调用方用 errors.As 取分类、用 errors.Is(err, context.Canceled)
//     判定取消（CLI 退出码 130 依赖它）；
//   - 不判定"任务是否成功"：finish_reason 为 length / content_filter 时仍正常返回，
//     由 Agent Loop 按 §3.5 的错误与预算策略处理。
func (o *ChatCompletions) Generate(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	endpoint, err := url.JoinPath(o.apiBase, chatCompletionsPath)
	if err != nil {
		return ModelResponse{}, &ProviderError{
			Category: CategoryRequest,
			Message:  fmt.Sprintf("API 地址无法解析：%q", o.apiBase),
			Err:      err,
		}
	}

	payload, err := toOpenAIRequest(o.model, req)
	if err != nil {
		return ModelResponse{}, &ProviderError{Category: CategoryRequest, Message: "请求构造失败", Err: err}
	}
	// 先序列化成 []byte 而不是直接把 Reader 交给请求：重试必须能重放同一份 body
	// （body Reader 一旦被消费就不可复用）。
	body, err := json.Marshal(payload)
	if err != nil {
		return ModelResponse{}, &ProviderError{Category: CategoryRequest, Message: "请求序列化失败", Err: err}
	}

	for attempt := 1; ; attempt++ {
		res, err := o.do(ctx, endpoint, body)
		if err != nil {
			if isRetryable(err) && attempt < maxAttempts {
				if werr := o.wait(ctx, attempt, 0); werr != nil {
					return ModelResponse{}, werr
				}
				continue
			}
			return ModelResponse{}, err
		}

		// 只接受 200：其他 2xx（如 204 空响应）没有可用 body，按协议错误处理比
		// 让 json.Unmarshal 抛出一个看不懂的错更有诊断价值。
		if res.statusCode != http.StatusOK {
			pe := errorFromResponse(res)
			if pe.Retryable && attempt < maxAttempts {
				if werr := o.wait(ctx, attempt, res.retryAfter); werr != nil {
					return ModelResponse{}, werr
				}
				continue
			}
			return ModelResponse{}, pe
		}

		var wire openaiResponse
		if err := json.Unmarshal(res.body, &wire); err != nil {
			return ModelResponse{}, &ProviderError{
				Category:   CategoryParse,
				StatusCode: res.statusCode,
				Message:    "响应不是合法 JSON",
				Err:        err,
			}
		}
		out, err := toModelResponse(wire)
		if err != nil {
			return ModelResponse{}, &ProviderError{
				Category:   CategoryParse,
				StatusCode: res.statusCode,
				Message:    "响应结构不符合协议",
				Err:        err,
			}
		}
		return out, nil
	}
}

// do 执行一次 HTTP 往来：构造请求、发送、限量读取并关闭响应体。
func (o *ChatCompletions) do(ctx context.Context, endpoint string, body []byte) (*apiResult, error) {
	rctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(rctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &ProviderError{Category: CategoryRequest, Message: "构造 HTTP 请求失败", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, o.classifyTransportError(ctx, err)
	}
	defer resp.Body.Close()

	raw, err := readCapped(resp.Body)
	if err != nil {
		msg := "读取响应体失败"
		if errors.Is(err, errResponseTooLarge) {
			msg = fmt.Sprintf("响应体超过 %d 字节上限", maxResponseBytes)
		}
		return nil, &ProviderError{Category: CategoryParse, StatusCode: resp.StatusCode, Message: msg, Err: err}
	}

	return &apiResult{
		statusCode: resp.StatusCode,
		body:       raw,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}, nil
}

// classifyTransportError 区分"用户的取消"、"我们的单次超时"和"传输层故障"。
// 三者的处置完全不同，混为一谈会把取消当成网络抖动去重试。
func (o *ChatCompletions) classifyTransportError(ctx context.Context, err error) error {
	// 外层 ctx 优先：取消意图不能被"可重试的网络错误"吞掉。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &ProviderError{Category: CategoryCanceled, Message: "请求已被取消", Err: ctxErr}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// 单次请求超时不重试：重放同一份上下文等于把同样的钱再花一次，
		// 还会静默放大 §3.4 的成本与 §7 的时间预算。
		return &ProviderError{
			Category: CategoryTimeout,
			Message:  fmt.Sprintf("模型请求超时（%s）", o.timeout),
			Err:      err,
		}
	}
	if errors.Is(err, context.Canceled) {
		return &ProviderError{Category: CategoryCanceled, Message: "请求已被取消", Err: err}
	}
	// 其余为传输层故障（DNS、连接被拒、TLS、连接中断），重试有意义。
	return &ProviderError{Category: CategoryNetwork, Retryable: true, Message: "网络请求失败", Err: err}
}

// errorFromResponse 把非 200 响应转成分类错误，并尽力带出服务端的 error.message。
// 状态码本身不足以诊断："额度不足"、"模型名不存在"、"上下文超限"只在 body 里。
func errorFromResponse(res *apiResult) *ProviderError {
	base := fmt.Sprintf("API 返回状态码 %d", res.statusCode)
	if msg := extractAPIMessage(res.body); msg != "" {
		base += "：" + msg
	}

	pe := &ProviderError{StatusCode: res.statusCode, Message: base}
	switch {
	case res.statusCode == http.StatusUnauthorized || res.statusCode == http.StatusForbidden:
		pe.Category = CategoryAuth
		// 鉴权失败重试不会变好，属任务级终止（§3.5）；提示里绝不出现密钥本身。
		pe.Message += "（鉴权失败，请检查 OPENAI_API_KEY 是否有效）"
	case res.statusCode == http.StatusTooManyRequests:
		pe.Category = CategoryRateLimit
		pe.Retryable = true
	case res.statusCode >= http.StatusInternalServerError:
		pe.Category = CategoryServer
		pe.Retryable = true
	default:
		pe.Category = CategoryRequest
	}
	return pe
}

// extractAPIMessage 尽力解析 {"error":{...}} 并截断到可控长度。
// 兼容网关不一定返回这个结构（HTML、纯文本都可能），所以解析失败时退回文本片段，
// 但**不能因为解析失败就丢掉状态码这个确定信息**。
func extractAPIMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var env openaiErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && strings.TrimSpace(env.Error.Message) != "" {
		return clipRunes(env.Error.Message, maxAPIMessageRunes)
	}
	// 非预期结构：取可打印文本片段兜底（body 已在 do 里限长，不会失控）。
	return clipRunes(strings.TrimSpace(string(body)), maxAPIMessageRunes)
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…（已截断）"
}

// isRetryable 判定传输层错误是否值得重试。
func isRetryable(err error) bool {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return false
}

// wait 在两次尝试之间退避。Retry-After 优先，缺失时指数退避。
// 等待必须是可取消的——用 time.Sleep 会让 Ctrl-C 等满整个退避时间（R-15）。
func (o *ChatCompletions) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	d := retryAfter
	if d <= 0 {
		d = o.backoff << (attempt - 1)
		if d > maxBackoff {
			d = maxBackoff
		}
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return &ProviderError{Category: CategoryCanceled, Message: "等待重试时被取消", Err: ctx.Err()}
	case <-timer.C:
		return nil
	}
}

// parseRetryAfter 支持 429/503 的两种合法写法：秒数与 HTTP-date。
// 解析不出就返回 0（退回指数退避），绝不因为头部格式奇怪而失败整个请求。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return clampDuration(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return clampDuration(d)
		}
		return 0
	}
	return 0
}

func clampDuration(d time.Duration) time.Duration {
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// readCapped 读取响应体。多读 1 字节用来判定"是否超限"，
// 这样超限能被明确检出，而不是把截断后的残缺内容当成合法响应。
func readCapped(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w（%d 字节，响应实际更大）", errResponseTooLarge, maxResponseBytes)
	}
	return raw, nil
}

var _ Provider = (*ChatCompletions)(nil)
