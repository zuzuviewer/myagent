# Go AI Coding Agent — 技术设计文档

> 文档基线：**V1.2**（单 Agent 纵向闭环修订稿）

## 目录

- [1. 项目范围与非目标](#1-项目范围与非目标)
- [2. 总体设计原则与规范条目](#2-总体设计原则与规范条目)
- [3. 模块设计](#3-模块设计)
- [4. 错误处理与可观测性](#4-错误处理与可观测性)
- [5. 测试策略](#5-测试策略)
- [6. 推荐目录结构](#6-推荐目录结构)
- [7. 纵向实施路线与阶段验收](#7-纵向实施路线与阶段验收)
- [8. MVP 与后续增强边界](#8-mvp-与后续增强边界)
- [9. 版本说明](#9-版本说明)

## 1. 项目范围与非目标

### 1.1 项目目标

使用 Go 从零实现一个学习型、可扩展的 Coding Agent。首期以 CLI 单 Agent 为核心，完成模型交互、工具调用、Agent Loop、文件操作、受控命令执行、上下文管理和基础权限系统。

### 1.2 当前明确决策

- 首个模型 API：OpenAI Chat Completions。
- 后续模型 API：OpenAI Responses API。
- 当前不接入 Claude Code / Anthropic Messages API。
- 当前所有阶段均为单 Agent。
- Plan Mode、Review Mode 作为同一 Agent 的只读工作模式实现。
- 当前不设计独立 Plan / Review / Coding Agent、Multi-Agent 或 Orchestrator。

### 1.3 非目标

MVP 不追求完整 IDE、任意 Shell 自动化、任意目录访问、无人审批的高风险操作或生产级容器沙箱。Multi-Agent 与 Orchestrator 是当前项目的非目标，不纳入后续实施路线。

## 2. 总体设计原则与规范条目

### 2.1 设计原则

1. **边界优先：** 不可信模型输出不能直接成为系统权限。
2. **单一执行入口：** 工具只能由 Tool Executor 调用。
3. **策略与执行分离：** Permission Layer 决策，Executor 执行。
4. **默认拒绝：** 未知工具、未知能力、非法参数和无法判定的副作用默认拒绝。
5. **最小权限：** 每个任务只获得完成任务所需的最小能力和路径范围。
6. **可审计：** 记录工具、模式、目标、决策、原因和执行结果；避免记录密钥或敏感文件内容。
7. **可测试：** 权限规则必须能在不调用真实工具的情况下进行单元测试。

### 2.2 规范条目表

下表是跨文档的唯一规范来源。README、架构文档和各节正文只引用条目 ID，不重复定义。“生效阶段”表示该条目最迟必须在对应里程碑落地；落地后持续回归。

| ID | 规范条目 | 生效阶段 |
|---|---|---|
| R-01 | 所有工具执行只能经 Tool Executor；工具实现不得被 Agent Core、CLI 或其他组件直接调用。 | M2 |
| R-02 | Tool Executor 是 Permission Layer 的唯一调用方；Permission Layer 只决策，不执行工具、不改文件、不起进程。 | M2 |
| R-03 | 未知工具、未知能力、非法参数和无法判定的副作用一律 Deny；无规则命中时默认 Deny。 | M2 |
| R-04 | Mode、Workspace、PolicyID 由应用创建并向下传递；模型参数中的同名字段必须忽略或拒绝，模型不能提升权限。 | M2 |
| R-05 | Plan / Review 只向模型暴露只读工具和结构化提交工具，并对写操作硬性 Deny；审批不能放开硬性禁止。 | M2 |
| R-06 | Ask 必须先展示实际工具、参数、目标与任务；批准后凭单次 ApprovalToken 对原请求重新授权，参数或安全上下文变化即失效。 | M5 |
| R-07 | Deny、审批拒绝和审批取消时，工具实现调用次数为零。 | M2 |
| R-08 | Tool Executor 是工具名、参数 Schema、参数大小和格式的权威校验边界；非法参数不得进入工具实现。 | M2 |
| R-09 | 能力标签与风险等级由应用注册，只描述工具“能做什么”，不构成授权；模型不能伪造或下调。 | M2 |
| R-10 | 文件路径必须在 Workspace 内解析真实路径，拒绝越界、符号链接跳出和设备文件；不得用字符串前缀判断归属。 | M2 |
| R-11 | 写入 / 编辑前必须已读取目标文件并校验内容哈希，文件变化时返回 StaleFileError 要求重读。 | M5 |
| R-12 | 写入使用临时文件 + rename 原子替换；单次编辑全有或全无，禁止部分应用。 | M5 |
| R-13 | 命令使用可执行文件与参数数组，不默认 Shell；仅白名单，固定工作目录、受控环境、超时、取消和输出上限。 | M6 |
| R-14 | 策略约束不等于系统隔离：子进程继承主机权限，Workspace 和网络拒绝都不是系统级强制边界。 | M6 |
| R-15 | Loop 轮数、任务时间、Token 和工具调用次数必须有上限；取消覆盖模型请求、审批等待和工具执行。 | M3 |
| R-16 | Tool Call 与其全部 Tool Result 是协议单元，按 tool_call_id 回填；不得产生孤立消息或伪造结果。 | M3 |
| R-17 | 文件内容、命令输出和工具结果有字节与 Token 上限，截断必须标记并保留原始大小。 | M3 |
| R-18 | 审计记录决策、RuleID、目标摘要和结果，不记录密钥与敏感内容。 | M5 |
| R-19 | 敏感文件（如 `.env`、`*.pem`、`id_rsa`）默认不读取、不外发给模型；确需读取时由策略显式放行。 | M2 |

## 3. 模块设计

### 3.1 CLI

职责：
- 解析子命令、参数、工作目录和配置路径。
- 创建 Session / Task 上下文。
- 调用 Mode Controller。
- 将 Agent 输出、审批提示和错误呈现给用户。

建议命令：
```bash
coding-agent "为项目增加配置加载"
coding-agent plan "设计配置加载模块"
coding-agent review ./                 # 审查指定路径的文件内容
coding-agent review --base main        # 审查相对 main 的 Diff
coding-agent --debug "读取 go.mod"     # 逐轮打印请求、响应与 Token 统计
```

Review 作用域二选一：位置参数（默认 `.`）表示审查该路径下的文件内容；`--base <ref>` 表示审查该 Git 引用到工作区的 Diff。两者互斥，同时给出时 CLI 直接报错退出，不进入 Agent Loop。

CLI 约定：
- 退出码：`0` 任务完成；`1` 任务失败或被拒绝；`2` 参数或配置错误；`130` 用户取消。具体细分在 M1 固化。验证退出码必须使用编译后的二进制：`go run` 会把子进程的非零退出码统一掩盖为 1，拿 `go run` 测 130 永远测不出真相。
- `--debug` 把每轮完整请求与响应、Token 估算与累计、上下文窗口占用占比、工具耗时、带币种的累计成本写入 stderr（脱敏后），与审计日志分离。
- 交互式审批仅在 TTY 下启用；非 TTY 环境下 Ask 一律按拒绝处理。

CLI 不直接访问文件系统工具或启动命令工具；它只负责启动应用流程，并通过 `Approver` 与 `AuditSink` 接口把交互和日志注入给 Executor。

M1 配置边界（固化 §3.1 中“具体细分在 M1 固化”的约定）：配置文件（`configs/config.yml`）只承载模型与预算配置——模型名、请求超时、`MaxOutputTokens`、`max_context_tokens`（模型上下文窗口，见 §3.6）、`currency` 与单价（成本估算用）；API Key 只从环境变量读取，不进入配置文件。三个 Token 相关量必须分清：上下文窗口（模型物理属性，input 与 output 总量上限）、输出预留（`MaxOutputTokens`）和可用输入预算；**可用输入预算不单独配置，由前两者派生**（公式见 §3.6），否则两个独立配置的数值可以相互矛盾却不报错。配置加载必须校验 `max_context_tokens > MaxOutputTokens`，不满足即启动失败。权限策略不进配置文件（与 §3.10 一致：MVP 规则集以代码内规则表定义）。可选兼容中转端点通过环境变量（如 `OPENAI_BASE_URL`）覆盖默认 Base URL，供 Provider 使用。

成本展示的两个约束：（1）单价**必须与 `currency`（`USD` / `CNY`，默认 `USD`）一起声明**，否则 OpenAI（美元计价）与 DeepSeek（人民币计价）的单价会被相加成一个不带任何意义且看起来精确的数字；`currency` 只接受已登记枚举，其他写法启动即失败。（2）单价**未配置时不得默认为 0**：0 单价会算出“成本 0.00 元”这种伪装精确的假数，必须显式标注“单价未配置，无法估算成本”。取什么单价：M1 无法感知提示缓存命中与时段价，应取**最贵一档**（宁可高估，不高估会让预算判断失真）。

### 3.2 Mode Controller

将 CLI 输入转换为应用认可的 `AgentMode`。模式只能由 CLI / 应用配置确定，不接受模型通过 Tool Call 参数更改。

```go
type AgentMode string

const (
    ModeExecute AgentMode = "execute"
    ModePlan    AgentMode = "plan"
    ModeReview  AgentMode = "review"
)
```

模式同时决定两件事：Permission Layer 允许的能力范围，以及任务级工具视图中向模型暴露的工具集合。Plan / Review 的工具视图只包含只读工具和 `submit_plan` / `submit_review`；即使模型伪造写工具调用，也会因 Registry 查找失败或权限层硬性拒绝而返回结构化错误。模式只能由应用选定，模型不能通过参数或自然语言改变。

### 3.3 Execution Context

在一次任务执行期间传递可信的安全上下文。模型不能创建或覆盖此结构。

```go
type ExecutionContext struct {
    SessionID  string
    TaskID     string
    Mode       AgentMode
    Workspace  string
    PolicyID   string
    DryRun     bool
}
```

字段说明：
- `SessionID`：一次 CLI 会话标识。
- `TaskID`：当前任务标识，便于日志和审计关联。
- `Mode`：应用选定的工作模式。
- `Workspace`：经解析和校验的工作区根路径。
- `PolicyID`：当前生效的规则集标识，写入审计事件。MVP 的规则集以代码内规则表定义，与代码版本对应；策略配置文件和热更新属于后续增强。
- `DryRun`：预演开关，由 CLI 或配置设置，模型不能开启。开启后权限判定照常执行，写操作与命令不实际生效，Executor 返回将要发生的变更（写入内容、Diff 或命令 argv）。它不能替代真正的权限控制。

实现要求：
- Context 由应用层创建，向下传递时使用值或只读接口。
- 不把 `ExecutionContext` 原样暴露给模型。
- 工具参数中即使出现 `mode=execute` 或其他 Workspace，也必须忽略或拒绝。
- 不允许 Agent 在 Plan / Review 过程中自行改变 Mode。

### 3.4 Model Provider

MVP 实现 OpenAI Chat Completions Provider。内部接口应避免把 OpenAI SDK 类型泄漏到 Agent Core。

接口与内部协议类型示例：
```go
type Provider interface {
    Generate(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

type ModelRequest struct {
    Messages        []Message
    Tools           []ToolSchema
    MaxOutputTokens int
}

type ModelResponse struct {
    Message      Message
    FinishReason string
    Usage        Usage
}

type Message struct {
    Role       MessageRole // system / user / assistant / tool
    Content    string
    ToolCalls  []ToolCall  // 仅 assistant
    ToolCallID string      // 仅 tool，回填时使用
    Name       string      // 仅 tool，便于调试阅读
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments json.RawMessage
}

type ToolResult struct {
    CallID        string
    Content       string
    IsError       bool
    Truncated     bool
    OriginalBytes int
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
}

type MessageRole string

const (
    RoleSystem    MessageRole = "system"
    RoleUser      MessageRole = "user"
    RoleAssistant MessageRole = "assistant"
    RoleTool      MessageRole = "tool"
)

// 仅包含构造提示词所需的字段，不携带 Risk / Capability 等策略元数据。
type ToolSchema struct {
    Name        string
    Description string
    Parameters  json.RawMessage
}
```

设计注意：
- 统一内部 Message / ToolCall / ToolResult 类型，Provider 负责 API 请求与内部类型之间的转换。Tool Call 统一从 `Message.ToolCalls` 读取，不另设重复字段，避免两份数据不一致。
- ToolResult 写入历史时序列化为 `role=tool` 消息：`ToolCallID` 对应 `tool_call_id`；Chat Completions 没有独立错误字段，`IsError` 编码进 `Content` 前缀（如 `ERROR: ...`），并在系统指令中说明该约定。
- `OriginalBytes` 记录截断前大小，截断后内容必须带明确标记，不能伪装成完整结果。
- MVP 请求固定 `parallel_tool_calls=false`，与串行执行一致；不要依赖模型自觉。
- `Usage` 优先取 API 返回；缺失时使用估算值，并在 debug 输出中标注来源。成本 = Usage × 配置单价，按轮累计，仅用于展示与预算，不作为授权依据。累加时必须携带 `currency` 币种；单价未配置时输出“未配置”而非 0（见 §3.1）。
- 设置请求超时、最大响应体、错误分类和有限重试。
- 只对可安全重试的错误进行重试；工具执行不能因为模型请求重试而重复执行。
- API Key **只从环境变量读取**（与 §3.1 一致：MVP 不支持把密钥写进配置文件或任何本地密钥文件），不写入仓库、不进入日志。默认 Base URL 指向 OpenAI，可通过环境变量覆盖为兼容 Chat Completions 的中转或自托管端点；MVP 仍要求密钥非空，因此无鉴权的本地端点属后续增强（见 §8）。
- 后续 Responses API 通过新增 Provider 或适配器接入，不重写 Agent Loop。

### 3.5 Agent Core 与 Agent Loop

Agent Core 维护单次任务状态，通过 Context Manager 构造模型请求，通过 Provider 获取响应，并将 Tool Call 统一提交给 Executor。MVP 一个任务只有一个 Agent Loop，不创建子 Agent。

典型循环：
1. 构造系统指令和当前任务消息，检查取消信号和初始预算。
2. 由 Context Manager 生成符合消息与 Token 预算的请求视图。
3. 调用 Provider，并将完整 Assistant Message 追加到历史，包括文本和全部 Tool Call。
4. 若响应包含 Tool Call，先检查调用 ID、工具名字段和参数 JSON 基本语法，再将调用提交给 Executor。
5. MVP 按模型返回顺序串行处理多个 Tool Call；每个结果都按原 `tool_call_id` 追加到历史，不能产生孤立或错配的工具消息。
6. 若响应不包含 Tool Call 且包含最终文本，输出任务总结并结束；需要用户澄清时明确等待用户输入。
7. 每次模型调用和工具执行后检查轮数、Token / 时间预算、工具调用次数和取消信号，再继续循环。

系统指令与工具描述：
- 系统指令声明指令层级：只有系统指令与用户任务属于指令；文件内容、命令输出和其他工具结果都是待分析的数据，不能改变 Mode、权限或任务目标。
- 工具描述面向模型编写：说明何时使用、何时不要使用、参数含义与边界，而不是只写函数签名。描述质量直接决定误调用率。
- 工具错误信息应可纠正：指出哪个参数不合法、期望什么，使模型能在剩余预算内重试。
- 系统指令与工具 Schema 视作稳定前缀，尽量不在同一任务内反复变化，便于缓存与调试。

响应与错误处理规则：
- 文本和 Tool Call 可以同时出现；此时文本不是最终答案，必须先处理全部 Tool Call。
- 未知工具、非法参数、权限拒绝、审批拒绝和普通工具执行失败，应转换为结构化 Tool Result 反馈给模型，使 Agent 在剩余预算内纠正或说明失败。
- 如果调用协议损坏到无法形成合法的 Tool Result 对应关系，应终止本次请求并返回 ValidationError，不能伪造工具结果。
- Provider 鉴权失败、不可重试请求错误、预算耗尽和用户取消属于任务级终止条件；可重试 Provider 错误只在 Provider 内有限重试。
- `finish_reason=length`、空响应或内容过滤表示本次生成未正常完成，不能当作任务成功；应按错误与预算策略终止或返回可诊断错误。
- 模型最终文本应区分已完成、部分完成和未完成，并列出实际执行过的验证，不能仅凭模型声称成功判定验收通过。

参数校验职责必须统一：Agent Core 只进行 Tool Call 外层结构和 JSON 基本语法的初步检查；Tool Executor 是工具名、参数 Schema、参数大小和具体格式的权威校验边界。初步检查不能替代 Executor 校验，非法参数绝不能进入工具实现。

必须设置：
- 最大 Agent Loop 轮数。
- 单任务时间预算和 Token 预算。
- 单次模型请求超时。
- 工具调用次数上限。
- 用户取消（`context.Context`），覆盖模型请求、审批等待和工具执行。
- 重复工具调用检测或循环告警。

模型返回的 Tool Call 是不可信输入，不能直接反序列化后执行。MVP 不并行执行 Tool Call，以避免结果错配、重复审批和资源竞争；后续如引入并行，必须先定义取消、结果顺序和审批语义。

### 3.6 Context Manager

Context Manager 负责单次任务的消息状态和每次模型请求的上下文视图，不负责权限决策。MVP 采用进程内消息历史，不实现跨进程 Session 恢复、检索增强或自动摘要。

主要职责：
- 保存系统指令、用户任务、Assistant Message、Tool Call 和 Tool Result 的顺序及对应关系。
- 构造包含工具 Schema 的请求视图，并为模型响应保留输出 Token 空间。
- 根据 Provider / 模型配置估算 Token；无法准确计算时使用保守估计，不能假设字符数等于 Token 数。
- 对文件内容、命令输出和单个 Tool Result 设置字节与 Token 上限，保留明确的截断标记和原始结果大小，不能把截断内容伪装成完整结果。
- 统计输入消息、工具 Schema 和输出预留的共同预算，预算分母是配置的模型上下文窗口；超限前返回 ContextLimitError 或按任务策略终止，而不是无限追加历史。
- 裁剪或未来压缩历史时，将 Tool Call 与其全部 Tool Result 作为不可分割的协议单元，禁止留下孤立消息。

文件内容、命令输出和其他工具结果均属于不可信数据，不能改变系统指令、Mode、Workspace 或 Permission Policy。审计日志中的上下文摘要仍需脱敏，并遵守输出大小限制。

预算分母的来源：共同预算以配置项 `max_context_tokens`（模型上下文窗口）为上限，按

```text
可用输入预算 = max_context_tokens - MaxOutputTokens - 工具 Schema 估算 - 安全余量（建议窗口的 5%）
```

派生。可用输入预算是**计算值，不是第三个配置旋钮**：两个独立配置的数值可以矛盾（预算大于窗口）而加载时无法发现，只在长对话中以 API 超限错误暴露；派生则从结构上排除这类矛盾。安全余量必需，因为 Token 估算永远不精确（见上条“保守估计”）且误差随历史长度累积。配置加载时校验 `max_context_tokens > MaxOutputTokens`，否则属于纯配置错误，启动即失败。

生效阶段：M1 只承载该配置、完成上述校验并在 `--debug` 展示窗口占用占比；裁剪、保守估算与 ContextLimitError 在 M3 随 R-15、R-17 一起落地，M1 不得提前实现裁剪逻辑。

后续可以在单 Agent 范围内增加摘要压缩和会话持久化，但必须先通过协议配对、预算和截断测试。

### 3.7 Tool Registry

Registry 保存工具定义与执行函数的注册关系，但只负责登记与查找，不负责执行，也不做授权。注册阶段校验名称唯一、Schema 合法、能力标签非空且属于已知枚举。

```go
type ToolDefinition struct {
    Name         string
    Description  string
    Parameters   json.RawMessage
    Risk         ToolRisk
    Capabilities []ToolCapability
}

// 仅由 Registry 持有，可执行；不进入提示词与审计。
type ToolHandler func(ctx context.Context, args json.RawMessage, execCtx ExecutionContext) (ToolResult, error)

type Registration struct {
    Definition ToolDefinition
    Handler    ToolHandler
}
```

`ToolDefinition` 只含可序列化元数据，可安全用于构造提示词和审计事件；`Handler` 通过 Registry 的注册关系取得，只能由 Executor 调用。

建议能力标签：
```go
type ToolCapability string

const (
    CapabilityListFiles       ToolCapability = "list_files"
    CapabilityReadFile        ToolCapability = "read_file"
    CapabilitySearchCode      ToolCapability = "search_code"
    CapabilityReadGitDiff     ToolCapability = "read_git_diff"
    CapabilityWriteFile       ToolCapability = "write_file"
    CapabilityApplyPatch      ToolCapability = "apply_patch"
    CapabilityDeleteFile      ToolCapability = "delete_file"
    CapabilityMoveFile        ToolCapability = "move_file"
    CapabilityRunCommand      ToolCapability = "run_command"
    CapabilityRunTests        ToolCapability = "run_tests"
    CapabilityNetworkAccess   ToolCapability = "network_access"
    CapabilityProcessControl  ToolCapability = "process_control"
    CapabilityEmitOutput      ToolCapability = "emit_structured_output"
)
```

能力标签表示工具可能做什么，不是授权结论。一个工具可有多个能力标签；标签由应用注册，不由模型填写。

### 3.8 Tool Risk

```go
type ToolRisk string

const (
    RiskLow      ToolRisk = "low"
    RiskMedium   ToolRisk = "medium"
    RiskHigh     ToolRisk = "high"
    RiskCritical ToolRisk = "critical"
)
```

Risk 是策略输入和审计信息，不应单独决定 Allow。最终决策还要结合 Mode、Capabilities、参数、路径和副作用。风险等级由工具实现者定义，不能由模型下调。

### 3.9 Tool Executor

Tool Executor 是唯一执行入口。建议抽象：
```go
type ToolExecutor interface {
    Execute(
        ctx context.Context,
        execCtx ExecutionContext,
        call ToolCall,
    ) (ToolResult, error)
}
```

执行步骤：
1. 查 Registry 中的工具定义与 Handler。
2. 权威验证工具名、参数 Schema、参数大小和具体格式；Core 的初步检查不能替代此步骤。
3. 由已注册、无副作用的参数适配器生成 `OperationIntent` 和规范化参数摘要，再构造可信的 `AuthorizationRequest`。
4. 不携带 ApprovalToken 调用 Permission Layer，完成首次授权。
5. 对 `Deny` 返回结构化拒绝结果；对 `Ask` 通过 `Approver` 展示实际操作并等待用户审批，此时尚未执行工具。
6. 用户批准后取得单次 ApprovalToken，使用原请求和 Token 重新授权；只有再次获得 `Allow` 才执行。拒绝、取消或 Token 校验失败时不得执行。
7. 执行工具并限制运行时间、输出大小和资源。
8. 记录审计事件，返回结构化结果；可恢复的工具失败通过 Tool Result 反馈，基础设施错误通过 Go `error` 返回。

不得：
- 在授权前运行有副作用的工具实现；参数适配器只能进行纯解析和规范化。
- 允许工具实现被 Agent Core 直接调用。
- 将模型给出的风险等级、能力标签或 Mode 当作可信数据。
- 因为工具名看似只读就跳过参数和路径检查。

Executor 通过接口注入交互与日志，不直接依赖终端或文件：
```go
type ApprovalRequest struct {
    ToolName    string
    Arguments   json.RawMessage
    Intent      OperationIntent
    Summary     string
    ExecContext ExecutionContext
}

type ApprovalDecision struct {
    Approved bool
    Reason   string
    Token    string // 仅 Approved 时有效，由 Approver 绑定该请求生成
}

type Approver interface {
    Request(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

type AuditSink interface {
    // AuditEvent 的字段见第 4 节。
    Record(ctx context.Context, event AuditEvent) error
}
```

CLI 提供终端实现，单元测试使用 Fake。审批拒绝与取消是正常返回路径，不是 Go `error`。批准时由 `Approver` 生成绑定该请求的 One-time ApprovalToken，Executor 不能自造 Token。

### 3.10 Permission Layer

建议使用三态决策：
```go
type PermissionDecision string

const (
    DecisionAllow PermissionDecision = "allow"
    DecisionDeny  PermissionDecision = "deny"
    DecisionAsk   PermissionDecision = "ask"
)

type OperationIntent struct {
    Paths          []string
    Executable     string
    Args           []string
    NetworkTargets []string
    SideEffects    []ToolCapability
}

type AuthorizationRequest struct {
    Definition    ToolDefinition
    Arguments     json.RawMessage
    Intent        OperationIntent
    ExecContext   ExecutionContext
    ApprovalToken string
}

type AuthorizationResult struct {
    Decision PermissionDecision
    Reason   string
    RuleID   string
}
```

`OperationIntent` 由工具适配器根据已通过权威校验的参数生成，模型不能直接填写。Permission Layer 使用该结构识别路径、命令和副作用，不负责猜测任意工具的原始 JSON 字段含义；`Arguments` 主要用于规范化摘要、审批绑定和审计。

职责：
- 根据 Mode、Capability、Risk、OperationIntent 和 ExecContext 计算决策。
- 默认拒绝未知能力、未知工具和无法安全判定的操作。
- 对需要用户确认的操作返回 Ask，并校验重新授权时携带的 ApprovalToken。
- 输出可解释的拒绝原因和命中规则 ID。
- 不执行工具、不修改文件、不启动进程。

权限优先级建议（固定顺序，先命中先生效）：
1. 未知工具、未知能力或未知 Mode：Deny。
2. 硬性禁止规则（Plan / Review 的写入、删除、命令等）：Deny，审批不可放开。
3. Workspace 路径和资源边界。
4. Mode 对 Capability 的允许范围。
5. Risk、参数、命令策略和副作用规则。
6. 用户审批规则（Ask）。
7. 无规则命中：Deny。

Deny 优先于 Ask：任一规则判定 Deny 后不再询问用户，也不允许审批覆盖。命中多条规则时以最先命中的规则为准，并记录 RuleID。

依赖方向：`permission` 可以引用 `tool` 的 `ToolDefinition`、`ToolCapability` 和 `ToolRisk` 类型；`tool` 不得引用 `permission`。Executor 位于独立的 `executor` 包，负责组装 `AuthorizationRequest` 并调起两侧，因此不存在包循环。

ApprovalToken 由审批组件生成，不能来自 Tool Call。它绑定工具名、规范化完整参数摘要、OperationIntent、TaskID、Mode、Workspace 和有效期，只能使用一次。重新授权必须使用原请求；任何参数或安全上下文变化都会使审批失效。首次 Ask、审批等待、审批拒绝和取消时，工具实现调用次数都必须为零。规范化和摘要函数只能在共享包中实现一份，Executor 与 Permission Layer 不得各自实现，否则审批绑定会静默失效。MVP 的单次使用记录保存在进程内，进程重启即失效。

Plan / Review 的只读约束属于硬性规则，普通审批不能把它临时升级为可写。若用户要修改，必须结束当前只读任务并显式启动 Execute Mode。

### 3.11 文件系统权限规则

最小实现建议：
- 启动时确定 Workspace 绝对路径。
- 所有文件工具只接受相对 Workspace 的路径，或将绝对路径解析后校验。
- `filepath.Clean` 只做词法清理，不足以防止符号链接越界。
- 需要解析现有路径的符号链接，并确认最终目标仍在 Workspace 内。
- 对新建文件，校验其父目录的真实路径；防止父目录符号链接跳出工作区。
- 拒绝空路径、设备文件、特殊文件和不支持的路径形式。
- 默认拒绝读取敏感文件模式（如 `.env`、`*.pem`、`id_rsa` 和云厂商凭据文件），避免内容被发送到模型服务；确需读取时由策略显式放行并记录审计。
- 不跟随工作区外的符号链接。
- 文件写入采用临时文件 + 原子替换时，也必须验证目标目录和权限。
- 注意 TOCTOU：检查路径与实际打开文件之间可能被替换。MVP 至少限制并发、拒绝可疑链接；更强保证需要平台相关的安全打开方式或沙箱。

不能仅用 `strings.HasPrefix(target, workspace)` 判断路径归属，因为 `/work/project-evil` 也可能具有 `/work/project` 前缀。应使用路径关系判断，并在必要时比较解析后的真实路径。

### 3.12 命令执行与进程安全

MVP 原则：
- 使用 `os/exec`，通过可执行文件路径和参数数组启动进程。
- 默认不使用 `sh -c`、`bash -c`、`cmd /c` 拼接模型生成的命令字符串。
- 初期命令采用显式白名单或受控工具（如测试工具），不开放任意命令。白名单之外不存在 Ask 路径：审批只能在白名单内对具体命令做二次确认。
- 固定并校验启动工作目录，并检查可执行文件与参数是否符合策略。
- 构造受控环境变量，避免把 API Key 等秘密传给子进程。
- 设置超时、取消、输出字节上限和并发上限。
- 进程取消时尽可能清理子进程；跨平台进程树清理需单独实现和测试。
- 网络访问在策略层默认拒绝；MVP 不提供放开路径，后续作为独立能力评估。
- 命令工具的能力标签应包含 `run_command`，但具体命令仍需按可执行文件、参数和副作用分类。

**策略约束不等于系统隔离。** 子进程继承当前用户的主机权限：`cmd.Dir` 只决定启动目录，不能阻止程序通过绝对路径访问 Workspace 之外的文件；参数数组只能避免默认 Shell 解析，不能限制程序自身行为；受控环境变量也不能阻止程序从其他位置读取秘密。因此，未使用沙箱时，不能把 Workspace 描述为子进程的文件系统边界。

同样，`network_access` 标签和 Permission Decision 只决定 Agent 是否被允许启动某项操作，不会关闭已启动进程的网络。需要强制文件系统或网络隔离时，必须使用容器、网络命名空间、沙箱或其他操作系统机制；这些属于后续增强。

MVP 只在临时测试仓库中运行显式白名单命令或专用测试工具，审批界面展示实际可执行文件和完整参数。不要把不可信仓库中的构建脚本、测试脚本或依赖安装脚本视为安全命令，也不要宣称 MVP 已具备主机级安全隔离。

示例：
```go
cmd := exec.CommandContext(ctx, "go", "test", "./...")
cmd.Dir = workspace
cmd.Env = safeEnv
stdout := &limitedBuffer{MaxBytes: 1 << 20}
stderr := &limitedBuffer{MaxBytes: 1 << 20}
cmd.Stdout = stdout
cmd.Stderr = stderr
err := cmd.Run()
```

注意：上例中的 `limitedBuffer` 需要自行实现；还要处理输出超限、进程退出码、超时与取消。`CommandContext` 会终止直接子进程，但进程树清理行为因操作系统而异。

### 3.13 Plan Mode

只读能力默认白名单：
- 列出工作区文件。
- 读取工作区文件。
- 搜索代码。
- 读取 Git 状态 / Diff（命令实现必须限定为只读子命令，或由专用 Git 工具实现）。

默认拒绝：
- 写入、Patch、删除、移动、重命名。
- 安装依赖、修改配置。
- 通用命令执行。
- 网络和外部系统操作。
- 任何不能证明无副作用的工具。

计划输出：
```go
type PlanStep struct {
    ID           string
    Description  string
    Files        []string
    Dependencies []string
    Verification string
}

type ExecutionPlan struct {
    Summary             string
    Goals               []string
    NonGoals            []string
    Steps               []PlanStep
    Risks               []string
    Assumptions         []string
    OpenQuestions       []string
    EstimatedComplexity string
}
```

Plan 通过 `submit_plan` 工具提交：该工具仅在 Plan 模式的任务级工具视图中暴露，参数 Schema 即 `ExecutionPlan`，能力标签为 `emit_structured_output`（无文件与命令副作用），Executor 完成权威 Schema 校验后结束本任务。模型只返回自由文本而未提交结构体时，视为未完成：提示一次后按 ValidationError 终止，不解析自然语言。

用户确认 Plan 只表示认可方案，不自动批准执行。真正实施时必须新建 Execute Mode 任务并重新进行权限判断。

### 3.14 Review Mode

初期只读取代码或 Diff，不修改项目。报告字段建议：
```go
type ReviewFinding struct {
    Severity    string
    File        string
    StartLine   int
    EndLine     int
    Title       string
    Evidence    string
    Description string
    Impact      string
    Suggestion  string
    Confidence  float64
}
```

置信度是模型估计，不应被当作客观概率保证。报告应区分确定缺陷、潜在风险和需要人工确认的问题。

Review 通过 `submit_review` 工具提交（参数为 `[]ReviewFinding`），校验失败时把结构化错误回填给模型重试。文件与行号必须落在本次已读取内容的范围内；文件名未读过或行号越界的条目在提交时标记为 `Unverified`，不把幻觉行号当作证据。审查范围由 CLI 决定（路径或 `--base` Diff），模型不能自行扩大。

### 3.15 写入与编辑工具

M5 采用锚点替换，不做整文件覆盖，也不做模糊匹配。

```go
type EditFileArgs struct {
    Path      string `json:"path"`
    OldString string `json:"old_string"`
    NewString string `json:"new_string"`
}
```

规则：
- `old_string` 必须在文件中唯一匹配；零匹配或多匹配都返回结构化错误，要求模型提供更多上下文，不允许猜测替换位置。
- 先读后写：同一任务内必须已成功读取该文件，并记录读取时的内容哈希；写入前重新计算哈希，不一致返回 `StaleFileError`，要求重新读取后再编辑。
- 原子写入：在同目录写临时文件，`fsync` 后 `rename` 覆盖；失败不留半成品。
- 原子性：一次编辑要么整体生效，要么完全不生效，禁止部分应用。
- 变更以统一 Diff 展示给审批者；审批绑定的是“路径 + 该 Diff 摘要”。
- MVP 不注册删除、移动、重命名工具，这类调用按未知工具 Deny；unified diff 的 `apply_patch` 留到后续增强。

## 4. 错误处理与可观测性

错误至少区分：
- ProviderError：网络、鉴权、限流、服务端错误、响应解析错误。
- ValidationError：工具参数或 Schema 不合法。
- PermissionError：拒绝或审批未完成。
- ToolExecutionError：工具自身执行失败。
- TimeoutError / CanceledError：超时或用户取消。
- ContextLimitError：上下文或预算限制。

分类错误**由产生它的包定义并导出**（ProviderError 在 `provider` 包，携带 Category 与 Retryable；PermissionError 在 `permission` 包，余类推），不单独建共享错误包。跨层传递必须用 `%w` 保留错误链：上层用 `errors.As` 取分类、用 `errors.Is(err, context.Canceled)` 判定取消（退出码 130 依赖它），不得比较错误字符串。Provider 的重试严格限制在自身内部且只针对可重试分类（单次超时与鉴权失败不重试）；工具执行永远不因模型请求重试而重复执行。

审计事件建议包含：
- 时间、SessionID、TaskID。
- Agent Mode。
- 工具名称、能力标签、风险等级。
- 经过脱敏的目标路径和参数摘要。
- Permission Decision、Reason、RuleID。
- 执行耗时、退出码、结果大小和错误类别。

```go
type AuditEvent struct {
    Time       time.Time
    SessionID  string
    TaskID     string
    Mode       AgentMode
    ToolName   string
    Risk       ToolRisk
    Target     string // 脱敏后
    ArgSummary string // 脱敏后
    Decision   PermissionDecision
    RuleID     string
    Reason     string
    Duration   time.Duration
    ExitCode   int
    ResultSize int
    ErrorClass string
}
```

不得记录 API Key、访问令牌、完整敏感环境变量或未经筛选的敏感文件内容。

调试输出与审计分离：`--debug` 面向开发者，把每轮完整请求与响应、Token 估算和工具耗时写入 stderr（脱敏后）；`AuditSink` 面向合规，记录上述结构化事件。两者都不得记录密钥、完整敏感文件内容或未脱敏的命令输出。

每轮记录 `Usage` 与累计成本估算（带 `currency` 币种，单价未配置时标注未配置）；成本仅用于展示和预算，不作为权限依据。

## 5. 测试策略

### 5.1 单元测试

- 配置加载：默认值 → 文件 → 环境变量 的优先级、严格解析（未知项 / 空文件 / 多文档）、Token 预算关系校验（窗口 > 输出预留）与数量级上限、币种枚举、输入归一化（空白、路径结尾 `/`），以及密钥不进入任何可打印输出。
- Provider 请求/响应转换与错误分类。
- Tool 参数 Schema 校验，以及 Core 初步检查与 Executor 权威校验的职责边界。
- Registry 重名、未知能力和非法定义。
- Permission Policy 各种 Mode / Capability / Risk 组合。
- OperationIntent 构造、审批绑定和 ApprovalToken 单次使用规则。
- Context Manager 的 Token 预算、输出截断和 Tool Call / Tool Result 协议配对。
- 路径规范化、越界路径和符号链接处理。
- 输出限流 Buffer、超时和取消逻辑。
- 锚点编辑的唯一性、先读后写、内容哈希过期和原子写入。
- Approver / AuditSink 的 Fake 实现与 Executor 拒绝路径。

### 5.2 集成测试

- 模拟 Provider 驱动 Agent Loop，覆盖文本与 Tool Call 同时返回、多个 Tool Call 串行执行和最终文本结束。
- Tool Call → Executor → Permission → Tool 的完整链路。
- Allow / Deny / Ask 三种决策路径；Ask 批准后重新授权才执行，拒绝和取消不执行。
- 未知工具和非法参数作为 Tool Result 反馈，且不会进入工具实现。
- 普通工具执行失败作为 Tool Result 反馈，Agent 可在剩余预算内恢复或报告未完成。
- Plan / Review 模式下写入工具不会被调用。
- 命令工具的工作目录、环境变量、超时和输出限制。
- `submit_plan` / `submit_review` 的 Schema 校验、失败回填与未提交处理。
- 审批拒绝、取消、Token 重用和参数变化均不执行工具实现。

### 5.3 安全回归测试

至少覆盖：
- `../` 路径穿越。
- 绝对路径访问工作区外文件。
- 指向工作区外的符号链接。
- 新建文件时父目录符号链接越界。
- 未知工具、未知能力和畸形参数。
- 敏感文件模式（`.env`、`*.pem`、`id_rsa`）读取被拒绝。
- Plan / Review 请求写文件、删除文件和运行命令。
- Shell 元字符、管道、重定向和命令注入尝试。
- 超时、取消、输出洪泛和子进程残留。
- Permission Deny 时确认工具实现调用次数为零。

测试应使用临时目录和伪造工具，不要在真实用户项目或主机敏感目录上运行破坏性测试。

### 5.4 任务级评测

单元和集成测试通过不代表 Agent 能完成任务。MVP 至少建立一组小型、可重复的固定仓库任务：

- 只读任务：读取项目文件并给出包含证据的总结或审查结果。
- 修改任务：在临时 Git 仓库完成小改动，检查最终 Diff 和实际测试结果，而不是检查模型是否声称成功。
- 恢复任务：注入非法参数、工具失败或权限拒绝，检查 Agent 是否在预算内纠正或明确报告未完成。
- 边界任务：检查 Loop 是否终止、是否越权、是否产生孤立工具消息，以及是否遵守输出和 Token 上限。

逻辑回归使用确定性 Fake Provider；少量真实模型任务用于冒烟评测，不断言完全相同的自然语言文本。记录成功率、工具调用次数、耗时和 Token 使用量，作为后续修改的基线。

## 6. 推荐目录结构

```text
.
├── README.md
├── docs/
│   ├── TECHNICAL_DESIGN.md
│   └── ARCHITECTURE.md
├── cmd/coding-agent/main.go
├── internal/
│   ├── cli/
│   ├── config/
│   ├── agent/
│   ├── provider/
│   ├── ctxmgr/
│   ├── tool/
│   ├── executor/
│   ├── permission/
│   └── audit/
├── configs/
└── go.mod
```

包职责与架构组件一一对应：`cli` 只做参数、渲染和审批交互；`config` 负责模型与策略配置；`agent` 包含 Mode Controller、Agent Loop 与 Plan / Review 的结构定义；`provider` 包含 Chat Completions 与 Fake Provider；`ctxmgr` 是 Context Manager（避开标准库 `context` 同名）；`tool` 包含 Definition、Registry 和内置工具；`executor` 是唯一执行入口，负责参数校验、授权调用、审批编排与结果封装；`permission` 只做决策，可引用 `tool` 的类型，但 `tool` 不得引用 `permission`；`audit` 定义事件与 Sink。Plan / Review 是工作模式，不再单独建包。

## 7. 纵向实施路线与阶段验收

不按周次拆分大量横向模块，而以可运行闭环组织学习。每个阶段都保留上一个阶段的演示和回归测试，完成 M6 即形成包含 Plan / Review 的单 Agent MVP。

| 阶段 | 学习与实现内容 | 阶段验收 |
|---|---|---|
| M1：单轮模型调用 | Go 骨架、CLI、配置、日志、Chat Completions Provider、Fake Provider、`--debug` | CLI 可完成一次真实单轮请求；Mock 覆盖成功、超时和错误分类；密钥不进入日志；`--debug` 可见完整上下文、`Usage` 与累计成本。M1 无工具，只发送用户消息并返回文本；退出码与配置字段按 §3.1 在 M1 固化。M1 尚未引入 Agent Core 与 Mode Controller，由 CLI 直接调用 Provider（临时形态）；M2 起该调用改由 Agent Core 组织，M1 不得提前搭建 Loop。 |
| M2：最小只读闭环 | Tool Schema、Registry、Executor、基础 Permission Layer、基础 Agent Loop、读取 / 列表工具、Workspace 基础约束 | Fake Provider 驱动 Agent 读取 `go.mod` 并总结项目；Tool Result 按 ID 回填；非法参数不会进入工具实现；Loop 有轮数上限 |
| M3：Loop 与上下文加固 | 多 Tool Call 串行处理、错误反馈、取消、预算、进程内 Context Manager、输出截断 | 文本与 Tool Call 同时返回可正确处理；无孤立工具消息；超长输出受控；Token / 时间 / 调用预算和用户取消测试通过 |
| M4：Plan / Review | Mode Controller、工具视图过滤、Capability 策略、只读硬拒绝、`submit_plan` / `submit_review` | 同一 Agent 可完成只读规划和审查；提交结果经 Schema 校验；伪造写入或命令请求被 Deny 且实现调用次数为零；确认 Plan 不会自动进入 Execute |
| M5：受控写入与审批 | 锚点编辑、先读后写、哈希校验、Allow / Deny / Ask、ApprovalToken、审计、Diff 展示 | Ask 等待期间不执行；批准后对原操作重新授权；拒绝、取消和参数变化均不执行；StaleFileError 与原子写入测试通过；能在临时 Git 仓库完成小修改并展示 Diff |
| M6：命令与 MVP 验收 | 白名单测试命令、进程限制、安全回归、端到端整合和任务级评测 | 在临时仓库完成“修改—验证—总结”；命令超时、取消和输出限制有效；全部 MVP 测试与固定任务评测通过 |

每个阶段先实现最小功能，再补对应测试；不要为后续阶段提前搭建独立 Agent 或调度框架。

## 8. MVP 与后续增强边界

以下清单用于 M6 验收；条目定义见 2.2 规范条目表。

### MVP 必须具备

- 单 Agent CLI、Chat Completions Provider 和可测试的 Agent Loop。
- 多 Tool Call 串行处理、Tool Result ID 对应、错误反馈、轮数 / 时间 / Token / 调用预算和取消。
- 进程内 Context Manager、工具输出截断和 Tool Call / Tool Result 协议配对保护。
- 工具执行唯一入口，Executor 承担权威参数校验，非法参数不会进入工具实现。
- Mode 由应用程序确定且不可由模型修改，Plan / Review 纳入 MVP 且写操作硬拒绝。
- 未知工具 / 能力默认拒绝，Workspace 路径约束和基本符号链接防护。
- 受控写入 / 锚点编辑、Ask 审批、原操作重新授权和单次 ApprovalToken 绑定。
- 写入前读取与内容哈希校验、锚点唯一匹配、原子写入与编辑原子性。
- 只读模式仅暴露只读工具与结构化提交工具；Plan / Review 结果经 `submit_plan` / `submit_review` 校验。
- `--debug` 逐轮输出请求、Token 与耗时；Approver 与 AuditSink 可替换（CLI / Fake）。
- 命令参数数组执行，不默认 Shell；命令超时、取消、启动目录约束和输出上限。
- 明确命令工具不提供主机级文件系统或网络隔离，只在临时测试仓库开放白名单操作。
- Permission 单元测试、关键安全回归测试、端到端集成测试和最小任务级评测。

### 后续增强（仍为单 Agent）

- OpenAI Responses API Provider。
- 流式输出（SSE）与增量 Tool Call 拼接。
- Context 摘要压缩、会话持久化和更精确的模型 Token 计算。
- 稳定前缀组织与 Prompt 缓存优化。
- 操作系统级沙箱、容器隔离。
- 更完善的 TOCTOU 防护与平台专属安全文件 API。
- 网络代理、域名白名单和系统级网络隔离。
- 无鉴权的本地 / 自托管端点（当前要求 API Key 非空），以及零单价（免费模型）与“单价未配置”的区分。
- 细粒度用户审批 UI、策略配置文件和策略热更新。
- 资源配额、受控工具并行和进程树治理。

## 9. 版本说明

**V1.2** 补充 Agent Loop 响应与错误语义、最小 Context Manager、Ask 重新授权、OperationIntent 和纵向实施路线，统一 MVP 与参数校验边界，并澄清命令执行不具备主机级隔离。当前路线保持单 Agent。

本稿不代表功能已经实现。文档中的 Go 结构体和接口为设计草案，编码时应通过测试验证并允许小幅演进。
