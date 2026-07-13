# Evidence Mining 实现路径（V1）

## 职责

在 Rerank（慢路径）或激活层证据组装（快路径）之后、EvidenceSet 构建之前，把知识单元粒度的候选加工为片段粒度的证据：LLM 从每个 KU 正文中逐字摘选支撑回答的原文片段，程序做原文匹配校验，幻构片段被拒绝。挖掘失败的 KU 整段回退，回答不中断。

设计依据 `docs/design/evidence-mining.md`。核心原则：**挖掘是摘选不是改写，摘选结果程序可校验**。

## 数据结构

证据挖掘不新增表。产出体现在 EvidenceSet 上：

```text
挖掘前（一个 KU 候选一条 EvidenceItem）：
  { unit_id, point_id, content=KU 整段正文, source_ref=KU 行范围, role }

挖掘后（一个片段一条 EvidenceItem，一个 KU 可产出多条）：
  { fact_id, unit_id, point_id（继承所属 KU 候选）, role（继承）,
    content = 片段原文（逐字）,
    source_ref = { source_id, line_start, line_end }（片段的绝对行号）,
    mined = true }

整段回退（该 KU 挖掘失败）：
  原 EvidenceItem 保留，mined = false
```

`fact_id` 从 KU 级细化到片段级；`point_id` 非空约束不变（片段继承候选的 point_id），Trace 的 direct_point_ids 归集逻辑因此无需改动。

## 配置项（config.yml: evidence 节，新增）

```yaml
evidence:
  enabled:              true
  batch_max_chars:      6000   # 单次 LLM 调用携带的 KU 正文字符数上限（rune）
  max_fragments_per_ku: 5      # 单 KU 最多保留片段数（超出按输出顺序截断）
  min_fragment_chars:   8      # 短于该长度的片段丢弃（无信息量）
  retry:                1      # 批次级 JSON/Schema 失败重试次数
```

## 实现步骤

### 步骤 1：内部接口与分批

```text
Mine(ctx, question, subject, intent, candidates []EvidenceItem)
  → []EvidenceItem

分批：按候选顺序（direct 在前）装箱，每批 KU 正文字符数（rune）合计
      ≤ batch_max_chars；单个 KU 超限时独占一批（不切开 KU——
      挖掘输入必须是完整 KU 正文，否则片段充分性无从保证）；
批次串行或并发执行均可，并发受 llm.max_concurrency 约束；
每个候选分配批内临时编号 c1、c2…（与 Rerank 的 candidate_id 机制同理，
  不出模块）。
```

### 步骤 2：挖掘 Prompt

Prompt 文件：`config/prompts/evidence_mine.md`

```
你是证据摘选助手。从每个知识单元的正文中，逐字摘出真正支撑回答该问题的原文片段。

规则：
1. 片段必须与原文逐字一致，不改写、不概括、不翻译、不合并不相邻的句子；
2. 每个片段是最小且充分的一段：不携带与回答无关的上下文，
   但脱离该单元其余内容后仍能独立理解；二者冲突时以充分为先；
3. 片段可以是一句话、连续几行步骤、一条命令或表格中的连续行；
4. 一个单元最多输出 {{max_fragments}} 个片段；
5. 单元中没有任何内容支撑回答时，该单元输出空数组，不要硬凑。

问题：{{question}}
核心主题：{{subject}}
意图：{{intent}}

知识单元列表（格式：【c编号】后接该单元完整正文）：
{{candidates}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

注入的 `{{json_schema}}` 示例 JSON：

```json
{
  "results": [
    { "candidate_id": "c1", "fragments": ["逐字摘出的原文片段一", "片段二"] }
  ]
}
```

程序整合后用本文件 `## Schema` 段校验：candidate_id 存在于当前批次；results 覆盖批内全部 candidate_id（允许 fragments 为空数组）；fragments 为字符串数组。

**调用参数**：extraction 模型，temperature 0，记录 prompt_version / token 用量。

### 步骤 3：原文校验与行号定位

对每个片段执行，全部纯程序：

```text
1. 精确匹配：strings.Index(KU 正文, fragment) ≥ 0 → 通过；
2. 宽松匹配（精确失败时）：将 KU 正文与片段的连续空白字符
   （空格/Tab/换行）各折叠为单个空格后再匹配；命中后按折叠映射
   还原出原文中的实际字节区间，以原文形态作为片段最终 content
   （容忍模型对换行/缩进的抄写偏差，content 始终取原文而非模型输出）；
3. 两级均失败 → 丢弃该片段，记录 warn（unit_id、片段前 30 字）；
4. 长度 < min_fragment_chars → 丢弃；
5. 行号定位：片段在 KU 正文中的起止偏移 → 换算 KU 内相对行号 →
   绝对行号 = KU.line_start - 1 + 相对行号（1-based, inclusive，
   遵守 foundation.md 行号约定）；片段跨行时取首尾行；
6. 同一 KU 的片段按原文出现顺序排序，行范围完全重叠的去重（保留先出现者）。
```

**验证不变式**：`strings.Join(lines[frag.line_start-1:frag.line_end], "\n")` 必须包含片段 content（宽松匹配折叠意义下），单元测试断言。

### 步骤 3.1：表格片段整体化（fragment 落在 markdown 表格里时的兜底）

真实案例暴露的问题：住宿限额标准表把城市分类写在列名里（"分类｜A 类城市｜B 类城市｜C 类城市｜D 类城市"），模型挖掘时只摘出了数据行"全体员工｜350元｜280元｜220元｜200元"，没带表头行——这一行数据脱离表头完全看不出哪个数字对应哪一类，下游回答对"福州属于 C 类城市，报销标准是多少"这类问题因此猜错了列（答成了 350 而不是 220）。`evidence_mine.md` 规则 4 已经要求模型"片段来自表格时必须整体摘出"，但这只是 prompt 层面的约束，不保证每次都生效，需要程序兜底：

```text
每个片段完成步骤 3 的行号定位后：
  判断片段行范围内是否有任意一行是 markdown 表格行（形如 "| ... | ... |"，
    表头/分隔行/数据行语法上无法区分，只要"是不是表格行"这一个判断）；
  不是 → 不处理，维持原行范围；
  是 → 以该行范围为起点，向前向后扫描 KU 正文里连续的表格行，
    扩展到整个连续表格块的完整范围（表头 + 分隔行 + 所有数据行），
    片段 content 同步替换为扩展后范围对应的原文（而不是只在原片段基础上拼接表头）；
  行号定位（fact_id/line_start/line_end）按扩展后的范围计算；
  多个片段扩展后落在同一张表格 → 行范围完全相同，步骤 3 第 6 条的去重
    自然会把它们合并成一条，不会重复出现同一张表格。
```

`internal/evidence/service.go` 的 `expandToTableBlock` 实现。这一步只处理"表格"这一种 markdown 不可分割元素——代码/脚本片段理论上有同样的问题（一段逻辑脱离它的条件判断、变量声明就可能读错），但语料里的技术文档普遍没有用 ` ``` ` 代码围栏（裸文本 SQL/Shell 命令直接和普通段落混排），markdown 语法层面分辨不出"这是代码"，暂不做程序兜底，等出现具体案例再评估怎么可靠识别。

### 步骤 4：失败处理与回退

```text
批次级：JSON 解析或 Schema 校验失败 → 同 prompt 重试 retry 次；
        仍失败 → 该批全部 KU 整段回退（mined=false），记录 error；

KU 级： fragments 为空数组（模型判定无支撑内容）→
          role=direct 的候选：整段回退（direct 是 Rerank/激活层判定的
          直接证据，宁可整段进入也不静默丢失）并记录 warn；
          role=supporting 的候选：丢弃该候选（挖不出内容的背景证据
          价值有限，减少噪声），记录 debug；
        片段全部被校验丢弃 → 同上按 role 处理；

产出组装：校验通过的片段生成新 EvidenceItem（继承 role/point_id/unit_id，
        mined=true）；回退候选保留原 item（mined=false）；
        分配 fact_id 在 EvidenceSet 构建时统一进行（retrieval.md 步骤 4）。
```

回退保证：`Mine` 在任何失败下都返回可用的候选列表，不向上抛错中断回答；`enabled=false` 时直接原样返回输入。

### 步骤 5：可观测性

```text
每次调用记录结构化日志：候选数、批次数、产出片段数、丢弃片段数、
  整段回退 KU 数、耗时；
answers.evidence_snapshot 自然携带 mined 字段——
  评估脚本可统计片段化率（mined item 数 / 总 item 数）与
  片段引用率（citations 中 mined fact_id 占比），
  验证"引用更精确"的设计目标。
```

## 依赖

```text
基础设施：LLM client（extraction 模型）、结构化日志
Retrieval：唯一调用方（快慢路径统一接入，见 retrieval.md 步骤 3）；
           KU 正文由调用方按 markdown_path + 行号切片后随候选传入，
           本模块不访问文件系统
Session：  subject / intent 由调用方透传
Answer / Trace：无直接依赖——通过 EvidenceSet 契约间接生效
```

## 完成标准

```text
正常路径：候选经挖掘产出片段级证据，content 与 KU 原文逐字一致，
  行号定位满足验证不变式；
幻构拦截：fake LLM 返回原文中不存在的片段时，该片段被丢弃且不进入
  EvidenceSet；
宽松匹配：仅空白差异的片段可通过，content 取原文形态；
回退路径：批次 JSON 失败重试后仍失败 → 全批整段回退，回答链路不中断；
  direct 候选挖空时整段回退，supporting 挖空时丢弃；
截断与去重：超过 max_fragments_per_ku 截断，重叠行范围去重；
enabled=false 时输出与输入完全一致（MVP 行为）；
citations 可引用片段级 fact_id 并经 evidence_snapshot 反查到片段行号；
fake LLM 下正常、幻构、空片段、批次失败四类场景测试稳定运行。
```
