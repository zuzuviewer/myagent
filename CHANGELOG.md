# Changelog

本项目变更日志，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

约定：

- **发布单位是里程碑（M1–M6）**，与 `docs/TECHNICAL_DESIGN.md` §7 的纵向实施路线一致；开始打 Git tag 后改用版本号。
- 本文件是**变更事件流水账**；各文档末尾的「版本说明」仍是该文档的**最新设计快照**，两者不互相替代。
- 设计文档基线 V1.0–V1.2 处于未发布阶段，按当时约定未逐条留痕，自 M1 起由本文件承接记录。

## [未发布]

### Added

- 本变更日志（M1 交付后建立）。

## [M1] - 2026-11-03

单轮模型调用：CLI 可完成一次真实请求，错误可诊断，逐轮可见完整上下文、Token 与耗时。已推送 `main`（`1e5159a`…`c3745cb`，5 个提交）。

### Added

- **config 包**：默认值 → 配置文件 → 环境变量 三层优先级；严格解析（未知项、空文件、纯注释文件、多文档一律启动即失败）；`max_context_tokens > max_output_tokens` 等预算关系校验；输入归一化（空白、路径结尾 `/`、币种大小写）；`Redact()` 掩码密钥。
- **provider 包**：内部协议类型（`Message` / `ToolCall` / `ToolResult` / `Usage` / 导出的 `FinishReason*`）与 Chat Completions wire 类型隔离，SDK 字段不外溢；`ChatCompletions` Provider（每请求 context 超时、`ProviderError` 错误分类、有限重试并尊重 `Retry-After`、响应体上限、非 200 时带出服务端诊断且不含密钥）；`Fake` 测试替身与真实实现遵守同一份契约。
- **cli 包与入口**：退出码 `0` / `1` / `2` / `130`；正文只走 stdout、诊断与 `--debug` 走 stderr；`--debug` 输出请求、响应、Token 来源标注（`api` / `estimated`）、上下文窗口占比与带币种的成本；`--provider fake` 支持零成本本地联调；系统指令显式声明"无工具能力"以避免模型编造读取结果。
- **scripts/**：`test-all.sh`（gofmt → vet → build → test → 覆盖率）、`build.sh`（产出 `bin/coding-agent` 并自检）、`smoke.sh`（用二进制断言 8 个退出码场景）。
- `go.mod` / `go.sum` 与 `configs/config.yml`（DeepSeek 兼容端点配置，不含密钥）。
- 测试：57 个顶层用例，总覆盖率 94.0%。

### Changed

- 文档固化 M1 配置边界：新增 `max_context_tokens`，并明确**可用输入预算由窗口与输出预留派生，不单设为第三个配置项**。
- 成本口径：单价必须与 `currency`（`USD` / `CNY`）绑定；未配置时显示"无法估算"而非 0；M1 无法感知缓存与时段，取最贵一档以避免低估。
- §4 明确分类错误**由产生它的包定义并导出**，跨层传递用 `%w` 保留错误链，上层用 `errors.As` / `errors.Is(context.Canceled)` 判定。
- Go 最低版本统一为 1.26（`go.mod` 的 `go` 指令与 README 同步，修改需两处一起）。
- README 运行示例按里程碑标注：M1 无工具、M2 起可读文件、M4 起可用 Plan / Review。

### Fixed

- 文档：README 的 M1 示例曾要求"读取 go.mod 并总结"，而 M1 并不具备工具能力，会误导首周验证；已按里程碑拆分示例。
- 文档：§3.4 曾写"API Key 从环境变量或本地密钥配置读取"，与 §3.1 / README 的"只从环境变量读取"冲突，实现者选哪边都"有据"；已统一为只从环境变量。
- 文档：记录 `go run` 会把子进程非零退出码统一掩盖为 1 的验证陷阱（§3.1），退出码验证必须使用编译后的二进制。

## 里程碑进度

- [x] M1 单轮模型调用 —— 2026-11-03，`c3745cb`
- [ ] M2 最小只读闭环
- [ ] M3 Loop 与上下文加固
- [ ] M4 Plan / Review
- [ ] M5 受控写入与审批
- [ ] M6 命令与 MVP 验收
