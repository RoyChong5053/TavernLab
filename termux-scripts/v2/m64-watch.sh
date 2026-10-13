#!/usr/bin/env bash
# m64-watch.sh — 跑在 m64，检测 TavernLab 新回复并推送到 GT20
#
#   ./m64-watch.sh --install --token <app_token> [--host http://127.0.0.1:8888]
#   ./m64-watch.sh --run        # 前台跑（tmux 里用这个）
#   ./m64-watch.sh --status     # 看 tmux + 最近日志
#
# 逻辑是 v1 tl-notify.sh 的搬家版：SSE管延迟、轮询管不漏、last_id单调高水位。
# 区别：HTTP 全走本机回环（零手机流量），弹通知靠 ssh 到手机调 tl-push。
set -uo pipefail

CONF="$HOME/.config/tl-push-watch/env"
D="$HOME/.local/share/tl-push-watch"
LOG="$D/log"

usage() { sed -n '2,8p' "$0"; exit "${1:-0}"; }

HOST="http://127.0.0.1:8888"; TOKEN=""; SESSION="auto"
INSTALL=0; RUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --install) INSTALL=1; shift ;;
    --run) RUN=1; shift ;;
    --status) tmux capture-pane -p -t tlpush 2>/dev/null | tail -8; echo ---; tail -6 "$LOG" 2>/dev/null; exit 0 ;;
    --host) HOST="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "未知参数: $1" >&2; usage 1 ;;
  esac
done

ts() { date '+%F %T'; }
log() { echo "$(ts) $*" >>"$LOG"; }

if [ "$INSTALL" = 1 ]; then
  [ -n "$TOKEN" ] || { echo "缺 --token（从 m64 data/settings.json 取 app_token）" >&2; exit 1; }
  mkdir -p "$(dirname "$CONF")" "$D"
  cat >"$CONF" <<EOF
# tl-push-watch 配置（由 m64-watch.sh --install 生成，勿提交）
HOST=$HOST
TOKEN=$TOKEN
TL_SESSION=$SESSION
# 手机推送目标（Tailscale，走内网直连）
TL_PHONE_USER=u0_a325
TL_PHONE_HOST=100.75.97.4
TL_PHONE_PORT=8022
TL_TITLE=TavernLab
TL_SSE_MAXTIME=900
TL_POLL=20
TL_SESSION_TTL=60
TL_BACKOFF_MAX=30
TL_SSH_TIMEOUT=10
EOF
  chmod 600 "$CONF"
  echo "写入 $CONF"
  if ! tmux has-session -t tlpush 2>/dev/null; then
    tmux new-session -d -s tlpush "$PWD/m64-watch.sh --run"
    echo "tmux tlpush 已起"
  else
    tmux kill-session -t tlpush 2>/dev/null
    sleep 1
    tmux new-session -d -s tlpush "$PWD/m64-watch.sh --run"
    echo "tmux tlpush 已重启"
  fi
  exit 0
fi

[ "$RUN" = 1 ] || usage 1
[ -f "$CONF" ] || { echo "缺 $CONF，先跑 --install" >&2; exit 1; }
# shellcheck disable=SC1090
. "$CONF"
: "${HOST:?}"; : "${TOKEN:?}"

mkdir -p "$D"
exec 9>"$D/lock"
flock -n 9 || { echo "已有实例在跑，退出"; exit 0; }

CH="${TL_CHANNEL:-tavernlab-reply}"
SESSION="${TL_SESSION:-auto}"
POLL="${TL_POLL:-20}"
SSE_MAXTIME="${TL_SSE_MAXTIME:-900}"
SESSION_TTL="${TL_SESSION_TTL:-60}"
BACKOFF_MAX="${TL_BACKOFF_MAX:-30}"
SSH_TIMEOUT="${TL_SSH_TIMEOUT:-10}"
PHONE="${TL_PHONE_USER}@${TL_PHONE_HOST}"
PORT="${TL_PHONE_PORT:-8022}"

command -v curl >/dev/null || { echo "缺 curl" >&2; exit 1; }
command -v jq >/dev/null || { echo "缺 jq" >&2; exit 1; }
command -v ssh >/dev/null || { echo "缺 ssh" >&2; exit 1; }

auth=(-H "Authorization: Bearer $TOKEN")
enc() { jq -rn --arg s "$1" '$s|@uri'; }

_cached_s=""; _cached_at=0
resolve_session() {
  [ "$SESSION" != auto ] && { printf %s "$SESSION"; return 0; }
  local now; now=$(date +%s)
  if [ -n "$_cached_s" ] && [ $((now - _cached_at)) -lt "$SESSION_TTL" ]; then
    printf %s "$_cached_s"; return 0
  fi
  local s
  s=$(curl -s -m 5 "${auth[@]}" "$HOST/api/settings" 2>/dev/null \
        | jq -r '.current_char // empty' 2>/dev/null)
  [ -n "$s" ] || { [ -n "$_cached_s" ] && { printf %s "$_cached_s"; return 0; }; return 1; }
  _cached_s="$s"; _cached_at="$now"
  printf %s "$s"
}

# 推送：同一 id 只推一次（SSE 和轮询并发到达，用 flock 串行化）。
exec 8>"$D/deliver.lock"
push_to_phone() {
  local id="$1" txt="$2"
  [ -n "$id" ] && [ -n "$txt" ] || return 0
  flock -w 3 8 || return 0
  [ "$id" = "$(cat "$D/last_id" 2>/dev/null)" ] && { flock -u 8; return 0; }
  echo "$id" >"$D/last_id"
  flock -u 8
  local short
  short=$(tr -s '[:space:]' ' ' <<<"$txt" | cut -c1-140)
  # body 走 stdin，避免引号/换行转义地狱；tl-push 从 stdin 读正文。
  if printf %s "$short" | timeout "$SSH_TIMEOUT" ssh -o BatchMode=yes \
      -o ConnectTimeout="$SSH_TIMEOUT" -p "$PORT" "$PHONE" \
      "tl-push '$id' '${TL_TITLE:-TavernLab}'" 2>>"$LOG"; then
    log "PUSH id=$id len=${#txt} head=${short:0:60}"
  else
    # 手机离线/Tailscale断：last_id 已推进，下次只推更新的（高水位语义，
    # 不补历史，只保证最新不漏——和 v1 一致）。
    log "push FAIL id=$id（手机不可达，记位不补）"
  fi
}

on_event() {
  local j="$1"
  [ "$(jq -r '.role // empty' <<<"$j" 2>/dev/null)" = assistant ] || return 0
  push_to_phone "$(jq -r '.id // empty' <<<"$j" 2>/dev/null)" \
                "$(jq -r '.text // empty' <<<"$j" 2>/dev/null)"
}

catchup() {
  local s="$1" j cand
  j=$(curl -s -m 6 "${auth[@]}" \
        "$HOST/api/history?session=$(enc "$s")&limit=10" 2>/dev/null) || return 0
  cand=$(jq -c '[.messages[]? | select(.role=="assistant" and ((.text//"")|length)>0)] | last // empty' \
        <<<"$j" 2>/dev/null)
  on_event "$cand"
}

seed() {
  [ -f "$D/last_id" ] && return 0
  local s j id
  s=$(resolve_session) || return 0
  j=$(curl -s -m 6 "${auth[@]}" \
        "$HOST/api/history?session=$(enc "$s")&limit=1" 2>/dev/null) || return 0
  id=$(jq -r '.messages[-1].id // empty' <<<"$j" 2>/dev/null)
  [ -n "$id" ] || return 0
  echo "$id" >"$D/last_id"
  log "seed last_id=$id (silent)"
}

sse_loop() {
  local backoff=1 s rc
  while :; do
    if ! s=$(resolve_session); then
      log "sse: resolve session 失败，${backoff}s 后重试"
      sleep "$backoff"
      [ "$backoff" -lt "$BACKOFF_MAX" ] && backoff=$((backoff * 2)) || backoff=$BACKOFF_MAX
      continue
    fi
    log "sse listen session=$s"
    timeout $((SSE_MAXTIME + 30)) curl -sN --max-time "$SSE_MAXTIME" "${auth[@]}" \
      "$HOST/api/events?session=$(enc "$s")" 2>>"$LOG" \
    | while IFS= read -r l; do
        case "$l" in data:*) on_event "${l#data: }" ;; esac
      done
    rc=${PIPESTATUS[0]}
    log "sse closed rc=$rc"
    if [ "$rc" = 28 ] || [ "$rc" = 124 ]; then
      backoff=1; sleep 1   # 健康断开立刻重连，不涨退避（v1锁相教训）
    else
      sleep "$backoff"
      [ "$backoff" -lt "$BACKOFF_MAX" ] && backoff=$((backoff * 2)) || backoff=$BACKOFF_MAX
    fi
  done
}

log "=== watch start host=$HOST session=$SESSION poll=${POLL}s phone=$PHONE ==="
seed
sse_loop &
while :; do
  if s=$(resolve_session); then
    catchup "$s"
  else
    log "poll: resolve session 失败"
  fi
  sleep "$POLL"
done
