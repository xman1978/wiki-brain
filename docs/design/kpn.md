# KnowledgePoint Network 设计

## 1. 定位

KnowledgePoint Network（KPN）是 KnowledgePoint 之间的轻量语义连接网络。

它连接的不是文档，也不是主题，而是已经从知识单元中提炼出的 KnowledgePoint。

KPN 不负责：

```text
直接检索答案；
替代 ActivationLink；
构建完整知识图谱；
形成新的主题聚类层；
独立扩散上下文。
```

KPN 负责：

```text
在核心 KnowledgePoint 被 ActivationLink 激活之后，补充当前问题需要的上下文；
帮助 Working Model 识别变量、边界、反例、前提、缺口；
支持复杂问题中的证据组织，而不是扩大检索范围。
```

整体职责边界是：

```text
ActivationLink
    负责找到知识

KnowledgePoint Network
    负责补充上下文

Working Model
    负责组织思考
```

KPN 不应替代 ActivationLink，也不应成为新的主题聚类层。详细设计边界见本文档；在系统总览中的位置见 `design.md`。

## 2. KPN 激活条件

KPN 不是默认激活。

只有满足以下条件时才激活：

```text
当前问题进入 Working Model；
已有核心 KnowledgePoint，但缺少上下文；
需要补充变量、边界、反例、前提、依赖关系；
存在证据冲突，需要查看相关 KnowledgePoint 的限制条件；
需要识别知识缺口。
```

简单事实问答、定义问答、明确材料定位问题，不应默认激活 KPN。

证据直答路径上，若 Rerank 后的直接证据已充分，也不应为了「补全上下文」而激活 KPN。

## 3. KPN 扩展边界

KPN 只能围绕当前 Working Model 扩展。

KPN 扩展只能服务四类目标：

```text
变量补充；
边界补充；
反例补充；
缺口识别。
```

扩展原则：

```text
KPN 扩展不是图遍历。
KPN 扩展必须绑定当前问题、当前思维模式和当前 Working Model。
KPN 不能因为一个知识点相关，就继续无限扩展到更多知识点。
```

KPN 不从用户问题直接进入，也不替代目录结构召回、全文检索或外部证据查找。它只在核心 KnowledgePoint 已被 ActivationLink 或补充查找激活之后，为 Working Model 服务。

## 4. KPN 停止条件

KPN 扩展应在以下任一条件满足时停止：

```text
当前 Working Model 的关键变量已经足够；
已经找到必要边界和反例；
新增 KnowledgePoint 只提供背景，不影响回答；
扩展内容开始偏离当前问题；
证据强度不足以支撑进一步推理；
认知预算耗尽。
```

停止不是失败，而是说明当前上下文已足够支撑本次思考，或继续扩展的成本与收益不匹配。停止时应保留已补充的上下文；仅当停止暴露知识缺口或边界问题时，才形成 Learning Event，不记录 KPN 每一步扩展过程。

## 5. KPN 与 Concept / KnowledgePoint / ActivationLink 的边界

**Concept** 是认知分类和导航入口，不直接承载知识内容。它帮助系统理解「当前问题属于哪个认知入口」，而不是提供「当前问题可使用的知识内容」。

**KnowledgePoint** 是从 KnowledgeUnit 中抽取或沉淀出的可复用知识表达。它提供可被使用、组合、验证和沉淀的知识内容，不是认知分类本身。

**ActivationLink** 是在特定认知视角、问题场景和历史反馈下形成的激活路径。它决定在当前条件下应激活哪些 KnowledgePoint，并通过知识点反查知识单元和来源。

**KPN** 是 KnowledgePoint 之间的上下文补充关系。它在已激活 KnowledgePoint 周边补充必要上下文，不负责找到知识，也不承担认知分类职责。

边界可概括为：

```text
Concept
    决定从哪个认知分类进入

ActivationLink
    决定激活哪些 KnowledgePoint

KnowledgePoint
    提供可使用的知识表达

KPN
    在已激活 KnowledgePoint 周边补充必要上下文
```

Concept 与 KnowledgePoint 不应混用：概念是入口和框架，知识点是内容与证据载体。KPN 连接的是知识点，不是概念；ActivationLink 经过概念导航到知识点，而不是把概念当作知识内容直接回答。

## 6. 总结

KPN 是上下文补充层，不是检索入口，也不是知识图谱。

一句话总结：

```text
ActivationLink 找到核心 KnowledgePoint 之后，KPN 在 Working Model 需要时补充变量、边界、反例和缺口；扩展有边界、有停止条件，并受认知预算约束。
```
