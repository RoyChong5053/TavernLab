#!/data/data/com.termux/files/usr/bin/bash
# install.sh — 把 gps-logger 装到 GT20 Termux 里 (幂等)
#   ./install.sh --host http://192.168.100.78:8888 --token <app_token> [--interval 600]
set -uo pipefail
export PATH="$HOME/bin:$PREFIX/bin:$PATH"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOST=""; TOKEN=""; INTERVAL=600
while [ $# -gt 0 ]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    -h|--help) sed -n '2,4p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
die() { echo "install: $*" >&2; exit 1; }

for c in curl jq; do command -v "$c" >/dev/null || die "缺 $c (pkg install $c)"; done
command -v termux-location >/dev/null || die "缺 Termux:API (装 com.termux.api)，且给位置权限"
command -v termux-battery-status >/dev/null || echo "  提示: termux-battery-status 不可用，batt=0"

CONF="$HOME/.config/gps-logger/env"
mkdir -p "$(dirname "$CONF")"
if [ -f "$CONF" ] && [ -z "$TOKEN" ]; then
  echo "  已存在，沿用 (要覆盖就加 --token)"
else
  [ -n "$HOST" ] || die "缺 --host"
  [ -n "$TOKEN" ] || die "缺 --token"
  cat >"$CONF" <<EOF
# gps-logger 配置 (由 install.sh 生成, 勿提交)
HOST=$HOST
TOKEN=$TOKEN
GL_INTERVAL=$INTERVAL
GL_WAKELOCK=1
EOF
  chmod 600 "$CONF"
  echo "  写入 $CONF"
fi

mkdir -p "$HOME/bin"
install -m 700 "$SRC/gps-logger.sh" "$HOME/bin/gps-logger.sh"
install -m 700 "$SRC/gps-ctl" "$HOME/bin/gps-ctl"
echo "  ~/bin/gps-logger.sh ~/bin/gps-ctl"

BOOTDIR="$HOME/.termux/boot"
mkdir -p "$BOOTDIR"
cat >"$BOOTDIR/10-gps-logger" <<'EOF'
#!/data/data/com.termux/files/usr/bin/bash
export PATH="$HOME/bin:$PREFIX/bin:$PATH"
exec "$HOME/bin/gps-ctl" start
EOF
chmod 700 "$BOOTDIR/10-gps-logger"
echo "  ~/.termux/boot/10-gps-logger"

"$HOME/bin/gps-ctl" restart
cat <<'EOF'

完成。常用:
  gps-ctl status / gps-ctl logs / gps-ctl restart
手机需: 位置权限始终允许 + 省电白名单 + Termux:Boot 开机自启。
EOF
