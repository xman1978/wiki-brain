# Retrieval 实现路径（V1）

## 职责

在 MVP 完整链路之上增加两个前置命中层与统一的生命周期过滤：

```text
Wiki 直答层   已发布 Wiki 页面命中 → 直接基于页面回答（见 wiki.md）
激活层        命中的观测条件按连续置信度分档服务（tier ∈ {exploring,
              self_graded, trusted}，见 activation.md「状态机」）：
              self_graded/trusted → 跳过过滤与召回，直达证据（快路径）；
              exploring → 小概率被本轮选中当一次真实试探，同样走快路径，
              未被选中则记 activation_hits、仍走慢路径（供积累证据）
完整链路      未命中时回落 MVP 链路：Domain → Source → Outline/FTS → RRF → Rerank（慢路径）
```

> **2026-08-13 编注**：以上一段沿用了本文档此前的表述习惯，把激活层命中分成
> "verified 直达证据"与"candidate 只记信号"两支。这个二元判断已经不存在——
> `activation.md`「状态机」把信任下沉到每一条观测条件，`status` 列（verified/
> candidate/deprecated）现在是从条件分数派生的缓存摘要，不是 Match 用来决定
> "服务还是不服务"的真相源。取而代之的是三档服务分档（exploring/self_graded/
> trusted），命中判定与本轮是否服务由 `activation.Match()` 一次调用内完成，
> 详见下方「步骤 2」的改写。旧的"verified 优先、candidate 只能补充信号"这句
> routing 表述，应理解为"self_graded/trusted 直接服务、exploring 按小概率
> 试探服务，三档命中都会记 activation_hits"。

> **熟路指针（2026-08-11，设计层面，非实现变更；2026-08-19 更正优先级表述**
> **以匹配下方"并行 + ActivationLink 优先"改判**）：`docs/impl/v1/activation-bundle.md`
> 提出的 ActivationBundle（熟路）目前已在实现中作为 ActivationLink 单链接 Match
> 遇到跨 unit 歧义时的仲裁层（见下方步骤 2），不是独立并行的第三层，因此当前
> 命中优先级是 ActivationLink/熟路（激活层，二者合一次判定）> Wiki 直答 > 慢
> 路径——不再是旧版"Wiki 直答 → 熟路 → 单链接"的顺序。熟路服务的是"一个问题
> 需要综合多个知识点才能回答"这类场景。ActivationBundle 文档「阶段 2」中
> `bundle_hits[]` 独立字段、Trace `bundle_success`/`bundle_failure` 事件等仍未
> 实现，具体接入点、是否影响 LLM 调用次数预算，还没有定案，需要单独评估后再
> 回来修订本文档，见 activation-bundle.md 步骤 4「匹配器契约」。

所有路径在 Rerank（或快路径的证据组装）之后统一经过证据挖掘（见 `evidence.md`），再进入充分性判断与 EvidenceSet 构建。

目标：熟悉问题的 LLM 调用从 ≥4 次降至 2-3 次（2026-08-12 改判：撤销 2026-08-11 把区间上沿从 3 调到 4 的修订——那次修订是为了容纳激活层 Match 第二轮模型辅助匹配可能额外触发的一次调用，该机制已整体撤销，见 activation.md 步骤 2，Match 恢复为纯程序精确匹配，快路径 LLM 调用数回到固定值，不再有"3 或 4 次"的分支），且快路径错误命中不直接出口（命中后经证据充分性校验把关）。

## 检索总流程（V1）

> **2026-08-19 改判**：以下第 0 层（Wiki 直答）与第 1 层（激活层 Match）此
> 前是串行——先等 Wiki 直答出结果，未命中才跑激活层。Wiki 直答的
> Concept/Fact 识别要调 LLM，比纯程序、免费的 ActivationLink Match 慢得
> 多，串行会让本该被激活层秒回的问题被 Wiki 的 LLM 调用拖慢。改为**两层
> 并行发起**，都返回后按优先级挑选结果：**ActivationLink 命中优先于 Wiki
> 直答**（同时命中时激活层更快、更可信；命中 Wiki 而激活层未命中则用 Wiki
> 直答结果；都未命中落入第 2 层慢路径）。这意味着即便本轮最终命中/使用的
> 是 ActivationLink，Wiki 直答的 LLM 调用仍然会发生（并被丢弃）——用一次
> 可能浪费的调用换取延迟下限，是本次改判的核心权衡。见
> `internal/retrieval/service.go` `RetrieveWithProgress`。

```text
问题（经 Session 补全，沿用 MVP）
  ├─ 第 0 层与第 1 层并行发起（2026-08-19 改判，见上方编注）：
  │
  │  第 0 层：Wiki 直答候选采集（三个入口，见 wiki.md「检索接入」；
  │    2026-08-18 单层化改造后，其中一个入口改为调用 LLM，不再是"三个入口
  │    全部不调 LLM"）
  │    a. Concept/Fact 识别：一次 LLM 判断问题主要涉及哪个/哪些已发布词条
  │       （matchEntriesByConceptRecognition，取代原四元组精确匹配入口，
  │       domain 候选列表为空时跳过、不调用）；
  │    b. 词法：wiki index 查询（title/content/aliases/trigger_questions），
  │       分数 ≥ wiki_min_score；
  │    c. 概念名词法包含：问题字面包含已发布页面的概念名称 → 直接入候选；
  │    优先级：Concept/Fact 识别命中 > 词法（分数降序）> 仅概念名包含命中；
  │    合并去重取前 wiki_max_candidates 个，按序直答尝试，
  │    某页 sufficient=true → Wiki 直答路径（path_type=wiki）候选就绪；
  │    候选耗尽或为空 → 本层判定为未命中
  │      （单层化改造后 Wiki 不再有"主题页聚合概念页成员"这一层，骨架
  │      注入慢路径与 topic_decompose_signal 已整体删除，见
  │      wiki-single-tier-open-questions.md「已拍板（2026-08-18）」）
  │
  │  第 1 层：激活层 Match（四元组精确匹配，纯程序、免费；2026-08-12
  │    改判撤销此前"未命中且有候选组时升级为模型辅助匹配"的第二轮，
  │    Match 全程不调 LLM，见 activation.md 步骤 2）；命中后按该条件
  │    当前的服务分档（tier，见 activation.md「状态机」）决定本轮走向
  │    ——2026-08-13 起 Match 输出不再是二元的"命中/未命中"，而是三种
  │    每条命中条件各自独立的结果（见下方「步骤 2」）：
  │      self_graded/trusted → 本轮直接服务，走快路径；
  │      exploring → 本轮按 explore_rate_low 概率被选中当一次真实试探，
  │        中选同样走快路径，未中选则只记 activation_hits、落入未命中
  │        分支（等价于本轮当作未命中处理）；
  │    快路径：
  │      链接目标 KP → 反查 KU（lifecycle=current）→ 直接构建 direct 候选
  │      → KPN 扩展补充 supporting（沿用 MVP 步骤 8）
  │      → 证据挖掘（1 次 LLM）
  │      → 快路径校验（1 次 LLM，见步骤 2a）：证据能否独立完整回答问题
  │         不通过 → 回落慢路径（步骤 7）
  │      → 充分性判断 → EvidenceSet(path_type=fast)
  │      → Answer（1 次 LLM）        共计 3 次 LLM 调用（Match 本身不调用
  │                                  模型，2026-08-12 改判后不再有 4 次分支）
  │      → 若本次命中被 Match() 判定 audit_sampled=true（self_graded/
  │        trusted 档各自按 explore_rate_self_graded/explore_rate_trusted
  │        概率抽样，见 activation.md「服务分档」），在快路径答案已经
  │        返回给用户之后，异步触发一次独立核实试验（见步骤 2c）——
  │        不阻塞、不延迟本次已经返回的回答
  │
  │  两层都返回后合并（2026-08-19 改判，见上方编注）：
  │    激活层判定为命中（含 exploring 中选）→ 用激活层结果，Wiki 直答结果
  │      即便也命中也一并丢弃；
  │    激活层未命中、Wiki 直答判定为命中 → 用 Wiki 直答结果；
  │    两层都未命中 → 落入未命中分支
  └─ 未命中 → 慢路径（MVP 完整链路）：
       Domain 预过滤 → Source 过滤 → Outline/FTS 召回 → RRF → Rerank
       → KPN 扩展 → 证据挖掘 → 充分性判断 → EvidenceSet(path_type=full)
       → Answer
```

## 配置项（config.yml: retrieval 节扩展）

```yaml
retrieval:
  # —— MVP 既有配置保留 ——
  outline_fts_min_score: 0.5
  rerank_top_n:          20
  # —— V1 新增 ——
  fast_path:              true   # false 时激活层只记录命中、不走快路径（灰度开关）
  activation_match_top:   5      # 激活层最多采用的链接数
  fast_path_verify:       true   # 快路径证据充分性校验开关（false 时跳过校验，
                                  # 仅灰度/评估用，生产不建议关闭）
  slow_path_verify:       true   # 慢路径答题前充分性校验（步骤 2b）；false 时跳过
  fast_path_fallback:     true   # 快路径回答失败时自动回落慢路径
  wiki_min_score:         2.0    # Wiki 直答的 Bleve 最低分（BM25，需评估集校准）
  wiki_max_candidates:    3      # 直答候选序列长度上限（依次尝试，
                                  # 首个 sufficient=true 即停；设 1 退化为原 top-1 行为）
  # activation_match_model_enabled 已删除（2026-08-12 改判：激活层 Match
  # 第二轮模型辅助匹配整体撤销，见 activation.md 步骤 2；对应的
  # RetrievalConfig.ActivationMatchModelEnabled 字段已从代码移除）
```

## 实现步骤

### 步骤 1：EvidenceSet 契约扩展

```text
EvidenceSet 新增：
  path_type            fast / full / wiki
  activation_hits[]    [{ link_id, point_id, match_score, matched_by }]
                        matched_by: 恒为 exact（2026-08-12 改判：`model`
                        取值随第二轮模型辅助匹配一并撤销，`MatchedByModel`
                        常量已从代码移除，字段本身为 API/schema 稳定性保留，
                        见 activation.md 步骤 2）
  bundle_hits[]        [{ bundle_id, member_point_ids[], match_score,
                        matched_by }]（2026-08-12 定案，阶段 2/熟路命中
                        用，见 activation-bundle.md 步骤 4；独立数组，不
                        并入 activation_hits[]——熟路是「一个 bundle_id
                        对一组 point_id」，跟 activation_hits[] 天然的
                        1:1 形状不同，硬塞进同一个数组要么用 link_id
                        装 bundle_id（字段语义对不上，下游按 link_id
                        反查 activation_links 表会查错表），要么加一个
                        可空字段让每处消费方都得先判断来源，两种都不如
                        分开干净；path_type 不新增取值，靠 bundle_hits[]
                        是否非空区分命中来源是链接还是熟路，见 trace.md
                        步骤 3 熟路指针；本字段本身仍是「阶段 2」范围，
                        Match/候选构建的实际接入未实现，这里只定字段形状）
  gap_reason           no_candidates / judge_filtered（空字符串＝非 gap 结果；
                        产出位置见步骤 6，消费方见 study.md「knowledge_gaps 表扩展」）
  filtered_evidence[]  结构同 Evidence，role 固定为 "irrelevant"；
                        rerank judge 判定不相关而被剔除的候选快照（产出位置见步骤 6）

EvidenceItem 新增：
  mined              bool（证据挖掘产出，见 evidence.md）
```

`path`（short/deep）、`fact_id`、`source_ref` 等既有字段语义不变。Answer 层无需改动：仍按 path 分发、按 fact_id 校验 citation；evidence_snapshot 自然携带新字段——`filtered_evidence` 与 `gap_reason` 也会随整个 EvidenceSet 一起序列化进 `answers.snapshot`，不需要 Answer/Trace 额外处理存储。

### 步骤 2：激活层（快路径证据构建）

上游 Session 合并定域+Parse 产出的 `domain_ids` 经 `QueryContext` 传入时：
- 快路径 Match 前按 Source.domain 过滤候选 link（`domain_ids` 空则不过滤）；
- **问题四元组归一化（2026-08-12 新增，config-gated，默认关闭）**：
  `session_normalize_tuple.md` 那次"Match 前盲改一遍四元组再赌重新匹配"
  的二次规范化调用仍然维持废弃（结论不变：直接精确匹配不需要先盲改）。
  这里恢复的是一个不同的机制——`activation.TupleNormalizer`（`internal/
  activation/tuplenorm.go`），在 `tryFastPath` 构造 `expandedQuery` 之前、
  `activation.Match` 之前跑，只在 `cfg.Retrieval.QuestionTupleNormEnabled=
  true` 且 Session 已解出非空 `domain_ids` 时生效：
  1. Tier 1 精确匹配：四元组（`text.Normalize`/`Terms`/`NormalizeCompact`
     归一化后）与 `question_tuple_norms` 表按 domain_id 查全字段相等，
     命中即用该行落库的 canonical 四元组替换，免费；
  2. Tier 2 本地词集 Jaccard 相似度：对该域下最近命中的候选逐字段算
     token-Jaccard 取四字段均值，达到 `question_tuple_norm_local_sim_min`
     即命中，免费；
  3. Tier 2.5 向量早筛（`vector_match_enabled` 独立开关，默认关闭）：
     goformer 本地 embedding 算余弦相似度，**只用于提前拒绝**——低于
     `vector_match_sim_min` 直接判未命中（连 LLM 都不试），达到或高于
     阈值仍然要进 Tier 3 交给 LLM 判断，向量分数从不单独确认命中（刻意
     的非对称设计：宁可多问一次 LLM，也不让向量分数直接拍板"是同一个
     问题"）；
  4. Tier 3 LLM 批量判断（`config/prompts/tuple_norm_match.md`）：把
     Tier 2（或 Tier 2.5 幸存）候选整批传给模型，一次调用给出匹配/
     不匹配 + 候选下标；audience/constraint 要求比 subject/intent 更严格
     的等价判断（不臆测不同受众/条件是一回事）；
  5. 全部未命中：以当前四元组为新的 canonical 记录写入
     `question_tuple_norms`（按 `domain_ids` 逐个域各插入一行）。
  消费入口（`activation.Matcher`/`BundleMatcher`）本身仍是纯精确匹配，
  不受影响——这一层只改变喂给它们的四元组，不改动匹配逻辑本身。
  （`wiki.matchFourTupleEntry` 已随 2026-08-18 单层化改造删除，不再是
  归一化的消费方之一——Wiki 检索侧的 Concept/Fact 识别入口是语义识别，
  不消费四元组，见 wiki.md「检索接入」；此处归一化机制仅服务于
  activation.Matcher/BundleMatcher 两个消费方。）
- **Match 本身（硬性过滤 + 精确匹配）不再有第二轮模型辅助匹配**
  （2026-08-12 改判，撤销 2026-08-11 引入的两级结构，见 activation.md
  步骤 2）；不区分"首次/非首次提问"，一律走同一套纯程序流程；
- 慢路径 Domain 预过滤直接复用 `domain_ids`，不再调用 `question_domain_match`（空则全库）。

```text
1. 调 activation.Match(expandedQuery) 得 LinkMatch 列表（≤ activation_match_top），
   每个 LinkMatch 携带 { link_id, point_id, match_score, matched_by, tier,
   mean, auditSampled }（见 activation.md 步骤 2「置信度分档判定」）；
   输入是 Session 产出的完整 ExpandedQuery（expanded_question + 四元组），
   不是用户原始输入——匹配含 audience / constraint 硬性守门与
   subject / intent 精确相等，规则见 activation.md 步骤 2；
   Match 返回全部 KP lifecycle=current 的命中条件，每条已经带有本轮是否
   服务的判定结果（2026-08-13 起不再是"verified 直达证据 / candidate
   只记信号"这一二元判断，见「职责」编注）：
     tier ∈ {self_graded, trusted} 的命中 → 本轮直接服务；
     tier == exploring 的命中 → Match() 内部已按 explore_rate_low 掷过
       一次 Bernoulli，中选的才会出现在返回列表里（未中选的条件本身
       "未产出该条件的 LinkMatch"，等价于未命中，不会混入下面的逻辑，
       见 activation.md 步骤 2「置信度分档判定」）；
   为空 → 慢路径；fast_path=false → 记录命中日志后仍走慢路径
   （activation_hits 照常写入 EvidenceSet，供灰度期观察命中质量）；
   命中反查后对应 **多个不同 unit** → 视为歧义，回落慢路径
   （命中分数恒为 1.0，没有排序依据；跨 KU 打包进快路径 direct 等于免检。
   同 unit 上多条本轮服务的命中 / 多个 point 不视为歧义——证据仍是同一 KU
   正文，可走快路径；2026-08 修订，见 plan-parser-vocab-and-unit-ambiguity.md）；
   跨 unit 歧义时 TouchLastUsed 不触发，但 activation_hits 仍照常写入
   （含 tier=exploring 未中选、仅记信号的命中），Trace 按普通快路径未命中
   一样评分；
   （2026-08-12 实现，取代上面"熟路指针"的待定案状态：跨 unit 歧义时不再
   直接回落慢路径，先consult ActivationBundle——`resolveBundleForAmbiguousHits`
   调 `activation.MatchBundles` 用同一个 `expandedQuery`/`matchCfg`：
   (a) 命中一个及以上 verified Bundle → 用其（合并后的）核心成员点集，
   跳过单 unit 限制，直接进入候选构建/证据充分性判断；命中多个 verified
   Bundle 时先用 `retrieval.Store.GetKPNConflicts` 做核心成员两两冲突判定
   （复用慢路径既有的 KPN `contradicts` 判定原语），任意一对冲突就仍判定
   为歧义、回落慢路径，不冲突则取并集合并使用，**不**因此新建 Bundle；
   (b) 没有 verified Bundle 覆盖 → 从这次观测实时新建/加强一条 candidate
   Bundle（`formCandidateBundle`，核心成员=本次命中点集，去重复用已有相同
   成员集合的 Bundle 而非重复创建），仍回落慢路径，只是多了这个副作用，
   为将来同样的歧义提供可匹配的 Bundle 素材。这是 Bundle 消费侧的部分实现
   ——只覆盖了"多链接歧义时查/建 Bundle"这一入口，`bundle_hits[]`
   独立字段、Trace 的 `bundle_success`/`bundle_failure`、Bundle 自己的
   `adopt_count`/`known_question_terms`/`auto_promote` 仍未实现，见
   activation-bundle.md 对应记录。**2026-08-13 编注（创建门槛，见
   `docs/design/activation-convergence.md` 第 11 节）**：`formCandidateBundle`
   这里第一次遇到就直接创建，不额外加 Beta 均值/宽度门槛（对照 Link/
   离线聚类新增的 create_confidence_min/create_width_max，见 study.md
   步骤 1）——这条路径本身的触发条件（两条独立的、已各自服务过/自证过
   的链接同时命中、却分属不同知识单元）已经是一个足够高的准入门槛，
   能走到这一步不是噪声，再叠加一层次数/比例检查是画蛇添足）；

2. 取命中链接的 point_id → 反查所属 KU：
     SELECT ... FROM knowledge_points p JOIN knowledge_units u ...
     WHERE p.point_id IN (?) AND p.lifecycle='current' AND u.lifecycle='current'
   全部反查为空（理论上不发生，Match 已过滤）→ 慢路径；

3. 按 unit_id 去重构建候选，role=direct，point_id=命中链接的 point_id，
   content 按 markdown_path + line_start/line_end 切片（沿用 MVP 规则）；
   跳过 Rerank——批量候选分类不需要；但快路径命中不等于证据仍然充分
   （KP 内容可能已更新、问题可能带四元组未捕捉的细节），正确性由
   步骤 2a 的单次校验把关，不再依赖 activation_failure 事后回流作为
   唯一防线；

4. KPN 扩展沿用 MVP 步骤 8（对 direct KU 查邻居 KP，role=supporting，
   邻居 KP 及其 KU 均要求 lifecycle=current）；

5. 异步 TouchLastUsed(本次用于快路径的 link_ids，含 self_graded/trusted 直接
   服务与 exploring 中选试探两类)，不阻塞请求。
```

### 步骤 2a：快路径校验（fast_path_verify）

证据挖掘完成后、充分性判断之前，对快路径证据做一次轻量 LLM 校验：

```text
输入：expanded_question + 全部快路径证据（direct + supporting，
      挖掘后的片段；整段回退时用整段）
Prompt：config/prompts/fast_verify.md，输出 JSON
      { "sufficient": true/false, "reason": "..." }
判定：sufficient=false 或调用失败/解析失败 → 视为快路径失败，
      触发步骤 7 回落（保守：校验环节任何异常都不放行）；
      sufficient=true → 继续充分性判断与 EvidenceSet 构建。
```

校验失败照常保留 activation_hits 进入慢路径 EvidenceSet，Trace 据此产生 activation_failure（机制同步骤 7），校验结果反馈进学习回路。

fast_path_verify=false 时跳过本步骤，直接进入充分性判断（行为与旧版一致，仅供灰度/评估对照，生产环境不建议关闭）。

### 步骤 2b：慢路径校验（slow_path_verify）

慢路径（`path_type=full`）在 Answer 生成之前做与步骤 2a **同构**的充分性校验（复用 `config/prompts/fast_verify.md`、classification 模型）：

```text
调用点：Answer.generate / AnswerStream，在已有 direct/supporting 证据、
      即将调用 answer_short/answer_deep 之前（Wiki/快路径不经过本步骤——
      各自已有 sufficient 闸门）。
输入：question + 全部证据（direct + supporting，挖掘后的片段或整段回退）
判定：sufficient=false → 拒答，返回与无证据兜底相同的固定话术
      （HasAnswer=false、citations 空、Path=none），EvidenceSet 原样保留
      供 Trace/审计观察「召回了什么但判定不够答」；
      调用失败/解析失败 → 放行继续生成（慢路径已无下一层可回落，
      与快路径「校验异常即回落」不对称，避免校验抖动导致大面积拒答）。
```

动机：慢路径 Rerank 常把「近邻相关」材料标成 direct（问 OA 填单召回易快报流程、问升级召回部署步骤）。答题模型在小参数下容易把近邻概念改写成所问概念的操作答案；充分性闸门输出的是结构化 `sufficient` 布尔，比依赖答题 Prompt 自守门更稳。

`slow_path_verify=false` 时跳过，行为与改前一致（灰度/对照用）。

### 步骤 2c：独立核实试验编排（audit-trial sampling，2026-08-13 新增）

`activation.md`「服务分档」把每条 self_graded/trusted 命中额外掷一次
`Bernoulli(explore_rate_self_graded)`/`Bernoulli(explore_rate_trusted)`，
决定 `LinkMatch.auditSampled` 是否为真——本步骤是这个标志位在 Retrieval
侧的消费方，也是 `docs/design/activation-convergence.md` 第 4 节「打破
自证循环」要求的具体落点：只让快路径自己验证自己，置信度再连续也可能
稳定收敛到一个错误结论上，需要偶尔拿一个独立跑出来的慢路径结果做对比。

```text
触发时机：步骤 2 之后、答案已经通过步骤 2a 校验并返回给用户之后
  （不阻塞、不延迟本次已经返回的回答——这是"审计"而非"校验"，校验
  失败会拦答案，审计失败只影响这条观测条件未来的置信度，不影响
  已经发出的这次回答，见 trace.md 步骤 3b）；

触发对象：本次 activation_hits 中 auditSampled=true 的每个
  (link_id, point_id) ——通常只是本次命中里的一小部分（低概率抽样），
  不是全部命中都会触发；

编排方式：Retrieval 另起一个不阻塞当前 HTTP 响应的后台任务（与
  TouchLastUsed 的异步落库不同，这里的后台任务要跑一次完整的慢路径
  检索，成本是一次全量慢路径调用，见下方「LLM 调用预算对照」），
  输入是同一个 expandedQuery，强制忽略激活层、直接从 Domain 预过滤
  开始走 MVP 完整链路，得到一份独立的 direct_point_ids；

比对与回写：后台任务完成后，把 (link_id, point_id, 独立慢路径的
  direct_point_ids) 交给 Trace（不是本模块自己写 learning_events 或
  调用 RecordAuditOutcome）——比对规则、事件类型
  （activation_audit_success / activation_audit_failure）、
  RecordAuditOutcome 的调用时机与失败处理，均由 trace.md 步骤 3b
  统一定义，Retrieval 只负责"触发"和"提供独立慢路径结果"这两件事，
  不重复实现比对逻辑；

失败处理：后台慢路径检索本身失败（超时、异常）→ 记录 warn 日志，
  不产生 activation_audit_* 事件（宁可少一次审计样本，也不能把一次
  基础设施故障误记成"独立核实认为这条条件不可信"）；

与 fast_path_verify（步骤 2a）的关系：两者都可能对同一次命中触发，
  互不影响——步骤 2a 是"这次证据够不够回答这个问题"的同步闸门，本步骤
  是"这条长期被信任的条件，独立重新走一遍会不会得到同样的结论"的异步
  抽查，回答的是两个不同的问题，共用同一个 expandedQuery 不代表可以
  合并成一次调用。
```

### 步骤 3：证据挖掘接入

快慢路径在候选确定后统一调用证据挖掘模块（`evidence.Mine`，见 evidence.md）：

```text
输入：question + subject/intent（Session 解析产出）+ 全部候选（direct + supporting）
输出：片段级 EvidenceItem 列表（挖掘失败的 KU 整段回退，mined=false）
evidence.enabled=false 时跳过，行为与 MVP 一致（整段证据）。
```

### 步骤 4：充分性判断与 EvidenceSet 构建

```text
充分性规则沿用 MVP（direct 非空 → short，否则 deep）；
快路径补充一条：direct 候选挖掘后片段全部为空且整段回退也为空
  （KU 正文读取失败等异常）→ 视为快路径失败，触发步骤 6 回落；
fact_id 分配：挖掘产出的每个片段一个 fact_id（见 evidence.md 步骤 4），
  其余构建逻辑沿用 MVP 步骤 10；
写入 path_type 与 activation_hits。
```

### 步骤 5：生命周期过滤（慢路径改动点清单）

慢路径逻辑不变，以下位置统一追加 lifecycle 过滤：

```text
units index / points index 查询：conjunction TermQuery(lifecycle=current)；
outline 召回取节点下 KU：SQL 追加 u.lifecycle='current'；
points→unit 反查、代表 KP 选取：追加 p.lifecycle='current'；
KPN 扩展邻居：邻居 KP 与其 KU 均要求 current；
Rerank 候选 content 切片前再校验一次 KU lifecycle（防扫描间隙状态变更）。
```

此外，MVP 沿用的 Domain 预过滤与 Source 语义过滤（`mvp/retrieval.md` 步骤 2-3）需追加 `sources.shadow_of IS NULL`，排除 reupload 期间正在处理的影子 Source——影子在换血完成前不应作为一个独立来源参与检索（见 `lifecycle.md` 步骤 2 的 Shadow Source 机制）。

### 步骤 6：Gap 诊断字段（gap_reason / filtered_evidence）

仅慢路径产出（快路径命中即为 direct，不存在这两个字段的取值场景）；用于 study.md 定位「检索不到」还是「检索到但被判无关」两类知识盲区，见 study.md「knowledge_gaps 表扩展」。两处改动点：

```text
1. RRF merge（MVP 步骤 6）合并结果为空时：
     gap_reason = "no_candidates"
     filtered_evidence 保持空（没有候选可判，无内容可展示）
     直接按 MVP 既有逻辑返回空 EvidenceSet（行为不变，只多写一个字段）

2. Rerank（MVP 步骤 7 / evidence.md 证据挖掘之前）：
     judge 返回 role="irrelevant" 的候选，原逻辑直接丢弃；
     现追加：按候选原有的 content 切片规则（同 direct/supporting 一致）
       构建 Evidence（role="irrelevant"，fact_id 留空——未挖掘、不参与 citation
       校验，仅供人工查看），追加进 filtered_evidence；
     数量上限＝rerank_top_n（与候选池同一上限，不单独配置）；
     若充分性判断后 direct、supporting 均为空（步骤 4）：
       gap_reason = "judge_filtered"
     否则（仍产出了 direct 或 supporting）：
       gap_reason 留空——不是 gap，filtered_evidence 仍然保留供参考，
       但不影响 study.md 的 gap 聚合（quality != gap 不产生 knowledge_gap 事件）。
```

`answer_error`（检索有证据、LLM 生成失败）不在此步骤产出——Trace 层直接读 `AnswerResult.Path == "error"` 判定，无需 retrieval 提供额外字段（见 trace.md）。

### 步骤 7：快路径回落

```text
触发条件（fast_path_fallback=true）：
  a. 步骤 2a 校验不通过（sufficient=false 或校验异常）；
  b. 步骤 4 判定快路径失败；
  c. Answer 完成后 has_answer=false 或 citations 为空
     （Answer 层回调 Retrieval 提供的 fallback 句柄）；

回落执行：以原问题走一遍慢路径并重新生成回答，最终返回慢路径结果；
  本次 trace 记录 path_type=full，activation_hits 保留原命中
  （Trace 据此产生 activation_failure，见 trace.md 步骤 3——
   命中的 KP 不在最终 direct_point_ids 中即判 failure）；
回落只执行一次，慢路径结果不再回落；
回落发生记录 warn 日志（link_ids、原因），供灰度期监控快路径质量。
```

### 步骤 8：HTTP API

```text
POST /retrieval（既有）
  响应 EvidenceSet 增加 path_type / activation_hits / mined /
    gap_reason / filtered_evidence 字段；
  新增请求参数 { "force_full": true } 强制走慢路径（调试与对照评估用）。
POST /answer（既有，见 answer 模块）
  响应增加 path_type 字段（Page 显示路径标识用）。
```

## LLM 调用预算对照

```text
慢路径（MVP + 挖掘）：domain(1) + source(1) + outline(0~N) + rerank(1)
                      + mining(1) + answer(1)               ≈ 5~6 次
快路径：              match(0) + mining(1) + verify(1)
                      + answer(1)                            = 3 次
                      （2026-08-12 改判撤销激活层 Match 第二轮模型辅助
                      匹配，Match 不再消耗调用预算，固定值取代此前
                      "3~4 次，多数 3 次"的区间口径）
                      问题四元组归一化（同日新增，默认关闭）额外只在
                      Tier 1/2 都未命中时才付一次 Tier 3 LLM 调用：多数
                      重复问答仍是 0 次额外调用（Tier 1 命中）或 3 次，
                      Tier 1/2 双未命中时 +1 次 → 4 次。
Wiki 直答：           entry_recognize(0~1) + answer(0~wiki_max_candidates)
                      典型 2 次（识别命中 1 次 + 回答 1 次），最坏 4 次
                      （sufficient=false 换下一候选页重试）；entry_recognize
                      是 2026-08-18 单层化改造新增的 Concept/Fact 识别调用
                      （docs/impl/v1/wiki-single-tier-task-brief.md 步骤 4），
                      取代原四元组精确匹配（不调用 LLM）的直答入口——当前
                      域下没有任何候选 entry（如全新领域）时跳过，不产生
                      这次调用；命中 0 个 entry 或全部 entry 都没有已发布
                      页面覆盖时，仍会退回 wiki 索引词法匹配 / 概念名词法
                      匹配两个不调 LLM 的入口，不代表直答整体失败。
独立核实试验（2026-08-13 新增，见步骤 2c）：
                      不改变已发出回答的调用预算——审计是后台、异步的，
                      不阻塞当前请求，因此上面「快路径 = 3 次」这个数字
                      对用户实际等到的这次请求仍然成立；但它在服务器
                      总体调用量上是真实的额外开销，按抽样比例摊到
                      "被命中的快路径流量"这个分母上：
                        +1 次完整慢路径调用（domain(1)+source(1)+
                        outline(0~N)+rerank(1)+mining(1) ≈ 4~5 次，
                        不含 answer——审计只需要 direct_point_ids 做
                        比对，不需要真的生成一段回答文本）
                        × explore_rate_self_graded（self_graded 档命中）
                        或 explore_rate_trusted（trusted 档命中，通常
                        更低，是定期复查而非持续验证）
                      配置默认值下（explore_rate_self_graded=0.10、
                      explore_rate_trusted=0.03，见 activation.md「配置
                      项」）：多数快路径流量不触发审计，触发的那一小部分
                      各自额外产生约 4~5 次调用，不产生 answer 调用。
```

## 依赖

```text
Activation：Match 匹配器（2026-08-12 改判后恢复为纯程序、不调用模型，
            2026-08-13 起返回值扩展为每条命中带 tier/mean/auditSampled，
            见 activation.md 步骤 2）、TouchLastUsed
Evidence：  Mine 接口（见 evidence.md）
Lifecycle： Bleve lifecycle 字段与 SQL 过滤条件
Wiki：      wiki index 查询与直答路径（见 wiki.md；未实现时第 0 层跳过）
Session / Answer / Trace：契约扩展见各自文档，链路顺序不变
Trace：      独立核实试验的比对与回写方（2026-08-13 新增，见步骤 2c）——
            本模块只触发后台慢路径检索、把独立结果交给 Trace，比对规则、
            activation_audit_success/failure 事件产生、
            activation.RecordAuditOutcome 调用均由 trace.md 步骤 3b 负责
Study：      gap_reason / filtered_evidence 消费方，见 study.md「knowledge_gaps 表扩展」
```

## 完成标准

```text
tier ∈ {self_graded, trusted} 的命中存在时同类问题走快路径，LLM 调用 ≤ 3 次
  （日志可验证；2026-08-12 改判撤销第二轮模型辅助匹配后固定为 ≤ 3 次，
  不再有 4 次分支，见 activation.md 步骤 2）；tier=exploring 的命中按
  explore_rate_low 概率同样走快路径，未中选的按未命中处理（测试用例：
  固定随机源下分别断言中选/未中选两条分支）；
四元组任一维度不等时不走快路径（行为与无链接一致）；
无链接命中时行为与 MVP 慢路径一致（加 lifecycle 过滤与挖掘）；
fast_path=false 时全部走慢路径但 activation_hits 照常记录；
命中反查后对应多个不同 unit 时不走快路径、回落慢路径，activation_hits 仍记录
  全部命中链接，且不触发 TouchLastUsed；同 unit 多 link 可走快路径；
force_full=true 强制慢路径生效；
audit_sampled=true 的命中在快路径答案返回给用户之后，异步触发一次独立
  慢路径检索，不阻塞、不延迟本次请求（测试用例：mock 后台任务，断言主
  请求的响应时间不受影响）；比对结果正确交给 Trace 产生
  activation_audit_success/failure 并调用 RecordAuditOutcome（见
  trace.md 完成标准，本文档只验证"触发了、且不阻塞"这一半）；
  后台慢路径检索本身失败时记 warn、不产生审计事件；
audit_sampled=false 的命中不触发步骤 2c；
fast_path_verify=true 时校验不通过自动回落慢路径且只回落一次，
  trace 记录 path_type=full、activation_hits 保留（产生 activation_failure）；
fast_path_verify=false 时跳过校验，行为同旧版快路径；
slow_path_verify=true 时 Answer 在生成前校验不通过则拒答（Path=none、
  HasAnswer=false），EvidenceSet 保留供审计；校验调用失败则放行生成；
slow_path_verify=false 时跳过，慢路径行为与改前一致；
快路径回答失败自动回落慢路径且只回落一次，trace 记录 path_type=full；
superseded/deprecated 的 KU/KP 不出现在任何路径的候选与证据中；
EvidenceSet 的 path_type / activation_hits / mined 正确传递至
  evidence_snapshot 与 Trace；
RRF merge 为空时 gap_reason="no_candidates"，filtered_evidence 为空；
rerank 后 direct/supporting 均为空时 gap_reason="judge_filtered"，
  filtered_evidence 含全部被判 irrelevant 的候选（unit_id/content/source_ref 齐全）；
产出了 direct 或 supporting 时 gap_reason 留空（即使 filtered_evidence 非空）；
对照评估：同一问题集 force_full 与快路径的 direct 命中率对比可产出
  （评估脚本遍历问题集分别请求两种路径，比较 direct_point_ids）；
fake LLM 下快路径、快路径校验不通过回落、慢路径、灰度开关四类场景测试稳定运行。
```
