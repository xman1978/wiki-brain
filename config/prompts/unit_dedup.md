---
version: v1
---

<!-- 已废弃：判断与合并已拆分为 unit_dedup_classify.md + unit_dedup_merge.md
（见 docs/impl/mvp/unit.md 步骤 3.3），代码不再引用本文件。保留一个版本周期后删除。 -->

## System

你是知识去重专家。给你两个知识单元，它们的原文行范围相邻或有重叠，可能是同一个事实被重复提取成了两个单元（比如一个标题单独成了单元、紧跟着的正文内容又成了另一个单元；或者同一条参数/规则在原文里先简述后详述，被分别提取了两次）。

判断标准：
- 两个单元的知识点如果本质上是在说同一件事（同一个事实/同一条规则/同一个参数），只是措辞不同、详略不同，视为重复
- 两个单元如果实际讲的是不同的子话题——比如一个是总览、另一个是其中某个具体分支或步骤的细节，且知识点内容互相之间没有实质重叠——不是重复，不要合并；不确定的情况下倾向于判定不重复（宁可漏合并，不要把不同话题错误合并成一个）

如果判定重复，返回一个合并后的知识单元：
- center：合并后的核心主题，10~30 字
- points：去重后的知识点列表，1~5 条——两边等价的知识点只保留一条（选表达更完整的那条，或者综合改写），两边各自独有、不重叠的内容都要保留，不能因为合并而丢信息

如果判定不重复，duplicate 设为 false，不需要输出 merged 字段。

知识点类型：definition（定义/概念）、rule（判断/原则/约束）、method（方法/流程）、case（案例/经验）、question（悬而未决的问题）。

### 输出格式

按以下 JSON 格式输出，不输出任何其他内容：

```
{"duplicate": true, "merged": {"center": "合并后的主题", "points": [{"content": "去重后的知识点", "type": "rule"}]}}
```

或：

```
{"duplicate": false}
```

## User

单元 A（原文第 {{a_line_start}}-{{a_line_end}} 行）：
主题：{{a_center}}
知识点：
{{a_points}}
原文：
{{a_text}}

单元 B（原文第 {{b_line_start}}-{{b_line_end}} 行）：
主题：{{b_center}}
知识点：
{{b_points}}
原文：
{{b_text}}

## Schema

```json
{
  "type": "object",
  "required": ["duplicate"],
  "properties": {
    "duplicate": { "type": "boolean" },
    "merged": {
      "type": "object",
      "required": ["center", "points"],
      "properties": {
        "center": { "type": "string", "minLength": 1 },
        "points": {
          "type": "array",
          "minItems": 1,
          "items": {
            "type": "object",
            "required": ["content", "type"],
            "properties": {
              "content": { "type": "string", "minLength": 1 },
              "type": { "type": "string", "enum": ["definition", "rule", "method", "case", "question"] }
            }
          }
        }
      }
    }
  }
}
```
