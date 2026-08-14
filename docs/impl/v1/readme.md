# Wiki-Brain V1 实现概览（能力提升版）

## 定位

MVP 已验证核心假设：知识可组织、检索可命中、回答可追溯、检索信号可积累为激活路径候选。但 MVP 的学习是「只看不动」的——Study 只输出报告，候选不改变系统行为，检索每次都走完整链路。

V1 的目标是让系统**基本具备学习转化能力**：

```text
使用信号（检索事件）
  -> 转化为长期记忆结构（ActivationLink、Wiki 初版）
  -> 反过来改变检索与回答行为（激活路径优先、LLM 调用减少、同类问题更快更稳）
```

一句话概括：MVP 验证「信号能积累」，V1 实现「积累能转化、转化能生效」。

设计依据：`docs/design/study.md`（检索事件驱动学习）、`docs/design/precompile.md`（ActivationLink）、`docs/design/retrieval.md`（分层检索）、`docs/design/evidence-mining.md`（片段级证据挖掘）、`docs/design/lifecycle.md`（记忆状态，V1 完整实现——3 状态模型即完整设计，非子集）、`docs/design/wiki.md`（Wiki 编译，V1 实现初版）、`docs/design/concept-evolution.md`（概念演化，V1 实现新增与合并）。

---

## 与 MVP 的边界

| 能力 | MVP | V1 |
|------|-----|----|
| ActivationLink | 仅 `link_candidates` 报告候选，不参与检索 | 正式数据结构 + 连续置信度（每条观测条件自己的成功/失败计数与服务分档，见 `activation.md`「状态机」），self_graded/trusted 档命中参与召回 |
| 学习信号 | Rerank 质量分级（confident / partial / gap）+ 共现统计 | 增加激活类事件：activation_success / activation_failure / activation_gap 及 repeated_* 累积 |
| Study | 定时扫描，只产报告 | 执行学习动作：形成候选、晋升、降权、淘汰，输出 Learning Result + Learning Reason |
| 检索链路 | 每次问答完整链路（≥4 次 LLM 调用） | ActivationLink 优先激活层；熟悉问题降至 3~4 次 LLM 调用（2026-08-11 修正：此前"1-2 次"是早期草案数字，与 `retrieval.md` 实际落地的 mining+verify+answer 三段式不符，见该文档「LLM 调用预算对照」） |
| 证据粒度 | 知识单元整段正文进入回答与引用 | 证据挖掘：Rerank 后逐字摘选片段级证据，程序原文校验 |
| KPN | 单 Source 内关系 | 跨 Source KP 对齐与关系合并 |
| 生命周期 | 无 | 完整 3 状态：current / superseded / deprecated |
| Wiki | 仅报告 Wiki 候选 | 编译初版：主题页 / 概念页，人工确认发布 |
| 用户反馈 | 无 | user_correction 通道（补充加速信号，非学习前提） |

---

## 核心能力

### 1. ActivationLink 落地

依据 `precompile.md` 与 `study.md` 第 8 节。

**数据模型**：ActivationLink 连接「问题条件」与一组 KnowledgePoint，可经 KP 反查 KU、回到来源证据。激活条件采用 Session 四元组（主题/意图作匹配计分，对象/约束作硬性守门；question_terms 保留作展示与回退，预留 scene / goal 字段），另含目标 point_id、状态、统计信号（采用次数、失败次数、最近使用时间）、创建来源（哪些 Learning Event）。匹配以 standalone 补全后的 expanded_question 及四元组为基准，与 traces 记录同源（见 activation.md 步骤 2）。

**置信度与服务分档（2026-08-13 起取代下方"状态机"这个提法，见 `activation.md`「状态机」）**：信任的单位不是链接整体，是链接下每一条具体的观测条件——每条条件用自己的 `success_count`/`failure_count` 算一个连续的 Beta 后验均值（`mean(cond) = (success+1)/(success+failure+2)`），按这个分数落入三档服务分档：

```text
exploring     mean 未过服务门槛，本轮只有小概率被当作一次真实试探
self_graded   mean 已过服务门槛，直接服务，偶尔被抽样做独立核实
trusted       mean 已过服务门槛且经受住足够的独立核实，直接服务，
              抽样核实频率更低（定期复查防漂移）
```

`activation_links.status` 列仍然存在，但含义改为从这三档派生、落库的缓存摘要（`verified ⟺ 存在 ≥1 条 tier∈{self_graded,trusted} 的条件`，`candidate ⟺` 不满足 verified 且 KP 仍 current，`deprecated ⟺` KP lifecycle 非 current），不再是驱动行为的真相源，也不再有 `weakened` 这个中间态——持续失败会让 mean 自然下滑回落 `candidate`，不需要一个单独的"降权"标签。

**没有单独的"状态迁移规则"这一说**：不存在攒够阈值触发的离散跳变，每条观测条件的 mean 随每次真实使用（自证）或定期抽样核实（独立核实，见 `retrieval.md` 步骤 2c）持续、连续地更新，见 `docs/design/activation-convergence.md`。

> 以下这段保留原文供对照历史演进，不代表当前实现——这套离散四态状态机与迁移表已于 2026-08-13 被上面的连续置信度机制取代：
>
> ```text
> candidate  候选链接，只辅助探索，不参与正式召回
> verified   已验证链接，参与正式召回，持续接受校验
> weakened   被降权链接，不作为首选激活路径
> deprecated 已淘汰链接，不再使用
> ```
>
> `conflicted` 状态依赖深想路径的 conflict 槽位处理，推迟到 V3（认知系统版，见 docs/impl/v3/readme.md）；V1 预留状态枚举值。
>
> ```text
> 共现积累 / activation_gap 满足阈值   -> 创建 candidate
> candidate 在相似条件下 repeated_success -> 晋升 verified
> verified 在相似条件下 repeated_failure  -> 降权 weakened
> weakened 长期无有效使用                -> 淘汰 deprecated
> ```
>
> 单次事件不改变状态；只有跨事件累积信号才推动迁移。

### 2. 检索事件体系

依据 `trace.md` 与 `study.md` 第 2 节。V1 在既有 Trace 分级之上，增加激活类 Learning Event 的自动产生：

```text
activation_success  ActivationLink 命中，且其 KP 进入 direct evidence 并被回答引用
activation_failure  ActivationLink 命中，但 KP 未被采用或未支撑有效回答
activation_gap      无合适 ActivationLink，但补充查找（outline / FTS 链路）找到被采用的知识
```

这些事件在问答中自动产生，不需要用户表态。写入沿用 MVP 的 `learning_events` 表和 `trace_write` 异步队列，扩展事件类型枚举。

`repeated_success / repeated_failure` 不是独立记录的事件，而是 Study 扫描时对同类条件下事件的累积判定。

### 3. Study 从报告升级为执行

依据 `study.md` 第 3、8、9、10 节。MVP 的定位是「验证而非执行」，V1 转为「执行 + 可审计」：

**学习动作**：

```text
形成候选：共现阈值或 activation_gap 累积 -> 创建 candidate ActivationLink
强化晋升：repeated_success               -> candidate 晋升 verified
降权：    repeated_failure               -> verified 降为 weakened
淘汰：    长期无效                        -> weakened 转 deprecated
缺口：    knowledge_gap 聚合              -> 知识缺口清单（沿用 MVP）
Wiki：    稳定 KP 簇                      -> Wiki 编译候选（见能力 8）
```

**Learning Result 与 Learning Reason**：每个学习动作落库为 Learning Result，附 Learning Reason，说明触发来源（哪些 Learning Event）、影响对象、动作类型、依据和适用边界，支持事后追踪与回滚（`study.md` 第 9 节）。

**人工监督（2026-08-12 修订，取代 2026-08-11「晋升默认自动，Wiki 材料准入单独加严」的口径）**：candidate 的创建与降权、淘汰全自动执行；**candidate → verified 的晋升默认也自动执行**（`study.auto_promote` 默认 `true`，可配置为人工确认），理由是 verified 唯一直接解锁的高风险动作——Retrieval 快路径——在生成答案前必经 `fast_path_verify`，误晋升的链接答不出来会回落慢路径，不会把错误答案直接送给用户；原先"默认人工确认"要防的风险已经被这道查询时的门槛结构性兜住。2026-08-11 曾额外认为 verified 不再隐含"人工看过、值得信赖到可以进入 Wiki 材料池"，因此给 Wiki 一阶编译材料的 qualifying 加了一道新的独立人工确认关卡（Wiki 材料确认）；2026-08-12 改判，该关卡整体废弃（见 `docs/design/wiki.md`「2026-08-12 改判」）——脱离具体 Wiki 主题语境，人工看着一条孤立的 KP 判断"值不值得沉淀"并不比程序多掌握信息，真正能做这个判断的时机是 Wiki 编译时（主题范围已定，编译时的整体判断——广度/连贯/稳定——自然回答了这个问题）。qualifying 因此恢复为只看 verified ActivationLink，V1 的"人工把关"仍然只落在一处："要不要相信这条激活路径"被查询时的 `fast_path_verify` 校验兜住，"要不要正式发布进 Wiki"由 `POST /wiki/compile` 的人工触发与编译时的整体判断把关，不需要在两者之间再加一道候选阶段的确认。

**运行方式**：沿用 MVP 的 `time.Ticker` 定时扫描，不走异步队列；报告继续生成，内容扩展为「本周期执行了哪些学习动作及原因」。

### 4. 检索链路升级：ActivationLink 优先激活层

依据 `retrieval.md` 与 `design.md` 第 3 节「分层检索与激活」。V1 检索流程变为：

```text
问题（经 Session 补全）
  -> ActivationLink 激活层：verified 链接按激活条件匹配，直接召回 KP -> KU
       ├─ 命中且充分：跳过 Domain 预过滤 / Source 过滤 / Outline 召回，
       │              直接进入 Rerank（或轻量校验）+ Answer   ← 快路径
       └─ 未命中或不足：回落到 MVP 完整链路（补充查找）        ← 慢路径
  -> 两路结果统一比较相关性与证据质量，不按来源层级简单排序
  -> KPN 扩展、充分性判断、EvidenceSet 构建（沿用 MVP）
```

关键约束：

- candidate 链接不参与快路径召回，最多作为 Rerank 候选的补充探索线索，且不得单独决定答案；
- 快路径命中后仍产生 activation_success / activation_failure 事件，verified 不免审；
- weakened / deprecated 不参与召回；
- 快路径目标：熟悉问题的 LLM 调用从 ≥4 次降至 3~4 次（挖掘 + 快路径校验 + Answer，命中需要模型辅助匹配时再加 1 次，见 `retrieval.md`「LLM 调用预算对照」、`activation.md` 步骤 2）；本条目原写"1-2 次（Rerank 精简或跳过）"，是 MVP readme 阶段的早期设想，实际落地路径没有走"精简版 Rerank"，2026-08-11 一并订正。

### 5. 证据挖掘：片段级证据

依据 `evidence-mining.md`。知识单元是主题单位，证据是回答单位；MVP 中 Rerank 保留的知识单元整段进入回答，噪声、引用粒度和学习信号都停留在 KU 级。V1 在 Rerank 之后、EvidenceSet 构建之前增加证据挖掘环节：

```text
Rerank 保留的每个 KU
  -> LLM 逐字摘选真正支撑回答的原文片段（句、步骤、命令、表行）
  -> 程序做原文子串校验：匹配不上的片段即幻构，重试或丢弃
  -> 校验通过的片段反算出 KU 内位置，fact_id 细化到片段级
  -> 整个 KU 挖掘失败时回退整段证据，标记"未挖掘"
```

关键收益与 V1 主线直接相关：**学习信号精度**。activation_success / failure 的判定依据从"这个 KU 被引用"细化为"KU 中哪个片段被采用"，ActivationLink 的强化与降权建立在更准的事实上；"KU 命中但挖不出片段"是一类新信号（主题相关、内容缺失），进入知识缺口清单。

无知识加工模式时按问题摘选；按槽位定向摘选依赖知识加工模式的在线执行，属 V3。

### 6. 跨 Source KPN

MVP 的 KPN 关系在单 Source 内生成。V1 扩展：

- 新 Source 完成 Unit 提取后，将其 KP 与既有同 Concept / 同 Domain 下的 KP 做批量匹配（LLM 批处理，复用 concept match 的批量模式）；
- 语义等价的 KP 建立跨 Source 关系（related / contradicts，与 MVP 内部关系类型一致，见 unit.md 设计决策），不合并删除原 KP——KP 保留各自来源以维持可追溯；
- contradicts 关系在 V1 只标记、进入学习报告提示，不做冲突消解（冲突检测是 V3 能力）。

### 7. 生命周期完整版

依据 `lifecycle.md`。3 状态即完整设计，不是子集，作用于 KnowledgeUnit / KnowledgePoint：

```text
current      默认状态，参与激活与回答
superseded   已被新版本替代，不参与当前回答
deprecated   来源已删除，不参与当前回答
```

触发与传导（`lifecycle.md` 第 2、4 节）：

```text
Source 重新上传成功（新内容 Unit 提取完成）-> 旧 KU/KP 一次性标记 superseded；
  上传处理链失败时旧 KU/KP 不受影响，仍为 current，复用既有
  sources.status=failed + POST /sources/:id/retry 提示重试，不引入新状态；
Source 删除 -> 该 source 全部 KU/KP（含已 superseded 的）标记 deprecated；
状态变化    -> 依赖的 ActivationLink 暂停强化，Bleve 索引同步过滤。
```

candidate / needs_verification / conflicted / historical / retracted 均已从场景倒推评估过，没有找到独立于 current/superseded/deprecated 的必要场景，不引入（详见 `lifecycle.md` 第 2 节）。

### 8. Wiki 编译初版

依据 `wiki.md`。V1 只做两种页面类型的最小闭环：

- **概念页**：Study 识别的 Wiki 候选（同 Concept 下多个高置信 KP + KPN 连接，沿用 MVP 候选逻辑）经人工在 Page 上确认后，触发 LLM 编译生成页面；主题页候选机制见下方「两层架构（扩展）」——不是同一套逻辑；
- 页面要素（防固化最小集）：稳定结论、证据来源（回链 KP / KU / source_ref）、待验证点、最近更新时间、依赖的核心 KU 列表；
- **重编译**：底层 KU/KP 状态变化或新的 wiki_update_candidate 信号时，Study 标记页面「待重编译」，人工确认后执行；每次编译记录触发来源，可追溯到 Learning Event；
- **检索接入**：已发布 Wiki 页面建立独立 Bleve 索引，作为快路径的直接命中层——同主题问题可直接引用 Wiki 结论并附证据回链（`study.md` 2.5 节所述正向反馈）。
- **两层架构（扩展）**：概念页为一阶编译（KP → 页面），主题页为二阶编译（已发布概念页 → 页面）；页面之间由程序从 KPN 派生 `related` / `contradicts` 关系，`contains` 由二阶编译写入，三者构成知识架构。**主题页在检索里的角色是召回骨架而非直答单元**——命中后展开成员概念页进候选，并把成员知识点注入慢路径 Rerank、跳过 Outline 召回（零额外 LLM 调用，这是复杂问题在 V1 唯一的实际收益）。写作出口是页面派生的草稿（`wiki_drafts`，主题页默认组装成员正文 + 只读证据清单），页面正文仍只由编译产生，无回写通道；草稿回流导入要打 `origin='wiki_draft'` 以阻断自体循环。详见 `wiki.md` 步骤 7-10。

方法页 / 经验页 / 问题页 / 决策页已从设计中删除（这四种类型此前只是名义上的分类，从未被接入过具体的编译流程，详见 `docs/design/wiki.md`「Wiki 页面类型」一节），不是推迟到 V3。认知视角差异化页面推迟到 V3。复杂问题的拆解与子结论聚合同样属 V3（深想 / Working Model）——V1 只准备好主题页结构，并记录 `topic_decompose_signal` 供后续使用。

### 9. 用户反馈通道

依据 `study.md` 第 2 节：user_correction 是补充加速信号，不是学习前提。V1 实现最小通道：

- Page 问答界面提供「有用 / 纠正」反馈入口，纠正可附文字说明；
- 反馈写入 `learning_events`（类型 user_correction），关联 answer_id 与本次采用的 KP；
- Study 消费：纠正事件加速相关 ActivationLink 的重新验证或降权（提高该链接累积信号的权重），不单独直接改状态。

### 10. Page 升级

- **ActivationLink 管理视图**：按状态分列（candidate / verified / weakened / deprecated），展示激活条件、命中统计、Learning Reason；支持人工确认晋升（`auto_promote=false` 时的灰度回退路径）、驳回候选；
- **Wiki 视图**：候选确认、页面阅读、待重编译标记、修订记录；
- **学习动作审计**：Learning Result 列表，可从动作回溯到触发它的 Learning Event；
- 问答界面增加反馈入口（能力 9）与快路径标识（本次回答走了激活层还是完整链路，便于验证学习效果）。

---

## 实现顺序

模块间依赖决定顺序，沿用 MVP「实现完成、测试通过再进入下一步」的原则：

```text
1. Lifecycle 基础     KU/KP 状态字段 + 索引过滤（后续所有能力都要感知状态）
2. ActivationLink     数据模型 + 连续置信度/服务分档 + 存储（学习转化的核心对象）
3. 检索事件           activation_* 事件产生与写入（学习燃料）
4. Study 执行         学习动作 + Learning Result / Reason + 人工确认流
5. 检索激活层         self_graded/trusted 档命中参与召回，快慢路径分叉
6. 证据挖掘           Rerank 后片段级摘选 + 原文校验（引用与学习信号细化到片段级）
7. 跨 Source KPN      KP 对齐与关系合并
8. Wiki 编译初版      候选确认 -> 编译 -> 发布 -> 检索接入 -> 重编译标记
9. 反馈通道 + Page    user_correction、管理视图、审计视图
10. 概念演化          gap_level 判定 + 候选聚合 + 新增默认自动确认/合并人工确认迁移
                      （依赖 trace / study / wiki 均已完成）
```

**ActivationBundle（熟路，2026-08-11 新增，设计方向，不在上面的强制顺序内）**：ActivationLink 只记「单个知识点管不管用」，锚点是 `point_id`；熟路是它之上的组合激活层，记「一组知识点合在一起，对同一类问题管不管用」。设计依据 `docs/design/activation-bundle.md`，工程方案 `docs/impl/v1/activation-bundle.md`。尚未排期，`wiki.md`/`wiki-generation.md`/`retrieval.md`/`trace.md`/`study.md`/`activation.md`/`lifecycle.md`/`evidence.md`/`page.md` 各留了「熟路指针」标注未来可能的接入点，当前判据/行为均未变。

1-5 构成「学习转化」最小闭环，是 V1 的主体；6-9 在闭环跑通后叠加。证据挖掘（6）独立于闭环，可视情况提前——它不依赖 ActivationLink，只依赖 Rerank 之后的既有链路。

---

## 数据与存储变化（概要）

```text
新增表
  activation_links        ActivationLink 主体（条件、目标 KP、状态、统计）
  learning_results        Study 学习动作记录（动作、对象、reason、关联事件）
  wiki_pages              Wiki 页面（内容、类型、状态、依赖 KU 列表、修订记录）
  wiki_page_relations     页面关系（related / contradicts 程序派生、contains 编译写入）
  wiki_drafts             写作草稿（来源页面 + 版本，人工自由编辑，不参与检索）

扩展
  knowledge_units / knowledge_points  增加 lifecycle 状态字段
  learning_events                     事件类型增加 activation_*（user_correction 沿用 MVP 通道）
  knowledge_point_relations           增加 scope 字段（intra/cross）与唯一索引
  traces                              增加 path_type / activation_link_ids /
                                      skeleton_page_id（主题页提供的召回骨架）
  wiki_pages                          增加 member_roles（结构化子主题分工）、
                                      uncovered_points（覆盖度清单，只作字段）
  sources                             增加 origin / origin_page_id
                                      （Wiki 草稿回流标记，阻断自体循环）
  EvidenceSet / evidence_snapshot     证据细化为片段级：片段原文 + KU 内位置，
                                      整段回退时保留"未挖掘"标记

Bleve
  新增 wiki 索引；units / points 索引查询按 lifecycle 状态过滤
```

具体 schema 在各模块实现文档中定义，migration 按版本号追加。

## 模块实现文档

按实现顺序阅读：

| 顺序 | 文档 | 内容 |
| --- | --- | --- |
| 1 | [lifecycle.md](./lifecycle.md) | KU/KP 生命周期状态、reupload/删除触发、Bleve 同步过滤、向链接与 Wiki 的传导 |
| 2 | [activation.md](./activation.md) | activation_links 数据模型、连续置信度与服务分档（取代原状态机与迁移约束）、激活条件匹配器、人工确认 API |
| 3 | [trace.md](./trace.md) | activation_success / failure / gap 事件产生规则、traces 表扩展、反馈通道扩展 |
| 4 | [study.md](./study.md) | 学习动作执行（创建/晋升/降权/淘汰）、learning_results、晋升确认流、报告扩展 |
| 5 | [retrieval.md](./retrieval.md) | Wiki 直答层与激活层、快慢路径分叉与回落、生命周期过滤改动点、LLM 调用预算 |
| 6 | [evidence.md](./evidence.md) | 证据挖掘：分批摘选 Prompt、原文校验与行号定位、失败回退、片段级 fact_id |
| 7 | [kpn.md](./kpn.md) | 跨 Source KP 匹配、scope 字段与去重、contradicts 报告接入 |
| 8 | [wiki.md](./wiki.md) | 页面编译 Prompt 与白名单校验、发布与 Wiki 直答、重编译生命周期 |
| 9 | [page.md](./page.md) | 反馈入口与路径徽标、链接管理 / Wiki / 审计视图、lifecycle 操作入口 |
| 10 | [concept-evolution.md](./concept-evolution.md) | 概念新增 / 合并：gap_level 判定、候选聚合、迁移事务（新增默认自动确认，合并仍人工确认，2026-08-14 改判）、preset 优先级 |

---

## V1 不做什么（推迟到 V2 / V3）

后续版本划分：V2 为认知材料构建版（把 V1 成果编译为结构化认知材料，见 docs/impl/v2/readme.md），V3 为认知系统版（在线推理与学习闭环完整化，见 docs/impl/v3/readme.md）。

```text
【推迟到 V2：材料与 Schema】
Claim 层（Wiki 编译双产物）与认知包组装
KPP 模式库定义与槽位 Schema、RP 输出契约（均只定义不执行）
认知视角对象与 ActivationLink 多链接化 / 认知化字段
  （perspective 差异化的 ActivationLink 与防固化要素补齐）

【推迟到 V3：在线执行】
认知路由三路径（找 / 浅想 / 深想）的完整实现与升级信号
  （V1 只有快/慢路径分叉，不是完整路由；快≈找+浅想，慢≈深想的前身）
Working Model 临时认知模型（三层结构、槽位状态机、处理上限）
KPP / RP 在线执行：模式识别、槽位驱动检索扩展
  （V1 证据挖掘按问题摘选；按槽位定向摘选依赖知识加工模式执行，属 V3）
实践路径（Practice Path）提炼与深想快路径调用
insufficient 槽位的外部补证（外部证据查找）
conflict 槽位处理与条件化结论（基于 contradicts 关系的正反证据整理，
  不引入 conflicted 生命周期状态——详见 `lifecycle.md` 第 2 节，
  3 状态已是完整设计，candidate / needs_verification /
  conflicted / historical / retracted 均无独立必要场景，不再规划引入）
视角在线推断与视角化学习累积、视角化 Wiki 编译
概念拆分与合并信号的在线产生点 ambiguous_match
  （V1 概念演化只做新增 / 合并，合并信号用离线共同采用统计，
  见 concept-evolution.md「与设计文档的 V1 适配」）
Domain 层面的新增与合并（门槛与影响面更大，机制同概念演化）
Agent 接入层（service / agent 架构对外开放）
```

---

## V1 成功标准

```text
学习转化闭环：同类问题反复问答后，系统自动形成 candidate ActivationLink，
              达到统计门槛后自动晋升 verified（2026-08-11 修订，默认不
              经人工确认），后续同类问题走快路径命中；这批 KP 同时即
              qualifying 为 Wiki 材料（2026-08-12 修订：不再单独经过
              Wiki 材料确认，是否够格立传由编译时的整体判断回答）；
检索行为改变：熟悉问题 LLM 调用降至 3~4 次（2026-08-11 订正，见能力 4），
              回答延迟明显下降，且 direct evidence 命中率不低于完整链路；
学习可审计：  每个 ActivationLink 状态迁移都能回溯到 Learning Result、
              Learning Reason 和支撑它的 Learning Event；
引用精确：    回答引用可定位到知识单元内的原文片段，幻构片段被
              原文校验拦截；挖掘失败回退整段且退化可观察；
自我修正：    人为制造失败场景（如删除相关 Source）后，
              repeated_failure 累积能推动链接降权，快路径自动回落慢路径；
生命周期生效：Source 更新/删除后，旧 KU/KP 不再进入回答，
              依赖链接停止强化；
Wiki 初版：   至少一个稳定主题完成「候选 -> 确认 -> 编译 -> 检索命中 -> 
              底层变化 -> 待重编译标记」的完整生命周期；
反馈生效：    user_correction 能在报告和链接信号中观察到加速作用。
```

如果上述标准成立，说明「使用信号 → 长期记忆 → 检索行为」的转化链路工程可行，可进入 V2（认知材料构建），再由 V3 实现完整认知系统。
