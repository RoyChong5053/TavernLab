#!/data/data/com.termux/files/usr/bin/bash
# install.sh — 把 gps-logger 装到 GT20 Termux 里 (幂等, 不自启)
#
#   ./install.sh --host http://192.168.100.78:8888 --token <app_token> [--interval 600]
#
# 注意: 这是**手动运行**的脚本, 不写 ~/.termux/boot, 不后台起任何东西。
# 装完自己敲 gps-logger-python 跑, Ctrl-C 退出。
set -uo pipefail
export PATH="$HOME/bin:$PREFIX/bin:$PATH"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOST=""; TOKEN=""; INTERVAL=600
while [ $# -gt 0 ]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done
die() { echo "install: $*" >&2; exit 1; }

echo "== 依赖检查 =="
command -v termux-location >/dev/null \
  || die "缺 termux-location (装 com.termux.api 并给位置权限, 且要选「始终允许」)"
command -v python3 >/dev/null || die "缺 python3 (pkg install python)"
if python3 -c 'import rich' 2>/dev/null; then
  echo "  rich 已装 (有彩色面板)"
else
  echo "  提示: 没装 rich, 仍可用 (pip install rich 可启用面板)"
fi
for p in com.termux com.termux.api; do
  pm path "$p" >/dev/null 2>&1 && echo "  $p 已安装" || echo "  警告: $p 未安装"
done

echo "== 生成配置 =="
CONF="$HOME/.config/gps-logger/env"
mkdir -p "$(dirname "$CONF")"
if [ -f "$CONF" ] && [ -z "$TOKEN" ]; then
  echo "  已存在, 沿用 (要覆盖就加 --token)"
else
  [ -n "$HOST" ] || die "缺 --host"
  [ -n "$TOKEN" ] || die "缺 --token"
  cat >"$CONF" <<EOF
# gps-logger 配置 (由 install.sh 生成, 勿提交)
HOST=$HOST
TOKEN=$TOKEN
GL_INTERVAL=$INTERVAL
EOF
  chmod 600 "$CONF"
  echo "  写入 $CONF"
fi

echo "== 安装脚本 =="
mkdir -p "$HOME/bin" "$PREFIX/var/lib/gps-logger"
install -m 700 "$SRC/gps-logger-python" "$HOME/bin/gps-logger-python"
echo "  ~/bin/gps-logger-python"

# 清掉旧版残留 (v0 的 bash 守护进程)。旧版会写 ~/.termux/boot 自启并常驻,
# 跟「手动运行」冲突, 这里一并清掉。只删自己装的路径。
echo "== 清理旧版 =="
for f in "$HOME/bin/gps-logger.sh" "$HOME/bin/gps-ctl" \
         "$HOME/.termux/boot/10-gps-logger"; do
  if [ -e "$f" ]; then rm -f "$f" && echo "  删除 $f"; fi
done
if [ -f "$PREFIX/var/lib/gps-logger/pid" ]; then
  p=$(cat "$PREFIX/var/lib/gps-logger/pid" 2>/dev/null)
  if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then
    kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null
    echo "  停掉旧 daemon pid=$p"
  fi
  rm -f "$PREFIX/var/lib/gps-logger/pid"
fi

cat <<'EOF'

完成。**不会开机自启, 装完自己跑**:

  gps-logger-python              # 前台面板, Ctrl-C 退出
  gps-logger-python --once       # 打一个点就退出 (可放 cron)
  gps-logger-python --interval 300
  gps-logger-python --no-post    # 只本地看, 不发 m64 (调试)

配套:
  pip install rich               # 彩色面板 (不装也能跑, 自动降级纯文本)
  Android 设置 -> 应用 -> Termux:API -> 通知 -> 全关
      从系统层面根治 Termux:API 报错弹窗, 比脚本 hack 可靠。

注意: 纯手动 = 只在你开着 Termux 时上报。关掉 Termux 就不再更新,
      服务端 30 分钟后会把 {{location}} 标成「已过期」。
EOF
