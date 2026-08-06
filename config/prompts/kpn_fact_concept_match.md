---
version: v3
---

## System

以下是一批"事实类"知识簇，每个簇对应一个具体对象（entity），已知它属于哪个知识领域，但还没有和该领域已有的"概念"（跨具体实现成立的机制/方法/理论/规则）建立关联。请判断每个簇最适合归入下面候选列表中的哪一个概念，如果没有任何一个真正贴合，就选 none。

判断标准：这个簇的内容，是不是在讲"该 entity 在这个 concept 维度下的具体情况"？例如 entity=MySQL、簇内容讲的是备份操作和策略，候选列表里有"数据库备份恢复"这个 concept，就应该匹配它；如果簇内容混杂了好几个不同 concept 维度的内容（比如既讲备份又讲高可用），选覆盖内容占比最大、最贴切的一个，不要强行拆分——拆分是聚类阶段的职责，这里不重新聚类，也不输出多个匹配；如果候选列表里没有任何一个概念的边界能覆盖这个簇的核心内容，选 none，不要牵强匹配、不要选一个只是沾边的概念。候选列表里的概念优先来自该领域的预制词条表（`preset/domains.json`），代表这个领域公认的划分颗粒度——匹配时按这个颗粒度判断，不要因为簇内容更细碎就拒绝匹配到一个更粗的预制概念，也不要因为簇内容混了多个预制概念就都不选，选覆盖最大的那个。

只能从候选列表给出的 entry_id 中选择，不要编造不存在的 id。

这一步不需要、也不应该给出最终词条名称——entity 与匹配到的概念名称的组合由程序按固定规则拼接，不需要模型改写措辞，以保证同一个 entity+概念 组合无论在哪一批、哪一次调用里出现，都拼成完全相同的字符串（否则下游按名称去重会失效，同一个真实概念被拆成好几个候选）。

按以下 json 格式输出，不输出任何其他内容：
{"matches":[{"cluster_index":0,"matched_concept_id":"..."}]}

## User

候选概念列表（该领域已有的 concept 词条，格式：entry_id TAB 名称 TAB 描述 TAB 边界）：
{{concepts}}

待匹配的事实簇（格式：cluster_index TAB entity TAB entity_type TAB 簇内容摘要）：
{{clusters}}

## Schema

```json
{
  "type": "object",
  "required": ["matches"],
  "properties": {
    "matches": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["cluster_index", "matched_concept_id"],
        "properties": {
          "cluster_index": { "type": "integer" },
          "matched_concept_id": { "type": ["string", "null"] }
        }
      }
    }
  }
}
```
