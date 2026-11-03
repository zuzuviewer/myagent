#!/usr/bin/env bash
# 一键构建：产出二进制到 bin/coding-agent，并做一次最小自检。
#
# 自检只跑 -h（退出码应为 0），目的是确认产物真的可执行，
# 而不是构建成功却产出无法运行的文件。
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p bin
go build -o bin/coding-agent ./cmd/coding-agent

echo "已构建：bin/coding-agent"
bin/coding-agent -h >/dev/null 2>&1
echo "自检通过：-h 返回退出码 0"
