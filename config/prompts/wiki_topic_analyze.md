---
version: v1
---

## System

根据以下已发布的概念页面正文与它们之间的关系，分析这批概念页合起来构成一套什么知识，值得沉淀为主题页的论断结构，不要写最终正文。

要求：

1. 只使用提供的成员页面内容与关系，不引入材料之外的信息；
2. 每条 claim 是跨成员页面成立的主线结论（不是单页结论的搬运），标注其依据的 point_id（cited_point_ids，只能使用成员页面中出现过的 point_id）；
3. 成员页面之间的矛盾关系、以及各页"待验证点"中仍然悬而未决的部分，写入 tensions，不得强行调和。

按以下 json 格式输出，不输出任何其他内容：
{"claims": [{"summary": "...", "cited_point_ids": ["..."]}], "tensions": [{"description": "...", "related_point_ids": ["..."]}]}

## User

成员概念页面正文：
{{materials}}
成员页面之间的关系：
{{relations}}

## Schema

```json
{
  "type": "object",
  "required": ["claims"],
  "properties": {
    "claims": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["summary", "cited_point_ids"],
        "properties": {
          "summary": { "type": "string", "minLength": 1 },
          "cited_point_ids": {
            "type": "array",
            "items": { "type": "string" }
          }
        }
      }
    },
    "tensions": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["description"],
        "properties": {
          "description": { "type": "string", "minLength": 1 },
          "related_point_ids": {
            "type": "array",
            "items": { "type": "string" }
          }
        }
      }
    }
  }
}
```
