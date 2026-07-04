# Retrieval 实现路径（V1）

## 职责

在 MVP 完整链路之上增加两个前置命中层与统一的生命周期过滤：

```text
Wiki 直答层   已发布 Wiki 页面命中 → 直接基于页面回答（见 wiki.md）
激活层        verified ActivationLink 命中 → 跳过过滤与召回，直达证据（快路径）
完整链路      未命中时回落 MVP 链路：Domain → Source → Outline/FTS → RRF → Rerank（慢路径）
```

所有路径在 Rerank（或快路径的证据组装）之后统一经过证据挖掘（见 `evidence.md`），再进入充分性判断与 EvidenceSet 构建。

目标：熟悉问题的 LLM 调用从 ≥4 次降至 1-2 次，且 direct 命中率不低于完整链路。

## 检索总流程（V1）

```text
问题（经 Session 补全，沿用 MVP）
  ├─ 第 0 层：Wiki 索引查询（不调 LLM）
  │    命中分 ≥ wiki_min_score → Wiki 直答路径（path_type=wiki，见 wiki.md）
  ├─ 第 1 层：激活层 Match（不调 LLM，见 activation.md 步骤 2）
  │    命中 → 快路径：
  │      链接目标 KP → 反查 KU（lifecycle=current）→ 直接构建 direct 候选
  │      → KPN 扩展补充 supporting（沿用 MVP 步骤 8）
  │      → 证据挖掘（1 次 LLM）→ 充分性判断 → EvidenceSet(path_type=fast)
  │      → Answer（1 次 LLM）                          共计 2 次 LLM 调用
  └─ 未命中 → 慢路径（MVP 完整链路）：
       Domain 预过滤 → Source 过滤 → Outline/FTS 召回 → RRF → Rerank
       → KPN 扩展 → 证据挖掘 → 充分性判断 → EvidenceSet(path_type=full)
       → Answer
```

## 配置项（config.yml: retrieval 节扩展）

```yaml
retrieval:
  # —— MVP 既有配置保留 ——
  outline_fts_min_score: 0.5
  rerank_top_n:          20
  # —— V1 新增 ——
  fast_path:              true   # false 时激活层只记录命中、不走快路径（灰度开关）
  activation_match_min:           0.7   # 四元组匹配计分阈值（见 activation.md 步骤 2）
  activation_match_min_fallback:  0.85  # 四元组缺失时的回退匹配阈值
  activation_match_top:           5     # 激活层最多采用的链接数
  fast_path_fallback:     true   # 快路径回答失败时自动回落慢路径
  wiki_min_score:         2.0    # Wiki 直答的 Bleve 最低分（BM25，需评估集校准）
```

## 实现步骤

### 步骤 1：EvidenceSet 契约扩展

```text
EvidenceSet 新增：
  path_type          fast / full / wiki
  activation_hits[]  [{ link_id, point_id, match_score }]

EvidenceItem 新增：
  mined              bool（证据挖掘产出，见 evidence.md）
```

`path`（short/deep）、`fact_id`、`source_ref` 等既有字段语义不变。Answer 层无需改动：仍按 path 分发、按 fact_id 校验 citation；evidence_snapshot 自然携带新字段。

### 步骤 2：激活层（快路径证据构建）

```text
1. 调 activation.Match(expandedQuery) 得 LinkMatch 列表（≤ activation_match_top）；
   输入是 Session 产出的完整 ExpandedQuery（expanded_question + 四元组），
   不是用户原始输入——匹配含 audience / constraint 硬性守门与
   subject / intent 计分，规则见 activation.md 步骤 2；
   为空 → 慢路径；fast_path=false → 记录命中日志后仍走慢路径
   （activation_hits 照常写入 EvidenceSet，供灰度期观察命中质量）；

2. 取命中链接的 point_id → 反查所属 KU：
     SELECT ... FROM knowledge_points p JOIN knowledge_units u ...
     WHERE p.point_id IN (?) AND p.lifecycle='current' AND u.lifecycle='current'
   全部反查为空（理论上不发生，Match 已过滤）→ 慢路径；

3. 按 unit_id 去重构建候选，role=direct，point_id=命中链接的 point_id，
   content 按 markdown_path + line_start/line_end 切片（沿用 MVP 规则）；
   跳过 Rerank——verified 链接的语义即"该 KP 历史上被反复确认为直接证据"，
   快路径的正确性由 Trace 的 activation_failure 事件事后校验，
   而不是每次用 LLM 复核（否则调用次数目标无法达成）；

4. KPN 扩展沿用 MVP 步骤 8（对 direct KU 查邻居 KP，role=supporting，
   邻居 KP 及其 KU 均要求 lifecycle=current）；

5. 异步 TouchLastUsed(命中 link_ids)，不阻塞请求。
```

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

### 步骤 6：快路径回落

```text
触发条件（fast_path_fallback=true）：
  a. 步骤 4 判定快路径失败；
  b. Answer 完成后 has_answer=false 或 citations 为空
     （Answer 层回调 Retrieval 提供的 fallback 句柄）；

回落执行：以原问题走一遍慢路径并重新生成回答，最终返回慢路径结果；
  本次 trace 记录 path_type=full，activation_hits 保留原命中
  （Trace 据此产生 activation_failure，见 trace.md 步骤 3——
   命中的 KP 不在最终 direct_point_ids 中即判 failure）；
回落只执行一次，慢路径结果不再回落；
回落发生记录 warn 日志（link_ids、原因），供灰度期监控快路径质量。
```

### 步骤 7：HTTP API

```text
POST /retrieval（既有）
  响应 EvidenceSet 增加 path_type / activation_hits / mined 字段；
  新增请求参数 { "force_full": true } 强制走慢路径（调试与对照评估用）。
POST /answer（既有，见 answer 模块）
  响应增加 path_type 字段（Page 显示路径标识用）。
```

## LLM 调用预算对照

```text
慢路径（MVP + 挖掘）：domain(1) + source(1) + outline(0~N) + rerank(1)
                      + mining(1) + answer(1)      ≈ 5~6 次
快路径：              mining(1) + answer(1)         = 2 次
Wiki 直答：           answer(1)                     = 1 次
```

## 依赖

```text
Activation：Match 匹配器、TouchLastUsed
Evidence：  Mine 接口（见 evidence.md）
Lifecycle： Bleve lifecycle 字段与 SQL 过滤条件
Wiki：      wiki index 查询与直答路径（见 wiki.md；未实现时第 0 层跳过）
Session / Answer / Trace：契约扩展见各自文档，链路顺序不变
```

## 完成标准

```text
verified 链接存在时同类问题走快路径，LLM 调用 ≤ 2 次（日志可验证）；
无链接命中时行为与 MVP 慢路径一致（加 lifecycle 过滤与挖掘）；
fast_path=false 时全部走慢路径但 activation_hits 照常记录；
force_full=true 强制慢路径生效；
快路径回答失败自动回落慢路径且只回落一次，trace 记录 path_type=full；
superseded/deprecated 的 KU/KP 不出现在任何路径的候选与证据中；
EvidenceSet 的 path_type / activation_hits / mined 正确传递至
  evidence_snapshot 与 Trace；
对照评估：同一问题集 force_full 与快路径的 direct 命中率对比可产出
  （评估脚本遍历问题集分别请求两种路径，比较 direct_point_ids）；
fake LLM 下快路径、慢路径、回落、灰度开关四类场景测试稳定运行。
```
