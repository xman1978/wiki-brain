# Source 主题原则 + FTS 双路召回

日期：2026-07-21

## 背景

问题「要账回款有奖励吗？」将《万相公文销售奖励制度》混入答案。上游：source 过滤因共享「回款/奖励」关键词放过不同主题文档；FTS 仅用裸问题打分，「奖励」词频抬高销售奖励类 KU，正确「回款激励/提成」章节未进 RRF top-N。

## 范围

1. **source_filter 提示词（v4）**：规则归属可答/可支撑则保留；仅词面重合则排除；吃不准才宁多勿漏。补传 audience/constraint。不写领域专名。
2. **FTS 双路 + RRF**：在现有 outline + `fts(question)` 外，增加 `fts(四元组)` 一路，三路一并 RRF 后截断 `rerank_top_n`。
3. **rerank_judge 提示词**：通用规则归属硬门——证据须与问题同一规则归属，或为该归属下可解释的前提/约束；`content_theme` 字面不同不自动否决 supporting；仅词面相近而归属不同则 irrelevant。不写领域专用例子。

## 设计

### Source 过滤

在 `config/prompts/source_filter.md` 筛选策略中增加一条原则表述：共享关键词不等于同一主题；主题不同时不得仅因关键词重叠而保留。

不新增用例，不改变「真正不确定时宁多勿漏」的整体姿态。

### FTS 双路

| 召回路 | 查询文本 | path 标签 |
|--------|----------|-----------|
| outline（已有） | `subject + intent` | `outline` |
| fts（已有） | `question` | `fts` |
| fts_tuple（新增） | `subject + intent + audience + constraint`（空字段跳过） | `fts_tuple` |

- `ftsRecall(queryText, sourceIDs, path)` 可复用；两路各自按 BM25 成独立排名列表。
- 四元组拼接结果为空时不建 `fts_tuple` 路，行为与改前一致。
- `rrfMerge` 接受多路候选列表，分别计 RRF 贡献后再按融合分截断 top-N。

## 非目标

- 不修改 `rerank_judge.md`
- 不修改 Domain 预过滤、activation 快路径
- 不把 source 过滤改为默认严苛排除
