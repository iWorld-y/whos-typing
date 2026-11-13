#!/usr/bin/env bash
# 为已推送的 master 创建版本标签，由 GitHub Actions 构建并发布。
set -euo pipefail

project_dir=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$project_dir"

if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
	printf '%s\n' '工作区有未提交改动，请先提交或处理后再发布。' >&2
	exit 1
fi
if [[ "$(git branch --show-current)" != master ]]; then
	printf '%s\n' '请在 master 分支执行发布。' >&2
	exit 1
fi

git fetch --no-tags origin master
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse refs/remotes/origin/master)" ]]; then
	printf '%s\n' '本地 master 与远端不一致，请先同步并推送。' >&2
	exit 1
fi

version_line=$(GOWORK=off go run ./cmd/whos-typing-cli --version)
case "$version_line" in
	"whos-typing-cli "*) version=${version_line#whos-typing-cli } ;;
	*) printf '无法读取 CLI 版本：%s\n' "$version_line" >&2; exit 1 ;;
esac
if [[ ! "$version" =~ ^[1-9][0-9]*$ ]]; then
	printf '版本格式无效：%s\n' "$version" >&2
	exit 1
fi
tag="v$version"

if git show-ref --verify --quiet "refs/tags/$tag"; then
	printf '本地标签已存在：%s\n' "$tag" >&2
	exit 1
fi
remote_tag=$(git ls-remote --tags origin "refs/tags/$tag")
if [[ -n "$remote_tag" ]]; then
	printf '远端标签已存在：%s\n' "$tag" >&2
	exit 1
fi

git tag "$tag"
if ! git push origin "refs/tags/$tag:refs/tags/$tag"; then
	printf '推送失败，本地标签 %s 已创建；请先核对远端状态。\n' "$tag" >&2
	exit 1
fi
printf '已推送 %s，GitHub Actions 将构建并发布 DMG。\n' "$tag"
