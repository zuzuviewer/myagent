#!/usr/bin/env bash
# 退出码冒烟测试：用**编译后的二进制**验证 §3.1 的退出码约定（0/1/2/130）。
#
# 为什么单独需要这个脚本：单元测试里断的退出码是 Run() 的返回值，而真实进程
# 还差一层 main 的 os.Exit；更要命的是 `go run` 会把子进程的非零退出码统一掩盖
# 成 1 —— 用 go run 验 130 或 2 永远验不出真相。这一层只能靠二进制本身来验。
#
# 全部使用 --provider fake：不产生真实模型请求，也不产生费用。
# 用法：先 scripts/build.sh，再 scripts/smoke.sh
set -uo pipefail
cd "$(dirname "$0")/.."

BIN="bin/coding-agent"
if [ ! -x "$BIN" ]; then
	echo "未找到可执行的 $BIN，请先运行 scripts/build.sh" >&2
	exit 1
fi

# config 包要求 API Key 非空；--provider fake 不会用到它，这里只给占位值。
export OPENAI_API_KEY="${OPENAI_API_KEY:-sk-smoke-placeholder-not-real}"

failed=0

# check <期望退出码> <说明> [参数...]
check() {
	local want="$1" desc="$2"
	shift 2

	local got=0
	"$BIN" "$@" >/dev/null 2>&1 || got=$?

	if [ "$got" != "$want" ]; then
		echo "✗ $desc：期望退出码 $want，实际 $got"
		failed=1
	else
		echo "✓ $desc（$got）"
	fi
}

check 2 "缺少任务描述"
check 2 "配置文件不存在" --config nope.yml "任务"
check 2 "plan 子命令在 M4 提供" plan "设计配置模块"
check 2 "未知的 --provider 取值" --provider bogus "任务"
check 2 "未知 flag" --nope "任务"
check 0 "帮助信息" -h
check 0 "fake Provider 单轮成功" --provider fake "用一句话解释 Go module 的作用"
check 0 "带 --debug 的成功路径" --debug --provider fake "任务"

if [ "$failed" -ne 0 ]; then
	echo "退出码冒烟失败" >&2
	exit 1
fi
echo "退出码冒烟全部通过"
