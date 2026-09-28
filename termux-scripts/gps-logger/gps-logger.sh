#!/data/data/com.termux/files/usr/bin/bash
# gps-logger.sh — GT20 地理位置反馈守护进程 (v0 极简版)
#
# 每 600s 取一次 termux-location (gps 优先, network 兜底)，
# 直 POST 到 m64 TavernLab /api/location (JSON)，服务端落盘 + 调 d3180
# Paikka 反查，time_anchor 自动带 [LOC ...]。本地只记日志 + last.json
# 重试，不做 sqlite 队列 (v0)。
#
# 依赖: com.termux + com.termux.api, curl jq timeout
# 配置: ~/.config/gps-logger/env (chmod 600, 勿提交)
set -uo pipefail

export PATH="$HOME/bin:$PREFIX/bin:$PATH"

CONF="$HOME/.config/gps-logger/env"
[ -f "$CONF" ] || { echo "gps-logger: 缺少 $CONF，先跑 install.sh" >&2; exit 1; }
# shellcheck disable=SC1090
. "$CONF"

: "${HOST:?HOST 未设置 (m64 TavernLab, 如 http://192.168.100.78:8888)}"
: "${TOKEN:?TOKEN 未设置 (app_token)}"

D="$PREFIX/var/lib/gps-logger"
mkdir -p "$D"
INTERVAL="${GL_INTERVAL:-600}"
LOG="${GL_LOG:-$D/log}"

ts() { date '+%F %T'; }
log() { echo "$(ts) $*" >>"$LOG"; }

command -v curl >/dev/null || { echo "缺 curl" >&2; exit 1; }
command -v jq >/dev/null || { echo "缺 jq" >&2; exit 1; }
command -v timeout >/dev/null || { echo "缺 timeout" >&2; exit 1; }

exec 9>"$D/lock"
flock -n 9 || { echo "gps-logger: 已有实例在跑，退出"; exit 0; }
echo $$ >"$D/pid"

if [ "${GL_WAKELOCK:-1}" = 1 ]; then
  timeout 8 termux-wake-lock 2>/dev/null || log "warn: termux-wake-lock 失败"
fi
cleanup() { rm -f "$D/pid"; timeout 8 termux-wake-unlock 2>/dev/null; return 0; }
trap cleanup EXIT INT TERM

auth=(-H "Authorization: Bearer $TOKEN")

get_location() {
  # 三级降级: gps 单次定位 -> network 单次 -> passive 最后已知位置 (瞬时)。
  # 实测 Termux:API 间歇性超时 (gps/network 双双 rc=124), passive/last
  # 几乎必中, 保证每轮都有点 (精度差但郊区级 Paikka 够用)。
  local loc dbg
  dbg=""
  loc=$(timeout 25 termux-location -p gps 2>/dev/null)
  dbg="g${#loc}"
  if ! echo "$loc" | grep -q '"latitude"'; then
    loc=$(timeout 20 termux-location -p network 2>/dev/null)
    dbg="$dbg/n${#loc}"
  fi
  if ! echo "$loc" | grep -q '"latitude"'; then
    loc=$(timeout 10 termux-location -p passive -r last 2>/dev/null)
    dbg="$dbg/p${#loc}"
  fi
  echo "$dbg $loc"
}

post_point() {
  local lat="$1" lon="$2" acc="$3" prov="$4" tst="$5" batt="$6"
  local payload
  payload=$(jq -n --argjson lat "$lat" --argjson lon "$lon" \
    --argjson acc "${acc:-0}" --arg prov "$prov" --argjson tst "$tst" \
    --argjson batt "${batt:-0}" \
    '{lat:$lat, lon:$lon, acc:$acc, provider:$prov, tst:$tst, batt:$batt}')
  echo "$payload" >"$D/last.json"
  local code
  code=$(timeout 20 curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$payload" "$HOST/api/location" 2>/dev/null)
  if [ "$code" = 200 ] || [ "$code" = 201 ]; then
    log "OK lat=$lat lon=$lon acc=${acc} prov=$prov batt=${batt}"
    return 0
  fi
  log "FAIL http=$code lat=$lat lon=$lon (下轮重发 last.json)"
  return 1
}

# 开机/重启后先补发上次没发出去的点
retry_last() {
  [ -f "$D/last.json" ] || return 0
  local payload code
  payload=$(cat "$D/last.json" 2>/dev/null)
  [ -n "$payload" ] || return 0
  code=$(timeout 20 curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" \
    -H 'Content-Type: application/json' -d "$payload" "$HOST/api/location" 2>/dev/null)
  [ "$code" = 200 ] || [ "$code" = 201 ] && log "RETRY OK"
}

log "=== start pid=$$ host=$HOST interval=${INTERVAL}s ==="
retry_last

while :; do
  raw=$(get_location)
  dbg="${raw%% *}"; raw="${raw#* }"
  lat=$(echo "$raw" | jq -r '.latitude // empty' 2>/dev/null)
  lon=$(echo "$raw" | jq -r '.longitude // empty' 2>/dev/null)
  acc=$(echo "$raw" | jq -r '.accuracy // 0' 2>/dev/null)
  prov=$(echo "$raw" | jq -r '.provider // "unknown"' 2>/dev/null)
  tst=$(date +%s)
  batt=$(timeout 5 termux-battery-status 2>/dev/null | jq -r '.percentage // 0' 2>/dev/null)
  [ "$batt" = "" ] && batt=0
  if [ -n "$lat" ] && [ -n "$lon" ]; then
    post_point "$lat" "$lon" "$acc" "$prov" "$tst" "$batt" || true
    sleep "$INTERVAL"
  else
    # 取失败早退避早重试: stall 窗口通常几分钟, 不等满 10 分钟
    log "SKIP 取不到定位 [$dbg]"
    sleep 120
  fi
done
