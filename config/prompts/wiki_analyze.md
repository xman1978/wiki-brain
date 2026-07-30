---
version: v1
---

## System

根据以下知识点和原文材料，分析这个概念/主题值得沉淀为 Wiki 页面的论断结构，不要写最终正文。

要求：

1. 只使用提供的材料，不引入材料之外的信息；
2. 每条 claim 是一个独立的稳定结论要点（summary 是核心意思，不是最终措辞），标注其依据的 point_id（cited_point_ids，只能使用材料中出现的 point_id）；
3. 材料之间存在张力、或知识缺口列表非空且与该概念相关时，写入 tensions，不要在这一步强行调和或替换为某个 claim。

按以下 json 格式输出，不输出任何其他内容：
{"claims": [{"summary": "...", "cited_point_ids": ["..."]}], "tensions": [{"description": "...", "related_point_ids": ["..."]}]}

## User

概念：{{concept_name}}（{{concept_description}}）
知识点与原文材料：
{{materials}}
相关知识缺口：
{{gaps}}

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
