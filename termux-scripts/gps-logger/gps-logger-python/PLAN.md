# gps-logger-python — GT20 → m64 TavernLab 位置反馈 (v1, Python + rich TUI)

最小成本的位置链路：GT20 取定位 → POST 到 m64 TavernLab → m64 调 d3180 Paikka
反查地名 → 结果进 `time_anchor` 的 `{{location}}` → 每次对话的上下文都带上事实位置。
角色因此有地点感知能力。**手机端改动，服务端零改动。**

```
GT20 Termux                    m64 TavernLab              d3180 Paikka
gps-logger-python --POST /api/location--> data/locations/*.jsonl --GET /reverse--> 地名
  (network/passive/gps -r last)      + location_latest.json      (display_name+hierarchy)
                                     + time_anchor {{location}}  (stale 30min)
```

---

## 一、要解决的问题 (已实机定位)

**Termux:API 报错弹窗不是「定位不到」，是脚本把 API 杀在飞行中。**

`LocationAPI` 的 `once` 请求在服务端 `latch.await(30, TimeUnit.SECONDS)` 硬等 30 秒
（`LocationAPI.java:181`）。旧脚本给 network 的 timeout 只有 15s、passive 10s、gps 20s、
battery 5s。API 还在等，脚本先 SIGKILL 客户端 → 服务端 30 秒后写回已死 socket →
`java.io.IOException: Connection refused` → `ResultReturner` 调
`sendPluginCommandErrorNotification` 弹一条「Termux:API Error」。

logcat 实证，与 `SKIP` 时刻一一对应：

```
09-30 02:34:47  E TermuxAPI.ResultReturner: java.io.IOException: Connection refused  <- LocationAPI
09-30 02:34:48  E TermuxAPI.ResultReturner: java.io.IOException: Connection refused  <- BatteryStatusAPI
```

一天 87 次 SKIP 中 26 次 `g0`（gps 超时被 kill）+ 57 次 `g-skip`，**没有一次是真定位失败**。

**关键测量 (GT20, Android 15 / SDK 35)**

| 调用 | 耗时 | 说明 |
|---|---|---|
| `termux-location -p gps` (旧写法) | 30s 阻塞 → 15s 被杀 | 弹窗来源 |
| `termux-location -p <p> -r last` | **1.05–3.2s** | 读缓存，瞬回 |
| `termux-battery-status` | ~1.1s | 旧脚本 timeout 5 偏紧 |
| 连跑 3 轮 `-r last` | ~1.1s | logcat ResultReturner 错误数 **0** |

Android 15 上 `termux-api.c` 只在 `api_level < 34` 走快 listen socket，≥34 一律
`exec am broadcast`（实测 ~1.0s），所以每次调用 ~1s 底噪是正常的，不是故障。

**连锁效应**：被 kill 的请求不会立刻消失，会继续占住 Termux:API 队列最多 30 秒，把后续
调用一起拖死 (实测 `gps -r last` 从 1.0s 劣化到 25s 超时)。改用纯 `-r last` 后没有请求会
被 kill，也就没有残留——这才是根治，不是把 timeout 调大。

## 二、为什么重写成 Python

要用 `rich` 就得 Python。更重要的是 bash 版每轮要 spawn 5–6 个子进程
(`timeout`→`termux-location`→`am`→`jq`×3)，Python 单进程直接 `subprocess.run`，
省掉一层 shell 和 `jq` 依赖。

## 三、核心逻辑

**定位** — `once` 换 `last`，这是根治点。`-r last` 读缓存瞬回，永远不进 30s latch，
永远不会被 kill：

```
network -r last (timeout 25s) → passive -r last (timeout 15s) → gps -r last (timeout 25s)
```

**点龄** — 解析 `elapsedMs` 存为点龄。实测 network 缓存已 39 分钟旧、gps 更旧 4.8 小时。
**这是 `-r last` 的固有代价，必须如实显示，不能假装实时。**

| 点龄 | 显示 |
|---|---|
| < 5min | 绿色 ● 实时 |
| 5–30min | 黄色 ● N 分钟前 |
| > 30min | 红色 ● N 小时前（缓存）|

**上报** — 严格对齐服务端契约 (`location.go:20-29`)：

```
POST {HOST}/api/location
Authorization: Bearer {TOKEN}
{"lat":..,"lon":..,"acc":..,"provider":..,"tst":..,"batt":..}
-> 200 {"ok":true,"place":"PJU 8","hierarchy":["Malaysia",...]}
```

`place`/`hierarchy` 直接显示在面板上。`received_at` 由服务端填，不发。
`/api/location` 在 authGate 里 (gated，非 open)，必须带 Bearer token。

**失败重试** — 保留 `last.json` 单独重试；连续失败 ≥3 次触发
`termux-api-stop` + `termux-api-start` 自愈 (已验证本机存在且可用：
`am startservice -n com.termux.api/.KeepAliveService`)，**仅失败时触发**，
正常路径绝不碰。

**自适应间隔** — 用 haversine 距离 (公式与服务端 `location.go:254 haversineKm` 一致)：
- 位移 < 100m → 间隔拉到 1200s
- 位移 > 100m → 回到 600s
- **静止期照常采样** (dwell 推断和 stale 判定依赖连续点位，不能断)

## 四、TUI 布局

`rich.Live` + `Panel`，宽屏 (≥76 列) 左右分栏、窄屏自动上下堆叠。
无 rich 时降级为 `clear` + 纯文本帧，不装也能跑。

```
╭─ GPS Logger ───────── 运行 12:34 · 第 7 轮 · 07:10:22 ─╮
│ ● 已连接   m64 192.168.100.78:8888   间隔 600s        │
╰──────────────────────────────────────────────────────╯
╭─ 定位 ──────────────────────╮ ╭─ 发送 ────────────────╮
│ provider  network           │ │ 上次   200 OK  24ms   │
│ 坐标  3.1769262,101.6171278  │ │ 地点   PJU 8, Petaling│
│ 精度  ±61 m                 │ │ 重试   0              │
│ 点龄  ● 39 分钟前（缓存）    │ │ 距上点 1.4 m           │
╰─────────────────────────────╯ ╰───────────────────────╯
╭─ 轮次 ──────────────────────────────────────────────────╮
│ 07:39 ● OK    network ±61m 39m前  200  24ms           │
│ 07:29 ● OK    network ±11m 38m前  200  19ms           │
│ 07:19 ● SKIP  n0/p0/g-skip    —     —   —             │
╰────────────────────────────────────────────────────────╯
 成功 5 · 失败 0 · 跳过 2 · 下轮 07:49 (4:12) ████████░░░
 [q]退出  [空格]立即取点
```

## 五、命令

```bash
gps-logger-python                    # 前台 TUI，Ctrl-C 退出
gps-logger-python --once             # 打一个点就退出 (可进 cron)
gps-logger-python --interval 300
gps-logger-python --plain            # 强制纯文本，不依赖 rich
gps-logger-python --no-post          # 只本地看，不发 m64 (调试)
```

## 六、部署

```bash
# 1. 停掉旧 daemon + 删 boot 自启 (旧版已归档到 termux-boot-gps-logger/)
gps-ctl stop
rm ~/.termux/boot/10-gps-logger
rm ~/bin/gps-logger.sh ~/bin/gps-ctl

# 2. 装
pip install rich
cd ~/mycode/TavernLab/termux-scripts/gps-logger/gps-logger-python
./install.sh

# 3. 跑
gps-logger-python
```

**注意**：纯手动 = 只在你开着 Termux 时上报。关掉 Termux 就不再更新，服务端 30 分钟后把
`{{location}}` 标成「已过期」。这是纯手动的必然代价，不是 bug。

**建议**：Android 设置 → 应用 → Termux:API → 通知，全关。系统级根治，比脚本 hack 可靠
——将来别的脚本再触发也不会弹。

## 七、与整体架构的关系

**当前链路 (本次交付范围)**

```
GT20 gps-logger-python
  └─ POST /api/location ──> m64 TavernLab
                            ├─ data/locations/YYYY-MM-DD.jsonl
                            ├─ data/location_latest.json
                            └─ GET d3180 Paikka /reverse  <- 拿地名
                                 └─ time_anchor {{location}} -> 对话上下文
```

两跳已验证：m64 `:8888` health 200、d3180 Paikka `:8081` reverse 200 (0.97s)。

**未来演进 (本次不做，仅记录)**

已部署 Reitti (m64 `:8080`，docker `reitti-reitti-1` + postgis + redis)，Mate10 用 Colota
持续喂数据。届时：
- 复杂移动语义 (停留点、行程、交通方式) 由 Reitti 数据库解析
- TavernLab 不再自己算 dwell，改从 Reitti 读
- 可考虑给 TavernLab 加个 Reitti MCP，让 LLM 主动查询而非被动注入

建议届时**保持 `{{location}}` 简单事实注入** (低延迟、零依赖)，把复杂查询放 MCP 按需触发
——避免每轮对话都等 Reitti。

## 八、验证

1. `gps-logger-python --once --no-post` → 面板正常，无 API 弹窗
2. `--once` → `curl $HOST/api/location` 见 `received_at` 更新
3. `curl $HOST/api/logs` 见 `location ingest` + `location geocode`
4. 聊一句，audit 里 `time_anchor` 带 `[LOC PJU 8, Petaling Jaya · 静止停留约N分钟]`
5. 手机离线超 30min，自动标 `stale`
6. **关键回归**：跑满 3 轮后 `logcat -d | grep -c "ResultReturner: java.io.IOException"`
   必须为 **0**
