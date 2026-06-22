# 设计文档目录

本目录存放知识大脑的设计思想文档。

这些文档描述系统做什么、为什么这样做，以及各概念对象之间如何协作。它们不写具体数据库表、接口或实现细节；工程实现见 `docs/impl/`。

建议阅读顺序：

```text
think.md          总体思想与核心闭环
  -> design.md     系统设计总览
  -> source.md     外部材料如何进入
  -> unit.md       知识单元与知识点
  -> precompile.md 导入阶段 vs 使用阶段；ActivationLink
  -> cognitive-routing.md  思维模式选择与问题分流
  -> retrieval.md  分层检索与证据查找
  -> working-model.md      复杂问题的临时认知模型
  -> trace.md      思考记录与追踪
  -> study.md      反馈学习
  -> lifecycle.md  记忆生命周期
  -> wiki-compilation.md   Wiki 编译与长期沉淀
```

## 文档列表

| 文档 | 标题 | 内容简介 |
| --- | --- | --- |
| [think.md](./think.md) | 知识大脑总体思想 | 定义知识大脑的目标与边界；说明为何不是普通知识库、Agent 平台或知识图谱；描述从材料进入到 Wiki 沉淀的核心闭环，以及简单/复杂问题分流原则。 |
| [design.md](./design.md) | 知识大脑系统设计 | 把 `think.md` 的总体思想转化为系统设计；定义核心对象、完整链路和 Agent 协作边界；是阅读其他专题文档前的总览入口。 |
| [source.md](./source.md) | 外部知识输入 | 描述外部材料如何进入系统、转换为规范化 Markdown 并保留来源；区分文档、对话、网页等不同材料类型及其处理方式。 |
| [unit.md](./unit.md) | 知识单元 | 定义知识单元与知识点的区别；说明如何从材料形成最小完整知识包，以及如何保留来源位置以支持追溯。 |
| [precompile.md](./precompile.md) | 初始激活结构 | 区分导入阶段形成的材料侧知识，与使用阶段形成的认知侧结构；说明领域、概念、ActivationLink 为何来自使用而非导入。 |
| [cognitive-routing.md](./cognitive-routing.md) | 思维模式选择 | 描述系统如何根据问题类型、熟悉度、不确定性和风险选择处理模式；定义直接记忆、快速检索、工作模型、查证、冲突检测等思维模式。 |
| [retrieval.md](./retrieval.md) | 知识激活与证据检索 | 描述 ActivationLink、目录结构树、全文检索和外部证据的分层检索路径；说明认知结构检索与补充查找如何协作。 |
| [working-model.md](./working-model.md) | 临时认知模型 | 描述复杂问题如何组织本次思考的工作结构；说明它与检索、思考记录和学习之间的关系。 |
| [trace.md](./trace.md) | 思考追踪 | 定义思考记录应保存什么、为何不是检索日志；说明记录深度如何与思维模式匹配。 |
| [study.md](./study.md) | 反馈学习 | 描述学习来自思考记录的哪些信号；区分材料层、认知层和表达层学习，以及强化、修正、降权、补充、重组和 Wiki 编译。 |
| [lifecycle.md](./lifecycle.md) | 记忆生命周期 | 说明知识为何需要状态管理；描述生命周期如何影响激活、ActivationLink 和 Wiki 页面，以及如何区分当前可用与历史解释知识。 |
| [wiki-compilation.md](./wiki-compilation.md) | Wiki 编译与长期知识沉淀 | 定义 Wiki 页面的定位与类型；说明 Wiki 如何由长期使用、反复激活和反馈强化后编译形成，而非导入时一次性生成。 |

## 文档关系

下面用流程图表示文档之间的主要依赖与协作关系。实线箭头表示「上游概念 feeds 下游」；虚线表示「横切关注点，影响多个环节」。

```mermaid
flowchart TB
  think["think.md<br/>总体思想"]
  design["design.md<br/>系统设计"]

  source["source.md<br/>外部知识输入"]
  unit["unit.md<br/>知识单元"]
  precompile["precompile.md<br/>初始激活结构"]

  routing["cognitive-routing.md<br/>思维模式选择"]
  retrieval["retrieval.md<br/>知识激活与证据检索"]
  working["working-model.md<br/>临时认知模型"]

  trace["trace.md<br/>思考追踪"]
  study["study.md<br/>反馈学习"]
  lifecycle["lifecycle.md<br/>记忆生命周期"]
  wiki["wiki-compilation.md<br/>Wiki 编译"]

  think --> design

  design --> source
  design --> routing
  design --> trace
  design --> study
  design --> lifecycle
  design --> wiki

  source --> unit
  unit --> precompile

  precompile --> retrieval
  precompile --> study
  precompile --> wiki

  routing --> retrieval
  routing --> working
  routing --> trace

  retrieval --> working
  retrieval --> trace
  retrieval --> study

  working --> trace
  working --> study

  trace --> study

  study --> precompile
  study --> wiki

  lifecycle -.-> retrieval
  lifecycle -.-> study
  lifecycle -.-> wiki
```

### 关系说明

**总体层**

- `think.md` 提供哲学与边界；`design.md` 将其展开为系统级对象和链路，是其他文档的总纲。

**材料进入与编码**

- `source.md` → `unit.md`：外部材料规范化后，形成可追溯的知识单元和知识点。
- `unit.md` → `precompile.md`：知识单元是长期记忆的基础；导入阶段只形成材料侧结构和初始线索。

**问题处理**

- `cognitive-routing.md` 决定问题走轻量路径还是深度路径，并调度后续环节。
- `retrieval.md` 在 ActivationLink、目录结构树、全文检索和外部证据之间分层召回。
- `working-model.md` 承接复杂问题，把激活结果和证据组织为本次思考结构。

**追踪、学习与沉淀**

- `trace.md` 记录每次问题处理的完整过程，是学习的数据来源。
- `study.md` 从思考记录中提取信号，调整材料层、认知层和表达层。
- `wiki-compilation.md` 是表达层学习的产物：把长期稳定的主题编译为 Wiki 页面。

**横切：生命周期**

- `lifecycle.md` 贯穿激活、学习和 Wiki 维护，确保过期或已被替代的知识不会通过旧路径继续误导当前回答。

## 核心链路（跨文档）

各文档共同描述的同一条主线：

```text
外部材料（source）
  -> 知识单元和知识点（unit）
  -> 使用中形成 ActivationLink（precompile）
  -> 思维模式选择（cognitive-routing）
  -> 分层检索与证据查找（retrieval）
  -> 复杂问题进入临时认知模型（working-model）
  -> 思考记录（trace）
  -> 反馈学习（study）
  -> Wiki 编译（wiki-compilation）
        ↑
  生命周期管理（lifecycle）持续影响激活、学习与 Wiki 有效性
```
