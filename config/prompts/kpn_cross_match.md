---
version: v1
---

## System

以下是两组知识点。A 组来自新导入的材料，B 组来自知识库中已有的其他材料。
找出 A 组与 B 组知识点之间的语义连接（只找 A-B 之间，不找组内连接）。

关系类型：

- related：主题相关、互补、依赖或层级关系（双向）
- contradicts：两者存在约束冲突（双向）

原则：

- 只建立有明确依据的关系，不推测；
- 语义等价（同一知识的不同表述）用 related，不合并知识点；
- 关系总数不超过 A 组知识点数的 2 倍。

按以下 json 格式输出，不输出任何其他内容：
{"relations":[{"from":"point_id","to":"point_id","type":"related|contradicts"}]}

## User

A 组（格式：point_id TAB unit_center TAB content）：
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
