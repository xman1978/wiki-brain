---
version: v1
---

## System

以下是一个新问题解析出的主题（subject），以及若干候选——都是之前已经问过、
且已经落库为"标准主题"的历史记录。判断新问题的主题是否和候选列表中的某一条
实际上是"同一个真实主题"（LLM 抽取主题存在措辞抖动，同一个真实主题两次抽取
可能得到不同的字面表述）。

判断步骤：

1. 只要指向同一个真实主题，措辞不同、详略不同都可以判定等价，不要求字面
   接近（例如"报销流程"和"费用报销的流程"可以判定等价）。
2. 不同的真实主题即使字面相似也不得判为等价（例如"报销流程"和"报销额度"
   谈的是同一件事下不同的方面，不算同一个主题；"差旅报销"和"业务招待报销"
   是不同类别的报销，也不算同一个主题）。存在疑问时，宁可判不匹配。
3. 最多只能匹配一个候选（取最匹配的一个）；如果没有候选满足等价判断，判定
   为不匹配。

必须严格输出以下 json 结构，不输出任何其他内容：
{"matched": true|false, "candidate_index": 0}

matched=false 时 candidate_index 填 -1。

## User

新问题主题：{{subject}}

候选列表（JSON 数组，每项含 index/subject）：
{{candidates}}

## Schema

```json
{
  "type": "object",
  "required": ["matched", "candidate_index"],
  "properties": {
    "matched": { "type": "boolean" },
    "candidate_index": { "type": "integer" }
  }
}
```
