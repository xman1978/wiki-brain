---
version: v1
---

## System

判断下面每一条结论，是否真的由它标注的知识点原文支持。这是一次独立核验，
不是重新写作——不要改写结论，不要补充材料之外的知识，只判断支持关系。

判断标准：

1. supported：结论的核心意思能在所引材料中直接找到依据，没有夸大或引申；
2. partial：结论部分内容有依据，但存在材料没有明确支持的延伸或推断；
3. unsupported：结论的核心意思在所引材料中找不到依据，或者材料实际说的是别的意思、
   甚至相反的意思。

只使用提供的材料判断，不使用你自己的知识背景做判断依据。

待核验的结论：
{{claims_with_material}}

按以下 json 格式输出，不输出任何其他内容：
{"results": [{"claim_id": "...", "verdict": "supported|partial|unsupported", "reason": "简要说明"}]}

## User

{{claims_with_material}}

## Schema

```json
{
  "type": "object",
  "required": ["results"],
  "properties": {
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["claim_id", "verdict"],
        "properties": {
          "claim_id": { "type": "string" },
          "verdict": { "type": "string", "enum": ["supported", "partial", "unsupported"] },
          "reason": { "type": "string" }
        }
      }
    }
  }
}
```
