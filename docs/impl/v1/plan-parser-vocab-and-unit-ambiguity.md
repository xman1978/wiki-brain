# 计划：合并定域+Parse + 快路径二次规范化 + 同 KU 歧义

> **「快路径二次规范化」部分已废弃（2026-08-12 定案，废弃结论不受下方追记影响）**：
> `session_normalize_tuple.md` 这次 Match 前的模型调用，最初被 `activation.md`
> 步骤 2 新增的 Match 内部第二轮模型辅助匹配取代——两者解决的是同一个问题（四元组
> 解析对不上已有观测组），继续保留 `session_normalize_tuple` 会在"域内有词表"场景
> 下为同一件事连续付两次模型调用；新机制直接判断"匹配不匹配"，不需要先盲改一遍
> 四元组再赌重新精确匹配能不能对上，覆盖范围也不区分首次/非首次提问，功能上完全
> 覆盖了这里要做的事。**本文档「合并定域+Parse」与「同 KU 歧义」两部分不受影响，
> 继续有效**——只有下面标题里「快路径二次规范化」这一段、以及正文中提到
> `session_normalize_tuple` 的部分作废，实现时不要再按这些段落构建二次规范化调用。
>
> **追记（2026-08-12 当天晚些时候）**：作为替代者被提及的"Match 内部第二轮模型
> 辅助匹配"本身也已改判撤销，见 `activation.md` 步骤 2、`retrieval.md` 步骤 2、
> CLAUDE.md 对应决策条目——Match 现在恢复为单级纯程序精确匹配，不调用模型。这
> **不代表 `session_normalize_tuple` 重新变得必要**：它的废弃理由是"直接精确匹配
> 不需要先盲改一遍四元组"，这个理由在 Match 变回纯精确匹配后依然成立，甚至更
> 直接——纯精确匹配下，盲改四元组再赌重新匹配能不能对上，价值比"有模型辅助匹配
> 兜底"时更低。`session_normalize_tuple` 维持废弃，代码层面的实际移除仍是待执行
> 的后续工作（见文档下方状态说明）。

> 状态：已按此方案改代码（2026-08-07，其中二次规范化部分见上方废弃说明，
> 代码层面的移除是本次文档修订之外的后续实现工作，本次修订不代为改代码）；
> **2026-08-07 修订**：四元组规范化改为 **Match 之前**执行（不再要求会话非首次、
> 也不再「先 Match 命中后再规范化」），并增加程序校验：规范化结果须对齐某一
> 观测组且不得冲突改写非空 constraint。（该 2026-08-07 修订本身也已被上方
> 2026-08-12 的废弃决定取代，保留在此仅作历史记录。）

> 相对前几版：取消独立 ExpandStandalone / 独立 Domain 调用；改为**一提示词同时定域+解析**；
> ~~域内 ActivationLink 条件组非空时，在快路径 **Match 前**再多一次规范化 Parse~~
> （已废弃，见上）。

## 已确认决策

1. **慢路径**：`parse + 定域` **同一提示词**一次完成；定域空 → Source **全库**、不再二次定域。
2. ~~**快路径规范化**~~（2026-08-12 已废弃，见文档顶部说明；下面四行保留仅作历史记录）
   - ~~Session 合并提示词产出 `domain_ids + 四元组`。~~（这一步不废弃，Session 合并提示词继续产出四元组，废弃的只是"Match 前额外调模型拉齐四元组"这一步）
   - ~~若该 domain 下 verified 条件组**非空**：Match **之前**调规范化提示词...~~
   - ~~条件组为空：直接用 Session 四元组 Match。~~
   - ~~（已取消「仅非首次才规范化」——同题新会话同样需要对齐观测组名字。）~~
3. **多 link**：`distinct(unit_id)==1` → 可 fast；`>1` → full。（不受本次废弃影响，继续有效）
4. ~~**慢路径不跑二次规范化**（即使用词表也只走第一枪合并结果）。~~（连带作废，慢路径本来就不涉及这套机制，条目本身也无意义了）

---

## LLM 次数（相对今日，**这份对照表整体作废，见文档顶部 2026-08-12 说明**——保留仅作历史记录，实际预算见 retrieval.md「LLM 调用预算对照」）

| 场景 | 今日 | 本方案 | 差额 |
|------|------|--------|------|
| 任意 → **慢路径** | Parse 1 + Domain 1 = **2** | 合并提示词 **1** | **-1** |
| **首次** → **快路径** | Parse **1**（不定域） | 合并 **1** | **持平**（多定了域，但未多一次调用） |
| **非首次** → **快路径**，域内无 AL 词表 | Parse **1** | 合并 **1** | **持平** |
| **非首次** → **快路径**，域内有 AL 词表 | Parse **1** | 合并 **1** + 规范化 **1** = **2** | **+1** |

结论：慢路径不增反减；快路径仅在「非首次且域内有可对齐词表」时多 1 次。

---

## 时序（含快/慢分叉，**下面时序图中「二次规范化」分支已作废，见文档顶部 2026-08-12 说明**；分支 A/慢路径部分继续有效）

会话层先做合并调用；~~是否第二次规范化取决于「是否走快路径尝试」且满足 2b~~（该判断连同二次规范化一并作废，快路径统一走 activation.md 步骤 2 的 Match——2026-08-12 当天晚些时候已改判为单级纯程序精确匹配，不再有"两级"结构，见文档顶部追记——不再有单独的"是否规范化"分叉）。

```text
POST /session/turn（或 retrieve 编排入口）
  1. 合并提示词（历史 + user_input）
       → domain_ids, subject, intent, audience, constraint, standalone_question
  2. 将 domain_ids 交检索；四元组暂存为 tuple0

Retrieve：
  · domain_ids 空 → 慢路径全库；快路径 Match 不域过滤
  · domain_ids 非空 → 慢路径按域滤 Source；快路径 Match 前按域滤 link

  尝试快路径 Match(tuple0)（域过滤后）：
    A. 未形成可走快路径的命中（无 verified / 多 unit 歧义等）
         → 慢路径；使用 tuple0；不再二次 Parse
    B. 可走快路径
         · 首次 session 提问 → 用 tuple0 直接 fast（+ verify）
         · 非首次 且 域内 AL 条件组非空
              → 第二次提示词：词表 + tuple0（+ 问题）→ tuple1
              → 用 tuple1 再 Match（或仅替换 qc 四元组后走证据）
              → 仍满足快路径则 fast；否则回落慢路径（tuple1 或 tuple0 策略见下）
         · 非首次 且 词表空 → 用 tuple0 直接 fast
```

### 「首次」判定

与前版一致：无 `LastQuestion` / 无可用历史 → 首次；否则非首次。

### 二次规范化失败

- 第二次调用失败或四元组无效 → **回退 tuple0** 继续快路径尝试；仍失败再慢路径。不阻断。

### 二次规范化后 Match 变空/变歧义

- 建议：回落慢路径；`domain_ids` 仍用第一枪结果（不再定域）。四元组优先 tuple1，无效则 tuple0。

---

## 改动 A：合并提示词（定域 + Parse）

### A1. Prompt

- 演进现有 `session_parse.md`（或新文件 `session_parse_with_domain.md`）：在现有四元组 + `standalone_question` 之外，增加 `domain_ids`（从给定 `domain_list` 中多选，格式与 `question_domain_match.md` 对齐）。
- 输入增加完整 `domain_list`（id/name/description），与今日定域 prompt 同源数据。
- 非首次：历史槽（上一轮问题、回答尾、上一轮解析）照旧，模型在同一输出里完成补全与定域。
- 首次：历史为空，直接对 `user_input` 定域+解析。

输出示例：

```json
{
  "domain_ids": ["..."],
  "intent": "...",
  "subject": "...",
  "audience": "...",
  "constraint": "...",
  "standalone_question": "..."
}
```

`domain_ids` 空数组 = 不定域成功 → 下游全库 / 不注入二次词表。

### A2. 废弃独立路径（本方案内）

- 不在 Parse 前单独调 `question_domain_match`
- 不做独立 `ExpandStandalone`（补全并入合并提示词）

慢路径检索内：若请求已带 `domain_ids`（含空），**跳过**原 Domain LLM。

---

## 改动 B：快路径二次规范化 Parse（**整节已废弃，2026-08-12，见文档顶部说明；`session_normalize_tuple.md` 不需要新建/保留**——当时的替代方案 `activation_match_judge.md` 已随 2026-08-12 当天晚些时候的改判一并撤销并删除，见 activation.md 步骤 2 与文档顶部追记，Match 现在不调用任何 prompt）

### B1. 触发条件（全部满足）

- 本轮判定为走**快路径**（域过滤后 Match 得到可采用的 verified，且 `distinct(unit_id)==1`）
- **非首次**提问
- 该 `domain_ids` 下 verified link 的 `observed_conditions`（关联典型问法/条件组）**非空**

### B2. 第二次提示词

- 新建如 `session_normalize_tuple.md`：
  - 输入：当前问题（standalone）、tuple0、`known_condition_groups`（域内 AL 条件组）
  - 输出：规范化后的四元组（可不要 domain）
  - 规则：能与某组明确对应则**逐字复用**该组字段；对不上则保留/微调 tuple0，禁止硬套

### B3. 词表查询

同前：verified ∩ current KP ∩ domain；展开条件组；上限约 40；audience/constraint 非空必带。

---

## 改动 C：检索共用 domain_ids + 域过滤

- `QueryContext.DomainIDs`
- 慢路径：非空按域滤 Source；空全库；不调定域 LLM
- 快路径 Match 前：非空按 Source.domain 滤 link；空不过滤

---

## 改动 D：同 KU 歧义

`len(unique unit_id) > 1` → full；`== 1` → 可 fast。单测 + `retrieval.md`。

---

## 编排落点建议

二次规范化依赖「是否快路径」，不宜只在裸 `postTurn` 里做完所有事。推荐：

1. `postTurn`：合并提示词 → 返回 `ExpandedQuery + domain_ids`（tuple0）  
2. `answer`/`RetrieveWithProgress`：用 tuple0+domain 试快路径；若满足 B1 → 调规范化 → 用 tuple1 完成快路径；否则慢路径用 tuple0+domain  

前端仍透传第一枪的 `domain_ids` + 四元组；二次规范化在服务端检索内完成（客户端无感）。

---

## 文档

- session：合并定域+Parse；快路径条件性二次规范化  
- retrieval：上游/合并产出的 domain_ids；空→全库；快路径域过滤与二次 parse 钩子；unit 歧义  
- activation：词表用于二次规范化，不再用于「Parse 前注入第一枪」（第一枪合并提示词可不带 AL 词表，避免未定域先注入）

---

## 不在首版

- 慢路径也做二次规范化  
- 同会话跳过合并定域的缓存  
- intent 同义词；M6/M7 测法  

---

## 实现顺序

1. **D** unit 歧义  
2. 合并 prompt + Parser/Turn 产出 domain_ids；retrieval 慢路径复用、取消内定域  
3. 快路径域过滤 + B1/B2 二次规范化钩子  
4. 单测：慢路径仅 1 次合并调用；快路径非首次有词表时出现第二次 normalize；F 组守门  

---

## 验收预期

| 项 | 预期 |
|----|------|
| 慢路径 | 定域+Parse 合计 1 次 LLM；空域全库 |
| 快路径首次 | 合并 1 次，无第二次 |
| 快路径非首次无词表 | 合并 1 次，无第二次 |
| 快路径非首次有词表 | 合并 1 + 规范化 1；四元组更贴 AL 条件组 |
| 同/异 unit | 同 unit 可 fast；异 unit full |
| F 组 | 守门失效 0 |
