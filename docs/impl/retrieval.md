# 知识检索认知服务实现方案

本文档描述知识大脑第一版中知识检索相关 cognitive service 的实现方案。

`retrieval` 不是完整思维模式，也不是一个 agent。

它是一组认知服务，负责在当前认知视角下激活已有领域和概念，同时从知识单元、知识点、原文目录和来源证据中进行补充查找。

系统冷启动或尚未学到 ActivationLink 时，材料侧多路召回是主路径。precompile 保存的 Domain / Concept 静态匹配候选不得作为 active link 直接召回；只有补充检索独立命中、结果被实际采用并写入 trace 后，study 才能据此创建 candidate ActivationLink。

这些服务不仅用于找答案，也用于发现当前认知结构的缺口。

第一版至少包括：

```text
ProblemFramingService
CognitiveActivationService
SupplementalRetrievalService
RetrievalFusionService
EvidenceRerankService
EvidenceAnswerService
EvidenceAggregationService
EvidenceCoverageService
GapDetectionService
EvidenceTracingService
ConflictDetectionService
ModeSwitchEvaluationService
ResponseAssemblyService
```

Agent 根据思维模式编排这些 service。

模型只参与其中需要语义判断的局部步骤，例如 Domain / Concept 激活，以及目录结构检索的两阶段语义路由。

## 默认证据短路径

在线检索默认走证据直答短路径，而不是先把每条证据转换成实体关系或 Working Model：

```text
ProblemFraming
  -> 目录结构召回 + FTS 等多路召回
  -> RetrievalFusion
  -> EvidenceRerank(light)
  -> DirectAnswerSufficiency
       -> sufficient: EvidenceAnswer -> EvidenceValidation -> ResponseAssembly(light)
       -> insufficient: 升级结构化深路径
```

`DirectAnswerSufficiency` 判断“现有证据是否直接包含问题所需答案”，而不是根据问题措辞判断是否像推理题。定义、实体或角色枚举、属性、出处、简单流程、明确规则、简单数值和证据内已有结论，均可走短路径。

`EvidenceAnswerService` 接收原问题和经过筛选的完整事实陈述。模型负责理解问题并组织答案，最小输出为答案条目及其使用的 `fact_ids`；模型不得生成证据 ID。代码必须校验 `fact_id` 白名单、事实的证据绑定状态和每个答案条目的引用，再通过稳定 ID 回填 `EvidenceRef`。

短路径不强制调用 `EvidenceAggregationService`、完整 `EvidenceCoverageService`、`WorkingModelService` 或 `ReasoningService`。只有直接证据不足、存在影响结论的冲突、需要多跳或跨材料关系组合、存在隐含条件推导、复杂比较、高风险判断，或用户明确要求展示推理过程时，才升级深路径。升级可以只覆盖局部缺口。

## 和思维模式的关系

`retrieval` 中定义的是检索相关认知服务，不是思维模式本身。

第一版先支持两个思维模式：

```text
检索模式；
推理模式。
```

它们都会使用检索相关 service，但使用深度不同。

检索模式关注：

```text
找到相关知识；
回到来源证据；
给出简短回答或材料列表；
暴露轻量缺口和冲突信号。
```

推理模式关注：

```text
在检索结果基础上组织临时认知模型；
检查证据、冲突、缺口和约束；
形成推理路径；
输出带证据来源和推断来源的判断或方案。
```

因此，检索服务的输出不能只面向最终回答。

它必须同时服务于：

```text
RetrievalAgent 的轻量回答；
ReasoningAgent 的 working model 和推理过程；
trace / study 的后续学习。
```

## 第一版定位

第一版重点实现内部知识检索相关 service。

搜索引擎和网页获取不作为第一版 source 导入主链路。

但 `VerificationService` 可以通过 DDG 免费搜索接口生成低可信外部证据候选，用于标记“需要查证”“外部候选依据”或“证据不足”。

这些候选不等同于内部 `EvidenceRef`，也不会自动进入知识单元。

第一版重点定义：

```text
CognitiveRouteContext：把输入问题拆成场景、目标、模式、注意焦点、知识相关性和历史反馈；
CognitiveActivationService：当前认知视角下的领域和概念激活；
SupplementalRetrievalService：知识单元、知识点、目录路径和中心句补充查找；
RetrievalFusionService：检索结果合并和排序；
EvidenceRerankService：按当前问题的直接回答价值重排候选，并标注证据角色；
EvidenceAggregationService：按问题类型抽取、归一、去重和归并证据事实；
EvidenceCoverageService：检查关键实体、证据槽位和答案要求是否被直接证据覆盖；
GapDetectionService：认知缺口和知识缺口识别；
EvidenceTracingService：来源位置回查；
VerificationService：低可信外部查证候选；
Trace 契约和 working model 输出契约。
```

## LLM Rerank 最小输出契约

`EvidenceRerankService` 必须遵循“模型只做语义决策，代码组装完整对象”的原则。

Rerank 的基本粒度是 `AnswerFact`，不是 `KnowledgeUnit`。进入模型前，代码应把多路召回候选展开为带稳定 ID 的事实候选，并携带只读的实体候选：

```text
RerankFactCandidate {
  fact_id
  text
  source_rank
  entity_candidates[] { entity_id, text }
}
```

模型不得回显 Unit、KnowledgePoint、原文、EvidenceRef 或完整事实对象。最小输出只包含模型无法由代码确定的决策：

```text
RerankDecision {
  fact_id
  role            // direct / supporting / background / irrelevant
  score
  selected_entity_ids[]
  entity_type     // optional: participant / decision_mechanism / condition
}
```

`statement`、`action`、`unit_id`、`knowledge_point_id`、`evidence_ref_id` 和来源位置都由代码通过 `fact_id` 回填。`supported_facts`、`supported_entities`、解释性 `reason` 和模型生成的 `warnings` 不进入正式输出 schema；需要调试信息时由 trace 根据输入和决策派生。

代码必须执行 JSON/schema、fact ID 白名单、entity ID 所属关系、枚举和重复决策校验，再确定性回填事实、实体、知识和证据。单条决策无效时只丢弃该条并记录 warning；有部分有效决策时返回 `partial`；全部无效时保守降级，不得把未经 rerank 的候选标记为直接证据。

候选较多时应分批 rerank。每批事实数由配置和上下文预算决定，默认建议 10 到 15 条；代码先按融合分数预筛，再合并各批决策。只有边界分数、重复或冲突候选需要进入一次小规模复排。

`EvidenceAggregationService` 使用通过校验的 ID 决策，从本地事实表回填内容并构建 `StructuredFact`。模型不生成 `StructuredFact`，也不负责 EvidenceRef 绑定。

## 问题类型驱动的检索策略

检索不能对所有问题统一执行“固定候选 Top-K -> 单一相关分重排 -> 全局 Top-N used”。`ProblemFramingService` 应识别问题类型和 `FactShape`，并生成贯穿候选召回、Evidence Rerank、Coverage 和 used evidence 选择的策略。

```text
RetrievalPolicy {
  question_type
  fact_shape
  candidate_top_k
  rerank_top_k
  route_weights
  preferred_fact_shapes
  preferred_evidence_roles
  diversity_key
  required_slots
  optional_slots
  follow_up_trigger
  used_selection
}
```

`candidate_top_k` 控制进入 rerank 前的候选池，不能用 rerank 阶段放大已经被截断的候选。`rerank_top_k` 控制语义决策成本。`used_selection` 不是所有问题都取全局最高分，而应按问题所需的实体、阶段、对象或证据链选择。

### 策略矩阵

| 问题类型 | 候选召回 | Rerank 目标 | Coverage | Used evidence 选择 |
| --- | --- | --- | --- | --- |
| 定义 | Point 和正文优先，较小 Top-K | 精确定义句、主语绑定、直接回答价值 | subject + definition | 1-2 条最佳直接证据 |
| 枚举 / 角色 | 较大 Top-K，同类实体和同级目录扩展 | 实体相关性、去重和多样性 | entity_collection | 每个实体一条最佳证据 |
| 属性集合 | 列表、表格和完整 Unit 优先 | 同一对象的属性完整性 | attribute_group | 保留完整属性组，不拆散字段 |
| 流程 / 步骤 | 扩展相邻目录和连续 Unit | 顺序、阶段和前后依赖 | prerequisite + steps + decision + outcome | 每个必要阶段至少一条证据 |
| 条件判断 / 决策 | 规则、条件、约束和红线优先 | 条件 -> 动作 -> 结果，主规则优先 | main_rule + conditions + outcome；exception 可选 | 主规则、必要条件、红线优先，例外其次 |
| 例外处理 | 异常词、否定条件和例外路径扩展 | 触发条件、批准主体、例外动作和结果 | trigger + exception_action + outcome | 保留完整例外链，并关联主规则 |
| 数值 / 阈值 | 参数、表格和数值 Point 优先 | 数值、单位、对象、适用条件绑定 | value + unit + subject + applicability | 精确数值证据和少量条件说明 |
| 比较 | 分别召回各比较对象 | 两侧证据对称、比较维度一致 | subject_a + subject_b + common_dimensions | 按对象和维度平衡选择 |
| 因果 / 根因 | 原因、机制和结果多路召回 | 因果方向和机制，降低仅词面相关事实 | cause + mechanism + effect | 连通的因果证据链 |
| 影响分析 | 直接和间接结果扩展 | 影响对象、方向、范围 | major_impact_dimensions | 按影响维度选择 |
| 关系查询 | 分别召回两实体和关系事实 | 显式关系优先，其次可验证中间链路 | entity_a + entity_b + relation_or_path | 最小连通证据集 |
| 证据定位 | SourceSpan、正文和目录优先 | 可引用性和位置准确性 | claim + source + quote | 每项结论一条最佳可引用证据 |
| 汇总 / 综述 | 多目录、多来源、较大 Top-K | 主题覆盖、多样性和去重 | major_topics | 每主题保留代表证据 |
| 多跳推理 | 分阶段召回并扩展中间实体 | 链路连续性和逐步证据 | 每个推理步骤 | 连通证据子图 |
| 冲突核查 | 多来源、多版本召回 | 对象、条件和时间对齐 | 至少两侧冲突证据 | 冲突双方均保留 |
| 时间 / 版本 | 日期、版本、状态信息优先 | 当前有效版本优先 | effective_time + version + status | 最新有效证据，旧版用于冲突说明 |
| 案例 / 示例 | case/example Unit 优先 | 与规则的贴合程度 | rule + example | 规则与案例配对 |
| 开放探索 | 多样化召回，较大 Top-K | 多视角价值和可信度 | 不要求穷尽 | 多样性选择并声明非穷尽 |

### 关键策略约束

定义型问题中，标题和目录相似只能辅助召回，不能压过正文中的精确定义句。枚举和角色问题必须以实体为多样性键，禁止全局 Top-N 只留下一个实体。流程问题必须按阶段覆盖选择，不能只保留分数最高的一步。

条件判断问题的证据优先级为：

```text
主判断规则
-> 必要条件和约束
-> 红线 / 阈值
-> 例外流程
-> 背景和职责
```

比较问题任一侧缺失时只能输出部分比较。数值缺少单位、对象或适用条件时不得直接回答。因果问题中共同出现和词面相关不能替代因果方向或机制证据。关系问题没有显式关系或完整中间链路时不得推断关系成立。

### Follow-up Retrieval

Coverage 在初次 rerank 后仍不完整时，可以执行一次受限的针对性补召回。补召回只针对缺失槽位，不重复原查询的普通 Top-K。

| 问题类型 | 触发条件 | 补召回方式 |
| --- | --- | --- |
| 枚举 / 角色 | 实体集合覆盖不足 | 同级目录、同类角色、相同流程参与者扩展 |
| 流程 | 缺少必要阶段 | 相邻目录和前后步骤扩展 |
| 比较 | 缺少一方或共同维度 | 针对缺失对象或维度单独召回 |
| 条件判断 | 缺主规则、条件或红线 | 规则、约束、阈值专项召回 |
| 因果 | cause / mechanism / effect 链路断裂 | 针对缺失环节召回 |
| 关系 | 两实体存在但关系缺失 | 关系词和中间实体召回 |
| 证据定位 | 有结论但没有可引用原文 | 按 Unit / Point 反查 SourceSpan |

补召回次数必须有上限。补召回结果仍须经过 rerank、EvidenceRef 回链和 Coverage，不得因“补充命中”自动进入 used evidence。

### 策略落地顺序

第一优先级是枚举 / 角色和条件判断策略；第二优先级是流程、定义和数值策略；第三优先级是比较、因果、关系、汇总、多跳和冲突策略。每类策略必须分别测试候选池、rerank、coverage 和 used selection，不能只测试最终分数。

### 四类基准问题的端到端策略

以下示例是策略验收基线，不要求通过关键词硬编码答案。每类问题都必须经过共享的目录结构召回和 FTS 召回，再执行该类型的语义分类、确定性选择和 Coverage。

#### 定义：成本红线

基础召回可以包含成本红线定义、审批确认、投入约束、超线预警和会议决策。语义分类应区分：

```text
direct_definition
applicability / constraint
approval_responsibility
consequence
decision_mechanism
```

确定性选择只保留最佳 `direct_definition` 和必要的适用范围或约束。标题、审批职责和预警后果不能替代正文定义。Coverage 至少要求 `subject + definition + direct_evidence`。如果“定义”只是模型概括而无法回链原文，只能作为归纳说明，不能伪装为原文明确定义。

#### 枚举：无合同立项审批角色

候选池应覆盖同一审批流程中的角色职责 Unit，而不只保留全局最高分角色。语义分类至少区分：

```text
approval_participant
decision_maker
risk_reviewer
applicant / reporting_participant
exception_decision_mechanism
downstream_executor
document_preparer
background
```

实体角色允许多标签；`decision_maker` 不能因此被排除出 participant 集合。正常审批角色、条件性参与者、例外决策机制和后续执行方必须分组表达。确定性选择按规范化实体去重，每个审批参与者保留一条最佳直接证据，并在要求依据时为每个实体绑定独立 EvidenceRef。

Coverage 不能把一个角色命中视为集合完整。若仍存在高相关但未验证的角色候选，应保持 `partial` 并触发一次同级目录或相同流程角色补召回。角色名称发生冲突时以 SourceSpan 原文为准。

#### 条件判断：无合同项目是否继续投入

语义分类应把证据组织为：

```text
main_rule
approval_condition
scope_constraint
cost_threshold
risk_constraint
contract_deadline
stop_or_continue_outcome
exception_condition
exceptional_escalation
monitoring_signal
```

确定性选择必须先保留主规则、审批条件、投入范围、成本红线、风险约束和结果，再保留延期或会议等例外路径。高分职责或例外会议不能替代投入控制主规则。

推理输入应支持如下条件链，而不是直接跳到例外：

```text
是否获批
-> 是否在审批范围和成本红线内
-> 是否风险可控并符合最小投入原则
-> 是否能在签约红线前签约
-> 不能时延期是否获批
-> 未获批原则上终止
-> 仍坚持投入时才进入例外会议决策
```

Coverage 缺少 `main_rule` 时，即使存在例外事实也必须是 `partial`，不得把回答标为完整成功。

#### 关系缺口：已知实体与不存在实体

关系问题必须分别验证 `entity_a`、`entity_b` 和 `relation_or_path`。只召回其中一个实体或仅有共同词汇时，不能推断关系成立。

```text
entity_a: covered
entity_b: missing
relation_or_path: missing
=> insufficient_evidence
```

系统只允许针对缺失实体和关系执行一次受限补召回。仍无结果时，应明确说明已找到哪一侧、缺少哪一侧及为何不能确认关系，并记录 `missing_entity` 和 `missing_relation`，不得用单侧证据生成关系结论。

### 四类基准的 trace 要求

Trace 和 eval 至少记录：

```text
primary_policy_type
effective_candidate_top_k
candidate_count_before_cut
candidate_count_after_cut
semantic_role per fact
entity / slot bindings
selected / rejected reason
coverage before follow-up
follow-up query and result
coverage after follow-up
facts_used_in_answer
```

验收必须检查候选生成、语义分类、确定性选择、Coverage、补召回和最终答案六个阶段；最终答案偶然正确不能证明策略正确。

## 思维模式下的检索深度

不同 agent 对 retrieval service 的调用深度不同。

### 检索模式

检索模式由 `RetrievalAgent` 承载。

目标：

```text
围绕当前问题找到最相关的知识单元、知识点和来源证据；
解释这些结果是通过哪个认知结构或召回路径找到的；
在证据足够时形成简短回答；
在证据不足或认知结构激活失败时记录轻量缺口。
```

建议编排：

```text
ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> EvidenceRerankService(light)
  -> DirectAnswerSufficiency
  -> EvidenceAnswerService
  -> EvidenceValidation
  -> GapDetectionService(light)
  -> EvidenceTracingService
  -> ConflictDetectionService(light)
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService(light)
```

检索模式下的 `GapDetectionService` 只做轻量缺口识别。

它应记录：

```text
是否没有匹配到 Domain / Concept；
是否匹配到 Concept 但没有 ActivationLink；
是否只有多路召回命中；
是否证据不可回链。
```

它不需要完整分析缺口形成原因，也不直接触发 study。

检索模式输出重点：

```text
ranked_results
used_knowledge
evidence_refs
evidence_summary
cognitive_gaps(light)
knowledge_gaps(light)
conflicts(light)
mode_switch_decision
warnings
```

如果检索模式发现以下情况，应返回 `mode_switch_request`：

```text
结果之间存在影响核心结论的冲突；
证据不足以直接回答；
答案需要多跳关系、跨材料组合或隐含条件推导；
用户要求解释推理过程；
当前认知结构缺口较强。
```

问题被分类为判断、比较、归因或设计，不构成单独的切换条件；如果召回证据已经明确给出所需规则或结论，仍应走证据直答短路径。

如果冲突只影响局部结论，检索模式不应默认整体切换。

它应生成 `local_mode_escalation`，只把冲突点放入推理模式或后续冲突处理流程。

只有当冲突影响核心问题结论时，才整体切换到推理模式。

### 推理模式

推理模式由 `ReasoningAgent` 承载。

目标：

```text
把认知结构激活结果、多路召回结果、证据、缺口和问题约束组织成临时认知模型；
基于临时认知模型形成推理路径；
输出可解释、可追溯、可复盘的回答。
```

建议编排：

```text
ProblemFramingService
  -> CognitiveActivationService
  -> SupplementalRetrievalService
  -> RetrievalFusionService
  -> EvidenceRerankService
  -> EvidenceAggregationService
  -> EvidenceCoverageService
  -> GapDetectionService
  -> EvidenceTracingService
  -> ConflictDetectionService
  -> WorkingModelService
  -> ReasoningService
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService
```

推理模式下，检索结果不是直接回答材料。

它们应先进入 `WorkingModelService`，被组织为：

```text
相关领域和概念；
已激活知识；
补充证据；
冲突点；
核心问题变量；
约束条件；
冲突和不确定性；
认知缺口；
知识缺口；
候选推理路径。
```

推理模式输出重点：

```text
working_model
reasoning_path
used_knowledge
evidence_refs
conflicts
mode_switch_decision
cognitive_gaps
knowledge_gaps
learning_suggestions
warnings
```

推理模式应保留 full trace。

它需要记录哪些知识真正参与推理，而不只是哪些知识被召回。

### 两种模式的共同约束

检索模式和推理模式都必须遵循：

```text
优先使用当前 Perspective 下的 Domain / Concept；
认知结构检索和多路召回并行互补；
融合排序保留 hit_route 和 score_breakdown；
EvidenceTracingService 负责证据回链；
GapDetectionService 只记录缺口，不修改长期认知结构；
ConflictDetectionService 识别冲突，但不直接替代推理；
ModeSwitchEvaluationService 区分整体切换和局部升级；
ResponseAssemblyService 负责把证据、推断、缺口和冲突组织进回答；
retrieved 和 used 必须区分；
模型调用应尽量少，并只接收必要上下文。
```

## 并行执行

知识检索过程中有多条路径可以并行执行。

第一版实现时，应把互不依赖的检索 service 并行化，但不能破坏结果融合、证据回链和 trace 顺序。

### 可并行步骤

问题理解完成后，可以并行执行：

```text
CognitiveActivationService；
SupplementalRetrievalService 中的 unit full-text retrieval；
SupplementalRetrievalService 中的 knowledge point retrieval；
SupplementalRetrievalService 中的 outline tree retrieval（两阶段，见下文）；
VerificationService(optional)，如果 require_external_evidence = true。
```

`SupplementalRetrievalService` 内部也可以并行：

```text
Bleve units 索引查询（经 unit_search_index 资格过滤）；
Bleve points 索引查询（经 knowledge_point_search_index 资格过滤）；
Bleve sources 索引查询（经 source_search_index 资格过滤，作为阶段一宽召回）；
Bleve outlines 索引查询（限定候选 SourceDocument，作为阶段二词法预筛或回退）。
```

目录结构检索的两阶段语义路由内部必须串行：阶段二依赖阶段一的候选文档集合。

见 `docs/impl/fts.md`。

这些路径的目标不同：

```text
认知结构检索负责激活已有认知结构；
全文和知识点检索负责材料侧召回；
文档级目录树检索负责先选文档、再定位结构范围；
外部查证负责生成低可信外部候选证据。
```

### 需要等待上游的步骤

以下步骤必须等待候选结果：

```text
outline tree match（阶段二）
  依赖 document route match（阶段一）得到候选文档集合；

RetrievalFusionService
  依赖认知结构检索和多路召回候选；

EvidenceRerankService
  依赖融合候选，并根据问题框架判断直接回答价值；

EvidenceAggregationService
  依赖 rerank 结果，只聚合 direct_answer 和必要的 supporting 证据；

EvidenceCoverageService
  依赖结构化聚合结果，检查关键实体、槽位和答案形态是否完整；

EvidenceTracingService
  依赖聚合后的事实、used_items 和其证据绑定；

GapDetectionService
  依赖认知结构检索、多路召回和融合信号；

ConflictDetectionService
  依赖证据引用、候选结论和 used_knowledge；

ModeSwitchEvaluationService
  依赖融合结果、缺口、冲突和证据信号；

ResponseAssemblyService
  依赖证据、缺口、冲突、模式评估和推理结果。
```

### 建议执行图

检索模式：

```text
ProblemFramingService
  -> CognitiveActivationService(active links only)
  -> validate activated KnowledgePoint / KnowledgeUnit
  -> if active path is insufficient:
       unit full-text retrieval
       knowledge point retrieval
       outline tree retrieval:
         phase 1: document route match
         phase 2: outline tree match
       VerificationService(optional)
  -> RetrievalFusionService
  -> parallel:
       GapDetectionService(light)
       EvidenceTracingService
  -> ConflictDetectionService(light)
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService(light)
```

推理模式：

```text
ProblemFramingService
  -> CognitiveActivationService(active links only)
  -> validate activated KnowledgePoint / KnowledgeUnit
  -> if active path is insufficient:
       unit full-text retrieval
       knowledge point retrieval
       outline tree retrieval:
         phase 1: document route match
         phase 2: outline tree match
       VerificationService(optional)
  -> RetrievalFusionService
  -> parallel:
       GapDetectionService
       EvidenceTracingService
  -> ConflictDetectionService
  -> WorkingModelService
  -> ReasoningService
  -> ModeSwitchEvaluationService
  -> ResponseAssemblyService
```

### 并行执行约束

```text
并行任务必须共享同一个 request_id 和 trace_id；
每个 service 独立记录 service_call；
单一路径失败不能取消其他路径；
失败路径产生 warning 和 SystemError；
融合阶段必须知道哪些路径成功、失败或降级；
模型调用路径必须遵守上下文预算；
不得为了并行而重复调用模型判断同一语义问题。
```

如果并行路径部分失败，`RetrievalFusionService` 应保留成功候选，并在 `rerank_warnings` 中记录缺失路线。

## 检索原则

第一版不区分冷启动和热启动两套流程。

系统始终使用当前认知视角下已有的领域和概念理解问题。

同时，系统并行执行补充查找。

如果领域和概念激活失败，但补充查找找到了有效知识并被采用，说明当前认知结构存在缺口。

该缺口应进入 trace 和 study。

## ProblemFramingService

`ProblemFramingService` 是 retrieval 在线链路的第一步。

它负责把用户输入的问题或待处理事项拆成可供后续服务使用的 `CognitiveRouteContext`。

它不检索知识，不修改长期认知结构，也不直接创建 Domain / Concept / ActivationLink。

查询上下文字段（`conversation_context` / `trace_context` / `feedback_context`）由 API 层 `QueryContextBuilder` 构造，见 `docs/impl/session.md`。

本节只描述 **ProblemFraming 如何消费这些输入并完成问题维度拆解**。

### 输入来源

问题拆解不能只看当前 `query`。

`RetrievalRequest` 应携带以下输入（由 `AgentRequest` 透传，`session.md` 负责在线构造）：

| 字段 | 构造方 | 在拆解中的作用 |
| --- | --- | --- |
| `query` | 客户端 | 最高优先级的问题文本 |
| `available_context` | 客户端显式传入 | 用户附加上下文、粘贴材料、上游注入 |
| `conversation_context` | `session.Service` | 补全指代、省略、连续追问和用户修正 |
| `trace_context` | `TraceContextResolver` | 延续近邻认知过程：scene/goal/pattern、缺口、模式切换 |
| `feedback_context` | `FeedbackContextResolver` | 历史有效/无效路径、边界失配、cluster 线索 |
| `perspective_id` | session / 请求 | 认知视角 |
| `mode` | router 已选 mode | pattern 判断的 hint，不得被 feedback 强行覆盖 |
| `source_filters` 等 | 请求 | 显式约束 |

其中：

```text
conversation_context
  回答「这轮对话里用户已经说过什么」；

trace_context
  回答「系统最近怎样处理过」，用于同一问题链路的延续判断；

feedback_context
  回答「历史上哪些路径有效/无效」，只能作为加权信号。
```

`ProblemFramingService` 不应在在线请求中扫描全量 trace，也不应临时聚合 study 明细。

在线链路最多读取：

```text
query 与 available_context；
session 构造的 conversation_context；
近邻 trace 轻量摘要（trace_context）；
study 预聚合反馈摘要（feedback_context）。
```

字段优先级：

```text
query > conversation_context > trace_context > feedback_context
```

### 输出

建议输出：

```text
ProblemFramingResult {
  query
  normalized_query
  question_type
  answer_intent
  risk_level
  require_evidence
  require_external_evidence
  candidate_modes
  selected_mode
  cognitive_route_context
  confidence
  warnings
}

CognitiveRouteContext {
  scene
  goal
  pattern
  focus
  constraints
  knowledge_relevance_hints
  feedback_context
  question_cluster_hint
}
```

字段说明：

| 字段 | 含义 |
| --- | --- |
| `scene` | 当前问题发生的场景，例如投资、故障排查、系统设计、文档查找 |
| `goal` | 用户想达成的目标，例如解释、判断、选择、定位原因、生成方案、验证事实 |
| `pattern` | 当前采用的思维模式，例如快速检索、工作模型、查证、冲突检测、经验路径 |
| `focus` | 本次问题优先关注的维度，例如风险、成本、增长、性能、根因、证据、边界 |
| `constraints` | 时间、来源、范围、证据、输出形式、权限等约束 |
| `knowledge_relevance_hints` | 从问题和上下文中抽取的知识相关性线索，用于 Domain / Concept / ActivationLink 预筛 |
| `feedback_context` | 相似问题的历史采用、正反馈、负反馈、未使用和边界失配摘要 |
| `question_cluster_hint` | 可选的问题簇线索，用于 trace / study 后续去重和累计 |

`ProblemFramingService` 可以使用规则和模型结合：

```text
规则负责识别显式模式、证据要求、风险词、来源约束和简单问题类型；
模型负责在上下文省略、目标含混或多模式候选时做结构化判断；
模型输出必须受 schema 约束；
低置信结果必须保留 warnings，并允许下游降级为更保守模式。
```

### 拆解方法

第一版按以下顺序拆解问题。

各步使用的输入字段与 `docs/impl/session.md` 对齐。

```text
1. 规范化 query
   以 query 为主文本，去掉无关寒暄，保留显式目标、对象、约束和输出要求。
   不用 conversation_context 覆盖 query 中的显式意图。

2. 合并 conversation_context
   用对话摘要和 recent turns 补全指代、省略和连续追问。
   只补「在说什么」，不把对话历史当作证据或结论来源。

3. 合并 trace_context
   读取近邻 trace 的 scene / goal / pattern、缺口标题、模式切换和 answer_summary（若有）。
   用于延续同一问题链路中的认知判断，不注入 evidence 原文或完整 reasoning。

4. 提取显式约束
   从 query、available_context、conversation_context 中提取来源范围、时间范围、
   证据要求、输出形式、风险偏好和禁止事项。
   router 已给出的 require_evidence 等等级不低于 framing 自行推断结果。

5. 判断 scene
   综合 query、conversation_context、trace_context 判断场景，
   例如故障排查、系统设计、投资判断、文档查找。

6. 判断 goal
   根据用户动词和期望产物判断目标，例如解释、判断、选择、定位原因、生成方案、验证事实。

7. 判断 pattern
   优先使用 router / agent 已给出的 mode 作为 hint；
   若 query 或 trace_context 表明需要查证、冲突处理或工作模型，可建议更保守 pattern；
   feedback_context 不得强行覆盖当前 query 的显式模式要求。

8. 提取 focus
   从问题、对话约束和 trace 缺口中提取注意焦点，例如性能、成本、根因、边界、证据。

9. 生成 knowledge_relevance_hints
   从 query、available_context、conversation_context 抽取实体、术语、动作、指标和语义线索，
   用于 Domain / Concept / ActivationLink 预筛。

10. 合并 feedback_context
    使用 FeedbackContextResolver 注入的 study 聚合摘要（见 session.md），
    记录历史有效路径、无效路径和边界失配。
    不读取 study candidate 原文，不在线扫全量 trace。

11. 生成 question_cluster_hint
    优先使用 FeedbackContextResolver 显式输出的 cluster；
    仅当会话或反馈摘要明确显示同类问题簇时才填写，否则为 null。
```

模型提示词和 schema：

```text
prompts/retrieval/problem_framing.md
schemas/retrieval/problem_framing.schema.json
```

规则前置可以直接生成高置信 framing。

当存在以下情况时应调用模型：

```text
当前 query 依赖 conversation_context；
trace_context 显示与当前 query 相关的未解决缺口或模式切换；
目标或场景含混；
同一问题可能对应多个 pattern；
需要从 feedback_context 中选择更合适的 route preference；
需要把复杂用户输入拆成多个 focus 或 constraints。
```

### 与上下文和历史 trace 的关系

会话上下文用于理解当前问题“在说什么”。

近邻 trace 摘要用于理解“系统最近怎样处理过类似步骤”。

study 聚合反馈用于理解“历史上哪些路径有效、哪些无效”。

三者不能混用，构造与注入规则见 `docs/impl/session.md`。

```text
conversation_context 影响本次 scene / goal / focus 的理解；
trace_context 影响对近邻认知过程、缺口和模式切换的延续判断；
feedback_context 影响 question_cluster_hint 和 ActivationLink 历史加权；
近邻 trace 与 study 反馈均不应覆盖用户当前显式目标；
用户当前问题修正了前文意图时，应优先采用最新 query。
```

如果缺少近邻 trace 或反馈摘要，对应字段可以为空。

缺失历史反馈不应阻止检索，只是降低 ActivationLink 匹配中的历史加权。

## 输入契约

建议输入：

```text
RetrievalRequest {
  request_id
  query
  perspective_id
  mode
  trace_id，可为空
  trace_level
  source_filters
  outline_filters
  max_results
  require_evidence
  require_external_evidence
  available_context
  conversation_context
  trace_context
  feedback_context
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `request_id` | 当前 HTTP 请求或后台任务请求 id |
| `query` | 用户问题或子问题 |
| `perspective_id` | 当前认知视角 |
| `mode` | 思维模式，例如检索模式、推理模式、查证模式 |
| `trace_id` | 当前思考记录 id，可为空；为空时由 agent 或 TraceService 创建 |
| `trace_level` | trace 深度，和 agent 输入保持一致 |
| `source_filters` | 来源范围过滤 |
| `outline_filters` | 原文目录或语义目录过滤 |
| `max_results` | 最大返回数量 |
| `require_evidence` | 是否必须回到证据 |
| `require_external_evidence` | 是否需要外部候选证据 |
| `available_context` | 用户或上游显式提供的附加上下文，见 `session.md` |
| `conversation_context` | 服务端构造的多轮对话摘要与 recent turns，见 `session.md` |
| `trace_context` | 近邻 trace 认知摘要，见 `session.md` § trace_context |
| `feedback_context` | study 聚合后的历史反馈摘要，见 `session.md` § feedback_context |

`conversation_context`、`trace_context`、`feedback_context` 由 `QueryContextBuilder` 在 `/api/query` 入口构造，不由 retrieval 自行查询 session / study 表。

`RetrievalRequest` 应能由 `AgentRequest` 直接派生。

其中 `request_id / query / perspective_id / mode / source_filters / outline_filters / require_evidence / require_external_evidence / available_context / trace_level` 与 `AgentRequest` 对齐。

`require_evidence` 和 `require_external_evidence` 由 `ProblemFramingService`、router 或用户显式要求决定。

如果 `perspective_id` 为空，retrieval 可以降级为默认 Perspective，但必须记录 `perspective_missing` warning。

如果 `trace_id` 为空，retrieval 不自行创建完整 trace，只在输出中返回可供 `TraceService` 写入的结构化记录。

## 多路检索

第一版至少包含以下检索路径：

```text
领域和概念激活；
unit full-text retrieval；
knowledge point retrieval；
outline tree retrieval（两阶段：document route match + outline tree match）；
来源位置回查。
```

第一版不把向量相似检索作为必要能力。

向量检索可以保留接口，但不作为 `precompile` 和 `retrieval` 的主链路假设。

### 领域和概念激活

使用当前认知视角下的 active / preset 领域和概念理解问题。

Domain / Concept 激活可以使用规则预筛加 LLM 判定。

LLM 只负责在候选 Domain / Concept 中选择，不生成新的正式结构。

如果规则预筛已经得到少量高置信 Domain / Concept，可以跳过 LLM 判定。

如果规则预筛没有候选，不应把全部认知结构输入模型。

提示词和输出 schema：

```text
prompts/retrieval/cognitive_activation.md
schemas/retrieval/cognitive_activation.schema.json
```

Domain / Concept 匹配必须消费 `CognitiveRouteContext`。

匹配方法：

```text
1. 用 knowledge_relevance_hints、scene 和 focus 规则预筛 Domain；
2. 在候选 Domain 下预筛 Concept；
3. 对候选 Domain / Concept 调用 cognitive_activation prompt；
4. 模型只能从候选集中选择，不得创建新结构；
5. 输出 relevance、confidence、route_match_reason 和 reason；
6. 低置信或边界不匹配的候选不得进入 matched_concepts。
```

判断标准：

```text
scene 决定问题应进入哪个业务或认知环境；
goal 决定概念是否服务本次目标；
pattern 决定是否需要经验路径、工作模型、查证或冲突处理；
focus 决定概念是否覆盖本次注意焦点；
knowledge_relevance_hints 决定文本和语义相关性；
feedback_context 只能作为加权信号，不能覆盖当前 query。
```

认知结构检索不是拿 query 直接搜索全文。

它的过程是：

```text
query
  -> ProblemFramingService 拆成 CognitiveRouteContext
  -> 匹配当前 Perspective 下的 Domain / Concept
  -> 生成这些 Concept 下的 ActivationLink 候选
  -> ActivationLinkMatcher 按 CognitiveRouteContext 选择 Top-K
  -> 通过 ActivationLink 找到 KnowledgePoint
  -> 通过 KnowledgePoint 回到 KnowledgeUnit
```

`Domain / Concept` 表示系统认为当前问题应该从哪个认知角度理解。

`ActivationLink` 表示在这个认知角度下，哪些知识点和知识单元已经被挂到该概念下面，可以被该概念激活。

因此，匹配到 `Domain / Concept` 后，retrieval 不应无条件展开概念下全部链接。

它应先查询 active 候选：

```text
activation_links
  where perspective_id = current perspective
  and concept_id in matched_concept_ids
  and status = active
```

retrieval 不得查询 candidate 作为召回候选。active 命中为空或验证不足时，直接转入 SupplementalRetrievalService；candidate 只由 study 的独立验证流程读取。

然后由 `ActivationLinkMatcher` 根据 `CognitiveRouteContext` 对 active 候选过滤和排序。

匹配信号至少包括：

```text
scene_match；
goal_match；
pattern_match；
focus_match；
knowledge_relevance_match；
historical_feedback_score；
link_confidence；
used_count / positive_feedback_count / negative_feedback_count；
boundary_mismatch_penalty。
```

第一版可以先实现为规则打分加文本相关性：

```text
ActivationLinkScore =
  domain_concept_score
  + link_confidence
  + knowledge_point_text_score
  + scene_goal_pattern_focus_score
  + historical_feedback_score
  - negative_feedback_penalty
```

`ActivationLinkMatcher` 输出 Top-K 激活链接。

只有 Top-K 链接才继续回查 `KnowledgePoint / KnowledgeUnit`。

模型提示词和 schema：

```text
prompts/retrieval/activation_link_match.md
schemas/retrieval/activation_link_match.schema.json
```

第一版推荐先规则打分。

当候选链接数量较少或规则分数差距明显时，可以不调用模型。

当出现以下情况时，可以调用 `activation_link_match` prompt：

```text
同一 Concept 下候选 ActivationLink 过多；
多个链接文本相似但适用场景不同；
scene / goal / pattern / focus 与 link tags 存在部分匹配；
历史反馈和当前 query 信号冲突；
需要判断 boundary 是否匹配。
```

模型输出只用于排序和解释。

它不能创建新的 ActivationLink，也不能把 candidate 注入本次检索结果。SupplementalRetrievalService 独立命中并采用知识后，可以写入学习信号，供 study 对相同目标的 candidate 进行验证。

每条 `ActivationLink` 至少指向：

```text
concept_id
knowledge_point_id
unit_id
```

随后 retrieval 回查：

```text
KnowledgePoint
KnowledgeUnit
SourceSpan
```

这样得到的不是普通文本命中，而是认知结构激活结果。

它可以解释：

```text
为什么这个知识单元被找出来；
它是通过哪个 Domain / Concept 被激活的；
它经过哪条 ActivationLink；
对应哪个 KnowledgePoint；
最终回到哪个 KnowledgeUnit。
```

例如：

```text
问题：为什么知识单元不能切得太碎？

匹配：
Domain: 知识组织
Concept: 知识单元边界

激活：
Concept: 知识单元边界
  -> ActivationLink
  -> KnowledgePoint: 知识单元不应该退化成一句话知识点
  -> KnowledgeUnit: 知识单元和知识点的边界
```

如果匹配到了 Concept，但没有对应 active `ActivationLink`，说明系统知道这个概念，但还没有把材料侧知识挂到这个概念下面。

这应记录为激活路径缺口。

如果通过 `ActivationLink` 激活出的结果在后续重排中相关性较弱，说明该激活路径可能过宽、过弱或边界不准。

这应记录为弱激活信号。

输出：

```text
matched_domain_ids
matched_concept_ids
domain_relevance
concept_relevance
activated_unit_ids
activated_knowledge_point_ids
activation_confidence
```

认知结构激活结果应携带：

```text
domain_id
concept_id
activation_link_id
knowledge_point_id
unit_id
activation_score
match_reason
source_spans
hit_route = domain_concept_activation
```

如果同一个 `KnowledgeUnit` 被多个 Concept 激活，应按 `unit_id` 合并，并保留所有相关的 `domain_id / concept_id / activation_link_id / knowledge_point_id`。

激活评分可以综合：

```text
domain_confidence；
domain_relevance；
concept_confidence；
concept_relevance；
activation_link.confidence；
activation_link.used_count；
knowledge_point.confidence；
unit.status；
evidence_ready。
```

第一版不要求复杂公式，但必须保留分数来源，方便后续解释、重排和学习。

### 状态过滤

retrieval 所有召回路径必须统一过滤不可用对象。

可参与新检索的对象：

```text
SourceDocument.status not in disabled / discarded / deleted；
KnowledgeUnit.status = active；
KnowledgePoint.status = active；
unit_search_index.status = active；
knowledge_point_search_index.status = active；
outline_search_index.status = active；
ActivationLink.status = active。
```

不可参与新检索、评分、used_knowledge 或 supported_claim 的对象：

```text
disabled；
discarded；
deleted。
```

如果历史 trace 或 EvidenceRef 指向不可用对象，只能作为历史引用展示，不得作为当前回答证据。

### 知识单元检索

知识单元检索基于 `precompile` 生成的 `unit_search_index`。

索引字段：

```text
title
center
content
source_spans
status
```

知识单元是第一版检索的基础层。

领域和概念激活失败时，知识单元检索仍应工作。

执行方式：

```text
query
  -> Bleve units 索引召回（gse 分词 + BM25；title^3 / center^2 / body）
  -> SQL 资格过滤（unit_search_index + knowledge_units + source_documents）
  -> 返回 unit_id、source_document_id、source_spans、route_score（归一化 BM25）
```

字段 boost 在 Bleve mapping 中配置；`title / center` 命中应高于普通正文命中。详见 `docs/impl/fts.md`。

`center` 表达知识单元的核心主题或判断，适合承接抽象问题。

返回候选：

```text
hit_route = unit_text_search
unit_id
source_document_id
source_spans
route_score
snippet
```

### 知识点检索

知识点检索基于 `precompile` 生成的 `knowledge_point_search_index`。

知识点用于提供更细粒度的激活线索。

返回知识点时，必须同时返回：

```text
knowledge_point_id
unit_id
source_spans
```

知识点不替代知识单元。

执行方式：

```text
query
  -> Bleve points 索引召回（knowledge_point_search_index.text）
  -> SQL 资格过滤
  -> 返回 knowledge_point_id
  -> 回到 unit_id
```

知识点命中说明 query 与某个可激活要点相关。

最终回答和证据仍应回到完整 `KnowledgeUnit`。

返回候选：

```text
hit_route = knowledge_point_search
knowledge_point_id
unit_id
source_document_id
source_spans
route_score
snippet
```

### 文档级目录树检索（两阶段）

目录结构检索应采用文档级树搜索，而不是跨全库直接把所有目录节点平铺召回。

该路线基于 `precompile` 生成的文档级描述、`semantic_outlines` 树结构，以及 `outline_search_index` 的节点到单元映射。

第一版按 PageIndex 思路，收敛为**两阶段语义路由**：

```text
问题多维度结果（CognitiveRouteContext）
  -> 阶段一：Domain + 文档摘要联合匹配
  -> 候选文档树（多个，带 source_score）
  -> 阶段二：候选文档树匹配
  -> 知识单元（多个，带 tree_score）
  -> 与多路召回、认知结构化检索结果融合
  -> 按 unit_id 去重并取 top_k
```

这比「文档选择 + BM25 平铺 + 逐单元 center match」更少模型调用，也更贴近 PageIndex 的多文档扩展方式。

#### 总体原则

```text
检索链路优先 BM25 与 SQL，大模型仅作兜底或精炼（BM25 未命中或归一化分数低于置信阈值时；与 unit 构建阶段的「模型优先、规则兜底」相反）；
阶段一只做候选路由，不直接产出最终知识单元；
阶段二必须按文档分别保留树结构，不得跨文档混成扁平节点列表；
显式 source_filters 时限制候选范围，但仍可进行摘要排序；
模型失败时分别回退，不得让整个目录检索链路静默失效；
top_k 必须在所有通道融合、归一化、去重之后执行。
```

#### 阶段一：Domain + 文档摘要联合匹配

阶段一负责回答：在当前问题视角下，应该先进入哪些 SourceDocument。

输入：

```text
query
CognitiveRouteContext（scene / goal / focus / knowledge_relevance_hints）
source_filters(optional)
available_context(optional)
conversation_context(optional)
规则预筛的 Domain 候选（可选，来自 cognitive 预筛或 framing hints）
BM25 宽召回的文档候选（source_search_index）
```

候选来源：

```text
source_search_index 的 title / description / top_outline_summary；
该 source 下 top-level outline_summary 聚合；
最近 trace 或用户显式选择的 source_filters；
Domain 信号作为路由加权，不直接硬过滤文档（文档表无 domain_id）。
```

执行方式：

```text
1. 有 source_filters 时，直接 SQL 取限定文档，再走语义排序或规则回退；
2. 无 source_filters 且 query 非空时，先 Bleve sources BM25 宽召回 top N（N 默认 8~12；低置信扩到 15）；
3. BM25 最高分达到 BM25ConfidenceThreshold（默认 0.5）时，直接采用 BM25 排序作为候选文档，不调用模型；
4. BM25 有结果但分数偏低：调用 document_route_match 对 BM25 候选做 Domain + 文档摘要联合排序，并写 warning document_route_bm25_low_score_fallback；
5. BM25 未命中（分数低于 min_normalized_score 或结果为空）：加载带摘要的文档池，以 semantic_only 模式调用 document_route_match 做语义兜底，并写 warning document_route_semantic_fallback；
6. 输出多个候选文档，每个带 source_score 和 reason；
7. 不做硬阈值淘汰，低相关文档可保留弱分，避免阶段一漏掉正确文档。
```

评分：

```text
document_route_score =
  domain_relevance * domain_confidence
  * document_relevance * document_confidence
```

其中 `domain_relevance / domain_confidence` 来自阶段一模型对 Domain 信号的判断；`document_relevance / document_confidence` 来自模型对文档摘要的判断。

提示词和输出 schema：

```text
prompts/retrieval/document_route_match.md
schemas/retrieval/document_route_match.schema.json
```

阶段一输出：

```text
DocumentRouteCandidate {
  source_document_id
  title
  description
  domain_score
  source_score
  reason
}
```

候选控制：

```text
有 source_filters 时，只使用 source_filters；
无 source_filters 时，先取 top N 个 source（N 建议 8~12，低置信扩到 15）；
不要用硬阈值过早淘汰候选文档；
BM25 top-3 应作为保底候选保留，即使 LLM 排序失败。
```

回退：

```text
BM25 置信（>= BM25ConfidenceThreshold）-> 直接采用 BM25 排序，source_score = 归一化 BM25；
BM25 低分 -> document_route_match 语义排序，写 document_route_bm25_low_score_fallback；
BM25 未命中 -> semantic_only document_route_match 对文档摘要池语义匹配，写 document_route_semantic_fallback；
LLM 联合排序失败 -> 使用 BM25 source_search_index 排序结果，source_score = 归一化 BM25；
Domain 预筛失败 -> 仅用文档摘要 + BM25 排序，domain_score 记为 0 并写 warning。
```

#### 阶段二：候选文档树匹配

阶段二负责回答：在每个候选文档内，哪些目录节点范围与 query 相关，并展开到 KnowledgeUnit。

输入：

```text
query
CognitiveRouteContext
阶段一输出的 DocumentRouteCandidate 列表
每个候选文档的去正文树结构
```

树结构来源：

```text
semantic_outlines（parent_id / path / summary / center）；
source outline 顶层节点摘要；
必要时从 outline_search_index 聚合 path + summary + children_count。
```

树结构输入应去掉正文，只保留：

```text
source_document_id
node_id
outline_type
outline_path
outline_summary
child_paths 或 children_count
unit_count（如有）
```

执行方式：

```text
对每个候选 source_document_id：
  1. 加载该文档的树结构（不传正文）；
  2. 先在候选 source 内执行 outline_path_search（Bleve outlines + SQL）；
  3. BM25 最高分达到 BM25ConfidenceThreshold 时，直接采用词法结果，不调用模型；
  4. BM25 未命中或分数偏低时，调用 outline_tree_match prompt，让模型返回合法 source_id + node_id；
  5. 将命中节点映射到 unit_id：
     - 节点有直接 unit：直接命中；
     - 节点无直接 unit：展开到相关子节点，或查该 path 下 outline_search_index 行；
  6. 父节点命中且子节点很多时，优先下钻子节点，而不是一次展开大量 unit；
  7. 模型失败或无结果时，回退到步骤 2 的 BM25 结果；
  8. 输出 tree_score 和 matched_path。
```

评分：

```text
tree_route_score =
  0.2 * document_route_score +
  0.8 * tree_node_relevance
```

提示词和输出 schema：

```text
prompts/retrieval/outline_tree_match.md
schemas/retrieval/outline_tree_match.schema.json
```

阶段二输出：

```text
hit_route = outline_tree_match
unit_id
source_document_id
node_id
matched_path
outline_summary
domain_score
source_score
tree_score
source_spans
match_reason
```

回退：

```text
BM25 置信 -> 直接采用 outline_path_search 结果；
BM25 低分或未命中 -> outline_tree_match 语义匹配；
LLM 失败或无结果 -> 回退到该 source 内的 outline_path_search（Bleve + SQL）；
单文档树过大 -> 先 BM25 预筛节点，再分批送模型；仍超限则只保留 BM25 结果。
```

目录节点质量控制：

```text
outline_summary 为空或低质量时，降低 tree_node_relevance；
semantic outline 节点带 needs_review 时，不作为唯一高置信召回依据；
source outline 节点只有标题、没有说明时，可参与 path filter，但不应单独触发高置信 tree match；
当父节点 summary 命中但子节点很多时，应优先继续检索子节点 summary，而不是直接展开大量 unit。
```

#### 词法回退路径：outline_path_search

当阶段二模型不可用，或需要与语义树匹配并行保留宽召回时，仍可在候选 source 内执行 Bleve outlines 词法召回。

执行方式：

```text
query
  -> 使用阶段一候选 source_document_id
  -> Bleve outlines 索引召回（outline_path / outline_summary / unit_title / unit_center）
  -> SQL 执行 source_document_id、outline_type、path_prefix、path_keyword 过滤
  -> 返回相关 outline node
  -> 展开最终 outline node 到对应 unit_id
```

返回候选：

```text
hit_route = outline_path_search
unit_id
source_document_id
source_score
matched_path
outline_summary
source_spans
route_score
```

`outline_path_search` 是目录树通道的词法回退，不是独立第三次模型调用。

`outline_summary` 由目录节点说明提供：

```text
semantic outline -> semantic_outlines.summary
source outline -> source heading section summary
recovered outline -> recovered heading section summary
manual outline -> manual summary or generated summary
```

它用于提高路径匹配的语义准确性，避免只依赖标题词面命中。

如果 `outline_summary` 只是标题复述或为空，该目录节点只能作为低质量路径命中，不应在融合阶段获得高权重。

### 多路召回执行流程

多路召回不是认知结构检索的替代品。

它负责在现有 `Domain / Concept` 没有覆盖或覆盖较弱时，仍然从材料侧发现相关知识。

第一版执行流程：

```text
RunSupplementalRetrieval(query):
  unit_hits = SearchUnitIndex(query)
  point_hits = SearchKnowledgePointIndex(query)
  doc_candidates = RunDocumentRouteMatch(query, cognitive_route_context, source_filters)
  tree_hits = RunOutlineTreeMatch(query, doc_candidates, outline_filters)
  outline_fallback_hits = SearchOutlineTree(query, doc_candidates, outline_filters)  // 阶段二失败时
  source_hits = ResolveSourceSpans(unit_hits + point_hits + tree_hits + outline_fallback_hits)
  return MergeSupplementalHitsByUnit(...)
```

其中：

```text
Bleve units 索引提供基础全文召回（BM25 + 字段 boost）；
Bleve points 索引提供判断、定义、规则等要点召回；
Bleve sources 索引提供阶段一宽召回；
阶段一 document_route_match 做 Domain + 文档摘要联合路由；
阶段二 outline_tree_match 按文档分别做树节点语义匹配；
Bleve outlines 索引在候选文档内提供词法预筛与 outline_path_search 回退；
SQLite 四张 search_index 表负责资格过滤与 rebuild 源；
source span lookup 保证候选结果可回到证据。
```

词法层实现见 `docs/impl/fts.md`。

如果某条路线失败，retrieval 应记录 warning，并继续使用其他路线。

多路召回结果必须保留 `hit_route`，否则后续无法判断是认知结构激活命中，还是材料侧补充查找命中。

### 来源位置回查

所有用于回答的知识必须能回到：

```text
KnowledgeUnit
SourceSpan
SourceDocument
normalized Markdown
preview HTML
original source
```

第一版以 Markdown 行号为精确证据锚点。

HTML 仅作为预览入口，不要求块级定位。

## RetrievalFusionService

`RetrievalFusionService` 负责融合认知结构检索和多路召回结果。

它默认不调用 LLM。

语义判断应尽量在上游召回路径内部完成：

```text
CognitiveActivationService
  使用模型判断 query 应激活哪些 Domain / Concept；

SupplementalRetrievalService
  阶段一使用 document_route_match 做 Domain + 文档摘要联合路由；
  阶段二使用 outline_tree_match 在候选文档树内做节点语义匹配。
```

`RetrievalFusionService` 消费这些上游结果中的语义分数和理由，做确定性合并、通道内归一化、去重、重排和信号生成。

只有在候选冲突严重、分数高度接近或推理模式明确需要按证据槽位组合候选时，才增加 LLM rerank。推理模式中的槽位 Reranker 属于模板驱动链路，不再以单一总相关分为目标。

它不是第一版主链路。

### 合并规则

认知结构检索和多路召回都会产生候选。

合并时应按 `unit_id` 归并。

同一知识单元可记录多个命中来源：

```text
domain_concept_activation
unit_text_search
knowledge_point_search
outline_tree_match
outline_path_search
source_span_lookup
```

`outline_center_match` 已由两阶段目录检索中的 `outline_tree_match` 取代，不再作为独立第三路模型调用。

合并时不能只保留最高分路径。

必须保留每一路命中信息，便于解释、trace 和后续 GapDetection。

融合结果结构：

```text
FusedRetrievalResult {
  unit_id
  knowledge_point_ids
  source_document_id
  source_spans

  title
  center
  snippet

  hit_routes
  hit_channels
  route_scores

  matched_domain_ids
  matched_concept_ids
  activation_link_ids
  matched_paths
  node_id

  domain_score
  source_score
  tree_score

  content_relevance
  cognitive_relevance
  multi_route_support
  route_agreement_bonus
  candidate_validation
  evidence_quality
  history_signal
  status_penalty
  rerank_score
  score_breakdown
  rank

  fusion_signals
  evidence_ready
  warnings
  slot_matches
}
```

`slot_matches` 记录当前知识单元覆盖哪些模板槽位、匹配分、绑定事实和证据质量：

```text
SlotMatch {
  slot_id
  score
  fact_ids
  evidence_ref_ids
  entity_binding
  temporal_binding
  authority_level
  warnings
}
```

### 槽位覆盖重排

`SlotCoverageRerankService` 消费 `ReasoningPatternPlan`、融合候选和 `slot_matches`。它的目标是在控制冗余和证据成本的同时覆盖必需槽位，并保留高权威冲突证据、例外和反证：

```text
maximize
  required_slot_coverage
  + evidence_authority
  + source_diversity
  + contradiction_visibility
  - redundancy
  - evidence_cost
```

它输出 `selected_candidates`、`slot_coverage`、`uncovered_required_slots`、`conflicting_slots`、`external_verification_slots` 和 `selection_reason`。

槽位覆盖不能只以存在匹配结果判定。只有实体一致、时间有效、来源质量满足模板要求且 EvidenceRef 可回链时，槽位才可标记为 `covered`。

### 分数来源

Fusion 使用已有信号，不重新做语义判断。

输入信号包括：

```text
来自认知结构检索：
  domain_relevance
  domain_confidence
  concept_relevance
  concept_confidence
  activation_link.confidence
  activation_link.status
  activation_link.used_count
  knowledge_point.confidence
  activation_reason

来自多路召回：
  unit_text_score
  knowledge_point_score
  outline_tree_score
  outline_path_score
  tree_node_relevance
  document_route_score
  tree_match_reason

来自目录树通道：
  domain_score
  source_score
  tree_score

来自证据和状态：
  source_spans 是否存在
  evidence_ready
  unit.status
  needs_review
  source quality

来自历史使用：
  activation_count
  used_count
  positive_feedback_count
  negative_feedback_count
```

第一版可按以下维度形成 `score_breakdown`：

```text
content_relevance
cognitive_relevance
tree_relevance
multi_route_support
route_agreement_bonus
candidate_validation
evidence_quality
history_signal
status_penalty
```

### 通道归一化与最终分数

最终融合时，不宜直接比较不同检索通道的原始分数。

应先在各通道内部归一化，再加权合并：

```text
normalized_tree_score        = normalize(outline_tree_match + outline_path_search)
normalized_multi_recall_score = normalize(unit_text_search + knowledge_point_search)
normalized_cognitive_score   = normalize(domain_concept_activation)

final_score =
  w_tree      * normalized_tree_score +
  w_multihit  * normalized_multi_recall_score +
  w_cognitive * normalized_cognitive_score
```

同一知识单元被多个通道命中时，应合并而非生成重复结果，并可增加一致性奖励：

```text
final_score += route_agreement_bonus
```

`route_agreement_bonus` 应随命中通道数递增，但不得让弱相关结果仅凭多路命中排到前面。

`top_k` 截断必须在通道归一化、加权、去重之后执行。

`rerank_score` 默认等于 `final_score`，并保留 `domain_score`、`source_score`、`tree_score` 和 `hit_channels` 便于调试与 trace。

### ActivationLink 评分约束

`ActivationLink` 评分必须符合其状态语义：

```text
active ActivationLink 可以贡献正式 cognitive_relevance；
candidate ActivationLink 不进入 retrieval 的召回、评分、融合和 used_knowledge；
disabled / discarded / deleted ActivationLink 不参与召回和评分。
```

建议规则：

```text
active_link_score =
  domain_relevance
  * domain_confidence
  * concept_relevance
  * concept_confidence
  * activation_link.confidence
  * knowledge_point.confidence

negative_feedback_count 必须降低 history_signal；
positive_feedback_count 和去重 used_count 只能在有证据回链时提高 history_signal。
```

运行时检索采用分阶段策略：

```text
阶段 1：仅匹配 active ActivationLink，直接取回其 KnowledgePoint / KnowledgeUnit；
阶段 2：验证正式路径的内容回答度、边界匹配和 EvidenceRef；
阶段 3：正式路径不足时才启动材料侧补充召回；
阶段 4：补充召回结果进入回答筛选，并作为独立材料交给 study 验证 candidate ActivationLink。
```

candidate 验证不得反向影响本次 retrieval 排名。验证通过后的关系在后续请求中以 `active` 身份参与优先检索；验证失败则由 study 转为 `discarded`。

建议配置：

```text
retrieval.fusion.w_tree = 0.35
retrieval.fusion.w_multihit = 0.40
retrieval.fusion.w_cognitive = 0.25
retrieval.fusion.route_agreement_bonus_per_channel = 0.05
retrieval.document_route.source_top_k = 12
retrieval.document_route.source_top_k_low_confidence = 15
```

### 排序原则

排序应体现：

```text
多路召回负责不要漏；
认知结构负责更会用；
目录树通道负责结构范围定位；
Fusion 负责在各类通道信号之间选择当前问题最值得使用的知识单元。
```

规则：

```text
不得直接比较不同通道的原始分数，必须先做通道内归一化；
同等内容相关性下，认知结构命中的结果可以适当靠前；
同一个 unit 被多条路线或通道命中，应提升 multi_route_support 和 route_agreement_bonus；
有明确 source_spans 和 evidence_ready 的结果应靠前；
needs_review、证据不足或转换质量弱的结果应降权；
认知结构命中但材料侧相关性弱，不应无条件靠前；
多路召回强但认知结构未命中，应保留并交给 GapDetection 判断；
top_k 必须在融合、归一化、去重之后截断。
```

### Fusion 信号

Fusion 阶段应标记可供 trace 和 GapDetection 使用的信号：

```text
strong_cognitive_hit
cognitive_only_hit
supplemental_only_hit
multi_route_strong_hit
weak_activation_signal
weak_evidence
needs_review
route_conflict
```

含义：

| 信号 | 含义 |
| --- | --- |
| `strong_cognitive_hit` | 认知结构命中，且材料侧召回也支持 |
| `cognitive_only_hit` | 认知结构命中，但材料侧相关性弱 |
| `supplemental_only_hit` | 多路召回命中，但认知结构没有命中 |
| `multi_route_strong_hit` | 多条材料侧路线命中同一 unit |
| `weak_activation_signal` | active ActivationLink 命中但内容、边界或证据支持不足 |
| `weak_evidence` | 缺少可靠来源证据 |
| `needs_review` | unit 或来源质量需要复核 |
| `route_conflict` | 不同路线给出的相关性判断冲突 |

`RetrievalFusionService` 不直接生成最终认知缺口。

它只产生融合结果和可判断信号。

最终缺口由 `GapDetectionService` 基于融合结果、最终采用结果、trace 和反馈判断。

### 输出契约

`RetrievalFusionService` 输出：

```text
ranked_results: FusedRetrievalResult[]
route_summary
fusion_signals
score_breakdown
rerank_warnings
```

其中：

```text
ranked_results
  按 rerank_score 排序后的融合候选；

route_summary
  统计每类 hit_route 的命中数量、最高分、是否参与最终结果；

fusion_signals
  从所有 fused results 汇总出的信号，供 trace 和 GapDetectionService 使用；

score_breakdown
  汇总本次融合使用的分数来源和权重；

rerank_warnings
  记录缺少证据、分数冲突、候选过多、部分路线失败等问题。
```

## GapDetectionService

`GapDetectionService` 负责识别认知结构缺口和知识缺口。

它不直接修改认知结构。

它只输出缺口记录和学习输入，进入 trace，再由 study 判断是否形成候选领域、候选概念或候选激活路径。

### 输入

```text
query
perspective_id
matched_domains
matched_concepts
activation_hits
supplemental_hits
fused_results
fusion_signals
used_items，可为空
feedback，可为空
```

### 认知结构缺口

认知结构缺口表示材料中可能有相关知识，但当前认知结构没有很好激活它。

触发信号：

```text
未匹配到 Domain / Concept；
匹配到 Concept 但没有 active ActivationLink；
认知结构激活结果为空；
认知结构激活结果低相关；
fusion_signals 包含 supplemental_only_hit；
多路召回结果最终被 used；
用户反馈表明当前概念理解不足。
```

缺口类型：

```text
domain_missing
concept_missing
activation_link_missing
weak_activation
concept_boundary_mismatch
domain_boundary_mismatch
```

建议结构：

```text
CognitiveGap {
  gap_type
  perspective_id
  query
  failed_domain_ids
  failed_concept_ids
  activation_link_ids
  adopted_unit_ids
  adopted_knowledge_point_ids
  supporting_hit_ids
  suggested_domain_names
  suggested_concept_names
  reason
  confidence
}
```

### 知识缺口

知识缺口表示系统没有足够材料或证据回答当前问题。

触发信号：

```text
认知结构检索无有效结果；
多路召回无有效结果；
检索结果 evidence_ready = false；
source_spans 缺失或无法读取；
结果存在明显冲突但缺少证据判断；
问题需要当前外部证据，但 DDG 外部查证也无法得到可用候选。
```

缺口类型：

```text
missing_material
missing_evidence
source_unavailable
conflicting_evidence
outdated_knowledge
external_verification_required
```

建议结构：

```text
KnowledgeGap {
  gap_type
  query
  unit_ids
  source_document_ids
  reason
  required_evidence
  severity
}
```

### 输出

```text
cognitive_gaps
knowledge_gaps
weak_activation_signals
suggested_learning_inputs
warnings
```

### 和 RetrievalFusionService 的边界

`RetrievalFusionService` 只产生融合结果和 `fusion_signals`。

`GapDetectionService` 基于这些信号、最终采用结果、trace 和反馈判断缺口。

它不创建正式 Domain / Concept，也不直接更新 ActivationLink。

## EvidenceTracingService

`EvidenceTracingService` 负责把候选知识和最终采用知识回到来源证据。

它不负责检索排序，也不负责判断认知缺口。

### 输入

```text
fused_results
used_items，可为空
require_evidence
```

### 证据回链目标

每个用于回答的知识至少应能回到：

```text
KnowledgeUnit
SourceSpan
SourceDocument
normalized Markdown
preview HTML
original source
```

第一版以 Markdown 行号为精确证据锚点。

HTML 仅作为预览入口，不要求块级定位。

如果 HTML 或原始文件包含图片化内容，不能强行生成精确 HTML block 证据。

应以 normalized Markdown 和 SourceSpan 为准，并在 warning 中标记证据精度。

### EvidenceRef

建议结构：

```text
EvidenceRef {
  unit_id
  knowledge_point_id
  source_document_id
  source_spans
  normalized_markdown_path
  preview_html_path
  original_source_path
  quote
  evidence_ready
  evidence_level
  warnings
}
```

`evidence_level` 可取：

```text
markdown_line
markdown_block
html_preview
original_file
unavailable
```

如果 EvidenceRef 指向的 SourceDocument / KnowledgeUnit / KnowledgePoint 当前状态为 `disabled / discarded / deleted`：

```text
evidence_ready = false；
evidence_level = unavailable；
warnings 包含 current_evidence_unavailable；
不得作为 supported_claim 的证据；
历史 trace 可以继续显示该引用及当前不可用状态。
```

如果状态为 `deleted`，展示层应说明来源已删除，不能尝试重新读取该来源作为当前证据。

### 输出

```text
evidence_refs
evidence_summary
evidence_ready
warnings
```

如果 `require_evidence = true`，且结果无法形成 `EvidenceRef`，该结果不能作为最终回答的强证据，只能作为候选或背景。

## VerificationService

`VerificationService` 负责对事实、来源和时效性进行轻量查证。

第一版不实现完整查证模式，也不把外部搜索结果导入 source 主链路。

它只通过 DDG 免费搜索接口生成低可信外部证据候选。

外部搜索证据的关键词规划、搜索结果去重、网页正文抽取、噪音清洗、模型证据抽取、异步任务和展示规则，单独定义在：

```text
docs/impl/external-evidence.md
```

本节只定义 `retrieval` 主链路与外部查证之间的契约边界。

第一版中，外部搜索默认作为异步辅助链路执行。

它不阻塞当前回答，不替代内部 `EvidenceRef`，也不自动影响当前回答的确定性。

### 输入

```text
query
claims_to_verify
evidence_refs
conflicts
require_external_evidence
source_filters
```

### 输出

```text
verification_start_result
verification_completion_result，可为空
warnings
```

同步调用阶段只保证返回 `verification_start_result`。

`verification_start_result` 对应 `docs/impl/external-evidence.md` 中的 `VerificationStartResult`，表示是否创建外部查证任务、是否跳过、是否创建失败。

`verification_completion_result` 只在以下情况可以非空：

```text
外部查证被配置为同步等待；
后台任务已经在当前请求返回前完成；
调用方读取的是异步完成后的结果。
```

第一版默认异步执行，因此主链路通常只有 `verification_start_result`，没有 `external_evidence_candidates / verified_claims / unverified_claims` 的完成结果。

如果主链路没有完成结果，`RetrievalResult.data.external_evidence_candidates` 应为空或只包含已有历史候选，不能把未完成任务当作候选证据。

### ExternalEvidenceCandidate

```text
ExternalEvidenceCandidate {
  title
  url
  snippet
  source_name
  retrieved_at
  search_provider
  search_query
  credibility
  confidence
  warnings
}
```

第一版规则：

```text
search_provider = ddg；
credibility 默认 low；
confidence 默认不高于 0.4；
搜索摘要不能当作事实原文；
外部搜索默认异步执行，不阻塞当前回答；
外部候选证据不能替代内部 EvidenceRef；
外部候选证据不能自动转成 KnowledgeUnit；
如果需要沉淀为长期知识，必须后续走 source 导入和 unit 切分流程。
```

如果 DDG 不可用，应返回 warning，并把相关 claim 标记为 `unverified`。

如果外部异步查证发现强冲突，只能生成 `external_conflict_found` 事件或 warning，提示后续进入查证或冲突检测流程，不能静默修改当前回答。

## ConflictDetectionService

`ConflictDetectionService` 负责识别检索结果、证据、结论和推理之间的冲突。

它不负责生成最终回答，也不直接决定是否切换整个思维模式。

第一版以规则判断为主。

### 输入

```text
query
ranked_results
used_items，可为空
evidence_refs
matched_domains
matched_concepts
cognitive_gaps
knowledge_gaps
```

### 冲突类型

```text
fact_conflict
definition_conflict
condition_conflict
time_conflict
source_conflict
reasoning_conflict
scope_difference
unresolved_conflict
```

### 规则

```text
同一问题下多个候选结论互斥，标记 fact_conflict；
同一概念在不同证据中定义边界不同，标记 definition_conflict；
结论不同但适用条件不同，优先标记 scope_difference；
新旧材料不一致，标记 time_conflict；
权威来源和非权威来源不一致，标记 source_conflict；
证据本身不冲突但推理结论不一致，标记 reasoning_conflict；
证据不足无法裁决，标记 unresolved_conflict。
```

### 冲突处理

```text
scope_difference 不直接当作强冲突，应在回答中说明适用条件；
time_conflict 可以优先新证据，但必须保留旧证据说明；
source_conflict 可以优先高可信来源，但必须标记不同说法；
unresolved_conflict 不输出单一确定结论；
strong_conflict 应进入推理模式处理；
require_evidence = true 且冲突无法解决时，不能给确定回答。
```

`strong_conflict` 不应默认把整个问题升级到推理模式。

如果冲突只影响局部结论，应生成 `conflict_case`，只把冲突点交给推理模式处理。

只有当冲突影响核心问题结论时，才建议整体切换到推理模式。

### ConflictCase

```text
ConflictCase {
  conflict_id
  conflict_type
  conflicting_claims
  conflicting_evidence_refs
  related_unit_ids
  related_knowledge_point_ids
  related_domain_ids
  related_concept_ids
  scope_conditions
  source_metadata
  why_conflict
}
```

处理结果可取：

```text
resolved
scope_split
prefer_one_with_reason
unresolved
need_more_evidence
```

### 输出

```text
conflicts
conflict_cases
strong_conflicts
resolution_suggestions
warnings
```

## ModeSwitchEvaluationService

`ModeSwitchEvaluationService` 负责判断当前流程是否继续、局部升级、整体切换或安全退出。

它不直接执行切换。

它只输出结构化决策，由 agent 和 router 执行。

### 输入

```text
query
current_mode
problem_framing
ranked_results
evidence_refs
cognitive_gaps
knowledge_gaps
conflicts
reasoning_result，可为空
require_evidence
```

### 输出

```text
continue_current_mode
whole_mode_switch
local_mode_escalations
fallback_action
reason
warnings
```

`whole_mode_switch` 表示整个问题切换到其他 agent。

`local_mode_escalation` 表示只把局部冲突、局部缺口或局部不确定性升级处理。

### 兜底规则

```text
检索结果足够、证据一致、问题简单，继续检索模式；
检索结果有用但不能直接回答，且需要多跳、跨材料组合或隐含推导，整体切到推理模式；
局部 strong_conflict 不影响核心答案，只生成 local_mode_escalation；
strong_conflict 影响核心答案，整体切到推理模式；
证据不足但能说明缺口，返回证据不足并记录 KnowledgeGap；
认知结构未命中但多路召回命中，继续回答并记录 CognitiveGap；
认知结构和多路召回都未命中，返回不知道，不编造；
require_evidence = true 且无证据，不给确定回答；
推理模式仍无法回答，返回部分结论、缺口和所需证据。
```

## ResponseAssemblyService

`ResponseAssemblyService` 负责把检索结果、推理结果、证据、冲突和缺口组织成最终回答。

它不负责检索，不负责推理，也不直接修改认知结构。

### 输入

```text
query
mode
reasoning_result，可为空
used_knowledge
evidence_refs
cognitive_gaps
knowledge_gaps
conflicts
mode_switch_decision
answer_style
```

### 输出

```text
answer
answer_sections
claim_refs
inference_refs
unsupported_claims
uncertainty_notes
warnings
```

### 回答内容分类

```text
supported_claim
inferred_claim
unsupported_or_gap
```

规则：

```text
supported_claim 必须绑定 EvidenceRef；
inferred_claim 必须绑定 used_knowledge 和 reasoning_step；
unsupported_or_gap 必须绑定 KnowledgeGap 或 warning；
冲突未解决时，不输出单一确定结论；
证据不足时，明确说明回答范围和缺失证据；
不能把模型推断伪装成原文事实。
```

第一版不要求复杂引用格式。

但结构化输出中必须保留 claim 到 evidence / inference / gap 的映射，方便 trace、展示和复查。

## 输出契约

`retrieval` 应返回 `RetrievalResult`。

外层结果字段遵循统一结果对象：

```text
RetrievalResult {
  success
  status
  data
  error
  warnings
  request_id
  trace_id
}
```

`status` 可取：

```text
succeeded
partial
failed
no_result
insufficient_evidence
```

`error` 必须使用 `SystemError` 字段，或包含同等字段的模块错误结果。

`data` 输出给 working model：

```text
query
perspective_id
mode
matched_domains
matched_concepts
results
used_knowledge
cognitive_gaps
knowledge_gaps
evidence_refs
evidence_summary
external_evidence_candidates
conflicts
conflict_cases
mode_switch_decision
answer，可为空
answer_sections，可为空
claim_refs
inference_refs
unsupported_claims
uncertainty_notes
learning_signals
```

其中 `results` 必须包含可追溯证据。

字段对齐关系：

| retrieval 输出 | 下游 |
| --- | --- |
| `results` | `WorkingModelService` 的候选知识输入 |
| `used_knowledge` | `AgentResult.used_knowledge` 和 `TraceRecord.used_items` |
| `evidence_refs` | `AgentResult.evidence` 和 `TraceRecord` 证据记录 |
| `cognitive_gaps` | `AgentResult.cognitive_gaps` 和 study 输入 |
| `knowledge_gaps` | `AgentResult.knowledge_gaps` 和 study 输入 |
| `conflicts / conflict_cases` | `WorkingModelService`、`ReasoningService`、trace |
| `external_evidence_candidates` | `VerificationService` 输出和 trace 记录 |
| `mode_switch_decision` | agent 的 `mode_switch_request` 来源 |
| `answer / answer_sections` | `ResponseAssemblyService` 的自然语言输出，可为空 |
| `claim_refs / inference_refs / unsupported_claims` | 回答证据映射和 trace |
| `learning_signals` | `LearningSignalService` 输出，第一版只保存 |

如果当前调用只执行检索相关 service，不执行 `ResponseAssemblyService`，则 `answer / answer_sections` 可以为空，但 `results / evidence_refs / warnings` 仍必须返回。

如果 `require_evidence = true` 且没有可用 `EvidenceRef`，不得输出确定回答，只能返回候选、缺口和 warning。

如果只有 `external_evidence_candidates` 而没有内部 `EvidenceRef`，不得把外部候选证据作为强证据。

## trace 记录

检索必须记录：

```text
使用了哪个认知视角；
匹配了哪些领域和概念；
领域和概念激活了哪些知识；
补充查找找到了哪些知识；
融合排序如何处理这些候选；
EvidenceTracingService 为哪些结果生成了证据引用；
VerificationService 生成了哪些外部候选证据；
ConflictDetectionService 发现了哪些冲突；
ModeSwitchEvaluationService 如何判断整体切换或局部升级；
ResponseAssemblyService 如何绑定 claim、evidence 和 inference；
哪些结果进入候选；
哪些结果最终被采用；
是否产生认知缺口或知识缺口。
```

trace 区分 retrieved 和 used。

被召回但未使用的知识不应自动强化。

## 数据库表

第一版建议表：

```text
retrieval_events
retrieval_hits
cognitive_gaps
knowledge_gaps
evidence_refs
external_evidence_candidates
conflict_cases
mode_switch_decisions
learning_signals
```

### retrieval_events

```text
id
trace_id
perspective_id
query
mode
created_at
```

### retrieval_hits

```text
id
retrieval_event_id
unit_id
knowledge_point_id
source_document_id
hit_route
hit_channels
domain_id
concept_id
activation_link_id
matched_path
node_id
domain_score
source_score
tree_score
route_score
rerank_score
rank
fusion_signals
source_spans
evidence_ready
used
used_reason
created_at
```

### cognitive_gaps

```text
id
trace_id
retrieval_event_id
gap_type
perspective_id
query
failed_domain_ids
failed_concept_ids
activation_link_ids
adopted_unit_ids
adopted_knowledge_point_ids
supporting_hit_ids
suggested_domain_names
suggested_concept_names
reason
confidence
created_at
```

### knowledge_gaps

```text
id
trace_id
retrieval_event_id
gap_type
query
unit_ids
source_document_ids
reason
required_evidence
severity
created_at
```

### evidence_refs

```text
id
retrieval_event_id
unit_id
knowledge_point_id
source_document_id
source_spans
normalized_markdown_path
preview_html_path
original_source_path
quote
evidence_ready
evidence_level
warnings
created_at
```

### external_evidence_candidates

```text
id
retrieval_event_id
title
url
snippet
source_name
retrieved_at
search_provider
search_query
credibility
confidence
warnings
created_at
```

外部候选证据默认低可信。

它不能替代 `evidence_refs`，也不能自动进入 `KnowledgeUnit`。

### conflict_cases

```text
id
retrieval_event_id
conflict_type
conflicting_claims
conflicting_evidence_ref_ids
related_unit_ids
related_knowledge_point_ids
related_domain_ids
related_concept_ids
scope_conditions
why_conflict
resolution
affects_core_answer
created_at
```

### mode_switch_decisions

```text
id
retrieval_event_id
current_mode
continue_current_mode
whole_mode_switch
local_mode_escalations
fallback_action
reason
warnings
created_at
```

### learning_signals

```text
id
trace_id
retrieval_event_id
signal_type
perspective_id
scene
goal
pattern
focus
related_domain_ids
related_concept_ids
related_unit_ids
related_knowledge_point_ids
reason
confidence
status
created_at
```

第一版只保存学习信号。

不在 retrieval 阶段执行 study，也不直接修改 Domain / Concept / ActivationLink。

retrieval 只输出与补充召回、证据和实际采用有关的学习信号，不读取 candidate 完成召回。candidate 的验证、激活或丢弃只能由 study / review 独立执行。

## 错误处理

retrieval 错误处理必须遵循 `docs/impl/error-handling.md` 的统一规则。

跨 service 或返回给 agent 时，错误必须转换为 `SystemError` 或包含同等字段的模块错误结果。

至少包含：

```text
error_code
message
module = retrieval
operation
stage
target
severity
retryable
entity_type
entity_id
trace_id
request_id
details
```

错误类型：

| 错误码 | 含义 | 处理 | retryable |
| --- | --- | --- | --- |
| `perspective_missing` | 未指定认知视角 | 使用默认认知视角并记录 warning | 否 |
| `index_unavailable` | 检索索引不可用 | 降级到数据库关键词检索 | 是 |
| `source_span_missing` | 缺少证据位置 | 返回候选但标记不可取证 | 否 |
| `evidence_resolve_failed` | 证据来源读取或回链失败 | 返回候选并标记 evidence_ready = false | 是 |
| `gap_detection_failed` | 缺口识别失败 | 保留检索结果并记录 warning | 否 |
| `conflict_detection_failed` | 冲突检测失败 | 保留检索和证据结果，回答中标记未完成冲突检查 | 否 |
| `external_verification_failed` | 外部查证失败 | 保留内部证据，标记 external_verification_unavailable | 是 |
| `mode_switch_evaluation_failed` | 模式切换评估失败 | 使用当前 agent 的保守兜底策略 | 否 |
| `response_assembly_failed` | 回答组织失败 | 返回结构化结果、证据和缺口，不生成确定自然语言结论 | 否 |
| `learning_signal_failed` | 学习信号保存失败 | 不影响回答，记录 warning | 是 |
| `no_result` | 无召回结果 | 返回证据不足 | 否 |

错误降级规则：

```text
索引失败不能导致伪造结果；
证据回链失败不能伪造 evidence_ref；
外部查证失败不能影响内部证据使用；
冲突检测失败时，回答必须标记未完成冲突检查；
模式评估失败时，agent 使用保守兜底策略；
回答组织失败时，只返回结构化结果，不输出确定自然语言结论；
学习信号保存失败不影响本次回答。
```

如果错误影响最终回答可信度，必须同时进入：

```text
warnings
TraceRecord.service_calls
SystemError 或模块错误结果
```

## 验收标准

第一版验收标准：

```text
能够按当前认知视角匹配领域和概念；
能够在领域和概念之外执行知识单元和知识点检索；
能够按目录路径过滤知识单元；
能够返回来源位置和证据摘要；
能够区分命中路线；
能够识别领域和概念激活失败但补充查找成功的情况；
能够识别证据和结论冲突，并区分局部升级和整体模式切换；
能够通过 DDG 外部查证生成低可信候选证据；
能够在回答中区分直接证据支持、推断和证据缺口；
能够把检索过程写入 trace；
不会因为补充查找成功而直接创建正式领域或概念。
```
