# Magic Browser 系统设计与可行性论证文档 (v1.0)

---

## 1. 项目概述与目标功能

### 1.1 项目简介

**Magic Browser** 是一款基于 **AI-Native Dynamic Web** 概念打造的探索型 Web 浏览器/应用平台。不同于传统浏览器去请求已构建好的静态或服务端渲染页面，Magic Browser 在用户输入任意网址或触发页面交互时，通过底层的超高吞吐量大模型（生成速度需达到 **900 token/s** 级别），**即时（Real-time）纯生成 HTML/CSS/JS 页面**。

### 1.2 目标功能矩阵

* **域名语义识别与生成 (Semantic Routing)：** 用户输入任意 URL（如 `[http://future-shop.magic](http://future-shop.magic)`），系统解析域名语义并由 AI 实时吐出对应的全套 HTML/Tailwind 页面。
* **双轨流式渲染 (Streaming Render & Shadow DOM)：** 依托 SSE（Server-Sent Events）实现逐 Token 流式吐包，配合前端 Shadow DOM 实现流式渲染与样式/脚本沙箱隔离。
* **状态化连续交互 (Stateful Transition)：** 截获页面内的深度点击/表单提交，提取操作 Action 与 State 上下文，交给 LLM 动态吐出“下一个页面”，实现完整的 Web 状态机转移。
* **运行态 UI 锁与自定义加载遮罩 (UI Lock & Custom Loading)：** 生成过程中自动开启全局遮罩阻断用户操作，展示动画及后台自定义文案（如：“豆包正在为你加载页面中~”）。
* **200k 上下文智能压缩机制 (Context Pruning)：** 当连续交互积累的 Context 达到 200k Tokens 阈值时，自动提取状态摘要（Summary）并剪枝冗余 HTML 标记。
* **轻量级管理后台 (Admin Panel)：** 提供基于鉴权的后台，支持配置访问密码、大模型 Endpoint/API Key/模型名称、上下文压缩阈值以及前端加载文案。
* **极低资源占用 (Low Footprint)：** 单执行文件部署，静态运行内存控制在 **50MB** 以内。

---

## 2. 可行性评估与论证

### 2.1 评估结论

| 评估维度 | 可行性等级 | 结论说明 |
| --- | --- | --- |
| **生成速度与首帧延迟** | **高可行** | 在 900 token/s 下，一个典型页面（1500~2000 token）生成仅需 1.6~2.2 秒。配合流式渲染，首屏视觉呈现可在 **500ms 内完成**。 |
| **交互逻辑流转** | **中高可行** | 把用户的每次点击/提交抽象为 `State + Action` 传回 LLM 重新生成下一个页面，可实现流畅的状态机迁移。 |
| **前端交互完整度** | **中可行** | 依赖 Tailwind CSS（CDN 引入）和 Alpine.js/Vanilla JS，LLM 可在单文件 HTML 中直接嵌入基础样式与微交互。 |
| **系统稳定性与一致性** | **瓶颈所在** | 频繁跨页面交互容易产生上下文漂移，需严格依赖上下文压缩与 Style Schema 系统提示词约束。 |

### 2.2 关键技术链路

系统本质上是将传统的 **服务端渲染（SSR）** 升级为 **LLM 实时生成（AI-SSR）**：

1. **访问链路：** 用户输入 URL $\rightarrow$ 后端拦截解析语义 $\rightarrow$ 注入全局依赖（Tailwind CDN/系统提示词） $\rightarrow$ 900 token/s LLM 流式输出 $\rightarrow$ Shadow DOM 拦截渲染。
2. **双轨交互机制：**
* **轻量交互（本地完成）：** 展开菜单、切换 Tab、弹窗等，由 LLM 生成时预置的微量 JS（Alpine.js / Vanilla JS）在客户端直接响应。
* **深度交互（触发下一页）：** 提交表单、点击链接时，截获 Click Event，提取当前 `State JSON`，连同 `Action` 发送给 Backend 重新生成 HTML。



---

## 3. 总体架构设计

### 3.1 技术选型

为了实现**极低运行态内存占用**（目标：单进程静态内存 `< 50MB`），后端放弃重型 Python/Node.js 运行时，采用编译型语言 + 嵌入式 KV 数据库构建。

```
+-----------------------------------------------------------------------+
|                             Magic Browser                             |
+------------------------------------+----------------------------------+
|               前端                  |               后端               |
|  - Vanilla JS / Tailwind CSS CDN   |  - Go (Gin/Fiber) 或 Rust        |
|  - HTML5 EventSource (SSE)         |  - SQLite / BoltDB (内嵌数据库)   |
|  - Shadow DOM (隔离生成页面)         |  - Zero-Copy SSE Proxy Handler   |
+------------------------------------+----------------------------------+

```

* **后端服务：** **Go** 或 **Rust**。编译为单一二进制文件，无外部依赖，运行态内存占用极低。
* **持久化层：** **BoltDB** 或 **SQLite**。本地文件型数据库，无单独数据库服务进程，零额外内存开销。
* **前端展示：** 原生 JavaScript + Shadow DOM，彻底隔离生成页面的 CSS/JS 样式与全局污染。

---

## 4. 核心功能设计与实现方案

### 4.1 低内存运行态设计

1. **零拷贝流式透传 (Zero-Copy Streaming Proxy)：** 后端收到 LLM API 的 SSE (Server-Sent Events) 数据流后，直接通过管道（Pipe）透传给前端，**不在后端内存中缓存**完整生成的 HTML。
2. **轻量配置加载：** 系统配置（密码、模型参数、提示词、文案）存入嵌入式数据库，启动时仅加载少量必要 Key-Value 至内存。

### 4.2 状态机与交互遮罩 (UI Lock & Loading Mask)

为满足“生成过程中不允许操作并显示自定义加载动画”的要求，前端维护轻量状态机：

* **状态迁移：** `IDLE (空闲)` $\rightarrow$ `GENERATING (生成中)` $\rightarrow$ `IDLE (空闲)`
* **UI 遮罩实现：**
* 用户发起访问或点击深度交互后，前端将全局蒙层 `div#loading-mask` 设置为 `display: flex`，阻断所有底层 pointer/click 事件。
* 蒙层展示 CSS 居中加载动画，并渲染从后台配置读取的**动态加载文案**（如：“豆包正在为你加载页面中~”）。
* 待 SSE 传输完成（收到 `[DONE]` 标识或连接关闭）且 DOM 渲染完毕后，解除蒙层。



### 4.3 200k 上下文自动压缩机制

当用户在同一个“生成会话”内连续点击交互时，系统需维护历史上下文：

1. **Token 估算：** 在后端准备发给 LLM 前，估算当前 `Session` 累计的历史 HTML 与 User Actions 的 Token 数（按 $1 \text{ char} \approx 0.35 \text{ token}$ 快速估算）。
2. **触发条件：** 累计 Context 超过 **200,000 Tokens** 时。
3. **压缩策略：**
* **异步 Summarize：** 提取历史页面中的关键 DOM 结构与用户已完成的操作路径，合成一段大约 1k Token 的 `State Summary`。
* **上下文剪枝：** 丢弃中间生成的冗余 HTML 标记，仅保留：`System Prompt` + `Style Schema` + `State Summary` + `Last 1 Page HTML` + `Current Action`。



---

## 5. 管理后台与配置设计

后台提供轻量级 Web 管理界面，基于 **Token / Cookie** 实现简单认证。

### 5.1 数据表配置项 (Config Schema)

| 配置键 (Key) | 数据类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `admin_password` | String | (初始化时设置) | 后台登录密码（bcrypt 加密存储） |
| `llm_provider_url` | String | `[https://api.openai.com/v1](https://api.openai.com/v1)` | 大模型 API Endpoint (支持 Groq/OpenAI 等) |
| `llm_api_key` | String | - | API Key |
| `llm_model_name` | String | `llama-3.3-70b-versatile` | 绑定的高吞吐模型名称 |
| `context_compress_threshold` | Int | `200000` | 触发上下文压缩的 Token 阈值 |
| `loading_text` | String | `豆包正在为你加载页面中~` | **前端加载动画自定义文案** |
| `system_prompt` | Text | (预置Prompt) | 生成 HTML 的全局系统提示词 |

---

## 6. 关键 API 接口与前端交互逻辑

### 6.1 核心 API 规范

* **`POST /api/v1/browser/navigate`** (页面生成/交互接口)
* **请求体：**
```json
{
  "session_id": "sess_12345",
  "url": "http://example.magic/shop",
  "action": { "type": "click", "element_id": "btn-buy" }
}

```


* **响应：** `text/event-stream` (SSE 数据流)


* **`POST /api/v1/admin/login`** - 后台登录获取 Token
* **`GET /api/v1/admin/config`** - 获取系统当前配置
* **`PUT /api/v1/admin/config`** - 更新系统配置

### 6.2 前端核心渲染与 UI 锁逻辑 (伪代码)

```javascript
// 前端 SSE 接收与 UI 锁/加载遮罩逻辑
async function navigateTo(url, action = null) {
  const mask = document.getElementById('loading-mask');
  const loadingTextEl = document.getElementById('loading-text');
  const shadowRoot = document.getElementById('browser-viewport').shadowRoot;
  
  // 1. 获取后台设置的自定义文案并锁定 UI
  loadingTextEl.innerText = window.SYS_CONFIG.loading_text || "页面加载中...";
  mask.classList.remove('hidden'); // 显示遮罩，阻断用户操作

  try {
    // 2. 发起 SSE 请求
    const response = await fetch('/api/v1/browser/navigate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url, action, session_id: currentSessionId })
    });

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let generatedHTML = '';

    // 3. 流式读取数据并灌入 Shadow DOM
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      
      const chunk = decoder.decode(value);
      generatedHTML += parseSSEChunk(chunk);
      
      // 实时更新 Shadow DOM 视图
      shadowRoot.innerHTML = generatedHTML; 
    }
  } catch (err) {
    console.error("Page generation failed:", err);
  } finally {
    // 4. 生成完毕，解锁 UI 遮罩
    mask.classList.add('hidden');
  }
}

```