package provider

import "encoding/json"

// 本文件是 OpenAI Chat Completions 的**线路格式（wire format）**定义：
// 字段名与 JSON tag 一一对应 API 的实际请求/响应体，只在 openai.go 内部使用。
//
// 抽象边界（TECHNICAL_DESIGN §3.4）：这些类型**不得离开 provider 包**，
// Agent Core 只认识 types.go 里的内部协议类型。本文件里的字段命名再丑、
// 再有历史包袱（function vs tool），也只能在这里被消化掉，不能外溢。

// ---------------------------------------------------------------- 请求侧 ----

// openaiRequest 是 POST {base}/chat/completions 的请求体。
type openaiRequest struct {
	Model    string             `json:"model"`
	Messages []openaiRequestMsg `json:"messages"`
	Tools    []openaiTool       `json:"tools,omitempty"`

	// MaxTokens 限制本次生成的输出长度。注意历史包袱：OpenAI 新一代推理模型
	// 要求改用 max_completion_tokens，而 DeepSeek 等兼容端点仍用 max_tokens。
	// 当前 MVP 的目标端点用 max_tokens；将来切 OpenAI 新模型时在这里加字段，
	// 内部类型 ModelRequest.MaxOutputTokens 不变。
	MaxTokens int `json:"max_tokens,omitempty"`

	// ParallelToolCalls 显式发送 false（§3.4：MVP 串行执行工具调用）。
	// 因此**不加 omitempty**——false 是我们要表达的语义，省略掉等于让服务端用默认值。
	ParallelToolCalls bool `json:"parallel_tool_calls"`

	// Stream 显式发送 false。流式（SSE）属 §8 后续增强，同样不加 omitempty。
	Stream bool `json:"stream"`
}

// openaiRequestMsg 是请求中的消息。字段合法性受 role 约束（§3.4）：
// tool_calls 只在 assistant 出现，tool_call_id 只在 tool 出现。
//
// Content 用 string 而非 *string：请求侧四种 role 都必须带 content 字段，
// 缺失会让部分兼容网关报错，而空字符串是所有端点都接受的写法。
type openaiRequestMsg struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openaiTool 描述一个可供模型调用的工具。Parameters 是 JSON Schema，
// 由 Registry 的 ToolDefinition.Parameters 直接搬运（§3.7）。
//
// 这里只有 Name/Description/Parameters 三项，**不含 Risk / Capability**：
// 策略元数据进入提示词等于告诉模型"哪个工具权限更宽"（R-09）。
type openaiTool struct {
	Type     string             `json:"type"` // 目前只有 "function"
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ------------------------------------------------------ 请求/响应共用类型 ----

// openaiToolCall 表示模型发起的一次工具调用。
//
// 关键坑：Function.Arguments 是**一段被当作字符串装起来的 JSON**（形如
// "{\"path\":\"go.mod\"}"），不是 JSON 对象。转换到内部 ToolCall.Arguments
// （json.RawMessage）时是"直接取字符串的字节"，**不要再 Marshal 一次**——
// 双重编码会得到 "\"{\\\"path\\\"...}\""，Executor 侧解析必然失败。
type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // 目前只有 "function"
	Function openaiFunctionCall `json:"function"`
}

type openaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ---------------------------------------------------------------- 响应侧 ----

// openaiResponse 是 Chat Completions 的响应体。
// 只声明我们用得到的字段；多余的未知字段（如 service_tier、
// reasoning_content 这类兼容端点私有扩展）会被 encoding/json 忽略，不影响解析。
type openaiResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage"`

	// M1 无工具，但字段先定义，避免 M2 改响应结构：finish_reason=tool_calls 时
	// 内容就在 Choices[i].Message.ToolCalls 里。
}

type openaiChoice struct {
	Index int `json:"index"`
	// Message 是完整消息（非流式）。流式的 delta 属后续增强，不在此定义。
	Message openaiResponseMsg `json:"message"`
	// FinishReason 取值语义见 types.go 的 FinishReason* 常量（内部协议词汇，不在 wire 层重复定义）。
	FinishReason string `json:"finish_reason"`
}

// openaiResponseMsg 是响应中的 assistant 消息。
//
// Content 用 *string：当 assistant 只发起工具调用时，协议返回的是
// `"content": null`，而不是空字符串。用 *string 才能区分"服务端说没有文本"
// （nil）与"服务端说文本为空串"（指向 ""）；用 string 这两种情况会塌成同一个值。
type openaiResponseMsg struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openaiUsage 是 token 计量。总用量由 PromptTokens + CompletionTokens 计费，
// 上下文窗口约束的是两者之和（§3.6），这也是 max_context_tokens 的语义来源。
type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// 明细字段直接影响真实成本，M1 先解析出来备用（§3.1 单价取最贵一档的根因就在这）：
	// CachedTokens —— 命中提示缓存的输入 token 单价远低于未命中（DeepSeek 相差 100 倍）；
	// ReasoningTokens —— 思考模式产生的推理 token 按输出价计费。
	PromptDetails     *openaiPromptDetails     `json:"prompt_tokens_details"`
	CompletionDetails *openaiCompletionDetails `json:"completion_tokens_details"`
}

type openaiPromptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openaiCompletionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// finish_reason 的取值语义属于内部协议，定义在 types.go（FinishReason* 常量），
// 不在此重复一份，避免 wire 层与协议层的枚举漂移。

// ------------------------------------------------------------ 错误响应 ----

// openaiErrorEnvelope 是 4xx / 5xx 的响应体。
// 注意：兼容网关不一定遵守这个格式（可能返回 HTML、纯文本或自定义结构），
// 所以解析错误响应必须是"尽力而为"——拿不到 message 就用状态码兜底，
// 不能因为错误体格式意外就让解析失败盖住真实的状态码。
type openaiErrorEnvelope struct {
	Error openaiError `json:"error"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"` // 如 invalid_request_error、rate_limit_error
	// Code 在不同端点上类型不统一（OpenAI 可能回字符串，兼容网关可能回数字），
	// 用 RawMessage 原样接住；若声明成 string，遇到数字 code 会整个解析失败。
	// omit 必加：RawMessage 为 nil 时不加会多出一个 "code": null（影响往返一致性）。
	Code  json.RawMessage `json:"code,omitempty"`
	Param string          `json:"param"`
}
