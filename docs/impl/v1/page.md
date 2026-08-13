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

**解释抽屉扩展**：证据列表每条显示 `mined` 标记（片段 / 整段）与片段行号；fast 路径显示 activation_hits（链接条件、match_score、matched_by）；证据 content 为片段原文，点击展开所属 KU 上下文（`GET /units/:id`）。**matched_by 字段现状（2026-08-12 当天两次修订）**：曾于当天新增用于区分"精确"还是"模型辅助"，方便核对模型判断、观测触发率；同一天晚些时候 Match 第二轮模型辅助匹配被改判撤销，`matched_by` 现在恒为 `exact`，"模型辅助"这个取值在实际数据里不会再出现。字段本身与 UI 展示分支不删（不破坏 API/schema 稳定性，展示逻辑保留但已成为死分支），仅在此注明其现状是历史遗留、不代表当前还会触发。

**熟路指针（2026-08-12，设计层面，非实现变更）**：`retrieval.md` 步骤 1 新增的 `bundle_hits[]`（熟路命中溯源，字段形状同 `activation_hits[]` 但 `point_id` 换成 `member_point_ids[]`）尚未实际产生数据——熟路的 Retrieval 消费是「阶段 2」范围，还没实现。解释抽屉展示 `bundle_hits[]` 的方式预期与 `activation_hits[]` 同构（链接条件、match_score、matched_by，只是"链接"换成"熟路成员组"），本文档暂不预先设计具体展示细节，待 `retrieval.md` 阶段 2 落地后再回来补。

### 步骤 2：ActivationLink 管理视图（2026-08-13 大幅改写：从状态机操作台改为置信度/分档观测台）

**2026-08-13 起的机制基准**：`activation.md`「状态机」已经把 candidate/verified/weakened/deprecated 四态离散跳变替换为每条观测条件自己的连续置信度（`mean(cond)`）与三档服务分档（exploring/self_graded/trusted），`POST /activation-links/:id/confirm` 已删除——没有"晋升"这个动作，也就没有对象可供人工确认。本视图相应地从「操作台」（人工在这里推进状态跳变）改为「观测台」（人工在这里看清楚每条条件当前的置信度、消耗了多少试探名额，以及要不要清空重来）。

顶部导航新增「激活链接」入口，独立视图（与学习报告视图同层切换）：

```text
布局：status Tab（candidate / verified / deprecated，2026-08-13 起只有
  三态，weakened 已退休——不是拿掉一个 Tab 那么简单，是这个中间态本身
  不再存在，见 activation.md「与旧状态机的映射」）+ 列表 + 详情侧栏

列表（GET /activation-links?status=...）：
  question_terms ｜ point_summary ｜ unit_center ｜
  adopt_count / fail_count ｜ last_used_at
  （adopt_count/fail_count 仍是展示用累计值，不是 tier 判定依据，
  见 activation.md「数据结构」；要看具体判定依据，进详情侧栏看
  每条条件的 mean/tier）

行内操作（2026-08-13 收窄为一种，晋升确认相关的按钮全部移除）：
  [清空重来] → POST /activation-links/:id/reject（对任意 status 生效，
    见 activation.md 步骤 3）；操作前弹确认框，文案需明确这不是"驳回
    候选"而是"清空该链接当前全部观测条件、置信度归零重新积累"，操作
    后该链接不会消失，只是回到没有可判断条件的默认状态（派生 status
    回落 candidate），后续新证据会重新积累；
  不再提供「确认晋升」按钮——没有晋升这个动作。

详情侧栏（GET /activation-links/:id）：
  完整字段、created_from；不再有 pending_promote_reason（这个字段
    已从 API 响应移除，见 activation.md 步骤 3）；

  **条件明细表（本次改写新增的核心可观测数据，替代原先只看链接整体
  一个状态标签的展示方式）**——GET /activation-links/:id 响应新增的
  conditions[] 数组，每行渲染：
    subject / intent / audience / constraint（这条条件的四元组原文）｜
    success_count / failure_count（原始计数，供人核对）｜
    mean（置信度分数，格式化为百分比，如 "72%"）｜
    tier 徽标（exploring 灰色 / self_graded 蓝色 / trusted 金色）｜
    audited_success_count / audited_failure_count（独立核实样本量，
      只在 tier=trusted 或 self_graded 且样本量接近 audit_sample_min
      时展示，避免全零列表噪声）｜
    last_seen_at；
  mean 用一条横向进度条辅助可视化（0.5 居中留白示意"还不确定"，
    向两端偏移颜色渐变），比单纯数字更快看出"这条条件到底稳不稳"；
  条件多于一屏时按 mean 降序排列（最可信的排前面，供人优先核对高分
    条件是否真的合理，其次核对低分条件是否该清空）；

  问法列表懒加载：GET /activation-links/:id/questions
    （matched = traces.activation_link_ids 命中；
     created_from = created_from 事件关联 traces）；
  学习动作时间线懒加载：GET /activation-links/:id/learning-results
    （前端中文映射 action/status/常见 reason；2026-08-13 起不会再看到
    promote/weaken/reverify 类 action，只会看到 create_candidate 与
    prune_condition 两种，见 study.md「learning_results action
    枚举变化」）。

导航入口徽标（2026-08-13 改写：从"待确认数量"改为"探索档存量"）：
  原「待确认晋升数」徽标（GET /study/results?action=promote&
    status=pending_confirm）已随晋升确认流程一并移除——没有待确认
    队列需要提醒人来清空；
  替代为一个展示性数字：tier=exploring 的条件总数（跨全部链接聚合，
    近似值即可，不要求强一致，30 秒轮询同既有节奏）——这不是一个
    "需要处理"的红点，是"系统里还有多少条件处于试探阶段、尚未收敛"
    的健康度概览，点击跳转本视图并预筛 tier=exploring；
  该徽标数字本身不驱动任何操作——不像原 pending_confirm 徽标暗示"点开
    一条条确认"，这里只是让人对系统整体的收敛程度有个直觉，具体要不要
    干预（例如判断某条长期停在 exploring 的条件是否该人工清空）仍要
    进入详情侧栏逐条查看。
```

**Wiki 材料确认队列（2026-08-11 新增，2026-08-12 整体删除）**：曾计划在「激活链接」入口下新增二级 Tab，承接 `wiki_material_confirm` 的确认/驳回操作。该人工确认关卡已随 `docs/design/wiki.md`「2026-08-12 改判」整体废弃——Wiki 材料是否够格由编译时的整体判断（广度/连贯/稳定）自然回答，不再需要候选阶段单独的人工确认，`web/index.html` 中相应的 Tab、按钮与 `loadWikiMaterialConfirmList`/`confirmWikiMaterial`/`rejectWikiMaterial` 函数已一并移除，本文档不再保留这段 UI 规格。

### 步骤 2c：Subject 同义词浏览与驳回队列（2026-08-12 新增，2026-08-13 从前端删除）

曾挂在「激活链接」入口下的二级 Tab，浏览 `subject_synonyms` 并对 active/candidate 行执行 confirm/reject。`web/index.html` 中该 Tab、列表与 `loadSynonymsList`/`confirmSynonymRow`/`rejectSynonymRow` 已一并移除；后端 `GET/POST /subject-synonyms*` 仍保留（见 `activation.md` 步骤 3a），preset 别名导入、gap_mined 挖掘链路与 Wiki 概念页别名展示不受影响。本文档不再保留这段 UI 规格。

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

综合满意度区（2026-08-13 新增，见 wiki.md 步骤 4a）：
  页面详情弹窗展示 mean(page)（同 activation-links 详情侧栏的进度条
    风格）与 synthesis_success_count/synthesis_failure_count 原始计数；
  只读展示，不提供任何操作按钮——mean(page) 不驱动 needs_recompile，
    "要不要因为这个数字重编译"完全是人工判断，UI 上不应该出现任何
    暗示"点这里就能处理"的按钮，避免误导成看似有自动化捷径；
  样本量为 0（尚未被独立核实抽样命中过）时显示"暂无独立核实数据"，
    不显示默认的 mean=0.5，避免人误读成"已核实、结果居中"。

覆盖度区（页面详情的 uncovered_points 字段）：
  折叠列表「本主题尚无可用材料的知识点（N）」，逐条显示 point_id 与摘要，
    点击查看 KP / KU；概念页为自身清单，主题页为成员并集；
  文案要说清它不是页面内容的一部分：这些 KP 还没有 verified 激活链接
    （2026-08-12 修订：qualifying 只看 verified，不再有 Wiki 材料确认
    这道单独关卡，见 wiki.md 步骤 3），不进稳定结论、不可引用；写作时
    把它读作「这几块还写不了」；
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
GET  /activation-links[/:id]                 activation.md（:id 响应
                                              2026-08-13 起含 conditions[]
                                              置信度明细）
POST /activation-links/:id/reject            activation.md（2026-08-13
                                              起对任意 status 生效，语义
                                              为"清空重来"；confirm 端点
                                              已随晋升确认流程删除）
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
链接详情侧栏正确渲染 conditions[] 明细（subject/intent/audience/
  constraint、success_count/failure_count、mean、tier 徽标），mean
  进度条与 tier 徽标颜色随数据变化正确更新（fake 环境构造不同 tier
  边界值断言三色渲染正确）；
[清空重来]（POST /activation-links/:id/reject）对任意 status 的链接可用，
  操作后该链接 conditions 清空、派生 status 回落 candidate，
  列表与「探索档存量」徽标数字刷新；不再存在「确认晋升」入口
  （UI 审计：页面不出现调用 /activation-links/:id/confirm 的代码路径）；
Wiki 候选可编译、draft 可发布、needs_recompile 可重编译，
  引用标注可点击回链 KP/KU；
审计视图可从任一学习动作下钻到支撑事件与 trace；
reupload / delete 入口可用，lifecycle 徽标正确显示；
全部功能在单文件 index.html 内实现，无构建步骤，go:embed 打包后可用。
```
