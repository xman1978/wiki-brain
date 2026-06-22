# Unit 实现方案

本文档用于描述知识大脑第一版中 `unit` 模块的实现方案。

`unit` 负责从 source 产出的 `normalized.md` 中生成知识单元，并记录知识单元与来源材料之间的可追溯关系。

## 第一版定位

第一版在知识构建阶段遵循**模型优先、规则兜底**：语义目录、节点摘要、材料维度分析与候选精炼均先调用大模型；仅在模型不可用、调用失败或输出不可用时，回退到规则与启发式路径。在线检索则相反：**BM25 优先、模型兜底**（未命中或分数低于置信阈值时再调用模型），见 `docs/impl/architecture.md`。

第一版重点定义：

```text
source 到 unit 的输入契约；
Markdown 切块规则；
知识单元数据结构；
知识单元位置记录；
证据引用关系；
失败结果和错误处理；
unit 构建队列和状态。
```

## 语义目录生成前提

语义目录属于 `unit` 阶段。

它不是 source 阶段恢复出来的原文目录，而是系统为了形成知识单元候选，对弱结构或过粗结构内容生成的派生结构。

因此，语义目录不是默认生成步骤。

第一版原则是：

```text
优先使用原文 Markdown 标题结构；
只有当原文结构不足以支持稳定切分知识单元时，才生成语义目录。
```

### 不同目录结构的边界

`unit` 阶段需要区分几类结构：

```text
source outline
  原文 Markdown 中明确存在的标题结构；

recovered outline
  source 阶段从版面、字体、编号或 OCR 结果中恢复出的原文结构；

semantic outline
  unit 阶段基于内容理解生成的语义目录；

manual outline
  人工整理或修正后的目录结构。
```

`source outline` 和 `recovered outline` 是材料结构。

`semantic outline` 是知识构建辅助结构。

知识单元可以记录自己所在的目录路径，但知识单元的身份不能依赖目录路径。

### 目录节点说明

目录结构不仅用于展示路径，也用于后续检索。

第一版应把目录视为可检索的结构节点，而不是只保存标题字符串。

每个可进入检索链路的目录节点建议具备：

```text
outline_type
path
title
summary
source_span
children
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `outline_type` | source、recovered、semantic 或 manual |
| `path` | 从根到当前节点的目录路径 |
| `title` | 当前目录节点标题 |
| `summary` | 该目录节点覆盖范围的内容说明，用于检索阶段判断范围相关性 |
| `source_span` | 节点覆盖的原文范围 |
| `children` | 子目录节点，可为空 |

`summary` 的作用不是替代知识单元正文，而是让 retrieval 可以先基于目录树缩小范围，再读取命中的知识单元和来源证据。

这与 PageIndex 类似：检索先查看带摘要的 tree structure，判断相关节点，再按页码或行号读取正文。第一版采用文档级树搜索语义：先选择候选 SourceDocument，再在该文档的目录树内定位相关节点；可以不实现完整 agentic tool-calling 流程，但必须保证目录节点说明能进入 `outline_search_index.outline_summary`。

不同目录类型的说明来源：

| 目录类型 | summary 来源 |
| --- | --- |
| `semantic` | 由语义目录模型输出的 `summary` |
| `source` | 对该 heading 覆盖范围生成目录说明；短内容可用标题 + 内容摘录，长内容应使用模型摘要 |
| `recovered` | 优先使用 source 阶段恢复出的标题范围摘要；缺失时按 source outline 规则生成 |
| `manual` | 优先使用人工提供的说明；缺失时按覆盖内容生成 |

退化策略：

```text
如果无法生成目录说明，可以临时使用 path + title + unit.center；
但该退化值必须被视为低质量 summary；
不能把缺失 summary 的情况视为已经满足目录检索质量要求。
```

### 前置分析

在生成语义目录之前，`unit` 应先执行文档结构分析。

该分析不直接生成知识单元，只判断哪些范围需要语义目录辅助。

建议输出：

```text
SemanticOutlineNeed {
  required
  scope
  targetSpans
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `required` | 是否需要生成语义目录 |
| `scope` | 生成范围，可为 `document`、`section` 或 `none` |
| `targetSpans` | 需要生成语义目录的原文范围 |

`targetSpans` 建议包含：

```text
source_heading_path
start_line
end_line
block_ids
reason
severity
```

### 触发条件

满足以下情况之一时，可以生成语义目录。

#### 文档没有有效标题结构

例如：

```text
没有 Markdown heading；
只有一个文档标题，后续全是正文；
标题只表示文档名，不构成内容层级。
```

第一版可使用以下启发式条件：

```text
heading_count <= 1
且 token_estimate > 1500
```

#### 标题章节过长

文档存在标题结构，但某个章节内部过长，原文标题不足以支持知识单元切分。

第一版可使用以下启发式条件：

```text
section_token_estimate > 2500
或 section_block_count > 25
或 paragraph_count > 15
```

#### 标题层级太浅

例如整篇文档只有一级标题，且每个一级标题下包含大量正文。

第一版可使用以下启发式条件：

```text
max_heading_depth <= 1
且 average_section_token_estimate > 1500
```

#### 标题质量差

有标题，但标题不能有效表达内容边界。

常见弱标题包括：

```text
说明；
正文；
其他；
附件；
内容；
概述；
详情；
第一部分；
第二部分。
```

第一版可以维护弱标题词表。

当弱标题占比较高，且章节内容较长时，生成语义目录。

#### 同一标题下存在明显多主题信号

例如同一章节内混合出现职责、流程、公式、例外、审批规则、参数表等不同内容。

第一版可使用以下结构信号：

```text
list_item_count >= 8
或 table_count >= 2
或 formula_like_line_count >= 3
```

这些信号只表示需要进一步判断，不代表一定要拆成多个知识单元。

#### 转换质量影响结构判断

如果 Markdown 转换结果存在明显结构损坏，可以使用语义目录辅助形成候选边界。

例如：

```text
标题丢失；
编号和正文粘连；
表格变形；
公式被压成一行；
页眉页脚混入正文。
```

这类范围生成出的语义目录和知识单元应标记 `needs_review`。

### 不触发条件

以下情况不应生成语义目录：

```text
原文标题结构已经足够清楚；
内容太短；
内容只是目录、文件变更记录、编制审核表、页眉页脚或联系方式；
材料不适合进入长期记忆。
```

第一版可使用以下短文本条件：

```text
token_estimate < 800
或 block_count < 6
```

短内容可以直接形成一个或少量 `OutlineSegment`，并在 segment 内完成材料维度分析。

### 对测试制度文档的判断

对于结构类似项目考核与激励制度的文档：

```text
一、 总则
  1.1 目的
  1.2 适用范围
二、 名词解释
三、 项目考核及执行流程
  3.1 考核责任人
  3.2 执行过程
四、 考核成绩评定
  4.1 考核标准
  4.2 成本考核评分
  4.3 项目需求变更
五、 奖金计算办法
  5.1 项目奖金包
  5.2 项目奖金核算
  5.3 项目奖金发放
```

不需要对整篇文档生成语义目录。

此类文档可以直接使用 source outline 形成 `OutlineSegment`，再在 segment 内做材料知识维度分析。

如果个别章节内部包含高价值参数表或公式，材料维度分析应识别为独立维度，或在边界裁决阶段拆分 / 标记 `needs_review`，而不是先生成整篇语义目录。

## 语义目录生成方法

语义目录生成必须受模型上下文长度约束。

`unit` 不能把无限长文档一次性输入大模型。

`unit` 阶段还应遵循模型调用最小化原则。

对于 30B 级、上下文窗口足以容纳当前 Source 的模型，默认使用一次主调用联合完成知识单元划分与知识点提取。模型输出必须以稳定引用把每个 `KnowledgePoint` 绑定到所属 `KnowledgeUnit` 和 `source_spans`。不得在主调用成功后再逐 Unit 调用模型提取 KnowledgePoint；只有单个 Unit 的结构、证据回链或长度校验失败时，才允许对该 Unit 做局部修复调用。

如果 Source 超过上下文预算，应按稳定的 outline segment 分批，每批仍联合输出 Unit 和 Point；不能退化为“先生成所有 Unit，再逐 Unit 提取 Point”的两阶段调用放大模式。

如果 source outline 或规则边界已经足够稳定，不应额外调用模型生成语义目录。

第一版原则是：

```text
语义目录生成必须基于受控上下文窗口；
优先在局部范围内生成；
再对局部结果进行合并。
```

### OutlineSegment

生成语义目录前，应先形成 `OutlineSegment`。

`OutlineSegment` 表示一段需要模型处理的受控输入窗口。

建议结构：

```text
OutlineSegment {
  id
  source_document_id
  source_heading_path
  start_line
  end_line
  block_ids
  text
  token_estimate
  reason
}
```

`reason` 可取：

```text
no_heading
long_section
weak_heading
multi_topic_signal
conversion_quality
oversized_document
```

`OutlineSegment` 不是知识单元，也不是语义目录节点。

它只是生成语义目录时的模型输入边界。

### 分段策略

分段应遵循以下顺序：

```text
优先按 source heading section 分段；
对过长 section 继续按 block 边界拆分；
无标题文档按连续 block 聚合；
保留必要的相邻上下文；
不拆断表格、代码块、公式和列表组。
```

相邻 segment 可以包含少量 overlap。

第一版建议：

```text
前后各 1-2 个段落；
或 200-400 tokens。
```

overlap 只用于模型理解，不作为默认 source span 归属。

### 上下文预算

调用模型前必须计算上下文预算。

上下文预算的基础值来自 `config.yaml` 中对应 OpenAI 模型的 `max_input_tokens`。

配置示例：

```yaml
openai:
  models:
    extraction:
      name: gpt-4.1-mini
      max_input_tokens: 1000000
      max_output_tokens: 4096
```

`unit` 不单独配置语义目录上下文预算。

语义目录生成应根据所用模型自动推算预算。

对应运行时结构：

```text
ModelContextBudget {
  model
  max_input_tokens
  reserved_instruction_tokens
  reserved_output_tokens
  reserved_safety_margin
  max_segment_tokens
  overlap_tokens
}
```

计算方式：

```text
max_segment_tokens =
  max_input_tokens
  - reserved_instruction_tokens
  - reserved_output_tokens
  - reserved_safety_margin
```

其中：

```text
max_input_tokens
  来自 openai.models.{model_key}.max_input_tokens；

reserved_output_tokens
  来自 openai.models.{model_key}.max_output_tokens；

reserved_instruction_tokens
  由 unit 内部根据提示词模板估算；

reserved_safety_margin
  由 unit 内部设置保守余量。
```

第一版不要用满模型上下文，即使模型支持很长输入，也应设置单个 `OutlineSegment` 的工程上限。

建议内部默认值：

```text
max_segment_tokens = min(4000, 计算得到的可用输入预算)
overlap_tokens = min(300, max_segment_tokens * 10%)
```

如果一个 segment 超过预算，应继续按 block 边界拆分。

### 局部生成

每个 `OutlineSegment` 独立生成局部语义目录。

模型输出必须是结构化结果。

提示词和输出 schema：

```text
prompts/unit/semantic_outline.md
schemas/unit/semantic_outline.schema.json
```

建议结构：

```text
SemanticOutlineItem {
  title
  center
  summary
  source_start_line
  source_end_line
  source_block_ids
  confidence
  needs_review
  reason
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `title` | 语义目录节点标题 |
| `center` | 该节点围绕的稳定主题或判断 |
| `summary` | 该节点的检索匹配短语（一句话或关键词），用于目录路径路由，不是给人阅读的摘要 |
| `source_start_line` | 来源起始行 |
| `source_end_line` | 来源结束行 |
| `source_block_ids` | 覆盖的 Markdown block |
| `confidence` | 模型对边界和主题判断的置信度 |
| `needs_review` | 是否需要人工复核 |
| `reason` | 生成该节点的理由 |

语义目录项必须带 source span。

没有 source span 的输出不能用于知识单元切分。

语义目录项的 `summary` 必须是**检索匹配短语**，不是给人阅读的说明。

要求：

```text
一句话以内，最好 8–40 字的主题关键词短语；
帮助后续模型判断“这个问题要不要进入该目录节点”，而不是解释节点内容；
可以只包含规则名、流程名、参数类型、判断对象等关键词；
不应写段落、不应复述标题、不应超出 source span。
```

### Source Outline Summary

source / recovered / manual outline 也需要目录节点说明。

`summary` 与 semantic outline summary 使用相同语义：**检索路由短语**，不是可读摘要。

当原文标题结构足够清楚、不需要生成 semantic outline 时，`unit` 仍应为进入检索链路的 source outline 节点生成或准备 summary。

提示词和输出 schema：

```text
prompts/unit/source_outline_summary.md
schemas/unit/source_outline_summary.schema.json
```

第一版触发条件：

```text
该 source outline 节点会被 KnowledgeUnit 引用；
该节点没有人工或上游提供的 summary；
该节点覆盖内容足以进入长期记忆；
该节点不是目录页、变更记录、页眉页脚或纯元信息。
```

输出的 `summary` 与 semantic outline summary 具有相同检索语义。

它会在 `precompile` 阶段进入 `outline_search_index.outline_summary`。

### 全局合并

长文档会生成多个局部语义目录。

合并阶段输入的是局部目录项列表，而不是全文。

合并目标：

```text
去重；
合并跨 segment 的同一主题；
统一标题；
保留 source span；
形成文档级 semantic outline。
```

第一版可使用规则合并：

```text
source span 高度重叠，且 center 相似，则合并；
source span 相邻，且 center 相同，则合并；
title 不同但 center 相同，则统一标题；
title 相同但 source span 相距很远，则保留为不同节点。
```

如果使用模型辅助合并，输入也应限制为：

```text
title；
center；
summary；
source span；
confidence；
reason。
```

不得重新输入全文。

### 和知识单元候选的关系

语义目录不是知识单元。

语义目录回答：

```text
这段材料内部有哪些稳定主题？
```

知识单元切分回答：

```text
哪些稳定主题可以形成最小完整知识包？
```

通常一个语义目录节点对应一个 outline segment，在该 segment 内再做材料知识维度分析。

但不是绝对一一对应：

```text
一个 outline segment 可能包含多个材料维度，需要拆成多个知识单元；
多个 outline segment 可能共同支撑一个材料维度，需要合并为一个知识单元；
某些语义目录节点只是过渡结构，不需要形成知识单元。
```

材料知识维度分析发生在 outline segment 确定之后、UnitBoundaryDecision 之前。

### 生成结果记录

语义目录是派生结构，需要记录生成信息。

建议记录：

```text
outline_type = semantic
generated_by = llm
model
prompt_version
generated_at
confidence
source_span
needs_review
```

这些字段用于后续调试、复核和重新生成。

## 知识单元切块策略

知识单元切分是 `unit` 的核心职责。

切分时必须以知识单元的核心定义为准：

```text
知识单元是围绕一个稳定主题或判断形成的最小完整知识包。
```

因此，知识单元不是：

```text
一句话知识点；
固定 token 长度的 chunk；
Markdown 段落；
Markdown 标题章节；
表格行；
列表项。
```

这些结构可以作为候选边界或单元内部材料，但不能直接等同于知识单元。

切块时三层职责分离：

```text
outline segment
  划定材料范围与目录上下文，不直接产出知识单元；

材料知识维度
  判断该范围内有几个可独立表达的知识面；

边界裁决
  判断每个知识面是否构成最小完整知识包，并决定 accept / split / merge / discard。
```

### 总体流程

第一版知识单元切分流程如下：

```text
normalized.md
  -> Markdown blocks
  -> source outline / recovered outline
  -> semantic outline need analysis
  -> semantic outline, optional
  -> OutlineSegment（目录划定范围）
  -> MaterialDimensionAnalysis（材料知识维度分析）
  -> UnitBoundaryDecision（边界裁决）
  -> KnowledgeUnitDraft
```

其中：

```text
Markdown blocks
  提供可追溯的原文结构和位置；

outline / OutlineSegment
  提供目录上下文和切块范围，不等于知识单元；

MaterialDimension
  表示某 outline segment 内一个可独立表达的知识面，带 center、unit_type 和 source_block_ids；

UnitBoundaryDecision
  对材料维度做 accept / split / merge / discard，判断是否满足最小完整知识包；

KnowledgeUnitDraft
  是最终写入 unit 构建结果的知识单元草稿。
```

知识单元生成与边界裁决使用：

```text
prompts/unit/knowledge_dimension_analysis.md
schemas/unit/knowledge_dimension_analysis.schema.json
prompts/unit/knowledge_unit.md
schemas/unit/knowledge_unit.schema.json
schemas/unit/knowledge_compile.schema.json
```

`knowledge_compile.schema.json` 是联合编译输出契约，至少包含 `knowledge_units[]`，且每个单元内包含 `knowledge_points[]` 及其 evidence/source span 引用。原有分步 prompt/schema 仅用于超长文档分批或局部修复，不是默认主链路。

当前实现：

```text
outline segment 由 BuildOutlineSegments 从 source / semantic outline 形成；
材料维度分析由 analyzeSegmentDimensions LLM-first，AnalyzeDimensionsRule 作 fallback；
边界裁决由 DecideBoundary 规则实现，产出 UnitCandidate；
knowledge_unit.md 仍用于候选精修 title / center / content。
```

### Markdown blocks

`unit` 应先把 `normalized.md` 解析为 block 序列。

建议结构：

```text
DocumentBlock {
  id
  source_document_id
  block_type
  heading_path
  start_line
  end_line
  text
  markdown
}
```

`block_type` 可取：

```text
heading
paragraph
list
list_item
blockquote
code_block
table
formula
metadata
noise
```

每个 block 必须记录所在的原文标题路径。

这个路径用于检索、展示和回溯，但不作为知识单元身份来源。

### OutlineSegment 与目录范围

`OutlineSegment` 表示一次切块分析的材料范围。

它来自 source outline section、recovered outline section、semantic outline item，或由连续 block 组、高价值参数表、公式上下文、流程列表等形成的局部范围。

职责只有一项：

```text
划定本次分析覆盖哪些 block、属于哪些目录路径。
```

outline segment 不直接产出知识单元，也不预设 segment 内只有一个知识面。

### 候选生成

`UnitCandidate` 是知识单元切分的中间产物，由材料知识维度分析在 outline segment 范围内生成。

候选不再默认等于标题章节或 semantic outline 节点。

候选来源包括：

```text
outline segment 内的材料知识维度；
经边界裁决保留或拆分后的维度子集；
高价值参数表及其解释；
公式及其上下文；
流程列表及其前后说明。
```

建议结构：

```text
UnitCandidate {
  id
  source_document_id
  candidate_type
  source_block_ids
  primary_heading_path
  covered_heading_paths
  semantic_outline_path
  start_line
  end_line
  text
  reason
}
```

`candidate_type` 可取：

```text
source_section
semantic_section
paragraph_group
list_group
table_with_context
formula_with_context
procedure_group
definition_group
```

`reason` 用于记录为什么形成该候选。

例如：

```text
材料维度分析识别出独立规则面；
材料维度分析识别出完整流程；
表格可被单独检索；
公式需要与解释合并；
连续列表共同描述一个流程。
```

### 材料知识维度分析

材料知识维度分析回答：

```text
在这个 outline segment 内，材料实际包含几个可独立表达、可单独引用的知识面？
```

它与以下概念必须区分：

| 概念 | 含义 | 阶段 |
| --- | --- | --- |
| outline segment | 目录划定的材料范围 | unit |
| 材料知识维度 | 原文内可独立表达的知识面（定义、规则、流程、参数表等） | unit |
| 认知路由维度 | 场景、目标、模式、焦点 | retrieval |
| KnowledgePoint 激活标签 | 概念匹配与激活用的摘要锚点 | precompile |

材料知识维度只服务于 unit 切块，不进入 retrieval 的 `CognitiveRouteContext`，也不与 precompile 的 KnowledgePoint 标签一一对应。

#### 输入与输出

输入限制在单个 `OutlineSegment` 内：

```text
segment 覆盖的 DocumentBlock 列表；
primary_heading_path / covered_heading_paths；
semantic_outline_path，如有；
source outline summary / semantic outline summary，如有。
```

不得整篇输入模型。

提示词和输出 schema：

```text
prompts/unit/knowledge_dimension_analysis.md
schemas/unit/knowledge_dimension_analysis.schema.json
```

建议输出：

```text
MaterialDimensionAnalysis {
  segment_id
  dimensions
}

MaterialDimension {
  dimension_id
  center
  unit_type
  source_block_ids
  confidence
  needs_review
  reason
}
```

`center` 是该维度围绕的稳定主题或判断句，是后续边界裁决和 KnowledgeUnit 的核心依据。

`unit_type` 可取：

```text
definition
rule
constraint
procedure
parameter_table
formula
case
experience
question
mixed
```

示例：

```json
{
  "segment_id": "seg-5-1",
  "dimensions": [
    {
      "dimension_id": "d1",
      "center": "项目奖金包按项目毛利的一定比例计提",
      "unit_type": "rule",
      "source_block_ids": ["b12", "b13"],
      "confidence": 0.86,
      "needs_review": false,
      "reason": "连续段落共同描述同一计提规则"
    },
    {
      "dimension_id": "d2",
      "center": "项目奖金核算需区分已回款与未回款项目",
      "unit_type": "constraint",
      "source_block_ids": ["b14", "b15", "b16"],
      "confidence": 0.82,
      "needs_review": false,
      "reason": "列表与后续说明构成完整约束"
    }
  ]
}
```

#### 判断原则

材料维度分析关注材料侧有几个独立知识面，不关注检索时用户会怎么问。

以下情况通常应识别为多个维度：

```text
同一段落内并存定义与适用边界；
规则正文与独立参数表；
流程步骤与例外处理；
案例说明与从中归纳的原则。
```

以下情况通常仍是一个维度：

```text
围绕同一 center 的标题、解释、依据和示例；
必须合并才能理解的公式与上下文；
共同描述同一流程的连续列表与前后说明。
```

每个 `MaterialDimension` 必须带 `source_block_ids`。

没有 source span 的维度不能进入边界裁决。

第一版应 LLM-first，规则仅作 fallback：

```text
按 block 类型和标题结构的粗粒度预分组；
模型不可用时退化为 outline segment 单维度候选。
```

### 目录路径记录

知识单元应记录自己来自哪个目录结构下，以支持目录检索和源文档回溯。

建议区分：

```text
primary_heading_path
covered_heading_paths
outline_paths
```

含义：

| 字段 | 含义 |
| --- | --- |
| `primary_heading_path` | 知识单元主要归属的原文标题路径 |
| `covered_heading_paths` | 知识单元覆盖到的所有原文标题路径 |
| `outline_paths` | source、recovered、semantic、manual 等多类目录路径 |

示例：

```text
primary_heading_path:
  ["五、 奖金计算办法"]

covered_heading_paths:
  ["五、 奖金计算办法", "5.1 项目奖金包"]
  ["五、 奖金计算办法", "5.2 项目奖金核算"]

outline_paths:
  source: ["五、 奖金计算办法", "5.1 项目奖金包"]
  semantic: ["奖金规则", "项目奖金包计算"]
```

目录路径是知识单元的来源上下文。

同一个知识单元可以出现在不同目录视图下，但其 `id` 不应因目录变化而改变。

目录路径本身不足以支撑高质量检索。

`unit` 还应保证每个被知识单元引用的目录节点可以回查到目录说明：

```text
source outline path -> source outline summary
semantic outline ref -> semantic_outlines.summary
manual outline ref -> manual outline summary
```

第一版可以不把完整目录说明内嵌到 `KnowledgeUnit`，但 `outline_refs` 必须足够让 `precompile` 回查说明并写入 `outline_search_index.outline_summary`。

### 边界判断

每个 `MaterialDimension`（或经拆分后的子维度）都需要经过边界判断。

判断目标不是“这段文字是否有知识点”，而是：

```text
这个知识面是否围绕一个稳定 center，形成了最小完整知识包？
```

建议输出：

```text
UnitBoundaryDecision {
  dimension_id
  candidate_id
  action
  center
  title
  unit_type
  source_block_ids
  reason
  confidence
  warnings
}
```

`dimension_id` 关联材料维度分析结果；`candidate_id` 在需要时关联落库的 `UnitCandidate`。

`action` 可取：

```text
accept
split
merge_with_previous
merge_with_next
merge_with_candidates
discard
needs_review
```

`center` 是边界判断中最重要的字段。

它表示该知识单元围绕的稳定主题或判断。

如果候选无法提炼出明确 `center`，通常不应直接成为知识单元。

### 接受条件

候选成为知识单元，需要同时满足以下条件：

```text
中心明确；
边界稳定；
脱离原文后仍可理解；
内容具备长期管理价值；
不是纯排版、过渡、目录或页眉页脚；
不是孤立知识点；
不是多个主题松散拼接。
```

可用如下判断函数表达：

```text
is_knowledge_unit(candidate) =
  has_stable_center
  and is_self_contained
  and has_long_term_value
  and is_minimal_complete_package
```

第一版实现时不应只返回 boolean。

应返回结构化解释，便于调试和人工复核。

### 拆分条件

材料维度过大或边界裁决判定 `split` 时应拆分。

触发条件包括：

```text
一个材料维度同时回答多个稳定问题；
一个 outline segment 内混合多个独立规则、流程、参数表或例外，但维度分析未充分拆开；
不同部分未来可能独立检索、独立更新或独立复核；
不同部分生命周期不同；
候选内容过长，超过单个知识单元的可读范围。
```

示例：

```text
一个章节同时包含：
  奖金包计算公式；
  项目级别与奖金系数表；
  亏损项目例外规则。

其中项目级别与奖金系数表可能被高频独立查询，可以拆成参数型知识单元。
```

### 合并条件

候选过小时应合并。

触发条件包括：

```text
单独存在时只是孤立断言；
多个候选共同回答同一个问题；
定义、解释、例子、反例和边界被错误拆开；
流程列表中的步骤共同构成一个完整流程；
表格脱离前后说明后不可理解。
```

示例：

```text
目的和适用范围虽然是两个小标题，
但它们共同回答制度为什么存在、适用于什么范围，
可以合并为一个知识单元。
```

### 丢弃条件

以下内容通常不生成知识单元：

```text
目录；
文件变更记录；
编制、审核、批准表；
页眉页脚；
联系方式；
纯过渡句；
重复导航；
广告或无关网页噪声；
无法进入长期记忆的临时材料。
```

这些内容可以进入 source metadata、document metadata 或被标记为 noise。

### 知识点与知识单元

知识点可以存在，但第一版不把知识点默认作为一等对象。

知识点应作为知识单元内部结构保存。

例如：

```text
知识单元：页面与知识单元的关系

内部知识点：
  页面不是知识单元；
  页面可以承载多个知识单元；
  一个知识单元可以出现在多个页面；
  知识单元身份不依赖页面路径。
```

这样可以避免一句话一个 unit。

### 表格、公式和列表

表格、公式和列表需要按语义处理。

规则如下：

```text
评分表通常应和评分公式、适用规则合并；
参数表如果具备独立查询价值，可以单独成为参数型知识单元；
公式必须和变量解释、适用条件一起形成知识单元；
流程列表通常整体形成流程型知识单元；
列表项默认不是独立知识单元。
```

如果 Markdown 转换导致公式、表格或编号粘连，应标记 `needs_review`。

### KnowledgeUnitDraft

边界判断完成后，生成 `KnowledgeUnitDraft`。

建议结构：

```text
KnowledgeUnitDraft {
  id
  source_document_id
  title
  center
  unit_type
  content
  internal_points
  source_block_ids
  source_spans
  primary_heading_path
  covered_heading_paths
  outline_paths
  outline_refs
  status
  confidence
  warnings
}
```

`unit_type` 可取：

```text
concept
judgment
principle
rule
procedure
calculation
parameter_table
case
question
mixed
```

`status` 可取：

```text
draft
active
needs_review
discarded
```

第一版可以默认生成 `draft`。

如果来源结构清晰、边界判断置信度高，可以进入 `active`。

如果来源转换质量差、公式表格损坏、边界不稳定或模型置信度低，应进入 `needs_review`。

### 输出质量约束

第一版切分结果应满足：

```text
不生成纯检索 chunk；
不默认一句话一个知识单元；
每个知识单元必须有 center；
每个知识单元必须可回到 source span；
每个知识单元必须记录目录上下文；
表格、公式、流程不能脱离解释上下文；
转换质量问题必须进入 warnings 或 needs_review。
```

### 示例判断

对于项目考核与激励制度类文档：

```text
3.2 执行过程
```

虽然包含 10 条列表项，但它们共同回答项目考核如何执行，应形成一个流程型知识单元，而不是 10 个知识单元。

```text
5.1 项目奖金包
```

其中项目奖金包计算规则可以形成一个知识单元；项目级别与奖金系数表因为具备独立查询价值，也可以形成一个参数型知识单元。

```text
4.2 成本考核评分
```

如果公式在 Markdown 中被压扁，应形成 `needs_review` 知识单元，而不是丢弃。

## 和 source 的边界

`source` 负责把外部材料转换成稳定输入。

`unit` 负责把稳定输入编码成知识单元。

边界如下：

| 能力 | 所属模块 |
| --- | --- |
| 接收原始文件 | source |
| 保存原始文件副本 | source |
| 调用转换服务生成 HTML / Markdown | source |
| 保存 HTML / Markdown 副本 | source |
| 记录来源上下文 | source |
| 判断是否进入长期候选 | source |
| 解析 Markdown block | unit |
| 生成语义目录 | unit |
| 形成知识单元候选 | unit |
| 判断最小完整知识包 | unit |
| 记录知识单元与来源位置关系 | unit |

`source` 不做知识理解。

它不负责生成知识单元、抽取概念、形成稳定认知结构或判断长期知识状态。

`unit` 不重新处理原始文件。

它只读取 `source` 保存的 `normalized.md`、HTML 路径和来源元数据。

## UnitSourceResult 输入契约

`unit` 构建入口应接收 `UnitSourceResult`。

建议结构：

```text
UnitSourceResult {
  source_id
  status
  ready_for_unit
  skip_reason
  error
  error_code
  error_message
  error_stage
  error_target
  retryable
  external_status
  external_message
  title
  source_kind
  intake_purpose
  trust_level
  origin_uri
  produced_at
  imported_at
  preview_html_path
  normalized_markdown_path
  source_page_count
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `source_id` | 来源文档 id，进入 unit 后映射为 `source_document_id` |
| `status` | source 当前状态 |
| `ready_for_unit` | 是否允许进入 unit 构建 |
| `skip_reason` | 未进入 unit 构建的原因 |
| `error` | source 阶段完整 `SystemError`，可为空；失败输入必须优先使用该字段判断 |
| `error_code` | source 阶段错误码，可为空 |
| `error_message` | source 阶段错误详情，可为空 |
| `error_stage` | source 阶段错误发生阶段，可为空 |
| `error_target` | source 阶段错误对象，可为空 |
| `retryable` | source 阶段错误是否适合重试 |
| `external_status` | 外部转换任务状态或 HTTP 状态，可为空 |
| `external_message` | 外部转换服务返回信息，可为空 |
| `title` | 来源文档标题 |
| `source_kind` | 来源类型 |
| `intake_purpose` | 导入用途 |
| `trust_level` | 来源可信等级 |
| `origin_uri` | 原始来源地址 |
| `produced_at` | 材料产生时间 |
| `imported_at` | 导入系统时间 |
| `preview_html_path` | 预览 HTML 路径 |
| `normalized_markdown_path` | 规范化 Markdown 路径 |
| `source_page_count` | 原文页数或等价数量 |

第一版仅对满足以下条件的 source 自动进入 unit 构建：

```text
ready_for_unit = true
status = ready_for_unit
intake_purpose = long_term_candidate
normalized_markdown_path 不为空
```

`temporary_evidence` 默认不进入长期知识单元构建，除非后续由人工或流程显式晋升。

`source_outline` 不要求由 source 输入。

第一版中，unit 可以在 Markdown 解析阶段从 heading 生成 source outline。

`recovered_outline` 属于 source 后续增强能力，第一版可以为空。

`error_code / error_message / error_stage / error_target` 只是 source 错误摘要。

如果 `ready_for_unit = false` 且 `error` 非空，unit 不创建构建 job，只记录跳过原因或把结果返回调用方。

如果 `ready_for_unit = true` 但缺少必要输入，unit 应生成自己的 `SystemError`，不能复用 source 的错误摘要。

转换质量告警第一版可以由 unit 在 Markdown 解析阶段生成。

## Markdown 解析规则

`unit` 应把 `normalized.md` 解析为稳定的 `DocumentBlock`。

解析目标不是渲染 Markdown，而是获得可追溯、可切分、可回链的结构。

### 行号和 block id

每个 block 必须记录：

```text
start_line
end_line
block_id
```

`block_id` 可以由以下信息稳定生成：

```text
source_document_id
start_line
end_line
block_type
content_hash
```

第一版可以不保证跨版本稳定，因为 source 第一版不做文件版本。

但同一次构建中，`block_id` 必须稳定。

### 标题解析

Markdown heading 生成 `heading` block，并维护当前 `heading_path`。

例如：

```text
# 第三章 采购需求
## 5. 技术要求
### 5.1 运行环境
```

后续普通 block 的 `heading_path` 应为：

```text
["第三章 采购需求", "5. 技术要求", "5.1 运行环境"]
```

如果文档中存在目录页，目录中的链接列表不应作为正文标题结构。

### 段落解析

连续普通文本形成 `paragraph` block。

如果转换结果把多个编号、公式或字段粘连到同一行，仍先作为 paragraph 记录，同时追加转换质量 warning。

### 列表解析

有序列表和无序列表应尽量保持为列表组。

第一版可以同时记录：

```text
list
list_item
```

其中 `list` 表示整体列表，`list_item` 用于细粒度 source span。

知识单元切分时默认使用 `list` 作为候选材料，避免一条列表项一个 unit。

### 表格解析

Markdown table 应作为整体 `table` block。

表格前后的标题、说明、公式或注释需要在候选生成时一起考虑。

如果表格列数不一致、表头缺失或内容明显粘连，应记录 `malformed_table` warning。

### 公式和疑似公式

第一版可以用启发式识别疑似公式行。

例如包含：

```text
=
＝
*
×
/
%
>=
<=
```

且同时包含业务字段名或数值。

疑似公式可标记为 `formula` 或 `paragraph` 加 `formula_like` warning。

如果公式被压扁，相关知识单元应进入 `needs_review`。

### metadata 和 noise

以下内容可标记为 `metadata` 或 `noise`：

```text
目录；
文档标题页；
编制、审核、批准表；
文件变更记录；
页眉页脚；
服务热线；
页码；
重复导航。
```

`metadata` 可用于 source/document metadata。

`noise` 默认不参与知识单元候选。

## 知识单元最小数据模型

第一版知识单元主表只保存长期需要稳定访问的字段。

建议模型：

```text
KnowledgeUnit {
  id
  source_document_id
  title
  center
  unit_type
  content
  original_excerpt
  internal_points
  primary_heading_path
  covered_heading_paths
  outline_paths
  outline_refs
  status
  confidence
  warnings
  created_at
  updated_at
}
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `title` | 展示标题 |
| `center` | 稳定主题或判断 |
| `unit_type` | 单元类型 |
| `content` | 整理后的完整知识包表达 |
| `original_excerpt` | 关键原文摘录或原文组合 |
| `internal_points` | 单元内部知识点 |
| `primary_heading_path` | 主要原文目录路径 |
| `covered_heading_paths` | 覆盖的原文目录路径 |
| `outline_paths` | source / recovered / semantic / manual 路径 |
| `outline_refs` | 关联的目录节点引用 |
| `status` | 构建状态 |
| `confidence` | 边界和内容置信度 |
| `warnings` | 质量告警 |

`outline_paths` 用于检索和展示。

`outline_refs` 用于回到完整目录节点，尤其是语义目录节点，便于调试和复核。

如果目录节点来自语义目录，`outline_refs` 应能回查到该节点的 `title / center / summary / path / source_span`。

其中 `summary` 用于后续 `precompile` 构建 `outline_search_index.outline_summary`，提高目录树词法预筛和 `outline_tree_match` 的范围匹配准确性。

如果目录节点来自 source outline，`outline_refs` 应至少能通过 `primary_heading_path / covered_heading_paths / source_spans` 回到对应 heading section，并生成或读取该 section 的目录说明。

第一版建议增加内部结构：

```text
OutlineNodeSummary {
  outline_type
  path
  title
  summary
  source_span
  confidence
  generated_by
}
```

`OutlineNodeSummary` 可以独立持久化，也可以由 `precompile` 在读取 `KnowledgeUnit` 时动态生成。

无论采用哪种实现，`precompile` 写入 `outline_search_index` 时必须能得到稳定的 `outline_summary`。

第一版至少支持：

```text
outline_refs.semantic_outline_ids
```

示例：

```text
outline_paths:
  source: ["第三章 采购需求", "5. 技术要求"]
  semantic: ["采购需求", "技术要求", "安全性要求"]

outline_refs:
  semantic_outline_ids: ["sem_001", "sem_002"]
```

知识单元只保存自己命中的目录路径和目录节点引用，不保存整棵语义目录。

整棵语义目录由 `semantic_outlines` 独立保存。

### content 策略

知识单元需要同时保留原文依据和整理表达。

第一版建议：

```text
original_excerpt
  保存来自 Markdown 的关键原文片段；

content
  保存经过整理后的知识单元正文；

source_spans
  保存原文位置。
```

这样可以兼顾：

```text
可读性；
检索质量；
事实追溯；
人工审核；
后续知识点提取和认知结构匹配。
```

`content` 不应引入原文没有依据的新结论。

如果模型整理时产生推断，应在 `warnings` 中标记 `derived_inference`，并在后续审核中确认。

## Markdown 位置记录

知识单元必须能回到 `normalized.md`。

建议结构：

```text
SourceSpan {
  id
  unit_id
  source_document_id
  block_id
  start_line
  end_line
  start_offset
  end_offset
  role
}
```

`role` 可取：

```text
primary
supporting
context
overlap
```

含义：

| 值 | 含义 |
| --- | --- |
| `primary` | 单元核心内容来源 |
| `supporting` | 公式、表格、例子、注释等支撑内容 |
| `context` | 帮助理解但不直接构成核心内容 |
| `overlap` | 分段 overlap，仅用于模型理解 |

如果知识单元跨多个标题，应记录多个 `SourceSpan`，并汇总 `covered_heading_paths`。

overlap 内容默认不进入 primary span。

## 证据回链

`unit` 的主证据位置是 Markdown。

第一版不要求把知识单元精确对应到 HTML 中的指定块。

原因是：

```text
HTML 主要用于预览；
部分 HTML 内容可能来自图片化页面；
图片化内容无法稳定建立文本块锚点；
HTML 结构可能受转换服务、样式和分页影响；
同一段知识在 HTML 中不一定有可靠 DOM 节点。
```

因此，第一版证据定位优先级是：

```text
Markdown source span
  精确证据锚点；

original source
  原始材料回看入口；

preview HTML
  文档级或页面级预览入口。
```

知识单元需要支持从自身回到：

```text
normalized Markdown；
original source；
preview HTML；
source metadata。
```

第一版回链方式：

```text
unit.source_document_id
  -> SourceDocument
  -> normalized_markdown_path
  -> preview_html_path
  -> original_path
```

其中：

```text
normalized_markdown_path + SourceSpan
  用于精确定位证据；

preview_html_path
  用于打开预览文档，不保证定位到具体块；

original_path
  用于回看原始材料。
```

如果转换服务或 source 后续能提供页码、HTML anchor、段落 id，且能够验证其稳定性，则 `SourceSpan` 可以扩展：

```text
source_page
html_anchor
original_position
```

这些字段是增强能力，不是第一版必需能力。

第一版必须保证至少可以回到 Markdown 行号。

## unit 构建状态机

unit 构建应以任务方式执行。

建议状态：

```text
pending
parsing
structure_analyzing
semantic_outlining
candidate_building
dimension_analyzing
boundary_deciding
unit_writing
completed
completed_with_review
failed
```

状态含义：

| 状态 | 含义 |
| --- | --- |
| `pending` | 等待构建 |
| `parsing` | 解析 Markdown blocks |
| `structure_analyzing` | 判断是否需要语义目录 |
| `semantic_outlining` | 生成语义目录 |
| `candidate_building` | 形成 OutlineSegment 与初始候选范围 |
| `dimension_analyzing` | 在 segment 范围内做材料知识维度分析 |
| `boundary_deciding` | 判断 accept / split / merge / discard |
| `unit_writing` | 写入知识单元和 source spans |
| `completed` | 构建成功 |
| `completed_with_review` | 构建成功，但存在需复核单元 |
| `failed` | 构建失败 |

失败任务应记录：

```text
error_code
error_message
failed_stage
retryable
```

## 数据库表

第一版建议表：

```text
unit_build_jobs
document_blocks
semantic_outlines
unit_candidates
knowledge_units
unit_source_spans
```

### unit_build_jobs

记录一次 source 到 unit 的构建任务。

字段：

```text
id
source_document_id
status
started_at
finished_at
failed_stage
error_code
error_message
created_at
updated_at
```

### document_blocks

保存 Markdown 解析结果。

字段：

```text
id
source_document_id
block_type
heading_path
start_line
end_line
text
markdown
warnings
created_at
```

### semantic_outlines

保存 unit 阶段生成的语义目录。

字段：

```text
id
source_document_id
outline_type
title
center
summary
path
source_start_line
source_end_line
source_block_ids
confidence
needs_review
generated_by
model
prompt_version
created_at
```

`summary` 是检索字段。

它必须被后续 `precompile` 原样或规范化后写入 `outline_search_index.outline_summary`。

`semantic_outlines` 不是知识单元正文表。

它保存的是目录树节点：

```text
title/path 用于定位；
summary 用于范围相关性判断；
source_start_line/source_end_line/source_block_ids 用于回到正文；
nodes/parent_id 用于表达层级关系。
```

第一版如果未显式保存 `parent_id`，也必须能从 `path` 推导父子关系。

### unit_candidates

保存候选，便于调试和人工复核。

字段：

```text
id
source_document_id
candidate_type
source_block_ids
primary_heading_path
covered_heading_paths
semantic_outline_id
semantic_outline_summary
start_line
end_line
reason
decision
warnings
created_at
```

### knowledge_units

保存知识单元。

字段：

```text
id
source_document_id
title
center
unit_type
content
original_excerpt
internal_points
primary_heading_path
covered_heading_paths
outline_paths
outline_refs
status
confidence
warnings
created_at
updated_at
```

`status`：

```text
draft
active
needs_review
disabled
discarded
deleted
```

状态语义：

```text
disabled  = 暂时不可用，不进入 precompile / retrieval / study 强化，可恢复；
discarded = 已判断不应继续作为知识使用，保留历史追溯，不进入新处理；
deleted   = 关联 SourceDocument 被删除或用户明确删除，不进入任何新处理。
```

当 SourceDocument 进入 `disabled / discarded / deleted` 时，关联 KnowledgeUnit 必须同步进入相同可用性状态。

`unit` 构建和重建只处理可用 SourceDocument，且只向 precompile 投递 `status = active` 的 KnowledgeUnit。

### unit_source_spans

保存知识单元到 Markdown block 的位置关系。

字段：

```text
id
unit_id
source_document_id
block_id
start_line
end_line
start_offset
end_offset
role
created_at
```

## 错误处理

unit 构建应返回 `UnitBuildResult`：

```text
UnitBuildResult {
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
skipped
needs_review
retrying
```

`data` 至少包含：

```text
source_document_id
unit_build_job_id
knowledge_unit_ids
unit_candidate_ids
semantic_outline_ids
document_block_ids
precompile_ready
```

第一版错误类型：

| 错误码 | 含义 | 是否可重试 |
| --- | --- | --- |
| `markdown_missing` | Markdown 文件不存在 | 否 |
| `markdown_read_failed` | Markdown 读取失败 | 是 |
| `markdown_parse_failed` | Markdown 解析失败 | 是 |
| `context_budget_exceeded` | 分段后仍超过上下文预算 | 否 |
| `llm_call_failed` | 模型调用失败 | 是 |
| `llm_output_invalid` | 模型输出结构不合法 | 是 |
| `source_span_mismatch` | 模型返回位置无法对齐原文 | 否 |
| `db_write_failed` | 数据库写入失败 | 是 |

处理原则：

```text
可重试错误进入 retry；
不可重试错误进入 failed；
局部模型失败不应导致整篇文档完全失败，能生成的单元继续生成；
位置无法对齐的语义目录或知识单元不得写入 active；
转换质量问题优先进入 warnings / needs_review。
```

`SystemError` 字段要求：

```text
module = unit
operation = unit.build
stage = 下表定义的阶段
target = markdown | block | semantic_outline | unit_candidate | knowledge_unit | source_span | database
entity_type = source | unit_build_job | knowledge_unit
entity_id = source_id / unit_build_job_id / knowledge_unit_id
request_id = UnitBuildResult.request_id
trace_id = UnitBuildResult.trace_id，可为空
```

阶段定义：

```text
unit.validate_source_input
unit.read_markdown
unit.parse_markdown
unit.analyze_structure
unit.build_semantic_outline
unit.build_blocks
unit.build_candidates
unit.validate_source_spans
unit.write_units
unit.write_source_spans
unit.enqueue_precompile
```

状态更新规则：

| 错误码 | 状态更新 | 是否进入 trace | 传给下一阶段 |
| --- | --- | --- | --- |
| `markdown_missing` | `unit_build_jobs.status = failed` | 否，除非当前请求依赖该材料 | 不传给 precompile |
| `markdown_read_failed` | `unit_build_jobs.status = retrying` 或 `failed` | 否，除非当前请求依赖该材料 | 不传给 precompile |
| `markdown_parse_failed` | `unit_build_jobs.status = failed` | 否，除非当前请求依赖该材料 | 不传给 precompile |
| `context_budget_exceeded` | `unit_build_jobs.status = failed`，相关候选可标记 `needs_review` | 否 | 不传给 precompile |
| `llm_call_failed` | 局部失败写入 warning；整篇不可用时 job `retrying` | 否，除非按需构建影响当前回答 | 只传 active 且可回溯的单元 |
| `llm_output_invalid` | 对应候选 `needs_review`，job 可为 `partial` | 否，除非按需构建影响当前回答 | 不传 invalid 候选 |
| `source_span_mismatch` | 对应候选 `needs_review` 或 `discarded` | 是，如果影响当前证据回链 | 不传缺少可靠 source span 的 active 单元 |
| `db_write_failed` | `unit_build_jobs.status = retrying` 或 `failed` | 否，除非当前请求依赖该材料 | 不传未持久化对象 |

失败时传给下一阶段的规则：

```text
success = false 时，不向 precompile 投递任何 KnowledgeUnit；
status = partial 时，只投递 status = active 且 source_spans 可回溯的 KnowledgeUnit；
status = needs_review 的候选不进入正式 precompile 索引；
warnings 必须随 UnitBuildResult 返回，并写入 unit_build_jobs 或相关候选对象；
error 必须使用 SystemError 或同等字段。
```

## 和 precompile 的输出契约

`unit` 输出给 `precompile` 的不是原始文档，而是结构化知识单元和知识点线索。

precompile 可依赖以下字段：

```text
id
source_id
title
center
unit_type
content
internal_points
primary_heading_path
covered_heading_paths
outline_paths
outline_refs
source_spans
status
confidence
warnings
```

precompile 不应直接依赖 Markdown 文件位置来理解知识。

Markdown 位置用于证据追溯。

precompile 的职责不是从单篇材料直接生成稳定领域和概念。

precompile 应基于知识单元：

```text
生成或接收知识点；
构建材料侧多路召回索引；
匹配已有领域和概念；
记录未匹配知识点和低置信激活路径；
保留来源和使用线索。
```

正式领域和正式概念应来自预制框架或反复使用后的稳定结果。

如果 `status = needs_review`，precompile 应跳过正式索引和正式激活路径。

候选领域和候选概念不应由 precompile 基于单篇材料直接生成。

它们更适合由 `retrieval -> trace -> study` 在真实使用后形成。

### UnitPrecompileResult

unit 构建完成后，如果存在可进入长期记忆构建的 active KnowledgeUnit，应生成 `UnitPrecompileResult`，作为投递 `PrecompileJob` 的唯一输入。

```text
UnitPrecompileResult {
  success
  status
  source_document_id
  unit_build_job_id
  knowledge_unit_ids
  skipped_unit_ids
  precompile_ready
  error
  warnings
  request_id
  trace_id
}
```

`precompile_ready = true` 的条件：

```text
UnitBuildResult.success = true；
UnitBuildResult.status = succeeded 或 partial；
至少存在一个 status = active 的 KnowledgeUnit；
这些 KnowledgeUnit 都具备 source_spans；
这些 KnowledgeUnit 不处于 needs_review / disabled / discarded / deleted；
```

当 `precompile_ready = true` 时，unit 应投递：

```text
PrecompileJob {
  id
  trigger = unit_build_completed
  source_document_id
  unit_build_job_id
  knowledge_unit_ids
  status = pending
  idempotency_key
  request_id
  trace_id，可为空
}
```

幂等键建议：

```text
idempotency_key = source_document_id + unit_build_job_id + knowledge_unit_ids_hash
```

投递失败时：

```text
UnitBuildResult.status = partial；
UnitPrecompileResult.precompile_ready = true；
warnings 包含 precompile_enqueue_failed；
error 使用 SystemError 或同等字段；
不得把 KnowledgeUnit 标记为 precompiled；
```

unit 只负责投递 `PrecompileJob`。

它不得在该阶段创建正式 Domain / Concept，也不得把候选领域或候选概念写入正式认知结构。

## 和 retrieval 的输出契约

`unit` 需要为 `retrieval` 提供可检索的知识单元、知识点线索和目录上下文。

retrieval 可依赖以下字段：

```text
id
title
center
unit_type
content
internal_points
primary_heading_path
covered_heading_paths
outline_paths
outline_refs
source_spans
status
confidence
warnings
```

其中 `outline_paths` 必须支持索引。

第一版 retrieval 至少应能基于目录结构执行：

```text
outline_type 过滤；
path 精确匹配；
path prefix 匹配；
path 节点关键词匹配。
```

示例：

```text
outline_type = semantic
path_prefix = ["采购需求", "技术要求"]
```

可以检索该语义目录下的所有知识单元。

语义目录路径也可以参与全文或向量检索增强。

索引文本可以包含：

```text
title
center
content
internal_points
outline_paths.semantic
outline_paths.source
```

但目录路径只是检索元数据和展示上下文，不是知识单元身份。

`retrieval` 不能用目录路径作为知识单元 id，也不能因为语义目录变化直接认定知识单元是新对象。

如果当前领域和概念激活失败，但基于知识单元或知识点的补充查找命中了有效内容，retrieval 应记录认知缺口，由 trace / study 后续处理。

## 和 trace 的输出契约

知识单元需要能被 trace 引用。

trace 记录的是一次问题处理过程中，哪些知识被召回、哪些知识被使用、哪些证据支撑了最终回答，以及哪些结果暴露了认知缺口。

`unit` 需要为 trace 提供稳定引用信息。

trace 引用知识单元时，至少应记录：

```text
unit_id
source_document_id
source_spans
outline_paths
retrieval_rank
used_in_answer
hit_route
```

字段含义：

| 字段 | 含义 |
| --- | --- |
| `unit_id` | 被召回或使用的知识单元 |
| `source_document_id` | 知识单元来源文档 |
| `source_spans` | 支撑该知识单元的 Markdown 位置 |
| `outline_paths` | 当时使用的目录上下文 |
| `retrieval_rank` | 检索召回排序，可为空 |
| `used_in_answer` | 是否真正进入临时认知模型或最终回答 |
| `hit_route` | 命中路线，例如领域概念激活或补充查找 |

trace 必须区分：

```text
retrieved
  被召回为候选；

used
  被临时认知模型、推理路径或最终回答采用。
```

这个区别用于后续学习。

被召回但未使用的知识，不应自动强化。

被使用且得到正反馈的知识，可以由后续 study / lifecycle 流程强化或沉淀。

如果知识单元是通过补充查找被采用，而不是通过当前领域和概念激活得到，trace 应记录该事实，作为候选领域或候选概念的来源线索。

## 和 lifecycle 的边界

第一版 `unit` 不实现完整记忆生命周期。

第一版只维护知识单元构建阶段需要的状态：

```text
draft
active
needs_review
discarded
```

这些状态含义是：

| 状态 | 含义 |
| --- | --- |
| `draft` | 已生成但尚未确认稳定 |
| `active` | 当前可用于检索和后续认知结构预编译 |
| `needs_review` | 来源、公式、表格、边界或模型输出需要复核 |
| `discarded` | 不作为知识单元使用 |

完整生命周期状态由后续 `lifecycle` 模块扩展。

例如：

```text
expired
superseded
historical
needs_validation
```

`unit` 第一版不处理知识过期、替代、历史解释和反馈强化。

但 `unit` 必须保留以下信息，为 lifecycle 后续判断提供基础：

```text
source_document_id；
source_spans；
status；
confidence；
warnings；
created_at；
updated_at。
```

## 验收标准

第一版 unit 构建验收标准：

```text
能够使用 test/*.md 作为 normalized Markdown fixture 构建测试 SourceDocument；
能够从 SourceDocument 读取 normalized.md；
能够解析 Markdown block 并记录行号；
能够识别并跳过目录、页眉页脚、文档元信息等非知识内容；
能够判断是否需要 semantic outline；
能够在上下文预算内生成 semantic outline；
能够基于 source / semantic outline 形成 OutlineSegment；
能够在 segment 范围内完成材料知识维度分析；
能够按最小完整知识包原则经边界裁决生成 KnowledgeUnit；
不会默认一句话一个知识单元；
不会默认一个标题章节一个知识单元；
不会生成固定 token chunk；
每个 KnowledgeUnit 必须有稳定 center，而非仅复述章节标题；
每个 KnowledgeUnit 必须有 title、center、unit_type、source_spans；
每个 KnowledgeUnit 必须记录目录上下文；
每个命中语义目录的 KnowledgeUnit 应记录 semantic outline path、semantic outline ref 和可回查的 summary；
KnowledgeUnit 的 outline_paths 可以被 retrieval 按 type、精确路径和 prefix 检索；
KnowledgeUnit 可以被 trace 通过 unit_id 和 source_spans 稳定引用；
第一版状态覆盖 draft、active、needs_review、disabled、discarded、deleted，不承担完整 lifecycle；
disabled / discarded / deleted 的 KnowledgeUnit 不进入 precompile、retrieval 或新学习；
表格、公式、流程列表不能脱离上下文；
转换质量异常会进入 warnings 或 needs_review；
构建任务状态可追踪；
失败原因可定位到具体阶段。
```

对于项目考核与激励制度类文档，期望结果是：

```text
目录、文件变更记录、页眉页脚不生成知识单元；
目的和适用范围可以合并为一个知识单元；
长流程列表整体形成流程型知识单元；
高价值参数表可以形成参数型知识单元；
公式粘连的单元标记 needs_review。
```
