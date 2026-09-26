#!/data/data/com.termux/files/usr/bin/bash
# uninstall.sh — 移除 tl-notify（可留数据，加 --purge 一并清）
set -uo pipefail
export PATH="$HOME/bin:$PREFIX/bin:$PATH"

D="$PREFIX/var/lib/tl-notify"
PURGE=0
[ "${1:-}" = "--purge" ] && PURGE=1

echo "== 停进程 =="
[ -f "$D/pid" ] && kill "$(cat "$D/pid")" 2>/dev/null
pkill -x curl 2>/dev/null
sleep 1
pkill -9 -x curl 2>/dev/null
termux-wake-unlock 2>/dev/null
rm -f "$D/pid"

echo "== 移除开机自启 =="
rm -f "$HOME/.termux/boot/00-tl-notify"

echo "== 移除脚本 =="
rm -f "$HOME/bin/tl-notify.sh" "$HOME/bin/tlctl"

if [ "$PURGE" = 1 ]; then
  echo "== 清数据 =="
  rm -rf "$D" "$PREFIX/share/tl-chime.wav"
  termux-notification-channel -d tavernlab-reply >/dev/null 2>&1
  echo "  已删渠道 tavernlab-reply"
fi
echo "  配置保留在 ~/.config/tl-notify/env（--purge 也不删，自己决定）"

echo "完成。"
