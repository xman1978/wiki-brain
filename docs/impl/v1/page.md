# Page 实现路径（V1 升级）

## 职责

在 MVP 测试控制台（`web/index.html`，单文件，go:embed）基础上新增：问答界面的反馈入口与路径标识、ActivationLink 管理视图、Wiki 视图、学习动作审计视图。继续沿用单文件前端与同域 API 调用，不引入前端构建。

## 实现步骤

### 步骤 1：问答界面升级

**路径标识**：每条回答气泡头部增加路径徽标，数据来自 `POST /answer` 响应新增的 `path_type` 字段：

```text
⚡ 快路径（fast）   蓝色徽标，hover 显示命中的链接条件（经解释抽屉查看详情）
📖 Wiki（wiki）     绿色徽标，附页面标题链接（跳转 Wiki 视图）
🔍 完整链路（full） 灰色徽标
```

**反馈入口**：每条回答下方两个按钮：

```text
👍 有用   → POST /traces/:id/feedback { "type": "positive" }
✏️ 纠正   → 展开文本框，提交 { "type": "correction", "content": "..." }
```

trace_id 获取沿用 MVP 方式：回答渲染后轮询 `GET /traces?answer_id=...`（Trace 异步写入），拿到前反馈按钮置灰。提交成功后按钮变为已反馈状态，不可重复提交（traces.has_feedback 已置位时后端幂等覆盖，前端只做展示约束）。

**解释抽屉扩展**：证据列表每条显示 `mined` 标记（片段 / 整段）与片段行号；fast 路径显示 activation_hits（链接条件、match_score）；证据 content 为片段原文，点击展开所属 KU 上下文（`GET /units/:id`）。

### 步骤 2：ActivationLink 管理视图

顶部导航新增「激活链接」入口，独立视图（与学习报告视图同层切换）：

```text
布局：状态 Tab（candidate / verified / weakened / deprecated）+ 列表 + 详情侧栏

列表（GET /activation-links?status=...）：
  question_terms ｜ point_summary ｜ unit_center ｜
  adopt_count / fail_count ｜ last_used_at

candidate Tab 行内操作：
  [确认晋升] → POST /activation-links/:id/confirm
  [驳回]     → POST /activation-links/:id/reject
  操作前弹确认框，显示该链接 pending 的 Learning Reason
  （GET /activation-links/:id 中关联的 learning_results）；

详情侧栏（GET /activation-links/:id）：
  完整字段、created_from、状态迁移史（learning_results 时间线，
  每条含 action / reason / status / created_at）。

待确认徽标：导航入口显示 pending_confirm 的 promote 数量
  （GET /study/results?action=promote&status=pending_confirm 计数），
  30 秒轮询，与健康检查共用定时器。
```

### 步骤 3：Wiki 视图

顶部导航新增「Wiki」入口：

```text
候选区（GET /study/results?action=wiki_candidate&status=pending_confirm）：
  显示 concept 名称、qualifying KP 数、Learning Reason；
  [编译] → POST /wiki/compile { concept_id, page_type, result_id }
  （page_type 下拉：topic / concept，默认 concept）；

页面列表（GET /wiki/pages）：
  按 status 分组（draft / published / needs_recompile / archived）；
  needs_recompile 页面标黄，显示标记原因；

页面阅读（GET /wiki/pages/:id）：
  渲染 Markdown 正文；[point_id] 标注渲染为可点击引用，
  点击在侧栏显示 KP 内容与所属 KU（GET /points/:id → GET /units/:id）；
  操作按钮按状态显示：
    draft            → [发布] POST /wiki/pages/:id/publish
    needs_recompile  → [重编译] POST /wiki/pages/:id/recompile
    published        → [归档] POST /wiki/pages/:id/archive
  修订记录列表，点击查看历史版本
  （GET /wiki/pages/:id/revisions/:rev）。
```

### 步骤 4：学习动作审计视图

学习报告视图内新增「学习动作」Tab：

```text
列表（GET /study/results，支持 action / object_type / status 筛选）：
  时间 ｜ action ｜ 对象（链接条件或 concept 名）｜ reason ｜ status

行点击展开（GET /study/results/:id）：
  支撑事件列表（event_type、payload 摘要、created_at），
  事件可跳转对应 trace 详情（既有调试视图复用）；

报告页 summary 区新增 fast_path_rate 指标卡与
learning_actions 汇总（本期各动作计数）。
```

### 步骤 5：文件抽屉扩展（lifecycle 操作入口）

```text
Source 列表行操作新增：
  [重新上传] → 文件选择后 POST /sources/:id/reupload，行内显示影子 Source
               的处理进度（沿用状态轮询，用户看不到影子 ID，只看到本行状态）；
               影子失败时行内显示"重新上传失败，原内容不受影响"并提供
               [重试] 按钮（调 POST /sources/:id/reupload/retry）；
               换血完成后行内刷新为新内容，旧 KU 数量以 superseded 角标展示；
  [删除]     → 二次确认后 DELETE /sources/:id，行置灰显示 deleted；
Source 详情的 KU 列表显示 lifecycle 徽标（current 之外的状态标灰/标黄）。
```

## 涉及的后端接口清单（均已在各模块文档定义）

```text
POST /traces/:id/feedback                    trace.md（MVP 既有）
GET  /activation-links[/:id]                 activation.md
POST /activation-links/:id/confirm|reject    activation.md
GET  /study/results[/:id]                    study.md
POST /wiki/compile、/wiki/pages/:id/publish|recompile|archive、GET /wiki/pages*   wiki.md
POST /sources/:id/reupload、DELETE /sources/:id                             lifecycle.md
POST /answer 响应 path_type、GET /traces 响应 path_type/activation_link_ids  retrieval.md / trace.md
```

Page 不新增后端接口；发现缺口时回到对应模块文档补充定义，不在前端绕行。

## 完成标准

```text
回答显示路径徽标，三种路径均正确渲染；
反馈两键可用，correction 生成 user_correction 事件（调试视图可见）；
证据列表区分片段/整段，片段可展开 KU 上下文；
candidate 链接可在页面完成确认/驳回，操作后列表与徽标计数刷新；
Wiki 候选可编译、draft 可发布、needs_recompile 可重编译，
  引用标注可点击回链 KP/KU；
审计视图可从任一学习动作下钻到支撑事件与 trace；
reupload / delete 入口可用，lifecycle 徽标正确显示；
全部功能在单文件 index.html 内实现，无构建步骤，go:embed 打包后可用。
```
