#!/usr/bin/env bash
# TavernLab run/deploy script — tmux based, no systemd.
#
# On m64:
#   scripts/serve.sh deploy     # git pull --ff-only + go build + restart in tmux
#   scripts/serve.sh start      # start (build first if binary missing)
#   scripts/serve.sh stop
#   scripts/serve.sh restart
#   scripts/serve.sh status     # tmux session + /api/health
#   scripts/serve.sh attach     # attach to the tmux session (Ctrl-b d to detach)
#   scripts/serve.sh logs       # tail -f tavernlab.log
#
# Env overrides: PORT (8080) DATA (./data) SESSION (tavernlab)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-8080}"
DATA="${DATA:-$ROOT/data}"
SESSION="${SESSION:-tavernlab}"
BIN="$ROOT/tavernlab"
LOG="$ROOT/tavernlab.log"
URL="http://127.0.0.1:${PORT}/api/health"

# Go is at /usr/local/go on m64 but not always in PATH.
export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"

need() { command -v "$1" >/dev/null 2>&1 || { echo "缺少依赖: $1" >&2; exit 1; }; }

is_running() { tmux has-session -t "$SESSION" 2>/dev/null; }

build() {
  need go
  echo "==> go build -> $BIN"
  go build -o "$BIN" .
}

start() {
  need tmux
  if is_running; then
    echo "已在运行 (tmux: $SESSION)。要重启用: $0 restart"
    return 0
  fi
  [ -x "$BIN" ] || build
  echo "==> 启动 tmux 会话 '$SESSION' (port=$PORT data=$DATA)"
  # tee 让 tmux 面板实时可见，同时落一份日志文件。
  tmux new-session -d -s "$SESSION" -c "$ROOT" \
    "exec $BIN -port $PORT -data '$DATA' 2>&1 | tee -a '$LOG'"
  sleep 1
  status || true
}

stop() {
  if is_running; then
    echo "==> 停止 tmux 会话 '$SESSION'"
    tmux kill-session -t "$SESSION"
  else
    echo "未在运行。"
  fi
}

restart() { stop || true; start; }

status() {
  if is_running; then
    echo "tmux: 运行中 ($SESSION)"
  else
    echo "tmux: 未运行"
  fi
  printf "health: "
  curl -fsS -m 5 "$URL" || echo "(不可达)"
  echo
}

attach() { need tmux; tmux attach -t "$SESSION"; }
logs() { tail -n 80 -f "$LOG"; }

deploy() {
  need git
  echo "==> git pull --ff-only"
  git pull --ff-only
  build
  restart
}

case "${1:-}" in
  build)   build ;;
  start)   start ;;
  stop)    stop ;;
  restart) restart ;;
  status)  status ;;
  attach)  attach ;;
  logs)    logs ;;
  deploy)  deploy ;;
  *) echo "用法: $0 {deploy|start|stop|restart|status|attach|logs|build}" >&2; exit 2 ;;
esac
