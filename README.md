# Magic Browser ✨

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-%3E%3D%201.22-00ADD8?logo=go)](go.mod)

**AI 原生浏览器**：你输入的每一个网址，都由大模型实时生成整个页面。

- **域名语义路由**：输入任意 URL（如 `future-shop.magic`），模型推断网站类型并流式吐出完整 HTML/Tailwind 页面。
- **流式渲染**：页面以 `text/html` chunked 流灌进沙箱 iframe（`sandbox="allow-scripts allow-forms"`），浏览器原生渐进解析——边生成边渲染，脚本自然执行。
- **状态化连续交互**：页面内 `data-magic-nav`/`data-magic-form` 标记的深度交互（点击/表单提交）携带 State+Action 交给模型生成"下一页"；其余轻量交互（Tab 切换、展开收起）由页面内联 JS 本地完成。
- **UI 锁与自定义加载文案**：生成期间全屏遮罩阻断一切操作，文案后台可配（默认"豆包正在为你加载页面中~"）。
- **200k 上下文自动压缩**：会话累计 token 超阈值时自动摘要剪枝，保留 system + 摘要 + 上一页 + 本次操作。
- **页面历史与分享**：每个生成页面持久化到 `data/pages.jsonl`（上限 500 条，溢出丢最老）。管理后台可浏览/预览/删除，生成 `/s/{id}` 分享链接（受访客密码门保护），并可「从这里继续」——以该页为上下文种子开启新会话，LLM 无缝延续。
- **管理后台**：`/admin`——配置访客访问密码、管理员密码、模型 Endpoint/Key/名称、压缩阈值、加载文案、系统提示词。
- **极低资源占用**：纯 Go 标准库零依赖、单二进制、实测 RSS ≈ 13MB（目标 <50MB）。

## 快速开始

```bash
# 构建（无需网络：零外部依赖）
go build -o magic-browser ./cmd/magic-browser
go build -o mockllm      ./cmd/mockllm      # 可选：本地 mock 模型

# 方式一：无外网，用本地 mock 验证全链路
./mockllm -addr 127.0.0.1:8090 -pages ./testdata -chunk 200 -tick 15 &
./magic-browser -addr :8080 -data ./data
# 首次启动会打印初始管理员密码（也写在 data/initial_admin_password.txt）
# 打开 http://localhost:8080 → 右上角 ⚙ 进后台
# 后台把 API Endpoint 填 http://127.0.0.1:8090/v1，模型名随意，即可体验

# 方式二：接入真实高速模型（Groq / 任何 OpenAI 兼容 endpoint）
# 后台填写：Endpoint https://api.groq.com/openai/v1，API Key，模型名如 llama-3.3-70b-versatile
```

## 架构

```
浏览器外壳 (browser.html/js)                管理后台 (admin.html/js)
   │ 地址栏 / 遮罩 / iframe 生命周期            │ 密码登录 / 配置表单
   ▼                                           ▼
POST /api/v1/browser/navigate ──► ticket ──► GET /api/v1/page/{t}   PUT /api/v1/admin/config
   │  (session + 压缩检查)          │                    │
   │                                │ text/html chunked  ▼
   │                                │            config.Store (JSON 原子落盘)
   ▼                                ▼
session.Store ──────────── llm.Client ──► OpenAI 兼容 /chat/completions (SSE)
 (内存, TTL+容量上限)        围栏剥离/合并flush/tee
```

关键设计：

- **两段式发起**：`POST /navigate`（带 action 载荷）返回一次性 ticket（60s TTL），前端把 `GET /page/{ticket}` 设为 iframe src——POST 承载数据、GET 天然可作 iframe 源。
- **沙箱 iframe 而非 Shadow DOM**：`innerHTML` 不执行 `<script>` 且逐块重解析是 O(n²)；iframe 流式渲染交给浏览器原生解析器，`sandbox` 属性提供真隔离，导航拦截脚本 `bridge.js` 由服务端预置于流头，经 `postMessage` 与父页通信。
- **内存约束**：会话 64 个 × ≤400KB（页面 128KB/页保尾 × 6 页 + 8KB 摘要），ticket 128 个，并发生成信号量 16，`debug.SetMemoryLimit(48MB)` 软限 + 30 分钟空闲清扫。
- **级联取消**：取消按钮移除 iframe → 页面流请求 ctx 取消 → 上游 LLM 请求同步取消（无 goroutine 泄漏）。

## 约定（生成页面的深度交互协议）

| 标记 | 行为 |
|---|---|
| `data-magic-nav`（配 `data-magic-url` 或 `href`） | 点击后跳转生成下一页 |
| `<form data-magic-form action="/path">` | 提交后携带表单数据生成下一页 |
| `data-magic-local` | 纯本地表单/交互，不触发导航 |
| 普通文字链接 `<a href>` | 也视为深度跳转 |

## API 一览

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/site` | — | 访客密码 → `magic_site` cookie |
| GET | `/api/v1/config/public` | — | `{loading_text, gate_enabled, site_name}` |
| POST | `/api/v1/browser/navigate` | 访客 | `{session_id?, url, action?}` → ticket |
| GET | `/api/v1/page/{ticket}` | 访客 | 一次性 `text/html` 流 |
| POST | `/api/v1/browser/replay` | 访客 | 重放已生成页（不调 LLM） |
| POST | `/api/v1/admin/login` | — | 管理员密码 → `magic_admin` cookie |
| GET/PUT | `/api/v1/admin/config` | 管理员 | 读取（key 打码）/ 部分更新 |
| GET | `/api/v1/admin/history?limit=&offset=` | 管理员 | 页面历史列表（新→旧，含 total） |
| DELETE | `/api/v1/admin/history/{id}` | 管理员 | 删除单条历史 |
| DELETE | `/api/v1/admin/history` | 管理员 | 清空全部历史 |
| GET | `/api/v1/history/{id}/meta` | 访客 | 历史条目元数据（分享页 gate 探针） |
| POST | `/api/v1/history/{id}/resume` | 访客 | 以该页为种子新建会话 → `{session_id, url}` |
| GET | `/api/v1/snapshot/{id}` | 访客 | 页面快照流（bridge + HTML，分享 iframe 用） |
| GET | `/s/{id}` | 壳公开 | 分享页（受访客密码门保护） |

## 测试

```bash
go test -race ./...   # 全部单元测试（SSE 解析/围栏剥离/会话/配置/鉴权/路由）
```

## 安全说明

- 密码使用盐化 SHA-256 + 恒时比较（零依赖权衡）。部署到不受信网络时建议换成 bcrypt（`golang.org/x/crypto`），并置于 HTTPS 反代之后。
- 生成页面在无 `allow-same-origin` 的沙箱内运行，模型输出（不可信输入）无法触碰父页 DOM 与 cookie；请勿随意放宽 sandbox。
- `data/config.json` 权限 0600，含 API Key 与密码哈希，不要对外提供服务。发布到版本库时请确保 `data/` 下的运行时凭证不被提交（已包含于 `.gitignore`）。

## 开源协议

本项目采用 [MIT 许可证](LICENSE)。
