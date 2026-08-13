# Wiki 编译实现路径（V1 初版）

## 职责

实现主题页 / 概念页的最小闭环：Study 产出的 Wiki 候选经人工确认后触发 LLM 编译，生成带证据回链的页面；人工发布后进入独立 Bleve 索引，作为检索的 Wiki 直答层；底层 KU/KP 状态变化时标记待重编译。

**两层架构扩展（步骤 7-10）**：词条页（概念页 / 事实页）为一阶编译（KP → 页面），主题页为二阶编译，两者由程序派生的页面关系（`related` / `contradicts` / `contains`）串成知识架构；写作出口是页面派生的草稿，页面本身保持只由编译产生。设计依据见 `docs/design/wiki.md`「页面关系只有三种，层级由 contains 承载」。

**概念页 / 事实页（2026-08-03 修订，V1 正式实现，替代此前"事实页推迟到 V3"的口径）**：一阶编译按知识点的归属判断结果分岔成概念页和事实页两条产出——归属判断依据见 `kpn.md` 步骤 3「类型标注」，落在 `entries.kind` 字段（concept / fact，migration 043）。概念页与事实页是**同一条一阶编译链路**（analyze → 确认 → 生成 → 发布 → 重编译 → 关系派生 → 二阶接入，即本文档步骤 1-9）的两个产出分支，不是两套并行的实现：qualifying/ready 判定、citation 白名单、页面关系派生、二阶主题页接入的判据逻辑完全共用，唯一的分岔点是 `wiki_pages.page_type` 由目标 concept 行的 `kind` 决定（kind=concept → page_type=concept，kind=fact → page_type=fact），以及编译 Prompt 依据 kind 调整生成措辞（见步骤 3）。下文「概念」在指代一阶编译输入的归属对象时，等同于「概念或事实（按 entries.kind 区分）」，仅在需要专门说明两者差异处才写全「概念/事实」。

**主题候选识别机制（2026-08-03 修订）**：步骤 8 的主题候选不再从"已发布概念页的图连通性"事后推导——设计依据变更为 `docs/design/wiki.md`「主题：从真实使用中识别，而不是从已发布词条事后聚类」，主题候选改为直接对真实提问的四元组（`traces.subject/intent/audience/constraint_text`）聚类，与材料侧当前是否已有已发布页面无关；候选范围确定后才在其中检索知识点、按归属分组，尚未独立发布的概念可随主题候选一并推进一阶编译。这一条修改了步骤 8 的候选产生机制、`POST /wiki/topics` 的人工指定语义，并影响 CLAUDE.md「V1 关键设计决策」第 6 条；**具体阈值配置（`wiki.topic_cluster_min_questions` 等）与"随批推进一阶编译"的落库细节是本次文档修订新提出的实现方案，设计文档只确立机制方向，未固定这些参数，编码前建议再核对一遍**。旧的连通分量口径已从本文档移除，`internal/study/`、`internal/wiki/` 中对应实现尚未跟进，此前引用旧口径的代码注释暂时失配，见步骤 8 说明。

方法页 / 经验页 / 问题页 / 决策页已从设计中删除（此前只是名义上的分类，从未接入过具体的编译流程，详见 `docs/design/wiki.md`「Wiki 页面类型」一节），不是推迟；视角化编译推迟到 V3；Claim 双产物与防固化要素补齐属 V2（见 docs/impl/v2/readme.md）。复杂问题的拆解与子结论聚合属深想路径 / Working Model，是 V3 能力——V1 只建结构并记录 `topic_decompose_signal`（步骤 9）。

**熟路（ActivationBundle，2026-08-11 新增，设计方向，尚未进入实现步骤）**：设计依据见 `docs/design/activation-bundle.md`。ActivationLink 只回答「单个知识点管不管用」，熟路补的是「一组知识点合在一起管不管用」——这一半信号目前 Wiki 侧完全没有，步骤 3 的 qualifying 判定与步骤 8 的候选范围检索都只能各自间接猜测。熟路成熟后，预期在本文档两处起补充作用（**不改变现行任何一处的实际判据**，仅在下方对应位置留了指针）：一是步骤 8 第 1 步的四元组聚类可与熟路显影共用同一次扫描，避免同一批 traces 在两处被分别归堆出不一致的分组；二是熟路的稳定核（历史上真被同一类问题依赖过的知识点组合）可以作为候选范围材料与可靠度判定的补充证据，比单纯语义检索更准。这条尚未有独立的 V1 实现文档（不在 CLAUDE.md「实现顺序」现有列表中），落地前需要先确定 ActivationBundle 自身的存储与匹配实现（预期挂在 `activation.md` 或新增同级文档）、以及下方两处的具体阈值/口径，均需用户先确认，本次修订只做设计层面的同步引用，不预先改写 qualifying 判据或候选检索逻辑。

## 数据结构

```sql
CREATE TABLE wiki_pages (
    page_id          TEXT PRIMARY KEY,
    page_type        TEXT NOT NULL,
    -- concept / fact / topic（V1 三种；concept/fact 编译输入与流程
    -- 完全相同，由目标 concept 行的 kind 决定取值，区别在生成措辞提示；
    -- topic 是二阶编译产物，区别见步骤 3）
    entry_id       TEXT REFERENCES entries(entry_id),
    title            TEXT NOT NULL,
    content          TEXT NOT NULL,
    -- 页面正文 Markdown（含防固化要素，见步骤 3）
    status           TEXT NOT NULL DEFAULT 'draft',
    -- draft / published / needs_recompile / archived
    source_point_ids TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：页面依赖的 KP（编译输入的 qualifying KP）
    source_unit_ids  TEXT NOT NULL DEFAULT '[]',
    source_link_ids  TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：编译时 source_point_ids 上已存在的 verified ActivationLink id
    -- （migration 028）。纯依赖回链元数据，不进编译输入/prompt——呼应
    -- docs/design/wiki.md 防固化要素"依赖的 ActivationLink"，
    -- 用于页面详情展示和未来生命周期追溯，不驱动重编译判断。
    aliases          TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：概念别名/缩写/口语叫法（migration 029，编译时 LLM 生成，
    -- 只进 wiki index 作检索字段，不属于正文，不参与 citation 白名单）
    trigger_questions TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：该页面能回答的典型问法（migration 029，5-10 条），
    -- 用于弥合用户措辞与页面用词的词汇鸿沟（Wiki = Answer + Retrieval Index）。
    -- 生成素材改为真实观测问法（见下方"编译输入"新增一项），LLM 角色从
    -- "凭材料想象"变为"从真实问法里挑选/归纳"（docs/design/wiki.md
    -- "触发问法取材真实观测，检索匹配复用四元组"）。
    observed_conditions TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：编译/重编译时，source_point_ids 上各 verified ActivationLink
    -- 的 observed_conditions 并集（新增 migration，编号待实现时按当前最大版本
    -- 号+1；结构与 activation_links.observed_conditions 相同，见 activation.md）。
    -- 只读消费 ActivationLink 已有数据，不驱动 promote/weaken 统计，也不影响
    -- 编译输入/citation 白名单——仅供步骤 4 检索侧的四元组入口匹配使用。
    compiled_from    TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：触发编译的 learning_result / report 标识；人工手动指定主题
    -- 编译（无 result_id，见步骤 2「人工指定主题手动编译」）时存哨兵值
    -- ["manual_trigger"]，不是空数组——用于区分页面来源，不新增列
    prompt_version   TEXT NOT NULL,
    model_name       TEXT NOT NULL,
    compiled_at      DATETIME,
    published_at     DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_status  ON wiki_pages(status);
CREATE INDEX idx_wiki_entry ON wiki_pages(entry_id);

CREATE TABLE wiki_revisions (
    revision_id  TEXT PRIMARY KEY,
    page_id      TEXT NOT NULL REFERENCES wiki_pages(page_id),
    content      TEXT NOT NULL,
    -- 该版本完整正文快照
    reason       TEXT NOT NULL,
    -- 编译/重编译原因（含触发来源描述）
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_rev_page ON wiki_revisions(page_id);
```

**两层架构扩展**（migration 编号按实现时当前最大版本号 +1，下称 035；设计依据见
docs/design/wiki.md「页面关系只有三种，层级由 contains 承载」）：

```sql
CREATE TABLE wiki_page_relations (
    relation_id   TEXT PRIMARY KEY,
    from_page_id  TEXT NOT NULL REFERENCES wiki_pages(page_id),
    to_page_id    TEXT NOT NULL REFERENCES wiki_pages(page_id),
    relation_type TEXT NOT NULL,
    -- related / contradicts：无向，from/to 按 page_id 字典序归一化只存一行，
    --   由程序从 KPN 派生（步骤 7），不调 LLM；
    -- contains：有向，from=主题页、to=成员概念页，二阶编译时写入（步骤 8）。
    -- 只有这 3 种，不引入 broader / narrower——KPN 只有 related/contradicts
    -- 且 direction 恒 bidirectional，entries 表在 domain 下平铺无父子，
    -- 都派生不出层级；层级唯一来源是 contains。
    derived_from  TEXT NOT NULL,
    -- kpn（related / contradicts）/ compile（contains）
    evidence      TEXT NOT NULL DEFAULT '{}',
    -- JSON：{"shared_point_ids":[...],"kpn_relation_count":N}
    --   related / contradicts 的派生依据，供页面详情展示与人工核对；
    --   contains 恒为 {}
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_wpr_uniq ON wiki_page_relations(from_page_id, to_page_id, relation_type);
CREATE INDEX idx_wpr_to ON wiki_page_relations(to_page_id, relation_type);

CREATE TABLE wiki_drafts (
    draft_id           TEXT PRIMARY KEY,
    page_id            TEXT NOT NULL REFERENCES wiki_pages(page_id),
    source_revision_id TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    -- 建草稿时来源页面的正文版本，用于判断草稿是否已落后于页面
    source_page_ids    TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：组装模式下实际并入的页面（主题页自身 + 成员概念页），
    -- 单页模式等于 [page_id]（见步骤 10）
    evidence_index     TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：只读证据清单，派生时生成、之后不随人工编辑变化
    -- [{ "point_id","point_summary","unit_id","unit_topic",
    --    "source_ref":{"source_id","line_start","line_end"} }]
    -- 人工改写会丢掉正文里的 [point_id] 标注，清单让引用还能重新挂回去
    title              TEXT NOT NULL,
    content            TEXT NOT NULL,
    note               TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_drafts_page ON wiki_drafts(page_id);
```

`wiki_pages` 同一 migration 新增两个字段：

```sql
ALTER TABLE wiki_pages ADD COLUMN member_roles     TEXT NOT NULL DEFAULT '[]';
-- 主题页专用（概念页恒 []）：二阶编译时结构化落库的成员分工，
-- [{ "member_page_id","aspect","question_types":["..."] }]
-- 与正文「子主题分工」一节同源，但不要求 V3 回去解析 Markdown
-- （V2 的立场是「不从 Wiki 文本临场解析」，见 docs/impl/v2/readme.md）；
-- 也是步骤 8 检索侧成员展开的排序依据。

ALTER TABLE wiki_pages ADD COLUMN uncovered_points TEXT NOT NULL DEFAULT '[]';
-- 覆盖度显式化：该页主题范围内 lifecycle=current 但尚不 qualifying
-- （2026-08-12 修订，取代 2026-08-11 的口径：qualifying 恢复为只看
-- verified ActivationLink，wiki_material_confirm 人工确认关卡整体
-- 废弃，故此处即「无 verified ActivationLink」，口径同步骤 3）的 KP 清单
-- [{ "point_id","summary" }]。
-- **只作字段，不进正文**——正文四节 / 五节结构与 citation 白名单校验
-- 保持不变，避免破坏既有校验与存量页面；Page 视图单独展示（page.md）。
-- 概念页在编译时计算，主题页取成员并集。用途：让页面呈现主题全貌，
-- 也直接是写作时「哪几块还没有材料可写」的清单。
```

`sources` 表扩展（回流防护，见步骤 10）：

```sql
ALTER TABLE sources ADD COLUMN origin         TEXT NOT NULL DEFAULT 'upload';
-- upload（人工上传，既有行为）/ wiki_draft（Wiki 草稿回流导入）
ALTER TABLE sources ADD COLUMN origin_page_id TEXT REFERENCES wiki_pages(page_id);
-- origin='wiki_draft' 时记录草稿的来源页面，用于识别自体祖先关系
```

`wiki_pages.page_type` 的语义随之收紧（不再是「只差 title 提示词」）：

```text
concept  一阶编译产物，输入是 qualifying KP，entry_id 必填且指向
         entries.kind='concept' 的行；
fact     一阶编译产物，输入是 qualifying KP，entry_id 必填且指向
         entries.kind='fact' 的行；与 concept 页共用完全相同的
         编译/发布/重编译/关系派生/二阶接入逻辑，仅 page_type 取值
         与生成措辞提示不同（见步骤 3）；
topic    二阶编译产物，输入是已发布的词条页（concept 或 fact），
         entry_id 恒为 NULL（成员词条经 contains -> 成员页面 ->
         entry_id 反查，反查到的 entries 行 kind 不限）。

服务端在 POST /wiki/compile 处理 page_type=concept|fact 时，校验
请求携带的 entry_id 对应 entries 行的 kind 与请求的 page_type 一致
（kind=concept 对应 page_type=concept，kind=fact 对应
page_type=fact），不一致返回 400——page_type 不是调用方自由指定的
展示标签，必须与 entries.kind 的归属判断保持一致。

存量处理：migration 035 把已有 page_type='topic' 且 entry_id 非空的行
  一次性改写为 page_type='concept'——它们是按旧口径由一阶编译产生的，
  语义上就是概念页，改写不影响正文、索引与依赖字段（这批存量行对应的
  entries.kind 迁移前统一为 concept 默认值，故改写目标固定为
  page_type='concept'，不涉及 fact 分支）。
```

Bleve 新增 `wiki` 索引（使用 `wiki_brain` analyzer）：

```text
写入字段：page_id、title、content、aliases、trigger_questions、entry_id、
         page_type、status
（aliases / trigger_questions 拼接为文本参与分词检索，放宽召回入口；
  误命中由直答阶段的 sufficient 判断兜底，见步骤 4；
  page_type 只用于候选排序，见步骤 8「检索接入」，不参与打分）
概念页与主题页共用同一个索引，字段口径一致。
只索引 status=published 的页面；发布时写入，archived / needs_recompile 时删除。
```

## 实现步骤

### 步骤 1：候选产生（Study 侧，已定义）

Study 报告的 wiki_candidates（recommendation=ready）写入 `learning_results(action=wiki_candidate, status=pending_confirm, object_id=entry_id)`，见 study.md 步骤 6。

### 步骤 2：人工确认与编译触发（分析 → 确认 → 生成）

编译内部拆成两个 LLM 步骤（见 docs/design/wiki.md "编译内部三个阶段、两次 LLM 调用"）：先分析出拟采用的论断结构供人工查看确认，确认后自动接着生成正文。分析产物不落库——它只存在于 `POST /wiki/compile/analyze` 的请求-响应往返中，人工确认时由调用方原样带回，服务端不做任何持久化或过期管理。

```text
POST /wiki/compile/analyze
  请求：{ "entry_id": "...", "page_type": "topic|concept|fact",
          "result_id": "..." }
  处理：
    1. 不改变任何状态（不置 wiki_candidate 为 applied，允许反复调用/重新分析）；
    2. 收集编译输入（见步骤 3「输入收集」），调用分析 Prompt
       （config/prompts/wiki_analyze.md），产出 claims / tensions
       结构（见步骤 3「分析产物」），做与生成阶段相同的
       cited_point_ids 白名单校验（越界剔除并记录 warn）；
    3. LLM 调用失败或校验后 claims 为空 → 500，不返回分析产物。
  响应：{ entry_id, page_type, result_id,
          claims: [{ summary, cited_point_ids, aspect_id? }],
          tensions: [{ description, related_point_ids }],
          readiness?: {...}  # 仅 concept 页，见下「人工指定主题手动编译」}

POST /wiki/compile
  请求：{ "entry_id": "...", "page_type": "topic|concept|fact",
          "result_id": "...",
          "claims": [...], "tensions": [...]  # 可选，见下 }
  处理：
    1. result_id 对应的 pending_confirm wiki_candidate 置 applied
       （confirmed_by=manual）；无 result_id 时允许直接指定 entry_id
       编译（见下「人工指定主题手动编译」，2026-07-31 起是正式支持的第二条
       生成口径，不再只是"调试用途"）；
    2. 同 entry_id 已有非 archived 页面 → 拒绝（409），
       重编译走步骤 5 流程；
    3. 请求体带 claims → 直接作为生成输入（人工确认/微调过的分析产物，
       不再重新调用分析 Prompt）；未带 claims（调试路径或客户端跳过分析
       直接确认）→ 服务端内部按分析步骤的逻辑自动跑一遍，效果与
       "先 analyze 再原样确认"等价，只是省去一次人工中间确认；
    4. 基于 claims 生成正文（见步骤 3「生成产物」），同步执行
       （编译时长可接受，不进异步队列；HTTP 超时上限相应放宽到 120s）；
    5. 成功 → 页面 status=draft，写首条 wiki_revisions；
       compiled_from：有 result_id 时存 [result_id]（同现状）；
       无 result_id（人工手动触发）时存 ["manual_trigger"]（而不是空数组），
       供以后区分页面来源（不新增列，复用既有字段）。
  响应：{ page_id, status: "draft", title }
```

**人工指定主题手动编译（第二条生成口径，复用同一条编译链路）**：Wiki 页面
一直有两条并行的产生方式——Study 定期扫描、达到"广度/related 连接/
contradicts 不反客为主/活跃天数/内聚度"五项 ready 判定后自动写
`wiki_candidate` learning_result（步骤 3 描述的口径）；以及人工不等 Study
推荐、直接从概念列表挑一个概念、原样调用上面同一组 `POST /wiki/compile/
analyze` + `POST /wiki/compile` 触发编译——两者是同一条 analyze→confirm→
generate 链路的两个入口，**不是两套生成逻辑**，`result_id` 是否为空是唯一的
分支点：

```text
唯一硬门槛（不可绕过）：该 concept 至少要有 1 条 qualifying KP——没有材料
  就没有页面，这条本来就是 analyze 阶段的既有校验（gatherAnalyzeInputs 在
  调 LLM 之前就检查）。

  qualifying 的定义按触发来源分两档（2026-08-07 修订，取代此前"两条口径
  统一要求 verified"的说法；2026-08-12 修订 Study 推荐一档，取代
  2026-08-11 曾加过的 wiki_material_confirm 关卡，见下）：
    - Study 推荐（result_id 非空）：qualifying = lifecycle=current 且已有
      verified 的 ActivationLink，仅此一条，不再有第二道人工确认关卡
      （2026-08-12 修订，取代 2026-08-11 定案：曾要求该 point_id 额外
      存在 applied 状态的 wiki_material_confirm；该关卡整体废弃，见
      `docs/design/wiki.md`「2026-08-12 改判」）。废弃理由：在还不知道
      某个 KP 最终会被哪个 Wiki 主题使用的前提下，人工看着一条孤立的 KP
      判断"值不值得沉淀"是个伪命题——脱离主题语境，人并没有比程序更多
      的信息可用于判断；真正能做出"这批材料够不够格立传"这个判断的时机
      是 Wiki 编译时，那时主题范围已经确定，编译时本来就有的整体判断
      （广度/连贯/稳定，见步骤 8）自然回答了这个问题，不需要在候选阶段
      单独再问一遍。ActivationLink 的 candidate→verified 晋升仍是默认
      自动（`study.auto_promote` 默认 true，见 activation.md），verified
      的含义因此重新收拢为"这条路径够格走 Retrieval 快路径，也够格作为
      Wiki 材料"——两件事不再分开判断，`study.qualifying_confirm_success_min`/
      `qualifying_confirm_distinct_min` 两个配置项随关卡一并废弃。
    - 人工手动触发（result_id 为空）：qualifying 只要求 lifecycle=current
      （+ 已归属该 concept），不要求 verified——口径与步骤 8「候选范围
      检索」的主题范围材料一致。理由：人工手动指定本身就是一次显式确认
      动作，材料是否已被真实使用验证是概念页发布正式化前才需要回答的
      问题，不应该挡在"能不能生成草稿"这一步；否则新入库、还没有真实
      使用积累的材料永远无法通过人工手动编译走到草稿阶段（详见分步向导
      一节，2026-08-07 新增，`docs/impl/v1/wiki.md` 步骤 8「分步向导」）。
      该口径只影响 analyze/compile 阶段"能不能生成"，**不影响** publish
      阶段既有的 selfcheck 质量核验（引用支持度等）——生成草稿容易，
      正式发布的质量门槛不因此降低。

Study 的五项 ready 判定（广度/related/contradicts/活跃天数/内聚度）在人工
  触发时改为仅展示、不阻断：POST /wiki/compile/analyze 的响应新增可选字段
  readiness = { qualifying_kp_count, related_connection_count,
  contradicts_connection_count, days_active, days_active_min, cohesion,
  cohesion_min }——五项信号照样算出来给人看，但没有 recommendation 判定，
  人工自己判断是否"够格"，不由系统替人工做决定（同一份字段两条口径下都会
  返回，Study 推荐的候选一样能看到）。

readiness 的 cohesion 与 Study 侧 Stats.Cohesion 不保证数值完全一致：Study
  的内聚度只用 KPN 关系 + 共现两路信号计算；这里直接复用编译本来就要算的
  切面聚类结果（阶段 B，intent/unit 信号更全，且经过 split/merge 后处理）。
  两者都是"这批 KP 是否围绕同一件事"的合理度量，只是分别服务于阻断判定和
  参考展示两种不同用途，允许有出入。

来源留痕：compiled_from 字段区分"Study 推荐"（存 result_id）与"人工手动
  触发"（存哨兵值 "manual_trigger"），不新增列、不新增表。
```

> **2026-08-13 编注（随 `docs/design/activation-convergence.md` 的连续置信度设计一并同步，措辞取代不重写以上历史段落）**：以上「唯一硬门槛」段落把 Study 推荐路径的 qualifying 判据写成"已有 verified 的 ActivationLink"。`activation.md`「状态机」已经把这个判定从离散的 `candidate → verified` 跳变改写为连续置信度——`status=verified` 现在是"该 KP 对应链接下至少一条观测条件的 `mean(cond)` 已经越过 `serving_confidence_min`（即该条件的服务分档 tier ∈ {self_graded, trusted}）"这一持续判断的派生/缓存结果，不再是一次单独的晋升动作。这段编注只是把 qualifying 判据的表述方式同步成"current 且置信度已达到服务门槛"，不改变判据本身——`status=verified` 这个标签依然是唯一要检查的字段（下方步骤 3「输入收集」直接读 `status=verified`，实现上不需要改成重新计算 mean，因为「状态机」一节已经保证这个派生字段在写入时实时同步），本条编注要交代的只是这个标签现在的产生方式变了，qualifying 的门槛位置、有没有第二道人工确认关卡等实质结论完全不受影响。

### 步骤 3：编译输入与 Prompt

**输入收集**：

```text
qualifying KP（Study 推荐路径，result_id 非空；人工手动触发路径见步骤 2
  「唯一硬门槛」，只要求 lifecycle=current，不适用本段）：该 concept 下
  同时满足以下条件的 KP（与 Study 候选口径一致，见 docs/design/wiki.md
  "ActivationLink 回答'这条管不管用'，Wiki 编译回答'这个主题够不够格
  立传'"）：
    lifecycle=current（KP 与所属 KU）；
    该 KP 存在对应 ActivationLink 且 status=verified
      （2026-08-12 修订，取代 2026-08-11 定案：此前还额外要求该
      point_id 存在 applied 状态的 wiki_material_confirm，该人工确认
      关卡已整体废弃，见步骤 2「唯一硬门槛」段说明；verified 单独
      即为准入判据，candidate/weakened/deprecated 状态的 KP 不计入
      qualifying）；2026-08-13 编注：`status=verified` 的读取方式不变
      （仍是查 `activation_links.status` 字段本身，不是现算 mean），
      只是这个字段现在是 `activation.md`「状态机」定义的连续置信度
      派生结果，见步骤 2 末尾编注；
  confident_count 仍会取出（MAX(lc.confident_count)），但只作素材
  排序/展示用途（见下），不再是准入条件；
KU 正文：qualifying KP 所属 KU 按行号切片（单页输入合计 ≤
  wiki.compile_max_chars，默认 12000 rune，超出按 confident_count
  降序截取 KP——排序权重，不是门槛）；
KPN 关系：qualifying KP 之间的 relations（含 cross）；
knowledge_gaps：question_terms 与该 concept 名称/KP 内容有词项重合的
  gap 条目（作为"待验证点"素材）；
真实观测问法（docs/design/wiki.md "触发问法取材真实观测，检索
  匹配复用四元组"）：对每个 qualifying KP 的 point_id，查
  retrieval_quality='confident' 且 direct_point_ids 含该 point_id 的
  traces.question（同 study.md ConfidentTraceQuadruples 的查询口径，只是
  多取 question 原文字段），跨 KP 去重、按 point_id 打散取样，
  上限 wiki.trigger_questions_max（默认 10）条，作为生成
  trigger_questions 的素材——LLM 从这些真实问法里挑选/归纳，而不是凭
  materials 自由想象；某 KP 无确证 trace（理论上不会发生，qualifying 已
  要求 verified 链接，但防御性处理）时跳过，不影响整体编译。
```

> **熟路指针（2026-08-11 设计层面提出，非实现变更；2026-08-12 随 qualifying 口径改判更新措辞，结论不变）**：上面 qualifying 现在的可靠性判据只剩 verified ActivationLink 一道关卡（人工确认的 wiki_material_confirm 已整体废弃，见步骤 2），仍然天然受制于「同一问法需要被精确重新命中」——措辞抖动一样会让实际常用的材料迟迟攒不够 verified 这道门槛的窗口统计。`docs/design/activation-bundle.md` 提出熟路的稳定核（一组知识点在真实问答里反复一起被依赖的组合）可以作为独立于单点 verified 之外的另一条可靠性证据，但**是否放宽、放宽到什么口径**（例如替代 verified 这道门槛，还是只作为 Wiki 编译时的参考信息展示），以及具体阈值，都还没有定案，需要用户先确认；本文档在此仅记录设计意图，qualifying 的实际判据（verified ActivationLink，仅此一条）保持不变，不要在实现时自行按熟路口径放宽这道关卡。
>
> **2026-08-13 附注**：以上"同一问法需要被精确重新命中"这条风险描述不变——`activation.md`「状态机」的连续置信度机制仍然要求 Match 精确匹配到具体的观测条件才能给该条件记一次证据，只是判定"够不够格"的方式从离散跳变改成了连续分数，措辞抖动导致证据分散到多条互不相认的条件、每条都攒不够置信度，这个风险原样保留，不因本次改写而减轻。熟路是否能补上这块缺口，仍是「待确认」，本条编注不代为决定。

**词条级 ready 判定**（概念、事实通用同一套判据，不区分 kind；Study 侧计算，决定是否写 wiki_candidate，见步骤 1；
对应 docs/design/wiki.md 拆出的三件事：广度与连贯、稳定，
可靠性已由上面的 qualifying KP 定义单独回答）：

```text
广度：qualifying_kp_count ≥ study.wiki_kp_min（同既有口径，不下调）；

连贯（口径修订——区分连接性质，不再只看数量）：
  related_connection_count ≥ 1（qualifying KP 之间 relation_type=related
    的连接数，即"这批知识彼此印证、互补"这件事本身要成立）；
  contradicts_connection_count < related_connection_count（矛盾类连接
    不能反客为主——不要求零冲突，冲突本就该如实呈现在页面"待验证点"里，
    只要求这批知识以互相印证为主导面貌，而不是矛盾占主导）；
  （**熟路指针**，2026-08-11，设计层面，非实现变更：related_connection_count
  目前只数 KPN related 边——这是内容层面的信号（这批 KP 说的是不是一回事），
  跟有没有人一起问过无关。`docs/design/activation-bundle.md` 的熟路稳定核
  是另一种独立证据（这批 KP 有没有被同一类真实问题一起依赖过），预期可以
  作为 KPN 之外的**另一条**连接来源并入这个计数——两条来源任一成立都算
  一条"相关"，不是拿熟路替换 KPN：真实问题大多只问一个切面，entry 下的
  KP 很少整体被同一批问题共同覆盖，如果连贯只认熟路会漏判大量内容上完全
  自洽、只是还没被问全的概念，KPN 在这里恰恰是在补熟路覆盖不到的地方。
  是否并入、具体怎么算一条"熟路来源的连接"，尚未定案，本次修订不改动
  上面的判据，只记录这个接入点——同样的思路已经在下面「内聚」判定里
  落地了一部分，可以参照）；

稳定：qualifying KP 关联的激活事件覆盖 days_active ≥
  wiki.qualifying_min_days_active（衡量"这批理解经受住了时间考验"的
  时间跨度，不是"被问得勤"的频率——沿用已有 DaysActive 计算口径）；

内聚（P1 新增第五项，见 docs/impl/v1/wiki-generation.md 2.2/2.4、
  docs/design/wiki.md "连贯性判断还需要第三层"，已实现）：
  qualifying KP 的 Louvain 社区检测（`internal/foundation/graph`，边权
  = KPN related/contradicts 关系 + 共享 confident 问题共现，contradicts
  同样计正权）最大社区占比 ≥ wiki.entry_cohesion_min；
  不达标时（前四项均满足但内聚不满足）不写 wiki_candidate，改为在学习
  报告 entry_split_signals 节记录各簇成员与建议名，供人工判断是否需要
  拆分概念（不建 entry_candidates(kind=split) 行，split 候选仍属 V3，
  详见 concept-evolution.md）；wiki.entry_cohesion_min ≤ 0 时该项恒真
  （门禁关闭，仅 Stats.Cohesion 展示，不影响 recommendation）。
  （**熟路指针**，2026-08-11，设计层面，非实现变更：这里的边权已经在混合
  "KPN 关系 + 共享 confident 问题共现"——后者本质上就是熟路想要形式化
  的同一种信号（现在是临时统计的共现次数，不是复用一个沉淀下来的对象），
  内聚判定其实已经是上面「连贯」那条熟路指针描述的模式的一个先例。
  Bundle 落地后，这里预期可以直接复用某个 entry 范围内已显影的熟路
  稳定核作为边权来源之一，替代现在临时统计共现的那部分计算，不用改
  Louvain 社区检测本身的逻辑。是否替换、替换后边权系数要不要重新校准，
  尚未定案，本次修订不改动上面的判据）；

以上五项同时满足 → ready，否则 needs_more_data（其中前四项满足、仅内聚
不满足时额外写 entry_split_signals）。
```

**分析产物**（步骤 2 `POST /wiki/compile/analyze` 的输出，不落库）：

```json
{ "claims": [{ "summary": "该论断的核心意思（要点，非最终措辞）",
               "cited_point_ids": ["..."] }],
  "tensions": [{ "description": "材料之间的张力或未决问题",
                 "related_point_ids": ["..."] }] }
```

**Prompt 文件（分析）**：`config/prompts/wiki_analyze.md`

```
根据以下知识点和原文材料，分析这个{{entry_kind_label}}/主题值得沉淀为
Wiki 页面的论断结构，不要写最终正文。

{{entry_kind_hint}}

要求：
1. 只使用提供的材料，不引入材料之外的信息；
2. 每条 claim 是一个独立的稳定结论要点，标注其依据的 point_id
   （只能使用材料中出现的 point_id）；
3. 材料之间存在张力、或 gap 列表非空且与该{{entry_kind_label}}相关时，
   写入 tensions，不要在这一步强行调和或替换为某个 claim。

{{entry_kind_label}}：{{entry_name}}（{{entry_description}}）
知识点与原文材料：
{{materials}}
相关知识缺口：
{{gaps}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

`{{entry_kind_label}}` 与 `{{entry_kind_hint}}` 由 page_type 决定
（page_type=topic 时不使用本 Prompt，走 wiki_topic_analyze.md）：

```text
page_type=concept：entry_kind_label="概念"；entry_kind_hint 提示
  "这是一个概念页，围绕通用规律/原理/规则组织论断，claim 应回答
  '这个概念的定义、边界、内部逻辑是什么'，不要陷入某一个具体实现
  的细节"；
page_type=fact：entry_kind_label="事实"；entry_kind_hint 提示
  "这是一个事实页，围绕这个具体、可唯一指认的对象组织论断，claim
  应回答'这是什么、当前状态如何、和哪些概念或其他事实存在关联'，
  不要泛化成脱离这个具体对象的通用规律"。
```

**分析后校验**（程序执行，同 citation 校验思想）：

```text
claims[].cited_point_ids / tensions[].related_point_ids ⊆ 输入 KP 集合，
  越界的剔除并记录 warn；
claims 为空 → 视为分析失败，按 LLM 失败处理（重试一次，仍失败返回 500，
  不返回分析产物）；tensions 允许为空。
```

**生成输入**：人工确认（可能微调）后的 claims / tensions（若跳过分析，见
步骤 2「未带 claims」分支，服务端补一次分析后代入），加上 claims 中
cited_point_ids 并集反查得到的 KU 正文切片——生成阶段的引用范围收窄到
这份并集，而不是完整 qualifying KP 集合。

**Prompt 文件（生成）**：`config/prompts/wiki_compile.md`

```
根据以下已确认的论断结构和原文材料，把它组织成一个{{entry_kind_label}}的
Wiki 页面正文。

{{entry_kind_hint}}

要求：
1. 只使用提供的材料和论断结构，不引入材料之外的信息，
   不得引用 claims / tensions 之外的 point_id；
2. 页面结构固定为四节：## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源
   （此结构已被 `docs/impl/v1/wiki-generation.md` 阶段 F 修订为五节，
   新增 `## 摘要`；本节保留供步骤 2 分析阶段字段参考，正文小节数以
   `wiki-generation.md` 第 6.1 节 / 冲突清单为准；概念页与事实页共用
   同一套小节结构，事实页"稳定结论"聚焦"这是什么/当前状态/关联对象"，
   "展开说明"承载版本、别名等事实专属细节，不新增小节）；
3. 稳定结论逐条对应输入的 claims，每条论断末尾以 [point_id] 标注该
   claim 已确认的 cited_point_ids；
4. tensions 非空时写入"待验证点"，不要强行调和；
5. "依赖来源"列出所用知识点所属的知识单元主题；
6. 额外输出检索触发信息：aliases（该{{entry_kind_label}}的别名、缩写、
   常见口语叫法或曾用名——事实页的旧称/俗称降级为别名正落在这个字段）
   与 trigger_questions（这个页面能够直接回答的 5-10 个典型问法）——
   trigger_questions 应从下方"真实观测问法"里挑选/归纳，不要凭材料
   臆造未出现过的表达方式；真实问法不足 5 条时才允许适度归纳补充，
   补充部分也要贴近已观测问法的措辞。

{{entry_kind_label}}：{{entry_name}}（{{entry_description}}）
已确认的论断结构：
{{claims}}
{{tensions}}
真实观测问法（trigger_questions 生成素材）：
{{observed_questions}}
知识点与原文材料：
{{materials}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

示例 JSON：

```json
{ "title": "页面标题", "content": "Markdown 正文", "cited_point_ids": ["..."],
  "aliases": ["..."], "trigger_questions": ["..."] }
```

**生成后校验**（程序执行，同 citation 校验思想）：

```text
cited_point_ids ⊆ claims 已确认的 cited_point_ids 并集（比步骤 3 的完整
  qualifying KP 集合更窄），越界的剔除并记录 warn；
content 中的 [point_id] 标注逐一提取，不在白名单内的替换为删除并 warn；
content 非空且包含四个固定小节标题，缺节 → 编译失败按 LLM 失败处理
  （重试一次，仍失败返回 500，不产生页面）；
aliases / trigger_questions 为空或缺失不算失败（记录 warn，存空数组）——
  它们只影响召回宽度，不影响页面正确性；条数超过 wiki.trigger_questions_max
  截断保留前 N 条。重编译时随正文一起重新生成、整体覆盖。
校验通过后：source_point_ids = content 中实际引用的 point_id 并集，
source_unit_ids 反查填入，source_link_ids = 这些 point_id 中 status=verified
的 activation_links.link_id 集合（无对应 verified 链接的 point_id 不计入，
查询失败降级为空数组并记录 warn，不影响编译成功）；
uncovered_points（覆盖度显式化，两层架构扩展）= 该 concept 下
  lifecycle=current 但不满足 qualifying 条件（即无 verified ActivationLink，
  2026-08-12 修订：wiki_material_confirm 关卡已废弃，口径同步骤 3）的 KP
  清单 [{ point_id, summary }]，编译与重编译时整体重算；
  **不进正文、不进 citation 白名单、不参与任何门槛判定**——三道闸门
  一条不动，它只是让页面能呈现主题全貌，并直接充当写作时「哪几块还
  没有材料可写」的清单（Page 视图单独展示，见 page.md）；
  查询失败降级为空数组并记录 warn，不影响编译成功；
observed_conditions = source_link_ids 对应链接的 observed_conditions 并集
（按四元组去重合并，同 activation 模块 AppendObservedCondition 的去重口径；
查询失败降级为空数组并记录 warn，不影响编译成功，此时该页面暂时只有
词法/概念两条直答入口，四元组入口空转直到下次重编译补上）。
```

**调用参数**：reasoning 模型（页面编译是 V1 唯一的长文生成任务），temperature 0.3，记录 prompt_version / model_name。

`page_type=concept`/`fact` 与 `topic` 的差别在 title 生成提示（concept/fact 页以概念/事实名为题，topic 页允许模型按材料聚合主题命名）；`concept` 与 `fact` 之间额外由 `{{entry_kind_label}}`/`{{entry_kind_hint}}` 区分生成措辞（见上）。三者共用 `wiki_analyze.md` / `wiki_compile.md` 同一份文件（topic 页走独立的 `wiki_topic_analyze.md`/`wiki_topic_compile.md`，见步骤 8），变量按 page_type 取值切换，不拆多份文件。

### 步骤 4：发布与检索接入

> **P0 修订（`docs/impl/v1/wiki-generation.md` 阶段 E/G，已实现）**：
> aliases/trigger_questions 不再由 LLM 生成，改为程序查
> `subject_synonyms`（别名）与真实 confident trace 问法取样（触发问法），
> `wiki_compile.md` 已不再要求这两项输出；编译后新增支持度核验
> （`wiki.claim_verify_enabled`，结果写 `wiki_claim_checks`，不阻断编译，
> 只阻断发布）；publish 前新增质量门（`wiki.selfcheck_enabled`，回放该页
> 真实 confident 问法验证 sufficient 率/材料利用率/无引用句比例/支持度核验，
> 结果写 `wiki_quality_checks`），未过门槛返回 409，人工可带 `force=true`
> 覆盖并留痕；新增 `POST /wiki/pages/:id/selfcheck` 单独触发质量回放。
> 两项开关默认值见 `config.yml` wiki 节，零值配置（含全部既有测试）行为
> 不变。详见 wiki-generation.md 阶段 E/G、第 12 节冲突清单。

POST /wiki/pages/:id/publish
  仅对 draft / needs_recompile 生效；status=published、
  published_at=now()、写入 wiki index。
  请求可选 { "force": bool }——质量门未过时不带 force 返回 409。
  响应：{ page_id, status: "published" }

检索接入（retrieval.md 第 0 层）——直答候选采集，三个入口，均不调 LLM
（docs/design/wiki.md "触发问法取材真实观测，检索匹配复用
四元组"）：

  a. 词法入口：对问题分词后查询 wiki index（title/content/aliases/
     trigger_questions 均参与打分；TermQuery status=published 已由索引
     写入策略保证），取分数 ≥ retrieval.wiki_min_score 的页面按分数降序；
  b. 概念入口：问题分词结果与 entries 表的概念名称做词法匹配
     （精确/包含，不调 LLM），命中概念存在 published 页面
     （wiki_pages.entry_id）→ 该页面直接进入候选，不看 Bleve 分数；
  c. 四元组入口（新增）：Session 已解析出的 qc.Subject/Intent/Audience/
     Constraint（问题经 Session 补全后才进入检索总流程，第 0 层和第 1 层
     共用同一次解析结果，见 retrieval.md 检索总流程），对每个 published
     页面的 observed_conditions 跑与 activation.Matcher.Match 相同的
     conditionGroupMatches（需要把该函数从 activation 包内部提升为可被
     wiki 包调用——迁移到共享位置或导出，具体做法留给实现时决定）；
     命中即入候选。qc.Subject/Intent 均为空时（未经 /session/turn 解析、
     直连 POST /answer 的调用路径）本入口直接空转，退化为 a/b 两条入口，
     不需要额外判断或报错。

  三入口合并去重与优先级：四元组命中（对已验证复现场景的精确匹配）排最
  前，其后是词法命中（按分数降序），仅概念命中的页面追加在最后；
  **命中的主题页不进直答序列，而是就地展开为其 contains 成员概念页**
  （两层架构，见步骤 8「检索接入」）——主题页是召回骨架，不是直答单元；
  截取前 retrieval.wiki_max_candidates 个（默认 3）作为直答候选序列。

  直答尝试（按候选顺序逐个执行，最多 wiki_max_candidates 次）：

  Prompt 文件：config/prompts/answer_wiki.md
    输入：question + 页面 title/content；
    要求：只依据页面内容回答，引用页面中的 [point_id] 标注作为 citations；
    输出：{ "content": "...", "citations": ["point_id..."], "sufficient": true|false }

  sufficient=false（该页面覆盖不了此问题）→ 尝试下一候选页面；
    全部候选耗尽仍 false → 回落激活层/慢路径继续；
  sufficient=true → 停止尝试，组装 AnswerResult：path_type=wiki，
    citations 经该页面 source_point_ids 白名单校验，
    evidence_snapshot 记录 { wiki_page_id, cited_point_ids }
    （point_id 可继续反查 KU 与 source_ref，证据回链完整）；
  Wiki 直答典型 1 次 LLM 调用，最坏 wiki_max_candidates 次
    （召回加宽后"正确页面排第二被首名挡住"是主要漏答模式，
    多试代价是轻量调用，漏答代价是整条慢路径，取前者）；
  trace 照常写入（path_type=wiki），不产生激活类事件（见 trace.md 步骤 3）。

  设计取向（对应本方案的准确率分工）：路由层（trigger/概念入口）只负责
  召回宽度，允许误召；准确率由三道既有闸门保证——编译输入的 qualifying KP
  口径（事实层）、sufficient 弃权判断（覆盖层）、citation 白名单（回链层）。
  放宽召回不得以削弱任何一道闸门为交换。
```

### 步骤 4a：综合满意度轴（synthesis satisfaction，2026-08-13 新增）

`docs/design/activation-convergence.md` 第 7 节把 Wiki 页面的不确定性拆成两根轴：**触发轴**（这个问题该不该落在这一页）复用页面自己的 `observed_conditions`，走与 ActivationLink 完全同一套 `activation.md` 置信度机制，上面步骤 3「输入收集」2026-08-13 编注已经交代过 qualifying 判据本身；**Wiki 独有的轴**——"这次综合改写有没有真的把问题说清楚"——材料底下引用的知识点即使全部验证有效，页面本身的组织、详略、表达角度仍可能没答到点子上，这是本步骤要补的信号。

**Schema（`wiki_pages` 新增列，与 `activation_links.observed_conditions` 内每条条件的成功/失败计数同一套设计，只是落在页面粒度）**：

```sql
ALTER TABLE wiki_pages ADD COLUMN synthesis_success_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_audited_success_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_audited_failure_count INTEGER NOT NULL DEFAULT 0;
-- 四列命名、含义、mean 公式与 activation_links 观测条件的
-- success_count/failure_count/audited_success_count/audited_failure_count
-- 逐字对应（见 activation.md「数据结构」「状态机」），选择落在 wiki_pages
-- 表而不是新开一张表：页面是这根轴唯一的粒度（不像 ActivationLink 一条
-- 链接下有多条独立的观测条件），不需要额外的一对多结构，四个整数列足够，
-- 同 wiki_pages 现有 adopt_count 风格的计数列放在一起最省心。
```

```text
mean(page) = (synthesis_success_count+1) /
             (synthesis_success_count+synthesis_failure_count+2)
-- 与 activation.md 的 mean(cond) 同一公式，新页面（0/0）mean=0.5，
-- 不预设"新写的页面默认可信"或"默认不可信"。
```

**证据来源与写入时机（复用 Retrieval 的独立核实/审计抽样机制，不为 Wiki 另造一套，见 retrieval.md 步骤 2c）**：

```text
每次 Wiki 直答成功服务（path_type=wiki，某候选页面 sufficient=true 并
  被采用，见步骤 4「直答尝试」）后，按 wiki.synthesis_audit_rate（配置，
  建议默认低概率，同 activation.md 的 explore_rate_trusted 量级——这是
  定期复查、不是持续验证）抽样：

  中选 → 触发与 retrieval.md 步骤 2c **完全同一套编排**：在已经把 Wiki
    直答返回给用户之后，异步另起一次不阻塞当前请求的独立慢路径检索
    （绕开 Wiki 直答层与激活层，直接从 Domain 预过滤开始走 MVP 完整
    链路），得到一份独立的 direct_point_ids；

  比对规则（复用 trace.md 步骤 3b 的比对语言，对象换成页面而非链接）：
    独立慢路径的 direct_point_ids 与该页面的 source_point_ids 交集非空
      （即独立算出来的证据落在这一页覆盖的知识范围内）
      → 写入 wiki_synthesis_audit_success 事件，agree=true；
        synthesis_audited_success_count++ 且 synthesis_success_count++；
    交集为空（独立慢路径认为这个问题该用的证据，这一页根本没覆盖到
      ——不是"页面写得不够好"，是"页面回答的其实是另一批知识"，
      同样计入 synthesis 失败：综合满意度问的正是"这次直答是否真的
      解决了问题"，证据都对不上号，答案不可能对得上号）
      → 写入 wiki_synthesis_audit_failure 事件，agree=false，
        reason="point_not_in_page_scope"；
        synthesis_audited_failure_count++ 且 synthesis_failure_count++；

  未中选 → 不产生任何 synthesis 事件，本次服务不计入 synthesis 计数
    （同 activation.md 的 exploring/self_graded/trusted 三档只对被抽中
    的样本计数一致——本设计对 Wiki 综合满意度不单独实现"未审计也计入
    自证成功"的自评分支：design.md 第 7 节强调 Wiki 的自证风险比
    ActivationLink 更高——内容是提前写好的成品，不是每次现场引用证据
    拼出来的，写完之后哪怕底下知识点都还有效，综合表达本身有没有可能
    已经不准，页面自己回答不了这个问题；因此 synthesis_success_count/
    synthesis_failure_count 只由独立核实产生，不接受"没被审计到、但看起
    来直答成功了"这种自证信号单独推高，避免重蹈"自己给自己批卷"的覆辙，
    见 activation-convergence.md 第 2 节）。

事件 payload（结构与 trace.md 的 activation_audit_success/failure 对称）：

// wiki_synthesis_audit_success
{ "page_id": "...", "audited_trace_id": "...",
  "slow_path_direct_point_ids": ["..."], "agree": true }

// wiki_synthesis_audit_failure
{ "page_id": "...", "audited_trace_id": "...",
  "slow_path_direct_point_ids": ["..."], "agree": false,
  "reason": "point_not_in_page_scope" }

调用方与失败处理：与 retrieval.md 步骤 2c / trace.md 步骤 3b 同构——
  Retrieval 触发独立慢路径检索并把结果交给 Trace，Trace 写事件并更新
  wiki_pages 的四个计数列（更新方法命名建议 wiki.RecordSynthesisOutcome，
  与 activation.RecordAuditOutcome 对称）；独立慢路径检索本身失败（超时、
  异常）记 warn、不产生事件，不影响已经返回给用户的这次 Wiki 直答。
```

**mean(page) 的消费方式**：只进页面详情的可观测数据（见 page.md 关于置信度/分档展示的扩展）与 Study 报告，**不驱动任何自动动作**——mean(page) 低不会自动触发 `needs_recompile`（这条硬边界详见步骤 5「重编译」开头的编注）、不会自动下线页面、不会跳过 selfcheck。它回答的是"这一页最近是不是经常被独立核实认为文不对题"，是给人看的信号，不是新的自动判据。

### 步骤 5：重编译

> **重编译依然 100% 人工触发（2026-08-13 明确重申，不因本次信号变精细而改变）**：本文档以下描述的两个来源都只是把页面标记为 `needs_recompile`，标记本身是自动的，但从 `needs_recompile` 到实际重新生成内容，永远需要人工调用 `POST /wiki/pages/:id/recompile` 确认——这条边界见 `CLAUDE.md`「Wiki 编译不是全自动的」，本次新增的综合满意度轴（步骤 4a）同样不改变这条边界：`mean(page)` 再低，也只是让人工在待办列表里看到"这一页最近常被独立核实认为文不对题"这个信号，不会自己触发 `needs_recompile`，更不会自己触发实际重编译。`docs/design/activation-convergence.md` 第 7 节原文同样明确"重新编译，依然是人工才能按下的按钮，这条不变"——这套设计改变的只是喂给人的信号有多准，不改变谁来按按钮。

```text
标记（两个来源，最终都调 MarkNeedsRecompile(pageID, reason)）：
  a. lifecycle 传导：SetUnitLifecycle 执行后，扫描 published 页面的
     source_point_ids 含受影响 KP → needs_recompile（见 lifecycle.md 步骤 4）；
  b. Study 周期扫描：published 页面的依赖 KP 出现新增 qualifying KP
     （数量比编译时增加 ≥ wiki.recompile_new_kp_min，默认 2）
     → needs_recompile + learning_results(action=recompile_flag)；
  c. 成员页面变化传导（两层架构，见步骤 9）：概念页进入 needs_recompile
     或 archived → 经 contains 反查其父主题页 → needs_recompile
     （reason=member_page_changed）；不再向上传导（只有两层）；
  d. verified 传导（2026-08-07 新增）：ActivationLink 状态转为 verified
     （TransitionLink 唯一入口，覆盖人工确认 / Study 自动 promote / Study
     reverify 三种触发来源）时，若其 point_id 被某已发布页面的
     source_point_ids 引用 → needs_recompile（reason=link_verified）；
     解决 observed_conditions（确证问法）编译后不随验证结果自动刷新的问题
     （见 docs/design/wiki-compilation.md「触发问法取材真实观测」）；
     activation → wiki 的跨模块通知走 WikiNotifier 接口（同 unit 模块已有的
     WikiNotifier 惯例），wiki 侧实现 NotifyLinkVerified(pointID)，全表扫描
     published 页面按 source_point_ids 命中；

     > **2026-08-13 编注**：以上"ActivationLink 状态转为 verified（TransitionLink
     > 唯一入口）"的措辞是离散状态机时代的表述——`TransitionLink` 已随
     > `activation.md`「状态机」的改写移除，`verified` 现在是每条观测条件
     > 置信度持续越过服务门槛后的实时派生结果，不再有一次单独的、可供挂钩子
     > 的"转为 verified"事件。通知时机相应改为：`RecordOutcome`/
     > `RecordAuditOutcome` 写入新计数后，若该链接的派生 `status` 由非
     > verified 变为 verified（即本次调用是这条链接"新晋出现至少一条
     > tier∈{self_graded,trusted} 条件"的那一次，需要在 activation 模块内
     > 对比写入前后的派生 status 是否跨越这条边界），才触发
     > `NotifyLinkVerified(pointID)`——效果与原先"监听一次跳变事件"等价
     > （只在真正首次越过门槛时通知一次，不会每次分数微调都重复触发），
     > 只是触发点从"状态机跳变的那次方法调用"改成"派生 status 计算结果
     > 相比调用前发生了变化"，具体判断留给 activation.md 实现时确认（该
     > 文档「与 Study 的分工变化」未展开这一细节，本条编注只指出触发时机
     > 需要同步调整，不代为决定 activation 包内部的具体比对写法）。

MarkNeedsRecompile：status=needs_recompile、从 wiki index 删除
  （旧结论可能失效，宁可回落慢路径也不用可疑页面直答）、记录原因日志；

重编译执行：人工在 Page 确认后 POST /wiki/pages/:id/recompile →
  重新收集输入（口径同步骤 3，qualifying KP 按当前数据重算）→
  内部按步骤 2/3 的分析→生成两步执行（不额外暴露 analyze 预览接口，
  人工确认"重编译"这个动作本身即视为对新一轮分析结果的确认）→
  校验 → 写新 wiki_revisions（reason 含触发来源）→ status=draft，
  等待再次 publish。每次编译均可经 compiled_from / revisions
  追溯到触发它的 learning_result 或 lifecycle 变更。
```

### 步骤 6：HTTP API 汇总（含步骤 7-10 的扩展端点）

> 步骤 1-6 是一阶编译（概念页）的最小闭环，已实现；步骤 7-10 是两层架构扩展。
> 步骤编号不重排——既有代码与文档已按 1-6 交叉引用（`internal/wiki/*.go`、
> `trace.md`、`retrieval.md`、`study.md`、`lifecycle.md`）。

```text
POST /wiki/compile/analyze        步骤 2，不落库、不改变任何状态
POST /wiki/compile               步骤 2
POST /wiki/pages/:id/publish           步骤 4
POST /wiki/pages/:id/recompile         步骤 5
POST /wiki/pages/:id/archive           status=archived，从索引删除
GET  /wiki/pages                 查询参数：status、entry_id、limit
                                 响应：[{ page_id, page_type, title, status,
                                          entry_id, compiled_at, published_at }]
GET  /wiki/catalog               按知识领域分组的 Wiki 目录（Page 模块 Wiki 视图）
                                 响应：[{ domain_id, name, description, wiki_count,
                                          pages: [{ kind, page_id?, page_type?,
                                            entry_id?, result_id?, title,
                                            description, status, updated_at? }] }]
                                 status ∈ pending_compile|draft|needs_recompile|
                                   published|archived；主题页按成员概念领域多归属
GET  /wiki/pages/:id             完整字段 + revisions 元信息列表
                                 （主题页附 contains 成员列表及其 status）
GET  /wiki/pages/:id/revisions/:rev  单版本正文

两层架构扩展（步骤 7-10）：
GET  /wiki/pages/:id/relations   该页的 related / contradicts / contains 关系
                                 响应：[{ relation_type, other_page_id, title,
                                          derived_from, evidence }]
POST /wiki/topics                步骤 8，人工指定主题范围创建主题页壳
                                 （2026-08-03 修订：给范围，不再要求给
                                   已发布成员页面 id 列表）
                                 请求：{ topic_name, topic_description,
                                          domain_id?: "..." }
                                 响应：{ page_id, status, title, member_page_ids,
                                          uncovered_entries,
                                          readiness?: { member_count,
                                            related_connection_count,
                                            contradicts_connection_count,
                                            reliability_ratio,
                                            reliability_min } }
GET  /wiki/topics                主题页列表（含未编译壳页）
POST /wiki/pages/:id/topic/analyze    步骤 8，二阶编译的分析步骤，不落库
POST /wiki/pages/:id/topic/compile    步骤 8，二阶编译
POST /wiki/pages/:id/drafts      步骤 10，派生写作草稿
                                 请求：{ mode: "page"|"assembled" }
                                 （主题页默认 assembled：组装成员页正文
                                   + 只读证据清单）
GET  /wiki/drafts                查询参数：page_id、limit
GET  /wiki/drafts/:id            含 stale 标记（source_revision_id 是否为
                                 该页最新版本）
PATCH /wiki/drafts/:id           更新 title / content / note，不做任何校验
DELETE /wiki/drafts/:id
```

主题页的 publish / archive / 列表 / 详情复用既有端点，无需新增。
`POST /wiki/pages/:id/recompile` 对主题页生效时走步骤 9 的前置检查。
**不提供**草稿回写页面的任何端点（步骤 10 硬约束）。

### 步骤 7：页面关系派生（程序派生，不调 LLM）

```text
时机（两处，都是纯 SQL + 内存计算，无 LLM；概念页与事实页一视同仁，
  统称"词条页"，不区分 kind）：
  a. 词条页（概念页或事实页）publish 时：以该页 source_point_ids 为
     一侧，与其余 published 词条页（含另一种 kind）两两比对，全量
     重写该页涉及的 related / contradicts 行——概念页与事实页之间
     同样可以产生 related/contradicts（例如某原理概念页与其代表性
     实现的事实页），不限同 kind 才比对；
  b. Study 周期扫描：跨 Source KPN 新增后（kpn.md），只重算涉及新增
     knowledge_point_relations 的页面对，不做全库两两扫描。

判定（A、B 为两个 published 词条页的 source_point_ids 集合）：
  related：A×B 之间 knowledge_point_relations.relation_type='related'
    的关系对数 ≥ wiki.relation_kpn_min（默认 1，scope 不限 intra / cross），
    或 |A ∩ B| ≥ wiki.relation_shared_point_min（默认 2）；
  contradicts：A×B 之间存在 relation_type='contradicts' 的关系对 ≥ 1。
  两者不互斥——同一对页面可以同时有 related 和 contradicts 两行，
  这正是主题页「跨主题矛盾」一节的素材来源。

归一化与去重：related / contradicts 无向，from_page_id / to_page_id 按
  page_id 字典序写入，配合 idx_wpr_uniq 保证一对页面一种关系只有一行；
  evidence 记录 shared_point_ids 与 kpn_relation_count，供人工核对。

清理：页面 archived → 删除其全部 related / contradicts 行；
  页面 needs_recompile → 保留关系行（关系依据是 KP 而非正文，
  重编译后依赖 KP 可能变化，publish 时按 a 全量重写即可）；
  contains 行不在此步骤维护，只由二阶编译与 archive 维护（步骤 8/9）。

lifecycle 过滤：判定只使用 lifecycle=current 的 KP 与 published 页面，
  与检索侧口径一致。
```

### 步骤 8：主题页候选与二阶编译

**主题候选识别（Study 侧，周期扫描；2026-08-03 修订，设计依据
`docs/design/wiki.md`「主题：从真实使用中识别，而不是从已发布词条事后聚类」）**：

不再从"已发布概念页的图连通性"事后推导候选——那要求先有一批已发布页面才能求连通分量，会让"有真实需求、材料还没跟上"的领域永远无法被看见。改为直接对真实提问的四元组聚类，候选产生早于、且独立于任何概念页是否已经编译发布。

> 以下具体阈值与流程细节是本次修订按 `docs/design/wiki.md` 机制方向新提出的实现方案（设计文档只确立"从真实提问识别，不依赖材料侧成熟度"这一立场，未固定参数）；`internal/study/`、`internal/wiki/` 中对应的连通分量实现尚未跟进这次修订，编码前应重新核对一遍本节，不要直接照搬旧实现的变量/表结构。

> **熟路指针（2026-08-11 新增，2026-08-12 随 ActivationBundle 机制定案更新描述，本节判据仍未改动）**：下面第 1 步的四元组聚类，和 `docs/impl/v1/activation-bundle.md` 步骤 2 描述的熟路显影用的是同一套归一化口径（含 subject 同义词归一化）——这一点不变。但两者不再是"同一次扫描"这么简单：熟路显影已经改为"先逐条 trace 匹配已有熟路（复用 activation.md 步骤 2 的 Match，含模型辅助匹配），未匹配上的才对残余 trace 聚类去发现全新熟路"，聚类只发生在残余池上，不是对整批窗口内 traces 聚类；本节的主题候选聚类仍然是对整批 traces 聚类（不要求 confident、不要求 direct_point_ids 非空，见下）。两者能共用的是同一个归一化+分组**函数**，不是同一次调用、同一份输入集合——之前"共用一次扫描"的表述不准确，已更正。熟路稳定的组合也预期能补进第 3-4 步的候选范围材料判断（比纯语义检索更准），这条仍未定案。**ActivationBundle 自身已有完整实现文档**（`docs/impl/v1/activation-bundle.md`，含存储、显影/巩固、Match 契约；调度顺序已定案排入 `study.md` 步骤 5b）——此前"还没有对应实现文档"的说法已过时；本节第 1-7 步的现有判据本次仍不改动，仅更新这条指针对 ActivationBundle 现状的描述。

```text
1. 四元组聚类：窗口内（study.event_window_days）的 traces，按归一化
   四元组 (subject, intent, audience, constraint_text) 分组——归一化
   口径与 study.md「问题复杂度观测量」的分组完全一致（含 subject 同义词
   归一化，见 study.md 步骤 2a），保证"同一类问题"在检索侧、学习侧、
   Wiki 侧是同一个定义。**不要求** retrieval_quality=confident、不要求
   direct_point_ids 非空——这是与既有 ConfidentTraceQuadruples / 问题
   复杂度观测量分组的关键差异：后两者只消费 confident 样本，主题识别
   消费全部样本，因为主题候选要回答的是"有没有人反复问"，不是"答没
   答上、引用了哪些知识点"；

2. 稳定簇判定：分组同时满足——
     distinct_question_count ≥ wiki.topic_cluster_min_questions
       （默认 3，与 study.complexity_min_questions 同量级但独立配置——
       两者语义不同，一个是"够格观测"，一个是"够格识别为主题候选"）；
     days_active ≥ wiki.topic_cluster_min_days_active（默认 7，衡量
       "反复以不同措辞被问到、跨越足够长时间"，计算方式与
       wiki.qualifying_min_days_active 相同的 DaysActive 口径，只是
       输入换成该分组内 traces.created_at 而不是激活事件）；
   两项均满足 → 该分组是一个主题候选，此刻不涉及任何知识点；
   已被某个非 archived 的 topic_page_candidate 覆盖（见「去重」）→ 跳过；

3. 候选范围检索：以该分组的 subject/intent/audience/constraint_text
   拼接为查询词，对知识点全文索引做语义检索——不限领域、不限概念，
   不要求历史上已被任何 trace 引用过；**不设分数门槛**（2026-08-07
   修订：`retrieval.wiki_min_score` 是为「Wiki 直答」这一精确匹配场景
   校准的门槛，候选范围检索是召回任务而非精确匹配——目的是把主题范围
   内现存的 KP 尽量收全，真正的把关在下一步 lifecycle/entry_id 过滤、
   以及步骤 7 的关联/可靠度判定，此处误收代价低、漏收代价高，复用直答
   门槛会导致主题范围检索被静默滤空，此前口径已废弃），全文索引召回结果
   直接进入下一步过滤，上限 wiki.topic_candidate_kp_max（默认 50，
   超出按分数降序截取，分数仍用于排序，只是不再作为过滤门槛）；
   （**熟路指针**，2026-08-11，设计层面：这一步目前完全依赖语义检索猜测
   材料范围，`docs/design/activation-bundle.md` 提出的熟路稳定核——该分组
   历史上真被同一类问题反复依赖过的知识点组合——预期可以并入这里的召回
   结果，且这类材料已有真实使用背书、不必再靠分数排序间接判断。是否并入、
   并入后如何与现有截断/排序共存，尚未定案，本次修订不改动上面的检索
   与截断逻辑）；

4. 从检索结果中筛出**主题范围材料 KP**（lifecycle=current，且已归属到
   词条 `entry_id IS NOT NULL`；**不**要求 ActivationLink 已 verified——
   主题范围只定位"哪些 current 知识点落在这个主题里"，以便人工/Study
   先看到需求并组装草稿；verified（2026-08-12 修订：wiki_material_confirm
   关卡已废弃，不再叠加）仍用于步骤 3 一阶材料 qualifying、词条级
   ready、步骤 7 整体可靠度，以及发布正式化，除非强制发布）；
   一条主题范围材料 KP 都没有 → 候选标记 needs_more_data（"有需求、
   缺材料"），写入学习报告 topic_signal_underfilled 节（四元组摘要、
   distinct_question_count、days_active），作为内容采集优先级信号，
   不再往下走，也不产生 wiki_page 壳页；

5. 按归属（entry_id）分组主题范围材料 KP——归属可以是概念，也可以是
   事实（`entries.kind` 区分，判断依据同 kpn.md 步骤 3「类型标注」），
   V1 正式实现两种词条类型，不再只做概念页（此前"事实页推迟到 V3"的
   口径已由 2026-08-03 修订取代）；方法页/经验页/问题页/决策页仍已从
   设计中删除，不是推迟，详见 `docs/design/wiki.md`「Wiki 页面类型」
   一节——V1 的词条页类型只有 concept 与 fact 两种；

6. 逐个 concept 分组（不论其 kind 是 concept 还是 fact）处理：
     该 concept 已有 published 词条页 → 直接复用为候选成员；
     尚无 published 页面，但组内**一阶 qualifying KP**（口径同步骤 3：
       current 且 verified，2026-08-12 修订：wiki_material_confirm 关卡
       已废弃）满足步骤 3「词条级 ready
       判定」四项（广度/连贯/稳定/内聚）→ 一并写该 concept 的
       wiki_candidate（action=wiki_candidate，口径同步骤 1，page_type
       由该 concept 行的 kind 决定），作为本次主题候选的"待发布成员"
       随人工确认一起推进；
     不满足 → 该分组不进候选成员集合，计入候选的 uncovered_entries
       清单（随候选写入 learning_result.reason，命名沿用既有字段，
       实际可能是概念也可能是事实），供后续材料积累后重新纳入；

7. 二阶编译准入（对候选内已具备 published 词条页的成员集合执行；
   成员数 < 2 时无意义，直接跳过——单一词条只产出该词条页本身，
   不构成主题）：
     关联：这批成员词条页（概念页与事实页混合不受限）两两之间 wiki_page_relations（步骤 7 派生）
       中 related 连接数 ≥ contradicts 连接数，且至少存在 1 条 related
       连接——口径与步骤 3 连贯判据同源，但只核验这批候选成员之间的
       关系，不再对全库概念页求连通分量；
     整体可靠度：候选范围检索出的**全部**知识点（步骤 4，不只是已进入
       某个成员页面 qualifying 集合的那部分）中，verified ActivationLink
       覆盖占比 ≥ wiki.topic_reliability_min（默认 0.5）——衡量"这批
       材料整体上验证得有多扎实"，不重复回答"有没有需求"（已在步骤
       1-2 由真实提问回答过）；
       （2026-08-12 修订，解除此前的待确认：wiki_material_confirm 关卡
       整体废弃后，qualifying 本身就等于 verified，这里的 verified 覆盖率
       与"qualifying 覆盖率"是同一件事，不再有二选一的问题——维持
       verified 覆盖率不变，这个指标本来就只是主题层面的宽松参考，真正的
       硬门槛在成员词条各自的一阶 qualifying 上）；
       （**熟路指针**，2026-08-11，设计层面：这里的可靠度目前只认单点
       verified 覆盖率；`docs/design/activation-bundle.md` 提出熟路命中率
       可作为另一条独立的整体可靠度证据——同一批材料作为组合被反复真实
       采用，本身就是一种验证。是否、如何叠加进这个占比计算尚未定案，
       本次修订不改动上面的判据）；
   两项均满足 → 进入候选创建（步骤 8）；否则标记 needs_more_data，
     原因区分"关联不够"还是"整体可靠度不够"，写入 learning_result.reason；

8. 满足 → 在一个事务里：
     步骤 6 中新写的成员 wiki_candidate 保持 pending_confirm（随本次
       主题候选一起展示，不单独由人工逐个确认）；
     建 draft 壳页并写 contains：
       wiki_pages(page_type='topic', entry_id=NULL, title=占位（成员页
         标题拼接，编译时由 LLM 覆盖）, content='', status='draft',
         prompt_version / model_name 留空, compiled_at=NULL)；
       每个**已发布**成员一行 wiki_page_relations(relation_type='contains',
         from=壳页, to=成员页, derived_from='compile')；尚未发布的成员
         （步骤 6 随批新写的 wiki_candidate）暂不写 contains，待其概念页
         编译发布后由步骤 7 页面关系派生流程补写——人工先子后父，
         与既有约束一致，主题页壳本身不因成员未全部发布而阻塞创建；
     壳页 content 为空但不会误入检索——索引只收 status=published；

9. learning_results(action='topic_page_candidate', object_type='wiki_page',
     object_id=壳页 page_id, status='pending_confirm')，reason 说明四元组
     聚类摘要（distinct_question_count、days_active、代表问法）、关联/
     可靠度核验结果、uncovered_entries。object_id 用 page_id 而不是
     四元组分组指纹：标识天然唯一，人工确认的对象就是一个具体页面；

10. 人工驳回 → 壳页 archive + 删除其 contains 行 + learning_result 置
     rejected，不留悬空壳页；已随批创建的成员 wiki_candidate 不受影响——
     成员概念页是否独立发布是另一件事，不因主题候选被驳回而撤销。

去重：同一四元组分组（或归一化后等价的分组）已产生非 archived 的
  topic_page_candidate（无论 pending_confirm、applied，还是已发布成员
  仍在 uncovered_entries 清单中）→ 不重复产候选，避免对同一批真实
  需求反复报告。
```

**人工手动指定主题（第二条生成口径，复用同一条二阶编译链路；2026-08-03
修订，设计依据 `docs/design/wiki.md`「主题候选的产生：两条来源」）**：
与概念页的「人工指定主题手动编译」同构——Study 产候选与人工指定是同一条
后续机制（材料组织、关联核验、二阶编译、支持度核验、发布前验收）的两个
触发入口，**不是两套生成逻辑**，区别只在触发方式。

旧版 `POST /wiki/topics` 要求人工直接给出一批**已发布概念页**作为成员
（`member_page_ids`），这与新设计冲突——新设计要求人工手动指定同样可以
"不要求这些知识点所属的词条已经独立发布"（`docs/design/wiki.md` 原文）。
`POST /wiki/topics` 的请求语义因此改为**给主题范围**，不是给成员页面：

```text
请求：{ "topic_name": "...", "topic_description": "...", "domain_id"?: "..." }
  人工直接给出一个主题的名称/范围描述（domain_id 可选，限定检索领域）；

处理：
  1. 候选检索（2026-08-07 再次修订：全文 ∪ 目录结构，取代此前"只做全文
     检索"的口径——**仅适用于人工触发的这两条口径**——`POST /wiki/topics`
     一把梭与分步向导第 1 步共用同一个 `retrieveAndGroupQualifyingKPs`
     实现，Study 自动路径 `DetectTopicCandidate`（上面步骤 8 第 3 步）
     不受影响，仍是纯全文检索，理由见「分步向导」小节末尾的范围说明）：
       a. 全文检索：以 topic_name + topic_description 为查询词，对知识点
          全文索引（points 索引）做语义检索（domain_id 非空时限定该
          领域），不设分数门槛（2026-08-07 首次修订，理由同步骤 8 第 3
          步）；
       b. 目录结构检索：同一查询词对 source_outlines 目录索引做语义检索，
          命中的目录节点按 source_id 分组展开子孙节点（同一来源下的
          子章节一并收进来，避免只命中父标题漏掉子章节材料），解析出
          知识单元，再取这些单元下**全部** lifecycle=current 的 KP（不是
          "每个单元只取一条代表 KP"——候选范围要广度，不是精确证据）；
       c. 两路结果取并集去重，domain 过滤统一在并集上做一次，按
          wiki.topic_candidate_kp_max 截断；
     筛主题范围材料 KP（lifecycle=current，不要求 verified，同步骤 8
     第 4 步）；
  1b. LLM 相关性判定（2026-08-07 新增）：候选检索是召回，召回结果里混有
     只是词面相关、实际不属于该主题范围的材料——对步骤 1 qualifying 过滤
     后的候选批量调用 LLM（`config/prompts/wiki_topic_candidate_rerank.md`，
     版式仿照检索慢路径 `rerank_judge.md`，但判断目标简化为二元"是否属于
     该主题范围"而非慢路径的 direct/supporting/irrelevant 三态——候选
     语义字段复用摄取阶段已经算好的 `unit_rerank_semantics`
     source_theme/content_theme/intent/object/scope，不重新抽取）；
     LLM 调用或解析失败时该批候选原样保留（fail-open，不因为判定环节
     本身出错反而让候选变少）；手动生成的是草稿，正式化仍看使用验证 /
     强制发布；
  2. 按归属分组，已发布概念页直接复用，未发布但满足概念级 ready 判定
     （ready 仍依赖一阶 qualifying = current 且 verified）的分组一并写
     wiki_candidate（同步骤 8 第 6 步）；
  3. 在一个事务里建 draft 壳页并写 contains（同步骤 8 第 8 步，已发布
     成员写 contains，未发布成员待各自发布后由步骤 7 补写）；
  4. 用「这批材料确实是在回答同一类问题」这一真实使用证据来确认候选，
     而不是仅凭语义相似——响应内附带该主题范围内匹配到的真实确证问法
     摘要（检索范围内主题范围材料 KP 各自的确证 trace 问法，供人工判断
     这批材料是否真的构成一个主题，而不是恰好共享了一些用词）。

Study 的关联 / 整体可靠度判定（步骤 8 第 7 步）在人工触发时改为仅展示、
  不阻断：响应附带可选字段 readiness = { member_count,
  related_connection_count, contradicts_connection_count,
  reliability_ratio, reliability_min }——信号照样算出来给人看，人工自己
  判断是否"够格"，不由系统替人工做决定。

不设成员数硬性区间：新机制的候选范围由真实提问的语义边界决定，不再是
  从全库图连通性里截出来的分量，"覆盖大半知识库"的风险结构上已经
  显著降低；member_count 仍会算出展示，但不作为拒绝创建的门槛。

来源留痕：壳页 compiled_from 存哨兵值 "manual_trigger"（与一阶人工编译
  同口径）；不写 topic_page_candidate learning_result（无 pending_confirm
  可驳回对象——壳页本身可直接 archive 清理）。

**分步向导（2026-08-07 新增，与上面的一把梭 `POST /wiki/topics` 并存，不是
替换）**：一把梭流程对"候选已经 ready"的场景仍然好用（未发布词条不满足
`isEntryReady` 时会被静默跳过，`uncovered_entries` 只是展示）；但对刚入库、
还没有真实使用积累的材料，`isEntryReady` 几乎必然不过（需要 ≥1 条 related
KPN 关系、`days_active` 达标），人工手动指定又拿不到"现在就编译"的选项。
分步向导把同一条检索/分组/编译/组页链路拆成人工可控的三步，新增两个端点，
复用两个已有端点（前端拆成两屏：步骤 1 屏做逐词条编译/发布并可展开查看
KP，步骤 2 屏勾选已发布成员并 `POST /wiki/topics/draft` 建主题壳——先子后父）：

```text
POST /wiki/topics/candidates   步骤 1，只读预览：{topic_name, topic_description,
  domain_id?} → 检索（全文 ∪ 目录结构 → LLM 相关性判定，口径同上面的
  一把梭步骤 1/1b，2026-08-07 再次修订）+ qualifying 过滤（result_id 为空
  → qualifying = lifecycle=current，不要求 verified，见步骤 2「人工指定
  主题手动编译」2026-08-07 修订）+ 按 entry_id 分组，不写任何
  wiki_candidate / 不建壳页；对每个词条返回 entry_id / entry_name /
  qualifying_kp_count / already_published_page_id（若有）/ is_ready
  （isEntryReady 只读结果，这一项仍按 Study 推荐路径的 qualifying 定义
  计算——即 verified（2026-08-12 修订：wiki_material_confirm 关卡已
  整体废弃，qualifying 恢复为只看 verified，与步骤 3 保持同一份定义）
  ——它是"readiness 参考信号"，不是"能不能生成"的门槛，两者口径本来就
  不同）/ readiness 明细；

POST /wiki/compile             步骤 2a，逐词条编译（复用步骤 2 已有端点，
  未改动，效果自然生效——manual 触发口径改为不要求 verified 后，未就绪
  词条也能编译）：人工对预览里挑中的未发布词条逐个调用，产出 status=draft
  页面；

POST /wiki/pages/:id/publish   步骤 2b，发布（复用已有端点，未改动）：草稿
  需要人工显式发布才能成为已发布概念页——publish 的 selfcheck 质量核验
  （引用支持度等）不受 qualifying 口径修订影响，材料越薄、支持度越难达标，
  这是符合预期的（生成容易，正式发布仍要经得起核验）；

POST /wiki/topics/draft        步骤 3，显式成员建草稿：{topic_name,
  member_page_ids} → 人工从"预览里已发布的 + 刚发布出的"概念页中勾选，
  显式给出主题成员列表（不再由系统按 ready 自动决定谁进谁出）；校验每个
  member 都是 status=published 且非 topic 类型页面，不满足报
  ErrMembersNotPublished；直接调用 createTopicShell 建 draft 壳页 +
  contains（与一把梭最终落库逻辑相同，只是成员列表由人工显式给出而不是
  程序按 ready 判定筛出）；compiled_from 同样存 "manual_trigger"。
```

不新建编译管线：壳页建好后的分析/生成完全复用
  POST /wiki/pages/:id/topic/analyze|compile。
```

**检索升级范围边界（2026-08-07）**：全文 ∪ 目录结构 → LLM 相关性判定这套
升级只覆盖 `retrieveAndGroupQualifyingKPs`——一把梭 `POST /wiki/topics`
与分步向导步骤 1 共用的实现，两者都是人工触发、偶发调用。上面步骤 8 第 3
步的 `DetectTopicCandidate`（Study 周期扫描、自动识别主题候选）**不受
影响**，仍是纯全文检索：那条路径是自动、高频触发的（每个扫描周期对每个
新识别出的稳定簇都跑一次），加一次 LLM 相关性判定会把常驻成本乘上候选簇
数量，且这次改动动机是提升人工触发场景的候选质量，不是普遍收紧 Study
自动路径的召回。

**二阶编译**：

```text
POST /wiki/pages/:id/topic/analyze
  前置：page_type='topic' 且 status ∈ {draft, needs_recompile}；
        全部 contains 成员 status='published'，否则 409 并列出待处理
        page_id（人工先子后父，见步骤 9）；
  处理：收集输入（见下）→ 调 config/prompts/wiki_topic_analyze.md →
        产出 claims / tensions，做与生成阶段相同的白名单校验；
        不落库、不改状态，可反复调用；
  响应：{ page_id, claims: [...], tensions: [...] }

POST /wiki/pages/:id/topic/compile
  请求：{ "claims": [...], "tensions": [...] }（可选，缺省时服务端内部
        自动跑一遍分析，与「先 analyze 再原样确认」等价，同步骤 2 口径）；
  处理：同前置检查 → 生成正文 → 校验 → status=draft、写 wiki_revisions →
        learning_results 置 applied（confirmed_by=manual）；
        同步执行，HTTP 超时上限同步骤 2（120s）；
  响应：{ page_id, status: "draft", title }
```

**输入收集（与一阶编译的关键差别：材料是页面，不是 KP 原文）**：

```text
成员页面 title + content 全文（含其四节结构）；
成员页面之间的 related / contradicts 关系行（含 evidence 摘要），
  用于组织「子主题分工」与「跨主题矛盾」两节；
成员页面「待验证点」小节文本的并集，作为主题级待验证素材；
成员页面的 trigger_questions 并集，作为主题页 trigger_questions 的
  挑选素材（仍是真实观测问法的传递，不新增臆造）；
上限 wiki.topic_compile_max_chars（默认 24000 rune），超出按成员页面
  source_point_ids 数量降序截取成员（排序权重，不是门槛）；
不取 KU 正文、不取 KP 原文、不取 knowledge_gaps——二阶编译只重组
  已编译结论，事实层的准入在一阶已经回答过（见 docs/design/
  wiki.md「证据回链必须穿透两层」）。
```

**Prompt 文件**：`config/prompts/wiki_topic_analyze.md` / `wiki_topic_compile.md`

```text
分析阶段要求（与 wiki_analyze.md 同构，材料形态不同）：
  1. 只使用提供的成员页面内容与关系，不引入材料之外的信息；
  2. 每条 claim 是跨成员页面成立的主线结论（不是单页结论的搬运），
     标注其依据的 point_id（只能使用成员页面中出现的 point_id）；
  3. 成员页面之间的 contradicts 关系、以及各页「待验证点」中仍然
     悬而未决的部分，写入 tensions，不得强行调和。

生成阶段：页面结构固定为五节
  ## 主题概览 / ## 主线结论 / ## 子主题分工 / ## 跨主题矛盾与待验证点 /
  ## 依赖页面
  主线结论逐条对应已确认 claims，末尾标注 [point_id]；
  子主题分工逐个说明成员页面在本主题里承担什么，并给出成员页面标题；
  依赖页面列出全部成员页面标题；
  额外输出 aliases 与 trigger_questions（从成员页面 trigger_questions
  并集里挑选 / 归纳，不臆造）；
  额外输出 member_roles——与「子主题分工」一节同源的结构化版本：
    [{ "member_page_id": "...", "aspect": "该成员在本主题里承担的面向",
       "question_types": ["这个成员能回答的问题类型，2-4 条"] }]
    正文一节给人读，member_roles 给程序读（落库到 wiki_pages.member_roles）；
    V3 沿主题结构拆解子问题时直接查这个字段，不回去解析 Markdown
    （V2 立场：不从 Wiki 文本临场解析）；V1 用它给检索侧的成员展开排序
    （见下「检索接入」）。
```

**生成后校验**：

```text
cited_point_ids ⊆ 成员页面 source_point_ids 的并集（分析、生成两阶段
  各校验一次）；正文 [point_id] 标注逐一提取，越界的删除并 warn；
content 必须包含上述五个固定小节标题，缺节 → 按 LLM 失败处理
  （重试一次，仍失败返回 500，不产生 / 不更新页面正文）；
source_point_ids = 正文实际引用的 point_id 并集；
source_unit_ids  = 由 source_point_ids 反查；
source_link_ids / observed_conditions = 成员页面同名字段的并集
  （直接并集去重，不重新查 activation_links——成员页面编译时已经
  查过，四元组入口因此天然被主题页继承）；
uncovered_points = 成员页面 uncovered_points 的并集（按 point_id 去重）；
member_roles 校验：member_page_id ⊆ contains 成员集合，越界的剔除并
  warn；缺失或为空不算编译失败（warn + 空数组），但会让检索侧的成员
  展开退化为按 source_point_ids 数量排序，且 V3 拆解无结构可用——
  实现时应在报告里可见这类页面，便于人工触发重编译补上；
aliases / trigger_questions 缺失不算失败（warn + 空数组），
  条数上限同 wiki.trigger_questions_max。
```

**调用参数**：同步骤 3（reasoning 模型，temperature 0.3，记录
`prompt_version` / `model_name`）。

**检索接入：主题页是召回骨架，不是直答单元**

```text
主题页与概念页同进 wiki index，三入口召回逻辑完全不变；但主题页命中后
**不进直答序列、不调 answer_wiki**，而是就地展开：

  1. 展开：取该主题页 contains 的成员概念页中 status=published 的，
     按 member_roles.question_types 与问题分词的词项重合度降序
     （member_roles 为空时退化为按 source_point_ids 数量降序），
     插入到该主题页原本所处的优先级档位上；
  2. 去重：成员页若已由词法 / 概念 / 四元组入口独立命中，保留其原有
     （更高的）档位，不因展开而降级；
  3. 截取：合并后仍按 retrieval.wiki_max_candidates（默认 3）截断，
     逐个尝试直答，sufficient 判断与 citation 校验完全不变；
  4. 骨架注入（无论直答是否成功都记录）：把展开出的全部成员页面
     （含被截断掉的）的 source_point_ids 并集作为 skeleton_point_ids
     带入检索上下文；直答全部失败回落慢路径时，这批 point_id 直接
     作为 Rerank 候选注入、**跳过 Outline 召回**（retrieval.md 第 2 层），
     不额外增加任何 LLM 调用。

为什么不让主题页直答（设计取向，实现时不要"顺手加上"）：
  主题页是概要，细节都在成员页里，它的 sufficient=false 概率天然高于
  概念页；让它占用一个候选位，代价是多一次 LLM 调用 + 挤掉一个可能
  有用的概念页，而 V1 并不具备拆解聚合能力（属 V3），赢不回来。
  主题页对复杂问题的价值是「这个问题覆盖这一簇、材料在这几页」——
  这是召回骨架的功能，查 contains 是一次 SQL，零 LLM 成本。

path_type 不新增：直答成功仍是 path_type=wiki（命中的是概念页）；
  骨架注入后走慢路径的仍是慢路径的 path_type，但 traces 需记录
  skeleton_page_id（哪个主题页提供了骨架），供步骤 9 的信号回填与
  「主题页边界切得对不对」的评估使用。
```

### 步骤 9：级联重编译与拆解信号

```text
传导（步骤 5 标记来源 c）：概念页 → needs_recompile / archived 时，
  经 contains 反查父主题页 → MarkNeedsRecompile(pageID,
  reason='member_page_changed:<member_page_id>')；主题页不再向上传导
  （只有两层，见 docs/design/wiki.md「当前阶段只做两层」）。

主题页重编译前置检查（POST /wiki/pages/:id/recompile 对 page_type='topic'）：
  全部 contains 成员 status='published' → 允许执行；
  存在非 published 成员 → 409，响应列出待处理的 page_id 与其 status，
    提示先处理成员页面（人工先子后父，与「编译永远人工确认」一致，
    不做自动级联编译）；
  成员被 archive → 重编译时从 contains 中移除该行；剩余成员数 <
    wiki.topic_member_min → 拒绝重编译并提示 archive 该主题页。

拆解信号（只记录，不执行）：
  触发条件（对应步骤 8 的骨架角色）：某次检索中主题页命中并展开了成员
  页面，但全部直答候选 sufficient=false、最终回落慢路径 →
  写 learning_events(event_type='topic_decompose_signal'，新增枚举值)。

  payload 分两次写（关键——只记"失败了"是噪声，记"最终由哪几块答出来"
  才是可学习的结构）：
    检索时：{ page_id, question, member_page_ids, skeleton_point_ids }；
    慢路径回答完成后回填（trace_write 异步任务内，与 trace 落库同一步，
      不额外起任务）：
      resolved_point_ids       最终被回答引用的 point_id（取自
                               traces.direct_point_ids，口径与共现统计一致）；
      resolved_member_page_ids resolved_point_ids 经成员页面
                               source_point_ids 反查出的成员页面集合；
      resolved_outside_count   不属于任何成员页面的 point_id 数量
                               （> 0 说明主题页的成员边界漏了东西）。
    回答未产生有效引用（partial / gap）时回填空数组并标记 unresolved，
      不重试——这类样本对拆解学习没有价值，但对边界评估仍有意义。

  「resolved_member_page_ids 含 2 个以上成员」正是"这个问题需要成员
  A 和 C 拼起来"的样本，是 V3 拆解策略的训练素材；
  「resolved_outside_count 持续 > 0」是主题页边界切得不对的证据，
  进学习报告提示人工重编译或调整簇。

  该事件只累积、进入学习报告统计节，**不驱动任何 V1 学习动作**
  （不改页面状态、不改 ActivationLink 统计、不触发重编译）；
  V1 照常回落慢路径，不做问题拆解——拆解与子结论聚合属深想路径
  / Working Model，是 V3 能力（见 docs/impl/v1/readme.md「V1 不做什么」）。
```

### 步骤 10：写作草稿（页面只读，编辑落在派生物上）

```text
POST /wiki/pages/:id/drafts
  请求：{ "mode": "page" | "assembled" }
        主题页默认 assembled，概念页默认 page（也允许显式指定 page）；
  前置：page_id 存在且有至少一条 revision（草稿可从 draft / published
        状态的页面派生；status 不限，便于对 needs_recompile 页面
        先取当前正文写作）；
  处理：
    mode=page：以该页最新 wiki_revisions 的 content / title 建草稿，
      source_page_ids=[page_id]；
    mode=assembled（写作的默认形态）：按 contains 顺序组装
      主题页正文 + 各成员概念页正文（每个成员一个二级小节，标题为
      成员页标题，正文原样并入），source_page_ids 记全部并入页面。
      理由：主题页正文按设计不含 KU 正文与 KP 原文（步骤 8 输入收集），
      单页快照给到写作者的是「摘要的摘要」，动不了笔；组装只是一次
      查询拼接，不调 LLM；
    两种模式都生成 evidence_index：source_page_ids 的 source_point_ids
      并集，逐条填 point_id / KP 摘要 / 所属 KU 主题 / source_ref，
      只读、不随人工编辑变化——人工改写时正文里的 [point_id] 标注会丢，
      清单让引用还能重新挂回去；
    同一页面可有多份草稿（不同写作用途）；
  响应：{ draft_id, page_id, mode, source_page_ids, title }

PATCH /wiki/drafts/:id   自由更新 title / content / note：
  不做 citation 白名单校验、不做小节结构校验、不做 point_id 提取——
  草稿是人工作品，不是编译产物，校验会让写作无法进行。

GET /wiki/drafts/:id     附 stale 标记：source_revision_id 是否仍是
  该页面最新 revision；页面重编译后草稿不被改动、不被删除，只变 stale。

硬约束（实现时必须逐条守住）：
  草稿不进 wiki index、不参与检索与直答；
  草稿不参与 lifecycle 传导、不产生 learning_events；
  不存在任何 draft → wiki_pages 的写回接口，wiki_pages.content
    仍然只由编译产生（docs/design/wiki.md「复杂问题与写作：
    两个出口的定位」，与 Claim 不可独立编辑同一条约束）；
  页面 archive 时其草稿保留（写作产物不该因页面归档而丢失），
    只在 GET 响应里标注来源页面已 archived。

内容回流：草稿中新增、值得长期保留的内容由人工导出为文件，走
  POST /sources 正常导入链路 → source_process → unit_extract，
  之后可能形成 qualifying KP，再经一阶 / 二阶编译回到 Wiki。
  人工写作因此是系统的输入，不是对表达层的旁路写入。

**回流的自体循环必须挡住（这是正确性问题，不是优化）**：

```text
风险：草稿内容本来就派生自 Wiki 页面，页面又派生自 KP。回流导入会产出
  一批与自己祖先近乎重复的 KP，这些 KP 与祖先 KP 之间会被 KPN 判为
  related，于是虚增关系边数、虚增 qualifying KP 数，反过来把同一批
  知识推成新的主题页候选 / 重编译候选——系统在自己身上打转，
  且所有计数看起来都在"增长"。

防护（三条，缺一条环就闭合）：
  1. 来源标记：草稿导出的文件经 POST /sources 导入时带
     origin='wiki_draft' + origin_page_id=来源页面
     （sources 表扩展见「数据结构」；请求体新增可选字段，
     默认 'upload'，既有调用行为不变）；
  2. 祖先关系跳过：跨 Source KPN 匹配时（kpn.md），若候选 KP 一侧来自
     origin='wiki_draft' 的 Source，另一侧属于 origin_page_id 页面的
     source_point_ids（含该页面历史 revision 引用过的 point_id），
     **不建立关系**——这不是知识之间的关系，是同一份知识的复印件；
  3. 统计排除：概念级 ready 判定、页面关系派生、主题候选的关联 / 整体
     可靠度核验都不计入被第 2 条跳过的边。回流 KP 本身仍正常参与激活、
     验证、与**其他**知识建立关系并可成为 qualifying——排除的只是自体
     祖先边，不是回流内容本身，否则回流就失去意义。

可观测：学习报告单列 origin='wiki_draft' 的 Source 及其产出 KP 数、
  被跳过的祖先边数，便于人工发现"系统在自己身上打转"的苗头。
```

## 配置项（config.yml: wiki 节，新增）

```yaml
wiki:
  compile_max_chars:            12000
  recompile_new_kp_min:         2
  trigger_questions_max:        10   # trigger_questions / aliases 各自的条数上限
  qualifying_min_days_active:   7    # 概念级 ready 判定新增门槛："持续采用"要求
                                     # qualifying KP 的激活事件覆盖至少 N 天，
                                     # 防止短时间集中追问刷够次数即触发编译

  # 两层架构（步骤 7-9）
  relation_kpn_min:             1    # 派生 related 所需的 KPN related 关系对数
  relation_shared_point_min:    2    # 派生 related 的另一条件：共享 KP 数
  topic_member_min:             3    # 重编译后主题页剩余成员数的下限
                                     # （2026-08-03 起不再兼作候选创建时的
                                     # 成员数门槛——新机制的候选范围来自
                                     # 四元组聚类，不设创建时的成员数上限）
  topic_compile_max_chars:      24000 # 二阶编译输入上限（成员页面正文合计）

  # 主题候选识别（步骤 8，2026-08-03 新增，四元组聚类替代连通分量）
  topic_cluster_min_questions:  3    # 稳定簇判定：分组内不同问法数下限
  topic_cluster_min_days_active: 7   # 稳定簇判定：分组激活时间跨度下限
                                     # （与 qualifying_min_days_active
                                     # 相同的 DaysActive 计算口径）
  topic_candidate_kp_max:       50   # 候选范围语义检索的知识点数上限
  topic_reliability_min:        0.5  # 二阶准入「整体可靠度」：候选范围
                                     # 全部知识点中 verified 覆盖占比下限

  # 综合满意度轴（步骤 4a，2026-08-13 新增）
  synthesis_audit_rate:         0.05 # 每次 Wiki 直答服务后，被抽中触发一次
                                     # 独立核实试验（复用 retrieval.md 步骤 2c
                                     # 的编排）的概率；建议默认值与
                                     # activation.explore_rate_trusted 同量级
                                     # （定期复查，不是持续验证），未经真实数据
                                     # 校准，同本节其余阈值的一贯做法
```

## 依赖

```text
基础设施：SQLite、Bleve（新增 wiki 索引）、LLM client（reasoning 模型）
Study：   wiki_candidate 确认流、recompile_flag（study.md 步骤 6）
Lifecycle：SetUnitLifecycle → MarkNeedsRecompile 联动
Retrieval / Answer：Wiki 直答层接入（retrieval.md 第 0 层；
  answer_wiki 路径产出标准 AnswerResult，Trace 无感知差异）；
  2026-08-13 新增：综合满意度轴的独立核实试验（步骤 4a）复用 retrieval.md
  步骤 2c 定义的同一套编排（触发方仍是 Retrieval，异步、不阻塞已发出的
  Wiki 直答），本文档不新增一套并行的审计编排逻辑
Unit / Trace：编译输入的 KP / KU / 共现 / gap / 确证问题原文（只读）；
  2026-08-13 新增：Trace 负责比对独立慢路径结果与页面 source_point_ids、
  写入 wiki_synthesis_audit_success/failure、调用
  wiki.RecordSynthesisOutcome 更新 wiki_pages 的四个综合满意度计数列
  （同构 trace.md 步骤 3b 对 activation_audit_* 的处理，本文档不代为
  修改 trace.md，实际写入逻辑落在该文档）
Activation：`status=verified` 链接的 observed_conditions（编译时聚合写入
  wiki_pages.observed_conditions，检索时四元组入口复用其
  conditionGroupMatches 匹配逻辑，只读消费，不产生反向统计；`verified`
  现在是 activation.md「状态机」定义的连续置信度派生结果，见步骤 3
  2026-08-13 编注）
KPN：      跨 Source 关系新增后触发页面关系重算（步骤 7b）；
  页面关系的 related / contradicts 完全派生自 knowledge_point_relations，
  不新增关系类型（KPN 仍只有 related / contradicts、bidirectional）
Study：    主题页候选（action=topic_page_candidate）与 topic_decompose_signal
  的报告聚合（study.md 步骤 6 扩展）
Source：   草稿内容回流走既有 POST /sources 导入链路，wiki 侧不提供回写
```

## 完成标准

```text
entry_id 指向 entries.kind='fact' 的行时，POST /wiki/compile/analyze
  与 POST /wiki/compile 正确产出 page_type='fact' 的页面，Prompt 使用
  事实专属的 entry_kind_hint 措辞；page_type 与 entries.kind 不一致的
  请求（如对 kind=concept 的行请求 page_type=fact）返回 400；
  事实页与概念页共用同一条 qualifying/ready 判定、citation 白名单、
  发布/重编译、页面关系派生、二阶主题接入逻辑（可用集成测试断言：
  两种 kind 的页面在这几步的代码路径完全一致，只 page_type 取值不同）；
候选确认 → analyze（拟采用论断结构）→ 确认 → 生成 → draft 的链路可走通，
  分析产物不落库、生成阶段的 citation 白名单收窄到已确认 claims 的并集；
未先调用 analyze 直接 POST /wiki/compile 时，内部自动分析后生成，
  结果与"先 analyze 再原样确认"等价；
cited_point_ids 白名单校验生效，越界标注被剔除并记录（analyze、生成
  两个阶段各自校验一次）；
publish 后页面进入 wiki index（含 aliases / trigger_questions 字段），
  同主题问题走 Wiki 直答（典型 1 次 LLM 调用），
  citations 可经 point_id 反查 KU 与来源位置；
措辞与页面正文不重合、但命中 trigger_questions 或概念名的问题
  也能进入直答候选（词汇鸿沟场景）；
trigger_questions 生成素材为真实确证 trace 的问题原文，非纯 LLM 想象
  （抽查编译产物，trigger_questions 与对应 qualifying KP 的确证 trace
  问题文本高度重合）；
观测四元组与 published 页面完全一致的问题（未见于词法/概念入口能命中
  的措辞）也能经四元组入口进入直答候选，且优先于纯词法命中；
qc.Subject/Intent 为空（未经 Session 解析的调用路径）时四元组入口正确
  空转，不影响词法/概念两条入口正常工作；
首个候选 sufficient=false 时正确尝试下一候选，
  全部候选耗尽后正确回落后续检索层；
依赖 KP 被 superseded 后页面自动 needs_recompile 并退出索引，
  同主题问题回落慢路径；
recompile → 新 revision → 再 publish 的完整生命周期可走通，
  每次编译可追溯触发来源；
archive 后页面退出索引且不可再编译；
fake LLM 下编译、校验失败、直答、回落、重编译路径测试稳定运行。
```

两层架构扩展（步骤 7-10）：

```text
两个概念页的 KP 之间存在 KPN related / contradicts 关系时，publish 后
  自动派生出对应的 wiki_page_relations 行，方向归一化后无重复行，
  evidence 可核对到具体 shared_point_ids / 关系对数；
关系派生全程不调 LLM，且只有 related / contradicts / contains 三种类型；
四元组分组同时满足 distinct_question_count / days_active 两项稳定簇
  判定、且候选范围检索出的 qualifying KP 分组满足关联与整体可靠度
  两项二阶准入时，Study 产出 topic_page_candidate 并留下 draft 壳页 +
  contains 关系（已发布成员）；分组内未发布但满足概念级 ready 判定的
  成员随批写 wiki_candidate；人工驳回后壳页 archive、contains 行清空，
  不留悬空壳页，但随批创建的成员 wiki_candidate 不受影响；
不满足稳定簇判定或候选范围内没有主题范围材料 KP（current）时不产候选，写

  topic_signal_underfilled 报告项（四元组摘要、distinct_question_count、
  days_active）；
人工指定主题（POST /wiki/topics，给 topic_name/topic_description，
  不再要求给已发布成员页面 id）可跳过 Study 建壳：走同一套候选范围检索
  + 归属分组 + 二阶准入计算，关联/整体可靠度信号仅作 readiness 展示
  不阻断；随后复用 topic/analyze|compile；
二阶编译输入只含成员页面正文与关系，不含 KU 正文 / KP 原文；
主题页正文的 [point_id] 全部落在成员页面 source_point_ids 并集内，
  越界标注被剔除；随机抽取一条标注可沿
  主题页 → 概念页 → KP → KU → source_ref 走通完整回链；
主题页 source_link_ids / observed_conditions 等于成员页面并集，
  四元组入口对主题页同样生效；
member_roles 落库且 member_page_id 全部在 contains 成员集合内；
  V3 拆解所需的成员分工可直接查字段得到，无需解析正文；
主题页命中后**不产生 answer_wiki 调用**，而是展开成员概念页进候选；
  可用调用计数断言：命中主题页的问答里 answer_wiki 调用次数 ≤
  展开后实际尝试的概念页数；
主题页展开后直答全失败时，慢路径的 Rerank 候选包含 skeleton_point_ids
  且未执行 Outline 召回（LLM 调用数不高于同问题无主题页命中时）；
概念页 uncovered_points 非空时，Page 能看到清单，且该清单不出现在
  页面正文、不进 citation 白名单、不影响 ready 判定；
成员概念页被 superseded 传导为 needs_recompile 后，父主题页自动
  needs_recompile 并退出索引；此时对主题页 recompile 返回 409 并列出
  待处理成员，成员重新 publish 后 recompile 可正常执行；
主题页展开后全部候选 sufficient=false 时产生一条 topic_decompose_signal，
  慢路径回答完成后 resolved_point_ids / resolved_member_page_ids /
  resolved_outside_count 被正确回填；该事件不改变任何页面状态与链接统计；
草稿可从页面派生、自由编辑、页面重编译后仅变 stale 而不被覆盖；
  assembled 模式的草稿包含成员页面正文与只读 evidence_index；
  全库检索与 lifecycle 传导不受草稿影响；代码中不存在 draft → page
  的写回路径（可用测试断言无该接口）；
回流防护：把某主题页的草稿导出后以 origin='wiki_draft' 导入，
  新 KP 与 origin_page_id 页面已引用的 KP 之间不产生 KPN 关系，
  该概念的 qualifying 计数、页面关系派生与主题候选核验不因这次回流而增长；
  同一批回流 KP 与**其他**知识之间的关系照常建立；

综合满意度轴（步骤 4a，2026-08-13 新增）：
  migration 后存量 wiki_pages 行 synthesis_success_count=
    synthesis_failure_count=synthesis_audited_success_count=
    synthesis_audited_failure_count=0，mean(page)=0.5；
  mean(page) 严格实现 (success+1)/(success+failure+2)；
  Wiki 直答成功服务后按 synthesis_audit_rate 概率触发独立核实（fake 环境
    下可注入固定随机源断言抽样边界），未中选不产生任何 synthesis 事件、
    不更新四个计数列；
  中选后异步触发的独立慢路径检索不阻塞、不延迟已经返回给用户的 Wiki
    直答响应（测试用例：mock 后台任务，断言主请求响应时间不受影响）；
  独立慢路径 direct_point_ids 与页面 source_point_ids 有交集 → 写
    wiki_synthesis_audit_success，synthesis_success_count 与
    synthesis_audited_success_count 同步 +1；无交集 → 写
    wiki_synthesis_audit_failure（reason=point_not_in_page_scope），
    synthesis_failure_count 与 synthesis_audited_failure_count 同步 +1
    （测试用例断言两对计数恒同方向变化，audited_* 恒为 success_count/
    failure_count 的子集，同 activation.md 对 RecordAuditOutcome 的
    验收口径一致）；
  独立慢路径检索本身失败时记 warn、不产生事件、不更新计数；
  mean(page) 不出现在 needs_recompile 判定、发布 selfcheck、页面下线
    的任何代码路径里（可用代码审计/测试断言：把某页面 mean(page) 人为
    压到接近 0 后，该页面的 status、wiki index 收录状态均不受影响，
    只有页面详情/报告接口能读到这个数值下降）；
  fake 环境下抽样命中/未命中、比对一致/不一致、后台检索失败四类场景
    测试稳定运行。
```
