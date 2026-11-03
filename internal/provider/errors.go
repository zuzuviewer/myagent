package provider

import "fmt"

// ErrorCategory 是 Provider 层的错误分类，对应 §4 中属于模型交互的部分。
// 上层用分类决定"终止还是重试"，CLI 用它映射退出码。
type ErrorCategory string

const (
	CategoryNetwork   ErrorCategory = "network"    // DNS/连接/TLS/中断，可重试
	CategoryTimeout   ErrorCategory = "timeout"    // 单次请求超时，不重试
	CategoryCanceled  ErrorCategory = "canceled"   // 用户取消，不重试
	CategoryAuth      ErrorCategory = "auth"       // 401/403，任务级终止
	CategoryRateLimit ErrorCategory = "rate_limit" // 429，尊重 Retry-After 后重试
	CategoryServer    ErrorCategory = "server"     // 5xx，可重试
	CategoryRequest   ErrorCategory = "request"    // 其他 4xx 与本地构造失败，重试无意义
	CategoryParse     ErrorCategory = "parse"      // 200 但响应不合法
)

// ProviderError 是带分类的 Provider 错误。
//
// 判定方式：用 errors.As 取分类，用 errors.Is(err, context.Canceled) 判定取消
// （CLI 依赖它返回退出码 130）；**不要比较错误字符串**。
//
// Message 面向诊断，绝不含 API Key 或其他密钥（§3.4 / §4）。
type ProviderError struct {
	Category   ErrorCategory
	StatusCode int  // 没有 HTTP 响应时为 0
	Retryable  bool // Provider 内部已按此字段重试；false 表示上层也不应重发同一请求
	Message    string
	Err        error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("provider: %s [%s]: %v", e.Message, e.Category, e.Err)
	}
	return fmt.Sprintf("provider: %s [%s]", e.Message, e.Category)
}

// Unwrap 保留错误链，使 errors.Is / errors.As 能穿透到 context.Canceled 等原因。
func (e *ProviderError) Unwrap() error { return e.Err }
