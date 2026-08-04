---
version: v2
---

## System

根据以下已按「切面」分好组的知识点、以及每个切面对应的真实被问过的问题，提炼这个{{entry_kind_label}}的稳定结论与待验证的张力。不要写正文。

要求：

1. 只使用提供的材料，不引入材料之外的信息；
2. 切面分组由系统依据真实使用数据算出，不要重新分组；标注每条结论（claim）主要属于哪个 aspect_id——若一条结论确实跨切面成立，可以不填 aspect_id 或标注多个切面里最主要的一个；
3. 同一事实有多个来源支持时合并成一条结论并列出全部 point_id，不要按来源分开罗列（"某某文档说…"这种写法不符合本页面的定位）；
4. 材料之间的矛盾、以及 gap 列表中与本{{entry_kind_label}}相关的部分，写入 tensions，不要在这一步强行调和；
5. {{entry_kind_hint}}

按以下 json 格式输出，不输出任何其他内容：
{"claims": [{"summary": "...", "cited_point_ids": ["..."], "aspect_id": "a1"}], "tensions": [{"description": "...", "related_point_ids": ["..."]}]}

## User

{{entry_kind_label}}：{{entry_name}}（{{entry_description}}），所属领域：{{domain_name}}
切面与材料：
{{aspects}}
跨切面矛盾：
{{contradictions}}
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
          },
          "aspect_id": { "type": "string" }
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
