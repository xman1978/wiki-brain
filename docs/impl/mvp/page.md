# Wiki-Brain MVP 测试页面设计

本文档定义 MVP 阶段的测试控制台页面（`web/index.html`）。页面目标是验证 Wiki-Brain MVP 的核心闭环：文件导入 → 知识提取 → 检索与回答 → 质量追踪 → 学习报告。

MVP 阶段不实现：

```text
正式领域 / 概念管理界面；
ActivationLink 状态机（只展示 link_candidates 候选统计）；
知识图谱浏览和激活路径可视化；
知识检查器（ActivationLink 详情抽屉）；
外部证据候选；
冲突检测、认知缺口；
候选自动晋升；
多人协作与用户权限。
```

---

## 1. 产品定位

测试控制台命名为：

```text
知识大脑控制台
```

它首先是面向开发验证的问答测试台，其次也可用作功能演示。

核心体验：

```text
中间对话式问答；
右侧证据与解释；
文件导入通过右侧抽屉完成；
学习报告通过独立视图展示。
```

页面采用双层设计：

```text
默认模式：展示回答、引用来源、检索质量、知识缺口；
调试模式：展示 trace 详情、共现统计、学习事件、原始响应。
```

展示分层：

```text
一级：回答正文、引用来源数量、检索质量（置信 / 部分 / 缺口）；
二级：证据列表（direct / supporting）、当次缺口说明；
三级（调试模式）：trace 详情、共现统计、学习事件、原始 JSON。
```

---

## 2. 整体布局

问答区全宽布局，历史记录和证据解释通过抽屉展示：

```text
┌─────────────────────────────────────────────────────────────────┐
│  知识大脑  ☰ 历史     健康  文件  学习报告  📖 解释  🔬调试  设置  │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│                         问答区（全宽）                            │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
         ↑                                              ↑
   历史抽屉（左滑入）                              解释抽屉（右滑入）
```

两个抽屉均为 `position:fixed` 叠加在内容之上：

```text
历史抽屉：从左侧滑入，宽 280px，z-index 90
解释抽屉：从右侧滑入，宽 320px，z-index 90
文件抽屉：从右侧滑入，宽 600px，z-index 101（优先级更高）
```

历史抽屉与解释抽屉互斥：打开其中一个会自动关闭另一个，共享同一层半透明遮罩（z-index 89），点击遮罩可关闭当前抽屉。

---

## 3. 顶部导航

```text
左侧：品牌标识"知识大脑" · 历史抽屉切换按钮（☰ 历史）
右侧：健康状态圆点 · 文件 · 学习报告 · 解释抽屉切换按钮（📖 解释）· 调试模式开关 · 设置
```

不包含全局搜索输入框（MVP 阶段仅通过底部输入框提问）。

抽屉切换按钮：

```text
☰ 历史    →  位于品牌标识右侧，切换问答历史抽屉（从左滑入）
📖 解释   →  位于导航右侧，切换证据与解释抽屉（从右滑入）
```

两个按钮均有激活状态（蓝色边框 + 浅蓝背景），对应抽屉打开时高亮显示。

设置弹窗仅包含：

```text
服务器地址（默认 http://localhost:8080）
```

服务健康状态（调用 `GET /health`）在导航栏右侧以小圆点展示，每 30 秒自动轮询。

---

## 4. 会话历史抽屉

从左侧滑入，宽 280px，点击导航栏"☰ 历史"按钮或关闭按钮（✕）切换显示。

会话列表由后端持久化，调用 `GET /sessions` 加载，刷新后不丢失。

```text
＋ 新对话

今天
────────────────────────────────
▶ 分析 retrieval.md 设计缺陷       （当前，高亮）
  刚才

  关于 KPN 关系生成的问题
  昨天

昨天
────────────────────────────────
  锁的种类有哪些？
  2 天前
```

### 4.1 会话列表

启动时调用 `GET /sessions`（按 `updated_at` 倒序），渲染会话列表。

每条会话显示：title（第一轮输入前 30 字）、最后活跃时间（相对时间，如"刚才"/"昨天"）、当前会话高亮。

### 4.2 新建会话

点击"＋ 新对话"：

```text
POST /sessions → 获得 session_id
清空对话区
切换到新 session_id（写入前端变量，后续请求携带）
会话列表顶部插入新条目
```

### 4.3 切换会话

点击历史条目：

```text
GET /sessions/:id/turns → 获取轮次列表
渲染历史对话记录：
  action=retrieve 且 answer_id 非空 → GET /answers/:id 加载回答内容
  action=clarify → 展示澄清问题文本
  action=interrupted → 展示"对话已重置"提示
切换前端当前 session_id
```

切换后用户可继续在该会话中提问，SessionState 从数据库自动恢复（服务端 SessionStore 内存 miss 时从 `state_snapshot` 反序列化）。

### 4.4 删除会话

每条会话右侧显示删除按钮（hover 时出现）：

```text
DELETE /sessions/:id
从列表中移除该条目
若删除的是当前会话 → 自动切换到列表第一条，若列表为空则新建会话
```

### 4.5 当前会话轮次展示

切换到某会话后，对话区按 `turn_index` 升序展示所有轮次：

```text
每轮：
  用户气泡：user_input
  系统响应：
    action=retrieve → 展示回答卡片（异步加载 GET /answers/:id）
    action=clarify  → 展示澄清选项卡（options 按钮列表）
    action=interrupted → 展示"对话已重置"灰色提示
```

Trace 异步生成后，`answer_id` 对应的 trace_id 通过 `GET /traces?answer_id=:id` 补查并更新回答卡片。

---

## 5. 中间问答区

### 5.1 问题提交

用户提交问题时，先经过 Session 模块处理再进入检索：

```text
1. POST /session/turn { session_id, user_input }
   → action=interrupted：清空对话区，展示"对话已重置"提示，流程结束
   → action=clarify：在对话区展示澄清选项卡，等待用户点选，流程暂停
   → action=retrieve：取 expanded_query.expanded_question 进入下一步

2. POST /answer { question: expanded_question, session_id, deep }
   → 获得 AnswerResult，渲染回答卡片

3. POST /session/working { session_id, answer_id, step_summary, continuable_action }
   → 更新工作状态，补填 session_turns.answer_id
```

**澄清选项卡**（action=clarify 时展示）：

```text
系统提示文本（clarification.question）
[ 选项A ]  [ 选项B ]  [ 选项C ]  [ 跳过 ]
```

用户点选后：

```text
POST /session/clarify { session_id, selected_ref }
→ action=retrieve：取 expanded_query.expanded_question 继续提交 POST /answer
→ action=skip：展示"无法确定对象，请重新描述"提示
```

原始检索接口调用：

```text
POST /answer
```

请求字段：

```json
{
  "question": "...",
  "session_id": "...",
  "deep": false
}
```

`deep=true` 对应请求系统优先使用深路径：只要存在 direct 或 supporting 证据即走 deep；无证据时仍走 none。

底部输入框支持：

```text
Enter 发送；
Shift+Enter 换行；
深度思考开关（映射 deep 字段）；
要求证据开关（MVP 仅预留 UI，不随 POST /answer 提交；后续版本再定义后端语义）。
```

### 5.2 回答展示

回答区展示 AnswerResult。页面显示时使用用户友好标签：

```text
回答正文：content
检索路径：path（short → 简洁模式 / deep → 深度推理 / none → 无法回答 / error → 出错）
检索质量：retrieval_quality（confident / partial / gap，通过关联 trace 获取）
引用：citations（fact_id 列表，点击高亮对应证据）
answer_id：可复制，可重新加载回答
trace_id：Trace 异步生成后显示，可复制，可加载思考记录
```

回答正文展示为对话式文本。引用 fact_id 以标签形式展示在回答下方，点击后在右侧来源面板高亮对应证据。

反馈：

```text
👍 有用  →  POST /traces/:id/feedback  { "type": "positive" }
👎 无用  →  POST /traces/:id/feedback  { "type": "negative" }
```

---

## 6. 证据与解释抽屉

从右侧滑入，宽 320px，点击导航栏"📖 解释"按钮或关闭按钮（✕）切换显示。

有新回答到来时自动打开（`renderRightPanel()` 检测到有效 AnswerResult 时调用 `openExplainDrawer()`）。

抽屉名称：**证据与解释**

### 6.1 标签页结构

默认三个标签页：

```text
概览 | 来源 | 缺口
```

调试模式开启后增加：

```text
共现统计 | 思考记录 | 原始响应
```

### 6.2 概览标签页

展示本次回答的检索过程摘要，不展示内部 ID 和评分明细。

示例：

```text
回答依据

● 问题
  什么是知识大脑

↓
● 检索路径
  简洁模式（short）

↓
● 检索质量
  置信（有直接证据被引用）

↓
● 引用来源
  3 条直接证据 · 2 条补充证据

思考记录
[trace_id 可复制]  [加载思考记录]
```

检索质量说明（用户语言）：

| 质量 | 说明 |
| --- | --- |
| 置信 | 找到了直接回答问题的证据，并被回答引用 |
| 部分 | 只找到了补充性背景证据，无直接证据支持 |
| 缺口 | 知识库中暂无相关内容 |

### 6.3 来源标签页

展示 AnswerResult 中的 `evidence_snapshot`，分两组：

```text
直接证据（direct）
  role=direct 的条目，来自 Rerank 分类为直接答案的 KU

补充证据（supporting）
  role=supporting 的条目，包含：
  - Rerank 分类为背景上下文的 KU
  - KPN 扩展步骤补充的邻居 KU（变量、边界、前提等）
```

每条证据卡片展示：

```text
角色标签（直接证据 / 补充证据）
来源文件名（source_ref → 查找 source 标题）
fact_id（可复制，调试模式可见）
证据文本摘要（前 200 字符）
[查看来源] 入口（调用 GET /sources/:id）
```

点击 `[查看来源]` 时弹出来源详情弹窗，或新标签打开 `GET /sources/:id/preview`。

### 6.4 缺口标签页

分两部分展示：

**本次缺口（来自当前 AnswerResult 的 retrieval_quality）：**

```text
quality=gap：      当前知识库中暂无与该问题相关的内容，已记录为知识缺口。
quality=partial：  未找到直接证据，仅有补充性背景内容。
quality=confident：本次未发现明显知识缺口。
```

**累计知识盲区（来自 `GET /study/gaps`）：**

调用 `GET /study/gaps?limit=10` 展示高频知识缺口列表（按 hit_count 降序）。

每条显示：问题关键词、出现次数、典型问题示例。

### 6.5 共现统计标签页（调试模式）

调用：

```text
GET /cooccurrence?min_confident_count=1&limit=50
```

展示 `question_kp_cooccurrence` 表数据，供调试验证 Trace 共现积累是否正确。

每行展示：

```text
question_terms | point_id | hit_count | confident_count | last_seen_at
```

支持按 point_id 或 question_terms 过滤。

### 6.6 思考记录标签页（调试模式）

调用：

```text
GET /traces/:id
```

展示当前 trace 的详细字段：

```text
问题：question
关键词：question_terms
检索质量：retrieval_quality
直接命中知识点：direct_point_ids（JSON 数组）
是否有反馈：has_feedback
反馈类型：feedback_type
反馈内容：feedback_content
创建时间：created_at
```

提供"展开原始数据"折叠区显示完整 trace JSON。

同时提供学习事件查看，调用 `GET /learning-events` 展示与当前 trace 关联的事件（knowledge_gap / user_correction）。

### 6.7 原始响应标签页（调试模式）

展示 `POST /answer` 的完整响应 JSON，格式化展示，便于核查所有字段。

---

## 7. 文件抽屉

点击顶部"文件"后打开右侧抽屉，宽度 600px。

布局：

```text
┌────────────────────────────────────────┐
│ 文件                                   │
├──────────────────┬─────────────────────┤
│ 上传区            │ 来源列表             │
└──────────────────┴─────────────────────┘
```

### 7.1 上传区

调用：

```text
POST /sources
Content-Type: multipart/form-data
```

表单字段：

```text
file             必填，文件内容
```

title 由服务端从原始文件名自动设置，不上传 title 字段。

支持文件类型（FileView 白名单）：

```text
MD · TXT · PDF · DOCX · HTML · PPT/PPTX · XLS/XLSX
```

上传成功后展示处理流程（不伪造后台进度）：

```text
✓ 文件上传
✓ 来源登记（source_id 已分配）
● Markdown 转换中（轮询 GET /sources/:id 更新状态）
○ 知识单元构建（Source completed 后后台任务已触发，需继续查询 Unit 列表确认）
```

后续状态通过轮询 `GET /sources/:id` 获取，间隔 3 秒，最长 5 分钟后停止。

Source 状态流转：

```text
pending → processing → completed / failed
```

### 7.2 来源列表

调用：

```text
GET /sources?status=&limit=20&offset=0
```

支持 status 过滤：pending / processing / completed / failed

来源卡片展示：

```text
标题
状态标签（pending / processing / completed / failed）
格式 · 创建时间
[详情] [预览] [复制 source_id]
```

操作接口：

| 操作 | 接口 |
| --- | --- |
| 详情 | `GET /sources/:id` |
| 预览 | `GET /sources/:id/preview` |
| Markdown | `GET /sources/:id/markdown` |

预览接口返回 HTML；Markdown 接口返回规范化 Markdown 原文。

---

## 8. 学习报告视图

点击顶部"学习报告"后打开全页覆盖视图。

### 8.1 操作入口

```text
▶ 立即扫描    →  POST /study/run
刷新          →  GET /study/reports/latest
历史报告列表  →  GET /study/reports
```

`POST /study/run` 响应：

```json
{
  "report_id":            "...",
  "candidates_flagged":   5,
  "gap_events_processed": 2,
  "elapsed_ms":           320
}
```

### 8.2 报告结构

调用 `GET /study/reports/latest` 返回最新报告（`study_reports.content` 字段解析）：

**概览统计（4 个数字卡片）：**

```text
总问答次数 | 置信 N（X%）| 部分 N（X%）| 缺口 N（X%）
```

**ActivationLink 候选表格（来自 `activation_link_candidates`）：**

| 问题关键词 | 知识点摘要 | 所属概念 | 命中 | 置信 | 纯度 | 广度 | 短路径 | 推荐 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |

字段说明：

```text
signal_purity      = confident_count / hit_count
activation_breadth = 不同问题关键词集合以高置信命中该 KP 的数量
short_path_rate    = confident 命中中使用短路径的比例
recommendation     = strong / candidate
```

推荐分级（供人工审核参考）：

```text
strong：    signal_purity ≥ 0.7 且 activation_breadth ≥ 3 且 short_path_rate ≥ 0.6
candidate： 满足候选阈值，但未同时满足 strong 全部条件
```

候选说明：

```text
以上候选基于检索信号统计生成，仅供人工审核参考。
MVP 阶段不自动晋升，晋升决策由人工通过 API 触发（V2 阶段实现）。
```

**Wiki 候选表格（来自 `wiki_candidates`）：**

| 概念 | 达标 KP 数 | 平均置信 | KPN 连接数 | 活跃天数 | 状态 |
| --- | --- | --- | --- | --- | --- |

状态标签：

```text
可编译（ready）：qualifying_kp_count ≥ wiki_kp_min 且 kpn_connection_count ≥ 1
数据不足（needs_more_data）：未达到上述条件
```

**知识盲区表格（来自 `knowledge_gaps`，hit_count 降序，最多 20 条）：**

| 问题关键词 | 出现次数 | 典型问题 |
| --- | --- | --- |

**调试模式额外展示：**

```text
共现统计总览（total_cooccurrence_pairs、candidates_flagged）
原始报告 JSON（完整 study_reports.content）
```

---

## 9. 健康检查

调用：

```text
GET /health
```

展示：

```text
服务状态圆点（绿色=正常 / 红色=异常 / 灰色=无法连接）
SQLite 可访问
Bleve 索引可访问
必要数据目录可写
```

---

## 10. 前端模块结构

```text
应用布局
├─ 顶部导航
│  ├─ 品牌标识
│  ├─ 历史抽屉切换按钮（☰ 历史）
│  ├─ 健康状态圆点
│  ├─ 功能入口（文件 · 学习报告）
│  ├─ 解释抽屉切换按钮（📖 解释）
│  ├─ 调试模式开关
│  └─ 设置入口
├─ 问答工作区（全宽）
│  ├─ 对话线程
│  │  ├─ 用户消息气泡
│  │  ├─ 澄清选项卡（action=clarify 时展示，含选项按钮和"跳过"）
│  │  ├─ 回答卡片（content / path / citations / feedback）
│  │  ├─ "对话已重置"提示（action=interrupted 时展示）
│  │  └─ 加载动画
│  └─ 底部输入框（deep / require_evidence 开关 · 发送按钮）
├─ 面板遮罩（z-index 89，历史 / 解释抽屉共用，点击可关闭）
├─ 会话历史抽屉（左侧滑入，z-index 90）
│  ├─ ＋ 新对话（POST /sessions）
│  └─ 会话列表（GET /sessions）
│     ├─ 会话条目（title · 相对时间 · 当前高亮）
│     │  ├─ 点击切换（GET /sessions/:id/turns → 渲染对话记录）
│     │  └─ 删除按钮（DELETE /sessions/:id）
├─ 解释抽屉（右侧滑入，z-index 90，有回答时自动打开）
│  ├─ 概览面板（path · quality · 证据数量 · answer_id / trace_id）
│  ├─ 来源面板（evidence_snapshot 分组展示）
│  ├─ 缺口面板（当次 quality + GET /study/gaps）
│  └─ 调试面板（调试模式）
│     ├─ 学习候选面板（GET /study/candidates）
│     └─ 原始响应面板
├─ 文件抽屉（右侧滑入，z-index 101，独立遮罩）
│  ├─ 上传区（拖拽 / 点击 · 进度流水线 · 轮询 source 状态）
│  ├─ 来源过滤（status）
│  └─ 来源列表（source 卡片）
├─ 学习报告视图（全页覆盖）
│  ├─ 概览统计
│  ├─ ActivationLink 候选表格
│  ├─ Wiki 候选表格
│  └─ 知识盲区表格
└─ 设置弹窗（服务器地址）
```

---

## 11. 技术约束

**前端技术栈：HTML + JS，不引入任何框架。**

```text
HTML + CSS + JavaScript（ES Modules）
无 React / Vue / Angular 等前端框架
无 npm / node_modules，无构建步骤
fetch API 调用后端接口
localStorage 仅存储：服务器地址、调试模式开关（不存会话内容，会话由后端持久化）
原生 DOM 操作（抽屉、弹窗、标签页）
当前 session_id 存储在前端内存变量中，刷新后从 GET /sessions 取列表第一条恢复
```

**部署模式：前后端一体，不分离。**

```text
web/index.html 是单一文件，内嵌全部 CSS 和 JS，无外部资源依赖；
通过 Go 标准库 go:embed 指令嵌入编译产物，随 wiki-brain 二进制一同分发；
Go HTTP 服务在 GET / 路由直接返回该 HTML，无需独立前端服务器；
不存在独立的前端构建链、部署流程或跨域请求；
用户访问 http://localhost:8080 即可使用，无需额外配置。
```

静态资源目录：

```text
web/
  index.html    单文件，内嵌全部 CSS 和 JS
```

约束：

```text
没有真实后端数据时展示空状态提示，不伪造数据；
一旦接口可用，优先调用真实接口；
所有接口响应必须能切换查看原始 JSON；
answer_id、trace_id 和 source_id 必须可一键复制；trace_id 未生成时显示"记录中"；
所有 HTTP 错误展示 status 和响应体原文。
```

---

## 12. 视觉风格

```text
浅色优先：白色 + 浅灰背景
正文：深灰（#111827）
强调：蓝色（#3b82f6）
成功 / 置信：绿色（#10b981）
警告 / 部分：黄色（#f59e0b）
错误 / 缺口：红色（#ef4444）
圆角：12px 主卡片 · 8px 小组件
阴影：轻柔投影
```

检索质量颜色：

| 质量 | 颜色 |
| --- | --- |
| confident（置信） | 绿色 |
| partial（部分）  | 黄色 |
| gap（缺口）      | 红色 |

---

## 13. MVP 验收标准

页面完成后，应能验证以下链路：

```text
1.  健康检查可查看，服务状态实时展示；
2.  会话列表可加载（GET /sessions），新建、删除、切换会话均正常；
3.  切换到历史会话后，对话记录正确还原（含回答卡片、澄清记录）；
4.  服务重启后切换到旧会话，SessionState 从数据库恢复，可继续对话；
5.  文件可以上传为来源，source_id 可复制；
3.  来源列表可过滤，状态更新通过轮询反映；
4.  来源详情可以查看（GET /sources/:id）；
5.  用户可以提交问题，回答正常展示（POST /answer）；
6.  回答 content 可读，path 标签正确（short / deep / none / error）；
7.  evidence_snapshot 在来源面板按 direct / supporting 分组展示；
8.  点击 fact_id 标签可高亮对应证据卡片；
9.  quality=gap 时，缺口面板展示提示，知识盲区列表正确加载；
10. quality=confident 时，概览面板展示有效检索信号；
11. 默认模式不直接暴露 point_id、fact_id 等内部 ID；
12. Trace 生成后反馈（正向 / 负向）可提交（POST /traces/:id/feedback），未生成前按钮禁用或提示"记录中"；
13. answer_id 可复制；trace_id 生成后可复制，GET /traces/:id 能加载思考记录；
14. 调试模式下可查看共现统计（GET /cooccurrence）；
15. 调试模式下可查看学习事件（GET /learning-events）；
16. 调试模式下可查看原始响应 JSON；
17. 学习报告视图能展示最新报告（GET /study/reports/latest）；
18. 立即扫描（POST /study/run）可触发，完成后报告可刷新；
19. ActivationLink 候选和 Wiki 候选正确渲染，含推荐分级；
20. 知识盲区列表正确展示 hit_count 和典型问题；
21. 设置可更改服务器地址，修改后立即生效。
```
