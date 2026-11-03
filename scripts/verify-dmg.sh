#!/usr/bin/env bash
# 校验 DMG 内容，并使用随包原生库执行项目测试。
set -euo pipefail

if [[ $# -ne 2 ]]; then
	printf '用法：bash scripts/verify-dmg.sh <DMG> <版本号>\n' >&2
	exit 1
fi
dmg=$1
version=$2
project_dir=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$project_dir"

case "$(go env GOARCH)" in
	arm64) arch=arm64 ;;
	amd64) arch=x86_64 ;;
	*) printf '%s\n' '不支持当前 Go 架构。' >&2; exit 1 ;;
esac
mount_dir=$(mktemp -d "${TMPDIR:-/tmp}/whos-typing-dmg-check.XXXXXX")
mounted=false
cleanup() {
	if [[ "$mounted" == true ]]; then
		hdiutil detach "$mount_dir" >/dev/null 2>&1 || true
	fi
	rmdir "$mount_dir" 2>/dev/null || true
}
trap cleanup EXIT

check() {
	if ! "$@"; then
		printf '校验失败：%s\n' "$*" >&2
		exit 1
	fi
}

hdiutil verify "$dmg"
hdiutil attach -readonly -nobrowse -mountpoint "$mount_dir" "$dmg"
mounted=true
app="$mount_dir/Who's Typing.app"
gui="$app/Contents/MacOS/Who's Typing"
cli="$app/Contents/Resources/bin/whos-typing-cli"
library="$app/Contents/Resources/libghostty-vt.dylib"
skill="$app/Contents/Resources/skills/whos-typing-cli"

check test -x "$gui"
check test -x "$cli"
check test -f "$library"
check test -L "$mount_dir/Applications"
check test "$(readlink "$mount_dir/Applications")" = /Applications
check test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$app/Contents/Info.plist")" = "$version"
check test "$("$cli" --version)" = "whos-typing-cli $version"
"$cli" --help >/dev/null
check test "$(lipo -archs "$gui")" = "$arch"
check test "$(lipo -archs "$cli")" = "$arch"
check test "$(lipo -archs "$library")" = "$arch"
check test "$(cat "$skill/WHOS_TYPING_SKILL")" = whos-typing-cli
cmp skills/whos-typing-cli/SKILL.md "$skill/SKILL.md"
cmp skills/whos-typing-cli/WHOS_TYPING_SKILL "$skill/WHOS_TYPING_SKILL"
codesign --verify --deep --strict "$app"

export MYGO_GHOSTTY_VT="$library" GOWORK=off
CGO_ENABLED=0 go test -count=1 ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./internal/app ./internal/control ./internal/shell ./internal/cliinstall

hdiutil detach "$mount_dir" >/dev/null
mounted=false
rmdir "$mount_dir"
trap - EXIT
printf 'DMG、版本、架构和测试均已通过：%s\n' "$dmg"
