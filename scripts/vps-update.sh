#!/usr/bin/env bash
#
# aiapiport VPS 手动更新脚本
#
# 从 GitHub Release 下载最新（或指定版本）的二进制，原子替换后重启服务，
# 并做健康检查；启动或健康检查失败会自动回滚到旧版本。
#
# 用法：
#   sudo ./scripts/vps-update.sh              # 更新到最新 release
#   sudo ./scripts/vps-update.sh -v 0.1.3     # 更新/切到指定版本
#   sudo ./scripts/vps-update.sh -f           # 已是最新也强制重装
#
# 也可以不落地直接执行：
#   curl -fsSL https://raw.githubusercontent.com/mgl666/aiapiport/main/scripts/vps-update.sh | sudo bash
#
set -euo pipefail

# 本脚本名，用于错误提示（管道执行时 $0 是 bash）
SELF_NAME="$(basename "${BASH_SOURCE[0]:-$0}")"
case "$SELF_NAME" in bash|sh|"") SELF_NAME="vps-update.sh" ;; esac

REPO="${REPO:-mgl666/aiapiport}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
SERVICE="${SERVICE:-aiapiport}"
if [ -n "${CONFIG_FILE:-}" ]; then CONFIG_FILE_GIVEN=1; else CONFIG_FILE_GIVEN=0; fi
CONFIG_FILE="${CONFIG_FILE:-/etc/aiapiport/config.yaml}"
HEALTH_URL="${HEALTH_URL:-}"
HEALTH_TRIES="${HEALTH_TRIES:-15}"

TARGET_VERSION=""
FORCE=0
RESTART=1
HEALTH_CHECK=1

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; }

usage() {
  cat <<'EOF'
用法：sudo ./scripts/vps-update.sh [选项]

选项：
  -v, --version <版本>   更新到指定版本（0.1.3 或 v0.1.3 均可），默认取最新 release
  -f, --force            当前已是目标版本时也强制重新下载安装
      --no-restart       只替换二进制，不重启服务
      --no-health-check  跳过替换后的健康检查
  -h, --help             显示本帮助

可用环境变量：
  REPO          仓库，默认 mgl666/aiapiport
  INSTALL_DIR   二进制所在目录，默认 /usr/local/bin
  BIN_NAME      二进制名，默认自动探测 aiapiport-bin / aiapiport
  SERVICE       systemd 服务名，默认 aiapiport
  CONFIG_FILE   配置文件路径，默认 /etc/aiapiport/config.yaml
  HEALTH_URL    健康检查地址，默认按配置文件里的 listen 端口推导
  HEALTH_TRIES  健康检查重试次数，默认 15（每次间隔 1 秒）
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -v|--version)      [ "$#" -ge 2 ] || { err "$1 需要一个版本号"; exit 1; }; TARGET_VERSION="$2"; shift 2 ;;
    -v=*|--version=*)  TARGET_VERSION="${1#*=}"; shift ;;
    -f|--force)        FORCE=1; shift ;;
    --no-restart)      RESTART=0; shift ;;
    --no-health-check) HEALTH_CHECK=0; shift ;;
    -h|--help)         usage; exit 0 ;;
    *) err "未知参数：$1"; echo; usage; exit 1 ;;
  esac
done

# ---- 前置检查 ----
command -v curl >/dev/null || { err "未安装 curl"; exit 1; }

if [ "$(id -u)" -ne 0 ] && [ ! -w "$INSTALL_DIR" ]; then
  command -v sudo >/dev/null || { err "$INSTALL_DIR 需要写权限，且当前用户无法使用 sudo"; exit 1; }
  as_root() { sudo "$@"; }
else
  as_root() { "$@"; }
fi

# ---- 探测二进制名 ----
if [ -z "${BIN_NAME:-}" ]; then
  BIN_NAME=""
  for candidate in aiapiport-bin aiapiport; do
    if [ -x "$INSTALL_DIR/$candidate" ]; then BIN_NAME="$candidate"; break; fi
  done
  if [ -z "$BIN_NAME" ]; then
    warn "$INSTALL_DIR 下没有找到 aiapiport 二进制，按 aiapiport-bin 处理"
    BIN_NAME="aiapiport-bin"
  fi
fi
BIN_PATH="$INSTALL_DIR/$BIN_NAME"
BACKUP_PATH="${BIN_PATH}.bak"

# ---- 探测正在运行实例的启动参数 ----
# 部署方式可能是 daemon 模式（aiapiport start -config X），配置路径只存在于正在运行
# 的进程命令行里；也可能是 systemd，路径写在 unit 的 ExecStart 里。两种都能从
# /proc/<pid>/cmdline 读到。必须在停服之前取到，否则重启时可能用一个不存在的默认
# 路径，服务就再也起不来了。
DETECTED_CONFIG=""
DETECTED_PORT=""

detect_running_args() {
  [ -r /proc/self/cmdline ] || return 1
  command -v pgrep >/dev/null 2>&1 || return 1

  local pid cwd i arg first
  for pid in $(pgrep -x "$BIN_NAME" 2>/dev/null) $(pgrep -f "$BIN_NAME serve" 2>/dev/null); do
    [ "$pid" = "$$" ] && continue
    [ -r "/proc/$pid/cmdline" ] || continue

    local -a argv=()
    while IFS= read -r arg; do argv+=("$arg"); done \
      < <(tr '\0' '\n' < "/proc/$pid/cmdline" 2>/dev/null) || continue

    first="${argv[0]:-}"
    case "$first" in *"$BIN_NAME"*) ;; *) continue ;; esac
    [ "${argv[1]:-}" = "serve" ] || continue

    cwd="$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)"
    for ((i = 2; i < ${#argv[@]}; i++)); do
      arg="${argv[$i]}"
      case "$arg" in
        -config=*)  DETECTED_CONFIG="${arg#-config=}" ;;
        --config=*) DETECTED_CONFIG="${arg#--config=}" ;;
        -config|--config)
          if [ $((i + 1)) -lt ${#argv[@]} ]; then i=$((i + 1)); DETECTED_CONFIG="${argv[$i]}"; fi ;;
        -port=*)  DETECTED_PORT="${arg#-port=}" ;;
        --port=*) DETECTED_PORT="${arg#--port=}" ;;
        -port|--port)
          if [ $((i + 1)) -lt ${#argv[@]} ]; then i=$((i + 1)); DETECTED_PORT="${argv[$i]}"; fi ;;
      esac
    done

    # 相对路径按原进程的工作目录解析
    if [ -n "$DETECTED_CONFIG" ] && [ "${DETECTED_CONFIG#/}" = "$DETECTED_CONFIG" ] && [ -n "$cwd" ]; then
      DETECTED_CONFIG="$cwd/$DETECTED_CONFIG"
    fi
    if [ -n "$DETECTED_CONFIG" ]; then return 0; fi
    DETECTED_PORT=""
  done
  return 1
}

# ---- 探测平台 ----
case "$(uname -s)" in
  Linux)
    case "$(uname -m)" in
      x86_64|amd64)  ASSET="aiapiport-linux-amd64" ;;
      aarch64|arm64) ASSET="aiapiport-linux-arm64" ;;
      *) err "不支持的架构：$(uname -m)"; exit 1 ;;
    esac
    ;;
  Darwin)
    case "$(uname -m)" in
      arm64)  ASSET="aiapiport-darwin-arm64" ;;
      x86_64) ASSET="aiapiport-darwin-amd64" ;;
      *) err "不支持的架构：$(uname -m)"; exit 1 ;;
    esac
    ;;
  *) err "不支持的系统：$(uname -s)"; exit 1 ;;
esac

# ---- 服务管理方式 ----
USE_SYSTEMD=0
if command -v systemctl >/dev/null 2>&1 \
   && systemctl list-unit-files --no-legend "${SERVICE}.service" 2>/dev/null | grep -q .; then
  USE_SYSTEMD=1
fi

restart_service() {
  if [ "$USE_SYSTEMD" -eq 1 ]; then
    as_root systemctl restart "$SERVICE"
  else
    local -a args=(-config "$CONFIG_FILE")
    if [ -n "$DETECTED_PORT" ]; then args+=(-port "$DETECTED_PORT"); fi
    "$BIN_PATH" stop >/dev/null 2>&1 || true
    "$BIN_PATH" start "${args[@]}"
  fi
}

# show_service_logs 打印最近的日志，启动失败时用来直接看出原因。
show_service_logs() {
  echo "   最近日志："
  if [ "$USE_SYSTEMD" -eq 1 ]; then
    as_root journalctl -u "$SERVICE" -n 20 --no-pager 2>/dev/null | sed 's/^/     /' || true
    return
  fi
  local lf="${AIAPIPORT_RUN_DIR:-$HOME/.aiapiport}/aiapiport.log"
  if [ -f "$lf" ]; then
    tail -n 20 "$lf" | sed 's/^/     /'
  else
    echo "     （未找到日志文件 $lf）"
  fi
}

stop_service() {
  if [ "$USE_SYSTEMD" -eq 1 ]; then
    as_root systemctl stop "$SERVICE"
  else
    "$BIN_PATH" stop >/dev/null 2>&1 || true
  fi
}

service_active() {
  if [ "$USE_SYSTEMD" -eq 1 ]; then
    systemctl is-active --quiet "$SERVICE"
  else
    "$BIN_PATH" status 2>/dev/null | grep -q '^running'
  fi
}

# ---- 确定配置文件 ----
detect_running_args || true
if [ -n "$DETECTED_CONFIG" ] && [ "$CONFIG_FILE_GIVEN" -eq 0 ] && [ -f "$DETECTED_CONFIG" ]; then
  CONFIG_FILE="$DETECTED_CONFIG"
  log "已从运行中的实例探测到配置文件：$CONFIG_FILE"
fi
if [ -n "$DETECTED_PORT" ]; then
  log "已从运行中的实例探测到端口参数：$DETECTED_PORT"
fi

# 配置不存在就直接退出：把服务停了却起不来，比不更新更糟。
if [ "$RESTART" -eq 1 ] && [ ! -f "$CONFIG_FILE" ]; then
  err "配置文件不存在：$CONFIG_FILE"
  echo "    请显式指定实际路径后重试："
  echo "      sudo CONFIG_FILE=/path/to/config.yaml $SELF_NAME"
  echo "      sudo CONFIG_FILE=/path/to/config.yaml $SELF_NAME --no-restart   # 只替换二进制"
  exit 1
fi
if [ "$RESTART" -eq 1 ]; then
  log "将使用配置：$CONFIG_FILE"
fi

# ---- 解析版本 ----
current_version() {
  [ -x "$1" ] || { echo ""; return; }
  "$1" --version 2>/dev/null | awk '{print $NF}' || echo ""
}

cur_version="$(current_version "$BIN_PATH")"
if [ -n "$cur_version" ]; then
  log "当前版本：v${cur_version}（$BIN_PATH）"
else
  warn "$BIN_PATH 不存在或无法执行，将按全新安装处理"
fi

if [ -n "$TARGET_VERSION" ]; then
  target="${TARGET_VERSION#v}"
else
  log "查询最新 release..."
  # 先把 JSON 收进变量再解析，避免 grep 提前退出导致 curl 报
  # "curl: (23) Failure writing output to destination"
  release_json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" || true)"
  target="$(printf '%s\n' "$release_json" \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/' || true)"
  [ -n "$target" ] || { err "无法获取最新版本（网络或仓库问题），可用 -v 指定版本"; exit 1; }
fi

if [ "$target" = "$cur_version" ] && [ "$FORCE" -eq 0 ]; then
  ok "已是最新版本 v${target}，无需更新（需要重装请加 -f）"
  exit 0
fi

URL="https://github.com/${REPO}/releases/download/v${target}/${ASSET}"
TMP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

log "下载 v${target}（${ASSET}）"
if ! curl -fsSL --retry 3 --connect-timeout 10 -o "$TMP_DIR/$BIN_NAME" "$URL"; then
  err "下载失败：$URL"
  exit 1
fi
chmod +x "$TMP_DIR/$BIN_NAME"

# 校验下载物：能被执行且版本号对得上（同时可挡住架构不匹配的文件）
downloaded_version="$(current_version "$TMP_DIR/$BIN_NAME")"
if [ "$downloaded_version" != "$target" ]; then
  err "二进制校验失败：期望 v${target}，实际得到 '${downloaded_version:-无法执行}'"
  exit 1
fi
ok "下载完成并校验通过（v${downloaded_version}）"

# ---- 备份旧版本 ----
if [ -x "$BIN_PATH" ]; then
  as_root cp -p "$BIN_PATH" "$BACKUP_PATH"
  ok "已备份旧版本到 $BACKUP_PATH"
fi

rollback() {
  err "$1"
  if [ -f "$BACKUP_PATH" ]; then
    err "正在回滚到旧版本 v${cur_version:-unknown}..."
    as_root install -m 0755 "$BACKUP_PATH" "$BIN_PATH"
    restart_service >/dev/null 2>&1 || true
    if service_active; then
      warn "已回滚并恢复运行，当前版本 v$(current_version "$BIN_PATH")"
    else
      err "回滚后服务仍未运行，请手动排查"
      show_service_logs
    fi
  else
    err "没有可用备份，请手动排查"
    show_service_logs
  fi
  exit 1
}

# ---- 替换二进制 ----
if [ "$RESTART" -eq 1 ]; then
  log "停止服务"
  stop_service
fi

log "安装到 $BIN_PATH"
as_root install -m 0755 "$TMP_DIR/$BIN_NAME" "$BIN_PATH"

if [ "$RESTART" -eq 0 ]; then
  ok "二进制已替换为 v${target}（未重启，--no-restart 生效）"
  exit 0
fi

# ---- 重启并验证 ----
if [ "$USE_SYSTEMD" -eq 0 ]; then
  log "启动命令：$BIN_PATH start -config $CONFIG_FILE${DETECTED_PORT:+ -port $DETECTED_PORT}"
fi
log "启动服务"
restart_service

sleep 1
service_active || rollback "服务启动失败（进程未运行）"
ok "服务已启动"

if [ "$HEALTH_CHECK" -eq 0 ]; then
  ok "已跳过健康检查，更新完成：v${cur_version:-none} → v${target}"
  exit 0
fi

if [ -z "$HEALTH_URL" ]; then
  port="$(grep -m1 -E '^[[:space:]]*listen:[[:space:]]*' "$CONFIG_FILE" 2>/dev/null \
    | sed -E 's/.*:([0-9]+)["'"'"']?[[:space:]]*$/\1/' || true)"
  case "$port" in
    ''|*[!0-9]*) port="" ;;
  esac
  [ -n "$port" ] && HEALTH_URL="http://127.0.0.1:${port}/health"
fi

if [ -z "$HEALTH_URL" ]; then
  warn "未能从 $CONFIG_FILE 推导出监听端口，跳过健康检查"
  ok "更新完成：v${cur_version:-none} → v${target}"
  exit 0
fi

log "健康检查：$HEALTH_URL"
healthy=0
for _ in $(seq 1 "$HEALTH_TRIES"); do
  if curl -fsS -m 3 "$HEALTH_URL" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 1
done

if [ "$healthy" -eq 0 ]; then
  rollback "健康检查失败（${HEALTH_TRIES} 次重试均未通过）：$HEALTH_URL"
fi

ok "健康检查通过"
ok "更新完成：v${cur_version:-none} → v${target}"

if [ "$USE_SYSTEMD" -eq 1 ]; then
  echo "   查看日志：journalctl -u ${SERVICE} -n 50 --no-pager"
else
  echo "   查看日志：$BIN_PATH logs -n 50"
fi
echo "   回滚备份：$BACKUP_PATH"
