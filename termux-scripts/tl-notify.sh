#!/data/data/com.termux/files/usr/bin/bash
# tl-notify.sh — TavernLab 后台回复通知守护进程
#
# 监听 TavernLab 的 SSE（GET /api/events），在收到一条完整落库的 assistant 回复时
# 弹一条本地通知。纯本地链路：不经 Google/FCM，不需要 ntfy，不改服务端一行代码。
#
# 为什么需要它：TavernLab App 在锁屏时会自己停掉轮询（didChangeAppLifecycleState
# → stopPolling），后台只剩一条不重连的 SSE，Doze 下一断就再也回不来。
# Termux 则是真正的前台服务（TermuxService.startForeground），配合 termux-wake-lock
# 拿到的 PARTIAL_WAKE_LOCK + WIFI_MODE_FULL_HIGH_PERF 可以在熄屏下持续收流。
#
# 依赖：com.termux + com.termux.api（+ com.termux.boot 用于开机自启）
# 配置：~/.config/tl-notify/env
set -uo pipefail

export PATH="$HOME/bin:$PREFIX/bin:$PATH"

CONF="$HOME/.config/tl-notify/env"
[ -f "$CONF" ] || {
  echo "tl-notify: 缺少 $CONF，先跑 install.sh" >&2
  exit 1
}
# shellcheck disable=SC1090
. "$CONF"

: "${HOST:?HOST 未设置}"
: "${TOKEN:?TOKEN 未设置}"

D="$PREFIX/var/lib/tl-notify"
mkdir -p "$D"

CH="${TL_CHANNEL:-tavernlab-reply}"
SESSION="${TL_SESSION:-auto}"          # auto = 跟随服务端 current_char
# 默认只走通知渠道：渠道声走 STREAM_NOTIFICATION，跟着响铃/DND/通知音量走，
# 是 Android 上正确的做法。chime 是备用——这台 ROM 曾有个「AI notification 过滤」
# 会把通知音效静音（当时音量 13/15、渠道声音已开、IMPORTANCE_HIGH 都无效），
# 那段日子靠 chime 顶着。现在过滤不拦了，但保着它，ROM 更新后再出现也不至于失声。
SOUND="${TL_SOUND:-notification}"      # notification | chime | both | none
CHIME="${TL_CHIME:-$PREFIX/share/tl-chime.wav}"
CHIME_MS="${TL_CHIME_MS:-1500}"       # chime 时长，播完就停
CHIME_BOOST="${TL_CHIME_BOOST:-0}"     # >0 时把 music 流临时抬到该音量再恢复
TTS="${TL_TTS:-0}"                     # 1 = 用 TTS 朗读回复全文
TTS_STREAM="${TL_TTS_STREAM:-ALARM}"
SSE_MAXTIME="${TL_SSE_MAXTIME:-120}"   # 长连接最长保持秒数，到点主动重连
BACKOFF_MAX="${TL_BACKOFF_MAX:-30}"
LOG="${TL_LOG:-$D/log}"

ts() { date '+%F %T'; }
log() { echo "$(ts) $*" >>"$LOG"; }
die() { echo "tl-notify: $*" >&2; exit 1; }

command -v curl    >/dev/null || die "缺 curl（pkg install curl）"
command -v jq      >/dev/null || die "缺 jq（pkg install jq）"
command -v timeout >/dev/null || die "缺 timeout（coreutils）"

# 每一个外部调用都必须有上限。
# 教训：Termux:API 的广播调用（termux-volume / termux-media-player / termux-notification）
# 偶尔会不返回，而 notify() 是在 SSE 读循环里同步调用的 —— 一个挂死的 API 调用
# 会把整个读循环卡死，之后所有通知都收不到，还会留下一堆持有 flock fd 9 的孤儿进程
# 让下次 start 起不来。所以统一走 t() 包一层。
T_NOTIF="${TL_T_NOTIF:-8}"     # 通知
T_CHIME="${TL_T_CHIME:-10}"    # 播放控制
T_VOL="${TL_T_VOL:-5}"         # 音量读写
T_TTS="${TL_T_TTS:-20}"        # TTS（要念完一整段，给够）
t() { timeout "$1" "${@:2}"; }

# ---- 单实例锁 ------------------------------------------------------------
# 之前用 pkill -f 清理时把执行命令的 shell 自己也匹配杀掉了，所以这里用 flock
# 做成硬保证：第二个实例直接退出，不会有两个进程抢同一个通知 id。
exec 9>"$D/lock"
flock -n 9 || { echo "tl-notify: 已有实例在跑，退出"; exit 0; }

# pidfile 归守护进程自己所有，tlctl 只读不猜——之前 tlctl 用 $! 记 setsid 的 pid，
# 而 setsid 是否 fork 取决于它是不是进程组长，记下来的 pid 未必是真正的脚本 pid。
PIDF="$D/pid"
echo $$ >"$PIDF"

# ---- 存活三件套 ----------------------------------------------------------
# Termux 自身是前台服务；termux-wake-lock 拿 PARTIAL_WAKE_LOCK +
# WIFI_MODE_FULL_HIGH_PERF 的 WifiLock（专治 Android 把连接按进省电态），
# 未豁免时还会弹窗请求忽略电池优化——那一下必须点「允许」。
if [ "${TL_WAKELOCK:-1}" = 1 ]; then
  t 8 termux-wake-lock || log "warn: termux-wake-lock 失败"
fi
cleanup() {
  rm -f "$PIDF"
  t 8 termux-wake-unlock
  return 0
}
trap cleanup EXIT INT TERM

# ---- 通知渠道（重要度不可变，只能删了重建）------------------------------
# 注意：termux-notification-channel 这个包装脚本只传 --es id / --es name，
# 从不传 priority，API 侧 priorityFromIntent() 拿不到就 fallback 到
# IMPORTANCE_DEFAULT —— 不弹头、也不保证响。必须直接调 libexec/termux-api。
# 渠道名不能含空格，Termux:API 会在第一个空格处截断。
if [ "$(cat "$D/channel.importance" 2>/dev/null)" != "${TL_IMPORTANCE:-high}" ]; then
  t 8 termux-notification-channel -d "$CH" >/dev/null 2>&1
  t 8 $PREFIX/libexec/termux-api NotificationChannel \
    --es id "$CH" --es name "${TL_CHANNEL_NAME:-TavernLabReply}" \
    --es priority "${TL_IMPORTANCE:-high}" >/dev/null 2>&1 \
    && log "channel $CH recreated IMPORTANCE=${TL_IMPORTANCE:-high}" \
    || log "warn: 渠道 $CH 重建失败"
  echo "${TL_IMPORTANCE:-high}" >"$D/channel.importance"
fi

# ---- 音效 ---------------------------------------------------------------
# 这台 Infinix 的 ROM 有「AI notification 过滤」，会把通知的**音效**静音
# （通知本身照常显示）。所以声音默认走独立播放路径，绕开通知管线。
play_chime() {
  [ -f "$CHIME" ] || { log "warn: chime 不存在 $CHIME"; return 0; }
  local orig=""
  if [ "$CHIME_BOOST" -gt 0 ] 2>/dev/null; then
    # media 播放走 STREAM_MUSIC；不抬音量的话 music 只有 5/15，锁屏基本听不见。
    # 读不到就放弃提升、按当前音量播，绝不因此卡住。
    orig=$(t "$T_VOL" termux-volume 2>/dev/null | jq -r '.[]|select(.stream=="music").volume' 2>/dev/null)
    case "$orig" in ''|null) orig="" ;; esac
    [ -n "$orig" ] && t "$T_VOL" termux-volume music "$CHIME_BOOST" >/dev/null 2>&1
  fi
  t "$T_CHIME" termux-media-player stop >/dev/null 2>&1
  t "$T_CHIME" termux-media-player play "$CHIME" >/dev/null 2>&1
  sleep "$(awk "BEGIN{print $CHIME_MS/1000}")"
  t "$T_CHIME" termux-media-player stop >/dev/null 2>&1
  [ -n "$orig" ] && t "$T_VOL" termux-volume music "$orig" >/dev/null 2>&1
  return 0
}

speak() {
  command -v termux-tts-speak >/dev/null || return 0
  # 掐掉 markdown 残留，免得 TTS 念出符号。
  # 注意别把局部变量叫 t —— 会遮蔽上面的 t() 超时包装函数。
  local say
  say=$(tr -d '*_`#>[]()|-' <<<"$1" | tr -s '[:space:]' ' ' | cut -c1-300)
  [ -n "$say" ] && t "$T_TTS" termux-tts-speak -s "$TTS_STREAM" -l "${TL_TTS_LANG:-zh-CN}" \
    -r "${TL_TTS_RATE:-1.15}" "$say" >/dev/null 2>&1
  return 0
}

notify() {
  local id="$1" title="$2" body="$3"
  # --action 在 TermuxService 进程里执行（见 open-app.sh 注释）。注意它需要
  # Termux 有「显示在其他应用上层」才能真的把 App 拉到前台，否则点了没反应。
  # TL_TAP=0 可关掉 —— 没权限时点了没反应比不设 action 更糟。
  local -a extra=()
  [ "${TL_TAP:-1}" = 1 ] && extra=(--action "$HOME/bin/open-app.sh")
  case "$SOUND" in
    notification|both)
      t "$T_NOTIF" termux-notification -i "$id" -t "$title" -c "$body" \
        --channel "$CH" --priority high --sound --vibrate 400,150,400 \
        "${extra[@]}" \
        >/dev/null 2>&1 || log "warn: termux-notification 超时或失败 id=$id"
      ;;
  esac
  case "$SOUND" in
    chime|both) play_chime ;;
  esac
  [ "$TTS" = 1 ] && speak "$body"
  return 0
}

# ---- SSE 会话 -----------------------------------------------------------
auth=(-H "Authorization: Bearer $TOKEN")

# auto 模式下问服务端要当前角色，这样 WebUI 换角色手机这边自动跟着走
resolve_session() {
  [ "$SESSION" != auto ] && { printf %s "$SESSION"; return 0; }
  local s
  s=$(curl -s -m 5 "${auth[@]}" "$HOST/api/settings" 2>/dev/null \
        | jq -r '.current_char // empty' 2>/dev/null)
  [ -n "$s" ] || return 1
  printf %s "$s"
}

handle_line() {
  local l="$1"
  case "$l" in data:*) ;; *) return 0 ;; esac   # 跳过 ": connected" / ": ping"
  local j=${l#data: }
  [ "$(jq -r .role <<<"$j" 2>/dev/null)" = assistant ] || return 0
  local id txt
  id=$(jq -r '.id // empty' <<<"$j" 2>/dev/null)
  txt=$(jq -r '.text // empty' <<<"$j" 2>/dev/null)
  [ -n "$id" ] && [ -n "$txt" ] || return 0
  # 去重：重连后同一条可能被重复看到
  [ "$id" = "$(cat "$D/last_id" 2>/dev/null)" ] && return 0
  echo "$id" >"$D/last_id"
  local short
  short=$(tr -s '[:space:]' ' ' <<<"$txt" | cut -c1-140)
  log "NOTIFY id=$id len=${#txt} head=${short:0:60}"
  notify "$id" "${TL_TITLE:-TavernLab}" "$short"
}

log "=== start pid=$$ host=$HOST session=$SESSION sound=$SOUND ==="

backoff=1
while :; do
  if ! s=$(resolve_session); then
    log "resolve session 失败，${backoff}s 后重试"
    sleep "$backoff"
    [ "$backoff" -lt "$BACKOFF_MAX" ] && backoff=$((backoff * 2)) || backoff=$BACKOFF_MAX
    continue
  fi
  log "listen session=$s backoff=${backoff}s"
  # --max-time 到点主动重连：既能拿到重连退避的收益，也避免半开连接静默死掉
  curl -sN --max-time "$SSE_MAXTIME" "${auth[@]}" \
    "$HOST/api/events?session=$(jq -rn --arg s "$s" '$s|@uri')" 2>>"$LOG" \
  | while IFS= read -r l; do handle_line "$l"; done
  rc=${PIPESTATUS[0]}
  log "sse closed rc=$rc"
  # 28=我们自己的超时（连接健康） 7=连不上（开机时 WiFi 还没起来就是它）
  if [ "$rc" = 7 ] || [ "$rc" = 6 ] || [ "$rc" = 28 ]; then
    sleep "$backoff"
    [ "$backoff" -lt "$BACKOFF_MAX" ] && backoff=$((backoff * 2)) || backoff=$BACKOFF_MAX
  else
    backoff=1
    sleep 2
  fi
done
