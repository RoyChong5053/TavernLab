# TavernLab · leer-chat

Prompt IDE + 薄Chat UI，替代 SillyTavern 臃肿部分。Plan v2 定案。

## 架构

```
[浏览器: 静态JS, llama.cpp风格玻璃UI, 无build]
  ↓ HTTP/SSE
[Go thin core: Prompt Engine + Context Budget + Chat Store + Audit + Memory Router]
  ├─ Vectra local (兜底, 无AVX, 随data/走, P0只读)
  └─ MCP client → rag_mcp_server/Qdrant (高端, LOQ)
[One-API: model/embed/rerank fan-out]
```

## 核心抽象

- `order` = 放哪里；`Level` = 超预算时谁先被淘汰
  - L1 locked: time_anchor, system, character, distilled（永不裁剪）
  - L2 trim: RAG 命中、chat（按 rerank 分数 / 轮龄淘汰，保底 4 轮）
  - L3 elastic: 实验性 block（空间不够整个先砍）
- Context Budget = `context_window − reply_reserve`，默认 `16384 − 4096 = 12288`（reply_reserve 同时作为上游 max_tokens）
- Audit 即核心：每次生成存 `data/audit/<id>.json`（各块tokens/Dropped/raw/reply/upstream usage），支持 Replay / Edit&Replay

## 运行

```bash
go run . --port 8080 --data ./data --upstream http://127.0.0.1:3000
ONEAPI_KEY=sk-xxx go run . --port 8080
```

打开 http://127.0.0.1:8080

## 登录鉴权（可选，服务端设定）

与 rag-mcp-server 同一套 one-api 风格门禁。默认关闭（`admin_user` 为空）。

```bash
# 密码 hash（hex(sha256(password))）
HASH=$(printf '%s' '你的密码' | sha256sum | awk '{print $1}')
# 方式一：环境变量（推荐给 systemd）
TAVERNLAB_ADMIN_USER=admin TAVERNLAB_ADMIN_SHA256=$HASH \
TAVERNLAB_APP_TOKEN=<手机App用的长随机串> ./tavernlab --port 8888 --data ./data
```

- 凭据只从服务端读：flags/env 优先，其次 `data/settings.json` 的 `admin_user`/`admin_password_sha256`/`session_days`/`app_token`（0600）。WebUI **无法**设置或读取密码/app token。
- 浏览器端：登录一次后 token 存 sessionStorage；勾「记住我」存 localStorage（默认 30 天），不会反复弹框。EventSource / `<img>` 走 `?token=` 回退。
- 手机 App：在设置里把 host headers 设为 `{"Authorization":"Bearer <app-token>"}`，此后静默鉴权（App 无登录 UI）。
- 密码或 app token 变更（或重启时 `sessions.json` 指纹不匹配）会令所有旧 token 失效，需重新登录。
- 放行清单：`/api/health`、`/api/login`、`/api/logout`、`/api/me` 与静态前端；其余 `/api/*`、`/v1/*`、`/chars/*` 需鉴权。

## 迁移（rclone友好）

- 二进制按arch分发一次，`data/` 用 `rclone copy --copy-links` 搬家
- `data/{chats,vectra,audit,memory,presets,config.yaml}` 全是纯文件，无sqlite原生模块

## 分期

- P0 Prompt Engine：blocks→budget→raw→one-api→audit（本版）
- P1 Chat：JSONL+streaming+选模型
- P2 Memory：Distilled/Vectra读/MCP统一`MemorySource`接口
- P3 IDE：拖拽/Token可视化/Audit抽屉/Replay/移动端打磨
