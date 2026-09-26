#!/data/data/com.termux/files/usr/bin/bash
# install.sh — 把 tl-notify 装到 Termux 里（幂等，可重复跑）
#
#   ./install.sh --host http://192.168.100.78:8888 --token <app_token> [--session auto]
#
# token 从 TavernLab 的 data/settings.json 取（app_token 字段），
# 它同时也是手机 App 用的那个静态 token，够用即可读 /api/events 与 /api/settings。
set -uo pipefail
export PATH="$HOME/bin:$PREFIX/bin:$PATH"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFIX_DIR="$PREFIX"

HOST=""; TOKEN=""; SESSION="auto"
while [ $# -gt 0 ]; do
  case "$1" in
    --host)    HOST="$2"; shift 2 ;;
    --token)   TOKEN="$2"; shift 2 ;;
    --session) SESSION="$2"; shift 2 ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done

die() { echo "install: $*" >&2; exit 1; }

echo "== 依赖检查 =="
for c in curl jq python flock; do
  command -v "$c" >/dev/null || die "缺 $c（pkg install $c）"
done
for p in com.termux com.termux.api; do
  pm path "$p" >/dev/null 2>&1 && echo "  $p 已安装" || echo "  警告：$p 未安装"
done
command -v termux-notification >/dev/null || die "缺 Termux:API（装 com.termux.api）"

echo "== 生成配置 =="
CONF="$HOME/.config/tl-notify/env"
mkdir -p "$(dirname "$CONF")"
if [ -f "$CONF" ] && [ -z "$TOKEN" ]; then
  echo "  已存在，沿用（要覆盖就加 --token）"
else
  [ -n "$HOST" ]  || die "缺 --host"
  [ -n "$TOKEN" ] || die "缺 --token"
  cat >"$CONF" <<EOF
# tl-notify 配置（由 install.sh 生成，勿提交）
HOST=$HOST
TOKEN=$TOKEN

# auto = 跟随服务端 current_char（WebUI 换角色自动跟随）；填字面量则钉死
TL_SESSION=$SESSION

# 声音：both=通知(可见+点击)+chime(发声，默认) | notification=只靠通知渠道
#       chime=只出声不弹通知 | none=静默
# both 是默认，因为本机 ROM 的 AI 通知过滤会静音通知音效，只走 notification 不会响
TL_SOUND=both
# media 播放走 STREAM_MUSIC。music 音量低时锁屏听不见，这里临时抬到多少再恢复（0=不动）
TL_CHIME_BOOST=11
# TTS 朗读回复全文（ALARM 流，锁屏可听）
TL_TTS=0
EOF
  chmod 600 "$CONF"
  echo "  写入 $CONF"
fi

echo "== 安装脚本 =="
mkdir -p "$HOME/bin" "$PREFIX_DIR/share"
for f in tl-notify.sh tlctl open-app.sh; do
  install -m 700 "$SRC/$f" "$HOME/bin/$f"
  echo "  ~/bin/$f"
done
chmod 700 "$SRC/gen-chime.py" 2>/dev/null
python3 "$SRC/gen-chime.py" "$PREFIX_DIR/share/tl-chime.wav"

echo "== 通知渠道（重要度不可变，先删后建） =="
CH=tavernlab-reply
termux-notification-channel -d "$CH" >/dev/null 2>&1
$PREFIX_DIR/libexec/termux-api NotificationChannel \
  --es id "$CH" --es name TavernLabReply --es priority high
mkdir -p "$PREFIX_DIR/var/lib/tl-notify"
echo high >"$PREFIX_DIR/var/lib/tl-notify/channel.importance"

echo "== 开机自启 =="
BOOTDIR="$HOME/.termux/boot"
mkdir -p "$BOOTDIR"
cat >"$BOOTDIR/00-tl-notify" <<'EOF'
#!/data/data/com.termux/files/usr/bin/bash
# Termux:Boot 会在每次开机后执行本文件。开机时 WiFi 通常还没起来，
# tl-notify.sh 内部的退避重连会自己接上（最多 BACKOFF_MAX 秒）。
export PATH="$HOME/bin:$PREFIX/bin:$PATH"
exec "$HOME/bin/tlctl" start
EOF
chmod 700 "$BOOTDIR/00-tl-notify"
echo "  ~/.termux/boot/00-tl-notify"

echo "== 重启使其生效 =="
# 必须 restart：env 是守护进程启动时 source 进来的，tlctl start 在已在跑时
# 会 early-return，配置改了不重启等于没改。restart 在没跑时也能正常工作。
"$HOME/bin/tlctl" restart

cat <<'EOF'

完成。常用：
  tlctl status    看状态 + 最近日志
  tlctl logs      跟日志
  tlctl restart   重启

重启手机后不需要手动操作：Termux:Boot 会自动跑 ~/.termux/boot/00-tl-notify。
若开机没自动起，检查 com.termux.boot 是否被 ROM 限制后台（见 README「故障排查」）。
EOF
