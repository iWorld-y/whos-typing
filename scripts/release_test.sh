#!/usr/bin/env bash
# 在临时 Git 仓库中验证发布前检查和标签推送。
set -euo pipefail

project_dir=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/whos-typing-release-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null

origin="$test_dir/origin.git"
work="$test_dir/work"
other="$test_dir/other"
mkdir -p "$test_dir/bin"
cat > "$test_dir/bin/go" <<'EOF'
#!/bin/sh
printf 'whos-typing-cli %s\n' "${WT_TEST_VERSION:-0.1.0}"
EOF
chmod +x "$test_dir/bin/go"
export PATH="$test_dir/bin:$PATH"

git init --bare --initial-branch=master -q "$origin"
git init --initial-branch=master -q "$work"
mkdir -p "$work/scripts"
cp "$project_dir/scripts/release.sh" "$work/scripts/"
git -C "$work" add scripts/release.sh
git -C "$work" -c user.name=Test -c user.email=test@example.invalid commit -q -m fixture
git -C "$work" remote add origin "$origin"
git -C "$work" push -q --set-upstream origin master

assert_fails() {
	local expected=$1
	if (cd "$work" && bash scripts/release.sh) >"$test_dir/result" 2>&1; then
		printf '发布应当失败：%s\n' "$expected" >&2
		exit 1
	fi
	case "$(cat "$test_dir/result")" in
		*"$expected"*) ;;
		*) cat "$test_dir/result" >&2; exit 1 ;;
	esac
}

: > "$work/untracked"
assert_fails '工作区有未提交改动'
rm "$work/untracked"

git -C "$work" checkout -qb other-branch
assert_fails '请在 master 分支'
git -C "$work" checkout -q master

git clone -q "$origin" "$other"
printf '%s\n' newer > "$other/change"
git -C "$other" add change
git -C "$other" -c user.name=Test -c user.email=test@example.invalid commit -q -m newer
git -C "$other" push -q origin master
assert_fails '本地 master 与远端不一致'
git -C "$work" reset --hard -q origin/master

git -C "$other" tag v0.1.0
git -C "$other" push -q origin refs/tags/v0.1.0
assert_fails '远端标签已存在'

export WT_TEST_VERSION=0.1.1
git -C "$work" tag v0.1.1
assert_fails '本地标签已存在'
git -C "$work" tag -d v0.1.1 >/dev/null

export WT_TEST_VERSION=invalid
assert_fails '版本格式无效'

export WT_TEST_VERSION=0.1.2
(cd "$work" && bash scripts/release.sh) >"$test_dir/result" 2>&1
test "$(git --git-dir="$origin" rev-parse refs/tags/v0.1.2)" = "$(git -C "$work" rev-parse HEAD)"
printf '%s\n' '发布脚本的检查和标签推送测试通过。'
