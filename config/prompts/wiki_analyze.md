---
version: v3
---

## System

根据以下按 Core / Context / Conflict 分组的知识点，提炼这个{{entry_kind_label}}的稳定结论与待验证的张力。不要写正文。

- Core 是本页应该讲的核心内容（如果材料标注了"父概念背景"，说明这条知识点借自父概念，属于背景而非本页独有结论，写结论时要能看出这层区别）；
- Context 是与 Core 有 related 关系的相关背景，可以用来充实说明，但不是本页的核心结论来源；
- Conflict 是与 Core 有 contradicts 关系的矛盾/例外，材料间的矛盾以及与本{{entry_kind_label}}相关的知识缺口，写入 tensions，不要在这一步强行调和。

要求：

1. 只使用提供的材料，不引入材料之外的信息；
2. 结论（claim）应主要基于 Core 材料；引用 Context/Conflict 中的 point_id 时同样合法（citation 白名单覆盖 Core/Context/Conflict 全部知识点），但不要把 Context/Conflict 材料当成本页的核心结论主体；
3. 同一事实有多个来源支持时合并成一条结论并列出全部 point_id，不要按来源分开罗列（"某某文档说…"这种写法不符合本页面的定位）；
4. {{entry_kind_hint}}

按以下 json 格式输出，不输出任何其他内容：
{"claims": [{"summary": "...", "cited_point_ids": ["..."]}], "tensions": [{"description": "...", "related_point_ids": ["..."]}]}

## User

{{entry_kind_label}}：{{entry_name}}（{{entry_description}}），所属领域：{{domain_name}}

Core（本页核心）：
{{core_material}}

Context（相关背景，一跳 related）：
{{context_material}}

Conflict（矛盾/例外，一跳 contradicts）：
{{conflict_material}}

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
