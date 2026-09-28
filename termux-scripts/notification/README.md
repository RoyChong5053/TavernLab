# termux-scripts — TavernLab 手机端后台通知

在手机上用 Termux 监听 TavernLab 的 SSE，回复到达时弹本地通知。
**纯本地链路**：不经 Google/FCM，不需要 ntfy，不改 TavernLab 服务端一行代码。

```
TavernLab (m64)                        手机
┌──────────────────────┐              ┌───────────────────────────────┐
│ 一次生成完成          │              │ Termux (前台服务)              │
│  AppendChat          │  ── SSE ──►  │  tl-notify.sh                 │
│   + events.publish   │  长连接       │   ├ sse_loop  后台：低延迟     │
│                      │              │   └ 主循环    每 5s：兜底      │
│  GET /api/history    │  ◄── 轮询 ── │      ↑ 两者都走 deliver()      │
│  ?limit=10           │              │        单调高水位 + flock 去重  │
│  (脚本只读，不写)     │              │            │                  │
└──────────────────────┘              │            ▼                  │
                                      │  termux-notification  → 可见+响 │
                                      │  --action: am start  → 回 App  │
                                      └───────────────────────────────┘
```

**SSE 只负责低延迟，轮询负责「不漏」。** 两者都调 `deliver()`，用同一个
`last_id` 高水位 + `flock` 去重，所以谁先到都只响一次。

## 为什么需要它

App 自己在锁屏时是收不到通知的，不是通知被拦，是**事件根本没到**：

1. `android_app/lib/main.dart` 的 `didChangeAppLifecycleState` 在非 resumed 时调
   `stopPolling()`，5 秒轮询（代码注释自称 primary path）直接停掉。
2. 后台只剩 `startEvents()` 那条 SSE，而它是**一次性的**：无重连、无心跳，
   `onError: (_) {}` + `cancelOnError: false` 把错误吞掉。Doze 一挂断网络就永久死掉。
3. 没有前台服务 / wake lock，进程是 app-freezer 和厂商清理的合法目标。

Termux 则是真正的前台服务（`TermuxService.startForeground`），加上
`termux-wake-lock` 拿到的 `PARTIAL_WAKE_LOCK` + `WIFI_MODE_FULL_HIGH_PERF`
的 WifiLock，可以在熄屏下持续收流。

**但光有 SSE 不够。** SSE 没有重放：断线窗口里 publish 的事件就永久丢失。
所以主路径是每 5 秒 `GET /api/history?limit=10` 对齐服务端真相，SSE 只是把
延迟从 ~5 秒压到 ~0.1 秒。这条是踩过坑才补上的，见下面「锁相」那条。

## 安装

在 Termux 里（`~/app/TavernLab/termux-scripts` 已 clone 到手机上的任意位置）：

```bash
# token 从 m64 的 data/settings.json 里取 app_token 字段
./install.sh --host http://192.168.100.78:8888 --token <app_token>
```

`--host` 用 m64 的 **WiFi IP**（`192.168.100.78`）。m64 有两条网线，
`192.168.10.2` 是有线口，手机在 `192.168.100.x` 的 WiFi 上，走不通（实测 curl 返回 000）。

install.sh 是幂等的，重复跑安全。依赖：`curl` `jq` `python` `flock` + `com.termux.api`；
开机自启另需 `com.termux.boot`。

## 重启手机后怎么运行

**什么都不用做。** `com.termux.boot` 会在每次开机后执行 `~/.termux/boot/` 下所有可执行文件，
install.sh 已经放了一个：

```
~/.termux/boot/00-tl-notify   ->  exec ~/bin/tlctl start
```

开机时 WiFi 通常还没起来，脚本内部会退避重连（1→2→4…上限 `BACKOFF_MAX`=30s），
自己就接上了。日志在 `$PREFIX/var/lib/tl-notify/log`。

想手动起：`tlctl start`。想停：`tlctl stop`（用 pidfile 精确定位，
不用 `pkill -f` —— 那会匹配到执行 pkill 的 shell 自己，实测会把自己杀掉）。

若开机没自动起，按下面「故障排查」第 1 条查 `com.termux.boot` 有没有被 ROM 限制。

## 配置

`~/.config/tl-notify/env`（`chmod 600`，**不要提交到 git**）

| 键 | 默认 | 说明 |
|---|---|---|
| `HOST` | 必填 | TavernLab 根地址，走 **WiFi IP** |
| `TOKEN` | 必填 | `app_token`，App 用的同一个静态 token |
| `TL_SESSION` | `auto` | `auto` = 跟随服务端 `current_char`（WebUI 换角色自动跟随）；填字面量则钉死 |
| `TL_SOUND` | `notification` | `notification` 只走通知渠道（默认）/ `chime` 只出声 / `both` 两者都发（**会响两声**）/ `none` 静默 |
| `TL_CHIME_MS` | `1500` | chime 播放时长（毫秒），播完即停 |
| `TL_CHIME` | `$PREFIX/share/tl-chime.wav` | 提示音路径 |
| `TL_CHIME_BOOST` | `0` | `>0` 时把 music 流临时抬到该音量播完再恢复（见下） |
| `TL_TTS` | `0` | `1` = 用 TTS 朗读回复全文 |
| `TL_TAP` | `1` | `0` = 不设点击动作（没开「显示在其他应用上层」时用） |
| `TL_TTS_STREAM` | `ALARM` | TTS 音频流 |
| `TL_POLL` | `5` | 兜底轮询间隔秒数，**这是「不漏」的实际保证** |
| `TL_SSE_MAXTIME` | `900` | SSE 长连接最长保持秒数（服务端每 25s 发 `: ping` 保活） |
| `TL_SESSION_TTL` | `60` | `current_char` 缓存秒数，免得每 5 秒问一次服务端 |
| `TL_BACKOFF_MAX` | `30` | 退避上限（秒） |
| `TL_CHANNEL` | `tavernlab-reply` | 通知渠道 id |
| `TL_IMPORTANCE` | `high` | 渠道重要度 |
| `TL_WAKELOCK` | `1` | 是否 `termux-wake-lock` |

改完 `tlctl restart`。

## 这台设备上的已知问题

**ROM 的「AI notification 过滤」曾经会静音通知的音效。** 当时的症状：通知正常
显示在锁屏，但完全没声音。已排除的原因：通知/响铃/闹钟音量都是 13/15、渠道
声音开关是开的、渠道已是 `IMPORTANCE_HIGH`、且 `termux-media-player` 与
`termux-tts-speak -s ALARM` 在锁屏下都能正常发声（说明是通知管线被拦，不是音频
能力问题）。后来这个过滤不再拦了，`TL_SOUND=notification` 单路径就能出一声干净的。

所以 `chime` 留作**备用**：ROM 更新后过滤若再次生效，改 `TL_SOUND=both` 或
`chime` 就能顶回来，不用改代码。

**别开 `both`。** 它会先响通知渠道、再播 chime，一句回复两声。

想彻底修根因：确认 ROM 设置里 Termux:API 的 AI 通知过滤是关的。

`termux-media-player` 走 `STREAM_MUSIC`，而本机 music 只有 5/15，锁屏偏轻。
`TL_CHIME_BOOST=11` 会在播之前把 music 临时抬到 11、播完恢复原值 ——
注意这是**会动你全局音乐音量**的，介意就设 0。

## 点通知跳 TavernLab：需要开一个权限

`--action` 的执行链路是：

```
用户点通知 → PendingIntent（属于 com.termux.api）
          → Intent(ACTION_SERVICE_EXECUTE, sh -c "<action>", runner=APP_SHELL)
          → setClassName("com.termux", TermuxService)
          → TermuxService 进程里执行 ~/bin/open-app.sh
          → am start -n com.tavernlab.app/.MainActivity
```

实测：action **确实执行了**，`am` 也**确实返回 rc=0**（日志见 `$PREFIX/tmp/open-app.log`），
但 App 没被拉到前台。原因是 **Android 10+ 的后台启动 Activity 限制**：
用户点通知给的是**通知所属的包**（`com.termux.api`）一个临时放行，
而实际启动 Activity 的是**另一个包**（`com.termux`），拿不到这个豁免，被系统静默拦下。

**解法：给 Termux 开「显示在其他应用上层」**（`SYSTEM_ALERT_WINDOW`），
这是该限制的永久豁免之一：

```
设置 → 应用 → Termux → 其他权限 → 显示在其他应用上层 → 允许
```

adb 的话：`adb shell appops set com.termux SYSTEM_ALERT_WINDOW allow`

没开这个权限时，点通知会**什么也不发生**（比不设 action 更糟）。
想临时验证声音可以先 `TL_TAP=0 tlctl restart`。

## 踩过的坑（都已在脚本里处理）

### 锁相：SSE 死窗口 + 同频聊天节奏 = 每条都漏

最早只靠 SSE，`--max-time 120` 每 120 秒**主动掐断一条健康连接**，掐完还
`sleep` 退避再翻倍，backoff 一路涨到 30s 上限。于是每 ~2.5 分钟有 30 秒死窗口
（占 21%）。更蠢的是把 `rc=28`（自己的超时＝连接健康）当失败处理。

然后它和聊天节奏**锁相**了。实测数据：

```
手机侧：连 120s ┄┄┄ 断 17s ┄┄ 连 120s ┄┄┄ 断 32s ┄┄ 连 …
回复：  05:34:09 ✗   05:36:21 ✗   05:39:04 ✗        （三条全落窗口内）
```

聊天间隔 ~132s / ~163s，守护循环 152s，两个周期同频 → 每次都精准落进断线窗口。
SSE 不重放，**永久丢失**。用户看到的现象是「重启后第一条响，之后全哑」。

修法两条，缺一不可：
1. 健康断开（`rc=28` / `rc=124`）立刻重连，**不涨退避**；只有真连不上才退避
2. 加 `TL_POLL` 兜底轮询，让「漏掉」不再可能

### 高水位：单个 last_id 表征不了「都通知过了」

轮询最初写成「取**最新一条未通知过**的 assistant」，结果在有积压时乒乓循环：

```
通知 fa2aedd → last_id=fa2aedd → 下一轮 77a8c6 变「未通知」→ 通知
→ last_id=77a8c6 → fa2aedd 又变「未通知」→ ……  每 6 秒两声，刷新不停止
```

正确做法是取**最新一条 assistant** 当高水位。聊天只追加，所以它单调前进、
自然收敛。实测：积压 3 条 → 只响 1 次（最新那条）然后停。

### 两条路径并发去重

SSE 在后台子 shell、轮询在主循环，是两个进程，会同时到达。
`deliver()` 的「检查 last_id → 写 last_id」必须用 `flock` 串行化，
否则两边可能同时通过检查、各响一次。

### 冷启动不能为旧消息响铃

`last_id` 不存在时（全新安装/清过状态），先把当前最新一条**静默**记下来再开始，
否则一开机就会为重启前的旧回复响一声。而 `last_id` 已存在时若发现有没通知过的
（正是上面漏掉的），则**应该补发**——那是修复，不是误报。

### timeout 包装不能套在长连接上

统一给外部调用加 `timeout` 防挂死时，把 SSE 也包成了 `t 5 curl -sN --max-time 900`，
结果每 5 秒自杀一次（`rc=124`），日志看着像「连不上」。
外层 timeout 只是安全网，必须比内层预算大：`t $((SSE_MAXTIME + 30))`。

### 其他

- **`termux-notification-channel` 从不传 `priority`。** 它只发 `--es id` / `--es name`，
  API 侧 `priorityFromIntent()` 拿不到就 fallback 到 `IMPORTANCE_DEFAULT` —— 不弹头、
  也不保证响。必须直接调 `libexec/termux-api NotificationChannel --es priority high`。
- **渠道重要度不可变。** 改不了只能 `termux-notification-channel -d` 删了再建。
  脚本用 `$D/channel.importance` 标记文件保证只自愈一次，之后不覆盖用户在系统里做的个性化。
- **渠道名不能含空格。** Termux:API 会在第一个空格处截断（`TavernLab Reply` → `TavernLab`）。
- **`termux-notification-list` 是瞎的。** API 30+ `getActiveNotifications()` 只返回
  调用方自己的通知，而通知属于 `com.termux.api`、查询也来自它，理论可见，
  但实测返回空。**别拿它判断通知有没有发出去**，看 `$D/log` 的 `NOTIFY` 行。
- **Termux:API 广播调用偶尔不返回**，而 `notify()` 是在主循环里同步调用的 ——
  一次挂死就永久卡死，还会留下持有 `flock` fd 的孤儿。全部外部调用加 `timeout`。
- **`pkill -f` 会自杀。** 模式会匹配到执行 pkill 的那个 shell 自己（实测自杀两次）。
  改用 pidfile；必须用 `pkill -f` 时套 bracket 技巧 `pkill -f "[l]ibexec/..."`。
- **改了 env 必须 restart。** env 是进程启动时 source 进来的，`tlctl start`
  在已在跑时会 early-return。`install.sh` 末尾因此走 `restart` 而不是 `start`。

## 故障排查

**开机没自动起**
1. `pm list packages | grep com.termux.boot` 确认装了
2. ROM 设置里把 Termux / Termux:API / Termux:Boot 都加进后台/省电白名单
3. 手动跑一次 `~/.termux/boot/00-tl-notify` 看报什么

**跑着但收不到**
1. `tlctl status` 看日志最后几行
2. `curl -sN -m 5 -H "Authorization: Bearer $TOKEN" "$HOST/api/events?session=<char>"`
   手动验证链路；只回 `: connected` 就说明网络和鉴权都通
3. `log` 里没有 `NOTIFY` = 没收到 assistant 事件（网络/Doze）；
   有 `NOTIFY` 但手机没反应 = 通知被 ROM 拦

**有通知但没声**
先确认 `TL_SOUND=notification`（`chime`/`none` 模式下本来就不发通知）。
渠道声音要在系统里是开的：`设置 → 应用 → Termux:API → 通知 → TavernLabReply`。
若确认音量正常、渠道已开、仍不响，怀疑 ROM 的 AI 通知过滤又生效了，
改 `TL_SOUND=both tlctl restart` 用 chime 顶上。日志里 `NOTIFY` 行会记 `head=`。

**Doze 豁免**
`termux-wake-lock` 只在**首次**且未豁免时弹窗请求忽略电池优化。本机已豁免，
所以没弹过。如果哪天弹了，必须点「允许」。

## 还没做的

- [ ] TTS 朗读（`TL_TTS=1` 已实现，未在锁屏下验证整段长回复）
- [ ] Android Direct Reply：通知上直接回话，不用开 App
- [ ] 「已读即静音」：比对 App 拉过的最后 id，避免开 App 后又被通知
- [ ] 多角色/多楼：目前单 session
- [ ] 让 App 在前台时不发通知（需要服务端加 `/api/presence`，或接受前台也弹）
