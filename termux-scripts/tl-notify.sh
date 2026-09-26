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
SSE_MAXTIME="${TL_SSE_MAXTIME:-900}"  # 长连接最长保持秒数（服务端每 25s 发 : ping 保活）
BACKOFF_MAX="${TL_BACKOFF_MAX:-30}"
POLL="${TL_POLL:-5}"                   # 轮询兜底间隔秒数，这是「不漏」的实际保证
SESSION_TTL="${TL_SESSION_TTL:-60}"    # current_char 缓存秒数
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
T_HTTP="${TL_T_HTTP:-8}"       # HTTP 探测（轮询/查 session）
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
  [ -n "${SSE_PID:-}" ] && kill "$SSE_PID" 2>/dev/null
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

# ---- 收信：轮询为主，SSE 只负责低延迟 ------------------------------------
# 为什么不能只靠 SSE：SSE 没有重放。任何在断线窗口里 publish 的事件就永久丢失，
# 而 `GET /api/history?limit=1` 能把状态对齐到「服务端真相」，所以它才是保证。
# SSE 只用来把延迟从 ~POLL 秒压到 ~0.1 秒；它挂了最多延迟一下，不会丢通知。
auth=(-H "Authorization: Bearer $TOKEN")

enc() { jq -rn --arg s "$1" '$s|@uri'; }

# auto 模式下问服务端要当前角色，这样 WebUI 换角色手机这边自动跟着走。
# 结果缓存 SESSION_TTL 秒，别每 5 秒问一次。
_cached_s=""; _cached_at=0
resolve_session() {
  [ "$SESSION" != auto ] && { printf %s "$SESSION"; return 0; }
  local now; now=$(date +%s)
  if [ -n "$_cached_s" ] && [ $((now - _cached_at)) -lt "$SESSION_TTL" ]; then
    printf %s "$_cached_s"; return 0
  fi
  local s
  s=$(t "$T_HTTP" curl -s -m 5 "${auth[@]}" "$HOST/api/settings" 2>/dev/null \
        | jq -r '.current_char // empty' 2>/dev/null)
  [ -n "$s" ] || { [ -n "$_cached_s" ] && { printf %s "$_cached_s"; return 0; }; return 1; }
  _cached_s="$s"; _cached_at="$now"
  printf %s "$s"
}

# 投递：去重 + 记状态 + 通知。SSE 和轮询共用，保证同一 id 只响一次。
# 两条路径是**两个进程**（SSE 在后台子 shell，轮询在主循环），会并发到达，
# 所以检查和写 last_id 必须用 flock 串行化，否则两边可能同时通过检查各响一次。
exec 8>"$D/deliver.lock"
deliver() {
  local id="$1" txt="$2"
  [ -n "$id" ] && [ -n "$txt" ] || return 0
  flock -w 3 8 || return 0
  [ "$id" = "$(cat "$D/last_id" 2>/dev/null)" ] && { flock -u 8; return 0; }
  echo "$id" >"$D/last_id"
  flock -u 8
  local short
  short=$(tr -s '[:space:]' ' ' <<<"$txt" | cut -c1-140)
  log "NOTIFY id=$id len=${#txt} head=${short:0:60}"
  notify "$id" "${TL_TITLE:-TavernLab}" "$short"
}

# SSE 事件 -> 投递
on_event() {
  local j="$1"
  [ "$(jq -r '.role // empty' <<<"$j" 2>/dev/null)" = assistant ] || return 0
  deliver "$(jq -r '.id // empty' <<<"$j" 2>/dev/null)" \
           "$(jq -r '.text // empty' <<<"$j" 2>/dev/null)"
}

# 轮询兜底：拉最近若干条，取**最新一条 assistant**。
# 这里是「漏不掉」的保证——不管 SSE 断了几秒、错过了几条，下一轮都会补齐。
#
# 关键：必须取「最新一条 assistant」当高水位，不能取「最新一条未通知过的
# assistant」。聊天是只追加的，所以最新 assistant 只会单调往前推进，last_id
# 跟着它走就自然收敛。取「最新未通知」则会在有积压时乒乓循环：
# 通知 A → last_id=A → 下一轮 B 变成未通知 → 通知 B → last_id=B → A 又变未通知…
# 实测就是这么刷成每 6 秒两声乒乓的。
catchup() {
  local s="$1" j cand
  j=$(t "$T_HTTP" curl -s -m 6 "${auth[@]}" \
        "$HOST/api/history?session=$(enc "$s")&limit=10" 2>/dev/null) || return 0
  cand=$(jq -c '[.messages[]? | select(.role=="assistant" and ((.text//"")|length)>0)] | last // empty' \
        <<<"$j" 2>/dev/null)
  on_event "$cand"
}

# 冷启动静默播种：last_id 不存在时，把当前最后一条记下来但**不通知**，
# 否则一开机就会为重启前的旧回复响一声。
seed() {
  [ -f "$D/last_id" ] && return 0
  local s j id
  s=$(resolve_session) || return 0
  j=$(t "$T_HTTP" curl -s -m 6 "${auth[@]}" \
        "$HOST/api/history?session=$(enc "$s")&limit=1" 2>/dev/null) || return 0
  id=$(jq -r '.messages[-1].id // empty' <<<"$j" 2>/dev/null)
  [ -n "$id" ] || return 0
  echo "$id" >"$D/last_id"
  log "seed last_id=$id (silent, 不为旧消息响铃)"
}
# SSE 独立循环，跑在后台。它挂了不影响轮询，反之亦然。
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
    # 外层 timeout 只能比 SSE_MAXTIME 大一点：它在这里是「防挂死」的安全网，
    # 不是节流阀。曾经写成 `t 5`，结果每 5 秒把长连接杀一次（rc=124），
    # 看着像「连不上」，其实是自杀。
    t $((SSE_MAXTIME + 30)) curl -sN --max-time "$SSE_MAXTIME" "${auth[@]}" \
      "$HOST/api/events?session=$(enc "$s")" 2>>"$LOG" \
    | while IFS= read -r l; do
        case "$l" in data:*) on_event "${l#data: }" ;; esac
      done
    rc=${PIPESTATUS[0]}
    log "sse closed rc=$rc"
    if [ "$rc" = 28 ]; then
      # rc=28 是**我们自己**的 --max-time 到点，连接本身是健康的。
      # 之前这里当成失败、sleep 退避后再涨，导致每 ~2.5 分钟就有 30 秒死窗口；
      # 而 SSE 不重放，落在窗口里的事件永久丢失。聊天节奏和这个周期一旦同频就会
      # 锁相，变成「每条都不通知」。所以健康断开必须立刻重连、不涨退避。
      backoff=1
      sleep 1
    elif [ "$rc" = 124 ]; then
      # 外层安全网超时。健康断开，同样立刻重连。
      backoff=1
      sleep 1
    else
      sleep "$backoff"
      [ "$backoff" -lt "$BACKOFF_MAX" ] && backoff=$((backoff * 2)) || backoff=$BACKOFF_MAX
    fi
  done
}

log "=== start pid=$$ host=$HOST session=$SESSION sound=$SOUND poll=${POLL}s ==="

seed
sse_loop &
SSE_PID=$!

# 主循环：只管轮询。间隔内没消息也照跑，SSE 的结果由 last_id 去重。
while :; do
  if s=$(resolve_session); then
    catchup "$s"
  else
    log "poll: resolve session 失败"
  fi
  sleep "$POLL"
done
