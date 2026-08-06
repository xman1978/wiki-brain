---
version: v2
---

## System

以下是一批知识点，它们已经被判定为同属于一个知识概念（下面会给出这个概念的名称和边界）。请你从这批知识点整体（包括它们的来源标题、摘要、内容）里，找出它们共同在讲的那一个具体对象（entity）——通常就是来源文档标题里点明的那个主要产品/技术/系统/组织制度，而不是某一条知识点自己局部提到的、只是这个主要对象的安装依赖、前置步骤或内部子功能（例如整批内容其实是某个数据库集群的安装配置说明，其中夹杂了配置 DNS/NTP、创建系统账户、安装依赖包这类步骤，这些步骤本身不构成一个独立对象，entity 仍然是那个数据库集群本身，不是"DNS"或"系统账户"）。

判断方法：先看这批知识点的来源标题/摘要——大多数情况下来源标题本身就点明了这个共同对象；再确认知识点内容和这个对象是否一致（不能是该文档里顺带提到、和这个概念维度无关的另一个独立产品）。

如果这批知识点确实找不到一个合理的共同 entity（比如它们其实分属好几个互不相关的独立对象，被误判到了同一个概念下），entity 输出空字符串，其余字段也都输出空字符串，不要勉强给一个牵强的答案。

entity_type 用简短词概括这个对象的类型（如"数据库产品""操作系统""公司规章制度""容器编排系统"），entity 为空时 entity_type 也输出空字符串。

description 用一两句话说明"你判断出的 entity，在这个概念维度下"具体是什么——即这个词条（entity+概念的组合）本身讲的是什么，必须在描述里点明 entity 具体是谁，不要只重复概念本身的定义。

boundary 单独用一两句话说明这个词条"包含什么、不包含什么"——例如只覆盖这一批知识点体现出的具体范围，不覆盖这个 entity 在其他概念维度下的内容，也不覆盖同一概念维度下其他 entity 的内容。不要和 description 写成同一句话的重复表达。

按以下 json 格式输出，不输出任何其他内容：
{"entity":"...","entity_type":"...","description":"...","boundary":"..."}

## User

概念：{{concept_name}}
概念边界：{{concept_boundary}}

这批知识点（格式：unit_center TAB 来源标题 TAB 来源摘要 TAB content；来源摘要可能为空）：
{{points}}

## Schema

```json
{
  "type": "object",
  "required": ["entity", "entity_type", "description", "boundary"],
  "properties": {
    "entity": { "type": "string" },
    "entity_type": { "type": "string" },
    "description": { "type": "string" },
    "boundary": { "type": "string" }
  }
}
```
