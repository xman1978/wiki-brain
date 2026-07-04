# 跨 Source KPN 实现路径（V1）

## 职责

MVP 的 KPN 关系只在单 Source 内生成。V1 在新 Source 完成 Unit 提取后，将其 KP 与既有其他 Source 的同类 KP 做批量匹配，建立跨 Source 关系（related / contradicts，与 MVP 内部关系类型一致，见 unit.md 设计决策），提升 KPN 扩展的跨文档覆盖。contradicts 只标记并进入学习报告，不做冲突消解（V2 能力）。

## 数据结构

`knowledge_point_relations` 表结构天然支持跨 Source（两端 point_id 无同源约束），扩展两处（migration 版本号顺延）：

```sql
ALTER TABLE knowledge_point_relations ADD COLUMN scope TEXT NOT NULL DEFAULT 'intra';
-- intra（单 Source 内，存量默认）/ cross（跨 Source）

CREATE UNIQUE INDEX idx_kp_relations_uniq
  ON knowledge_point_relations(source_point_id, target_point_id, relation_type);
-- 防重复写入；migration 前先清理存量重复行（保留 created_at 最早者）
```

## 实现步骤

### 步骤 1：触发时机

在 `unit_extract` 任务的 Concept 批量匹配（MVP 步骤 5）完成后追加执行，仍在同一任务 goroutine 内顺序运行：

```text
Step 1~5  MVP 既有流程（切块 → 提取 → KPN(intra) → Concept 匹配）
Step 6    跨 Source KPN 匹配（本文档）
```

失败隔离沿用既有原则：本步骤失败记录 warn，不影响 Source 完成状态。

### 步骤 2：候选配对

```text
新 KP 集合：当前 source 下 status=completed 的 KU 的全部 KP
            （lifecycle=current）；

对端 KP 集合，按优先级取（均要求 lifecycle=current 且 source_id != 当前）：
  1. 同 concept_id 的 KP（经 KP → KU → concept_id）；
  2. 新 KP 所属 KU 无 concept_id 时，取同 domain_id 下全部 KP
     （经 KU → source → domain_id）；
  3. 两者皆无（source 无 domain）→ 跳过该组新 KP，记录 debug；

规模控制：
  对端集合按 concept 分组，每组与对应新 KP 组成一个匹配批次；
  单批（新 KP + 对端 KP）合计 ≤ 60 个，超出时对端按
  question_kp_cooccurrence.confident_count 降序截取（优先与被验证过
  的知识建立关系），仍超则硬切多批；
  单个 source 的匹配批次总数上限 kpn.cross_max_batches（默认 20），
  超出记录 warn 后放弃剩余（防大库导入时调用爆炸）。
```

### 步骤 3：跨 Source 匹配 Prompt

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

### 步骤 4：写入与去重

```text
direction 恒为 bidirectional（related / contradicts 均为双向关系，与 MVP 内部关系一致）；
INSERT OR IGNORE（撞 idx_kp_relations_uniq 即为已存在，跳过）；
scope='cross'；
写入计数记录 info 日志（新增关系数、contradicts 数）。
```

### 步骤 5：contradicts 进入学习报告

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

仅展示提示，无任何自动动作。

### 步骤 6：检索侧生效

无需改动：MVP 的 KPN 扩展（retrieval 步骤 8）按 point_id 查 `knowledge_point_relations`，跨 Source 关系自然参与扩展；lifecycle 过滤已由 retrieval.md（V1）步骤 5 覆盖。cross 关系与 intra 关系类型一致（related / contradicts，均 bidirectional），扩展规则沿用 MVP：related 走 GetKPNNeighbors 补充 supporting，contradicts 走 GetKPNConflicts 单独写入 EvidenceSet.conflicts。

### 步骤 7：HTTP API

```text
GET /points/:id/relations（既有）
  响应增加 scope 字段；
  新增查询参数 scope（intra / cross 过滤）。

POST /sources/:id/kpn-cross
  手动补触发指定 source 的跨 Source 匹配（幂等，重复关系被
  idx_kp_relations_uniq 拦截）；用于对存量 Source 回填跨源关系。
  响应：{ source_id, batches: N, relations_created: M }
```

## 依赖

```text
基础设施：SQLite（migration）、LLM client
Unit：    unit_extract 任务链追加 Step 6；复用 KPN 写入与校验代码
Lifecycle：候选配对过滤 lifecycle=current
Trace：   question_kp_cooccurrence 用于对端截取排序（只读）
Study：   报告新增 cross_source_conflicts 节（只读查询）
```

## 完成标准

```text
新 Source 提取完成后自动执行跨源匹配，失败不影响 Source 状态；
关系写入 scope=cross，from 恒为新 KP，type 限 2 种枚举（related / contradicts）；
重复触发（POST /sources/:id/kpn-cross）不产生重复关系；
对端候选按 concept → domain 优先级选取，超限截取与批次上限生效；
contradicts 关系出现在下一期学习报告的 cross_source_conflicts 节；
KPN 扩展可经跨源关系召回其他 Source 的 supporting 证据（集成测试：
  A Source 的 direct 证据经 cross 关系扩展出 B Source 的邻居 KU）；
非 current 的 KP 不参与配对；
fake LLM 下匹配、去重、回填路径测试稳定运行。
```
