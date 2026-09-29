# PLAN — Reitti 接入 TavernLab 蒸馏（地理增强的 auto-diary）

> 状态：**待实施**。Reitti MCP 侧已完成（`~/app/reitti-mcp-server`，见其 `PLAN.md`）。
> 本文是 TavernLab 侧的实现计划。设计讨论与决策记录在 reitti-mcp-server/PLAN.md。
> 最后更新：2026-09-30

## 一句话目标

每次蒸馏时，除了聊天记录再抓一段最近的 Reitti 移动数据（经 reitti-mcp 解析），
让 LLM 把「我在哪/在做什么」与「我说了什么」关联，产出地理更厚实的 `distilled.md`，
并把事件自动向量化进独立 collection 供 8 回合一次的浮动召回。

## 设计原则（已与用户确认）

1. **两条地理线互补且独立开关**
   - GT20 `gps-logger` → `time_anchor.{{location}}`：低成本、始终在跑，
     「角色大概知道我在哪」，服务角色卡。**万能保底**。
   - Mate10 + Colota → Reitti → reitti-mcp：数据丰富，给蒸馏提供地理证据
     （将来可做跑步节奏等）。**实验线，可随时关**。
   - Reitti 线关掉后，蒸馏行为完全回落到今天，`{{location}}` 不受影响。
2. **窗口不写死 3 小时**：由调用方给（默认「距上次蒸馏」），MCP 侧 `max_hours` 兜底。
3. **合并进现有那一次 distill LLM 调用**，不新增第二次调用。
4. **`[USER STATE]` 卡片粒度暂不决定** → 做成 setting（`diary_store_state`，默认关）。

## 依赖的前置

- [x] `~/app/reitti-mcp-server` 完成并实测（`:8200`，工具 `get_movement_window`）
- [ ] reitti-mcp-server 部署到 m64（systemd，与 TavernLab 同机，走 `127.0.0.1:8200`）
- [ ] TavernLab 能拿到一个 reitti-mcp MCP 端点

## 关键代码位置（已勘察）

| 位置 | 作用 | 本次要动吗 |
|---|---|---|
| `main.go:2451 runDistill()` | 蒸馏主体：拼输入→LLM→ApplyDelta→Retain→存盘 | **动**（加 `[Recent Movement]` 段） |
| `main.go:2404 maybeDistill()` | 每满 8 user turn 异步触发 | 不动（节拍复用） |
| `internal/distill/distill.go DefaultPrompt` | 增量提取器提示词 | **动**（地理证据规则） |
| `main.go:2257 resolveMCP()` | main RAG：query=当前句 | 不动 |
| `main.go:2316 listInputs()` | 把同一份 hits 绑给所有 mcp block | **动**（按 collection 分流） |
| `internal/mcp/mcp.go` | 只有 `Search()` | **动**（加 `Store()`） |
| `internal/settings/settings.go` | 配置 | **动**（加 `reitti_*` / `diary_*`） |
| `internal/engine/defaults.go` | 默认 blocks | **动**（加 `diary_rag`） |

## 分阶段

### P1 — 注入 `[Recent Movement]` + 提示词改写
> **验收：停下来给用户看真实蒸馏输出再继续。**

1. 新 `internal/reitti`（MCP 客户端）
   - 调 `get_movement_window`，`since` = `meta.LastRun`（上次蒸馏时间）
   - **fail-open**：Reitti/mcp 挂了 → 蒸馏照常，只是不带地理段
   - 短超时（建议 10–15s），蒸馏本身是后台任务，不能拖
2. 新增 settings
   - `reitti_enabled`（默认 false）、`reitti_mcp_url`、`reitti_window_hours`（默认 0=自动）
3. `runDistill()`：把移动段加进 user 消息
4. **提示词改写（成败关键）**：`DefaultPrompt` 现有「只记录用户明确说或做的事」
   会让 LLM 忽略地理数据、或写噪音行。要加：
   - 移动数据是**证据/背景**，用于消歧（用户说「加班」+ 当时在 Speedmart 停 8 分钟
     → 不是通勤）；
   - **禁止**把原始轨迹写成独立 `[LOG]` 行；
   - 地理证据与用户自述冲突时**以用户自述为准**。
5. **真机跑一轮看真实输出**，再迭代措辞。

### P2 — 日记卡片向量化
1. `internal/mcp` 加 `Store()`（tools/call `store_memory`）
2. 蒸馏成功后把**新增 `[LOG]` 行**逐条存进 collection `diary_cards`，
   metadata `{date, session, kind}`
3. **去重**：记录已存 `logOrder` 键（存 `distilled.meta.json`），避免重复蒸馏灌重复卡片
4. `diary_store_state`（默认 false）控制 `[USER STATE]` 行是否也存（实验旋钮）

### P3 — 浮动召回 `diary_rag` block
1. 改 `listInputs()` 按 collection 分流（带 `engine_test.go` 回归）
2. 新增 `diary_rag` block（Order 56, L2, 独立 topK≈5/budget），collection `diary_cards`
3. **query = 最近 3 条 user 消息拼接**（不含 assistant；每条被
   `mcp_per_hit_chars=500` 截断，user+assistant 拼接必超）
4. 召回时机**挂 `runDistill` 的 8-turn 节拍**：成功顺便刷新 → 写
   `diary_recall.json`；聊天只读缓存 → 每轮零网络调用，内容持续 8 回合

### P4 — DEVLOG + 端到端真机验证

## 已知风险

- Reitti 公共 API 无 trips/交通方式，提示词不能暗示有
- Reitti 目前仅 1 天数据，窗口常空 → 必须 fail-open
- 时区已在 MCP 侧锁死（有回归测试），TavernLab 侧别再引入第二次时区处理
- `listInputs()` 改动影响现有 `rag_mcp` block，须带回归
- 每次蒸馏多 N 次 `store_memory`（本地 embedding，便宜）
- 隐私：Reitti 是高精度轨迹，进日记后会被向量化长期留存——这是明确要的实验，
  但要知道它与 GT20 保底线是两套独立数据
