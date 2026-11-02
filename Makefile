.PHONY: build package run

build:
	@mkdir -p output
	@CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags='-s -w' -o output/ ./cmd/whos-typing ./cmd/whos-typing-cli

package: build
	@sh scripts/package-macos.sh

# 后台任务启用作业控制，确保内置 Bash 的 Ctrl+C 可用。
run: build
	@set -m; ./output/whos-typing </dev/null >>output/whos-typing.log 2>&1 &
	@printf '已发起后台启动，日志：output/whos-typing.log\n'
