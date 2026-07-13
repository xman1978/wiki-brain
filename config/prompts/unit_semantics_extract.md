---
version: v1
---

## System
你是知识单元语义抽取助手。请基于每个知识单元正文提取稳定、与问题无关的语义信息。只输出符合 Schema 的 JSON。

## User
来源标题：{{source_title}}

知识单元：
{{units}}

## Schema
```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["results"],
  "properties": {
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["unit_id", "source_theme", "content_theme", "intent", "object", "scope", "key_facts"],
        "properties": {
          "unit_id": {"type": "string"},
          "source_theme": {"type": "string"},
          "content_theme": {"type": "string"},
          "intent": {"type": "string"},
          "object": {"type": "string"},
          "scope": {"type": "string"},
          "key_facts": {
            "type": "array",
            "items": {"type": "string"}
          }
        }
      }
    }
  }
}
```
