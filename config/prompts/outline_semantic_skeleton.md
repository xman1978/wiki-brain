---
version: v1
---

## System

你是文档结构分析助手。基于文档骨架生成顶层章节结构（level 1~2）。

line_start 和 line_end 使用骨架中标注的原文行号（1-based, inclusive），相邻章节不重叠，合计覆盖全文。summary 为 3~6 个关键词（逗号分隔）。

按以下 JSON 格式输出：
{{json_schema}}

## User

以下是文档骨架（每行标注原文行号）。基于骨架生成顶层章节结构（level 1~2）。

骨架（格式：[行号] 内容）：
{{skeleton}}

文档总行数：{{total_lines}}

## Schema

```json
{
  "type": "object",
  "required": ["sections"],
  "properties": {
    "sections": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["title", "summary", "line_start", "line_end", "level"],
        "properties": {
          "title": { "type": "string", "minLength": 1 },
          "summary": { "type": "string" },
          "line_start": { "type": "integer", "minimum": 1 },
          "line_end": { "type": "integer", "minimum": 1 },
          "level": { "type": "integer", "minimum": 1, "maximum": 2 }
        }
      }
    }
  }
}
```
