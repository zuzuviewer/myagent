# Go AI Coding Agent — 技术设计文档

> 文档基线：**V1.1**（技术细节增强修订稿）

## 1. 项目范围与非目标

### 1.1 项目目标

使用 Go 从零实现一个学习型、可扩展的 Coding Agent。首期以 CLI 单 Agent 为核心，完成模型交互、工具调用、Agent Loop、文件操作、受控命令执行、上下文管理和基础权限系统。

### 1.2 当前明确决策

- 首个模型 API：OpenAI Chat Completions。
- 后续模型 API：OpenAI Responses API。
- 当前不接入 Claude Code / Anthropic Messages API。
- MVP 为单 Agent。
- Plan Mode、Review Mode 先作为单 Agent 的模式实现。
- 独立 Plan / Review / Coding Agent 与 Orchestrator 属于后续阶段。

### 1.3 非目标

MVP 不追求完整 IDE、任意 Shell 自动化、任意目录访问、无人审批的高风险操作、复杂多 Agent 自治，也不试图一次实现生产级容器沙箱。

## 2. 总体设计原则

1. **边界优先：** 不可信模型输出不能直接成为系统权限。
2. **单一执行入口：** 工具只能由 Tool Executor 调用。
3. **策略与执行分离：** Permission Layer 决策，Executor 执行。
4. **默认拒绝：** 未知工具、未知能力、非法参数和无法判定的副作用默认拒绝。
5. **最小权限：** 每个任务只获得完成任务所需的最小能力和路径范围。
6. **可审计：** 记录工具、模式、目标、决策、原因和执行结果；避免记录密钥或敏感文件内容。
7. **可测试：** 权限规则必须能在不调用真实工具的情况下进行单元测试。

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
coding-agent review --base main
```

CLI 不直接访问文件系统工具或启动命令工具；它只负责启动应用流程。

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
- `PolicyID`：当前采用的权限策略标识。
- `DryRun`：预演开关；它不能替代真正的权限控制。

实现要求：
- Context 由应用层创建，向下传递时使用值或只读接口。
- 不把 `ExecutionContext` 原样暴露给模型。
- 工具参数中即使出现 `mode=execute` 或其他 Workspace，也必须忽略或拒绝。
- 不允许 Agent 在 Plan / Review 过程中自行改变 Mode。
- 子 Agent 将来只能获得父任务显式授予的能力子集。

### 3.4 Model Provider

MVP 实现 OpenAI Chat Completions Provider。内部接口应避免把 OpenAI SDK 类型泄漏到 Agent Core。

接口方向示例：
```go
type Provider interface {
    Generate(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

type ModelRequest struct {
    Messages []Message
    Tools    []ToolSchema
}

type ModelResponse struct {
    Message      Message
    ToolCalls    []ToolCall
    FinishReason string
}
```

设计注意：
- 统一内部 Message / ToolCall 类型。
- Provider 负责 API 请求与内部类型之间的转换。
- 设置请求超时、最大响应体、错误分类和有限重试。
- 只对可安全重试的错误进行重试；工具执行不能因为模型请求重试而重复执行。
- API Key 从环境变量或本地密钥配置读取，不写入仓库、不进入日志。
- 后续 Responses API 通过新增 Provider 或适配器接入，不重写 Agent Loop。

### 3.5 Agent Core 与 Agent Loop

典型循环：
1. 构造系统指令和当前任务消息。
2. 调用 Provider。
3. 若模型返回最终文本，结束或请求用户澄清。
4. 若模型返回 Tool Call，验证调用格式和参数。
5. 将调用提交给 Tool Executor。
6. 接收工具结果并追加到上下文。
7. 检查最大轮数、Token / 时间预算和取消信号。
8. 继续调用模型，直到结束或触发限制。

必须设置：
- 最大 Agent Loop 轮数。
- 单任务时间预算。
- 单次模型请求超时。
- 工具调用次数上限。
- 用户取消（`context.Context`）。
- 重复工具调用检测或循环告警。

模型返回的 Tool Call 是不可信输入，必须经过 JSON Schema / 参数校验，不能直接反序列化后执行。

### 3.6 Tool Registry

Registry 保存工具的静态元数据和执行函数的关联。注册阶段校验名称唯一、Schema 合法、能力标签非空且属于已知枚举。

```go
type ToolDefinition struct {
    Name         string
    Description  string
    Parameters   json.RawMessage
    Risk         ToolRisk
    Capabilities []ToolCapability
}
```

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
)
```

能力标签表示工具可能做什么，不是授权结论。一个工具可有多个能力标签；标签由应用注册，不由模型填写。

### 3.7 Tool Risk

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

### 3.8 Tool Executor

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
1. 查找 Registry 中的工具定义和实现。
2. 验证工具名、参数 Schema、参数大小和基本格式。
3. 生成可信的 `AuthorizationRequest`。
4. 调用 Permission Layer。
5. 对 `Deny` 返回拒绝结果；对 `Ask` 进入审批流程；只有 `Allow` 才执行。
6. 执行工具并限制运行时间、输出大小和资源。
7. 记录审计事件，返回结构化结果。

不得：
- 在授权前运行工具代码。
- 允许工具实现被 Agent Core 直接调用。
- 将模型给出的风险等级、能力标签或 Mode 当作可信数据。
- 因为工具名看似只读就跳过参数和路径检查。

### 3.9 Permission Layer

建议使用三态决策：
```go
type PermissionDecision string

const (
    DecisionAllow PermissionDecision = "allow"
    DecisionDeny  PermissionDecision = "deny"
    DecisionAsk   PermissionDecision = "ask"
)

type AuthorizationRequest struct {
    Definition  ToolDefinition
    Arguments   json.RawMessage
    ExecContext ExecutionContext
}

type AuthorizationResult struct {
    Decision PermissionDecision
    Reason   string
    RuleID   string
}
```

职责：
- 根据 Mode、Capability、Risk、参数和路径计算决策。
- 默认拒绝未知能力、未知工具和无法安全判定的操作。
- 对需要用户确认的操作返回 Ask。
- 输出可解释的拒绝原因和命中规则 ID。
- 不执行工具、不修改文件、不启动进程。

权限优先级建议：
1. 硬性禁止规则（例如 Plan / Review 写操作）优先级最高。
2. 工作区和路径边界检查。
3. 模式能力白名单。
4. 风险等级与命令策略。
5. 用户审批规则。
6. 无匹配规则时默认 Deny。

Plan / Review 的只读约束属于硬性规则，普通审批不能把它临时升级为可写。若用户要修改，必须结束当前只读任务并显式启动 Execute Mode。

### 3.10 文件系统权限规则

最小实现建议：
- 启动时确定 Workspace 绝对路径。
- 所有文件工具只接受相对 Workspace 的路径，或将绝对路径解析后校验。
- `filepath.Clean` 只做词法清理，不足以防止符号链接越界。
- 需要解析现有路径的符号链接，并确认最终目标仍在 Workspace 内。
- 对新建文件，校验其父目录的真实路径；防止父目录符号链接跳出工作区。
- 拒绝空路径、设备文件、特殊文件和不支持的路径形式。
- 不跟随工作区外的符号链接。
- 文件写入采用临时文件 + 原子替换时，也必须验证目标目录和权限。
- 注意 TOCTOU：检查路径与实际打开文件之间可能被替换。MVP 至少限制并发、拒绝可疑链接；更强保证需要平台相关的安全打开方式或沙箱。

不能仅用 `strings.HasPrefix(target, workspace)` 判断路径归属，因为 `/work/project-evil` 也可能具有 `/work/project` 前缀。应使用路径关系判断，并在必要时比较解析后的真实路径。

### 3.11 命令执行与进程安全

MVP 原则：
- 使用 `os/exec`，通过可执行文件路径和参数数组启动进程。
- 默认不使用 `sh -c`、`bash -c`、`cmd /c` 拼接模型生成的命令字符串。
- 初期命令采用显式白名单或受控工具（如测试工具），不开放任意命令。
- 固定并校验工作目录；不允许通过参数切换到 Workspace 之外。
- 构造受控环境变量，避免把 API Key 等秘密传给子进程。
- 设置超时、取消、输出字节上限和并发上限。
- 进程取消时尽可能清理子进程；跨平台进程树清理需单独实现和测试。
- 网络访问默认不开放；需要时作为独立能力授权。
- 命令工具的能力标签应包含 `run_command`，但具体命令仍需按可执行文件、参数和副作用分类。

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

### 3.12 Plan Mode

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

用户确认 Plan 只表示认可方案，不自动批准执行。真正实施时必须新建 Execute Mode 任务并重新进行权限判断。

### 3.13 Review Mode

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

## 4. 错误处理与可观测性

错误至少区分：
- ProviderError：网络、鉴权、限流、服务端错误、响应解析错误。
- ValidationError：工具参数或 Schema 不合法。
- PermissionError：拒绝或审批未完成。
- ToolExecutionError：工具自身执行失败。
- TimeoutError / CanceledError：超时或用户取消。
- ContextLimitError：上下文或预算限制。

审计事件建议包含：
- 时间、SessionID、TaskID。
- Agent Mode。
- 工具名称、能力标签、风险等级。
- 经过脱敏的目标路径和参数摘要。
- Permission Decision、Reason、RuleID。
- 执行耗时、退出码、结果大小和错误类别。

不得记录 API Key、访问令牌、完整敏感环境变量或未经筛选的敏感文件内容。

## 5. 测试策略

### 5.1 单元测试

- Provider 请求/响应转换与错误分类。
- Tool 参数 Schema 校验。
- Registry 重名、未知能力和非法定义。
- Permission Policy 各种 Mode / Capability / Risk 组合。
- 路径规范化、越界路径和符号链接处理。
- 输出限流 Buffer、超时和取消逻辑。

### 5.2 集成测试

- 模拟 Provider 驱动 Agent Loop。
- Tool Call → Executor → Permission → Tool 的完整链路。
- Allow / Deny / Ask 三种决策路径。
- Plan / Review 模式下写入工具不会被调用。
- 命令工具的工作目录、环境变量、超时和输出限制。

### 5.3 安全回归测试

至少覆盖：
- `../` 路径穿越。
- 绝对路径访问工作区外文件。
- 指向工作区外的符号链接。
- 新建文件时父目录符号链接越界。
- 未知工具、未知能力和畸形参数。
- Plan / Review 请求写文件、删除文件和运行命令。
- Shell 元字符、管道、重定向和命令注入尝试。
- 超时、取消、输出洪泛和子进程残留。
- Permission Deny 时确认工具实现调用次数为零。

测试应使用临时目录和伪造工具，不要在真实用户项目或主机敏感目录上运行破坏性测试。

## 6. 推荐目录结构

```text
.
├── README.md
├── docs/
│   ├── TECHNICAL_DESIGN.md
│   └── ARCHITECTURE.md
├── cmd/coding-agent/main.go
├── internal/
│   ├── agent/
│   ├── provider/
│   ├── context/
│   ├── tool/
│   ├── permission/
│   ├── execution/
│   ├── plan/
│   └── review/
├── configs/
└── go.mod
```

## 7. 开发路线与阶段验收

| 周次 | 工作 | 验收标准 |
|---|---|---|
| 1 | Go 项目骨架、CLI、配置、日志 | CLI 可运行，配置错误可诊断 |
| 2 | Chat Completions Provider | Mock 测试通过；真实 API 可完成一次请求 |
| 3 | Tool Schema / Registry / Tool Call 解析 | 非法参数不会进入执行器 |
| 4 | Agent Loop | 有轮数、超时、取消和预算限制 |
| 5 | 文件读取、列表、搜索与路径权限基础 | 越界路径和符号链接测试通过 |
| 6 | 受控写入、命令与测试工具 | 写操作经过授权；命令有超时和输出限制 |
| 7 | Context Manager、审批、审计和安全回归 | 关键权限路径可追踪、可测试 |
| 8 | 单 Agent MVP 整合 | 端到端任务可运行，失败可恢复 |
| 9 | Plan Mode / Review Mode | 只读保证通过安全回归测试 |
| 10+ | 独立 Agent 与 Multi-Agent | Agent 仅获得显式授权的能力子集 |
| 后续 | Orchestrator | 任务拆解、调度、交接和结果汇总 |

## 8. MVP 与后续增强边界

### MVP 必须具备

- 工具执行唯一入口。
- Mode 由应用程序确定且不可由模型修改。
- 未知工具/能力默认拒绝。
- Workspace 路径约束和基本符号链接防护。
- Plan / Review 写操作硬拒绝。
- 命令参数数组执行，不默认 Shell。
- 命令超时、取消、工作目录约束和输出上限。
- Permission 单元测试与关键安全回归测试。

### 后续增强

- 操作系统级沙箱、容器隔离。
- 更完善的 TOCTOU 防护与平台专属安全文件 API。
- 网络代理与域名白名单。
- 细粒度用户审批 UI、策略配置文件和策略热更新。
- 资源配额、并发调度和进程树治理。
- 多 Agent 的身份、能力委派和审计关联。

## 9. 版本说明

保持 **V1.1** 基线。本稿属于技术细节增强修订，不代表已经实现这些功能。文档中的 Go 结构体和接口为设计草案，编码时应通过测试验证并允许小幅演进。
