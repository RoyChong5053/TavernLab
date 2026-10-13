#!/data/data/com.termux/files/usr/bin/bash
# install-v2.sh — 手机侧装 v2（只放 ~/bin，不写 ~/.termux/boot）
set -uo pipefail
export PATH="$HOME/bin:$PREFIX/bin:$PATH"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "== 依赖检查 =="
command -v termux-notification >/dev/null || { echo "缺 Termux:API" >&2; exit 1; }
command -v python3 >/dev/null || { echo "缺 python" >&2; exit 1; }
[ -x "$HOME/bin/open-app.sh" ] || { echo "缺 ~/bin/open-app.sh（先装过一次 v1，点跳要用它）" >&2; exit 1; }
command -v sshd >/dev/null || { echo "缺 openssh（pkg install openssh）" >&2; exit 1; }

echo "== 安装 =="
mkdir -p "$HOME/bin" "$PREFIX/var/lib/tl-push"
install -m 700 "$SRC/tl-push" "$HOME/bin/tl-push"
install -m 700 "$SRC/tl-receiver.py" "$HOME/bin/tl-receiver.py"
echo "  ~/bin/tl-push"
echo "  ~/bin/tl-receiver.py"

echo "== 通知渠道（只建一次，不覆盖已有） =="
CH=tavernlab-reply
if [ ! -f "$PREFIX/var/lib/tl-push/channel.done" ]; then
  $PREFIX/libexec/termux-api NotificationChannel \
    --es id "$CH" --es name TavernLabReply --es priority high >/dev/null 2>&1 \
    && touch "$PREFIX/var/lib/tl-push/channel.done" \
    && echo "  渠道 $CH 已建 IMPORTANCE_HIGH" \
    || echo "  渠道创建失败（已有也算正常）"
else
  echo "  渠道已建过，跳过"
fi

echo "== 自检 =="
echo "receiver自检" | "$HOME/bin/tl-push" "selftest" "TavernLab" \
  && echo "  本地通知链路 OK（手机上应有一条自检通知）"

cat <<'EOF'

完成。注意：
- 没有写 ~/.termux/boot，不自启。tl-receiver.py 想看才跑，不跑也收推送。
- 旧 v1 要下线：tlctl stop && rm ~/.termux/boot/00-tl-notify（保留 00-sshd！）
- m64 侧起 watcher：termux-scripts/v2/m64-watch.sh --install --token <app_token>
EOF
