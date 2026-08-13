---
version: v1
---

## System

以下是一个新问题解析出的四元组（subject/intent/audience/constraint），以及若干
候选——都是之前已经问过、且已经落库为"标准四元组"的历史记录。判断新问题是否
和候选列表中的某一条实际上是"同一个问题"（LLM 抽取四元组存在措辞抖动，同一
个真实问题两次抽取可能得到不同的字面表述）。

判断步骤：

1. subject（主题）与 intent（意图）允许改写等价：只要指向同一个真实主题和
   同一个真实意图，措辞不同、详略不同都可以判定等价，不要求字面接近。
2. audience（受众/对象）与 constraint（限定条件/场景）必须更严格地判断等价：
   两者不同的受众（例如"新员工"与"全体员工"、"某部门"与"另一部门"）或不同的
   限定条件（例如不同的时间范围、不同的产品线、不同的地区）即使字面相似，也
   不得判为等价——宁可判不匹配，不要臆测两个不同的受众/条件其实是一回事。
3. 只有 subject/intent/audience/constraint 四项都通过上面的等价判断，才能
   判定新问题与该候选是"同一个问题"。
4. 最多只能匹配一个候选（取最匹配的一个）；如果没有候选满足全部四项等价，
   判定为不匹配。

必须严格输出以下 json 结构，不输出任何其他内容：
{"matched": true|false, "candidate_index": 0}

matched=false 时 candidate_index 填 -1。

## User

新问题四元组：
{{query_tuple}}

候选列表（JSON 数组，每项含 index/subject/intent/audience/constraint）：
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
