package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func fakeReq() ModelRequest {
	return ModelRequest{
		Messages:        []Message{{Role: RoleUser, Content: "hi"}},
		MaxOutputTokens: 100,
	}
}

func TestFakeServesScriptsInOrder(t *testing.T) {
	f := NewFake(ScriptText("第一条"), ScriptText("第二条"))

	first, err := f.Generate(context.Background(), fakeReq())
	if err != nil || first.Message.Content != "第一条" {
		t.Fatalf("第一次调用: %v %+v", err, first)
	}
	second, err := f.Generate(context.Background(), fakeReq())
	if err != nil || second.Message.Content != "第二条" {
		t.Fatalf("第二次调用: %v %+v", err, second)
	}
	if f.Remaining() != 0 {
		t.Errorf("脚本应耗尽, Remaining = %d", f.Remaining())
	}
	if len(f.Calls()) != 2 {
		t.Errorf("应记录 2 次调用, got %d", len(f.Calls()))
	}
}

// 脚本耗尽必须报错：返回零值成功会让被测的 Loop 误以为"模型说完了"，
// 把测试基础设施的破裂伪装成任务完成（§3.5）。
func TestFakeExhaustedIsError(t *testing.T) {
	f := NewFake(ScriptText("只有一条"))
	f.Generate(context.Background(), fakeReq())

	_, err := f.Generate(context.Background(), fakeReq())
	if err == nil {
		t.Fatal("脚本耗尽应报错")
	}
	if !strings.Contains(err.Error(), "脚本已耗尽") {
		t.Errorf("错误应说明脚本耗尽, got %v", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Errorf("错误应是分类错误以便上层处理, got %T", err)
	}
}

// Fake 对坏请求必须和真实 Provider 一样严格，否则测试通过只证明了 Fake 宽容。
func TestFakeRejectsSameRequestsAsRealProvider(t *testing.T) {
	cases := []struct {
		name string
		req  ModelRequest
		want string
	}{
		{"无消息", ModelRequest{MaxOutputTokens: 1}, "不含任何消息"},
		{"输出上限为 0", ModelRequest{Messages: []Message{{Role: RoleUser}}}, "MaxOutputTokens"},
		{
			"tool 消息缺 ID",
			ModelRequest{Messages: []Message{{Role: RoleTool, Content: "结果"}}, MaxOutputTokens: 1},
			"ToolCallID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewFake(ScriptText("不该被消费")).Generate(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并指向 %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFakeRecordsRequestSnapshot(t *testing.T) {
	f := NewFake(ScriptText("ok"))
	req := fakeReq()
	f.Generate(context.Background(), req)

	// 调用方复用底层数组篡改历史，不应影响已记录的快照。
	req.Messages[0].Content = "被改写了"
	req.Messages = append(req.Messages, Message{Role: RoleUser, Content: "多出来的一条"})

	got := f.Calls()[0]
	if got.Messages[0].Content != "hi" {
		t.Errorf("Calls 应记录调用时的快照, got %q", got.Messages[0].Content)
	}
	if len(got.Messages) != 1 {
		t.Errorf("Calls 不应受后续 append 影响, 消息数 %d", len(got.Messages))
	}
}

// 延迟必须可取消：用 time.Sleep 实现的假延迟会让"取消覆盖模型请求"这条
// 测试永远测不出真相（R-15）。
func TestFakeDelayIsCancellable(t *testing.T) {
	f := NewFake(ScriptDelay(5*time.Second, ScriptText("不该返回")))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := f.Generate(ctx, fakeReq())
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("取消应立即返回, 耗时 %s", elapsed)
	}
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回可判定的取消错误, got %v", err)
	}
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Category != CategoryCanceled {
		t.Errorf("应分类为 canceled, got %q", pe.Category)
	}
}

func TestFakeErrorScript(t *testing.T) {
	sentinel := errors.New("注入的失败")
	f := NewFake(ScriptError(sentinel))

	_, err := f.Generate(context.Background(), fakeReq())
	if !errors.Is(err, sentinel) {
		t.Fatalf("应原样返回注入的错误, got %v", err)
	}
}

func TestFakeScriptsAreCopied(t *testing.T) {
	scripts := []Script{ScriptText("一"), ScriptText("二")}
	f := NewFake(scripts...)
	scripts[0] = ScriptText("被外部改写")

	got, err := f.Generate(context.Background(), fakeReq())
	if err != nil || got.Message.Content != "一" {
		t.Errorf("NewFake 应复制脚本, got %q (%v)", got.Message.Content, err)
	}
}

func TestFakeCannedReplySatisfiesCLI(t *testing.T) {
	f := FakeCannedReply("读取 go.mod 并总结这个项目")
	resp, err := f.Generate(context.Background(), fakeReq())
	if err != nil {
		t.Fatalf("canned 回复应成功: %v", err)
	}
	if resp.FinishReason != FinishReasonStop {
		t.Errorf("canned 回复应正常结束, got %q", resp.FinishReason)
	}
	// 明确否认工具能力，避免本地联调时误以为 M1 已能读文件。
	if !strings.Contains(resp.Message.Content, "没有工具") {
		t.Errorf("canned 回复应说明当前无工具: %q", resp.Message.Content)
	}
}
