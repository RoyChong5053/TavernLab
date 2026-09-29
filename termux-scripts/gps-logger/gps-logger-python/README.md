# gps-logger — GT20 地理位置反馈 (v1, Python + rich 面板)

取定位 → POST 到 m64 TavernLab → m64 调 d3180 Paikka 反查地名 → 结果进 `time_anchor`
的 `{{location}}` → 每次对话的上下文都带上事实位置。角色因此有地点感知能力。
**手机端改动，服务端零改动。**

```
GT20 Termux                    m64 TavernLab              d3180 Paikka
gps-logger-python --POST /api/location--> data/locations/*.jsonl --GET /reverse--> 地名
  (network/passive/gps -r last)      + location_latest.json      (只取 display_name+hierarchy)
                                     + time_anchor {{location}}  (stale 30min)
```

## 安装 (GT20 Termux)

```bash
cd ~/mycode/TavernLab/termux-scripts/gps-logger/gps-logger-python
pip install rich          # 可选, 不装自动降级纯文本
./install.sh --host http://192.168.100.78:8888 --token <app_token>
```

`--host` 用 m64 的 **WiFi IP**（`192.168.100.78`；有线 `192.168.10.2` 手机走不通），
`app_token` 从 m64 `data/settings.json` 取（和 notify/App 同一个）。
`--interval` 默认 600 秒。

**不写 `~/.termux/boot`，不后台自启。** 装完自己敲 `gps-logger-python` 跑，Ctrl-C 退出。
`install.sh` 会顺手清掉 v0 的旧 bash 守护进程和 boot 残留。

## 用法

```bash
gps-logger-python                    # 前台面板, Ctrl-C 退出
gps-logger-python --once             # 打一个点就退出 (可放 cron)
gps-logger-python --interval 300     # 覆盖间隔
gps-logger-python --plain            # 强制纯文本, 不用 rich
gps-logger-python --no-post          # 只本地看, 不发 m64 (调试)
```

面板里：`空格` 立即取点，`q` 退出。

## 为什么定位必须用 `-r last`

这是本版最关键的设计，**不要改回默认的 `once`**。

`LocationAPI` 的 `once` 请求在服务端 `latch.await(30, TimeUnit.SECONDS)` 硬等 30 秒
（`LocationAPI.java:181`）。v0 的 bash 脚本给 network 的 timeout 只有 15s，于是每次都在
API 还在等的时候被 SIGKILL → 服务端 30 秒后写回已死的 socket →
`java.io.IOException: Connection refused` → `ResultReturner` 弹一条「Termux:API Error」。

一天几十条弹窗就是这么来的，**不是「定位不到」**。实测一天 87 次 SKIP 中 26 次是 `g0`
（gps 超时被 kill）+ 57 次 `g-skip`，没有一次是真定位失败。

`-r last` 读缓存瞬回（实测 1.05–3.2s），永远不进那个 latch，也就永远不会被杀。
把 timeout 调大是治标——被 kill 的请求还会继续占住 Termux:API 队列最多 30 秒，
把后续调用一起拖死（实测 `gps -r last` 从 1.0s 劣化到 25s 超时）。

**代价**：拿到的可能是几十分钟前的缓存点位。所以面板解析 `elapsedMs` 把**点龄**如实标出：

| 点龄 | 显示 |
|---|---|
| < 5min | 绿色 ● 实时 |
| 5–30min | 黄色 ● N 分钟前 |
| > 30min | 红色 ● N 小时前（缓存） |

实测这台机器 network 缓存已 39 分钟旧、gps 更旧 4.8 小时。**不要假装这是实时定位。**

## 面板读什么

| 区块 | 内容 |
|---|---|
| 头 | 连接状态、m64 地址、当前间隔（静止时会自动拉长）、运行时长、轮次 |
| 定位 | provider、坐标、精度、**点龄**、距上一点的位移 |
| 发送 | 上次 HTTP 码与耗时、**服务端反查出的地点**、成功/失败数、补发数 |
| 轮次 | 最近 8 轮的时间/来源/精度/点龄/HTTP/耗时 |
| 底栏 | 成功·失败·跳过 计数、到下一轮的倒计时进度条 |

## 自适应间隔

用 haversine 距离（公式与服务端 `location.go:254 haversineKm` 一致）判断移动：

- 位移 < 100m → 间隔拉到 1200s
- 位移 > 100m → 回到 600s

**静止期照常采样**——服务端 `locationDwell` 的「静止停留约N分钟」推断和 `stale` 判定
都依赖连续点位，断了这两项就废。

## Termux:API 自愈

连续 3 次失败后，自动 `termux-api-stop` + `termux-api-start` 重启
`com.termux.api/.KeepAliveService`，再继续重试。借鉴自 BuriXon-code 的 Termux-GPS-Tracker。

只在真失败时触发——正常路径绝不碰，因为重启会让本来 1 秒能好的调用变成几秒。

## 配置

`~/.config/gps-logger/env`（chmod 600，勿提交）：

```
HOST=http://192.168.100.78:8888
TOKEN=<app_token>
GL_INTERVAL=600
```

状态目录 `$PREFIX/var/lib/gps-logger/`：`log`（运行日志）、`last.json`（未发出去待补发的点）。

## 验证

1. `gps-logger-python --once --no-post` → 面板正常，**无 Termux:API 弹窗**
2. `gps-logger-python --once` → m64 `curl $HOST/api/location` 见 `received_at` 更新
3. `curl $HOST/api/logs` 见 `location ingest` + `location geocode`
4. 聊一句，audit 的 `time_anchor` 应带 `[LOC PJU 8, Petaling Jaya · 静止停留约N分钟]`
5. 手机离线超 30min，服务端自动标 `stale`
6. **关键回归**：跑满 3 轮后
   `logcat -d | grep -c "ResultReturner: java.io.IOException"` 必须为 **0**

## 排障

**还有 Termux:API 弹窗？**
Android 设置 → 应用 → Termux:API → 通知 → 全关。系统级根治，比脚本 hack 可靠。

**一直显示「缓存」/ 红色点龄？**
`-r last` 拿的是系统缓存，Android 自己没更新就不会变新。这是预期行为，不是 bug。
想要更新的点位就得用 `-r once`，代价是重新引入上面那个 30s 阻塞问题。

**连不上 m64？**
手机走 WiFi，`192.168.100.78` 是 m64 的 WiFi IP。有线口 `192.168.10.2` 手机不可达。

## 相关

- 旧版（v0 bash 守护进程 + termux-boot 自启）归档在 `../termux-boot-gps-logger/`
- 设计细节与实机测量数据见 [PLAN.md](PLAN.md)
- 服务端实现：`main.go:1084`（`/api/location`）、`location.go`（geocode + dwell + 宏渲染）
