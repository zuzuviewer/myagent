// Package provider 定义 Agent 与模型交互的内部协议类型。
//
// 这是整个项目的模型抽象边界（TECHNICAL_DESIGN §3.4）：Agent Core 只认识本文件里的
// 类型，不认识任何 SDK 类型；OpenAI Chat Completions 的请求/响应结构只允许出现在
// Provider 实现内部，转换后落入这些类型。后续接入 Responses API 时新增 Provider，
// 不改动这里，也不重写 Agent Loop。
package provider

import (
	"context"
	"encoding/json"
)

// Provider 是模型能力的唯一抽象。实现必须尊重 ctx 的超时与取消，并把鉴权失败、
// 限流、服务端错误、响应解析错误分类后返回（见 §4 的 ProviderError）。
type Provider interface {
	Generate(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

// ModelRequest 是一次模型请求的内部表示，不携带任何权限或策略元数据。
// MVP 固定 parallel_tool_calls=false，与串行执行工具调用一致（§3.4）。
type ModelRequest struct {
	Messages        []Message
	Tools           []ToolSchema
	MaxOutputTokens int
}

// ModelResponse 是一次模型返回的内部表示。
// FinishReason 为 length、空响应或 content_filter 时，本次生成未正常完成，
// 调用方不能把它当作任务成功（§3.5）。
type ModelResponse struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Message 的字段受角色约束，实现 Provider 时必须按约束校验，避免拼出非法请求：
//   - ToolCalls 只出现在 assistant 消息；
//   - ToolCallID 与 Name 只出现在 tool 消息，ToolCallID 对应 API 的 tool_call_id，
//     用于把 Tool Result 回填到原 Tool Call（R-16：不得产生孤立消息）。
//
// M1 只使用 Role 与 Content；下列工具字段是协议契约的一部分，在此定义是为了让
// Provider 的转换逻辑一次成型，不代表 M1 已具备工具能力。
type Message struct {
	Role       MessageRole
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolCall 是模型请求调用工具的结构。Arguments 属于未经校验的模型输出，是不可信
// 输入：不能直接反序列化后执行（§3.5），Tool Executor 才是工具名与参数的权威校验
// 边界（R-08），非法参数绝不能进入工具实现。
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult 是工具执行结果，写入历史时序列化为 role=tool 消息。
// Chat Completions 没有独立的错误字段，IsError 编码进 Content 前缀（如 "ERROR: ..."），
// 并在系统指令中说明该约定（§3.4）。Truncated 为 true 时内容必须带明确截断标记，
// OriginalBytes 保留截断前大小，不能把截断内容伪装成完整结果（R-17）。
type ToolResult struct {
	CallID        string
	Content       string
	IsError       bool
	Truncated     bool
	OriginalBytes int
}

// Usage 优先取 API 返回；缺失时使用估算值，并在 debug 输出中标注来源。
// 成本 = Usage × 配置单价，仅用于展示与预算，不作为授权依据（§3.4）。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// FinishReason 取值：这是内部协议词汇，不是 wire 层细节——CLI 与 M2 的 Agent Loop
// 都要据此判定"本次生成是否完成"（§3.5），所以必须导出供跨层使用。
// 除 FinishReasonStop 外都不能当作任务成功；Provider 只透传不判定。
const (
	FinishReasonStop          = "stop"           // 正常结束
	FinishReasonLength        = "length"         // 输出被 max_tokens 截断
	FinishReasonToolCalls     = "tool_calls"     // 模型发起了工具调用
	FinishReasonContentFilter = "content_filter" // 内容过滤中断
)

type MessageRole string

// 角色取值与 Chat Completions 保持一致；工具结果统一使用 RoleTool。
const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// ToolSchema 只包含构造提示词所需的字段，刻意不携带 Risk / Capability 等策略元数据：
// 元数据由应用注册、供 Permission Layer 决策使用（R-09），不能进入提示词让模型看到，
// 否则模型会知道"哪些工具权限更宽"并据此尝试越权。
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
