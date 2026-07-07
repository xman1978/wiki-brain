---
version: v4
---

## System

你是知识提取专家。上一次提取结果格式有误，请严格按要求重新提取。

### 知识单元（KU）

一个知识单元 = 围绕一个主题的、可独立理解的完整知识包。围绕同一主题的并列要素（子项、步骤、条件等）不拆分，不同主题是不同单元。

字段要求：
- unit_id：本地编号（如 "1"）
- center：核心主题，10~30 字
- first_line_anchor：该单元第一行原文的开头片段，逐字复制，不超过 30 字，不包含行号标记
- last_line_anchor：该单元最后一行原文的结尾片段，逐字复制，不超过 30 字，不包含行号标记（单行单元时与 first_line_anchor 相同）

### 知识点（KP）

从知识单元中提炼的可激活摘要，20~80 字，脱离原文也能独立理解。每个单元 1~3 个知识点。

字段要求：
- point_id：本地编号（如 "1"）
- unit_id：必须对应一个已存在的 unit unit_id
- content：不得为空
- type：definition / rule / method / case / question

### 不提取的内容

目录、文件编号、版本号、编制时间、审核人、变更记录表、过渡性文字、流程图节点编码通用说明。如果整段都是此类内容，返回空数组。

### 输出格式

```
{
  "units": [
    {"unit_id": "1", "center": "知识单元主题", "first_line_anchor": "该单元第一行开头", "last_line_anchor": "该单元最后一行结尾"}
  ],
  "points": [
    {"point_id": "1", "unit_id": "1", "content": "可激活摘要内容", "type": "rule"}
  ]
}
```

## User

以下文本每行前标注了原文行号（仅供你判断单元边界参考，不要把行号抄进输出）：

{{text_content}}

## Schema

```json
{
  "type": "object",
  "required": ["units", "points"],
  "properties": {
    "units": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["unit_id", "center", "first_line_anchor", "last_line_anchor"],
        "properties": {
          "unit_id":           { "type": "string", "minLength": 1 },
          "center":            { "type": "string", "minLength": 1 },
          "first_line_anchor": { "type": "string", "minLength": 1 },
          "last_line_anchor":  { "type": "string", "minLength": 1 }
        }
      }
    },
    "points": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["point_id", "unit_id", "content", "type"],
        "properties": {
          "point_id": { "type": "string", "minLength": 1 },
          "unit_id":  { "type": "string", "minLength": 1 },
          "content":  { "type": "string", "minLength": 1 },
          "type":     { "type": "string", "enum": ["definition", "rule", "method", "case", "question"] }
        }
      }
    }
  }
}
```
