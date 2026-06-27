# 反馈学习

知识大脑必须从使用中学习。

学习不是简单添加新文档，也不是复盘每次问答过程。

Study 是长期记忆学习机制：根据 Learning Event 调整长期记忆结构，尤其是 ActivationLink。

## 1. Study 的重新定位

Study 不是复盘模型思考过程。

Study 不是解释每次回答为什么这样生成。

Study 不是分析所有检索和推理步骤。

Study 不是完整反思系统。

Study 的输入是 Learning Event。

Study 的目标是根据 Learning Event 调整长期记忆结构，尤其是 ActivationLink，使长期记忆越来越可靠、边界越来越清晰、激活路径越来越有效。

Study 的目标不是让知识越来越多。

学习闭环是：

```text
Learning Event
  -> Study
  -> Learning Result
  -> ActivationLink / KnowledgePoint / Concept / Wiki 调整
```

Learning Event 由 Trace 记录，详见 `trace.md`。没有 Learning Event 支撑的学习动作，不应直接改变长期记忆的稳定结构。

## 2. 检索事件驱动的学习

知识大脑的有效学习**不依赖**用户持续纠正。实现者和读者不应误以为：没有 `user_correction` 或显式用户反馈，ActivationLink 就无法演化。

主学习燃料来自每次问题处理中自然产生的**检索事件**。用户反馈是补充加速，不是前提。

### 三类核心检索事件

```text
activation_success
  ActivationLink 命中，且其知识被实际采用、支撑了有效回答
  -> 累积正向证据，观察是否应在相似 scene / goal 下强化或晋升

activation_failure
  ActivationLink 命中，但未能支撑有效回答，或命中知识未被采用
  -> 标记路径风险；多次在相似条件下重复出现才推动降权或淘汰

activation_gap
  没有合适 ActivationLink，但补充查找找到了被采用的有效知识
  -> 暴露应形成候选链接的缺口；Study 可据此创建 candidate ActivationLink
```

这些事件在问答中自动产生，记录召回、采用和缺口事实，不需要用户额外表态。

### 累积与晋升

单次检索事件不足以改变稳定结构。Study 依赖跨事件的累积：

```text
同类问题在相似 scene / goal 下多次 activation_success
  -> repeated_success，推动 ActivationLink 强化或 candidate -> verified 晋升；

同类问题在相似条件下多次 activation_failure
  -> repeated_failure，推动降权、淘汰或边界收窄；

多次 activation_gap 且补充结果被采用
  -> 形成并验证 candidate ActivationLink，无需 user_correction。
```

即使没有用户反馈，上述累积也足以驱动 ActivationLink 的形成、强化、降权和淘汰。`user_correction` 在信号模糊或需要加速重新验证时尤其有价值，但应视为增强而非依赖。

### 与「正反馈」的关系

学习中的「正反馈」首先指**知识被实际采用并支撑了有效回答**（体现在 `activation_success` 中），而不是默认要求用户点赞或口头肯定。被召回但未采用的知识不应自动强化。

## 2.5 MVP 阶段：消费共现统计，形成 Wiki 与 ActivationLink 候选

MVP 阶段尚未积累 `activation_success / activation_failure / activation_gap` 等激活类事件，Study 的输入主要来自 Trace 积累的 `question_kp_cooccurrence` 统计表和 `knowledge_gap` 事件。

`question_kp_cooccurrence` 记录了每对「(问题关键词集合, KnowledgePoint)」的共现情况：

```text
hit_count       : 该 KP 在含这些关键词的问题中出现的次数（含 partial）
confident_count : 其中 Rerank 质量为 confident（直接证据且被引用）的次数
```

Study 定期扫描此表，执行以下动作：

**识别 candidate ActivationLink**

```text
条件：某 point_id 在多个不同 question_terms 下均积累了足够的 confident_count
      （阈值由配置控制，例如 confident_count ≥ 5 且 hit_count / confident_count ≥ 0.6）
动作：为该 KP 创建 candidate ActivationLink，关联的 question_terms 作为激活条件候选
说明：candidate 不参与正式召回，须经进一步使用验证后才能晋升 verified
```

**触发 Wiki 编译候选**

```text
条件：某主题（domain/concept）下多个 KP 都积累了较高的 confident_count，
      且这些 KP 之间存在 KPN 连接（related / hierarchical）
动作：生成 wiki_update_candidate 事件，建议将这批 KP 编译为 Wiki 页面
说明：Wiki 页面一旦形成，后续同类问题可直接命中，不再经历完整检索链
```

**消费 knowledge_gap 事件**

```text
条件：同一主题的 knowledge_gap 事件频繁出现（question_terms 高度相似的 gap 累积 ≥ N 次）
动作：标记知识缺口，供知识管理员补充材料
```

这一机制将每次问答的隐式信号转化为长期记忆调整，形成正向反馈：confident 积累 → ActivationLink 形成 → 同类问题跳过完整检索链 → 回答更快更稳。

## 3. Study 的主要任务

Study 根据 Learning Event 类型和累积信号，对长期记忆执行有针对性的调整。

Study 主要做：

```text
强化有效 ActivationLink；
降权失败 ActivationLink；
淘汰长期无效 ActivationLink；
根据 activation_gap 形成候选 ActivationLink；
根据 repeated_success 提升 ActivationLink 稳定性；
根据 repeated_failure 标记路径风险；
根据 knowledge_conflict 标记知识冲突；
根据 user_correction 触发知识重新验证；
根据 concept_boundary_signal 调整 Concept 边界；
根据 wiki_update_candidate 触发 Wiki 重编译候选；
根据 knowledge_gap 标记知识缺口；
根据 repeated_success 与结构相似信号形成候选实践路径。
```

这些任务分布在材料层、认知层和表达层，但核心焦点始终是：哪些路径有效、哪些知识可靠、哪些边界需要修正。

### 学习结果的层次

学习不是一个单一动作。

同一次或多次 Learning Event 可能同时暴露材料、认知和表达三个层面的信号：

```text
材料层学习：KnowledgePoint、KnowledgeUnit、来源证据的可靠性、重新验证与缺口；
认知层学习：Concept 边界、ActivationLink 状态与适用条件、实践路径；
表达层学习：Wiki 重编译候选、主题稳定性、适用边界更新。
```

三层学习都依赖 Learning Event，但调整的对象不同，演化速度也不同。

### 学习信号的使用原则

```text
被召回但未使用的知识不应自动强化；
被实际采用并支撑有效回答的路径（activation_success）才适合累积正向证据；
单次 Learning Event 不应直接重写长期记忆；
多次稳定检索事件才适合推动 ActivationLink 状态迁移和 Wiki 页面演化。
```

一次 activation_success 只说明路径值得观察。一次 activation_failure 可能来自证据不足或问题理解偏差，不应立即废弃整条 ActivationLink。只有当 repeated_success、repeated_failure 等检索累积信号在多次事件中反复出现，Study 才应推动状态迁移；`user_correction` 可加速这一过程，但不是必要条件。

## 4. Study 不做什么

Study 不做：

```text
模型思维链复盘；
回答生成过程解释；
检索每一步原因分析；
rerank 解释；
KPN 扩展过程解释；
Working Model 内部推理解释；
Agent 任务失败复盘；
行动计划优化；
完整人类反思能力模拟。
```

这些属于 Agent Runtime 或其他上层系统。

Knowledge Brain 的 Study 只处理对长期记忆有学习价值的事实信号，不保存完整任务执行过程，也不替代 Agent 做任务级复盘。

## 5. Learning Event 从哪里来

Study 的输入不是完整 Trace，也不是 Chain-of-Thought。

Study 的输入是 Learning Event：一次问题处理中，对长期记忆学习有价值的事件。

Learning Event 在以下情况产生。检索事件（前三类）是主驱动；其余类型来自使用过程暴露的缺口、冲突或边界问题；用户纠正是补充加速。

```text
【检索事件——主驱动】
ActivationLink 成功命中并被采用（activation_success）；
ActivationLink 命中但失败（activation_failure）；
激活路径缺口，但补充查找找到了被采用的有效知识（activation_gap）；
同类问题在相似条件下多次成功或失败（repeated_success / repeated_failure）；

【使用过程暴露的其他信号】
暴露知识缺口或知识冲突；
Concept 边界混淆、过宽或过窄；
Wiki 页面可能需要重编译或调整适用边界；

【用户反馈——补充加速，非必要】
用户明确纠正（user_correction）。
```

没有学习价值的问题处理，可以不产生 Learning Event，Study 也不必介入。

Learning Event 应保留与长期记忆相关的认知维度，供 Study 判断适用边界：

```text
场景；
目标；
认知视角；
思维模式；
注意焦点；
知识点相关性；
系统结果信号（采用、失败、缺口、冲突等）；
边界不匹配信号。
```

用户口头或显式反馈可以写入事件，但 ActivationLink 演化所依赖的「正反馈」首先指知识被实际采用并支撑有效回答，见第 2 节。

## 6. KPN 与 ActivationLink 学习

KPN 本身不直接形成稳定激活路径。

```text
KPN 连接 ≠ ActivationLink；
KPN 上下文被采用 ≠ 立即晋升为 ActivationLink。
```

当 activation_gap 或 repeated_success 表明某条补充路径在多次事件中稳定有效时，Study 可以形成候选 ActivationLink。

candidate ActivationLink 仍必须经过独立补充查找、实际采用、证据回链和反馈验证，才能晋升为 verified。Study 不应根据 KPN 扩散过程本身晋升链接。

## 7. 实践路径：从 Working Model 到 Experience Path

实践路径（Practice Path，在经验路径模式中调用）描述某类问题「如何组织思考」的可复用模板。它与 ActivationLink 不同：ActivationLink 回答「在特定条件下通过哪个概念激活哪些知识点」；实践路径回答「面对这类问题时，应按什么变量框架、步骤顺序和证据检查方式组织思考」。

Working Model 是一次性工作结构，不能被直接保存为长期记忆。但当某类问题的 Working Model 结构在多次 Learning Event 中被证明稳定有效时，Study 可以从事件中提炼结构模式，形成候选实践路径。

### 形成信号

典型信号包括：

```text
activation_success 反复出现，且问题处理走的是工作模型模式；
repeated_success 表明同类问题在相似场景下多次成功；
多次事件的 scene / goal / pattern / focus 高度相似；
Working Model 的核心变量组合与组织方式跨事件高度相似；
相似的问题类型、变量框架和推理步骤反复被采用且得到正反馈。
```

单次 activation_success 不足以形成实践路径。Study 的输入始终是 Learning Event，不是 Working Model 本身；它从事件中抽取可复用的结构摘要，而不是保存某次完整推理过程。

### 提炼内容

候选实践路径通常包含：

```text
适用任务类型和问题模式；
核心变量组合与建议检查顺序；
建议激活的领域、概念和知识点范围；
步骤框架、检查清单或复盘结构；
适用边界和已知失效条件。
```

一条实践路径可以引用多条 ActivationLink，但不等于某次 Working Model 的快照。

### 验证与晋升

候选实践路径必须经过独立验证，才能被经验路径模式可靠调用：

```text
在新问题中按模板组织思考，仍能产生有效回答；
不依赖某次特定材料或偶然组合的偶然成功；
适用边界清晰，失效时可回退到工作模型模式；
验证过程应记录为 Learning Event，而不是默认一次成功即晋升。
```

验证通过后，实践路径成为长期记忆中的稳定模板。认知路由在识别到匹配的任务类型、场景和风险水平时，可优先选择经验路径模式并激活该路径，而不是每次都从零构建 Working Model。详见 `cognitive-routing.md` 与 `working-model.md`。

### 与 Wiki 编译的关系

实践路径偏向「怎么做」的过程模板；Wiki 页面偏向「是什么」的稳定表达。二者都可能来自多次 Learning Event，但沉淀层级不同。反复有效的实践路径可以进一步成为 Wiki 中方法类页面的编译输入，但不应把 Wiki 页面直接当作经验路径模式的执行模板。

## 8. ActivationLink 质量控制

ActivationLink 不是「被使用过」就会自动稳定。

Study 对 ActivationLink 的目标，不是生成更多链接，而是筛选、验证、降权和淘汰链接。

### 有效性判断标准

一条 ActivationLink 是否值得保留或晋升，应综合以下标准判断：

```text
答案贡献度：没有该链接，答案质量是否明显下降；
场景匹配度：该链接是否只在特定问题类型、任务场景、领域上下文中有效；
证据独立性：是否由不同来源、不同知识单元、不同使用场景共同支持；
反例稳定性：是否在相似问题中多次被证明不适用；
替代路径竞争：是否存在更短、更准确、更稳定的激活路径；
边界清晰度：是否明确知道该链接适用和不适用的条件。
```

只有多条标准在多次 Learning Event 中共同支持，链接才适合从 candidate 向 verified 晋升。这些标准主要可从检索事件中观察：是否反复被采用（activation_success）、是否反复命中后失败（activation_failure）、是否在相似 scene / goal 下独立重复出现——均不需要用户显式反馈。

### 状态模型

```text
candidate：候选链接，只能辅助探索；
verified：已验证链接，可参与正式召回；
weakened：被降权链接；
conflicted：存在冲突的链接；
deprecated：不再推荐使用的链接。
```

Study 根据 Learning Event 推动链接在以上状态间迁移，而不是让链接数量持续增长。

```text
candidate 不能直接决定答案，只能辅助探索；
verified 可以参与正式召回，但不能永久免审；
weakened、conflicted、deprecated 都不应作为当前首选激活路径。
```

### 学习动作形态

Study 将 Learning Event 转化为长期记忆调整时，常见动作包括：

**强化**：反复有效的 ActivationLink 在特定场景、目标、思维模式和注意焦点下变得更易被采用。

**修正**：收窄链接或 Concept 的适用条件，或拆成更精确的激活路径。

**降权**：在特定维度或整体上降低无效、误导或边界过宽的链接权重。

**淘汰**：长期无效、无证据支撑或存在更优替代路径的链接转为 deprecated。

**补充**：knowledge_gap 形成新的学习目标或候选结构。

**重组**：Concept 拆分、合并，候选领域或概念晋升，激活路径按场景重组。

候选 ActivationLink 是待验证的学习假设，不得参与正式召回。它应带上从 Learning Event 中观察到的场景、目标、思维模式、注意焦点和适用边界，而不是粗糙的「概念到知识点」连接。

## 9. Learning Reason

Learning Reason 不解释完整思考过程。

Learning Reason 只解释 Study 为什么对长期记忆做出某个学习动作。

Learning Reason 的依据来自 Learning Event，而不是完整思维过程。

Learning Reason 用于说明：

```text
为什么强化某个 ActivationLink；
为什么降权某个 ActivationLink；
为什么淘汰某个 ActivationLink；
为什么形成候选 ActivationLink；
为什么形成或强化候选实践路径；
为什么标记知识冲突；
为什么触发知识重新验证；
为什么触发 Wiki 重编译候选；
为什么调整 Concept 边界。
```

Learning Reason 应能关联：

```text
触发来源：检索事件累积（activation_success / activation_failure / activation_gap、repeated_success / repeated_failure）、回答失败、证据冲突、外部材料变化；用户反馈可作为加速信号；
影响对象：ActivationLink、Concept、KnowledgePoint、KnowledgeUnit、Wiki 页面；
学习动作：强化、降权、淘汰、标记冲突、标记缺口、触发重编译等；
依据：来自哪些 Learning Event、哪些证据、哪些反馈；
边界：该学习结论适用于哪些认知视角和问题场景；
不确定性：是否还需要后续验证。
```

Learning Reason 不直接改变知识本身，而是解释学习动作的原因，为后续追踪、回滚、重评估提供依据。

## 10. Study 的输出边界

Study 的输出是长期记忆调整建议或结果，不是复盘报告。

Study 输出可以包括：

```text
ActivationLink 强化；
ActivationLink 降权；
ActivationLink 淘汰；
候选 ActivationLink；
候选实践路径；
KnowledgePoint 重新验证；
Concept 边界调整候选；
Wiki 重编译候选；
知识缺口；
知识冲突标记；
知识状态调整。
```

Learning Result 是 Study 对长期记忆做出的可追踪调整，应能回溯到支撑它的 Learning Event 和 Learning Reason。

Study 不输出「系统为什么这样回答」的叙事，不输出检索链路说明，不输出 Agent 任务复盘结论。

## 11. 学习需要谨慎

不是每个 Learning Event 都应立即改变长期记忆。

一次 activation_failure 可能来自问题表达、证据不足或推理错误，也可能来自知识本身错误。

Study 应结合事件类型、事件可靠性、多次累积信号和 Learning Reason，再决定强化、降权还是暂缓。

## 12. 总结

反馈学习让知识大脑不是越存越大，而是越用越准。

对 ActivationLink 而言，Study 的目标不是链接越多越好，而是让 verified 链接更少、更准、边界更清晰。

一句话总结：

```text
Study 根据 Learning Event 调整长期记忆，输出 Learning Result，而不是复盘思考过程或解释每次回答。
ActivationLink 演化主要由检索事件累积驱动，不依赖用户持续纠正。
```
