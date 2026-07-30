# 跨 Source KPN 实现路径（V1）

## 职责

MVP 的 KPN 关系只在单 Source 内生成。V1 在新 Source 完成 Unit 提取后，将其 KP 与既有其他 Source 的同类 KP 做批量匹配，建立跨 Source 关系（related / contradicts，与 MVP 内部关系类型一致，见 unit.md 设计决策），提升 KPN 扩展的跨文档覆盖。contradicts 只标记并进入学习报告，不做冲突消解（V3 能力）。

跨 Source 匹配只在同 concept_id 的 KP 之间进行；concept_id 缺失的 KP 不回退到同 domain 全量候选（早期设计曾如此，验收测试发现该回退会在概念颗粒度覆盖不到的领域——尤其宽泛制度类文本——产出大量表面同域实则无关的 related 关系，人工合理率远低于要求）。这批 KP 直接转入**概念演化模块**（`docs/impl/v1/concept-evolution.md`，V1 实现顺序第 10 位，已先于 KPN 完整实现）已有的 `concept_candidates(kind=add)` 候选与确认机制，本轮暂不建立跨 Source 关系，待概念补齐后定向重新匹配。KPN 模块不新建任何独立表，只是 `concept_candidates` 的另一个候选生产者。

## 数据结构

`knowledge_point_relations` 表结构天然支持跨 Source（两端 point_id 无同源约束），扩展两处（migration 版本号顺延）：

```sql
ALTER TABLE knowledge_point_relations ADD COLUMN scope TEXT NOT NULL DEFAULT 'intra';
-- intra（单 Source 内，存量默认）/ cross（跨 Source）

CREATE UNIQUE INDEX idx_kp_relations_uniq
  ON knowledge_point_relations(source_point_id, target_point_id, relation_type);
-- 防重复写入；migration 前先清理存量重复行（保留 created_at 最早者）
```

`concept_candidates`（`concepts`/`concept_candidates` 两表）沿用 `docs/impl/v1/concept-evolution.md` 的既有结构，本文档不新增字段，仅约定 `evidence` JSON 增加一个通用键：

```text
origin: "usage_driven"（概念演化模块自身基于 activation_gap 的候选，既有行为）
      | "content_driven"（本文档：KPN 跨 Source 匹配时，对 concept_id 为空的
        KP 按 domain 聚类直接产生的候选，不依赖任何查询信号）
```

两种来源共用同一张表、同一套 `GET/POST /concepts/candidates*` 确认流程，`origin` 只用于审计展示，不改变确认执行逻辑。

## 实现步骤

### 步骤 1：触发时机

在 `unit_extract` 任务的 Concept 批量匹配（MVP 步骤 5）完成后追加执行，仍在同一任务 goroutine 内顺序运行：

```text
Step 1~5  MVP 既有流程（切块 → 提取 → KPN(intra) → Concept 匹配）
Step 6    跨 Source KPN 匹配（本文档步骤 2、4~7）
Step 7    content_driven 概念候选生成（本文档步骤 3，调用概念演化模块）
```

失败隔离沿用既有原则：本步骤失败记录 warn，不影响 Source 完成状态。

### 步骤 2：候选配对

```text
新 KP 集合：当前 source 下 status=completed 的 KU 的全部 KP
            （lifecycle=current）；

对新 KP 按其所属 KU 的 concept_id 分流：
  concept_id 非空 -> 参与本轮跨 Source 匹配（步骤 4~5），
    对端 KP 集合 = 同 concept_id 且 lifecycle=current 且
    source_id != 当前的 KP；
  concept_id 为空 -> 不参与本轮匹配，转入步骤 3
    （按 domain_id 聚类，交给概念演化模块生成/合并 content_driven 候选）；
  source 无 domain（domain_id 为空）-> 两条路径都不适用，
    跳过该组新 KP，记录 debug。

规模控制（仅对 concept_id 非空的匹配批次生效）：
  对端集合按 concept 分组，每组与对应新 KP 组成一个匹配批次；
  单批（新 KP + 对端 KP）合计 ≤ 60 个，超出时对端按
  question_kp_cooccurrence.confident_count 降序截取（优先与被验证过
  的知识建立关系），仍超则硬切多批；
  单个 source 的匹配批次总数上限 kpn.cross_max_batches（默认 20），
  超出记录 warn 后放弃剩余（防大库导入时调用爆炸）。

自体祖先排除（Wiki 草稿回流防护，见 wiki.md 步骤 10「回流的自体循环」）：
  当前 source 的 origin='wiki_draft' 时，从对端 KP 集合中剔除
  sources.origin_page_id 指向的那个 Wiki 页面已引用过的 point_id
  （该页面当前 source_point_ids ∪ 其历史 wiki_revisions 引用过的
  point_id）——这些不是"另一份知识"，是同一份知识的复印件，
  与之建立 related 关系会虚增关系边、虚增 qualifying 计数，
  反过来把同一批知识推成新的主题页 / 重编译候选（自指正反馈环）；
  剔除只作用于这一对来源关系：回流 KP 与**其他**知识照常匹配、
  照常可成为 qualifying KP，否则回流就失去意义；
  被剔除的对端数量计入 warn 日志与学习报告的 wiki_draft_reflow 项。
```

### 步骤 3：content_driven 概念候选

```text
输入：本次导入中 concept_id 为空、且 source 有 domain_id 的 KU 集合
      （步骤 2 分流出的部分）。

按 domain_id 分组，每组一次轻量 LLM 调用（类似 unit_concept_match，
但不是从既有概念列表选，而是对这组内容提出建议概念名与边界描述），
再调用概念演化模块的候选写入接口：

  同 domain 下已存在 status=pending_confirm 的 kind=add 候选
    -> 合并：point_ids 并入该候选，suggested_name 保留原值不因新
       一批内容漂移改名，evidence 追加本次涉及的 source_id，
       last_signal_at/updated_at 刷新（不重复建行，呼应
       concept-evolution.md 步骤 2"同簇已有 pending_confirm 候选
       更新，不重复建行"的既有原则，扩展到 content_driven 来源）；
  否则 -> 新建候选（kind=add, evidence.origin=content_driven）。

这批 KP 本轮不建立任何跨 Source 关系，记录 info 日志（跳过 KP 数、
涉及候选数）；LLM 调用失败按既有失败隔离原则处理，记录 warn，
不阻塞 Source 完成，这批 KP 保持未处理，下次该 domain 有新导入时
会被重新尝试聚类。
```

**调用参数**：extraction 模型，temperature 0。Prompt 文件：`config/prompts/kpn_concept_propose.md`（结构类似 `unit_concept_match.md`，输出 `{"suggested_name","suggested_description"}`）。

**模块边界**：本步骤只负责聚类、命名和写入候选，不做任何概念确认或 KU 迁移——那部分完全复用概念演化模块既有的 `Service.Confirm`/`Reject`（`docs/impl/v1/concept-evolution.md` 步骤 3），KPN 模块不重复实现。

### 步骤 4：跨 Source 匹配 Prompt

Prompt 文件：`config/prompts/kpn_cross_match.md`

```
以下是两组知识点。A 组来自新导入的材料，B 组来自知识库中已有的其他材料。
找出 A 组与 B 组知识点之间的语义连接（只找 A-B 之间，不找组内连接）。

关系类型（与 MVP 内部关系保持一致，仅 2 种，见 unit.md 设计决策）：
- related：主题相关、互补、依赖或层级关系（双向）
- contradicts：两者存在约束冲突（双向）

原则：
- 只建立有明确依据的关系，不推测；
- 语义等价（同一知识的不同表述）用 related，不合并知识点；
- 关系总数不超过 A 组知识点数的 2 倍。

A 组（格式：point_id TAB unit_center TAB content）：
{{new_points}}

B 组（同格式）：
{{existing_points}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

示例 JSON 与校验规则同 MVP `kpn_extract.md`：`{"relations": [{"from","to","type"}]}`；程序校验 from ∈ A 组、to ∈ B 组（方向约束：跨 Source 关系统一以新 KP 为 from）、from != to、type ∈ 2 种枚举（related / contradicts）。

**调用参数**：extraction 模型，temperature 0。

### 步骤 5：写入与去重

```text
direction 恒为 bidirectional（related / contradicts 均为双向关系，与 MVP 内部关系一致）；
INSERT OR IGNORE（撞 idx_kp_relations_uniq 即为已存在，跳过）；
scope='cross'；
写入计数记录 info 日志（新增关系数、contradicts 数）。
```

### 步骤 6：概念候选确认后的定向重新匹配

概念演化模块的 `POST /concepts/candidates/:id/confirm` 对 `kind=add` 候选执行确认（新建概念，或归入已有概念——见下）后，其涉及的 point_id 对应 KU 已获得 `concept_id`。确认成功（事务提交）之后，同步追加一次**只针对这批 point_id** 的跨 Source 匹配：候选池此时直接走步骤 2 的"同 concept_id"路径查询对端（按 point_id 所属 source_id 分别排除自身），复用步骤 4~5 的匹配与写入逻辑，不再需要任何回退。这一步由概念演化模块通过一个通知接口回调 KPN 模块执行（避免概念演化模块反向依赖 KPN/Unit 包），失败记录 warn，不影响概念确认本身已提交的结果。

`POST /concepts/candidates/:id/confirm` 的 `kind=add` 确认支持两种执行方式，均触发上述定向重新匹配：

```text
新建概念（既有行为）：body 可覆盖 suggested_name/domain_id，事务内
  INSERT concepts(origin='evolved')，point_ids 对应 KU 迁移到新 concept_id；

归入已有概念（本文档新增）：body 改传 concept_id，事务内跳过创建
  概念，直接把 point_ids 对应 KU 的 concept_id 迁移为该已有值——用于
  修正 unit_concept_match 漏判导致的误判空概念场景，避免概念表因
  content_driven 候选而不必要地膨胀；concept_id 必须存在且未被合并
  （merged_into IS NULL），否则拒绝执行。
```

### 步骤 7：contradicts 进入学习报告 / 检索侧生效

Study 报告（study.md 步骤 7）新增一节，数据来源为只读查询：

```json
"cross_source_conflicts": [
  { "relation_id": "...", "point_a": {"point_id","content","source_title"},
    "point_b": {"point_id","content","source_title"}, "created_at": "..." }
]
```

```text
SELECT ... FROM knowledge_point_relations
WHERE relation_type='contradicts' AND scope='cross'
ORDER BY created_at DESC LIMIT 20
```

仅展示提示，无任何自动动作。`docs/impl/v1/concept-evolution.md` 步骤 5 的报告 `concept_candidates` 节自然覆盖 content_driven 候选（evidence.origin 区分展示），不需要额外的报告字段。

检索侧无需改动：MVP 的 KPN 扩展（retrieval 步骤 8）按 point_id 查 `knowledge_point_relations`，跨 Source 关系自然参与扩展；lifecycle 过滤已由 retrieval.md（V1）步骤 5 覆盖。cross 关系与 intra 关系类型一致（related / contradicts，均 bidirectional），扩展规则沿用 MVP：related 走 GetKPNNeighbors 补充 supporting，contradicts 走 GetKPNConflicts 单独写入 EvidenceSet.conflicts。

### 步骤 8：HTTP API

```text
GET /points/:id/relations（既有）
  响应增加 scope 字段；
  新增查询参数 scope（intra / cross 过滤）。

POST /sources/:id/kpn-cross
  手动补触发指定 source 的跨 Source 匹配；用于对存量 Source 回填
  跨源关系。仅处理 concept_id 非空的 KP（步骤 2、4~5、7）；concept_id
  为空的 KP 按步骤 3 逻辑生成/合并 content_driven 概念候选。
  响应：{ source_id, batches: N, relations_created: M,
  concept_candidates_touched: P }

GET/POST /concepts/candidates*（既有，概念演化模块）
  不新增路由；POST .../confirm 的 body 新增 concept_id 字段（步骤 6
  "归入已有概念"路径），与既有 suggested_name/domain_id/target 字段
  互斥使用（kind=add 时：传 concept_id 走归入已有分支，否则走新建
  分支）。
```

**重复触发的语义**：`idx_kp_relations_uniq` 只保证不写入重复行（同一 from/to/type 组合不会插入两次），不保证两次触发得到相同的关系集合。每次触发都是一次新的 LLM 判断调用（步骤 4），同样的候选配对两次调用可能因采样波动给出不同的 related/contradicts 判断，属该步骤依赖 LLM 主观判断的固有属性，不是实现缺陷。因此重复触发是安全的（不产生脏数据），但不是严格幂等——不应假设一次触发就覆盖了当次候选空间的全部配对，也不应假设多次触发的结果集合收敛到固定值。如需提高覆盖率，可多次触发；不追求靠重试把它变成确定性过程。

## 依赖

```text
基础设施：SQLite（LLM client；无新增 migration，复用 concept_candidates）
Unit：    unit_extract 任务链追加 Step 6~7；复用 KPN 写入与校验代码
概念演化：content_driven 候选写入、确认（新建/归入已有）与定向重新匹配的
          回调通知，均通过概念演化模块既有/新增的接口对接，KPN 模块不
          直接操作 concept_candidates 表
Lifecycle：候选配对过滤 lifecycle=current
Trace：   question_kp_cooccurrence 用于对端截取排序（只读）
Study：   报告新增 cross_source_conflicts 节（只读查询）；
          concept_candidates 节自然覆盖 content_driven 候选
```

## 完成标准

```text
新 Source 提取完成后自动执行跨源匹配，失败不影响 Source 状态；
关系写入 scope=cross，from 恒为新 KP，type 限 2 种枚举（related / contradicts）；
重复触发（POST /sources/:id/kpn-cross）不产生重复关系，但不保证结果集合
  与前次一致（步骤 8 已注明：受 LLM 判断调用采样波动影响，非严格幂等）；
对端候选严格按 concept_id 精确匹配，不回退同 domain 全量候选；
concept_id 为空的 KP 转入 concept_candidates(kind=add, evidence.origin=
  content_driven)，不建立任何跨 Source 关系；
同 domain 内容语义相近的多次导入合并进同一 pending_confirm 候选，不重复建行；
confirm（新建或归入已有）后受影响 KU 的 concept_id 正确更新，且触发定向
  重新匹配、产出与直接同 concept 匹配等价的关系；
reject 后对应 point_id 保持无跨 Source 关系，不再被重复聚类；
contradicts 关系出现在下一期学习报告的 cross_source_conflicts 节；
KPN 扩展可经跨源关系召回其他 Source 的 supporting 证据（集成测试：
  A Source 的 direct 证据经 cross 关系扩展出 B Source 的邻居 KU）；
非 current 的 KP 不参与配对；
fake LLM 下匹配、去重、回填、content_driven 候选生成与确认路径测试稳定运行。
```
