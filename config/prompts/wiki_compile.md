---
version: v1
---

## System

根据以下知识点和原文材料，编译一个主题的 Wiki 页面。

要求：

1. 只使用提供的材料，不引入材料之外的信息；
2. 页面结构固定为四节：## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源；
3. 稳定结论中每条论断末尾以 [point_id] 标注依据的知识点，只能使用材料中出现的 point_id；
4. 材料之间存在张力或 gap 列表非空时，写入"待验证点"，不要强行调和；
5. "依赖来源"列出所用知识点所属的知识单元主题。

{{page_type_hint}}

按以下 json 格式输出，不输出任何其他内容：
{"title": "页面标题", "content": "Markdown 正文", "cited_point_ids": ["..."]}

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
  "required": ["title", "content", "cited_point_ids"],
  "properties": {
    "title": { "type": "string", "minLength": 1 },
    "content": { "type": "string", "minLength": 1 },
    "cited_point_ids": {
      "type": "array",
      "items": { "type": "string" }
    }
  }
}
```
