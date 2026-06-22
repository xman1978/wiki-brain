# 第一版实现顺序

本文档是 Cursor 或工程实现者的入口文档。

实现时必须先读：

```text
docs/impl/architecture.md
docs/impl/runtime.md
docs/impl/error-handling.md
docs/impl/tech-stack.md
```

第一版只实现最小业务闭环。

不要实现 `docs/impl/architecture.md` 中标为第二版的能力。

## 总体顺序

第一版按以下顺序实现：

```text
1. runtime / config / storage / errors / logging
2. source
3. unit
4. precompile
5. retrieval services
6. router + thinking agents
7. working model + reasoning + response assembly
8. trace
9. study
10. external evidence async task
```

每一步必须能独立测试。

仓库 `test/*.md` 是测试用内容文件。

它们可以作为 normalized Markdown fixture 用于 source / unit / precompile / retrieval / 端到端集成测试。

测试必须仍然经过对应模块边界：

```text
source 创建 SourceDocument；
unit 从 normalized_markdown_path 读取；
precompile 消费 KnowledgeUnit；
retrieval 查询索引和 ActivationLink；
agent 返回 answer 和 trace。
```

不得让 runtime、retrieval 或 agent 直接读取 `test/*.md` 绕过前置链路。

如果某一步未完成，不要通过假写下游状态来伪造闭环。

## 1. Runtime 基础

目标：

```text
默认加载 /Users/jxu/Code/knowledge/schema/config.yml，并支持 WIKI_CONFIG_PATH 或 --config 覆盖；
初始化 SQLite；
执行 migration；
创建 perspectives / domains / concepts 基础表；
初始化 default Perspective；
导入预制 Domain / Concept；
初始化文件系统目录；
生成 request_id / trace_id；
提供统一 Result 和 API 响应；
提供 SystemError；
提供 fake LLM client；
提供后台 job runner 基础能力。
```

必须参考：

```text
docs/impl/runtime.md
docs/impl/error-handling.md
docs/impl/tech-stack.md
```

验收：

```text
配置加载测试通过；
SQLite 测试库可迁移；
启动初始化幂等；
启动后 default Perspective 和预制 active Domain / Concept 已写入数据库；
OpenAI 模型配置来自 config.yml 的 openai.models；
文件目录可创建；
SystemError 可序列化；
API 响应包含 ok/data/warnings/error/request_id/trace_id；
fake LLM client 可替代真实 OpenAI 调用。
```

## 2. Source

目标：

```text
上传文件；
保存原始文件；
调用配置的转换服务生成 HTML / Markdown，默认 base_url = http://192.168.0.169:9000；
保存 preview HTML 和 normalized Markdown；
写 source_documents；
支持 DELETE /api/sources/{sourceId} 逻辑删除；
支持 disable / discard / restore；
生成 UnitSourceResult；
ready_for_unit=true 时投递 unit 构建。
```

必须参考：

```text
docs/impl/source.md
docs/impl/schema.md
docs/impl/api.md
```

禁止：

```text
source 不生成 KnowledgeUnit；
source 不抽取 Concept；
source 不判断长期知识有效性；
source 不实现网页抓取、搜索引擎导入、IM 导入。
```

验收：

```text
长期候选文件转换成功后 status=ready_for_unit；
fake ConvertClient 可以使用 test/*.md 作为 Markdown 转换结果；
temporary_evidence 不进入 unit 队列；
Markdown 转换失败时不进入 unit 队列；
转换服务不可用时 source 状态为 failed；
ConvertClient base_url 来自 source.convert.base_url；
删除 source 会级联将关联 KnowledgeUnit / KnowledgePoint / ActivationLink / 索引标记为 deleted；
disable / discard source 会级联标记关联对象为 disabled / discarded；
重复删除幂等，不删除历史 trace；
失败结果包含 SystemError 或同等字段。
```

## 3. Unit

目标：

```text
消费 UnitSourceResult；
解析 normalized Markdown；
生成 document_blocks；
必要时生成 semantic_outlines；
生成 unit_candidates；
写 active KnowledgeUnit；
写 unit_source_spans；
返回 UnitBuildResult；
生成 UnitPrecompileResult；
precompile_ready=true 时投递 PrecompileJob。
```

必须参考：

```text
docs/impl/unit.md
docs/impl/schema.md
```

禁止：

```text
unit 不读取原始文件；
unit 不生成正式 Domain / Concept；
unit 不把 needs_review 单元投递到正式 precompile；
unit 不伪造 source_spans。
```

验收：

```text
KnowledgeUnit 能回到 SourceSpan；
test/*.md 可以驱动 unit 构建测试，并生成可回链 SourceSpan；
status=needs_review 的候选不进入 PrecompileJob；
UnitBuildResult.status=partial 时只投递 active 且可回溯的 KnowledgeUnit；
投递 PrecompileJob 失败时不得标记 precompiled。
```

## 4. Precompile

目标：

```text
消费 PrecompileJob；
读取 runtime 已初始化的 default Perspective；
读取预制 active Domain / Concept；
从 active KnowledgeUnit 生成 KnowledgePoint；
写 unit_search_index / knowledge_point_search_index / outline_search_index（SQLite）；
同步写 Bleve sidecar 索引（见 docs/impl/fts.md）；
匹配已有 active Domain / Concept；
写带 scene / goal / pattern / focus 预留字段和 relevance_summary / boundary 的 ActivationLink；
写 precompile_events；
返回 PrecompileJobResult。
```

必须参考：

```text
docs/impl/precompile.md
docs/impl/schema.md
docs/impl/fts.md
```

禁止：

```text
不得因一次导入自动创建正式 Domain / Concept；
不得自动晋升候选 Domain / Concept；
不得把 ActivationLink 当作开放知识图谱关系；
不得在 Concept 匹配失败时删除材料侧索引。
```

验收：

```text
active KnowledgeUnit 可被材料侧索引召回；
KnowledgePoint 可回到 KnowledgeUnit 和 SourceSpan；
匹配已有 Concept 时建立带使用条件字段的 ActivationLink；
未匹配知识点记录 precompile_event；
Concept 匹配失败时 Retrieval 仍可通过多路召回找到知识。
```

## 5. Retrieval Services

目标：

```text
接收 RetrievalRequest；
执行 ProblemFramingService，生成 CognitiveRouteContext；
执行认知结构激活；
通过 ActivationLinkMatcher 选择 Top-K ActivationLink；
执行 unit / knowledge point / outline 多路召回（词法层经 Bleve + gse，见 docs/impl/fts.md）；
融合排序并保留 hit_route；
回链 EvidenceRef；
识别 gap / conflict；
按 require_evidence 约束回答；
返回 RetrievalResult。
```

必须参考：

```text
docs/impl/retrieval.md
docs/impl/services.md
docs/impl/fts.md
prompts/retrieval/problem_framing.md
schemas/retrieval/problem_framing.schema.json
prompts/retrieval/cognitive_activation.md
schemas/retrieval/cognitive_activation.schema.json
prompts/retrieval/activation_link_match.md
schemas/retrieval/activation_link_match.schema.json
```

禁止：

```text
不得无证据输出确定回答；
不得把外部候选证据当内部强 EvidenceRef；
不得因为补充查找命中就创建正式 Concept；
不得伪造 evidence_ref；
不得让单一路径失败取消其他召回路径。
```

验收：

```text
能通过 ActivationLink 找回 KnowledgeUnit；
Concept 命中后不会无条件展开全部 ActivationLink；
只有 active ActivationLink 进入 ActivationLinkMatcher；
candidate ActivationLink 不进入 retrieval、融合或 used_knowledge，只由 study 使用独立补充召回与 EvidenceRef 验证；
能通过 unit_search_index 找回 KnowledgeUnit；
disabled / discarded / deleted 对象不会被召回、used 或作为 supported_claim 证据；
能区分 retrieved 和 used；
能识别认知结构未命中但多路召回命中的 gap；
require_evidence=true 且无 EvidenceRef 时返回 insufficient_evidence。
```

## 6. Router 和 Agent

目标：

```text
CognitiveRouter 生成 RouterDecision；
RouterDecision 映射为 AgentRequest；
RetrievalAgent 编排轻量检索回答；
ReasoningAgent 编排检索、working model、reasoning、response assembly；
AgentResult 使用 success/status/error/warnings/request_id/trace_id。
```

必须参考：

```text
docs/impl/router.md
docs/impl/thinking-agents.md
```

禁止：

```text
Agent 不直接访问数据库；
Agent 不把 service 暴露给模型自由调用；
Router 不直接生成回答；
第一版不单独实现 DirectMemoryAgent / VerificationAgent / ConflictAgent / ReflectionLearningAgent。
```

验收：

```text
简单查找路由到 RetrievalAgent；
设计、比较、判断类问题路由到 ReasoningAgent；
require_evidence / require_external_evidence 能从 Router 传到 Agent 和 RetrievalRequest；
service 失败时 AgentResult 能表达 partial 或 failed。
```

## 7. Working Model / Reasoning / Response

目标：

```text
WorkingModelService 消费 RetrievalResult.data；
构建 WorkingModelResult；
ReasoningService 基于 reasoning_inputs 输出推理结果；
ResponseAssemblyService 绑定 claim / evidence / inference / gap；
最终回答区分证据支持、推断和缺口。
```

必须参考：

```text
docs/impl/working-model.md
docs/impl/retrieval.md
docs/impl/services.md
```

禁止：

```text
WorkingModelService 不生成最终结论；
ReasoningService 不伪造 EvidenceRef；
ResponseAssemblyService 不把 unsupported claim 写成确定事实；
模型输出 JSON Schema 校验失败时不得进入业务状态。
```

验收：

```text
复杂问题能生成 working_model_summary；
缺少证据时 completeness=insufficient；
冲突未解决时回答保留不确定性；
claim_refs 能回到 evidence / inference / gap。
```

## 8. Trace

目标：

```text
记录 trace_records；
记录 service_calls；
记录 cognitive_route_context；
记录 activations / retrieval_hits / used_items / evidence_refs；
记录 gaps / conflicts / mode_switch / claim_refs / learning_signals；
为 study 提供输入。
```

必须参考：

```text
docs/impl/trace.md
```

禁止：

```text
trace 不是工程日志；
trace 不自动执行 study；
trace 不把 retrieved 当 used；
trace 写入失败不得伪造 trace_id。
```

验收：

```text
RetrievalAgent 产生 light trace；
ReasoningAgent 产生 full trace；
retrieved 和 used 可查询；
gap / conflict / learning_signal 可供 study 消费。
```

## 9. Study

目标：

```text
消费 trace 和 learning_signals；
以 StudyJob 后台运行；
生成 study_candidates；
写 activation_link_route_stats；
写 study_events；
不创建正式 Domain / Concept；
可以基于阈值更新已有 ActivationLink 状态、全局计数字段和适用条件候选。
```

必须参考：

```text
docs/impl/study.md
```

禁止：

```text
不得自动创建正式 Domain / Concept；
不得根据单次信号激活或降级 ActivationLink；
不得根据单次反馈修改 KnowledgeUnit；
不得阻塞当前回答。
```

验收：

```text
无 learning_signals 时 job skipped；
重复 job 幂等；
候选写入失败时 job retrying 或 dead；
study_candidate 可表达 candidate_domain / candidate_concept / candidate_activation_link / candidate_review_task。
learning_signal 中的 scene / goal / pattern / focus 会写入 activation_link_route_stats；
candidate ActivationLink 达到阈值时可以更新为 active；
candidate -> active 必须优先基于 activation_link_route_stats 的去重统计，不能只看全局计数；
active ActivationLink 出现弱相关、负反馈或边界失配时可以降级。
同一 question_cluster_id 的多次 used 不能重复计入 ActivationLink 激活阈值；
缺少 question_cluster_id 时不得自动激活 candidate ActivationLink；
ActivationLink 状态变化必须写 study_event，包含 previous_status / next_status / trigger_signal_ids / question_cluster_ids / details。
```

## 10. External Evidence

目标：

```text
同步阶段只创建 VerificationStartResult；
异步 worker 生成 VerificationCompletionResult；
候选证据写入 external_evidence_candidates；
必要时生成 import_external_source 学习建议。
```

必须参考：

```text
docs/impl/external-evidence.md
```

禁止：

```text
不得阻塞当前回答；
不得把 snippet 当事实原文；
不得自动写入 active KnowledgeUnit；
不得替代内部 EvidenceRef；
```

验收：

```text
require_external_evidence=true 时创建 VerificationTask；
DDG 不可用时当前回答仍可基于内部证据继续；
只有 VerificationCompletionResult 完成后才产生 external_evidence_candidates；
外部候选沉淀只生成 import_external_source 建议。
```
