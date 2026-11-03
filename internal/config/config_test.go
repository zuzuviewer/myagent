package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validEnv 设置通过校验所需的最小环境变量，并在测试结束后自动恢复。
func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "sk-test-key-1234")
}

// writeYAML 在临时目录写入配置文件并返回路径。
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	validEnv(t)
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\n"))
	if err != nil {
		t.Fatalf("最小合法配置不应报错: %v", err)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("Timeout 默认值 = %v, 期望 30s", c.Timeout)
	}
	if c.MaxOutputTokens != 1024 {
		t.Errorf("MaxOutputTokens 默认值 = %d, 期望 1024", c.MaxOutputTokens)
	}
	if c.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL 默认值 = %q, 期望 https://api.openai.com/v1", c.BaseURL)
	}
	if c.MaxContextTokens != defaultMaxContextTokens {
		t.Errorf("MaxContextTokens 默认值 = %d, 期望 %d", c.MaxContextTokens, defaultMaxContextTokens)
	}
	if c.Currency != "USD" {
		t.Errorf("Currency 默认值 = %q, 期望 USD", c.Currency)
	}
	// 单价默认必须是"未配置"而不是一个看起来合理的非零值：
	// 用美元默认价给人民币计费的模型算成本，数字会精确地错。
	if c.InputPricePerMTok != 0 || c.OutputPricePerMTok != 0 {
		t.Errorf("单价默认值应为 0（未配置）, got (%v, %v)", c.InputPricePerMTok, c.OutputPricePerMTok)
	}
	if c.PriceConfigured() {
		t.Error("默认配置下 PriceConfigured 应为 false")
	}
}

func TestFileOverrides(t *testing.T) {
	validEnv(t)
	c, err := NewConfig(writeYAML(t, `
model: gpt-4o-mini
timeout: 45s
max_context_tokens: 1000000
max_output_tokens: 2048
currency: CNY
input_price_per_mtok: 0.15
output_price_per_mtok: 0.6
base_url: https://api.example.com/v1
`))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if c.Model != "gpt-4o-mini" || c.Timeout != 45*time.Second || c.MaxOutputTokens != 2048 {
		t.Errorf("文件覆盖失败: %+v", *c)
	}
	if c.MaxContextTokens != 1000000 {
		t.Errorf("max_context_tokens 覆盖失败: %d", c.MaxContextTokens)
	}
	if c.Currency != "CNY" {
		t.Errorf("currency 覆盖失败: %q", c.Currency)
	}
	if !c.PriceConfigured() {
		t.Error("已配置单价，PriceConfigured 应为 true")
	}
	// 已配价时币种必须随价格一起出现在展示里，否则数字没有单位含义。
	if !strings.Contains(c.Redact(), "CNY") {
		t.Errorf("Redact 应带出币种, got %s", c.Redact())
	}
	if c.InputPricePerMTok != 0.15 || c.OutputPricePerMTok != 0.6 {
		t.Errorf("单价覆盖失败: (%v, %v)", c.InputPricePerMTok, c.OutputPricePerMTok)
	}
	if c.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL 覆盖失败: %q", c.BaseURL)
	}
}

func TestEnvPrecedence(t *testing.T) {
	validEnv(t)
	t.Setenv("OPENAI_BASE_URL", "https://relay.example.com/v1")
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\nbase_url: https://api.openai.com/v1\n"))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	// 优先级：环境变量 > 配置文件 > 默认值。
	if c.BaseURL != "https://relay.example.com/v1" {
		t.Errorf("OPENAI_BASE_URL 应覆盖文件值, got %q", c.BaseURL)
	}
}

func TestAPIKeyTrimmed(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "  sk-padded-9999  ")
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\n"))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if c.APIKey != "sk-padded-9999" {
		t.Errorf("APIKey 应去除首尾空白, got %q", c.APIKey)
	}
}

func TestMissingAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := NewConfig(writeYAML(t, "model: gpt-4o\n"))
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("缺 API Key 应报错并指名环境变量, got %v", err)
	}
}

func TestEmptyFileRejected(t *testing.T) {
	validEnv(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"零字节", "", "为空"},
		{"空白字符", "   \n\t\n", "为空"},
		// 只剩注释：字节非空但解不出文档，应报“不含配置项”而不是混进“语法错误”。
		{"只有注释", "# 配置被我清掉了\n", "不含任何配置项"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConfig(writeYAML(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("空配置应报错而非静默走默认值（期望指向 %q）, got %v", tc.want, err)
			}
		})
	}
}

func TestMultiDocRejected(t *testing.T) {
	validEnv(t)
	// 第二个文档若不检查会被静默丢弃，model 取第一个文档的值。
	_, err := NewConfig(writeYAML(t, "model: gpt-4o\n---\nmodel: gpt-3.5-turbo\n"))
	if err == nil || !strings.Contains(err.Error(), "多个 YAML 文档") {
		t.Fatalf("多文档应报错, got %v", err)
	}
	// 合法的结尾 `---` 分隔单文档不应误报。
	if _, err := NewConfig(writeYAML(t, "model: gpt-4o\n---\n")); err != nil {
		t.Errorf("单文档带结尾 --- 不应报错, got %v", err)
	}
}

func TestMissingConfigFile(t *testing.T) {
	validEnv(t)
	missing := filepath.Join(t.TempDir(), "nope.yml")
	_, err := NewConfig(missing)
	if err == nil {
		t.Fatal("配置文件不存在应报错")
	}
	// 错误须带路径（诊断价值）且可用 errors.Is 还原底层原因（%w 包装）。
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("错误信息应包含文件路径, got %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("错误应保留 os.ErrNotExist 语义, got %v", err)
	}
}

func TestMalformedYAML(t *testing.T) {
	validEnv(t)
	_, err := NewConfig(writeYAML(t, "model: [unclosed\n  bad: {"))
	if err == nil {
		t.Fatal("坏语法应报错")
	}
	if !strings.Contains(err.Error(), "解析配置文件") {
		t.Errorf("错误应指明解析阶段, got %v", err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	validEnv(t)
	_, err := NewConfig(writeYAML(t, "time_out: 30s\n"))
	if err == nil || !strings.Contains(err.Error(), "time_out") {
		t.Fatalf("未知 key 应报错且指名该 key, got %v", err)
	}
}

// 结构性安全保证：api_key 没有 yaml 映射（yaml:"-"），写进配置文件会被当未知项拒绝，
// 而不是被读取或被静默忽略。
func TestAPIKeyInFileRejected(t *testing.T) {
	validEnv(t)
	_, err := NewConfig(writeYAML(t, "model: gpt-4o\napi_key: sk-from-file\n"))
	if err == nil {
		t.Fatal("配置文件中的 api_key 应被拒绝")
	}
	if strings.Contains(err.Error(), "sk-from-file") {
		t.Errorf("错误信息不得回显密钥内容, got %v", err)
	}
}

func TestValidationBoundaries(t *testing.T) {
	validEnv(t)
	cases := []struct {
		name string
		yaml string
		want string // 期望错误信息包含的字段名
	}{
		{"空 model", "model: \"\"\n", "model"},
		{"timeout 过短", "model: gpt-4o\ntimeout: 500ms\n", "timeout"},
		{"timeout 过长", "model: gpt-4o\ntimeout: 2h\n", "timeout"},
		{"timeout 显式为 0", "model: gpt-4o\ntimeout: 0s\n", "timeout"},
		// yaml 将不带单位的整数按纳秒解析：`timeout: 60` 实际是 60ns，必须拦住。
		{"timeout 无单位", "model: gpt-4o\ntimeout: 60\n", "timeout"},
		{"max_output_tokens 为 0", "model: gpt-4o\nmax_output_tokens: 0\n", "max_output_tokens"},
		// 把上下文窗口误当输出上限是典型手滑，必须启动即报错而不是运行时报 400。
		{"max_output_tokens 超上限", "model: gpt-4o\nmax_output_tokens: 1024000\n", "max_output_tokens"},
		{"负输入单价", "model: gpt-4o\ninput_price_per_mtok: -1\n", "单价"},
		{"负输出单价", "model: gpt-4o\noutput_price_per_mtok: -0.5\n", "单价"},
		// 窗口与输出预留是两个独立手填值，写反了不会自然报错，必须在这里拦住，
		// 否则 §3.6 的可用输入预算会算出 0 或负数。
		{"窗口为 0", "model: gpt-4o\nmax_context_tokens: 0\n", "max_context_tokens"},
		{"窗口超上限（K 写成 M 的手滑）", "model: gpt-4o\nmax_context_tokens: 1024000000\n", "max_context_tokens"},
		{"窗口小于输出预留", "model: gpt-4o\nmax_context_tokens: 512\nmax_output_tokens: 1024\n", "max_context_tokens"},
		{"窗口等于输出预留", "model: gpt-4o\nmax_context_tokens: 1024\nmax_output_tokens: 1024\n", "max_context_tokens"},
		{"未知币种", "model: gpt-4o\ncurrency: RMB\n", "currency"},
		{"http 而非 https", "model: gpt-4o\nbase_url: http://api.openai.com/v1\n", "base_url"},
		{"非法 URL", "model: gpt-4o\nbase_url: \"://bad\"\n", "base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConfig(writeYAML(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错指向 %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCurrencyNormalizedToUpper(t *testing.T) {
	validEnv(t)
	// 大小写与多余空白属于格式问题，应归一化而不是报错。
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\ncurrency: cny \n"))
	if err != nil {
		t.Fatalf("小写币种应被归一化而非报错: %v", err)
	}
	if c.Currency != "CNY" {
		t.Errorf("Currency = %q, 期望归一化为 CNY", c.Currency)
	}
}

// 输入归一化：空白、结尾斜杠、币种大小写属于格式问题，应规范化而不是报错；
// 不规范会给 Provider 带入"看起来对但实际错"的值（模型名 404、请求路径 //chat/completions）。
func TestInputNormalization(t *testing.T) {
	validEnv(t)
	c, err := NewConfig(writeYAML(t, "model: \"  gpt-4o  \"\nbase_url: https://api.example.com/v1///\ncurrency: cny\n"))
	if err != nil {
		t.Fatalf("仅格式问题不应报错: %v", err)
	}
	if c.Model != "gpt-4o" {
		t.Errorf("Model 应去除首尾空白, got %q", c.Model)
	}
	if c.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL 应去掉结尾 /, got %q", c.BaseURL)
	}
	if c.Currency != "CNY" {
		t.Errorf("Currency 应归一化为大写, got %q", c.Currency)
	}
}

// 未配置单价时，展示层必须能区分"没配"和"成本为 0"（§3.1）。
func TestUnpricedCostNotShownAsZero(t *testing.T) {
	validEnv(t)
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\n"))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if c.PriceConfigured() {
		t.Fatal("未配单价时 PriceConfigured 应为 false")
	}
	if !strings.Contains(c.Redact(), "未配置") {
		t.Errorf("Redact 应标注单价未配置, got %s", c.Redact())
	}
}

func TestRedactNeverLeaksKey(t *testing.T) {
	const secret = "sk-super-secret-8888"
	t.Setenv("OPENAI_API_KEY", secret)
	c, err := NewConfig(writeYAML(t, "model: gpt-4o\n"))
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	out := c.Redact()
	if strings.Contains(out, secret) {
		t.Errorf("Redact 输出泄露了密钥: %s", out)
	}
	if !strings.Contains(out, "APIKey: ***") {
		t.Errorf("Redact 应掩码 APIKey, got %s", out)
	}
	// 窗口是 --debug 判断预算占用的分母，无论是否配价都必须可见；
	// 币种只在有单价时才有意义（未配价时展示"未配置"，见 TestUnpricedCostNotShownAsZero）。
	if !strings.Contains(out, "MaxContextTokens") {
		t.Errorf("Redact 应包含上下文窗口, got %s", out)
	}
}
