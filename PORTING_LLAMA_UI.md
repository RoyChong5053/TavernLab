# llama.cpp UI 移植表（只读调研 2026-09-22）

llama.cpp 新版 UI 位置：`/home/roychong/app/llama.cpp/tools/ui/src/`（SvelteKit）
构建产物：`/home/roychong/app/llama.cpp/build_CUDA/tools/ui/dist/`
运行中 11436/11437 是 embedding/reranker 专用 server，无 UI（`--embeddings/--reranking` 模式不 serve 聊天页）。

## 结论：借 UX，不拷结构

llama.cpp UI 是 SvelteKit + npm 重型工程（`package.json: llama-ui, vite build, playwright, storybook`），
整套搬进来就违背 `rclone免编译` 初衷。所以 TavernLab 前端用 vanilla 单文件重写，只借鉴：

| llama.cpp 有什么 | TavernLab 怎么借 | 状态 |
|---|---|---|
| `routes/(chat)/` 聊天页 + `ChatScreen/ChatMessages/ChatForm/ChatTabs` | `web/index.html` 三栏 Blocks\|Chat\|Audit | ✅ 重写完成 |
| `services/chat.service.ts` SSE收流 | `web/app.js` stream reader + `internal/proxy` 中继 | ✅ 已实现 |
| `stores/conversations/device/ui` 玻璃+移动端抽屉+PWA splash | `web/style.css` glass + `@media(max-width:900px)` bottom bar | ✅ 已实现简化版 |
| `services/mcp.service.ts + components/mcp` | P2 `MemorySource.MCPSource` 再接 | ⏳ P2 |
| `services/models.service.ts` 模型选择 | `model` 输入框先顶着，P1再做KScope式选择器 | ⏳ P1 |
| `database/migration.service.ts` IndexedDB | 不用，坚持文件 `data/` 可rclone | ❌ 不抄 |

## 不抄清单（防止 scope creep）

多用户/权限/主题商城/插件市场/agentic tools/sandbox/mcp marketplace，全部不进 TavernLab。
