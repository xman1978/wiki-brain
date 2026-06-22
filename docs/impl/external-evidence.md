# 外部搜索证据实现方案

本文档描述第一版外部搜索证据的工程实现。

外部搜索证据不属于核心检索主链路。

它是 `VerificationService` 的异步辅助能力，用于在内部证据不足、需要时效性确认、需要寻找反例或用户明确要求外部查证时，生成低可信外部候选证据。

外部搜索结果不能替代内部 `EvidenceRef`，也不能自动进入长期知识。

如果外部来源需要成为正式知识，必须后续走 source 导入、unit 切分、知识点生成和证据回链流程。

## 设计边界

外部搜索证据的定位：

```text
辅助当前回答；
暴露外部候选依据；
发现可能冲突；
提示知识可能过期；
为后续 source 导入提供候选来源。
```

外部搜索证据不做：

```text
不阻塞当前回答；
不提升内部 EvidenceRef 的证据强度；
不自动修改 KnowledgeUnit / KnowledgePoint / Concept；
不自动更新长期记忆；
不把搜索摘要当作事实原文；
不把网页候选直接当作确定结论。
```

第一版外部搜索证据默认异步执行。

当前回答仍然基于内部检索结果、内部证据、缺口和推理路径生成。

异步外部搜索完成后，只作为回答后的辅助证据、trace 记录或后续查证提示展示。

## 触发条件

Router 或 retrieval service 在以下情况可以创建外部查证任务：

```text
用户要求当前、最新、精确外部来源；
问题涉及时效性；
问题风险较高；
内部证据不足；
已有内部材料可能过期；
检索结果存在冲突但内部证据不足以裁决；
ReasoningAgent 的 working model 识别出需要外部证据的缺口。
```

满足条件时，设置：

```text
require_external_evidence = true
```

但 `require_external_evidence = true` 不表示当前回答必须等待外部搜索完成。

它只表示应创建或调度外部查证任务。

## 同步阶段

同步阶段只做轻量工作：

```text
判断是否需要外部查证；
从 query / claims / gaps / conflicts 中生成 VerificationTask；
记录 trace 事件；
在当前回答中标记外部查证状态。
```

同步阶段不等待：

```text
DDG 搜索；
网页抓取；
正文抽取；
广告和噪音清洗；
模型证据抽取；
交叉来源排序。
```

同步阶段输出 `VerificationStartResult`，只表示外部查证任务是否创建或跳过，不表示已经完成搜索和证据抽取：

```text
VerificationStartResult {
  success
  status
  verification_task
  error
  warnings
  request_id
  trace_id
}
```

`status` 可取：

```text
pending
skipped
failed
```

`verification_task` 结构：

```text
VerificationTask {
  id
  retrieval_event_id
  query
  claims_to_verify
  gaps
  conflicts
  status
  created_at
}
```

同步阶段不得返回 `external_evidence_candidates`、`verified_claims` 或 `unverified_claims` 作为完成结果。

如果同步阶段创建任务失败，应返回 `success = false` 和 `error = SystemError`。

如果不需要外部查证，应返回：

```text
success = true
status = skipped
verification_task = null
```

## 异步处理流程

后台 worker 执行完整外部查证流程：

```text
VerificationTask
  -> SearchQueryPlanning(model)
  -> DDG search
  -> SearchResultCollection
  -> URL normalize / dedupe
  -> source filtering
  -> preliminary ranking
  -> page fetch
  -> content extraction
  -> noise removal
  -> EvidenceExtraction(model)
  -> claim alignment
  -> final ranking
  -> persist ExternalEvidenceCandidate
```

其中模型主要用于：

```text
从问题、claim、缺口和冲突中提取搜索关键词；
将复杂问题拆成多个可搜索的事实子问题；
从清洗后的网页正文中提取与 claim 对齐的证据；
判断证据是支持、反对、限制条件还是背景信息。
```

规则逻辑主要用于：

```text
URL 规范化；
重复结果折叠；
来源类型判断；
域名和路径过滤；
网页正文抽取；
广告、导航、页脚和推荐内容清洗；
时效性初筛；
排序打分；
失败重试、超时和限流。
```

## 搜索关键词规划

外部搜索不能直接把用户原始问题丢给 DDG。

系统应先从以下输入生成结构化搜索计划：

```text
query
claims_to_verify
knowledge_gaps
conflicts
available_internal_evidence
source_filters
```

搜索计划结构：

```text
SearchQueryPlan {
  id
  verification_task_id
  claim_id
  purpose
  query
  language
  expected_source_types
  required_terms
  optional_terms
  negative_terms
  freshness_required
  max_results
}
```

`purpose` 可取：

```text
claim_verify
counter_evidence
freshness_check
source_lookup
definition_check
```

关键词生成规则：

```text
每个待查证 claim 最多生成 2 到 4 条 query；
优先保留实体、谓词、指标、版本、时间和地点；
去掉泛化意图词，例如“如何看待”“是否合理”“为什么”；
技术、论文、开源、国际标准优先英文；
中文本土事实、政策、机构和事件优先中文；
需要原始来源时加入 official、documentation、announcement、report、standard 等限定词；
需要反证时加入 limitation、criticism、controversy、failure、risk 等限定词。
```

示例：

```text
用户问题：
某个开源项目的新版本是否已经支持某能力？

claim_verify:
project name capability version release notes

source_lookup:
project name official documentation capability

freshness_check:
project name changelog latest capability

counter_evidence:
project name capability issue limitation
```

## 搜索结果收集

第一版搜索 provider：

```text
search_provider = ddg
```

DDG 搜索结果先进入 `SearchResultCandidate`：

```text
SearchResultCandidate {
  id
  verification_task_id
  search_query_plan_id
  title
  url
  snippet
  source_name
  search_provider
  search_query
  provider_rank
  retrieved_at
  warnings
}
```

DDG 返回的 `snippet` 只能用于候选筛选和展示。

它不能作为事实原文，也不能单独生成已验证结论。

## 去重和来源折叠

搜索结果进入网页抓取前必须先做规范化和去重。

规范化字段：

```text
canonical_url
domain
registrable_domain
normalized_path
title_hash
snippet_hash
```

去重规则：

```text
完全相同 canonical_url 去重；
移除常见 tracking 参数后 URL 相同则合并；
同一 domain 下标题高度相似则折叠；
聚合页、转载页和镜像页优先让位于原始来源；
同一来源最多保留少量高分结果，避免单一站点占满候选集。
```

常见应移除的 URL 参数：

```text
utm_source
utm_medium
utm_campaign
utm_term
utm_content
fbclid
gclid
ref
spm
```

## 来源类型和初排

来源类型：

```text
official
primary
reputable_secondary
community
aggregator
unknown
```

含义：

```text
official：官网、政府、标准组织、项目官方文档、公司公告；
primary：论文、规范、release note、财报、原始数据；
reputable_secondary：研究报告、专业媒体、百科、技术博客；
community：论坛、问答、GitHub issue、社交媒体；
aggregator：搜索聚合页、转载站、内容农场；
unknown：无法判断。
```

初排应考虑：

```text
provider_rank；
query relevance；
source_type；
freshness；
是否可能为原始来源；
是否重复；
是否疑似广告或内容农场。
```

但来源类型不能机械决定价值。

例如故障、限制、兼容性、实践经验类问题中，GitHub issue、论坛和问答可能是重要反例候选。

## 网页抓取和正文抽取

只有成功抓取并抽取网页正文的结果，才可以进入模型证据抽取。

网页抓取应设置：

```text
超时；
最大页面大小；
重试次数；
允许的 content-type；
robots 和站点访问策略；
失败 warning。
```

正文抽取目标：

```text
保留标题、发布时间、更新时间、正文段落、表格文本和代码块必要上下文；
去除广告、导航、页脚、相关推荐、评论区、订阅弹窗、cookie banner；
保留原始 URL 和抓取时间；
尽量保留段落位置，便于后续引用。
```

抽取后的结构：

```text
FetchedPageContent {
  search_result_candidate_id
  final_url
  title
  source_name
  published_at
  updated_at
  fetched_at
  text_blocks
  extraction_quality
  warnings
}
```

`text_blocks` 应包含：

```text
block_id
text
block_type
position
```

`extraction_quality` 可取：

```text
high
medium
low
failed
```

如果正文抽取失败，应保留 `SearchResultCandidate`，但不能生成强候选证据。

## 模型证据抽取

模型输入不应是完整网页原文。

应先通过规则和轻量相关性筛选，把候选 block 限制在可控范围内。

模型任务：

```text
判断 block 是否与 claim 相关；
抽取最小可用证据片段；
判断证据支持、反对、限定、背景或无关；
生成简短 paraphrase；
标记不确定性和缺失条件。
```

证据关系：

```text
supports
contradicts
qualifies
background
unrelated
```

模型输出：

```text
ExtractedExternalEvidence {
  id
  verification_task_id
  search_result_candidate_id
  claim_id
  relation
  quoted_text
  paraphrase
  block_ids
  source_url
  source_title
  source_name
  published_at
  fetched_at
  extraction_confidence
  warnings
}
```

`quoted_text` 应保持短片段，只用于定位和复查。

不能要求模型基于搜索摘要补全证据。

如果模型只能基于摘要判断，应降级为 `SearchResultCandidate`，并标记：

```text
snippet_only
```

## 候选证据排序

外部候选证据排序应拆分多个维度：

```text
relevance_score
source_credibility_score
evidence_strength_score
freshness_score
independence_score
extraction_quality_score
duplication_penalty
noise_penalty
```

综合分：

```text
external_evidence_score =
  relevance_score
  + source_credibility_score
  + evidence_strength_score
  + freshness_score
  + independence_score
  + extraction_quality_score
  - duplication_penalty
  - noise_penalty
```

第一版规则：

```text
credibility 默认 low；
confidence 默认不高于 0.4；
只有官方或一手来源、正文抽取质量高、证据与 claim 明确对齐时，才可以接近上限；
多来源一致可以提高展示排序，但不能自动转成内部强证据；
反对证据和限制条件不能因为不支持当前回答而被降权隐藏。
```

## 持久化对象

后台 worker 完成后输出 `VerificationCompletionResult`：

```text
VerificationCompletionResult {
  success
  status
  verification_task_id
  external_evidence_candidates
  verified_claims
  unverified_claims
  conflicts_found
  error
  warnings
  request_id
  trace_id
}
```

`status` 可取：

```text
completed
partial
failed
cancelled
```

该结果由异步 worker 写入数据库、trace 或后续展示通道。

它不自动改写当前回答。

最终对外展示和 trace 使用 `ExternalEvidenceCandidate`：

```text
ExternalEvidenceCandidate {
  id
  retrieval_event_id
  verification_task_id
  claim_id
  title
  url
  snippet
  source_name
  source_type
  relation
  quoted_text
  paraphrase
  retrieved_at
  fetched_at
  search_provider
  search_query
  credibility
  confidence
  warnings
  created_at
}
```

其中：

```text
snippet 来自搜索结果；
quoted_text 来自网页正文抽取；
paraphrase 来自模型证据抽取；
relation 表示与 claim 的关系；
confidence 表示候选证据可靠程度，不表示内部知识置信度。
```

如果没有成功抓取正文：

```text
quoted_text = null
relation = background 或 unknown
warnings 包含 snippet_only 或 page_fetch_failed
```

## 回答展示

外部搜索证据完成后，可以追加展示在回答后。

建议展示分组：

```text
外部辅助证据
支持候选
反对候选
限制条件
相关但未直接验证
查证失败或无法访问
```

展示文案应避免：

```text
已证实；
证明；
确定；
权威依据。
```

推荐使用：

```text
外部候选证据；
外部查证线索；
辅助参考；
待导入来源；
可能的反对证据。
```

## 与当前回答的关系

外部异步结果不应自动改变当前回答。

规则：

```text
不自动提升 claim confidence；
不自动降低原回答置信度；
不自动改写 answer；
不自动写入 KnowledgeUnit；
不自动替代 EvidenceRef；
只生成辅助展示、trace、warning 或后续建议。
```

如果外部候选证据发现强冲突，应生成：

```text
external_conflict_found
```

该事件可以触发后续冲突检测或查证流程，但不能静默修改当前回答。

## 错误处理

外部搜索失败不能影响内部检索主链路。

错误应记录 warning：

```text
search_provider_unavailable
search_timeout
page_fetch_failed
content_extraction_failed
evidence_extraction_failed
source_type_unknown
snippet_only
external_conflict_found
```

如果 DDG 不可用：

```text
VerificationTask.status = failed
相关 claim 标记为 unverified
当前回答继续使用内部证据和缺口说明
```

## 后续沉淀

当外部候选证据被用户确认有价值，或在多个任务中反复出现时，可以生成学习建议：

```text
LearningSuggestion {
  suggestion_type = import_external_source
  source_url
  reason
  related_claims
  related_gaps
}
```

该建议只表示“值得导入为材料候选”，不表示外部证据已经成为长期知识。

后续沉淀必须走完整知识构建链路：

```text
ExternalEvidenceCandidate
  -> LearningSuggestion(import_external_source)
  -> SourceDocument(intake_purpose = long_term_candidate)
  -> normalized Markdown / preview HTML
  -> UnitBuildResult
  -> PrecompileJob
  -> KnowledgePoint / retrieval indexes / ActivationLink to existing Concept
```

只有经过 source 导入、unit 切分、证据回链和 precompile 后，外部材料才可能成为：

```text
SourceDocument
NormalizedMarkdown
KnowledgeUnit
KnowledgePoint
EvidenceRef
可检索索引
```

这保证外部网页不会绕过知识生命周期和证据回链机制。

外部搜索摘要、snippet 或模型 paraphrase 不得直接写入 active KnowledgeUnit。

如果只能获得搜索摘要，最多生成：

```text
study_candidate.import_external_source
warning = snippet_only
```
