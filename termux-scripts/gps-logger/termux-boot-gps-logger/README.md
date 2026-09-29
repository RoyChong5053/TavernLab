# gps-logger — GT20 地理位置反馈 (v0 极简版)

每 10 分钟取一次定位，直 POST 到 m64 TavernLab，服务端落盘 + 调 d3180 Paikka 反查，`time_anchor` 自动带 `[LOC …]`。

```
GT20 Termux                    m64 TavernLab              d3180 Paikka
gps-logger.sh --POST /api/location--> data/locations/*.jsonl --GET /reverse--> 地名
  (gps->network 兜底)            + location_latest.json      (只取 display_name+hierarchy)
                                 + obs 日志 ingest+geocode
                                 + time_anchor {{location}} (stale 30min)
```

## 安装 (GT20 Termux 里)

```bash
./install.sh --host http://192.168.100.78:8888 --token <app_token>
```

`--host` 用 m64 WiFi IP，`app_token` 从 m64 `data/settings.json` 取（和 notify/App 同一个）。
`--interval` 默认 600（秒），改完重跑 install 或改 `~/.config/gps-logger/env` + `gps-ctl restart`。

## 验证

1. `gps-ctl status` 看 `OK lat=…` 行。
2. m64 `curl $HOST/api/location` 见 latest；`/api/logs` 见 `location ingest` + `location geocode`。
3. 聊天看 audit 的 `time_anchor` 带 `[LOC …]`；手机离线超 30min 自动标 `stale`。

## 还没做的 (v1)

- sqlite 排队/批量补传（v0 只有 last.json 单点重试）
- 移动加速采样（静止 10min / 移动 1-2min）
- Reitti 双写（Colota 那条线另跑，数据齐了再对接）
