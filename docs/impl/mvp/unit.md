# Unit 实现路径

## 职责

Unit 从规范化 Markdown 中提取 KnowledgeUnit 和 KnowledgePoint，并生成 KnowledgePoint 之间的轻量语义关系网络（KPN），构成系统长期记忆的材料侧基础，写入 SQLite 和 Bleve 索引供后续检索使用。

## 核心组件

```text
Outline-segment 切块器（按 source_outlines 划定提取范围）
LLM 联合提取调用（一次调用产出 KnowledgeUnit[] + KnowledgePoint[]）
JSON Schema 校验器（校验 LLM 结构化输出）
单元级重试处理器（失败单元局部重试，不重跑整份材料）
KPN 关系生成器（提取完成后对全 Source KP 做关系分析）
KnowledgeUnit / KnowledgePoint / KPN 存储（SQLite）
Rerank 语义提取与持久化（SQLite，供在线 Rerank 直接读取）
Bleve units / points 索引写入
HTTP API
```

## 数据结构

### knowledge_units 表

```sql
CREATE TABLE knowledge_units (
    unit_id        TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    outline_id     TEXT REFERENCES source_outlines(outline_id),
    concept_id     TEXT REFERENCES concepts(concept_id),
    -- 匹配到的知识概念（可为空，批量匹配后写入）
    center         TEXT NOT NULL,
    -- 'center' 是该单元的核心主题或判断，10~40 字，供检索和展示使用
    line_start     INTEGER NOT NULL,
    -- 单元内容在规范化 Markdown 中的起始行号（1-based, inclusive，绝对行号，见 foundation.md 行号约定）
    line_end       INTEGER NOT NULL,
    -- 单元内容在规范化 Markdown 中的结束行号（1-based, inclusive，绝对行号）
    status         TEXT NOT NULL DEFAULT 'pending',
    -- pending / completed / extraction_failed
    error_msg      TEXT,
    prompt_version TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_knowledge_units_source_id ON knowledge_units(source_id);
CREATE INDEX idx_knowledge_units_outline_id ON knowledge_units(outline_id);
```

### knowledge_points 表

```sql
CREATE TABLE knowledge_points (
    point_id       TEXT PRIMARY KEY,
    unit_id        TEXT NOT NULL REFERENCES knowledge_units(unit_id),
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    content        TEXT NOT NULL,
    -- 可激活摘要，20~80 字，可在不读完整段落的情况下独立理解该知识面的核心主张
    point_type     TEXT NOT NULL,
    -- definition / rule / method / case / question（共 5 种，与 KP 提取 Prompt 枚举对齐）
    -- definition：定义或概念解释
    -- rule：判断、原则、约束
    -- method：方法、流程、步骤
    -- case：案例、经验
    -- question：悬而未决的问题
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_knowledge_points_unit_id ON knowledge_points(unit_id);
CREATE INDEX idx_knowledge_points_source_id ON knowledge_points(source_id);
```

### knowledge_point_relations 表（KPN）

```sql
CREATE TABLE knowledge_point_relations (
    relation_id      TEXT PRIMARY KEY,
    source_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    target_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    relation_type    TEXT NOT NULL,
    -- related / contradicts（枚举 2 种，见下方设计决策说明）
    direction        TEXT NOT NULL DEFAULT 'directed',
    -- directed（有向）/ bidirectional（双向）
    -- related / contradicts → bidirectional；程序当前只写 bidirectional
    -- 字段保留 directed 语义与默认值，为未来引入有向关系类型预留扩展空间，不代表当前会产生 directed 数据
    prompt_version   TEXT NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_kp_relations_source ON knowledge_point_relations(source_point_id);
CREATE INDEX idx_kp_relations_target ON knowledge_point_relations(target_point_id);
```

**设计决策（`prompt_version: v2` 起生效）**：KPN 关系类型最初设计为 5 种（related / hierarchical / depends / supplements / contradicts），MVP 实践中发现细粒度分类对检索召回质量提升有限，反而因 LLM 分类不稳定导致关系数量膨胀、噪音增多。收窄为 2 种：

```text
related：主题相关、互补、依赖或层级关系（原 related/hierarchical/depends/supplements 合并），双向
contradicts：约束冲突，双向
```

收窄后 `direction` 恒为 `bidirectional`，有向关系（directed）分支不再产生数据，但 schema 和 retrieval 侧查询逻辑保留对 directed 类型的支持（见 retrieval.md 步骤 8），便于未来若重新引入有向关系类型时无需迁移。

## 任务执行模型

Unit 提取任务以 **source 为粒度**，一个 source 对应一条队列任务，由单个 goroutine 顺序执行全部步骤：

```text
队列任务载荷：{ task_type: "unit_extract", source_id: "..." }

消费者执行顺序（顺序执行，单 goroutine，不并发）：
  Step 1  切块（所有分段在内存中确定）
  Step 2  遍历分段，逐段顺序调用 LLM 提取（Step 2 + Step 3 合并在循环内）
  Step 4  全部分段处理完后（无论部分失败），触发 KPN 关系生成
  Step 5  KPN 完成后触发 Concept 批量匹配
```

**选择顺序执行而非并发分段的原因：**
- 消除分段完成计数器和跨 goroutine 协调，实现最简；
- 单 source 的 LLM 调用已受 `llm.max_concurrency` 全局约束，并发分段不会加速；
- 顺序执行保证 Step 4（KPN）在所有分段结束后自然触发，无需额外信号；
- MVP 阶段 source 数量有限，顺序吞吐足够。

**"全部分段处理完"的判断**：循环结束即完成，无需数据库计数器。失败分段已在循环内标记 `extraction_failed` 并记录 `error_msg`，不影响循环继续。

**任务失败隔离**：LLM 调用失败（含重试后）只标记当前分段为 `extraction_failed`，不中止整个任务，后续分段继续处理。仅当切块本身（Step 1）或数据库读取失败时，整体任务终止并记录 error 日志。

---

## 实现步骤

### 步骤 1：Outline-segment 切块

Unit 提取按 outline 节点划定范围，每个范围作为一次 LLM 联合提取调用的输入：

```text
从 source_outlines 取当前 source 的全部节点（按 line_start 升序）；
取叶子节点（无子节点的节点）作为基础切块单位；
  - 叶子节点行范围内容字符数（rune）≤ segment_max_chars（默认 4000）：直接作为一段；
  - 叶子节点内容 > segment_max_chars 字符（rune）：按 Markdown 元素边界切行，每段字符数不超过 segment_max_chars，
    不可分割元素（表格、代码块等）整体保留，允许略超限；
  - 相邻叶子节点内容合计字符数（rune）< source.min_segment_chars（默认 400）：合并为一段，
    但合并不会跨越不同的顶层（Level 1）结构祖先——即使两个叶子节点各自都很小，
    只要分别属于不同的顶层章节，也不再合并进同一批次，宁可多几次很小的 LLM 调用，
    也不允许一个提取批次里混入两个不相关章节的内容（`internal/unit/segment.go` 的
    `topAncestorAtLine`：找到覆盖该行最深的 outline 节点，沿 ParentID 一路走到根节点，
    比较两侧的根节点是否相同）；
每段记录：outline_id（或 null）、绝对 line_start、绝对 line_end。

segment_max_chars 与 source.md 中语义目录生成的 segment_max_chars 为同一配置项。
source.md 的语义目录生成保证叶节点内容 ≤ segment_max_chars（见 source.md 6.5：叶节点超长细化改为
程序按 Markdown 结构确定性切分、模型只打标题，不再可能产生重叠区间），此处切块仅作语义目录生成
失败时的安全兜底，正常情况下不触发；不过目前 Unit 侧尚未真正实现这个兜底切分逻辑本身
（`segmentMaxChars` 参数已预留但未使用），已知的既有缺口，不在本次改动范围内。
```

切块策略影响提取粒度，通过评估集验证，不在运行时动态调整。

### 步骤 2：LLM 联合提取调用

#### 2.1 提取 Prompt

Prompt 文件：`config/prompts/unit_extract.md`（`prompt_version: v5` 起生效，见 2.3 的设计决策）

```
分析以下文本，识别其中可独立引用的知识面，每个知识面生成一个知识单元（unit）和对应知识点（points）。

知识单元是围绕一个稳定主题形成的最小完整知识包。过细则合并，过粗则拆分；过渡文字和格式噪声不生成单元。

每个单元用 unit_id（如 "1"、"2"）标记，points 通过 unit_id 关联对应单元，每个单元 1~3 个知识点。

知识点类型：
- definition（定义/概念）
- rule（判断/原则/约束）
- method（方法/流程）
- case（案例/经验）
- question（问题）

来源目录节点：{{outline_title}}
以下文本每行前标注了原文行号（仅供你判断单元边界参考，不要把行号抄进输出）：
{{text_content}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

#### 2.2 提取输出格式

units 和 points 平铺为两个独立数组，通过 `unit_id` 关联，避免数组嵌套。`unit_id` 是模型自定义的本地编号（如 "1"、"2"），由程序在写库时替换为系统生成的 UUID；`point_id` 同理，由程序写库时生成。

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL：

```json
{
  "units": [
    {"unit_id": "1", "center": "知识单元主题", "line_start": 5, "first_line_anchor": "第5行本身的原文开头，逐字，不超过30字", "line_end": 8, "last_line_anchor": "第8行本身的原文结尾，逐字，不超过30字"}
  ],
  "points": [
    {"point_id": "1", "unit_id": "1", "content": "可激活摘要内容", "type": "definition|rule|method|case|question", "line_start": 6, "first_line_anchor": "第6行本身的原文，逐字，不超过30字", "line_end": 6, "last_line_anchor": "第6行本身的原文，逐字，不超过30字"}
  ]
}
```

point 的 `line_start`/`first_line_anchor`/`line_end`/`last_line_anchor` 取值方式与 unit 级别完全一样（抄自 `[N]` 标记、逐字复制该行本身），但含义不同：这是**这一条 point 自己**取材于原文的行范围，不是它所属 unit 的整体范围，两者用途见 2.3 的 v6 决策说明。

程序将模型输出解析并整合后（锚点定位算出绝对行号、unit_id/point_id 本地编号→UUID、推断归属关系），用 `unit_extract.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查：每个 unit 的 `line_start`/`first_line_anchor`/`line_end`/`last_line_anchor` 非空；每个 point 的 `unit_id` 存在于 units 中、且同样有非空的 `line_start`/`first_line_anchor`/`line_end`/`last_line_anchor`；每个 unit 至少有 1 个 point；`content` 非空。

#### 2.3 边界定位（不单独信任模型自报的行号，也不单独信任模型抄写的锚点文本）

**设计决策沿革**：

- `prompt_version: v2`~`v3` 让模型直接抄行号 `N`，程序只校验 `line_start <= line_end`，不做任何位置校验。实践中出现过模型抄错/数错行号、导致一个单元的行范围大幅跨界、吞并大量无关内容的问题（例如把一个"配置 SSH 无密码登录通道"的单元错误标成跨 25 行，吞掉了中间十几个无关单元）。
- `v4` 改为只让模型给"该单元第一行/最后一行的原文锚点文本"（`first_line_anchor`/`last_line_anchor`，逐字复制、不超过 30 字），行号完全由程序在 segment 范围内逐行查找定位。这避免了 v2/v3 的越界吞并问题，但引入了新的失败模式：`findAnchorLine` 是逐条物理行比对（`mdLines[i-1]` 与锚点整体比较，从不做多行拼接），而模型写锚点时依据的是语义连贯性而非 Markdown 转换产生的物理换行——当一个知识单元的起始句恰好被转换器的段落空行切成两条物理行（例如标题独占一行、隔一个空行后正文另起一行）时，模型自然会把标题和正文开头连成一句去描述"这个单元的开头"，这段锚点文本就不会完整落在任何一条物理行内，导致定位失败、进而被判定为"抽取失败"——即使模型对单元本身的语义切分是完全正确的。这类失败在标题与正文习惯性分行的文档（如"一、二、三…"式条款、每条标题单独成行）中会大量出现，是 V1 阶段发现的"部分文档 KU 覆盖率异常低"问题的根因。
- `v5` 改为让模型把行号和该行内容一起报（`line_start`+`first_line_anchor`、`line_end`+`last_line_anchor`），程序先校验"模型报的行号"和"模型抄的内容"是否在 `mdLines[N-1]` 上互相印证，只有印证通过才直接采信该行号；印证不通过（模型数错行号，或者锚点文本本身跨越了物理行，本质上还是 v4 的失败场景）则退回 v4 的逐行扫描兜底，安全性下限与 v4 完全一致，不会重新引入 v2/v3 的越界吞并问题。同时，`first_line_anchor`/`last_line_anchor` 的措辞改为强调"必须是该行本身的原文，不得拼接下一行"，从源头减少模型写出跨物理行锚点的概率。
- `v6` 解决的是 v5 也没解决的另一类问题："选对了但选窄了"——unit 的 `points` 是模型综合理解一段内容后自由归纳的摘要，取材范围可能跨好几行；而 `line_start`/`line_end` 是模型另一次独立的"抄写起止行"判断。这两件事之间没有任何机制强制对齐：实测里反复出现 unit 把 `line_end` 报在一张参数表的表头行，而它自己的 `points` 却综合了表头下面好几行数据的情况——`LocateUnitBounds` 对这种"锚点真实存在、只是选窄了"的语义错误无能为力（2.3 末段已经说明这不在这套机制的覆盖范围内）。v6 让**每条 point 也报一遍自己的 `line_start`/`first_line_anchor`/`line_end`/`last_line_anchor`**（取值方式与 unit 级别完全一样），程序用 `WidenBoundsFromPoints`（`internal/unit/boundary.go`）对 unit 下所有能定位成功的 point 取行范围并集，写库的最终 `line_start`/`line_end` 是"unit 自报范围 ∪ 所有 point 定位到的范围"，不再单独信任 unit 级别那一次判断。定位不到锚点的 point（`first_line_anchor`/`last_line_anchor` 为空，例如仍在用 v5 输出的 `unit_extract_retry.md`；或锚点确实找不到）不参与并集计算，但知识点本身照常保留——这一步只影响行范围计算，不影响知识点取舍。

`internal/unit/boundary.go` 的 `LocateUnitBounds` 实现（复用证据挖掘模块 `docs/impl/v1/evidence.md` 同款"精确匹配→空白折叠模糊匹配→放弃"算法，见 `internal/foundation/textmatch`）：

```text
逐个 unit（按模型输出顺序）：
  先验证模型报的 line_start：line_start 必须落在 segment 范围内，且 mdLines[line_start-1]
    能匹配 first_line_anchor（精确匹配→空白折叠模糊匹配）；验证通过则直接采信 line_start；
  验证不通过（行号越界、或该行内容对不上锚点）→ 退回逐行扫描：
    从 cursor（首个 unit 为 segment.line_start，之后取上一个 unit 定位到的 line_end+1）开始，
      在 segment 范围内逐行找 first_line_anchor；找不到则从 segment.line_start 整段重新找一遍
      （容忍模型未按文档顺序输出 units）；
  line_end 同理：先验证模型报的 line_end 与 last_line_anchor 是否在 mdLines[line_end-1] 上互相印证，
    验证通过直接采信；不通过则从上一步定位到的行开始（含），在 segment 范围内逐行找 last_line_anchor；
  两者都命中 → 绝对 line_start/line_end 就是命中的行号（单行单元时两个锚点会落在同一行）；
  任一锚点（含验证与兜底扫描）在 segment 范围内完全找不到 → 视为该 unit 定位失败，
    走步骤 3 的单元重试/extraction_failed 路径，不写入行号。

point.point_type = llm_output.type
point.unit_id    = 系统为对应 unit_id（本地编号）的 unit 分配的 UUID
```

cursor 只是"从哪里开始找"的提示、用于消解同一 segment 内重复出现的行内容（如反复出现的 `# su – oracle`），不是强制的单调不重叠约束——语料里存在"总览单元合理包住若干细分单元"的场景，不应被这个机制破坏。有了模型报的行号后，重复行内容优先靠行号本身消歧，cursor 只在行号验证不通过时才需要介入。

这个机制能保证的是：定位到的行号一定落在 segment 范围内、一定对应 segment 里真实存在的某一行文字，不会再出现模型凭空报错行号导致越界吞并无关内容的情况；模型报的行号只有在与其抄写的内容互相印证时才会被采信，单独报错行号或单独写错内容都不会被静默接受。它不能保证的是：模型选中的锚点文本即使真实存在，也可能选到了本单元语义边界之外、但仍在 segment 内的另一行（比如误把下一个无关小节的标题当作本单元的结尾）——这类"选错了但选的是真文字"的语义错误不在这个机制的覆盖范围内；也不能保证模型一定会把锚点收敛到单一物理行内——prompt 的措辞降低了这个概率，但概率模型仍可能出错，出错时会退回定位失败路径，而不是产出错误的行范围。

#### 2.4 调用参数

```text
模型：extraction 模型
temperature：0
每次调用记录：source_id、outline_id、prompt_version、模型名、token 用量
```

### 步骤 3：校验和重试

```text
LLM 输出先做 JSON 解析和 JSON Schema 整体校验；

整体 JSON 解析失败或 Schema 校验失败：
  - 对整个 segment 使用 unit_extract_retry.md 重试一次；
  - 重试成功后继续进入逐条 KnowledgeUnit 业务校验；
  - 重试仍失败：记录该 segment 提取失败日志，不写入 KU/KP，继续处理后续 segment。

Schema 校验通过后，逐条校验每个 KnowledgeUnit：
  - center 非空；
  - line_start/first_line_anchor/line_end/last_line_anchor 经 LocateUnitBounds（见 2.3）能在 segment 内定位到行号；
  - points 非空且每条 content 非空；
校验通过的单元进入内存候选池（line_start/line_end 取定位结果），待语义抽取完成后由 PublishGeneration 统一写入；
校验失败的单元（含定位失败）若仍可从 LLM 输出中定位其本地 unit_id，则带原始文本段单独重试一次，使用重试 Prompt（见步骤 3.1），重试的定位同样走 LocateUnitBounds（cursor 固定为 segment.line_start，因为单独重试不再有兄弟单元的顺序上下文）；
  - 重试成功：加入同一个内存候选池；
  - 重试仍失败（含重试结果的锚点依然定位不到）：记录结构化 warn，不在 PublishGeneration 前写入 KU/KP、语义或索引文档；
无法定位到具体 unit 的失败项：记录 warn，跳过该项，不阻塞其他单元入库；
不重跑整份材料。
```

#### 3.1 重试 Prompt

Prompt 文件：`config/prompts/unit_extract_retry.md`

```
从以下文本提取知识单元和知识点，严格按 JSON Schema 输出。

要求：
1. 每个 unit 必须有 unit_id（如 "1"）、center（10~40字）、line_start/first_line_anchor、line_end/last_line_anchor（line_start/line_end 抄自该行前的 [N] 标记；first_line_anchor/last_line_anchor 必须是该 N 行本身的原文，逐字，不超过30字，不得拼接相邻行，单行单元 line_start 等于 line_end、两个锚点相同）
2. 每个 point 必须有 point_id（如 "1"）和 unit_id，且 unit_id 必须对应一个已存在的 unit unit_id
3. content 不得为空

文本（原文第 {{segment_line_start}} 行到第 {{segment_line_end}} 行，共 {{segment_line_count}} 行，每行前标注了原文行号，仅供参考，不要抄进输出）：
{{text_content}}

按以下 JSON Schema 输出：
{{json_schema}}
```

重试使用同一 prompt 的 `## Schema` 段校验，与 `unit_extract.md` 同步升级到 `v5`（字段结构一致）。

### 步骤 3.2：缺口回填（segment 内覆盖率兜底）

步骤 3 的逐单元校验/重试解决的是"单个 unit 定位失败"；但完整通过校验的 units 集合本身，也可能没有覆盖 segment 的全部行——模型会把表格里的某几行、代码里的某个条件分支等整体跳过（既不是"不提取的内容"列举的类型，也没有尝试为它生成 unit，纯粹是抽取遗漏），这类内容此前完全没有兜底，是"部分文档 KU 覆盖率异常低"的另一个根因（与 2.3 记录的定位失败是不同的失败模式）。这一步在 segment 的所有 unit 处理完（含步骤 3 的单元重试）后运行一次，程序自动定位并处理这些遗漏：

```text
1. 用 segment 内本次实际写入（含单元重试写入）的所有 unit 的 line_start/line_end，
   计算 segment 范围内未被任何 unit 覆盖的行区间（gap），算法与 ComputeCoverage
   一致（见 coverage.go），只是直接用内存里刚解析出的行范围，不用重新查库；

2. 每个 gap 按内容类型分两档处理（internal/unit/gapfill.go）：
   - gap 全部由标题行（# 开头）、空行、分隔线（---/===）、表格分隔行（| --- | --- |）
     组成 → 判定为纯排版噪声，不调用 LLM，直接走 3；
   - gap 含实质内容 → 用 unit_extract.md 对 gap 发起一次抽取调用（outline_title
     沿用父 segment 的标题）。text_content 不是只发 gap 自己的行：gap 有可能是连续
     代码/正文中间被切出来的一段（比如缺口两侧本该同属一个 IF...END IF 分支，但
     IF 那一行落在缺口外、已经被相邻 unit 吞掉了），只给模型看孤立的几行会让它要
     么正确放弃、要么在没有上下文的情况下编出一个似是而非但脱离语境的 unit——两
     者从返回结果上无法区分。因此 text_content 把 gap 所在整个 segment 的原文都带
     上（segment 受 segment_max_chars 限制，成本很低），但用 `[以下第 X-Y 行是上
     下文，仅供理解，不要在这些行上生成 unit]` / `[以下第 X-Y 行是本次需要处理的
     目标行范围，只在这个范围内生成 unit]` 明确标出哪段是目标、哪段只是给它理解
     用的上下文（internal/unit/gapfill.go 的 gapContextText）。
     定位与校验仍复用与主流程完全相同的 LocateUnitBounds，且 segment 参数固定传
     gapStart/gapEnd（而不是父 segment 的范围）：模型即使没听话在上下文范围里也
     生成了 unit，其锚点找不到落在 gapStart..gapEnd 内的位置，会被直接判定定位失
     败、丢弃，不会把上下文区域的内容当成新 unit 重复插入——这一层不依赖模型是否
     遵守文字指示。
       - 返回非空且校验通过的 unit(s) → 按正常流程写入新的 completed KU/KP
         （prompt_version 仍记 v5，这是同一份 unit_extract.md 的调用，不是新 prompt）；
       - 返回空 units 数组（模型判断这段内容本身不构成独立单元，与"不提取的内容"
         分支同一语义）、返回的 unit 全部因锚点落在上下文区域而定位失败、或调用
         失败 → 走 3；

3. 合并：将 gap 的行区间并入 segment 内与它行距最近的 unit（可能是本次抽取出的、
   也可能是步骤 3 之前就写入的），只扩大该 unit 的 line_start/line_end，不改写它的
   center/points；若与多个 unit 距离相等（典型场景：gap 恰好夹在两个相邻 unit 中间，
   到两侧的行距必然都是 1），固定并入行号更靠前的一侧。持久化后对该 unit 重新调用
   indexUnit 刷新 bleve 索引内容，使证据挖掘（docs/impl/v1/evidence.md）在这个 unit
   的正文范围内也能看到被合并进来的原文。

一个 segment 若整体提取失败（无任何 unit 写入，见步骤 3 的 extraction_failed 路径），
没有可并入的邻居，这一步不处理，维持现状。
```

这一步不改变步骤 2.3 的边界定位算法本身，只是在其之上加一层"整段抽取完之后、查漏补缺"的收尾。合并进邻居的 gap 只是扩大了该 unit 的证据行范围，并不会为其生成新的知识点——它能否被检索到仍然取决于该 unit 现有知识点的语义匹配；只有走 2 里"独立抽取"分支的 gap 才会产出真正可检索的新 KP。

### 步骤 3.3：重复合并（segment 内去重兜底）

步骤 3.2 解决的是"漏了"，这一步解决相反的问题——"同一个事实被重复提取成了两个 unit"：模型有时会把一段内容的标题单独提成一个 unit、紧跟着的正文内容又提成另一个 unit；或者一张参数表/一段简短命令块被换一种措辞重复讲了两三遍，各自成了独立的 unit（真实案例：`docs/impl/mvp/unit.md` 编写期间在"更新达梦数据库统计信息"这类表格/短命令相邻的段落上观察到过一个事实被提取 2~3 次的情况）。`unit_extract.md` 的"不重复提取"要求（见步骤 2.1）能从源头减少这类情况，但和步骤 3.2 的"覆盖完整性"要求存在张力（越强调不遗漏，模型就越可能因为拿不准而重复生成），不能只靠 prompt 兜底，需要程序在结果层面再检查一遍。这一步在步骤 3.2（缺口回填）之后运行一次：

```text
1. 将 segment 内当前的 units 按 line_start（相同则按 line_end）排序，只检查排序后相邻的
   两个 unit 是否行范围相邻/重叠/间隔不超过 dedupMaxGapLines（3 行，internal/unit/dedup.go）
   ——不做 O(n²) 全量两两比较，因为 3.2 跑完后 segment 内的 units 基本已经首尾相接，
   真正重复的一对必然相邻或隔着几行无实质内容的间隔（比如一个空行、一个已被 3.2 判定
   为琐碎跳过的标题行）；间隔阈值不是 0——实测发现真正的重复经常隔着 1~3 行才出现
   （标题和它的正文之间隔一个空行、同一参数在表格附近被换一种说法讲了第二遍中间隔着
   别的表格行），严格零间隔会漏掉这些；

2. 相邻/重叠/间隔不超过阈值的一对，按三级裁决处理（internal/unit/dedup.go 的 judgePair，
   由入库前的 resolveCandidateDuplicate 调用）：

   a. 确定性合并（不调 LLM）：行范围完全相同 且 规范化后（去空白/标点/括号补充）的
      center 完全相同——这是"同一个 span 被提取了两次、只是标题措辞略有出入"的形状
      （典型来源：一次抽取失败重试产生的孪生 unit），不需要模型判断。程序直接合并：
      center 取两者中更长的一个，points 按规范化文本去重取并集（等价的保留更完整的
      那条，独有的都保留）。

   b. 关系分类（unit_dedup_classify.md，只判断、不合并）：输出四分类之一——
        - duplicate：同一事实/规则/参数，只是措辞详略不同 → 进入 c；
        - parent_child：一个是总览、另一个是可独立存在的细节 → 两个都保留；
        - parallel：同一上级主题下的不同参数/步骤/分支 → 两个都保留；
        - distinct：不同内容 → 两个都保留。
      判断和合并分成两次调用，是因为一次调用同时要求"判断+生成合并结果"会使模型为了
      完成合并输出而倾向于确认重复；不确定时分类提示词要求宁判 parallel/distinct。
      不要求模型输出置信度小数（中小模型的数值不具备稳定校准意义）。

   c. 合并生成（unit_dedup_merge.md，仅 duplicate 时调用）：返回合并后的 center +
      1~5 条去重后的 points，两边独有的内容都必须保留。

   历史注：v1 版本用单个 unit_dedup.md 一次完成判断+合并，已被上述两段式取代，
   模板保留一个版本周期后删除。

3. 判定重复：把两个 unit 合并为一个——保留其中一个 unit 的 unit_id，center 和
   line_start/line_end（取两者并集）都改写为合并结果，原有 points 全部删除、替换成
   模型返回的去重后 points；另一个 unit 连同它的 points 一起硬删除（不是 lifecycle
   软删除——这一步发生在 KPN 生成、概念匹配等下游步骤之前，此时还没有任何东西引用过
   这两个 unit 的行，硬删除是安全的），并从 bleve 索引里移除。删除/合并后从头重新排序
   再扫一遍，直到一整轮没有发生任何合并为止（应对同一事实被重复 3 次以上的情况：先
   合并出一个新 unit，它可能又和第三个重复项相邻，需要继续检查）。
```

这一步和步骤 3.2 使用同一套"程序检测触发时机、LLM 判断语义、程序落盘"分工：程序只负责"这两个 unit 挨得够近，值得问一下"，真正判断"是不是同一件事"完全交给模型——这类语义判断（拆分是否正确、是否只是同一事实的不同措辞）本来就不是行号匹配能解决的，见步骤 2.3 末尾对"选错了但选的是真文字"这类语义错误不在定位机制覆盖范围内的说明。

### 步骤 4：KPN 关系生成

KPN 关系在当前 Source 全部 KU/KP 提取完成（无论部分失败）后，对状态为 `completed` 的 KU 下的 KP 统一运行一次关系分析。

#### 4.1 输入准备

```text
查询当前 source_id 下所有 status = completed 的 KU 关联的 KP，取：
  point_id、unit_id（对应 unit.center）、content、point_type

点数 ≤ 60：整个 source 发起一次 KPN 调用；
点数 > 60：按顶层 outline 节点分组，每组独立发起一次 KPN 调用；
  同一顶层节点下所有 KP 为一组，组内点数仍超 60 时硬切为每批 60 个；
  跨组关系不在本次生成，留待后续跨 Source 关系任务处理。
```

#### 4.2 KPN Prompt

Prompt 文件：`config/prompts/kpn_extract.md`

```
分析以下知识点列表，找出知识点之间的语义连接。

关系类型（仅 2 种）：
- related：两个知识点主题相关、互为补充、存在依赖或层级关系（双向）
- contradicts：两个知识点存在约束冲突或矛盾（双向）

原则：
- 只建立有明确依据的关系，不推测
- 同一单元内的知识点不建立关系（unit_id 相同的跳过）
- 关系总数不超过知识点数的 1.5 倍

知识点（格式：point_id TAB unit_center TAB content）：
{{knowledge_points}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

#### 4.3 KPN 输出格式

`direction` 不由模型输出，程序统一写入 `bidirectional`（`related`/`contradicts` 均为双向关系）。

注入 prompt 的 `{{json_schema}}` 是示例 JSON，不是 JSON Schema DSL：

```json
{
  "relations": [
    {"from": "point_id", "to": "point_id", "type": "related|contradicts"}
  ]
}
```

程序将模型输出解析整合后，用 `kpn_extract.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查：`from` 和 `to` 存在于当前批次 point_id 集合中；`from != to`。

#### 4.4 KPN 校验和写入

```text
校验每条关系：
  - from 和 to 必须存在于当前批次的 point_id 集合中；
  - from != to；

校验通过后写入 knowledge_point_relations：
  - source_point_id = from，target_point_id = to
  - direction 恒为 bidirectional（related / contradicts 均为双向关系）

KPN 生成失败（Schema 校验失败或 LLM 报错）记录 warn 日志，不阻塞 Source 完成；
KPN 生成不改变 KU / KP 的状态。
```

### 步骤 5：Concept 批量匹配

KPN 关系生成（步骤 4）完成后，对当前 Source 下所有状态为 `completed` 的 KU 做批量 Concept 匹配，写入 `knowledge_units.concept_id`。

**输入**：

```text
所有 completed KU 的 unit_id + center；
sources.domain_id（若有，则只取该 domain 下的 concept 列表；若为 null，则取全部 concept 列表）；
concept 列表：concept_id + name + description。
```

**Prompt 文件**：`config/prompts/unit_concept_match.md`

```
以下是一批知识单元，每条包含编号和主题描述：
{{units_list}}

以下是可用的知识概念列表：
{{concept_list}}

请为每个知识单元选择最匹配的概念 concept_id。若没有匹配的概念，concept_id 输出空字符串。

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

`{{units_list}}` 每行格式：`[unit_id] center 描述`
`{{concept_list}}` 每行格式：`[concept_id] name：description`

注入 prompt 的 `{{json_schema}}` 是示例 JSON：

```json
{
  "matches": [
    {"unit_id": "unit_uuid_xxx", "concept_id": "xxx"}
  ]
}
```

程序将模型输出解析整合后，用 `unit_concept_match.md` 内 `## Schema` 段的 JSON Schema 校验整合结果，检查 `unit_id` 存在于当前批次 unit_id 集合中。

**批量策略与结果处理**：

```text
单次 LLM 调用携带不超过 50 个 KU；超出则分批，每批独立调用；
concept_id 非空且存在于 concepts 表：写入 knowledge_units.concept_id；
concept_id 为空或不存在：concept_id 保持 null；
  → concept_id 为 null 的 KU 不受 concept 过滤约束；
LLM 调用失败：记录 warn 日志，当前批次 KU 的 concept_id 保持 null，不阻塞 Source 完成。
```

### 步骤 6：提取并原子持久化 Rerank 语义

每个将被发布为可检索的 KnowledgeUnit 都必须先完成 Rerank 语义提取。程序按 unit 的
Markdown 行范围读取正文，调用 `config/prompts/unit_semantics_extract.md` 生成并校验以下
语义分类字段：`source_theme`、`content_theme`、`intent`、`object`、`scope`。
（早期版本还抽取 `key_facts`，V1 已废弃——事实点统一由 KP 承载，见
`docs/impl/v1/semantics-curation.md`。）

```text
对候选 KU 批量提取语义；每个 unit_id 必须恰好返回一条语义结果；
语义与 KU / KP 的当前代写入同一 SQLite 事务，写入 unit_rerank_semantics；
语义 prompt_version 固定为当前提取版本（v1）；
事务成功后才发布新的 current KU/KP，并在随后写入 Bleve 索引；
```

语义提取、解析、覆盖校验或持久化任一步失败时，整个新代不发布：先前的 current KU/KP、
对应语义行和索引继续可用，失败代不会成为可检索内容。这样在线 Retrieval 不需要也不允许
针对原始候选正文补做语义提取。

### 步骤 7：写入 Bleve 索引

KnowledgeUnit 和 KnowledgePoint 入库后同步写入对应 Bleve 索引。KU 正文在写入时按 `sources.markdown_path` + `line_start` / `line_end` 动态切片读取，不单独存文件：

```text
units index 写入字段：
  unit_id、source_id、center、line_start、line_end、content（行号切片正文，供 FTS）

points index 写入字段：
  point_id、unit_id、source_id、content、point_type
```

KPN 关系不写入 Bleve（通过 SQLite 按 point_id 查询）。

索引写入失败记录错误日志，不将 Source 标记为 failed。

### 步骤 8：暴露 HTTP API

```text
POST   /sources/:id/units
  触发对指定 Source 的 Unit 提取（含 KPN 生成）
  仅对 status=completed 的 Source 生效
  响应：{ source_id, triggered_at }

GET    /sources/:id/units
  列出 Source 下的所有 KnowledgeUnit
  响应：[{ unit_id, outline_id, center, line_start, line_end, status }]

GET    /units/:id
  查询单个 KnowledgeUnit 及其 KnowledgePoint
  响应：{
    unit_id, source_id, outline_id, concept_id, center, line_start, line_end, status,
    points: [{ point_id, content, point_type }]
  }

GET    /units/:id/points
  列出 KnowledgeUnit 下的 KnowledgePoint
  响应：[{ point_id, content, point_type }]

GET    /points/:id/relations
  列出指定 KnowledgePoint 的 KPN 关系（双向合并）
  响应：[{
    relation_id, related_point_id, related_point_content,
    relation_type, direction, as_source（bool）
  }]
```

## 依赖

```text
基础设施：SQLite、Bleve 索引、LLM client、结构化日志、HTTP 框架
Source：依赖 Source 完成后产出的规范化 Markdown 和 source_outlines 树
Prompt 文件：config/prompts/ 下，版本号在文件内 frontmatter 中管理，不用文件名前缀
```

## 完成标准

```text
能对已完成的 Source 稳定触发 Unit 提取；
提取结果（KU + KP）写入 SQLite 和 Bleve，可通过 API 查询；
每个新发布为 current 的 KU 在同一事务中写入一条 prompt_version=v1 的 Rerank 语义；
Rerank 语义提取或落库失败时，上一代 current KU/KP、语义和索引保持不变；
锚点定位正确：unit.line_start / line_end 由 LocateUnitBounds 在 segment 范围内定位得出，为规范化 Markdown 的绝对行号（1-based, inclusive）；
可通过 strings.Split(markdown, "\n")[line_start-1:line_end] 还原单元原文内容（`markdown_path` 来自 sources 表）；
重试机制正常工作：可定位的单元业务校验失败时单元级重试一次；失败诊断只写日志，不越过原子发布边界创建 current KU；
KPN 生成在全 Source KP 提取完成后运行，关系写入 SQLite，可通过 API 按 point_id 查询；
KPN 生成失败不阻塞 Source 完成状态；
Concept 批量匹配在 KPN 完成后运行，concept_id 写入 SQLite，可通过 GET /units/:id 查询；
Concept 匹配失败不阻塞 Source 完成状态；
fake LLM client 下，提取、KPN 和 Concept 匹配路径测试可稳定运行，不依赖真实 LLM 调用。
```
