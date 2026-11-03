package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }

// 期望的 M1 最小请求体形状。这个断言之所以逐字节比对，是因为 wire 层的
// 正确性只能靠"和协议文档一模一样"来保证；字段名、omitempty 决策、
// false 是否被省略，全都在这一条测试里现形。
const wantMinimalRequestJSON = `{"model":"gpt-4o","messages":[` +
	`{"role":"system","content":"sys prompt"},` +
	`{"role":"user","content":"读取 go.mod"}],` +
	`"max_tokens":1024,"parallel_tool_calls":false,"stream":false}`

func TestToOpenAIRequestShape(t *testing.T) {
	got, err := toOpenAIRequest("gpt-4o", ModelRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "sys prompt"},
			{Role: RoleUser, Content: "读取 go.mod"},
		},
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(b) != wantMinimalRequestJSON {
		t.Errorf("请求体形状与协议不符\n got: %s\nwant: %s", b, wantMinimalRequestJSON)
	}
	// M1 无工具：tools 必须整个缺席，而不是发出 null 或空数组。
	if strings.Contains(string(b), `"tools"`) {
		t.Errorf("M1 不应发送 tools 字段: %s", b)
	}
	// 这两个 false 是语义本身，被 omitempty 吞掉就等于把决定权交给服务端。
	if !strings.Contains(string(b), `"parallel_tool_calls":false`) || !strings.Contains(string(b), `"stream":false`) {
		t.Errorf("parallel_tool_calls / stream 的 false 必须显式发送: %s", b)
	}
}

// 锁住最大的坑：arguments 在线上是"被字符串装起来的 JSON"，不能双重编码。
func TestToOpenAIRequestArgumentsAreStringifiedJSON(t *testing.T) {
	got, err := toOpenAIRequest("gpt-4o", ModelRequest{
		Messages: []Message{{
			Role:    RoleAssistant,
			Content: "我先读文件",
			ToolCalls: []ToolCall{{
				ID:        "call_1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"path":"go.mod"}`),
			}},
		}},
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	// 解回通用结构，检查 arguments 的真实类型与内容。
	var raw struct {
		Messages []struct {
			ToolCalls []struct {
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("请求体无法解析: %v (%s)", err, b)
	}
	args := raw.Messages[0].ToolCalls[0].Function.Arguments
	if args != `{"path":"go.mod"}` {
		t.Errorf("arguments 应是原始 JSON 文本, got %q（若含转义嵌套即为双重编码）", args)
	}
	if !json.Valid([]byte(args)) {
		t.Errorf("arguments 解出来不是合法 JSON: %q", args)
	}
	if raw.Messages[0].ToolCalls[0].Type != "function" {
		t.Errorf("tool_calls[].type 应为 function, got %q", raw.Messages[0].ToolCalls[0].Type)
	}
}

func TestToOpenAIRequestEmptyDefaults(t *testing.T) {
	got, err := toOpenAIRequest("m", ModelRequest{
		Messages: []Message{{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:        "call_1",
				Name:      "list_files",
				Arguments: nil, // 无参数调用
			}},
		}},
		MaxOutputTokens: 100,
		Tools: []ToolSchema{
			{Name: "list_files", Description: "列目录"}, // 无 parameters，应被补全
			{Name: "read_file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
		},
	})
	if err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}
	if got.Messages[0].ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("空 arguments 应归一化为 {}, got %q", got.Messages[0].ToolCalls[0].Function.Arguments)
	}
	// 端点要求 parameters 是对象 schema，缺省必须补全而不是省略。
	if !json.Valid(got.Tools[0].Function.Parameters) || len(got.Tools[0].Function.Parameters) == 0 {
		t.Errorf("空 parameters 应补成合法 JSON Schema, got %s", got.Tools[0].Function.Parameters)
	}
	if got.Tools[0].Type != "function" {
		t.Errorf("tools[].type 应为 function, got %q", got.Tools[0].Type)
	}
	// 已提供的 schema 必须逐字节透传：任何重排或再编码都会改动约束条件。
	const wantSchema = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	if string(got.Tools[1].Function.Parameters) != wantSchema {
		t.Errorf("parameters 应原样透传\n got: %s\nwant: %s", got.Tools[1].Function.Parameters, wantSchema)
	}
}

func TestToOpenAIRequestRejects(t *testing.T) {
	cases := []struct {
		name  string
		model string
		req   ModelRequest
		want  string
	}{
		{"空模型名", "", ModelRequest{Messages: []Message{{Role: RoleUser}}, MaxOutputTokens: 1}, "模型名"},
		{"无消息", "m", ModelRequest{MaxOutputTokens: 1}, "不含任何消息"},
		{"输出上限为 0", "m", ModelRequest{Messages: []Message{{Role: RoleUser}}}, "MaxOutputTokens"},
		{"输出上限为负", "m", ModelRequest{Messages: []Message{{Role: RoleUser}}, MaxOutputTokens: -1}, "MaxOutputTokens"},
		{
			"未知角色", "m",
			ModelRequest{Messages: []Message{{Role: MessageRole("root"), Content: "x"}}, MaxOutputTokens: 1},
			"角色",
		},
		{
			"tool 消息缺 ToolCallID", "m",
			ModelRequest{Messages: []Message{{Role: RoleTool, Content: "结果"}}, MaxOutputTokens: 1},
			"ToolCallID",
		},
		{
			"user 消息带 ToolCalls", "m",
			ModelRequest{Messages: []Message{{Role: RoleUser, ToolCalls: []ToolCall{{ID: "1", Name: "t"}}}}, MaxOutputTokens: 1},
			"不应携带 ToolCalls",
		},
		{
			"system 消息带 ToolCallID", "m",
			ModelRequest{Messages: []Message{{Role: RoleSystem, ToolCallID: "x"}}, MaxOutputTokens: 1},
			"不应携带 ToolCallID",
		},
		{
			"Tool Call 缺 ID", "m",
			ModelRequest{Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "t"}}}}, MaxOutputTokens: 1},
			"缺少 ID",
		},
		{
			"Tool Call 缺工具名", "m",
			ModelRequest{Messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1"}}}}, MaxOutputTokens: 1},
			"缺少工具名",
		},
		{
			"Tool Call 参数非法 JSON", "m",
			ModelRequest{Messages: []Message{{Role: RoleAssistant,
				ToolCalls: []ToolCall{{ID: "1", Name: "t", Arguments: json.RawMessage(`{oops`)}}}}, MaxOutputTokens: 1},
			"不是合法 JSON",
		},
		{
			"工具名为空", "m",
			ModelRequest{Messages: []Message{{Role: RoleUser}}, Tools: []ToolSchema{{Description: "d"}}, MaxOutputTokens: 1},
			"缺少名称",
		},
		{
			"工具名重复", "m",
			ModelRequest{Messages: []Message{{Role: RoleUser}},
				Tools: []ToolSchema{{Name: "a"}, {Name: "a"}}, MaxOutputTokens: 1},
			"重复",
		},
		{
			"工具 schema 非法", "m",
			ModelRequest{Messages: []Message{{Role: RoleUser}},
				Tools: []ToolSchema{{Name: "a", Parameters: json.RawMessage(`[1,2`)}}, MaxOutputTokens: 1},
			"合法 JSON Schema",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toOpenAIRequest(tc.model, tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错包含 %q, got %v", tc.want, err)
			}
			if !strings.HasPrefix(err.Error(), "provider: ") {
				t.Errorf("错误应带 provider: 前缀便于定位来源, got %v", err)
			}
		})
	}
}

func responseWith(content *string, finish string, toolCalls []openaiToolCall) openaiResponse {
	return openaiResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion",
		Model:  "gpt-4o",
		Choices: []openaiChoice{{
			Index: 0,
			Message: openaiResponseMsg{
				Role:      string(RoleAssistant),
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: finish,
		}},
	}
}

func TestToModelResponseText(t *testing.T) {
	resp := responseWith(strptr("项目使用 Go 实现"), FinishReasonStop, nil)
	resp.Usage = &openaiUsage{PromptTokens: 120, CompletionTokens: 34, TotalTokens: 154}

	got, err := toModelResponse(resp)
	if err != nil {
		t.Fatalf("合法响应不应报错: %v", err)
	}
	if got.Message.Role != RoleAssistant || got.Message.Content != "项目使用 Go 实现" {
		t.Errorf("消息转换错误: %+v", got.Message)
	}
	if got.FinishReason != FinishReasonStop {
		t.Errorf("FinishReason 应原样透传, got %q", got.FinishReason)
	}
	if got.Usage.PromptTokens != 120 || got.Usage.CompletionTokens != 34 {
		t.Errorf("Usage 转换错误: %+v", got.Usage)
	}
}

// FinishReason 的异常取值不属于"结构非法"，Provider 不自作主张判定成败，
// 必须原样交给上层按 §3.5 处理。
func TestToModelResponseCarriesAbnormalFinishReason(t *testing.T) {
	for _, reason := range []string{FinishReasonLength, FinishReasonContentFilter, FinishReasonToolCalls} {
		got, err := toModelResponse(responseWith(strptr("部分文本"), reason, nil))
		if err != nil {
			t.Fatalf("finish_reason=%s 不应由 Provider 报错: %v", reason, err)
		}
		if got.FinishReason != reason {
			t.Errorf("FinishReason 应透传 %s, got %q", reason, got.FinishReason)
		}
	}
}

// content 为 null（只有工具调用）与 content 为 ""（空回复）都归一成空串，
// 区分依赖 FinishReason 与 ToolCalls，不依赖 Content。
func TestToModelResponseNullContent(t *testing.T) {
	got, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, []openaiToolCall{{
		ID:   "call_9",
		Type: "function",
		Function: openaiFunctionCall{
			Name:      "read_file",
			Arguments: `{"path":"go.mod"}`,
		},
	}}))
	if err != nil {
		t.Fatalf("合法响应不应报错: %v", err)
	}
	if got.Message.Content != "" {
		t.Errorf("null content 应归一为空串, got %q", got.Message.Content)
	}
	if len(got.Message.ToolCalls) != 1 {
		t.Fatalf("应解析出 1 个 Tool Call, got %d", len(got.Message.ToolCalls))
	}
	tc := got.Message.ToolCalls[0]
	if tc.ID != "call_9" || tc.Name != "read_file" {
		t.Errorf("Tool Call 字段错误: %+v", tc)
	}
	// 反向坑：响应侧同样是"字符串里的 JSON"，取字节即可，不可二次编码。
	if string(tc.Arguments) != `{"path":"go.mod"}` {
		t.Errorf("Arguments 应为原始 JSON, got %q", tc.Arguments)
	}
	if !json.Valid(tc.Arguments) {
		t.Errorf("Arguments 不是合法 JSON: %s", tc.Arguments)
	}
}

func TestToModelResponseMissingUsage(t *testing.T) {
	got, err := toModelResponse(responseWith(strptr("hi"), FinishReasonStop, nil))
	if err != nil {
		t.Fatalf("合法响应不应报错: %v", err)
	}
	// 兼容端点常不回 usage：保持零值，估算与标注来源由展示层负责（§3.4）。
	if got.Usage != (Usage{}) {
		t.Errorf("usage 缺失时应为零值, got %+v", got.Usage)
	}
}

func TestToModelResponseRejects(t *testing.T) {
	t.Run("空 choices", func(t *testing.T) {
		if _, err := toModelResponse(openaiResponse{Choices: nil}); err == nil ||
			!strings.Contains(err.Error(), "choices") {
			t.Fatalf("应报错, got %v", err)
		}
	})
	t.Run("多个 choices", func(t *testing.T) {
		resp := openaiResponse{Choices: []openaiChoice{
			{Message: openaiResponseMsg{Role: "assistant", Content: strptr("a")}},
			{Message: openaiResponseMsg{Role: "assistant", Content: strptr("b")}},
		}}
		if _, err := toModelResponse(resp); err == nil ||
			!strings.Contains(err.Error(), "只接受 1 个") {
			t.Fatalf("多 choice 应报错而不是静默丢弃, got %v", err)
		}
	})
	t.Run("角色非 assistant", func(t *testing.T) {
		resp := openaiResponse{Choices: []openaiChoice{{
			Message: openaiResponseMsg{Role: "user", Content: strptr("x")},
		}}}
		if _, err := toModelResponse(resp); err == nil ||
			!strings.Contains(err.Error(), "assistant") {
			t.Fatalf("应报错, got %v", err)
		}
	})
	t.Run("重复 Tool Call ID", func(t *testing.T) {
		dup := []openaiToolCall{
			{ID: "same", Type: "function", Function: openaiFunctionCall{Name: "a", Arguments: "{}"}},
			{ID: "same", Type: "function", Function: openaiFunctionCall{Name: "b", Arguments: "{}"}},
		}
		if _, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, dup)); err == nil ||
			!strings.Contains(err.Error(), "重复") {
			t.Fatalf("重复 ID 会造成回填错配（R-16），应报错, got %v", err)
		}
	})
	t.Run("非法 arguments", func(t *testing.T) {
		bad := []openaiToolCall{
			{ID: "c1", Type: "function", Function: openaiFunctionCall{Name: "a", Arguments: `{"a":`}},
		}
		if _, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, bad)); err == nil ||
			!strings.Contains(err.Error(), "不是合法 JSON") {
			t.Fatalf("截断的 arguments 应报错, got %v", err)
		}
	})
	t.Run("空 arguments 归一化", func(t *testing.T) {
		// 部分端点对无参工具回 "" 而不是 "{}"，不归一化会把非法 JSON 带给 Executor。
		empty := []openaiToolCall{{
			ID: "c1", Type: "function",
			Function: openaiFunctionCall{Name: "list_files", Arguments: ""},
		}}
		got, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, empty))
		if err != nil {
			t.Fatalf("空 arguments 不应报错: %v", err)
		}
		if string(got.Message.ToolCalls[0].Arguments) != "{}" {
			t.Errorf("空 arguments 应归一为 {}, got %q", got.Message.ToolCalls[0].Arguments)
		}
	})
	t.Run("Tool Call 缺 ID", func(t *testing.T) {
		noID := []openaiToolCall{{Type: "function", Function: openaiFunctionCall{Name: "a", Arguments: "{}"}}}
		if _, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, noID)); err == nil ||
			!strings.Contains(err.Error(), "缺少 ID") {
			t.Fatalf("缺 ID 无法回填（R-16），应报错, got %v", err)
		}
	})
	t.Run("Tool Call 缺工具名", func(t *testing.T) {
		noName := []openaiToolCall{{ID: "c1", Type: "function", Function: openaiFunctionCall{Arguments: "{}"}}}
		if _, err := toModelResponse(responseWith(nil, FinishReasonToolCalls, noName)); err == nil ||
			!strings.Contains(err.Error(), "缺少工具名") {
			t.Fatalf("无工具名的调用无法映射回 Registry，应报错, got %v", err)
		}
	})
}
