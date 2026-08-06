# Retrieval 实现路径（V1）

## 职责

在 MVP 完整链路之上增加两个前置命中层与统一的生命周期过滤：

```text
Wiki 直答层   已发布 Wiki 页面命中 → 直接基于页面回答（见 wiki.md）
激活层        verified ActivationLink 命中 → 跳过过滤与召回，直达证据（快路径）；
              candidate 命中只记 activation_hits，仍走慢路径（供晋升信号）
完整链路      未命中时回落 MVP 链路：Domain → Source → Outline/FTS → RRF → Rerank（慢路径）
```

所有路径在 Rerank（或快路径的证据组装）之后统一经过证据挖掘（见 `evidence.md`），再进入充分性判断与 EvidenceSet 构建。

目标：熟悉问题的 LLM 调用从 ≥4 次降至 2-3 次，且快路径错误命中不直接出口（命中后经证据充分性校验把关）。

## 检索总流程（V1）

```text
问题（经 Session 补全，沿用 MVP）
  ├─ 第 0 层：Wiki 直答候选采集（不调 LLM，三个入口，见 wiki.md 步骤 4）
  │    a. 词法：wiki index 查询（title/content/aliases/trigger_questions），
  │       分数 ≥ wiki_min_score；
  │    b. 概念：问题分词与概念名称词法匹配 → 该概念的 published 页面直接入候选；
  │    c. 四元组：qc.Subject/Intent/Audience/Constraint 与页面
  │       observed_conditions 做 conditionGroupMatches（Session 未解析时空转）；
  │    优先级：四元组命中 > 词法（分数降序）> 仅概念命中；
  │    命中的主题页不进直答序列，就地展开为其 contains 成员概念页
  │       （主题页是召回骨架，不是直答单元，见 wiki.md 步骤 8「检索接入」）；
  │    合并去重取前 wiki_max_candidates 个，按序直答尝试，
  │    某页 sufficient=true → Wiki 直答路径（path_type=wiki）；
  │    候选耗尽或为空 → 落入第 1 层，并携带 skeleton_point_ids
  │      （展开成员页面的 source_point_ids 并集）与 skeleton_page_id；
  │      同时写 topic_decompose_signal（见 wiki.md 步骤 9）
  ├─ 第 1 层：激活层 Match（不调 LLM，四元组完全匹配，见 activation.md 步骤 2）
  │    命中 → 快路径：
  │      链接目标 KP → 反查 KU（lifecycle=current）→ 直接构建 direct 候选
  │      → KPN 扩展补充 supporting（沿用 MVP 步骤 8）
  │      → 证据挖掘（1 次 LLM）
  │      → 快路径校验（1 次 LLM，见步骤 2a）：证据能否独立完整回答问题
  │         不通过 → 回落慢路径（步骤 7）
  │      → 充分性判断 → EvidenceSet(path_type=fast)
  │      → Answer（1 次 LLM）                          共计 3 次 LLM 调用
  └─ 未命中 → 慢路径（MVP 完整链路）：
       Domain 预过滤 → Source 过滤 → Outline/FTS 召回 → RRF → Rerank
       → KPN 扩展 → 证据挖掘 → 充分性判断 → EvidenceSet(path_type=full)
       → Answer

  骨架注入（第 0 层带下来 skeleton_point_ids 时，两层架构，
  见 wiki.md 步骤 8「检索接入」）：
    这批 point_id 反查 KU 后直接作为 Rerank 候选注入，**跳过 Outline/FTS
    召回与 RRF**（它们本就是为了找到这些 KU；主题页已经给出了经过一阶
    编译验证的候选集），其余环节不变；
    注入候选与 rerank_top_n 的关系：注入优先占位，不足则由既有召回补齐
    （skeleton_point_ids 数 ≥ rerank_top_n 时按所属成员页面在候选序列中的
    顺序截断）；
    不增加任何 LLM 调用——这是主题页在 V1 的全部收益来源：
    同一个复杂问题，命中主题页时慢路径少一轮召回、候选质量更高。
```

## 配置项（config.yml: retrieval 节扩展）

```yaml
retrieval:
  # —— MVP 既有配置保留 ——
  outline_fts_min_score: 0.5
  rerank_top_n:          20
  # —— V1 新增 ——
  fast_path:              true   # false 时激活层只记录命中、不走快路径（灰度开关）
  activation_match_top:   5      # 激活层最多采用的链接数
  fast_path_verify:       true   # 快路径证据充分性校验开关（false 时跳过校验，
                                  # 仅灰度/评估用，生产不建议关闭）
  slow_path_verify:       true   # 慢路径答题前充分性校验（步骤 2b）；false 时跳过
  fast_path_fallback:     true   # 快路径回答失败时自动回落慢路径
  wiki_min_score:         2.0    # Wiki 直答的 Bleve 最低分（BM25，需评估集校准）
  wiki_max_candidates:    3      # 直答候选序列长度上限（依次尝试，
                                  # 首个 sufficient=true 即停；设 1 退化为原 top-1 行为）
                                  # 主题页命中不占候选位——它展开为成员概念页，
                                  # 占位的是成员（wiki.md 步骤 8）
```

## 实现步骤

### 步骤 1：EvidenceSet 契约扩展

```text
EvidenceSet 新增：
  path_type            fast / full / wiki
  activation_hits[]    [{ link_id, point_id, match_score }]
  gap_reason           no_candidates / judge_filtered（空字符串＝非 gap 结果；
                        产出位置见步骤 6，消费方见 study.md「knowledge_gaps 表扩展」）
  filtered_evidence[]  结构同 Evidence，role 固定为 "irrelevant"；
                        rerank judge 判定不相关而被剔除的候选快照（产出位置见步骤 6）

EvidenceItem 新增：
  mined              bool（证据挖掘产出，见 evidence.md）
```

`path`（short/deep）、`fact_id`、`source_ref` 等既有字段语义不变。Answer 层无需改动：仍按 path 分发、按 fact_id 校验 citation；evidence_snapshot 自然携带新字段——`filtered_evidence` 与 `gap_reason` 也会随整个 EvidenceSet 一起序列化进 `answers.snapshot`，不需要 Answer/Trace 额外处理存储。

### 步骤 2：激活层（快路径证据构建）

```text
1. 调 activation.Match(expandedQuery) 得 LinkMatch 列表（≤ activation_match_top）；
   输入是 Session 产出的完整 ExpandedQuery（expanded_question + 四元组），
   不是用户原始输入——匹配含 audience / constraint 硬性守门与
   subject / intent 计分，规则见 activation.md 步骤 2；
   Match 返回 verified + candidate（均要求 KP lifecycle=current）；
   为空 → 慢路径；fast_path=false → 记录命中日志后仍走慢路径
   （activation_hits 照常写入 EvidenceSet，供灰度期观察命中质量）；
   全部命中均为 candidate → 记录 activation_hits 后回落慢路径
   （candidate 只记信号、不直答；2026-07-22）；
   其中 verified 命中 >1 条不同链接 → 视为歧义，同样回落慢路径（2026-07-19
   实测发现：命中分数恒为 1.0，没有排序依据取舍，若不管直接把
   多个 point 的 KP 都当 direct 证据塞入，等于让一条不相关但读起来
   沾边的证据免检直接进答案；慢路径的 rerank + 证据挖掘本就能正确
   处理"一个问题需要综合多个 KP"的情况，交给它比在快路径里无差别
   打包更安全）；此时 TouchLastUsed 不触发（这些链接没有被真正用于
   回答），但 activation_hits 仍照常写入（含 candidate），Trace 按普通
   快路径未命中一样评分；

2. 取命中链接的 point_id → 反查所属 KU：
     SELECT ... FROM knowledge_points p JOIN knowledge_units u ...
     WHERE p.point_id IN (?) AND p.lifecycle='current' AND u.lifecycle='current'
   全部反查为空（理论上不发生，Match 已过滤）→ 慢路径；

3. 按 unit_id 去重构建候选，role=direct，point_id=命中链接的 point_id，
   content 按 markdown_path + line_start/line_end 切片（沿用 MVP 规则）；
   跳过 Rerank——批量候选分类不需要；但快路径命中不等于证据仍然充分
   （KP 内容可能已更新、问题可能带四元组未捕捉的细节），正确性由
   步骤 2a 的单次校验把关，不再依赖 activation_failure 事后回流作为
   唯一防线；

4. KPN 扩展沿用 MVP 步骤 8（对 direct KU 查邻居 KP，role=supporting，
   邻居 KP 及其 KU 均要求 lifecycle=current）；

5. 异步 TouchLastUsed(本次用于快路径的 verified link_ids)，不阻塞请求。
```

### 步骤 2a：快路径校验（fast_path_verify）

证据挖掘完成后、充分性判断之前，对快路径证据做一次轻量 LLM 校验：

```text
输入：expanded_question + 全部快路径证据（direct + supporting，
      挖掘后的片段；整段回退时用整段）
Prompt：config/prompts/fast_verify.md，输出 JSON
      { "sufficient": true/false, "reason": "..." }
判定：sufficient=false 或调用失败/解析失败 → 视为快路径失败，
      触发步骤 7 回落（保守：校验环节任何异常都不放行）；
      sufficient=true → 继续充分性判断与 EvidenceSet 构建。
```

校验失败照常保留 activation_hits 进入慢路径 EvidenceSet，Trace 据此产生 activation_failure（机制同步骤 7），校验结果反馈进学习回路。

fast_path_verify=false 时跳过本步骤，直接进入充分性判断（行为与旧版一致，仅供灰度/评估对照，生产环境不建议关闭）。

### 步骤 2b：慢路径校验（slow_path_verify）

慢路径（`path_type=full`）在 Answer 生成之前做与步骤 2a **同构**的充分性校验（复用 `config/prompts/fast_verify.md`、classification 模型）：

```text
调用点：Answer.generate / AnswerStream，在已有 direct/supporting 证据、
      即将调用 answer_short/answer_deep 之前（Wiki/快路径不经过本步骤——
      各自已有 sufficient 闸门）。
输入：question + 全部证据（direct + supporting，挖掘后的片段或整段回退）
判定：sufficient=false → 拒答，返回与无证据兜底相同的固定话术
      （HasAnswer=false、citations 空、Path=none），EvidenceSet 原样保留
      供 Trace/审计观察「召回了什么但判定不够答」；
      调用失败/解析失败 → 放行继续生成（慢路径已无下一层可回落，
      与快路径「校验异常即回落」不对称，避免校验抖动导致大面积拒答）。
```

动机：慢路径 Rerank 常把「近邻相关」材料标成 direct（问 OA 填单召回易快报流程、问升级召回部署步骤）。答题模型在小参数下容易把近邻概念改写成所问概念的操作答案；充分性闸门输出的是结构化 `sufficient` 布尔，比依赖答题 Prompt 自守门更稳。

`slow_path_verify=false` 时跳过，行为与改前一致（灰度/对照用）。

### 步骤 3：证据挖掘接入

快慢路径在候选确定后统一调用证据挖掘模块（`evidence.Mine`，见 evidence.md）：

```text
输入：question + subject/intent（Session 解析产出）+ 全部候选（direct + supporting）
输出：片段级 EvidenceItem 列表（挖掘失败的 KU 整段回退，mined=false）
evidence.enabled=false 时跳过，行为与 MVP 一致（整段证据）。
```

### 步骤 4：充分性判断与 EvidenceSet 构建

```text
充分性规则沿用 MVP（direct 非空 → short，否则 deep）；
快路径补充一条：direct 候选挖掘后片段全部为空且整段回退也为空
  （KU 正文读取失败等异常）→ 视为快路径失败，触发步骤 6 回落；
fact_id 分配：挖掘产出的每个片段一个 fact_id（见 evidence.md 步骤 4），
  其余构建逻辑沿用 MVP 步骤 10；
写入 path_type 与 activation_hits。
```

### 步骤 5：生命周期过滤（慢路径改动点清单）

慢路径逻辑不变，以下位置统一追加 lifecycle 过滤：

```text
units index / points index 查询：conjunction TermQuery(lifecycle=current)；
outline 召回取节点下 KU：SQL 追加 u.lifecycle='current'；
points→unit 反查、代表 KP 选取：追加 p.lifecycle='current'；
KPN 扩展邻居：邻居 KP 与其 KU 均要求 current；
Rerank 候选 content 切片前再校验一次 KU lifecycle（防扫描间隙状态变更）。
```

此外，MVP 沿用的 Domain 预过滤与 Source 语义过滤（`mvp/retrieval.md` 步骤 2-3）需追加 `sources.shadow_of IS NULL`，排除 reupload 期间正在处理的影子 Source——影子在换血完成前不应作为一个独立来源参与检索（见 `lifecycle.md` 步骤 2 的 Shadow Source 机制）。

### 步骤 6：Gap 诊断字段（gap_reason / filtered_evidence）

仅慢路径产出（快路径命中即为 direct，不存在这两个字段的取值场景）；用于 study.md 定位「检索不到」还是「检索到但被判无关」两类知识盲区，见 study.md「knowledge_gaps 表扩展」。两处改动点：

```text
1. RRF merge（MVP 步骤 6）合并结果为空时：
     gap_reason = "no_candidates"
     filtered_evidence 保持空（没有候选可判，无内容可展示）
     直接按 MVP 既有逻辑返回空 EvidenceSet（行为不变，只多写一个字段）

2. Rerank（MVP 步骤 7 / evidence.md 证据挖掘之前）：
     judge 返回 role="irrelevant" 的候选，原逻辑直接丢弃；
     现追加：按候选原有的 content 切片规则（同 direct/supporting 一致）
       构建 Evidence（role="irrelevant"，fact_id 留空——未挖掘、不参与 citation
       校验，仅供人工查看），追加进 filtered_evidence；
     数量上限＝rerank_top_n（与候选池同一上限，不单独配置）；
     若充分性判断后 direct、supporting 均为空（步骤 4）：
       gap_reason = "judge_filtered"
     否则（仍产出了 direct 或 supporting）：
       gap_reason 留空——不是 gap，filtered_evidence 仍然保留供参考，
       但不影响 study.md 的 gap 聚合（quality != gap 不产生 knowledge_gap 事件）。
```

`answer_error`（检索有证据、LLM 生成失败）不在此步骤产出——Trace 层直接读 `AnswerResult.Path == "error"` 判定，无需 retrieval 提供额外字段（见 trace.md）。

### 步骤 7：快路径回落

```text
触发条件（fast_path_fallback=true）：
  a. 步骤 2a 校验不通过（sufficient=false 或校验异常）；
  b. 步骤 4 判定快路径失败；
  c. Answer 完成后 has_answer=false 或 citations 为空
     （Answer 层回调 Retrieval 提供的 fallback 句柄）；

回落执行：以原问题走一遍慢路径并重新生成回答，最终返回慢路径结果；
  本次 trace 记录 path_type=full，activation_hits 保留原命中
  （Trace 据此产生 activation_failure，见 trace.md 步骤 3——
   命中的 KP 不在最终 direct_point_ids 中即判 failure）；
回落只执行一次，慢路径结果不再回落；
回落发生记录 warn 日志（link_ids、原因），供灰度期监控快路径质量。
```

### 步骤 8：HTTP API

```text
POST /retrieval（既有）
  响应 EvidenceSet 增加 path_type / activation_hits / mined /
    gap_reason / filtered_evidence 字段；
  新增请求参数 { "force_full": true } 强制走慢路径（调试与对照评估用）。
POST /answer（既有，见 answer 模块）
  响应增加 path_type 字段（Page 显示路径标识用）。
```

## LLM 调用预算对照

```text
慢路径（MVP + 挖掘）：domain(1) + source(1) + outline(0~N) + rerank(1)
                      + mining(1) + answer(1)               ≈ 5~6 次
快路径：              mining(1) + verify(1) + answer(1)      = 3 次
Wiki 直答：           answer(1~wiki_max_candidates)          典型 1 次，最坏 3 次
                      （sufficient=false 换下一候选页重试，见 wiki.md 步骤 4）
```

## 依赖

```text
Activation：Match 匹配器、TouchLastUsed
Evidence：  Mine 接口（见 evidence.md）
Lifecycle： Bleve lifecycle 字段与 SQL 过滤条件
Wiki：      wiki index 查询与直答路径（见 wiki.md；未实现时第 0 层跳过）
Session / Answer / Trace：契约扩展见各自文档，链路顺序不变
Study：      gap_reason / filtered_evidence 消费方，见 study.md「knowledge_gaps 表扩展」
```

## 完成标准

```text
verified 链接存在时同类问题走快路径，LLM 调用 ≤ 3 次（日志可验证）；
四元组任一维度不等时不走快路径（行为与无链接一致）；
无链接命中时行为与 MVP 慢路径一致（加 lifecycle 过滤与挖掘）；
fast_path=false 时全部走慢路径但 activation_hits 照常记录；
命中 >1 条不同链接时不走快路径、回落慢路径，activation_hits 仍记录
  全部命中链接，且不触发 TouchLastUsed；
force_full=true 强制慢路径生效；
fast_path_verify=true 时校验不通过自动回落慢路径且只回落一次，
  trace 记录 path_type=full、activation_hits 保留（产生 activation_failure）；
fast_path_verify=false 时跳过校验，行为同旧版快路径；
slow_path_verify=true 时 Answer 在生成前校验不通过则拒答（Path=none、
  HasAnswer=false），EvidenceSet 保留供审计；校验调用失败则放行生成；
slow_path_verify=false 时跳过，慢路径行为与改前一致；
快路径回答失败自动回落慢路径且只回落一次，trace 记录 path_type=full；
superseded/deprecated 的 KU/KP 不出现在任何路径的候选与证据中；
EvidenceSet 的 path_type / activation_hits / mined 正确传递至
  evidence_snapshot 与 Trace；
RRF merge 为空时 gap_reason="no_candidates"，filtered_evidence 为空；
rerank 后 direct/supporting 均为空时 gap_reason="judge_filtered"，
  filtered_evidence 含全部被判 irrelevant 的候选（unit_id/content/source_ref 齐全）；
产出了 direct 或 supporting 时 gap_reason 留空（即使 filtered_evidence 非空）；
对照评估：同一问题集 force_full 与快路径的 direct 命中率对比可产出
  （评估脚本遍历问题集分别请求两种路径，比较 direct_point_ids）；
fake LLM 下快路径、快路径校验不通过回落、慢路径、灰度开关四类场景测试稳定运行。
```
