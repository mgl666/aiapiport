#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$APP_DIR"

if [ "$#" -eq 0 ] || [ -z "${1//[[:space:]]/}" ]; then
  echo "用法：./scripts/commit-and-push.sh \"提交说明\""
  echo "示例：./scripts/commit-and-push.sh \"feat: improve routing fallback\""
  exit 1
fi

command -v git >/dev/null || { echo "✗ 未安装 Git"; exit 1; }

branch="$(git branch --show-current)"
if [ -z "$branch" ]; then
  echo "✗ 当前不在一个 Git 分支上"
  exit 1
fi
if ! git remote get-url origin >/dev/null 2>&1; then
  echo "✗ 没有配置 origin 远程仓库"
  exit 1
fi

if command -v go >/dev/null; then
  echo "==> 检查代码格式"
  unformatted="$(gofmt -l . 2>/dev/null || true)"
  if [ -n "$unformatted" ]; then
    echo "✗ 以下文件未通过 gofmt，请先执行 gofmt -w："
    echo "$unformatted"
    exit 1
  fi

  echo "==> 静态检查（go vet）"
  go vet ./...

  echo "==> 构建检查"
  build_tmp="$(mktemp -t aiapiport-build.XXXXXX)"
  trap 'rm -f "$build_tmp"' EXIT
  go build -trimpath -ldflags "-s -w" -o "$build_tmp" .
  rm -f "$build_tmp"
  trap - EXIT
else
  echo "! 未检测到 Go 工具链，跳过格式 / vet / 构建检查"
  echo "  （最终由 GitHub Actions 构建，如需本地校验请先安装 Go）"
fi

echo "==> 暂存项目改动"
git add -A

# 示例配置可以提交（config.yaml.example），真实配置文件和环境文件不可以提交。
sensitive_files="$(git diff --cached --name-only | grep -E '(^|/)\.env($|\.)|(^|/)config\.yaml$|(^|/)config\.yaml\.(local|prod|production|server)$|\.(pem|key|p12|pfx)$|(^|/)id_rsa' || true)"
if [ -n "$sensitive_files" ]; then
  echo "✗ 发现可能包含密钥的文件，已停止提交："
  echo "$sensitive_files"
  echo "请先将这些文件取消暂存并加入 .gitignore。"
  exit 1
fi

if git diff --cached --quiet; then
  echo "✓ 没有需要提交的改动"
  exit 0
fi

echo "==> 创建提交"
git commit -m "$1"

echo "==> 推送到 origin/$branch"
if git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1; then
  git push origin "$branch"
else
  git push -u origin "$branch"
fi

echo "✅ 提交并推送完成"
