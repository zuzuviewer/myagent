#!/usr/bin/env bash
# 一键测试：格式检查 → 静态分析 → 构建 → 全量测试（含覆盖率）。
#
# 顺序不是随便排的：gofmt 与 vet 在本项目里抓到过真实缺陷（重复 package 行、
# 错误字段类型导致的解析失败、未使用导入），所以它们必须在测试之前就拦下。
#
# 用法：
#   scripts/test-all.sh            # 输出总覆盖率
#   scripts/test-all.sh --html     # 额外生成 coverage.html
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> gofmt"
unformatted="$(gofmt -l cmd internal)"
if [ -n "$unformatted" ]; then
	echo "以下文件未格式化，请先运行 gofmt -w cmd internal:" >&2
	echo "$unformatted" >&2
	exit 1
fi
echo "OK"

echo "==> go vet"
go vet ./...

echo "==> go build"
go build ./...

echo "==> go test (-count=1 强制不复用缓存)"
go test ./... -count=1 -coverprofile=coverage.out

echo "==> 总覆盖率"
go tool cover -func=coverage.out | tail -1

if [ "${1:-}" = "--html" ]; then
	go tool cover -html=coverage.out -o coverage.html
	echo "覆盖率报告已生成：coverage.html"
fi

echo "全部检查通过"
