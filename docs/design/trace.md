# Learning Event 与 Trace 设计

## 1. Trace 的重新定位

Trace 不是完整过程记录，也不是 Chain-of-Thought，也不是审计日志。

Trace 是 Learning Event 的记录形式。

Learning Event 是一次问题处理过程中，对长期记忆学习有价值的事件。

Trace 的目标不是解释系统为什么这样回答。

Trace 的目标是为 Study 提供可以学习的事实样本。

### Rerank 质量是 MVP 阶段的核心隐式信号

MVP 阶段尚未形成 ActivationLink，无法产生 `activation_success / activation_failure / activation_gap` 等激活类事件。**Rerank 输出的证据质量分级是这一阶段最主要的学习信号**，不依赖用户任何显式操作。

每次问答中，Rerank 已对候选证据做了语义分类（direct / supporting / irrelevant），Answer 的 citation 进一步确认了哪些直接证据被实际采用。两者组合形成**隐式质量分级**：

```text
confident：直接证据非空且被 Answer 引用
            → 检索准确，问题-KP 路径值得积累
partial  ：无直接证据但支持性证据非空，或直接证据未被引用
            → 检索偏弱，知识点相关但缺乏直接回答能力
gap      ：直接和支持性证据均为空
            → 知识库盲区，立即生成 knowledge_gap 事件
```

这一分级在每次问答中自动产生，不需要用户表态。积累足够多的 `confident` 质量问答后，统计出的「问题关键词 × KP 共现」是形成 ActivationLink 和 Wiki 编译的直接材料，见 Study 文档的共现消费机制。

### 学习信号以检索事件为主

知识大脑的有效学习**不依赖**用户持续主动纠正。当前文档中 `user_correction`、`repeated_success` / `repeated_failure` 等类型并列出现，容易让读者误以为系统必须靠用户反馈才能学习；实际上，**主学习燃料来自每次问题处理中自然产生的检索事件**。

三类核心检索事件是 ActivationLink 演化的主要驱动：

```text
activation_success：ActivationLink 命中且其知识被实际采用，支撑了有效回答；
activation_failure：ActivationLink 命中但未能支撑有效回答，或命中知识未被采用；
activation_gap：当前没有合适 ActivationLink，但补充查找找到了被采用的有效知识。
```

这些事件在问答过程中自动产生，记录的是「哪条路径被召回、是否进入最终回答、是否暴露缺口」等事实，不需要用户额外表态。`repeated_success` 和 `repeated_failure` 是对上述检索事件在相似场景下跨次累积的归纳，同样 primarily 来自使用过程，而非用户纠正。

`user_correction` 是高价值但**非必要**的补充信号：它能加速边界修正和知识重新验证，在检索信号模糊时提供决定性证据，但**不是** ActivationLink 能够演化的前提。即使完全没有用户反馈，系统仍应仅凭检索事件的累积，完成候选链接形成、verified 晋升、降权和淘汰。

### Trace 只记录什么

Trace 只记录与长期记忆学习相关的事实，不记录过程叙事：

```text
发生了什么事件；
涉及哪些知识对象；
最终结果是什么；
是否被采用；
是否暴露了知识缺口、冲突或激活路径问题；
是否对 ActivationLink、KnowledgePoint、Concept、Wiki 或知识状态产生潜在影响。
```

### Trace 不记录什么

Trace 不服务于在线解释，也不保存完整认知链路：

```text
模型详细推理过程；
模型为什么这样回答；
每一步检索为什么发生；
每个 rerank 分数为什么这样；
为什么选择某个证据而没有选择另一个证据；
KPN 每一步扩展过程；
Working Model 内部每一步组织和推导；
解释性、叙事性、反思性内容。
```

被召回但未采用的路径、中间检索候选、Rerank 细节和 Working Model 内部修正，除非它们构成学习事件本身，否则不必进入 Trace。

## 2. Trace 的触发条件

不是每次问答都需要 Trace。

不是每次检索都需要 Trace。

不是每次 Working Model 都需要 Trace。

只有当一次问题处理产生学习价值时，才生成 Learning Event。

触发条件包括：

```text
ActivationLink 成功命中并被采用；
ActivationLink 命中但失败；
当前没有合适 ActivationLink，但通过目录结构召回、全文检索、外部证据或用户补充找到了有效知识；
用户明确纠正系统回答；
检索或回答暴露知识缺口；
检索或回答暴露知识冲突；
某类问题多次通过同一路径成功；
某类问题多次通过同一路径失败；
某个 Concept 边界出现混淆、过宽、过窄；
某个 Wiki 页面可能需要重编译或调整适用边界。
```

没有学习价值的问题处理，可以不生成 Learning Event。简单、低风险、证据充分且未改变任何长期记忆判断的问题，不必为了「留痕」而记录 Trace。

## 3. Learning Event 类型

Learning Event 按学习信号分类，每种类型对应一类可被 Study 消费的事实。**ActivationLink 相关学习以检索事件为主**（`activation_success`、`activation_failure`、`activation_gap` 及其累积形态）；用户反馈类事件（如 `user_correction`）是补充加速，不是主驱动。

**activation_success**：ActivationLink 命中且其指向的知识被实际采用，支撑了成功回答。单次事件只说明路径值得观察；多次在相似 scene / goal 下重复出现，可累积为 `repeated_success` 并推动强化或晋升。

**activation_failure**：ActivationLink 命中但未能支撑有效回答，或命中知识未被采用、导致回答失败或降级。单次失败可能来自问题理解偏差或证据不足，不应立即废弃整条链接；多次在相似条件下重复出现，可累积为 `repeated_failure` 并推动降权或淘汰。

**activation_gap**：当前没有合适 ActivationLink，但通过目录结构召回、全文检索、外部证据或用户补充找到了有效知识，暴露激活路径缺口。若补充结果被实际采用，Study 可据此形成候选 ActivationLink，无需等待用户纠正。

**knowledge_conflict**：检索或回答中出现多来源、多结论或路径间的稳定冲突，需要标记或进入冲突处理。

**user_correction**：用户明确纠正系统回答，表明当前知识、路径或边界存在问题。这是高价值补充信号，可加速重新验证或边界修正，但不应被理解为 ActivationLink 演化的必要条件。

**repeated_success**：同类问题在相似场景下多次通过同一路径成功，适合强化 ActivationLink 或实践路径。若多次事件还表现出相似的核心变量组合与 Working Model 组织方式，可作为 Study 提炼候选实践路径的结构信号。详见 `study.md` 第 7 节。

**repeated_failure**：同类问题在相似场景下多次通过同一路径失败，适合降权、淘汰或重组路径。

**knowledge_gap**：回答或检索暴露缺失事实、缺失上下文或材料侧不足，形成可追踪的知识缺口。

**wiki_update_candidate**：底层知识、Concept 边界或 ActivationLink 变化，使某 Wiki 页面可能需要重编译或调整适用边界。

**concept_boundary_signal**：Concept 在分类、导航或激活中出现混淆、过宽、过窄，需要拆分、合并或收窄边界。

同一问题处理可能产生零个、一个或多个 Learning Event，取决于实际暴露的学习信号，而不是处理深度。

## 4. Learning Event 的最小记录内容

Learning Event 只记录最小必要信息，避免过度解释。

每条事件应能回答：发生了什么、涉及谁、结果如何、Study 可以据此做什么。

关注的设计概念包括：

```text
事件类型；
问题所属场景、目标和认知视角；
涉及的 ActivationLink、KnowledgePoint、KnowledgeUnit、Concept、Wiki 页面；
最终采用的知识或路径；
结果状态：成功、失败、冲突、缺口、用户纠正；
支撑事件成立的证据来源；
用户反馈或系统结果信号；
对 Study 的学习提示；
事件可靠性或置信度。
```

这些是设计概念，不是字段清单。记录应足够让 Study 判断是否需要强化、降权、淘汰、标记冲突、触发重编译或暂缓学习，而不必复述完整问答过程。

Learning Event 不必包含「为什么模型这样推理」或「为什么某步检索发生」。学习提示应描述事实信号，例如「该 ActivationLink 在相似场景中第三次失败」，而不是叙事性复盘。

## 5. Trace 的边界

Trace 不服务于在线回答。

Trace 不应该阻塞回答流程。

Trace 不应该扩大检索成本。

Trace 不应该让系统为了可解释性牺牲回答效果。

Trace 不应该导致系统记录一切、解释一切、学习一切。

Trace 不是：

```text
审计日志；
完整链路日志；
Chain-of-Thought；
复盘系统；
Agent 任务执行日志；
模型推理解释器。
```

Trace 与在线回答解耦：回答完成后，根据学习价值异步沉淀 Learning Event。为 Trace 而增加检索、扩展 KPN 或拉长 Working Model，违背 Trace 的设计定位。

## 6. Trace 与 Study 的关系

```text
Question
  -> Answer

如果产生学习价值
  -> Learning Event
  -> Study
  -> ActivationLink / KnowledgePoint / Concept / Wiki 调整
```

关系可概括为：

```text
Trace 是学习事件记录。
Learning Event 是 Study 的输入。
Study 是长期记忆调整机制。
```

Study 根据 Learning Event 中的事实样本执行强化、降权、淘汰、标记冲突、触发 Wiki 重编译等动作。Learning Reason 解释 Study 为何采取该动作，便于后续追踪、回滚和重评估。

没有 Learning Event 支撑的学习动作，不应直接改变长期记忆的稳定结构。单次事件通常不足以晋升 verified ActivationLink 或触发 Wiki 重编译；Study 应结合多次检索事件累积、证据来源，以及可选的用户反馈信号综合判断。ActivationLink 演化**不依赖**用户纠正，检索事件本身即可提供足够驱动。

## 7. Trace 与 Agent Runtime 的边界

Knowledge Brain 只负责知识、经验、证据和激活路径的积累。

完整复盘能力，例如：

```text
为什么一次任务失败；
为什么一个计划没有完成；
为什么一次决策偏差；
如何改进行动策略；
如何调整任务执行流程；
```

这些属于 Agent Runtime 或上层任务系统。

Knowledge Brain 可以为 Agent Runtime 提供知识和证据，但不负责保存完整任务执行过程，也不负责替代 Agent 做复盘。

Agent 的任务日志、计划状态、工具调用链和行动策略调整，不应混入 Knowledge Brain 的 Trace。若任务失败暴露了知识缺口、路径失效或 Wiki 适用边界问题，应提炼为 Learning Event，而不是把整段任务执行过程记入 Trace。

## 8. 总结

Trace 从「完整思考过程记录」收缩为「学习事件记录」。

一句话总结：

```text
Trace 只记录对长期记忆有学习价值的事件，为 Study 提供事实样本，不解释系统为何这样回答，也不承担任务复盘职责。
```
