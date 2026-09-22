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

- `order` = 放哪里，`priority` = 超预算谁先被压
  - LOCKED(100): time_anchor, system
  - HIGH(90): character, 最近4轮, distilled
  - ELASTIC(40): RAG, 旧chat
- Context Budget = MaxTokens - ResponseReserve，默认 `16384-4096`
- Audit 即核心：每次生成存 `data/audit/<id>.json`（各块tokens/Dropped/raw/reply/upstream usage），支持 Replay / Edit&Replay

## 运行

```bash
go run . --port 8080 --data ./data --upstream http://127.0.0.1:3000
ONEAPI_KEY=sk-xxx go run . --port 8080
```

打开 http://127.0.0.1:8080

## 迁移（rclone友好）

- 二进制按arch分发一次，`data/` 用 `rclone copy --copy-links` 搬家
- `data/{chats,vectra,audit,memory,presets,config.yaml}` 全是纯文件，无sqlite原生模块

## 分期

- P0 Prompt Engine：blocks→budget→raw→one-api→audit（本版）
- P1 Chat：JSONL+streaming+选模型
- P2 Memory：Distilled/Vectra读/MCP统一`MemorySource`接口
- P3 IDE：拖拽/Token可视化/Audit抽屉/Replay/移动端打磨
