package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 只承载模型与预算配置（TECHNICAL_DESIGN §3.1 M1 配置边界）：
// 权限策略不进配置文件；API Key 只从环境变量读取，不进入配置文件与日志。
type Config struct {
	Model              string        `yaml:"model"`                 // 模型名
	Timeout            time.Duration `yaml:"timeout"`               // 单次模型 API 请求超时（非任务总时长）
	MaxContextTokens   int           `yaml:"max_context_tokens"`    // 上下文窗口：input 与 output 的总量上限（模型物理属性）
	MaxOutputTokens    int           `yaml:"max_output_tokens"`     // 单次请求最大输出 token（从窗口中预留）
	Currency           string        `yaml:"currency"`              // 单价币种，USD 或 CNY；成本必须带币种，跨币种相加无意义
	InputPricePerMTok  float64       `yaml:"input_price_per_mtok"`  // 输入单价，每 1M token，币种见 Currency；0 表示未配置
	OutputPricePerMTok float64       `yaml:"output_price_per_mtok"` // 输出单价，每 1M token，同上
	BaseURL            string        `yaml:"base_url"`              // Chat Completions 基址，由 Provider 拼 /chat/completions
	APIKey             string        `yaml:"-"`                     // 只从 OPENAI_API_KEY 读取；配置文件中的同名字段永不生效
}

// 支持的币种白名单。新增 Provider 时在此登记：一个不带币种、看起来精确的成本数字
// 比没有数字更容易骗人，所以宁可启动即失败，也不接受 "RMB"、"yuans"、"dollar" 这类写法。
var supportedCurrencies = map[string]bool{"USD": true, "CNY": true}

const (
	// maxOutputTokensCeiling 拦截数量级手滑：常见模型的**输出**上限在几十 K 量级
	//（DeepSeek 合法范围为 1~393216），写出 1024000 这类数字几乎一定是把上下文窗口
	// 误当成 max_output_tokens。上限取得比任何真实模型都宽，只用于拦截数量级错误。
	maxOutputTokensCeiling = 512 * 1024

	// defaultMaxContextTokens 是"配置文件完全没写窗口"时的兜底值 128K。
	// 取 128 * 1000 而不是 128 * 1024：主流模型标称的 128K 通常就是 128000，
	// 往小偏才能保证不会超出真实窗口（估大→API 硬失败，估小→我们自己提前限制）。
	// 但**没有任何默认值对所有模型都安全**（gpt-3.5-turbo 类只有 16K~32K），
	// 所以真实窗口必须在配置文件里显式写，这个默认值只用于兜住"完全未配置"。
	defaultMaxContextTokens = 128 * 1000

	// maxContextTokensCeiling 拦住把 K 写成 M 的手滑（例如 1024000000）。
	// 目前已知最大窗口是 1M 级，留 10 倍余量给未来模型。
	maxContextTokensCeiling = 10 * 1000 * 1000
)

// NewConfig 按 默认值 → 配置文件 → 环境变量 的优先级加载配置，完成校验后返回。
// 任何失败都由调用方（CLI）映射为退出码 2。
func NewConfig(configPath string) (*Config, error) {
	// 1. 默认值。Timeout 必须有默认：0 值无法与"用户显式配置"区分，缺省即请求立即超时。
	// 单价默认为 0 = "未配置"，绝不能给一个"看起来合理"的非零默认价：
	// 用美元默认价给人民币计费的模型算成本，输出的数字会精确地错。
	c := &Config{
		Model:              "gpt-3.5-turbo",
		Timeout:            30 * time.Second,
		MaxContextTokens:   defaultMaxContextTokens,
		MaxOutputTokens:    1024,
		Currency:           "USD",
		InputPricePerMTok:  0,
		OutputPricePerMTok: 0,
		BaseURL:            "https://api.openai.com/v1", // 基址，不含 /chat/completions 路径（Provider 负责拼路径）
	}

	// 2. 读取配置文件。错误用 %w 包装，保留 errors.Is/As 能力，并带上路径方便诊断。
	b, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", configPath, err)
	}
	// 空文件（或只有注释）必须报错：否则会把"配置丢了"误当成"用户没改默认值"，静默跑在默认配置上。
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("配置文件 %s 为空", configPath)
	}
	// 严格解析：未知配置项直接报错。拼错的 key（如 time_out）若被静默忽略，
	// 你以为配置生效了其实没有，这类问题极难排查，宁可启动即失败。
	// yaml.v3 已无 UnmarshalStrict，等价做法是 Decoder + KnownFields(true)。
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		// 文件字节非空但解不出文档（只剩注释）：单独报错，不混进"语法错误"，避免误导。
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("配置文件 %s 不含任何配置项（空文件或只有注释）", configPath)
		}
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", configPath, err)
	}
	// 拒绝多文档：`---` 后第二个有内容的文档若不检查会被静默丢弃，属于同一类隐患。
	// 用 yaml.Node 接住，结尾孤立的 `---`（空文档）不算违规。
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		// 正常：没有后续文档。
	case err != nil:
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", configPath, err)
	default:
		if !isEmptyDoc(&extra) {
			return nil, fmt.Errorf("配置文件 %s 含多个 YAML 文档，仅支持单文档", configPath)
		}
	}

	// 3. 环境变量覆盖，优先级最高。
	// OPENAI_BASE_URL 用于兼容中转 / 自托管端点（§3.4）。
	if v := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); v != "" {
		c.BaseURL = v
	}
	// API Key 只从环境变量读取；TrimSpace 防止 .env 拷贝时带入空白。
	c.APIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))

	// 4. 归一化后再校验：大小写、空白、结尾斜杠属于输入格式问题，不是错误，先规范化再判合法性。
	// 不归一化会把"看起来对但实际错"的值带给 Provider：model 带空白 → 404 找不到模型；
	// base_url 带结尾 / → 拼出 //chat/completions 的请求路径。
	c.Model = strings.TrimSpace(c.Model)
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")

	// 5. 校验。错误信息指出字段名与期望值，直接作为给用户看的诊断书。
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("配置无效: %w", err)
	}
	return c, nil
}

// isEmptyDoc 判断一个文档节点是否只等于空内容（结尾孤立 `---` 的合法情形）。
// 解到 yaml.Node 时外层通常是 DocumentNode，需拆 Content 后才是真正的内容节点。
func isEmptyDoc(n *yaml.Node) bool {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return true
		}
		return isEmptyDoc(n.Content[0])
	}
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// PriceConfigured 报告输入与输出单价是否都已配置。
// 未配置时调用方必须显式打印"单价未配置，无法估算成本"，
// 不能拿 0 单价算出"成本 0.00 CNY"这种伪装精确的假数（§3.1）。
func (c *Config) PriceConfigured() bool {
	return c.InputPricePerMTok > 0 && c.OutputPricePerMTok > 0
}

func (c *Config) validate() error {
	if c.Model == "" {
		return errors.New("model 不能为空")
	}
	// 下限防瞬间超时误发，上限防把请求超时当成任务预算用。
	if c.Timeout < time.Second || c.Timeout > 10*time.Minute {
		// yaml 把不带单位的整数按纳秒（time.Duration 的底层类型）解析，
		// 写成 `timeout: 60` 会变成 60ns，必须提示用户加单位而不是只报错值。
		return fmt.Errorf("timeout 必须在 1s 到 10m 之间，当前为 %s（不带单位的数字按纳秒解析，请写成 \"60s\" 这种形式）", c.Timeout)
	}
	if c.MaxOutputTokens <= 0 || c.MaxOutputTokens > maxOutputTokensCeiling {
		return fmt.Errorf("max_output_tokens 必须在 1 到 %d 之间，当前为 %d（注：这是单次输出上限，不是模型上下文窗口）",
			maxOutputTokensCeiling, c.MaxOutputTokens)
	}
	if c.MaxContextTokens <= 0 || c.MaxContextTokens > maxContextTokensCeiling {
		return fmt.Errorf("max_context_tokens 必须在 1 到 %d 之间，当前为 %d（注：窗口是 input+output 总量上限）",
			maxContextTokensCeiling, c.MaxContextTokens)
	}
	// 窗口必须容得下输出预留，否则 §3.6 的可用输入预算会算出 0 或负数。
	if c.MaxContextTokens <= c.MaxOutputTokens {
		return fmt.Errorf("max_context_tokens（%d）必须大于 max_output_tokens（%d）：输出预留占满窗口就没有输入空间了",
			c.MaxContextTokens, c.MaxOutputTokens)
	}
	if c.InputPricePerMTok < 0 || c.OutputPricePerMTok < 0 {
		return fmt.Errorf("单价不能为负（input: %v, output: %v）", c.InputPricePerMTok, c.OutputPricePerMTok)
	}
	if !supportedCurrencies[c.Currency] {
		return fmt.Errorf("currency 必须是 %s 之一，当前为 %q", currencyHint(), c.Currency)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("base_url 必须是合法的 https 地址，当前为 %q", c.BaseURL)
	}
	if c.APIKey == "" {
		// 注意：只报告缺失，不报告值。任何错误信息都不得包含密钥内容。
		return errors.New("OPENAI_API_KEY 环境变量未设置")
	}
	return nil
}

// currencyHint 从白名单生成错误提示，避免提示信息与 supportedCurrencies 两处不一致。
func currencyHint() string {
	list := make([]string, 0, len(supportedCurrencies))
	for k := range supportedCurrencies {
		list = append(list, k)
	}
	sort.Strings(list)
	return strings.Join(list, " 或 ")
}

// Redact 返回可安全打印的配置摘要，供 --debug 与诊断输出使用（§3.4 / §4：调试输出不得记录密钥）。
func (c *Config) Redact() string {
	price := "未配置（成本不可估算）"
	if c.PriceConfigured() {
		price = fmt.Sprintf("in %.4f + out %.4f / 1M %s", c.InputPricePerMTok, c.OutputPricePerMTok, c.Currency)
	}
	return fmt.Sprintf(
		"Config{Model: %s, Timeout: %s, MaxContextTokens: %d, MaxOutputTokens: %d, BaseURL: %s, Price: %s, APIKey: ***}",
		c.Model, c.Timeout, c.MaxContextTokens, c.MaxOutputTokens, c.BaseURL, price,
	)
}
