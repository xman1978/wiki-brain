---
version: v1
---

## System

根据以下已确认的论断结构和成员概念页面正文，把它组织成一个主题的 Wiki 页面正文。这是二阶编译：只重组已发布概念页已经确认的结论，不新增事实、不引用成员页面之外的 point_id。

要求：

1. 只使用提供的成员页面内容与已确认的论断结构，不引入材料之外的信息，不得引用 claims / tensions 之外的 point_id；
2. 页面结构固定为五节：## 主题概览 / ## 主线结论 / ## 子主题分工 / ## 跨主题矛盾与待验证点 / ## 依赖页面；
3. 主线结论逐条对应输入的 claims，每条论断末尾以 [point_id] 标注该 claim 已确认的 cited_point_ids；
4. tensions 非空时写入"跨主题矛盾与待验证点"，不要强行调和；
5. "子主题分工"逐个说明每个成员页面在本主题里承担什么面向，并给出成员页面标题；
6. "依赖页面"列出全部成员页面标题；
7. 额外输出检索触发信息：aliases（该主题的别名、缩写、常见口语叫法）与 trigger_questions（这个页面能够直接回答的 5-10 个典型问法，从成员页面 trigger_questions 中挑选/归纳，不要臆造）；
8. 额外输出 member_roles——与"子主题分工"一节同源的结构化版本，每个成员一条：{"member_page_id": "...", "aspect": "该成员在本主题里承担的面向", "question_types": ["该成员能回答的问题类型", "2-4 条"]}。

按以下 json 格式输出，不输出任何其他内容：
{"title": "页面标题", "content": "Markdown 正文", "cited_point_ids": ["..."], "aliases": ["..."], "trigger_questions": ["..."], "member_roles": [{"member_page_id": "...", "aspect": "...", "question_types": ["..."]}]}

## User

已确认的论断结构（claims）：
{{claims}}
已确认的张力/待验证点（tensions）：
{{tensions}}
成员页面 id 与标题对照：
{{member_pages}}
成员概念页面正文：
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
    },
    "member_roles": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["member_page_id", "aspect"],
        "properties": {
          "member_page_id": { "type": "string" },
          "aspect": { "type": "string" },
          "question_types": {
            "type": "array",
            "items": { "type": "string" }
          }
        }
      }
    }
  }
}
```
