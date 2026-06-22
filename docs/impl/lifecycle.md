# Lifecycle 实现方案

本文档描述知识大脑第一版中 `lifecycle` 的工程边界。

记忆生命周期是长期记忆有效性机制，用于区分当前可用知识、候选理解、需要验证、已过期、被替代和仅用于历史解释的知识。

## 第一版范围

第一版不实现完整 `LifecycleService`。

第一版只做：

```text
在相关模块中保留 lifecycle 所需基础字段；
不删除历史知识；
不自动把旧知识标记为过期；
不自动把新知识替代旧知识；
不基于单次反馈修改生命周期状态；
不在检索阶段执行完整生命周期过滤策略。
```

第一版必须避免把 lifecycle 逻辑分散实现成隐式规则。

如果模块需要表达材料或知识状态，只能使用本模块已有状态，并明确这些状态不等同于完整生命周期状态。

## 后续职责

完整 `LifecycleService` 后续负责：

```text
判断知识是否仍然当前有效；
标记知识需要验证；
标记知识已过期；
记录知识被新知识替代；
保留历史解释所需的旧知识；
决定哪些知识默认进入当前回答；
决定哪些知识只能用于历史 trace 解释；
在材料更新、用户反馈或外部证据变化后触发重新验证。
```

## 预留状态

后续完整生命周期状态可以包括：

```text
active
candidate
needs_validation
expired
superseded
historical
discarded
```

含义：

| 状态 | 含义 |
| --- | --- |
| `active` | 当前可作为回答依据 |
| `candidate` | 候选理解，尚未稳定 |
| `needs_validation` | 需要重新验证后才能作为强依据 |
| `expired` | 已过期，不应默认进入当前回答 |
| `superseded` | 已被新知识替代 |
| `historical` | 仅用于解释历史 trace 或知识演化 |
| `discarded` | 不作为知识使用 |

第一版不创建这些完整状态表，也不执行状态流转。

## 第一版模块边界

`source` 只记录材料来源、可信度、产生时间、导入目的和转换状态。

`unit` 只记录知识单元构建状态，例如 `draft`、`active`、`needs_review`、`discarded`。

`precompile` 只生成初始激活结构和候选认知线索，不判断长期有效性。

`retrieval` 第一版可以读取模块已有状态和 warning，但不实现完整生命周期推理。

`trace` 可以记录“已有知识可能过期”“需要验证”等信号，供后续 lifecycle 使用。

`study` 可以保存 lifecycle 相关候选 review task，但不直接修改生命周期状态。

## 预留数据

第一版相关模块应尽量保留以下信息，供后续 lifecycle 判断：

```text
source_document_id
source_version
source_uri
produced_at
retrieved_at
trust_level
unit_id
knowledge_point_id
status
confidence
warnings
trace_id
feedback
created_at
updated_at
```

这些字段只是 lifecycle 输入，不代表完整生命周期实现。

## 和当前回答的关系

第一版当前回答仍优先使用：

```text
状态可用的 KnowledgeUnit；
可回到来源的 EvidenceRef；
没有明显 warning 的知识；
trace 中被实际 used 且没有冲突的知识。
```

如果 retrieval、reasoning 或 verification 发现知识可能过期、来源不可靠或存在新旧冲突，应：

```text
降低回答确定性；
在 answer / warnings 中表达不确定性；
在 trace 中记录 knowledge_gap、cognitive_gap 或 conflict；
必要时生成 learning_signal 或 study_candidate。
```

第一版不因为这些信号直接修改长期记忆状态。

## 和 study / review 的关系

第一版 lifecycle 只提供状态信号和判断输入，不执行完整状态流转。

当 retrieval、reasoning、verification、trace 或 study 发现以下情况时：

```text
知识可能过期；
来源不可靠；
外部证据与内部知识冲突；
材料被新来源替代；
实践路径被反馈为失败；
```

只能生成：

```text
warning；
trace gap / conflict；
learning_signal；
study_candidate(candidate_review_task)；
```

第一版不得自动执行：

```text
active -> expired；
active -> superseded；
candidate -> active；
needs_validation -> active；
删除 KnowledgeUnit；
替换 EvidenceRef；
```

这些状态变化必须等待第二版 `LifecycleService` 或人工 review 流程确认。

## 验收标准

第一版应满足：

```text
有独立 lifecycle 实现文档说明第一版不实现完整生命周期；
source、unit、trace、study 的边界不被误解为完整 lifecycle；
不会自动过期、替代或删除长期知识；
不会把单次反馈直接写成生命周期状态变化；
能够保留后续 lifecycle 判断所需的基础字段；
发现过期、替代或需验证风险时，至少通过 warning、trace 或 study candidate 记录。
```
