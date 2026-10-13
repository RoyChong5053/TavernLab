#!/data/data/com.termux/files/usr/bin/python3
# -*- coding: utf-8 -*-
# tl-receiver — v2 手机侧手动面板 (只看不轮询, Ctrl-C/q 退出)
#
# 不检测 TavernLab 新消息 (那是 m64-watch.sh 的活)。本面板只显示:
#   m64 经 Tailscale 可达性 / sshd / 上次推送 / v1 旧守护进程残留警告
# 关掉 = 确定性零耗电, 推送照样收 (靠 sshd + tl-push)。
import json
import os
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

PREFIX = os.environ.get("PREFIX", "/data/data/com.termux/files/usr")
D = Path(PREFIX) / "var" / "lib" / "tl-push"
LAST_JSON = D / "last.json"
COUNT = D / "count"
M64 = os.environ.get("TL_M64", "http://100.116.75.51:8888")

try:
    from rich.console import Console
    from rich.live import Live
    from rich.table import Table
    from rich import box
    HAS_RICH = True
except ImportError:
    HAS_RICH = False

_STOP = [False]

def _on_signal(signum, frame):
    _STOP[0] = True

signal.signal(signal.SIGINT, _on_signal)
signal.signal(signal.SIGTERM, _on_signal)

def sh(cmd, timeout=6):
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True,
                           timeout=timeout, text=True)
        return r.returncode, (r.stdout or "").strip()
    except Exception:
        return -1, ""

def m64_health():
    t0 = time.time()
    try:
        req = urllib.request.Request(M64 + "/api/health", method="GET")
        with urllib.request.urlopen(req, timeout=6) as r:
            body = r.read(120).decode("utf-8", "replace")
            ms = int((time.time() - t0) * 1000)
            return True, "%dms" % ms, body[:60]
    except Exception as e:
        return False, "-", str(e)[:60]

def sshd_ok():
    rc, out = sh("pgrep -a sshd | head -2")
    return rc == 0 and bool(out), out.split("\n")[0][:60] if out else "-"

def v1_running():
    rc, out = sh("pgrep -af tl-notify.sh | grep -v pgrep | head -3")
    procs = [l for l in out.split("\n") if l and "tl-receiver" not in l
             and "grep" not in l and "sshd-session" not in l
             and "echo ===" not in l]
    # 上面经 ssh -c 传进来会把检查命令自己也匹配上, 只认 home/bin 路径的
    procs = [l for l in procs if "home/bin/tl-notify.sh" in l]
    return bool(procs), procs[:2]

def last_push():
    try:
        j = json.loads(LAST_JSON.read_text(encoding="utf-8"))
        return j.get("at", "-"), j.get("head", "-"), j.get("id", "-")[:8]
    except Exception:
        return "-", "尚无推送", "-"

def count():
    try:
        return int(COUNT.read_text().strip())
    except Exception:
        return 0

def self_test():
    sh("tl-push 'selftest' 'TavernLab' <<< 'receiver自检: 本地通知链路正常'", timeout=10)

def build_rich(st):
    t = Table(title="tl-receiver · v2被动通知面板 (q退出/空格自检)",
              box=box.SIMPLE, expand=True)
    t.add_column("项", style="cyan")
    t.add_column("状态")
    t.add_column("详情", overflow="fold")
    ok, ms, _ = st["m64"]
    t.add_row("m64(Tailscale)", "[green]可达 %s[/]" % ms if ok else "[red]不可达[/]",
              M64 if ok else st["m64"][2])
    ok, d = st["sshd"]
    t.add_row("sshd", "[green]运行中[/]" if ok else "[red]没起!![/]", d)
    running, procs = st["v1"]
    if running:
        t.add_row("v1旧守护", "[red]还在跑(双响+耗电)[/]", procs[0][:70])
    else:
        t.add_row("v1旧守护", "[green]已停[/]", "tl-notify 无残留")
    at, head, iid = st["last"]
    t.add_row("上次推送", at, "%s… %s" % (iid, head[:50]))
    t.add_row("累计推送", str(st["n"]), "关掉本面板照样收 (靠sshd+tl-push)")
    return t

def build_plain(st):
    L = ["== tl-receiver (q退出/空格自检) =="]
    ok, ms, d = st["m64"]
    L.append("m64: %s %s" % ("可达 " + ms if ok else "不可达", "" if ok else d))
    ok, d = st["sshd"]
    L.append("sshd: %s %s" % ("运行中" if ok else "没起!!", d))
    running, procs = st["v1"]
    L.append("v1旧守护: %s" % ("还在跑!! " + (procs[0][:60] if procs else "") if running else "已停"))
    at, head, iid = st["last"]
    L.append("上次推送: %s %s %s" % (at, iid, head[:50]))
    L.append("累计推送: %d" % st["n"])
    return "\n".join(L)

def snapshot():
    return {"m64": m64_health(), "sshd": sshd_ok(),
            "v1": v1_running(), "last": last_push(), "n": count()}

def main():
    import termios
    import tty
    import select
    console = Console() if HAS_RICH else None
    fd = sys.stdin.fileno()
    old_attrs = None
    try:
        old_attrs = termios.tcgetattr(fd)
        tty.setcbreak(fd)
    except Exception:
        pass

    def cleanup():
        if old_attrs is not None:
            try:
                termios.tcsetattr(fd, termios.TCSADRAIN, old_attrs)
            except Exception:
                pass
        print("\n已退出 · 推送不受影响 (sshd+tl-push 常在)")

    try:
        if HAS_RICH:
            with Live(console=console, refresh_per_second=2,
                      screen=True) as live:
                while not _STOP[0]:
                    st = snapshot()
                    live.update(build_rich(st))
                    end = time.time() + 5
                    while not _STOP[0] and time.time() < end:
                        try:
                            r, _, _ = select.select([fd], [], [], 0.4)
                        except Exception:
                            break
                        if r:
                            ch = os.read(fd, 1).decode("utf-8", "replace")
                            if ch in ("q", "Q", "\x03"):
                                _STOP[0] = True
                                break
                            if ch == " ":
                                self_test()
                                break
        else:
            while not _STOP[0]:
                print("\033[2J\033[H" + build_plain(snapshot()))
                time.sleep(5)
    except KeyboardInterrupt:
        pass
    finally:
        cleanup()

if __name__ == "__main__":
    main()
