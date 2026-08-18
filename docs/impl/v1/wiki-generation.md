# Wiki 生成质量门（claim verify / selfcheck）

> **2026-08-18 重写说明**：本文档原描述编译链路的「阶段 A-G」全套重构方案，
> 核心是阶段 B「切面聚类」（Louvain 社区检测把 qualifying KP 分组，供阶段 C/D
> analyze/compile 按切面组织材料与正文）。`docs/impl/v1/wiki-single-tier-
> task-brief.md` 步骤 3 已把切面聚类**整体取代**为 Core/Context/Conflict 子图
> （`internal/wiki/subgraph.go`，见 `docs/impl/v1/wiki.md`「编译材料：Core/
> Context/Conflict 子图」）——`internal/wiki/aspect.go` 的 `buildAspects`/
> `BuildAspectEdges`/`ClusterAspects`/`SuggestAspectName` 在当前代码里已确认
> 无调用方（`internal/wiki/service.go` 不再引用），是本次改造遗留的孤儿代码
> （代码清理属于代码改动范围，本次任务未处理，见 `docs/impl/v1/wiki.md`
> 「已知遗留」）。
>
> 原文档阶段 A（信号归集）、阶段 C/D（analyze/compile 的具体材料分组与正文
> 分节要求）、阶段 F 的切面结构化落库（`wiki_pages.aspects`）、阶段 8（增量
> 重编译，未做）、阶段 9（主题页二阶编译同构改造）随切面聚类/两层架构一并
> 失去存在依据，本次重写整体删除，不再保留。**阶段 E（支持度校验）与阶段 G
> （发布质量门）不依赖切面聚类，是独立生效的质量机制**，在 Core/Context/
> Conflict 材料结构下原样成立（`verifyClaims`/`Selfcheck` 的实现只是把材料
> 来源从"按切面分组"换成"按 Core/Context/Conflict 分组"，校验与门槛逻辑本身
> 未变）——本文档现在只保留这两节，改名聚焦为"生成质量门"。

## 背景

编译内部是两次 LLM 调用（analyze 产出论断结构 claims/tensions 供人工确认，
compile 据此生成正文），这条结构与调用预算（固定 2 次）不受单层化改造影响，
详见 `docs/impl/v1/wiki.md`「触发与 API」。以下两道质量门在这两次调用之外
独立生效，解决同一个问题：**citation 白名单只能保证"引用的 point_id 合法"，
不能保证"这条结论确实由这些 KP 支持"、也不能保证"这一整页组织起来后真的能
答对当初这批材料被慢路径答对过的问题"**。

## 阶段 E：支持度校验（1 次批量 LLM）

```text
输入：Compile/Recompile 确认后的全部 claims，每条附其 cited_point_ids 对应
      的 KP content（取自 buildKnowledgeSubgraph 的 Core/Context/Conflict
      并集，见 internal/wiki/service.go:verifyClaims）；
Prompt：config/prompts/wiki_claim_verify.md
  「判断每条结论是否能由其所附材料支持。只判断支持关系，不改写结论，
    不补充材料之外的知识。」
输出：[{ claim_id, verdict: "supported"|"partial"|"unsupported", reason }]
调用参数：reasoning 模型，temperature 0，一次批量（claims 通常 < 20 条）。
开关：wiki.claim_verify_enabled（默认 true），关闭时整个阶段跳过。
```

处置（不改正文，只落库 + 进门槛）：

```text
supported   -> 无动作；
partial     -> 落库，页面详情展示提示，计入质量分（不阻断）；
unsupported -> 落库，阻断 publish（阶段 G），人工可选择：
               重编译、人工确认放行（force）、或把该 claim 移入待验证点后重编译。
```

表结构（migration 039，不受本次改造影响）：

```sql
CREATE TABLE wiki_claim_checks (
    check_id        TEXT PRIMARY KEY,
    page_id         TEXT NOT NULL REFERENCES wiki_pages(page_id),
    revision_id     TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    claim_id        TEXT NOT NULL,
    claim_text      TEXT NOT NULL,
    cited_point_ids TEXT NOT NULL DEFAULT '[]',
    verdict         TEXT NOT NULL,              -- supported / partial / unsupported
    reason          TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_wcc_page ON wiki_claim_checks(page_id, revision_id);
```

## 阶段 G：发布质量门（K 次 LLM，复用 answer_wiki）

### 核心思想

页面依赖的 KP 当初是被慢路径以 `retrieval_quality='confident'` 答对过的，
对应的真实问法是**带真值的评测集**。页面如果答不了这些问题，说明它没把这批
知识组织好——这是用系统自己的能力验收自己的产物，不是 LLM 自评分。

### 执行

```text
POST /wiki/pages/:id/selfcheck        （也在 publish 内联调用一次，结果复用）
  1. 取该页 source_point_ids 的真实 confident 问法，按 point_id 打散抽样
     wiki.selfcheck_replay_n（默认 5）条；
  2. 逐条调用既有 answerFromPage（config/prompts/answer_wiki.md，完全复用
     检索侧同一个直答函数）；
  3. 计算四项指标，落库 wiki_quality_checks；
  响应：{ page_id, revision_id, metrics{...}, passed, blocking_reasons[] }
```

### 指标与门槛

```text
replay_sufficient_rate   sufficient=true 的比例 >= wiki.selfcheck_min_sufficient_rate（默认 0.6）
material_usage_rate      |source_point_ids| / |Core∪Context∪Conflict 覆盖点| >=
                          wiki.selfcheck_min_material_usage（默认 0.5）
uncited_sentence_rate    「稳定结论」+「展开说明」中不含 [point_id] 的句子占比
                         <= wiki.selfcheck_max_uncited_rate（默认 0.3）
                         （「摘要」「待验证点」「依赖来源」三节不计入）
unsupported_claim_count  阶段 E 的 unsupported 计数，必须 == 0
```

### 与 publish 的关系

```text
POST /wiki/pages/:id/publish
  请求可选 { "force": true }；
  未 force 且 selfcheck 未通过 -> 409，响应带 metrics 与 blocking_reasons，
    页面保持 draft / needs_recompile；
  force=true -> 正常发布，wiki_quality_checks.forced=1（force 覆盖唯一的
    留痕方式；不写 learning_results 事件、不进学习报告——"编译/发布永远需要
    人工确认"这条本身已经蕴含"人工可以决定覆盖"，不需要再叠一层学习事件）。

selfcheck 结果按 (page_id, revision_id) 缓存：同一 revision 重复 publish 不
重跑回放。wiki.selfcheck_enabled=false 时整个阶段跳过（退回无门行为）。
```

## 配置项（config.yml: wiki 节，与本文档相关的部分）

```yaml
wiki:
  claim_verify_enabled:           true
  selfcheck_enabled:              true
  selfcheck_replay_n:             5
  selfcheck_min_sufficient_rate:  0.6
  selfcheck_min_material_usage:   0.5
  selfcheck_max_uncited_rate:     0.3
```

切面聚类专属的 `aspect_*`/`entry_cohesion_min` 等配置项、二阶编译专属的
`topic_*` 配置项仍残留在 `config.yml` 的 wiki 节但已无代码引用，见
`docs/impl/v1/wiki.md`「已知遗留」，本文档不再描述其含义。

## 完成标准

```text
支持度校验（回归项）：
  构造一条引用合法 point_id 但材料并不支持的 claim（fake LLM），
    verdict 判为 unsupported 且 publish 被 409 阻断；force=true 可发布并留痕。

质量门（回归项）：
  回放问法取自该页 KP 的真实 confident trace，非 LLM 想象；
  sufficient 率低于门槛时 publish 被阻断，metrics 可读；
  同一 revision 重复 publish 不重跑回放（缓存生效）；
  selfcheck_enabled=false 时行为退回现状。
```
