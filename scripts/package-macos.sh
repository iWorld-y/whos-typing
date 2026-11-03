#!/bin/sh
# 组装资源并生成当前架构的 macOS 安装镜像。
set -eu

project_dir=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
arch=$(go env GOARCH)
case "$(uname -s):$(go env GOOS):$arch" in
	Darwin:darwin:arm64|Darwin:darwin:amd64) ;;
	*) printf '%s\n' '打包需要 macOS 和 darwin/arm64 或 darwin/amd64 的 Go 环境。' >&2; exit 1 ;;
esac

stage=$(mktemp -d "${TMPDIR:-/tmp}/whos-typing-package.XXXXXX")
trap 'rm -rf "$stage"' EXIT

cp -R "$project_dir/cmd" "$project_dir/internal" "$stage/"
cp "$project_dir/go.mod" "$project_dir/go.sum" "$project_dir/mygo.json" "$project_dir/appinfo.go" "$stage/"
mkdir -p "$stage/resources/darwin-$arch/bin" "$stage/resources/skills"
cp "$project_dir/output/whos-typing-cli" "$stage/resources/darwin-$arch/bin/"
cp -R "$project_dir/skills/whos-typing-cli" "$stage/resources/skills/"

(
	cd "$stage"
	GOWORK=off go run -mod=readonly github.com/egoist/mygo/cmd/mygo build \
		-platform "darwin/$arch" -o "$stage/dist"
)

set -- "$stage/dist/darwin-$arch/"*.dmg
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
	printf '%s\n' '未找到生成的 DMG。' >&2
	exit 1
fi
cp "$1" "$project_dir/output/"
printf '打包完成：%s/output/%s\n' "$project_dir" "$(basename "$1")"
