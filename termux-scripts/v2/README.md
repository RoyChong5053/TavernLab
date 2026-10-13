# v2 — m64 检测 + 手机被动通知（手动运行，零 termux-boot）

旧 `notification/`（手机轮询+SSE常驻）一行不动，留着当回滚。
v2 把“检测有没有新消息”搬到 m64，手机只在被推时醒一次。
平时不跑任何守护进程也能收通知——唯一的常驻是本来就要活着的 `sshd`。

```
m64 (常开)                              GT20 (平时睡觉)
┌────────────────────────────┐          ┌──────────────────────────┐
│ m64-watch.sh (tmux tlpush) │          │ sshd:8022 (boot自启,保留) │
│  ├ SSE 订阅 localhost:8888 │          │                          │
│  └ 每20s轮询兜底(本机回环) │          │  ssh 推来时才执行:        │
│         │ last_id 高水位    │  ssh via Tailscale   │  tl-push → termux-  │
│         └──────────────────┼──100.75.97.4:8022──► │  notification      │
└────────────────────────────┘          └──────────────────────────┘
手机手动跑 tl-receiver.py 看面板(连接/上次推送), Ctrl-C 即关, 确定性不耗电。
```

## 省电账

| | v1（手机常驻） | v2 |
|---|---|---|
| 手机轮询/SSE | 每5s醒一次 + 900s长连 | 无 |
| 手机常驻脚本 | tl-notify.sh 全天 | 无（receiver 手动开才跑，不开也照样收推送） |
| 手机wakelock | tl-notify 持有（可关，但你别的脚本要） | v2 不动它，别的脚本照用 |
| 新增成本 | — | m64 每20s本机回环一次（忽略不计）+ 推来时手机醒1s |

## 文件

| 文件 | 跑在哪 | 说明 |
|---|---|---|
| `m64-watch.sh` | m64，tmux `tlpush` | 检测+推送，见下 |
| `phone/tl-push` | 手机 `~/bin/tl-push` | 被ssh调用，无循环，只弹一条（flock+去重+timeout） |
| `phone/tl-receiver.py` | 手机手动跑 | rich面板：m64连通/sshd/上次推送/计数，`空格`自检，`q`退出 |
| `phone/install-v2.sh` | 手机上跑一次 | 装`~/bin`两个文件+建渠道，**不写`~/.termux/boot`** |

`--action` 点跳复用 v1 的 `~/bin/open-app.sh`（已在手机上，不动它；
`install-v2.sh` 会检查，缺了就报错提醒先装过一次 v1）。

## 安装

m64（一次）：

```bash
cd ~/app/TavernLab/termux-scripts/v2
TOKEN=$(python3 -c "import json;print(json.load(open('/home/roychong/app/TavernLab/data/settings.json'))['app_token'])")
./m64-watch.sh --install --token "$TOKEN"   # 写入 ~/.config/tl-push-watch/env + 起 tmux tlpush
```

手机（一次，本仓库已在手机上，或 `scp` 过去）：

```bash
cd ~/app/TavernLab/termux-scripts/v2/phone   # 路径按你手机实际clone位置
./install-v2.sh
tl-receiver.py        # 想看面板就跑，不跑也能收推送
```

旧守护进程下线（手机 \(ssh gt20\)，**保留 `00-sshd`**）：

```bash
tlctl stop
rm ~/.termux/boot/00-tl-notify
pgrep -af tl-notify.sh   # 应为空
```

## 验证

1. `ssh m64 'tmux capture-pane -p -t tlpush | tail -5'` 见 `listen/watch ok`。
2. 发一句，手机 1~2s 内响；`~/.local/share/tl-push-watch/log` 有 `PUSH id=`。
3. 手机断网 2 分钟再回，只补最新一条（高水位，不补历史）。
4. `tl-receiver.py` 面板 m64 显示绿色可达。

## 回滚

```bash
# 手机
tlctl start   # v1 回来（boot 文件删了，重启后不会自起，想自起再装一次v1 install.sh）
# m64
tmux kill-session -t tlpush
```

## 还没做的

- App 在前台时 m64 跳过推送（需 `/api/presence`，目前前台会和 App 自己的通知各响一次，可接受）。
- 多角色：`TL_SESSION=auto` 跟随 `current_char`，和 v1 一致。
