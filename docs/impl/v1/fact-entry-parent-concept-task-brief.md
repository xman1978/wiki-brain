# 编码任务指令：持久化事实类词条的父概念（parent_entry_id）

## 背景

事实类词条（`entries.kind='fact'`）在生成时（`kpn.md` 步骤 3「Fact 新建」）已经明确知道自己是从哪个概念类词条（`entries.kind='concept'`）分类而来——`kpn_orphan_fact_match.md` 把 fact leftover KP 匹配到某个 `matched_concept_id`，同一 `(source_id, concept_id)` 分组后再反推 entity、拼出 `suggested_name = entity + concept名`。但这个 `concept_id` 只存在于生成过程的内存中间结构（`internal/unit/kpn_entry_propose.go` 的 `factGroupKey{SourceID, ConceptID}`）里，写入 `entry_candidates` 时只落地了拼接后的名字字符串，转正写入 `entries` 时这层关系已经彻底丢失、之后无法查询。

本任务只做一件事：把生成时已经计算出来、但当前被丢弃的 `concept_id` 持久化下来，命名为 `parent_entry_id`。**不**改变现有分类/命名算法，**不**引入新的推断逻辑，**不**实现基于这个字段的下游消费（子图展开、检索增强等——那是另一个更大的、尚未定案的方向，本任务只打地基）。

## 与设计文档的关系

这是对 `docs/impl/v1/kpn.md` 步骤 3「Fact 新建」与 `docs/impl/v1/concept-evolution.md`「entries 表扩展」的**补充**，不是对已有决策的修改。完成后请把本文件的落地情况回填一句话到 `kpn.md` 步骤 3 末尾（类似其余"2026-08-xx 修订"的记法），指向本文件。

## 实现步骤

### 步骤 1：Migration（foundation）

新增 migration 文件（延续现有编号序列，`internal/foundation/db/migrations/0NN_fact_entry_parent.sql`）：

```sql
ALTER TABLE entries ADD COLUMN parent_entry_id TEXT REFERENCES entries(entry_id);
-- 非空 = 该词条（通常是 fact）生成时归属的 concept 词条；
-- 只在生成时按已知 concept_id 写入一次，不追踪后续变化，
-- 不因目标概念被合并（merged_into）而自动更新或清空。

ALTER TABLE entry_candidates ADD COLUMN parent_entry_id TEXT REFERENCES entries(entry_id);
-- kind=add 且 entry_kind=fact 的候选，写入时携带已匹配到的 concept entry_id；
-- 其余候选（concept 自由聚类、usage_driven 新增、人工手动创建）留 NULL。

CREATE INDEX idx_entries_parent ON entries(parent_entry_id);
```

不需要唯一约束（同一个 concept 下可以有多个 fact 子词条）。不迁移存量数据——历史 fact 词条的父概念关系已在生成时丢失，无法反推，`parent_entry_id` 留空即可，不做补录。

### 步骤 2：生成时写入（`internal/unit/kpn_entry_propose.go`）

`writeFactGroupCandidates`（第 459 行）消费 `matchOrphansToExistingConcepts` 产出的分组（`factGroupKey{SourceID, ConceptID}`），当前只用 `key.ConceptID` 拼 `suggested_name`。改动：写入 `entry_candidates` 行时，把 `key.ConceptID` 一并写入新增的 `parent_entry_id` 列。

不改变分组逻辑、不改变 `joinEntityConcept` 命名规则、不改变"没匹配上任何 concept 的 fact KP 保持 orphan"的既有行为。

### 步骤 3：转正时写入（`internal/entry/store.go`）

`Store.ConfirmAdd`（第 888 行）单事务 `INSERT INTO entries(...)`，改动：新增 SQL 列 `parent_entry_id`，取值来自被确认的 `entry_candidates` 行的 `parent_entry_id`（可为 NULL）。

`entry_add_auto_confirm=true`（默认，见 `concept-evolution.md` 步骤 3）走的是同一条 `ConfirmAdd` 执行路径，无需额外改动即可覆盖自动确认场景。

「归入已有概念」路径（`kpn.md` 步骤 6，`POST /entries/candidates/:id/confirm` 传 `entry_id` 直接迁移 KU 而不新建 entry）不涉及新建 entries 行，不需要处理 `parent_entry_id`——该路径下没有"新词条"这个对象。

### 步骤 4：查询暴露（可选，最小化）

`GET /entries/:id` 或词条详情接口，返回体新增 `parent_entry_id` 字段（原样透出，不做任何聚合或展开）。**不**在这一步实现"列出某概念下所有子事实词条"的反向查询接口——如果需要，等下游消费场景明确后再加，避免为一个还没使用的字段预先造接口。

## 边界（不做的事）

```text
不反向补录存量 fact 词条的 parent_entry_id；
不在概念被合并（merged_into 非空）时级联更新/清空子词条的 parent_entry_id；
不实现基于 parent_entry_id 的子图展开、检索召回、Wiki 编译消费——
  这些是独立方向，需要用户另行确认范围后才排期；
不改变 kind=concept 候选（自由聚类）、usage_driven 新增候选、
  人工手动创建候选（CreateManualCandidate）的行为——这三类候选
  本来就不知道 concept 归属，parent_entry_id 留空是预期行为，
  不需要为它们新增推断逻辑。
```

## 完成标准

```text
migration 正确应用：entries.parent_entry_id / entry_candidates.parent_entry_id
  两列存在，可为 NULL，索引存在；
Fact 候选生成：kpn_orphan_fact_match 匹配到 concept 的分组，其
  entry_candidates 行 parent_entry_id 正确等于该 concept 的 entry_id；
  未匹配到 concept 的 fact KP（保持 orphan）不产生候选，无需验证；
转正后：无论人工 confirm 还是 entry_add_auto_confirm=true 自动执行，
  新建的 fact entries 行 parent_entry_id 与其候选行一致；
  kind=concept 候选转正后 parent_entry_id 恒为 NULL；
「归入已有概念」路径不受影响，不产生新 entries 行；
既有 kpn.md / concept-evolution.md 描述的分类、命名、合并、
  preset UPSERT 规则均不受影响，相关既有测试不因本改动失败；
新增测试覆盖：fact 候选生成携带 parent_entry_id、转正写入正确、
  concept 候选与人工手动创建候选 parent_entry_id 为 NULL。
```
