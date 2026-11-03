package provider

import (
	"context"
	"fmt"
	"time"
)

// Script 是一次可编程的 Generate 结果。
type Script struct {
	// Response 在成功路径返回；Err 非空时优先返回错误。
	Response ModelResponse
	Err      error
	// Delay 在返回前等待，并且**可被 ctx 取消**——这是测试"取消覆盖模型请求"
	// （R-15）的唯一手段，用 time.Sleep 会让取消等到睡完才生效。
	Delay time.Duration
}

// ScriptText 构造一轮纯文本成功回复，是最常用的脚本单元。
func ScriptText(content string) Script {
	return Script{Response: ModelResponse{
		Message:      Message{Role: RoleAssistant, Content: content},
		FinishReason: FinishReasonStop,
	}}
}

// ScriptError 构造一次失败调用，用于覆盖错误分类与恢复路径。
func ScriptError(err error) Script { return Script{Err: err} }

// ScriptDelay 构造延迟回复；配合极小的 ctx 超时即可稳定复现超时场景。
func ScriptDelay(d time.Duration, s Script) Script {
	s.Delay = d
	return s
}

// TextWithUsage 构造带计量的文本回复，用于断言成本与窗口占比。
func TextWithUsage(content string, usage Usage) Script {
	s := ScriptText(content)
	s.Response.Usage = usage
	return s
}

// Fake 是 Provider 的测试替身：按脚本顺序应答，并记录收到的每个请求。
//
// 它与 ChatCompletions 遵守同一份契约：请求本身非法就报错，脚本耗尽也报错。
// 如果 Fake 对坏请求宽容、或对"没有预置响应"返回零值成功，那么测试通过的
// 就只是 Fake 的宽松，而不是实现的正确（§3.5：不能凭表面现象判定成功）。
type Fake struct {
	scripts []Script
	calls   []ModelRequest
	next    int
}

// NewFake 按给定脚本构造 Fake。
func NewFake(scripts ...Script) *Fake {
	// 复制一份：调用方后续修改传入的切片不应改变已交付的行为。
	copied := make([]Script, len(scripts))
	copy(copied, scripts)
	return &Fake{scripts: copied}
}

// Generate implements [Provider].
func (f *Fake) Generate(ctx context.Context, req ModelRequest) (ModelResponse, error) {
	// 与真实 Provider 同构的请求校验：转换层的契约不因替身而放松。
	if len(req.Messages) == 0 {
		return ModelResponse{}, &ProviderError{Category: CategoryRequest, Message: "请求不含任何消息"}
	}
	if req.MaxOutputTokens <= 0 {
		return ModelResponse{}, &ProviderError{Category: CategoryRequest, Message: "MaxOutputTokens 必须大于 0"}
	}
	for i, m := range req.Messages {
		if m.Role == RoleTool && m.ToolCallID == "" {
			return ModelResponse{}, &ProviderError{
				Category: CategoryRequest,
				Message:  fmt.Sprintf("第 %d 条 tool 消息缺少 ToolCallID", i),
			}
		}
	}

	if f.next >= len(f.scripts) {
		// 脚本耗尽必须失败：静默返回零值会让 Agent Loop 以为"模型说完了"，
		// 把测试的破裂伪装成任务的完成。
		return ModelResponse{}, &ProviderError{
			Category: CategoryRequest,
			Message: fmt.Sprintf("Fake 脚本已耗尽（第 %d 次调用没有预置响应，脚本共 %d 项）；"+
				"这通常意味着被测逻辑比预期多调了一轮模型", f.next+1, len(f.scripts)),
		}
	}

	script := f.scripts[f.next]
	f.next++
	// 记录请求副本：Messages 是切片，调用方复用底层数组会篡改历史。
	f.calls = append(f.calls, cloneRequest(req))

	if script.Delay > 0 {
		timer := time.NewTimer(script.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ModelResponse{}, &ProviderError{
				Category: CategoryCanceled,
				Message:  "Fake 在模拟延迟时被取消",
				Err:      ctx.Err(),
			}
		case <-timer.C:
		}
	}

	if script.Err != nil {
		return ModelResponse{}, script.Err
	}
	return script.Response, nil
}

// Calls 返回收到的请求副本，供断言上下文组装是否正确。
func (f *Fake) Calls() []ModelRequest {
	out := make([]ModelRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// Remaining 返回尚未消费的脚本数量。
func (f *Fake) Remaining() int { return len(f.scripts) - f.next }

func cloneRequest(req ModelRequest) ModelRequest {
	out := ModelRequest{
		Messages:        make([]Message, len(req.Messages)),
		Tools:           make([]ToolSchema, len(req.Tools)),
		MaxOutputTokens: req.MaxOutputTokens,
	}
	copy(out.Messages, req.Messages)
	copy(out.Tools, req.Tools)
	for i, m := range req.Messages {
		if len(m.ToolCalls) > 0 {
			out.Messages[i].ToolCalls = make([]ToolCall, len(m.ToolCalls))
			copy(out.Messages[i].ToolCalls, m.ToolCalls)
		}
	}
	return out
}

// FakeCannedReply 供 CLI 的 --provider fake 本地联调使用：一条固定的单轮回复。
// 它不进入任何验收路径，只用于在没有 API Key 的情况下调试 CLI 管道与退出码。
func FakeCannedReply(task string) *Fake {
	return NewFake(ScriptText(fmt.Sprintf(
		"[fake] 已收到任务（%d 字符）。当前没有工具，因此不会读取任何文件；"+
			"M2 起 Agent 才能使用只读工具完成此类请求。", len([]rune(task)))))
}

var _ Provider = (*Fake)(nil)
