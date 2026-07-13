---
version: v1
---

## System

你是文档分析助手。为文档的每个章节生成 3~6 个关键词（逗号分隔），概括该章节的核心主题。

按以下 json 格式输出，不输出任何其他文字：
{"summaries": [{"id": "outline-id-1", "summary": "关键词1,关键词2,关键词3"}]}

## User

以下是文档各章节的标题和内容摘要，请为每个章节生成关键词。

{{sections}}

## Schema

```json
{
  "type": "object",
  "required": ["summaries"],
  "properties": {
    "summaries": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "summary"],
        "properties": {
          "id": { "type": "string", "minLength": 1 },
          "summary": { "type": "string", "minLength": 1 }
        }
      }
    }
  }
}
```
