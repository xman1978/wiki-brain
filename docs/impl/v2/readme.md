# Wiki-Brain V2 实现概览（完整认知系统 · 最终版本）

## 定位

V1 完成后，系统已具备学习转化能力：检索事件驱动 ActivationLink 演化，学习结果改变检索行为，稳定主题沉淀为初版 Wiki。但它仍然是「一条链路处理所有问题」的检索问答系统——只有快/慢路径之分，没有按问题性质选择思考方式的能力。

V2 的目标是实现 `docs/design/` 全部设计能力，把系统从「会学习的检索问答」升级为完整的**个人知识大脑**：

```text
问题进入
  -> 认知路由选择思维模式（简单问题轻量处理，复杂问题深度建模）
  -> 知识加工模式（KPP）识别，生成领域证据槽位（知识结构先于检索确定）
  -> ActivationLink 激活知识，槽位驱动召回扩展，KPN 补充上下文
  -> 证据挖掘按槽位定向摘选片段级证据
  -> 复杂问题进入 Working Model（KPP 实例化的五层认知工作空间）
  -> 推理模式（RP）在 Working Model 上执行领域无关的受约束推理
  -> 高风险 / 高时效问题进入查证，矛盾证据进入冲突检测
  -> 生成带边界的回答

若产生学习价值：
  -> Learning Event -> Study 调整长期记忆
  -> 反复有效的思考结构提炼为实践路径
  -> 稳定主题按认知视角编译为 Wiki
  -> 生命周期持续管理记忆有效性
```

最终目标（`docs/design/readme.md`）：Trace 是「学习事件记录系统」而非过程记录系统；Study 是「长期记忆学习系统」而非思考复盘系统；系统越用越准，而不是越存越大。

设计依据：`cognitive-routing.md`、`knowledge-processing-pattern.md`、`reasoning-pattern.md`、`evidence-mining.md`、`working-model.md`、`study.md`、`lifecycle.md`、`wiki-compilation.md`、`kpn.md`、`design.md` 第 7-9 节。

---

## 与 V1 的边界

| 能力 | V1 | V2 |
|------|----|----|
| 问题处理 | 快路径（激活层）/ 慢路径（完整链路）二分 | 七种思维模式 + 认知判定维度 + 认知预算 + 升降级 |
| 复杂问题 | deep 路径（单次 LLM 结构化推理） | Working Model：KPP 实例化的五层认知工作空间 |
| 知识加工模式（KPP） | 无 | 领域槽位模板库（挂 Domain/Concept）+ 前置识别 + 槽位驱动检索扩展 + 槽位状态充分性判断 |
| 推理模式（RP） | 无 | 领域无关推理形式库（六种脚手架），intent 映射选择，主辅组合 |
| 证据挖掘 | 按问题摘选片段级证据 | 按 KPP 槽位定向摘选 |
| 经验复用 | 无 | 实践路径提炼、验证与经验路径模式 |
| 证据来源 | 仅库内知识 | 查证模式接入外部证据 |
| 冲突处理 | contradicts 关系仅标记 | 冲突检测模式，基于 contradicts 关系整理正反证据（不引入 KU/KP 层面的 conflicted 生命周期状态，见 lifecycle.md） |
| 生命周期 | 完整 3 状态（current/superseded/deprecated） | 与 V1 一致，3 状态已覆盖全部场景，无需扩展；证据链传导补齐 |
| 认知视角 | 无（单一视角） | 视角差异化的 ActivationLink 与 Wiki |
| Wiki | 主题页 / 概念页 | 全部六种页面类型 + 防固化完整要素 + 视角化编译 |
| Concept / Domain | 预制静态 | 边界调整、候选晋升、拆分合并 |
| ActivationLink 晋升 | 人工确认 | 检索事件累积全自动晋升 |
| 对外 | 面向用户的 Page | Agent 接入层，知识大脑作为认知服务开放 |

---

## 核心能力

### 1. 认知路由（Cognitive Routing）

依据 `cognitive-routing.md`。问题进入后、深度检索前，先做认知判定，再映射到处理模式：

**认知判定维度**：确定性、风险、时效性、证据充分性、冲突度、复杂度、用户意图、认知预算。判定由程序规则（链接状态、证据统计、时效标记）与 LLM 语义判断（意图、复杂度）共同完成，不完全依赖模型主观判断。

**七种思维模式**：

```text
直接记忆    高确定性低风险 -> 稳定长期记忆（verified 链接 / Wiki 结论）直答
快速检索    明确查找类问题 -> 定位来源与证据，轻量组织
经验路径    反复出现的任务类型 -> 调用已验证的实践路径
工作模型    设计 / 判断 / 决策 / 跨文档综合 -> Working Model 完整流程
查证        高时效 / 高风险 / 要求依据 -> 外部证据确认
冲突检测    多来源矛盾 / conflicted 链接命中 -> 整理正反证据，条件化结论
反思学习    回答失败 / 用户纠正 / 反复追问 -> 分析失败原因，形成学习信号
```

**升降级规则**：处理过程中按 `cognitive-routing.md` 第 3 节规则切换（如快速检索证据不足升级工作模型、发现冲突升级冲突检测、工作模型发现问题简单降级直答）。硬约束由程序强制：高风险不得停留直接记忆；conflicted 链接命中必须触发冲突检查；candidate 链接任何模式下不得直接决定答案。

**认知预算**：low / medium / high 三档，约束检索深度、KPN 扩展、Working Model 规模、查证与冲突检测强度。预算与模式绑定默认值，可随证据状态升降；high 预算下仍无法形成可靠结论时输出边界与缺口，不无限扩展。

### 2. 架构落地：认知能力 → Service，思维模式 → Agent

依据 `design.md` 第 1、9 节。V2 做一次架构重组，把 MVP/V1 的模块化流水线改造为可编排的认知服务：

```text
Service（稳定认知能力，internal/service/ 或按现有模块演化）
  问题理解 / 认知结构激活 / 补充查找 / 结果融合 / 缺口识别 /
  证据回溯 / 临时认知建模 / 推理组织 / 学习事件记录 / 学习沉淀

Agent（思维模式承载，internal/agent/）
  每种思维模式一个 agent，负责按该模式编排 service 调用序列；
  模式切换 = agent 间移交，携带已完成的中间结果，不重复检索

约束
  service 负责数据访问、索引、模型调用、错误处理、trace 输出；
  LLM 只在 service 内部需要语义判断的局部步骤被调用；
  认知能力不做成模型自由调用的 skill。
```

MVP/V1 的 Retrieval、Answer 等模块内部逻辑大量复用，重组的是调用编排层。

### 3. 知识加工模式 + Working Model + 推理模式

依据 `knowledge-processing-pattern.md`、`working-model.md`、`reasoning-pattern.md` 与 `cognitive-routing.md` 第 6 节。三者构成两轴一空间：KPP（领域相关，知识怎么组织）× RP（领域无关，怎么推理），Working Model 是 KPP 实例化的认知工作空间，RP 在其上执行。拆两轴避免模式库组合爆炸（N 领域 + M 推理形式，而非 N×M）。

**知识加工模式 KPP（前置于检索）**：问题理解阶段按领域选定模式并生成证据槽位。前置的原因是词汇鸿沟——只有槽位关键词能把召回引向材料实际的表达方式：

```text
模式库：先验手写小库起步（运维操作类 / 诊断类 / 规则判定类 / 方案类），
  挂载 Domain / Concept——KPP 选择复用 Domain 匹配，零新增 LLM 调用；
无匹配时回退通用默认模式（槽位 = 核心变量/已知知识/证据/缺口）；
模式库演化：反复回退或反复出现的领域结构，经 Study 提炼候选新模式。
```

**槽位机制承担三件事**：

```text
检索扩展：按槽位关键词补充召回，跨越词汇鸿沟；
知识转换：证据挖掘按槽位摘选片段填入，材料即按问题视角重组
          （部署文档 -> 运维回答），每格可回链原文，无自由改写；
充分性判断：以必需槽位状态替代"是否存在 direct 证据"，
          空槽位即显式知识缺口，先补召回，仍空则如实输出缺口。
```

**Working Model（五层认知工作空间）**：per-question 生命周期（Created → Evidence Filling → Knowledge Processing → Reasoning → Answer Ready → Discard），不落长期存储：

```text
L1 Problem            本次问题与边界（含 Session 四元组）
L2 Slots              KPP 实例化槽位 + 片段级证据填充
L3 Reasoning State    每槽位状态：confirmed / pending / conflict / insufficient
                      （程序维护，驱动补召回、升降级与缺口上报）
L4 Derived Knowledge  槽位内容的受控综合（Stop+Start -> Restart Procedure），
                      每条必须绑定片段级出处，程序白名单校验，无出处不得进入回答
L5 Candidate Answer   RP 执行后的回答草稿 + 推理路径 + 回答边界
```

**推理模式 RP（推理脚手架，非推理引擎）**：领域无关的六种形式（deductive / inductive / causal / comparative / decision / procedural），每种 = 推理步骤模板 + 输出契约 + 程序检查（结论回链槽位证据、必需槽位不满足输出保守结论）。选择由 Session intent 映射（可配置规则，误配经 Learning Event 由 Study 修正），主辅可组合；置信度低回退按证据状态直接组织回答。

**质量检查**：输出前检查问题边界、变量完整性、证据覆盖、缺口显式性、正反证据并存、推理可回溯性、回答边界与证据强度匹配（`design.md` 第 7 节）。

**KPN 协作**：核心 KP 经 ActivationLink 激活后，KPN 在 Working Model 需要时做局部上下文补充（变量、边界、反例、缺口），受认知预算约束扩展深度，不独立扩散（`kpn.md`）。

**边界**：Working Model 是一次性思考结构，完整内容不进入 Trace、不直接沉淀为长期记忆；只有其结构模式经多次事件被 Study 提炼（见能力 4）。

**关系沉淀副产品**：槽位组装本身是关系发现——不同材料的 KP 填入同一模式的槽位时，归属与先后关系已在使用中显现。这类关系随 Learning Event 进入 Study，跨多次事件稳定复现才沉淀为 KPN 长期关系（`study.md` 第 6 节）；不在导入期批量抽取关系。

### 4. 实践路径（Practice Path）与经验路径模式

依据 `study.md` 第 7 节。工作模型 → 经验路径的闭环：

```text
形成：同类问题在工作模型模式下 repeated_success，且多次事件的
      scene / goal / 核心变量组合高度相似 -> Study 提炼候选实践路径
内容：适用任务类型、核心变量组合与检查顺序、建议激活的领域/概念/KP 范围、
      步骤框架、适用边界与已知失效条件
验证：候选路径在新问题中按模板组织思考仍有效，验证过程记录为 Learning Event，
      通过后晋升为稳定模板
调用：认知路由识别到匹配任务类型时优先选择经验路径模式；
      路径失效或超出边界时回退工作模型模式重新探索
```

实践路径引用 ActivationLink 但不是 Working Model 快照；反复有效的实践路径可作为 Wiki 方法页的编译输入。实践路径与两轴模式的边界：实践路径是"KPP × RP"组合在具体任务上反复成功后的特化，携带具体变量组合与失效条件；路径失效时回退到通用的 KPP + RP 重新组织（`knowledge-processing-pattern.md` 第 6 节）。

### 5. 查证模式与外部证据

依据 `cognitive-routing.md` 5.5 节与 `design.md` 第 3 节。库内知识不足、可能过期或风险较高时，由问题和知识缺口驱动外部查找（web 检索或用户配置的外部源）：

- 列出需确认的事实点 → 查找外部证据 → 比较来源可靠性与时效 → 区分已确认 / 未确认 / 争议内容；
- 外部证据进入 EvidenceSet 时明确标记来源类型，与库内证据统一比较，不默认更高或更低优先级；
- 查证发现库内知识可能过期时，触发一条 Learning Event 提示人工核实来源材料是否需要重新上传
  （不直接改变 KU/KP 的 lifecycle——只有 reupload 成功产出新内容后才会转 superseded，见 lifecycle.md）；
- 被反复采用的外部证据可提示用户导入为正式 Source（补充学习闭环）。

### 6. 冲突检测模式

依据 `cognitive-routing.md` 5.6 节。V1 已标记的 contradicts 关系与查证发现的矛盾在 V2 贯通处理：

- 识别冲突点，分别整理支持与反对证据，判断冲突来自事实、条件、时间还是立场差异；
- 冲突信息直接来自 KPN 的 contradicts 关系（检索时动态查出，见 retrieval.md 步骤 8 的 EvidenceSet.conflicts），
  不引入持久化的 conflicted 状态——冲突是"两条知识之间的关系"，不是"某条知识自身的属性"；
- 可消解的冲突回到来源确认（消解后其中一方可能因 reupload 转 superseded）；
  不可消解的冲突在回答中输出条件化结论与冲突边界，不强行合并；
- ActivationLink 自身预留了 conflicted 状态（见 activation.md，V1 不产生），
  命中时须触发冲突检查，不直接作为答案依据；Wiki 编译时不将存在 contradicts 关系的知识
  强行合并进单一结论（争议点要素）。

### 7. 生命周期：与 V1 一致，不扩展

依据 `lifecycle.md`。V1 已经是完整设计（current / superseded / deprecated），不存在"V1 子集、V2 补齐"的关系——设计文档已经把 candidate / needs_verification / conflicted / historical / retracted 逐一评估过，均无独立于这 3 种状态的必要场景（详见 `lifecycle.md` 第 2 节）。V2 在这 3 状态之上需要的能力，都能落到已有机制而不必新增状态：

- 反思学习与用户纠正：复用 `trace.md` 已有的 `user_correction` Learning Event，用于给 ActivationLink 降权，不需要给 KU/KP 加"错误知识"标记；
- 冲突处理：复用 contradicts 关系（见上一节），不需要 conflicted 生命周期状态；
- 证据链传导：来源被替换/删除 → KU/KP 转 superseded/deprecated → 依赖的 ActivationLink 暂停强化 → Wiki 待重编译，这条链路在 V1 已完整（见 lifecycle.md 第 4 节），V2 无需扩展。

### 8. 认知视角（Cognitive Perspective）

依据 `design.md`、`precompile.md`。同一批材料在不同使用视角下形成不同认知结构：

- 视角作为一等对象（使用者 / 角色 / 任务类型 / 专业方向），会话可声明或推断当前视角；
- Domain / Concept 保持通用共享，**视角差异只体现在 ActivationLink 上**：链接携带视角与使用条件（scene / goal / 思维模式 / 注意焦点），同一 KP 在不同视角下可有不同激活路径；
- 检索事件与 Study 学习按视角维度累积，一个视角的强化不污染其他视角；
- Wiki 页面按视角编译：同一批 KU 在不同视角下可形成不同页面。

### 9. Wiki 编译完整版

依据 `wiki-compilation.md`。在 V1 主题页/概念页基础上补齐：

- **全部六种页面类型**：主题页、概念页、方法页（实践路径输入）、经验页（反馈学习输入）、问题页（反复出现的问题类型）、决策页（高风险判断场景）；
- **防固化完整要素**：稳定结论、适用边界、证据来源、争议点、待验证点、最近更新时间、依赖 KU 与 ActivationLink、被替代/修订记录；
- **重编译全自动候选**：由 repeated_success / repeated_failure / knowledge_conflict / user_correction / concept_boundary_signal / 知识状态变化驱动，每次重编译附 Learning Reason，可追溯到触发事件；
- **认知预算约束引用**：low 预算直接引用 Wiki 结论；medium 检查适用边界；high 必须回到证据与 Working Model，不得只依赖 Wiki。

### 10. Concept / Domain 演化

依据 `study.md` 第 3、8 节与 `precompile.md`。预制认知框架开始随使用演化：

- concept_boundary_signal（边界混淆、过宽、过窄）驱动 Concept 边界调整候选；
- 使用中反复出现、无法归入现有结构的锚点形成候选 Concept / 候选 Domain，经累积验证后晋升；
- 支持 Concept 拆分与合并，激活路径按场景重组；所有演化动作经 Learning Result 记录，可回滚。

### 11. 反思学习模式

依据 `cognitive-routing.md` 5.7 节。由明确失败信号触发（用户纠正、连续追问同一缺口、检索失败、推理路径失效）：

- 回看问题理解、检索覆盖、模型组织、推理路径、长期记忆五个环节定位不足；
- 输出学习建议、修正建议、缺口记录，形成 Learning Event 供 Study 消费；
- 单次反馈不重写稳定知识；同类问题反复失败才推动结构性修正（谨慎原则）。

反思学习聚焦知识层信号；Agent 任务级复盘不属于知识大脑职责。

### 12. Agent 接入层

依据 `design.md` 第 8 节。知识大脑作为认知服务开放给 Agent 平台：

```text
提供：领域记忆激活、来源证据、思维模式选择、临时认知模型、
      冲突检测、Learning Event 记录、经验沉淀与学习建议
形态：HTTP API（沿用现有 net/http 栈），按领域边界授权访问，
      不暴露整个知识仓库为通用 RAG 工具
反哺：Agent 使用结果产生 Learning Event，回流 Study
不做：任务调度、工具运行时、多 Agent 协作协议、权限沙箱（属于 Agent 平台）
```

同时 ActivationLink 晋升转为全自动（V1 的人工确认退化为可选审计），学习闭环不再依赖人工介入。

---

## 实现顺序

四个阶段，每阶段可独立交付验证：

```text
阶段一：深度思考能力
  1. 认知路由（判定维度 + 模式选择 + 升降级 + 认知预算）
  2. Service / Agent 架构重组（存量模块能力包装为 service）
  3. 知识加工模式库（挂 Domain）+ 前置识别 + 槽位驱动检索扩展 + 槽位定向证据挖掘
  4. Working Model（五层认知工作空间）+ 推理模式脚手架 + 质量检查
  —— 交付：复杂问题回答质量显著优于 V1 deep 路径；
           词汇鸿沟类问题（问"重启"、材料只写"停止/启动"）可正确召回作答

阶段二：证据可靠性
  5. 查证模式与外部证据
  6. 冲突检测模式
  7. 生命周期证据链传导补齐（状态本身沿用 V1 的 3 状态，不扩展）
  —— 交付：高时效 / 高冲突问题给出带来源与边界的可靠回答

阶段三：经验与视角
  8. 实践路径提炼、验证与经验路径模式
  9. 反思学习模式
  10. 认知视角（视角化 ActivationLink 与学习累积）
  —— 交付：同类复杂问题第二次处理明显快于第一次

阶段四：沉淀与开放
  11. Wiki 编译完整版（六类型 + 防固化 + 视角化 + 自动重编译候选）
  12. Concept / Domain 演化
  13. Agent 接入层 + ActivationLink 全自动晋升 + 知识加工模式库演化
  —— 交付：知识体系可读可维护，可被外部 Agent 消费
```

---

## 数据与存储变化（概要）

```text
新增
  knowledge_processing_patterns  知识加工模式库（适用领域/Concept、证据槽位、
                                 槽位关系、加工规则、状态）；推理模式（RP）为
                                 固定小集合，以 prompt 模板文件承载，不建表
  practice_paths            实践路径（模板内容、适用边界、状态、验证记录）
  perspectives              认知视角定义
  external_evidences        查证获得的外部证据记录（可选持久化）
  concept_candidates        候选 Concept / Domain 及演化记录

扩展
  activation_links          增加视角、scene / goal / 思维模式 / 注意焦点等使用条件，
                            补齐 conflicted 状态（ActivationLink 自身状态机，与
                            knowledge_units/points 的 lifecycle 无关）
  learning_events           事件类型补齐 knowledge_conflict / concept_boundary_signal /
                            wiki_update_candidate / mode_misjudge / slot_gap /
                            relation_candidate（槽位组装暴露的关系候选）等
  wiki_pages                页面类型、视角、防固化要素、修订链
  sessions                  当前认知视角、认知预算
```

具体 schema 在各模块实现文档（`docs/impl/v2/<module>.md`）中定义。

---

## V2 成功标准

```text
按需思考：    简单问题不进 Working Model，复杂问题不停留浅检索；
              模式选择准确率经人工抽样达到可接受水平，误判可被反思学习捕获；
深度回答：    设计 / 判断 / 决策类问题输出含变量、正反证据、推理路径和
              边界的结构化回答，推理可回溯到证据；证据充分性以必需槽位
              填充率判定，空槽位在回答中显式呈现为缺口而非被内容凑满；
结构先行：    词汇鸿沟类问题经模式槽位扩展正确召回（问"重启"能召回
              "停止/启动/验证"）；跨视角提问（材料写部署、用户问运维）
              经槽位重组正确作答，且每格内容可回链原文；
证据可靠：    高时效问题回答附外部查证来源；矛盾证据输出条件化结论
              而非强行合并；过期知识不进入当前回答；
经验复用：    同类复杂问题反复处理后形成实践路径，再次出现时经验路径
              模式的处理成本与延迟显著低于工作模型模式；
视角分化：    同一知识库在两个不同视角下形成可观察的不同激活路径
              和不同 Wiki 页面；
自主学习：    ActivationLink 全生命周期（形成 -> 晋升 -> 降权 -> 淘汰）
              无需人工介入，人工仅做审计；
体系沉淀：    稳定使用一段时间后，Wiki 页面覆盖主要高频主题，
              页面结论均可回链证据，底层变化能触发重编译；
Agent 可用：  外部 Agent 通过 API 完成「激活记忆 -> 获取证据 -> 
              记录学习事件」的完整调用，且其使用反哺学习；
边界守住：    Trace 仍只记录学习事件不存思考过程；Study 仍只调整
              长期记忆不做复盘；系统数据增长以 verified 链接质量
              和 Wiki 覆盖率衡量，而非存储量。
```

V2 完成后，`docs/design/` 描述的知识大脑能力全部落地：材料进入 → 编码 → 按需思考 → 可靠回答 → 学习转化 → 体系沉淀 → 生命周期管理的完整闭环。
