---
version: v3
---

## System

以下是两组知识点。A 组来自新导入的材料，B 组来自知识库中已有的其他材料。
找出 A 组与 B 组知识点之间的语义连接（只找 A-B 之间，不找组内连接）。

关系类型：

- related：主题相关、互补、依赖或层级关系（双向）
- contradicts：两者存在约束冲突（双向）

判断前必须先做文档级粗筛（对 related 和 contradicts 都适用，不只是 contradicts）：

- 每条知识点都带有 `source_title`，标识它所属文档/制度的业务领域。建立任何关系前，先判断 A 组这条与 B 组那条各自所属的 `source_title` 是否属于同一业务领域（例如都属于"奖金/薪酬制度"，或都属于"数据库运维"，或都属于"某产品的用户手册"）。
- `source_title` 指向的业务领域明显不同（例如一份是"算力/云资源计费管理办法"，另一份是"员工绩效奖金制度"；或一份是数据库运维手册，另一份是人事考勤制度）时，即使两条知识点的内容里出现相同或相似的表层模式（比如都提到百分比、都提到"奖励""费用""扣除"这类词，或数字量级接近），也不得判定为 related——这些是巧合的字面/数字模式重合，不是业务语义关联，必须不建立关系。
- `source_title` 领域相同或紧密相关（例如同类系统的不同版本/不同厂商实现，如 Oracle 11g 与 Oracle 19g、达梦与金仓数据库、Docker Swarm 与 K8S 的同类操作对照），才允许在此基础上进入下一步的知识点级判断。

只有通过文档级粗筛（同领域）的知识点对，才继续判断：

- 只建立有明确依据的关系，不推测；
- 语义等价（同一知识的不同表述）用 related，不合并知识点；
- 判 contradicts 前必须先确认两条知识点的约束是否针对同一主体/范围（可参考 source_title 判断，如标题指向不同公司/组织的文件）；如果两条知识点分属不同主体，即使数值或规则表面不同，也不构成真实冲突，应判 related（同话题、不同范围）或不建立关系，不能判 contradicts；
- 关系总数不超过 A 组知识点数的 2 倍。

按以下 json 格式输出，不输出任何其他内容：
{"relations":[{"from":"point_id","to":"point_id","type":"related|contradicts"}]}

## User

A 组（格式：point_id TAB source_title TAB unit_center TAB content）：
{{new_points}}

B 组（同格式）：
{{existing_points}}

## Schema

```json
{
  "type": "object",
  "required": ["relations"],
  "properties": {
    "relations": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["from", "to", "type"],
        "properties": {
          "from": { "type": "string", "minLength": 1 },
          "to":   { "type": "string", "minLength": 1 },
          "type": { "type": "string", "enum": ["related", "contradicts"] }
        }
      }
    }
  }
}
```
