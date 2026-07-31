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
  完整字段、created_from、pending_promote_reason（若有）；
  问法列表懒加载：GET /activation-links/:id/questions
    （matched = traces.activation_link_ids 命中；
     created_from = created_from 事件关联 traces）；
  状态迁移史懒加载：GET /activation-links/:id/learning-results
    （前端中文映射 action/status/常见 reason）。

待确认徽标：导航入口显示 pending_confirm 的 promote 数量
  （GET /study/results?action=promote&status=pending_confirm 计数），
  30 秒轮询，与健康检查共用定时器。
```

### 步骤 3：Wiki 视图

顶部导航新增「Wiki」入口。布局对齐「知识领域」页（左侧 rail + 右侧卡片网格）：

```text
目录（GET /wiki/catalog）：
  左侧按知识领域列出；计数为本领域可见 Wiki 数；
  右侧展示该领域下全部 Wiki 卡片（含归档），不按候选/正式分区；
  卡片字段：主题（title）、说明（description）、状态徽标
    （待编译 / 草稿 / 待重编译 / 已发布 / 已删除）；
  卡片来源：
    - 概念页：归入 concept.domain_id；
    - 主题页：归入全部成员概念所属领域（可多领域重复出现）；
    - 待编译候选（wiki_candidate pending_confirm，且尚无非 archived 页面）；
  排序：待编译 → 草稿 → 待重编译 → 已发布 → 已删除，同状态按更新时间倒序。

点击待编译卡片：
  [分析] → POST /wiki/compile/analyze → [确认并编译] POST /wiki/compile
  （page_type=concept；有 result_id 时一并带回）。

人工指定主题（wiki.md 步骤 2 / 步骤 8 第二条生成口径）：
  抽屉顶栏 [+ 生成 Wiki] 提供两种模式：
    概念页：选知识领域 → 搜索/点选单个概念 →
      POST /wiki/compile/analyze → 确认并编译（无 result_id）；
    主题页：多选已发布概念页（跨领域可选）→
      POST /wiki/topics 建壳 → topic/analyze → 确认并编译；
  不在概念详情页放入口。

点击已生成页面卡片 → GET /wiki/pages/:id 打开详情弹窗：
  渲染 Markdown 正文；[point_id] 标注可点击引用；
  操作按钮按状态显示：
    draft            → [发布] POST /wiki/pages/:id/publish
    needs_recompile  → [重编译] POST /wiki/pages/:id/recompile
    published        → [归档] POST /wiki/pages/:id/archive
    主题页壳（content 为空）→ [分析]/[驳回]（topic/analyze|compile|archive）
  修订记录列表，点击查看历史版本
  （GET /wiki/pages/:id/revisions/:rev）。
```

两层架构扩展（wiki.md 步骤 7-10）：

```text
主题页壳（Study topic_page_candidate 产物）出现在成员概念所属领域的卡片网格中；
  详情弹窗内 [分析] → POST /wiki/pages/:id/topic/analyze →
  [确认并编译] POST /wiki/pages/:id/topic/compile；[驳回] → 壳页 archive；
  成员页面存在非 published 时编译返回 409 并列出待处理页面；

页面阅读增加关系区（GET /wiki/pages/:id/relations）：
  概念页：related / contradicts 邻居页面链接（附共享 KP 数），
    以及「所属主题页」（contains 反向）；
  主题页：contains 成员页面列表（附各自 status，非 published 标黄）
    与 member_roles（每个成员承担的面向 / 能回答的问题类型）；
  关系是程序派生的只读信息，不提供人工增删关系的入口；

覆盖度区（页面详情的 uncovered_points 字段）：
  折叠列表「本主题尚无可用材料的知识点（N）」，逐条显示 point_id 与摘要，
    点击查看 KP / KU；概念页为自身清单，主题页为成员并集；
  文案要说清它不是页面内容的一部分：这些 KP 还没有 verified 激活链接，
    不进稳定结论、不可引用；写作时把它读作「这几块还写不了」；
  为空时整区隐藏。

写作草稿（GET /wiki/drafts?page_id=）：
  页面阅读页 [派生草稿] → POST /wiki/pages/:id/drafts
    （主题页默认 mode=assembled，界面上说明"将并入 N 个成员页面正文"；
     概念页默认 mode=page，可切换）；
  草稿编辑器：textarea 直接改 title / content / note，
    失焦或手动保存 → PATCH /wiki/drafts/:id（不做任何校验提示）；
  右侧固定证据清单（evidence_index，只读）：point_id / KP 摘要 /
    KU 主题 / 来源位置，点击复制 [point_id] 标注，供人工改写后重新挂引用；
  stale 标记（来源版本已不是页面最新版本）显示为提示条，
    提供并列查看当前页面正文的入口，但**不提供**「合并回页面」按钮；
  草稿区文案需明确：草稿不参与检索；内容要长期沉淀请导出文件走导入，
    导入时勾选「来自 Wiki 草稿」（origin=wiki_draft）以免自体循环。
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
POST /wiki/compile、/wiki/pages/:id/publish|recompile|archive、GET /wiki/pages*、GET /wiki/catalog   wiki.md / page.md
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
