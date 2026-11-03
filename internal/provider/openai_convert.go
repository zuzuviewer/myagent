package provider

import (
	"encoding/json"
	"errors"
	"fmt"
)

// 内部类型 ↔ Chat Completions 线路格式的双向转换（TECHNICAL_DESIGN §3.4）。
//
// 这里的报错都是**转换期错误**：结构非法、字段缺失、JSON 不合法。§4 的
// ProviderError / TimeoutError 分类属于 Generate 的职责，不在这一层做，
// 目的是让这两个函数能被纯单元测试覆盖（§5.1），不依赖网络和 http.Client。

// defaultToolArguments 用于把"没有参数"归一化成合法的空 JSON 对象。
const defaultToolArguments = "{}"

// emptyObjectSchema 是工具 parameters 缺省时的合法 JSON Schema。
// OpenAI 与兼容端点都要求 parameters 是一个对象 schema，省略会报错。
const emptyObjectSchema = `{"type":"object","properties":{}}`

// toOpenAIRequest 把内部请求转换为线路格式。
//
// model 单独作为参数传入：内部 ModelRequest 刻意不含模型名（§3.4），
// 它是 Provider 的配置而不是每次请求的可变参数，这样换模型不需要改调用点。
//
// 转换同时承担 role 与字段搭配的合法性校验。非法组合在部分端点上会被"接受"
// 并返回难以解释的响应，在转换期失败远比上线后 debug 便宜。
func toOpenAIRequest(model string, req ModelRequest) (openaiRequest, error) {
	if model == "" {
		return openaiRequest{}, errors.New("provider: 未配置模型名")
	}
	if len(req.Messages) == 0 {
		return openaiRequest{}, errors.New("provider: 请求不含任何消息")
	}
	// MaxTokens 带 omitempty，0 会被整个字段省略，等于把输出长度交给服务端默认值。
	// 而 §3.4 把 MaxOutputTokens 列为请求契约的必填项，预算控制依赖它，必须显式为正。
	if req.MaxOutputTokens <= 0 {
		return openaiRequest{}, fmt.Errorf("provider: MaxOutputTokens 必须大于 0，当前为 %d", req.MaxOutputTokens)
	}

	out := openaiRequest{
		Model:             model,
		Messages:          make([]openaiRequestMsg, 0, len(req.Messages)),
		MaxTokens:         req.MaxOutputTokens,
		ParallelToolCalls: false, // 显式发送 false（§3.4：MVP 串行执行工具调用）
		Stream:            false, // M1 非流式（§8 后续增强）
	}

	for i, m := range req.Messages {
		if err := checkMessageShape(i, m); err != nil {
			return openaiRequest{}, err
		}

		wire := openaiRequestMsg{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}

		if len(m.ToolCalls) > 0 {
			wire.ToolCalls = make([]openaiToolCall, 0, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				if tc.ID == "" {
					return openaiRequest{}, fmt.Errorf("provider: 第 %d 条消息的第 %d 个 Tool Call 缺少 ID", i, j)
				}
				if tc.Name == "" {
					return openaiRequest{}, fmt.Errorf("provider: Tool Call %s 缺少工具名", tc.ID)
				}
				args, err := requestArguments(i, j, tc)
				if err != nil {
					return openaiRequest{}, err
				}
				wire.ToolCalls = append(wire.ToolCalls, openaiToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: openaiFunctionCall{Name: tc.Name, Arguments: args},
				})
			}
		}

		out.Messages = append(out.Messages, wire)
	}

	if len(req.Tools) > 0 {
		out.Tools = make([]openaiTool, 0, len(req.Tools))
		seen := make(map[string]bool, len(req.Tools))
		for _, t := range req.Tools {
			if t.Name == "" {
				return openaiRequest{}, errors.New("provider: 工具 Schema 缺少名称")
			}
			// Registry 已保证名称唯一（§3.7），这里是不依赖上游的自保：
			// 重名工具会让模型选到预期之外的 schema，属于必须挡住的输入。
			if seen[t.Name] {
				return openaiRequest{}, fmt.Errorf("provider: 工具名 %q 在请求 Schema 中重复", t.Name)
			}
			seen[t.Name] = true

			params, err := toolParameters(t)
			if err != nil {
				return openaiRequest{}, err
			}
			out.Tools = append(out.Tools, openaiTool{
				Type: "function",
				// 只搬运 Name/Description/Parameters：Risk 与 Capability 是策略元数据，
				// 进入提示词等于告诉模型哪个工具权限更宽（R-09）。
				Function: openaiToolFunction{Name: t.Name, Description: t.Description, Parameters: params},
			})
		}
	}

	return out, nil
}

// checkMessageShape 校验 role 与字段的搭配约束。
func checkMessageShape(i int, m Message) error {
	switch m.Role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		return fmt.Errorf("provider: 第 %d 条消息角色 %q 未知", i, m.Role)
	}

	if m.Role == RoleTool && m.ToolCallID == "" {
		// 没有 ToolCallID 的 tool 消息无法回填到原调用，会制造孤立消息（R-16）。
		return fmt.Errorf("provider: 第 %d 条 tool 消息缺少 ToolCallID", i)
	}
	if m.Role != RoleTool && m.ToolCallID != "" {
		return fmt.Errorf("provider: 第 %d 条 %s 消息不应携带 ToolCallID", i, m.Role)
	}
	if m.Role != RoleAssistant && len(m.ToolCalls) > 0 {
		return fmt.Errorf("provider: 第 %d 条 %s 消息不应携带 ToolCalls", i, m.Role)
	}
	return nil
}

// requestArguments 把内部 RawMessage 转成线路上的 arguments 字符串。
//
// 关键：线上的 arguments 是"被字符串装起来的 JSON"，所以这里只做字节转换，
// **绝不能再 Marshal 一次**——双重编码会得到 "\"{\\\"a\\\"...}\""，
// 服务端或 Executor 侧解析必然失败。
func requestArguments(i, j int, tc ToolCall) (string, error) {
	if len(tc.Arguments) == 0 {
		return defaultToolArguments, nil // "无参数"归一化为合法空对象，而不是发 null
	}
	if !json.Valid(tc.Arguments) {
		return "", fmt.Errorf("provider: 第 %d 条消息的第 %d 个 Tool Call 参数不是合法 JSON", i, j)
	}
	return string(tc.Arguments), nil
}

// toolParameters 补全并校验工具的 JSON Schema。
func toolParameters(t ToolSchema) (json.RawMessage, error) {
	if len(t.Parameters) == 0 {
		return json.RawMessage(emptyObjectSchema), nil
	}
	if !json.Valid(t.Parameters) {
		return nil, fmt.Errorf("provider: 工具 %q 的 parameters 不是合法 JSON Schema", t.Name)
	}
	return t.Parameters, nil
}

// toModelResponse 把线路响应转换为内部表示。
//
// 只在这里判定**结构是否成立**；是否"正常完成"由 FinishReason 交给上层按
// §3.5 的错误与预算策略处理，Provider 不自作主张把 length / content_filter
// 改写成成功或失败。
func toModelResponse(resp openaiResponse) (ModelResponse, error) {
	if len(resp.Choices) == 0 {
		return ModelResponse{}, errors.New("provider: 响应不含 choices")
	}
	if len(resp.Choices) > 1 {
		// 只取第 0 个会静默丢弃其余内容，属于"看起来成功但少了东西"，直接报错。
		return ModelResponse{}, fmt.Errorf("provider: 响应含 %d 个 choices，M1 只接受 1 个", len(resp.Choices))
	}

	ch := resp.Choices[0]
	if ch.Message.Role != "" && ch.Message.Role != string(RoleAssistant) {
		return ModelResponse{}, fmt.Errorf("provider: 响应消息角色为 %q，期望 assistant", ch.Message.Role)
	}

	// content 为 null 表示"本次没有文本"（例如只有工具调用），内部类型统一为空串。
	// 判空要结合 FinishReason 与 ToolCalls，不能只看 Content（§3.5 空响应语义）。
	var content string
	if ch.Message.Content != nil {
		content = *ch.Message.Content
	}

	out := ModelResponse{
		Message: Message{
			Role:    RoleAssistant,
			Content: content,
		},
		FinishReason: ch.FinishReason,
	}

	seenID := make(map[string]bool, len(ch.Message.ToolCalls))
	for j, tc := range ch.Message.ToolCalls {
		if tc.ID == "" {
			return ModelResponse{}, fmt.Errorf("provider: 响应第 %d 个 Tool Call 缺少 ID", j)
		}
		// 重复 ID 会让 Tool Result 回填错配（R-16），必须在此拦住而不是留给上层猜。
		if seenID[tc.ID] {
			return ModelResponse{}, fmt.Errorf("provider: 响应存在重复的 Tool Call ID %q", tc.ID)
		}
		seenID[tc.ID] = true

		if tc.Function.Name == "" {
			return ModelResponse{}, fmt.Errorf("provider: Tool Call %s 缺少工具名", tc.ID)
		}

		args := json.RawMessage(tc.Function.Arguments) // 直接取字节，不做二次编码
		if len(tc.Function.Arguments) == 0 {
			args = json.RawMessage(defaultToolArguments)
		} else if !json.Valid(args) {
			// 模型可能吐出截断或非法 JSON。这里不"修复"内容，只标记不合法，
			// 交给 Executor 的权威校验与 Tool Result 反馈让模型纠正（R-08）。
			return ModelResponse{}, fmt.Errorf("provider: Tool Call %s 的参数不是合法 JSON", tc.ID)
		}

		out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	if resp.Usage != nil {
		out.Usage = Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		}
	}
	// usage 缺失时保持零值：估算与"标注来源"是展示层职责（§3.4），
	// Provider 只搬运事实，不在这里编造数字。
	return out, nil
}
