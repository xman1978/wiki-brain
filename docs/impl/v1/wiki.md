# Wiki 编译实现路径（V1 初版）

## 职责

实现主题页 / 概念页的最小闭环：Study 产出的 Wiki 候选经人工确认后触发 LLM 编译，生成带证据回链的页面；人工发布后进入独立 Bleve 索引，作为检索的 Wiki 直答层；底层 KU/KP 状态变化时标记待重编译。

**两层架构扩展（步骤 7-10）**：概念页为一阶编译（KP → 页面），主题页为二阶编译（已发布概念页 → 页面），两者由程序派生的页面关系（`related` / `contradicts` / `contains`）串成知识架构；写作出口是页面派生的草稿，页面本身保持只由编译产生。设计依据见 `docs/design/wiki-compilation.md`「页面关系与两层知识架构」。

方法页 / 经验页 / 问题页 / 决策页与视角化编译推迟到 V3；Claim 双产物与防固化要素补齐属 V2（见 docs/impl/v2/readme.md）。复杂问题的拆解与子结论聚合属深想路径 / Working Model，是 V3 能力——V1 只建结构并记录 `topic_decompose_signal`（步骤 9）。

## 数据结构

```sql
CREATE TABLE wiki_pages (
    page_id          TEXT PRIMARY KEY,
    page_type        TEXT NOT NULL,
    -- topic / concept（V1 两种；编译输入相同，区别在标题组织，见步骤 3）
    concept_id       TEXT REFERENCES concepts(concept_id),
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
    -- docs/design/wiki-compilation.md 防固化要素"依赖的 ActivationLink"，
    -- 用于页面详情展示和未来生命周期追溯，不驱动重编译判断。
    aliases          TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：概念别名/缩写/口语叫法（migration 029，编译时 LLM 生成，
    -- 只进 wiki index 作检索字段，不属于正文，不参与 citation 白名单）
    trigger_questions TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：该页面能回答的典型问法（migration 029，5-10 条），
    -- 用于弥合用户措辞与页面用词的词汇鸿沟（Wiki = Answer + Retrieval Index）。
    -- 生成素材改为真实观测问法（见下方"编译输入"新增一项），LLM 角色从
    -- "凭材料想象"变为"从真实问法里挑选/归纳"（docs/design/wiki-compilation.md
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
CREATE INDEX idx_wiki_concept ON wiki_pages(concept_id);

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
docs/design/wiki-compilation.md「页面关系与两层知识架构」）：

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
    -- 且 direction 恒 bidirectional，concepts 表在 domain 下平铺无父子，
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
-- （无 verified ActivationLink）的 KP 清单 [{ "point_id","summary" }]。
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
concept  一阶编译产物，输入是 qualifying KP，concept_id 必填；
topic    二阶编译产物，输入是已发布的 concept 页，concept_id 恒为 NULL
         （成员概念经 contains -> 成员页面 -> concept_id 反查）。

存量处理：migration 035 把已有 page_type='topic' 且 concept_id 非空的行
  一次性改写为 page_type='concept'——它们是按旧口径由一阶编译产生的，
  语义上就是概念页，改写不影响正文、索引与依赖字段。
```

Bleve 新增 `wiki` 索引（使用 `wiki_brain` analyzer）：

```text
写入字段：page_id、title、content、aliases、trigger_questions、concept_id、
         page_type、status
（aliases / trigger_questions 拼接为文本参与分词检索，放宽召回入口；
  误命中由直答阶段的 sufficient 判断兜底，见步骤 4；
  page_type 只用于候选排序，见步骤 8「检索接入」，不参与打分）
概念页与主题页共用同一个索引，字段口径一致。
只索引 status=published 的页面；发布时写入，archived / needs_recompile 时删除。
```

## 实现步骤

### 步骤 1：候选产生（Study 侧，已定义）

Study 报告的 wiki_candidates（recommendation=ready）写入 `learning_results(action=wiki_candidate, status=pending_confirm, object_id=concept_id)`，见 study.md 步骤 6。

### 步骤 2：人工确认与编译触发（分析 → 确认 → 生成）

编译内部拆成两个 LLM 步骤（见 docs/design/wiki-compilation.md "编译内部分两步"）：先分析出拟采用的论断结构供人工查看确认，确认后自动接着生成正文。分析产物不落库——它只存在于 `POST /wiki/compile/analyze` 的请求-响应往返中，人工确认时由调用方原样带回，服务端不做任何持久化或过期管理。

```text
POST /wiki/compile/analyze
  请求：{ "concept_id": "...", "page_type": "topic|concept",
          "result_id": "..." }
  处理：
    1. 不改变任何状态（不置 wiki_candidate 为 applied，允许反复调用/重新分析）；
    2. 收集编译输入（见步骤 3「输入收集」），调用分析 Prompt
       （config/prompts/wiki_analyze.md），产出 claims / tensions
       结构（见步骤 3「分析产物」），做与生成阶段相同的
       cited_point_ids 白名单校验（越界剔除并记录 warn）；
    3. LLM 调用失败或校验后 claims 为空 → 500，不返回分析产物。
  响应：{ concept_id, page_type, result_id,
          claims: [{ summary, cited_point_ids, aspect_id? }],
          tensions: [{ description, related_point_ids }],
          readiness?: {...}  # 仅 concept 页，见下「人工指定主题手动编译」}

POST /wiki/compile
  请求：{ "concept_id": "...", "page_type": "topic|concept",
          "result_id": "...",
          "claims": [...], "tensions": [...]  # 可选，见下 }
  处理：
    1. result_id 对应的 pending_confirm wiki_candidate 置 applied
       （confirmed_by=manual）；无 result_id 时允许直接指定 concept_id
       编译（见下「人工指定主题手动编译」，2026-07-31 起是正式支持的第二条
       生成口径，不再只是"调试用途"）；
    2. 同 concept_id 已有非 archived 页面 → 拒绝（409），
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
唯一硬门槛（不可绕过）：该 concept 至少要有 1 条 qualifying KP（lifecycle=
  current 且已 verified 的 ActivationLink）——没有材料就没有页面，这条本来
  就是 analyze 阶段的既有校验（gatherAnalyzeInputs 在调 LLM 之前就检查），
  人工触发不豁免。

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

### 步骤 3：编译输入与 Prompt

**输入收集**：

```text
qualifying KP：该 concept 下同时满足以下条件的 KP（与 Study 候选口径一致，
  见 docs/design/wiki-compilation.md "ActivationLink 回答'这条管不管用'，
  Wiki 编译回答'这个主题够不够格立传'"）：
    lifecycle=current（KP 与所属 KU）；
    该 KP 存在对应 ActivationLink 且 status=verified
      （可靠性只由这一个状态判断回答——晋升本身已要求窗口内
      success_n / distinct_n 达标，见 study.md 步骤 5；不再叠加
      confident_count 次数门槛，那是用另一种计数方式重新验证
      verified 已经回答过的同一件事，不是 Wiki 本质需要的信息；
      candidate/weakened/deprecated 状态的 KP 不计入 qualifying）；
  confident_count 仍会取出（MAX(lc.confident_count)），但只作素材
  排序/展示用途（见下），不再是准入条件；
KU 正文：qualifying KP 所属 KU 按行号切片（单页输入合计 ≤
  wiki.compile_max_chars，默认 12000 rune，超出按 confident_count
  降序截取 KP——排序权重，不是门槛）；
KPN 关系：qualifying KP 之间的 relations（含 cross）；
knowledge_gaps：question_terms 与该 concept 名称/KP 内容有词项重合的
  gap 条目（作为"待验证点"素材）；
真实观测问法（docs/design/wiki-compilation.md "触发问法取材真实观测，检索
  匹配复用四元组"）：对每个 qualifying KP 的 point_id，查
  retrieval_quality='confident' 且 direct_point_ids 含该 point_id 的
  traces.question（同 study.md ConfidentTraceQuadruples 的查询口径，只是
  多取 question 原文字段），跨 KP 去重、按 point_id 打散取样，
  上限 wiki.trigger_questions_max（默认 10）条，作为生成
  trigger_questions 的素材——LLM 从这些真实问法里挑选/归纳，而不是凭
  materials 自由想象；某 KP 无确证 trace（理论上不会发生，qualifying 已
  要求 verified 链接，但防御性处理）时跳过，不影响整体编译。
```

**概念级 ready 判定**（Study 侧计算，决定是否写 wiki_candidate，见步骤 1；
对应 docs/design/wiki-compilation.md 拆出的三件事：广度与连贯、稳定，
可靠性已由上面的 qualifying KP 定义单独回答）：

```text
广度：qualifying_kp_count ≥ study.wiki_kp_min（同既有口径，不下调）；

连贯（口径修订——区分连接性质，不再只看数量）：
  related_connection_count ≥ 1（qualifying KP 之间 relation_type=related
    的连接数，即"这批知识彼此印证、互补"这件事本身要成立）；
  contradicts_connection_count < related_connection_count（矛盾类连接
    不能反客为主——不要求零冲突，冲突本就该如实呈现在页面"待验证点"里，
    只要求这批知识以互相印证为主导面貌，而不是矛盾占主导）；

稳定：qualifying KP 关联的激活事件覆盖 days_active ≥
  wiki.qualifying_min_days_active（衡量"这批理解经受住了时间考验"的
  时间跨度，不是"被问得勤"的频率——沿用已有 DaysActive 计算口径）；

内聚（P1 新增第五项，见 docs/impl/v1/wiki-generation.md 2.2/2.4、
  docs/design/wiki-compilation.md "连贯性判断还需要第三层"，已实现）：
  qualifying KP 的 Louvain 社区检测（`internal/foundation/graph`，边权
  = KPN related/contradicts 关系 + 共享 confident 问题共现，contradicts
  同样计正权）最大社区占比 ≥ wiki.concept_cohesion_min；
  不达标时（前四项均满足但内聚不满足）不写 wiki_candidate，改为在学习
  报告 concept_split_signals 节记录各簇成员与建议名，供人工判断是否需要
  拆分概念（不建 concept_candidates(kind=split) 行，split 候选仍属 V3，
  详见 concept-evolution.md）；wiki.concept_cohesion_min ≤ 0 时该项恒真
  （门禁关闭，仅 Stats.Cohesion 展示，不影响 recommendation）。

以上五项同时满足 → ready，否则 needs_more_data（其中前四项满足、仅内聚
不满足时额外写 concept_split_signals）。
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
根据以下知识点和原文材料，分析这个概念/主题值得沉淀为 Wiki 页面的
论断结构，不要写最终正文。

要求：
1. 只使用提供的材料，不引入材料之外的信息；
2. 每条 claim 是一个独立的稳定结论要点，标注其依据的 point_id
   （只能使用材料中出现的 point_id）；
3. 材料之间存在张力、或 gap 列表非空且与该概念相关时，写入 tensions，
   不要在这一步强行调和或替换为某个 claim。

概念：{{concept_name}}（{{concept_description}}）
知识点与原文材料：
{{materials}}
相关知识缺口：
{{gaps}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
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
根据以下已确认的论断结构和原文材料，把它组织成一个主题的 Wiki 页面正文。

要求：
1. 只使用提供的材料和论断结构，不引入材料之外的信息，
   不得引用 claims / tensions 之外的 point_id；
2. 页面结构固定为四节：## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源；
3. 稳定结论逐条对应输入的 claims，每条论断末尾以 [point_id] 标注该
   claim 已确认的 cited_point_ids；
4. tensions 非空时写入"待验证点"，不要强行调和；
5. "依赖来源"列出所用知识点所属的知识单元主题；
6. 额外输出检索触发信息：aliases（该概念的别名、缩写、常见口语叫法）
   与 trigger_questions（这个页面能够直接回答的 5-10 个典型问法）——
   trigger_questions 应从下方"真实观测问法"里挑选/归纳，不要凭材料
   臆造未出现过的表达方式；真实问法不足 5 条时才允许适度归纳补充，
   补充部分也要贴近已观测问法的措辞。

概念：{{concept_name}}（{{concept_description}}）
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
  lifecycle=current 但不满足 qualifying 条件（无 verified ActivationLink）
  的 KP 清单 [{ point_id, summary }]，编译与重编译时整体重算；
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

`page_type=concept` 与 `topic` 的差别仅在 title 生成提示（concept 页以概念名为题，topic 页允许模型按材料聚合主题命名），prompt 通过 `{{page_type_hint}}` 变量区分，不用两份文件。

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
（docs/design/wiki-compilation.md "触发问法取材真实观测，检索匹配复用
四元组"）：

  a. 词法入口：对问题分词后查询 wiki index（title/content/aliases/
     trigger_questions 均参与打分；TermQuery status=published 已由索引
     写入策略保证），取分数 ≥ retrieval.wiki_min_score 的页面按分数降序；
  b. 概念入口：问题分词结果与 concepts 表的概念名称做词法匹配
     （精确/包含，不调 LLM），命中概念存在 published 页面
     （wiki_pages.concept_id）→ 该页面直接进入候选，不看 Bleve 分数；
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

### 步骤 5：重编译

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
GET  /wiki/pages                 查询参数：status、concept_id、limit
                                 响应：[{ page_id, page_type, title, status,
                                          concept_id, compiled_at, published_at }]
GET  /wiki/catalog               按知识领域分组的 Wiki 目录（Page 模块 Wiki 视图）
                                 响应：[{ domain_id, name, description, wiki_count,
                                          pages: [{ kind, page_id?, page_type?,
                                            concept_id?, result_id?, title,
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
POST /wiki/topics                步骤 8，人工指定成员创建主题页壳
                                 请求：{ member_page_ids: ["...", ...] }
                                 响应：{ page_id, status, title, member_page_ids,
                                          readiness?: { member_count,
                                            related_connection_count,
                                            contradicts_connection_count,
                                            member_min, member_max } }
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
时机（两处，都是纯 SQL + 内存计算，无 LLM）：
  a. 概念页 publish 时：以该页 source_point_ids 为一侧，与其余 published
     概念页两两比对，全量重写该页涉及的 related / contradicts 行；
  b. Study 周期扫描：跨 Source KPN 新增后（kpn.md），只重算涉及新增
     knowledge_point_relations 的页面对，不做全库两两扫描。

判定（A、B 为两个 published 概念页的 source_point_ids 集合）：
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

**候选产生（Study 侧，周期扫描）**：

```text
1. 取全部 published 概念页 + wiki_page_relations 的 related 行构成无向图
   （忽略 contradicts 边），求连通分量；
2. 分量筛选（与概念级 ready 判定的连贯口径同源，见步骤 3）：
     成员数 ≥ wiki.topic_member_min（默认 3）；
     成员数 ≤ wiki.topic_member_max（默认 8）——**上限是必需的**：
       relation_kpn_min 默认 1，意味着两页只要有一对 KP 有 related
       关系就连边，这种低阈值的图在几百个概念页规模上几乎必然长出一个
       覆盖大半知识库的巨型分量，主题页会退化成「什么都装」的垃圾桶。
       超限时不产候选，写报告项 oversized_topic_cluster
       （成员数、代表页面、边数），提示人工提高 relation_kpn_min /
       relation_shared_point_min 或按 domain 切分——不自动切分，
       因为切在哪里是知识判断，不是阈值判断；
     分量内 contradicts 边数 < related 边数（矛盾不能反客为主）；
     分量内至少 wiki.topic_member_min 个成员尚未被任何非 archived
       主题页 contains（避免对同一批页面反复产候选）；
3. 满足 → 在一个事务里建 draft 壳页并写 contains：
     wiki_pages(page_type='topic', concept_id=NULL, title=占位（成员页
       标题拼接，编译时由 LLM 覆盖）, content='', status='draft',
       prompt_version / model_name 留空，compiled_at=NULL)；
     每个成员一行 wiki_page_relations(relation_type='contains',
       from=壳页, to=成员页, derived_from='compile')；
   壳页 content 为空但不会误入检索——索引只收 status=published；
4. learning_results(action='topic_page_candidate', object_type='wiki_page',
     object_id=壳页 page_id, status='pending_confirm')，reason 说明分量成员
     与 related / contradicts 边数。object_id 用 page_id 而不是概念 id 或
     成员集合指纹：标识天然唯一，人工确认的对象就是一个具体页面；
5. 人工驳回 → 壳页 archive + 删除其 contains 行 + learning_result 置
     rejected，不留悬空壳页。
```

**人工指定成员手动创建主题页（第二条生成口径，复用同一条二阶编译链路）**：
与概念页的「人工指定主题手动编译」同构——Study 产候选与人工挑选是同一条
analyze→confirm→generate 链路的两个入口，**不是两套生成逻辑**。人工不等
Study 推荐、直接挑选若干已发布概念页作为成员，调用
`POST /wiki/topics` 建 draft 壳页 + contains，再走下面同一组
`topic/analyze` + `topic/compile`：

```text
唯一硬门槛（不可绕过）：
  成员数 ∈ [topic_member_min, topic_member_max]（默认 3/8）；
  每个 member_page_id 必须存在、page_type='concept'、status='published'
  （与二阶编译「人工先子后父」一致——没有已发布概念页就没有主题页材料）；
  去重后写入；不接受主题页作成员（只有两层）。

Study 的连通分量 / contradicts < related / 「足够未归入成员」判定在人工
  触发时改为仅展示、不阻断：创建响应附带可选字段
  readiness = { member_count, related_connection_count,
  contradicts_connection_count, member_min, member_max }——信号照样算出来
  给人看，人工自己判断是否"够格"，不由系统替人工做决定。

来源留痕：壳页 compiled_from 存哨兵值 "manual_trigger"（与一阶人工编译
  同口径）；不写 topic_page_candidate learning_result（无 pending_confirm
  可驳回对象——壳页本身可直接 archive 清理）。

不新建编译管线：壳页建好后的分析/生成完全复用
  POST /wiki/pages/:id/topic/analyze|compile。
```

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
  wiki-compilation.md「证据回链必须穿透两层」）。
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
  （只有两层，见 docs/design/wiki-compilation.md「当前阶段只做两层」）。

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
    仍然只由编译产生（docs/design/wiki-compilation.md「复杂问题与写作：
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
  3. 统计排除：概念级 ready 判定、页面关系派生、连通分量计算都不计入
     被第 2 条跳过的边。回流 KP 本身仍正常参与激活、验证、与**其他**
     知识建立关系并可成为 qualifying——排除的只是自体祖先边，
     不是回流内容本身，否则回流就失去意义。

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
  topic_member_min:             3    # 主题页候选的连通分量最小成员数，
                                     # 也是重编译后剩余成员数的下限
  topic_member_max:             8    # 连通分量成员数上限，超限不产候选、
                                     # 只写 oversized_topic_cluster 报告项
                                     # （防止巨型分量把主题页变成垃圾桶）
  topic_compile_max_chars:      24000 # 二阶编译输入上限（成员页面正文合计）
```

## 依赖

```text
基础设施：SQLite、Bleve（新增 wiki 索引）、LLM client（reasoning 模型）
Study：   wiki_candidate 确认流、recompile_flag（study.md 步骤 6）
Lifecycle：SetUnitLifecycle → MarkNeedsRecompile 联动
Retrieval / Answer：Wiki 直答层接入（retrieval.md 第 0 层；
  answer_wiki 路径产出标准 AnswerResult，Trace 无感知差异）
Unit / Trace：编译输入的 KP / KU / 共现 / gap / 确证问题原文（只读）
Activation：verified 链接的 observed_conditions（编译时聚合写入
  wiki_pages.observed_conditions，检索时四元组入口复用其
  conditionGroupMatches 匹配逻辑，只读消费，不产生反向统计）
KPN：      跨 Source 关系新增后触发页面关系重算（步骤 7b）；
  页面关系的 related / contradicts 完全派生自 knowledge_point_relations，
  不新增关系类型（KPN 仍只有 related / contradicts、bidirectional）
Study：    主题页候选（action=topic_page_candidate）与 topic_decompose_signal
  的报告聚合（study.md 步骤 6 扩展）
Source：   草稿内容回流走既有 POST /sources 导入链路，wiki 侧不提供回写
```

## 完成标准

```text
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
related 连通分量满足成员数与「contradicts 边数 < related 边数」时，
  Study 产出 topic_page_candidate 并留下 draft 壳页 + contains 关系；
  人工驳回后壳页 archive、contains 行清空，不留悬空壳页；
人工指定成员（POST /wiki/topics）可跳过 Study 建壳：成员数落在
  [topic_member_min, topic_member_max]、且全部为 published 概念页时
  建 draft 壳 + contains，compiled_from 含 manual_trigger；连通性信号
  仅作 readiness 展示不阻断；随后复用 topic/analyze|compile；
连通分量成员数超过 topic_member_max 时不产候选，只出
  oversized_topic_cluster 报告项；
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
  该概念的 qualifying 计数与连通分量不因这次回流而增长；
  同一批回流 KP 与**其他**知识之间的关系照常建立。
```
