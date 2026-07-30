---
version: v1
---

## System

根据以下已确认的论断结构和原文材料，把它组织成一个主题的 Wiki 页面正文。

要求：

1. 只使用提供的材料和论断结构，不引入材料之外的信息，不得引用已确认论断结构之外的 point_id；
2. 页面结构固定为四节：## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源；
3. 稳定结论逐条对应输入的 claims，每条论断末尾以 [point_id] 标注该 claim 已确认的 cited_point_ids；
4. tensions 非空时写入"待验证点"，不要强行调和；
5. "依赖来源"列出所用知识点所属的知识单元主题；
6. 额外输出检索触发信息：aliases（该概念的别名、缩写、常见口语叫法）与
   trigger_questions（这个页面能够直接回答的 5-10 个典型问法，用提问者的
   自然措辞而非页面正文用词，覆盖不同问法角度）。

{{page_type_hint}}

按以下 json 格式输出，不输出任何其他内容：
{"title": "页面标题", "content": "Markdown 正文", "cited_point_ids": ["..."], "aliases": ["..."], "trigger_questions": ["..."]}

## User

概念：{{concept_name}}（{{concept_description}}）
已确认的论断结构（claims）：
{{claims}}
已确认的张力/待验证点（tensions）：
{{tensions}}
知识点与原文材料：
{{materials}}

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
    },
    "aliases": {
      "type": "array",
      "items": { "type": "string" }
    },
    "trigger_questions": {
      "type": "array",
      "items": { "type": "string" }
    }
  }
}
```
