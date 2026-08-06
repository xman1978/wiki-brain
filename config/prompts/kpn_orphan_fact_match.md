---
version: v1
---

## System

以下是一批还没有归入任何知识概念的知识点（point），它们都属于同一个知识领域。请把每个知识点分别去匹配这个领域下面给出的"已有概念"列表——判断这个知识点讲的是"某个具体对象在这个概念维度下的具体情况"，例如概念是"数据库复制"、知识点内容是"MySQL 如何配置主从同步"，就应该匹配"数据库复制"。

这一步只做分类，不做聚类、不做命名、不用管这个知识点具体讲的是哪个对象（entity）——只判断它属于已有概念列表里的哪一个，或者都不属于。

判断标准：
- 只能从候选列表给出的 entry_id 中选择，不要编造不存在的 id；
- 如果一个知识点的内容明显是在讲一个具体产品/技术/组织的安装、部署、配置、监控、优化、故障处理等某个维度的具体情况，且候选列表里有对应维度的概念，就匹配它——即使这个知识点本身讲的是一个很小的安装依赖或前置步骤（例如配置 DNS/NTP、创建系统账户、安装某个系统包），只要这类步骤明显是从属于来源标题里那个主要产品/技术的安装配置过程，同样按它所服务的那个维度（如"数据库配置""容器部署"）匹配，不要因为步骤本身很小、很具体就跳过或强行判成 none；
- 如果没有任何一个概念的边界能覆盖这个知识点的核心内容，就选 none，不要牵强匹配；
- 每个知识点独立判断，不要因为和上一个知识点主题相关就直接复用同一个结果。

按以下 json 格式输出，不输出任何其他内容：
{"matches":[{"point_index":0,"matched_concept_id":"..."}]}

## User

已有概念列表（该领域已有的 concept 词条，格式：entry_id TAB 名称 TAB 描述 TAB 边界）：
{{concepts}}

待分类的知识点（格式：point_index TAB unit_center TAB 来源标题 TAB 来源摘要 TAB content；来源摘要可能为空）：
{{points}}

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
        "required": ["point_index", "matched_concept_id"],
        "properties": {
          "point_index": { "type": "integer" },
          "matched_concept_id": { "type": ["string", "null"] }
        }
      }
    }
  }
}
```
