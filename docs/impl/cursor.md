# Cursor 实现指令

本文档提供可复制到 Cursor 的分步实现指令。

每条指令只实现一个能力。

不要一次性让 Cursor 实现全部系统。

每次执行前，先让 Cursor 阅读该指令中列出的文档。

每次执行后，必须运行对应测试并对照验收标准。

## 通用前置指令

复制给 Cursor：

```text
你正在实现知识大脑第一版。请严格遵守以下文档：

- docs/impl/architecture.md
- docs/impl/implementation-order.md
- docs/impl/project-structure.md
- docs/impl/schema.md
- docs/impl/api.md
- docs/impl/runtime.md
- docs/impl/error-handling.md
- docs/impl/tech-stack.md

第一版只实现最小闭环，不实现第二版能力。

仓库 `test/*.md` 是测试用内容文件。

可以在测试中把它们作为 normalized Markdown fixture 使用，但不得让运行时代码依赖 `test/*.md`，也不得绕过 source / unit / precompile 直接用它们回答。

禁止实现：
- 自动创建正式 Domain / Concept
- 自动晋升候选 Domain / Concept
- 根据单次信号激活或降级 ActivationLink
- 网页抓取导入主链路
- IM 导入
- 完整 LifecycleService 状态流转
- 独立 DirectMemoryAgent / VerificationAgent / ConflictAgent / ReflectionLearningAgent
- 向量库作为核心依赖
- 知识图谱推理

所有跨模块结果必须使用 Result 结构：
success/status/data/error/warnings/request_id/trace_id

所有错误必须使用 SystemError 或同等字段。

实现时优先保持小步可测试，不要做未要求的抽象和重构。
```

## 指令 1：Runtime 基础

复制给 Cursor：

```text
请实现第一版 runtime 基础能力。

必须先阅读：
- docs/impl/runtime.md
- docs/impl/error-handling.md
- docs/impl/tech-stack.md
- docs/impl/project-structure.md
- docs/impl/schema.md

实现范围：
1. 建立 Go 项目基础结构，如果已有结构则按 docs/impl/project-structure.md 映射。
2. 实现 config 加载：默认读取 /Users/jxu/Code/knowledge/schema/config.yml，并支持 WIKI_CONFIG_PATH 或 --config 覆盖。
3. 实现 SQLite 初始化和 migration runner，并创建 runtime 基础表。
4. 实现统一 SystemError。
5. 实现统一 Result 和 HTTP API 响应对象。
6. 实现 request_id / trace_id 生成和传播基础工具。
7. 实现文件系统路径管理。
8. 实现 LLM client 接口和 fake LLM client，不要直接散落 OpenAI 调用。
9. 实现基础 job runner 表和 claim/retry/dead 状态处理。
10. 实现 /api/health。
11. 创建 perspectives、domains、concepts 基础表。
12. 程序启动时执行 InitSQLite / RunMigrations / EnsureDefaultPerspective / ImportPresetDomainsAndConcepts。

禁止：
- 不要实现 source / unit / precompile / retrieval 等业务模块。
- 不要调用真实外部模型做测试。
- 不要把错误只写成字符串。

测试要求：
- 默认配置路径和 WIKI_CONFIG_PATH 覆盖测试。
- SQLite migration 测试。
- 启动初始化幂等测试。
- 预制 Domain / Concept 启动导入测试。
- SystemError JSON 序列化测试。
- Result/API response 序列化测试。
- fake LLM client 测试。
- /api/health 测试。

验收：
- API 响应包含 ok/data/warnings/error/request_id/trace_id。
- JSON Schema 校验失败不能进入业务状态。
- OpenAI 模型配置来自 /Users/jxu/Code/knowledge/schema/config.yml 的 openai.models，不在业务模块硬编码。
- 启动后 default Perspective 和预制 active Domain / Concept 已写入数据库。
- trace_id 为空时不能伪造已写入 trace。
```

## 指令 2：Source 文件导入

复制给 Cursor：

```text
请实现 source 文件导入能力。

必须先阅读：
- docs/impl/source.md
- docs/impl/schema.md
- docs/impl/api.md
- docs/impl/error-handling.md
- docs/impl/project-structure.md

依赖：
- runtime 基础已完成。

实现范围：
1. 创建 source_documents 表 migration。
2. 实现 SourceDocument model 和 repository。
3. 实现文件保存路径策略：original/html/markdown。
4. 实现 ConvertClient 接口，base_url 来自 source.convert.base_url，默认 http://192.168.0.169:9000；可用 fake convert client 测试。
5. 实现 SourceImportService。
6. 实现 POST /api/sources/files。
7. 实现 GET /api/sources/{sourceId}。
8. 实现 GET /api/sources/{sourceId}/preview。
9. 实现 GET /api/sources/{sourceId}/markdown。
10. 实现 GET /api/sources。
11. 实现 DELETE /api/sources/{sourceId} 逻辑删除。
12. 实现 POST /api/sources/{sourceId}/disable、/discard、/restore。
13. 转换成功且 intake_purpose=long_term_candidate 时生成 UnitSourceResult 并投递 unit_build job。

禁止：
- source 不生成 KnowledgeUnit。
- source 不抽取 Concept。
- source 不实现网页抓取。
- source 不把 temporary_evidence 投递到 unit。

测试要求：
- ConvertClient 默认使用配置中的 http://192.168.0.169:9000，测试中可替换为 fake client。
- fake ConvertClient 可以使用 test/*.md 作为 Markdown 转换结果。
- 长期候选文件导入成功，status=ready_for_unit。
- temporary_evidence 转换成功但不进入 unit job。
- Markdown 转换失败，status=preview_ready_but_not_learnable，不进入 unit job。
- 转换服务不可用，status=failed，last_error 为 SystemError。
- 删除 source 时，关联 KnowledgeUnit / KnowledgePoint / ActivationLink / 索引 status 级联为 deleted。
- disable / discard source 时，关联对象 status 级联为 disabled / discarded。
- 重复 DELETE 幂等，不物理删除 trace。
- preview/markdown API 返回正确 content type。

验收：
- UnitSourceResult 包含 error: SystemError? 和轻量错误摘要。
- source 状态和错误一致。
- unit job 投递失败时 source 不能伪造 unit 已完成。
```

## 指令 3：Unit 知识单元构建

复制给 Cursor：

```text
请实现 unit 知识单元构建能力。

必须先阅读：
- docs/impl/unit.md
- docs/impl/schema.md
- docs/impl/error-handling.md
- docs/impl/project-structure.md

依赖：
- runtime 基础已完成。
- source 已能投递 UnitSourceResult。

实现范围：
1. 创建 unit_build_jobs、document_blocks、semantic_outlines、unit_candidates、knowledge_units、unit_source_spans 表。
2. 实现 UnitSourceResult 输入处理。
3. 实现 Markdown block 解析，保留 heading_path、start_line、end_line。
4. 实现语义目录触发判断；第一版可先规则实现，LLM 用 fake client 测试。
5. 在 outline segment 范围内实现材料知识维度分析，输出 MaterialDimension；再经 UnitBoundaryDecision 裁决 accept / split / merge / discard。
6. 实现 KnowledgeUnitDraft 到 KnowledgeUnit 的构建。
6. 实现 SourceSpan 写入。
7. 实现 UnitBuildResult。
8. 实现 UnitPrecompileResult。
9. precompile_ready=true 时投递 PrecompileJob。

禁止：
- 不读取原始文件。
- 不生成正式 Domain / Concept。
- 不把 needs_review / disabled / discarded / deleted 单元投递到 precompile。
- 不伪造 source_spans。

测试要求：
- 使用 test/*.md 覆盖 Markdown 解析和 KnowledgeUnit 构建。
- Markdown heading/paragraph/list/table block 解析测试。
- 能生成 active KnowledgeUnit 和 unit_source_spans。
- needs_review 单元不进入 PrecompileJob。
- UnitBuildResult.status=partial 时只投递 active 且可回溯单元。
- 缺少 Markdown 时返回 SystemError，不投递 precompile。

验收：
- KnowledgeUnit 可以通过 unit_id 回到 SourceDocument 和 SourceSpan。
- UnitPrecompileResult.precompile_ready 条件符合 docs/impl/unit.md。
- 投递 PrecompileJob 失败时，不标记 KnowledgeUnit 已预编译。
```

## 指令 4：Precompile 初始激活结构

复制给 Cursor：

```text
请实现 precompile 初始激活结构。

必须先阅读：
- docs/impl/precompile.md
- docs/impl/schema.md
- docs/impl/fts.md
- docs/impl/unit.md 中 UnitPrecompileResult 部分
- docs/impl/error-handling.md

依赖：
- unit 已能投递 PrecompileJob。

实现范围：
1. 创建 `knowledge_points`（Unit 阶段）、`unit_search_index`、`knowledge_point_search_index`、`outline_search_index`、`concept_match_candidates`、`precompile_events`、`precompile_jobs` 表；`activation_links` 表保留供 Study 写入。
2. 读取 runtime 已初始化的 default Perspective 和预制 active Domain / Concept。
3. Unit 阶段实现 `unit.knowledge_compile` 联合生成 KnowledgePoint。
4. 实现 MultiRouteIndexBuilder。
5. 实现 DomainCandidateBuilder / ConceptCandidateBuilder / ConceptMatcher；LLM 联合匹配使用 `precompile.domain_concept_match`。
6. Precompile 写入 `concept_match_candidates`，不调用 ActivationLink builder。
7. 实现 PrecompileJobResult。
8. 支持按 source_id 或 unit_id 增量执行。

禁止：
- 不自动创建正式 Domain / Concept。
- 不自动晋升候选 Domain / Concept。
- 不把 ActivationLink 当知识图谱关系。
- Concept 匹配失败不能删除材料侧索引。

测试要求：
- default Perspective 幂等创建。
- preset JSON 校验失败会失败。
- active KnowledgeUnit 在 Unit 阶段生成 1-5 个 KnowledgePoint。
- 写入 unit_search_index / knowledge_point_search_index / outline_search_index（SQLite）；
- 同步写入 Bleve sidecar 索引（internal/searchindex，见 docs/impl/fts.md）。
- Precompile 写入 concept_match_candidates；Study 从真实使用创建 candidate ActivationLink。
- ActivationLink 写入 scene_tags、goal_tags、pattern_tags、focus_tags、relevance_summary、boundary 预留字段；无法可靠判断 tags 时写空数组，但保留 relevance_summary 和 match_reason。
- ConceptMatcher 返回 0.55 <= confidence < 0.80 时，写入 activation_links.status=candidate、link_type=candidate。
- Concept 匹配失败仍保留材料侧索引并记录 precompile_event。

验收：
- retrieval 可以通过材料侧索引找回 KnowledgeUnit。
- retrieval 可以通过 ActivationLink 找回 KnowledgeUnit。
- retrieval 默认只把 status=active 的 ActivationLink 作为正式召回路径。
- retrieval 在 active 命中不足时，可以读取 status=candidate 的 ActivationLink 作为弱激活信号，并用多路召回验证。
- 未匹配 KnowledgePoint 不创建正式 Concept。
```

## 指令 5：Retrieval Services

复制给 Cursor：

```text
请实现 retrieval services。

必须先阅读：
- docs/impl/retrieval.md
- docs/impl/services.md
- docs/impl/schema.md
- docs/impl/fts.md
- docs/impl/error-handling.md
- prompts/retrieval/problem_framing.md
- schemas/retrieval/problem_framing.schema.json
- prompts/retrieval/cognitive_activation.md
- schemas/retrieval/cognitive_activation.schema.json
- prompts/retrieval/activation_link_match.md
- schemas/retrieval/activation_link_match.schema.json

依赖：
- precompile 已写入索引和 activation_links。

实现范围：
1. 实现 RetrievalRequest / RetrievalResult。
2. 实现 ProblemFramingService：根据 query、conversation_context、trace_context、feedback_context 生成 CognitiveRouteContext。
3. 实现 CognitiveActivationService：按 Perspective 下 Domain / Concept 匹配，并交给 ActivationLinkMatcher 选择 Top-K ActivationLink。
4. 实现 ActivationLinkMatcher：只读取 active，按 scene / goal / pattern / focus / relevance / feedback 打分；candidate 由 study 独立验证，不进入 matcher。
5. 实现 SupplementalRetrievalService：Bleve + gse 词法召回，经 SQLite 三表做资格过滤（见 docs/impl/fts.md）。
6. 实现 RetrievalFusionService：按 unit_id 合并，保留 hit_route 和 score_breakdown。
7. 实现 EvidenceTracingService：生成 EvidenceRef。
8. 实现 GapDetectionService：识别认知结构未命中但补充召回命中。
9. 实现 ConflictDetectionService 第一版规则。
10. 实现 ModeSwitchEvaluationService。
11. 实现 ResponseAssemblyService 的轻量回答。
12. 写 retrieval_events、retrieval_hits、gaps、evidence_refs、conflict_cases、mode_switch_decisions、learning_signals。

禁止：
- 不伪造 EvidenceRef。
- 不把 external_evidence_candidates 当内部强证据。
- 不因为补充召回命中创建正式 Concept。
- 不让单一路径失败取消其他召回路径。

测试要求：
- activation link 检索命中。
- ProblemFramingService 输出 CognitiveRouteContext，包含 scene / goal / pattern / focus / feedback_context。
- Concept 命中后不会无条件展开全部 ActivationLink，只展开 ActivationLinkMatcher Top-K。
- ActivationLinkMatcher 只接收 active ActivationLink；candidate 不进入 retrieval 召回链路。
- Bleve 词法检索命中（unit / knowledge_point / outline 三路）。
- outline path 过滤命中。
- disabled / discarded / deleted 的 SourceDocument、KnowledgeUnit、KnowledgePoint、ActivationLink 和索引不得被 retrieval 召回。
- 多路命中同一 unit 时合并且保留 hit_route。
- candidate ActivationLink 不参与融合、排序和 used_knowledge。
- 补充召回独立命中并采用知识后，可生成 candidate 验证信号；验证通过转 active，失败转 discarded。
- require_evidence=true 且无 EvidenceRef 时 status=insufficient_evidence。
- 认知结构未命中但多路召回命中时生成 cognitive_gap。

验收：
- RetrievalResult 使用 success/status/data/error/warnings/request_id/trace_id。
- returned results 可回到 KnowledgeUnit / SourceSpan / SourceDocument。
- retrieved 和 used 可区分。
```

## 指令 6：Router 和 Agents

复制给 Cursor：

```text
请实现 CognitiveRouter、RetrievalAgent 和 ReasoningAgent 编排框架。

必须先阅读：
- docs/impl/router.md
- docs/impl/thinking-agents.md
- docs/impl/retrieval.md
- docs/impl/error-handling.md

依赖：
- retrieval services 已完成。

实现范围：
1. 实现 RouterRequest / RouterDecision。
2. 实现规则优先的 CognitiveRouter。
3. 实现 AgentRequest / AgentResult。
4. 实现 RetrievalAgent，编排 retrieval service 并形成轻量回答。
5. 实现 ReasoningAgent 编排骨架，先接 retrieval result，后续接 working model。
6. 支持 mode_switch_request。
7. 传递 request_id / trace_id / require_evidence / require_external_evidence。

禁止：
- Agent 不直接访问数据库。
- Router 不生成回答。
- 不实现第二版独立 agent。
- 不让模型自由调用 service。

测试要求：
- 查找/出处类问题路由到 RetrievalAgent。
- 解释/比较/设计类问题路由到 ReasoningAgent。
- Router 失败时默认 ReasoningAgent。
- require_evidence 能进入 RetrievalRequest。
- service warning 能进入 AgentResult.warnings。

验收：
- AgentResult 使用 success/status/error/request_id/trace_id。
- RetrievalAgent 可形成 answer/evidence/trace_id。
- ReasoningAgent 在 working model 未实现前能返回明确 partial 或 mode_switch_requested，而不是伪造推理。
```

## 指令 7：Working Model、Reasoning、Response

复制给 Cursor：

```text
请实现 WorkingModelService、ReasoningService 和 ResponseAssemblyService。

必须先阅读：
- docs/impl/working-model.md
- docs/impl/services.md
- docs/impl/retrieval.md
- docs/impl/error-handling.md

依赖：
- RetrievalResult 已可用。
- ReasoningAgent 编排骨架已存在。

实现范围：
1. 实现 WorkingModelRequest / WorkingModelResult。
2. 从 RetrievalResult.data 构建 working_model。
3. 生成 reasoning_inputs。
4. 实现 ReasoningService，fake LLM 可测试结构化 ReasoningResult。
5. 实现 ResponseAssemblyService，输出 answer_sections、claim_refs、inference_refs、unsupported_claims。
6. 更新 ReasoningAgent 编排。

禁止：
- WorkingModelService 不输出最终结论。
- ReasoningService 不伪造 EvidenceRef。
- ResponseAssemblyService 不把 unsupported claim 写成确定事实。
- LLM schema 校验失败不得进入业务状态。

测试要求：
- evidence_refs 进入 evidence_map。
- knowledge_gaps 进入 answer_boundaries。
- conflict_cases 进入 unresolved_conflicts。
- completeness=insufficient 时回答不输出确定结论。
- claim_refs 能绑定 evidence / inference / gap。

验收：
- 复杂问题返回 working_model_summary。
- 回答区分证据支持、推断和缺口。
- trace 写入失败时不伪造 full trace。
```

## 指令 8：Trace

复制给 Cursor：

```text
请实现 TraceService。

必须先阅读：
- docs/impl/trace.md
- docs/impl/schema.md
- docs/impl/error-handling.md

依赖：
- AgentResult / RetrievalResult / WorkingModelResult 已定义。

实现范围：
1. 创建 trace_* 表。
2. 实现 TraceRecord 写入。
3. 记录 service_calls。
4. 记录 activations、retrieval_hits、used_items、evidence_refs。
5. 记录 gaps、conflicts、mode_switch_decisions、claim_refs、learning_signals。
6. 实现 GET /api/traces/{traceId}。
7. Agent 执行时写 light/full trace。

禁止：
- trace 不是工程日志。
- trace 不执行 study。
- 不把 retrieved 自动当 used。
- trace 写入失败不得伪造 trace_id。

测试要求：
- RetrievalAgent 生成 light trace。
- ReasoningAgent 生成 full trace。
- retrieved / used 分表或字段可区分。
- gap/conflict/learning_signal 可查询。
- trace 写入失败时 AgentResult 有 warning。

验收：
- trace 可作为 study 输入。
- trace 中能解释问题如何被理解、哪些知识被召回、哪些被采用。
```

## 指令 9：Study

复制给 Cursor：

```text
请实现 StudyJob 和候选学习输入保存。

必须先阅读：
- docs/impl/study.md
- docs/impl/trace.md
- docs/impl/schema.md
- docs/impl/error-handling.md

依赖：
- TraceService 已完成。

实现范围：
1. 创建 study_jobs、study_candidates、study_events、activation_link_route_stats 表。
2. TraceService 写入 learning_signals 后可投递 StudyJob。
3. 实现 StudyJob claim / retry / dead / skipped。
4. 从 trace 和 learning_signals 生成 study_candidates。
5. 从 learning_signals 的 scene / goal / pattern / focus / question_cluster_id 更新 activation_link_route_stats。
6. 实现 GET /api/study/candidates。

禁止：
- 不自动创建正式 Domain / Concept。
- 不根据单次信号激活或降级 ActivationLink。
- 不修改 KnowledgeUnit。
- 不阻塞当前回答。

测试要求：
- 没有 learning_signals 时 job skipped。
- 重复 job 不重复创建 candidate。
- candidate_domain / candidate_concept / candidate_activation_link / candidate_review_task 可生成。
- activation_link_route_stats 按 activation_link_id + question_cluster_id + scene + goal + pattern + focus 去重累计。
- candidate ActivationLink 达到阈值时可以更新为 active。
- candidate -> active 必须优先使用 activation_link_route_stats，缺少稳定 route stats 时只能写 review_required 候选或事件。
- active ActivationLink 出现弱相关、负反馈或边界失配时可以降级为 candidate / disabled / discarded。
- 同一 question_cluster_id 的多次 used 不能重复计入 ActivationLink 激活阈值。
- 缺少 question_cluster_id 时不得自动激活 candidate ActivationLink，只能写 review_required 的候选或事件。
- candidate -> active 必须写 study_event，包含 previous_status / next_status / trigger_signal_ids / question_cluster_ids / details。
- active 降级必须写 study_event，并保留降级 reason 和配置快照。
- candidate 写入失败时 job retrying 或 dead。

验收：
- study 可以写 study_* 表，并可更新 ActivationLink 状态和计数字段。
- study 不创建正式 Domain / Concept，不修改 KnowledgeUnit。
```

## 指令 10：External Evidence

复制给 Cursor：

```text
请实现第一版外部搜索证据异步辅助能力。

必须先阅读：
- docs/impl/external-evidence.md
- docs/impl/retrieval.md 的 VerificationService 部分
- docs/impl/schema.md
- docs/impl/error-handling.md

依赖：
- retrieval 和 job runner 已完成。

实现范围：
1. 创建 verification_tasks 和 external_evidence_candidates 表。
2. VerificationService 同步阶段只返回 VerificationStartResult。
3. require_external_evidence=true 时创建 VerificationTask。
4. 后台 worker 执行搜索计划、DDG 搜索、结果去重、可选网页抓取、候选证据抽取。
5. 完成后返回 VerificationCompletionResult。
6. 生成 external_evidence_candidates。
7. 可生成 study_candidate.import_external_source。

禁止：
- 不阻塞当前回答。
- 不把 snippet 当事实原文。
- 不把外部候选证据替代 EvidenceRef。
- 不自动写入 active KnowledgeUnit。

测试要求：
- require_external_evidence=true 创建 pending VerificationTask。
- DDG 不可用时 VerificationTask failed，但当前回答不失败。
- 只有 completion 后才有 external_evidence_candidates。
- snippet_only 时只生成 warning，不生成强候选证据。
- import_external_source 只进入 study_candidate。

验收：
- RetrievalResult 主链路没有 completion 时 external_evidence_candidates 为空或仅历史候选。
- 外部候选沉淀必须走 source -> unit -> precompile。
```

## 指令 11：端到端最小闭环

复制给 Cursor：

```text
请实现并验证第一版端到端最小闭环。

必须先阅读：
- docs/impl/implementation-order.md
- docs/impl/api.md
- docs/impl/architecture.md

依赖：
- runtime/source/unit/precompile/retrieval/router/agents/trace/study 已完成。

场景：
1. 使用 `test/*.md` 中任意一个 Markdown 文件作为测试输入，按上传文件流程创建 source，intakePurpose=long_term_candidate。
2. source 进入 ready_for_unit。
3. unit 生成 KnowledgeUnit 和 SourceSpan。
4. unit 投递 PrecompileJob。
5. unit 联合生成 KnowledgeUnit / KnowledgePoint，precompile 生成索引和 Domain / Concept 静态匹配候选，不生成 ActivationLink。
6. 调用 POST /api/query 查询材料中的知识。
7. retrieval 返回 EvidenceRef。
8. Agent 返回 answer 和 trace_id。
9. Trace 可查询 retrieved / used / evidence_refs。
10. 如果认知结构未命中但材料侧召回命中，生成 learning_signal 并由 StudyJob 生成 study_candidate。

禁止：
- 不通过硬编码答案通过测试。
- 不跳过 unit/precompile 直接查 Markdown。
- 不自动创建正式 Domain / Concept。

测试要求：
- 集成测试覆盖上述 10 步。
- 集成测试可以使用 test/*.md，但必须通过 source / unit / precompile 链路进入系统。
- 断言 EvidenceRef 能回到 normalized Markdown 行号。
- 断言 trace 区分 retrieved 和 used。
- 断言 study_candidate 不修改正式 Domain / Concept。

验收：
- 这个闭环证明系统不是传统 chunk RAG，而是 source -> unit -> precompile -> retrieval -> trace -> study 的第一版知识大脑最小闭环。
```
