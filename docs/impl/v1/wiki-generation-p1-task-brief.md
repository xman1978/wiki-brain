# 编码任务指令：Wiki 编译链路重构（切面聚类 + 提纲 + 逐节生成）

> **状态：已过时，被 `docs/impl/v1/wiki-generation-simplify-task-brief.md` 取代。**
> 本文件描述的提纲合成 + 逐节生成架构已确认测试门槛过高、收益不足以覆盖复杂度，
> 方案改为保留切面聚类、撤回提纲/逐节拆分、恢复两次整页 LLM 调用。本文件仅保留
> 供追溯，**不要按本文件继续实现或扩展**；新的编码任务请看上面那份文件。

> 这是一份交给编码会话的任务说明，不是设计文档。设计与实现口径**一律以下列文档为准**，
> 本文件只负责给出范围、顺序、约束与验收门。文档与本文件冲突时以文档为准；
> 文档本身有歧义时**停下来问用户**，不要按直觉补全。

## 任务

把 Wiki 概念页编译从「一次 LLM 把一堆 KP 写成一整页」改造成「先派生切面结构 →
生成提纲 → 逐节生成 → 装配」，解决"KP 堆砌"问题。这是 `docs/impl/v1/
wiki-generation.md` 分期计划里的 **P1**，P0（支持度核验、发布前质量门、
aliases/trigger_questions 程序化、概念内聚度的最小子集）已经实现。

**必读文档**（先全部读完再动手，不要边读边写）：

```text
docs/impl/v1/wiki-generation.md   全文，重点第 1-4、6、11-14 节；
                                  第 12 节「与 wiki.md 的冲突清单」是本任务
                                  改动面的权威清单；第 13 节记录了 P0 已实现
                                  的具体范围，不要重做。
docs/design/wiki-compilation.md   「编译内部分三步」「编译产物的支持度核验」
                                  「编译产物的发布前验收」「各层对象的职责
                                  区分」（切面的定位）
docs/impl/v1/wiki.md              数据结构 + 步骤 2、3、5（现行实现，本任务
                                  要替换/扩展的基线）
CLAUDE.md                         「V1 关键设计决策」全部条目，尤其 Wiki 相关的几条
```

## 第 0 步：确认基线（不要跳过）

```text
1. go build ./... && go test ./... 先跑一遍，确认基线全绿。
   上一轮改动（P0：aliases/trigger_questions 程序化、支持度核验、发布前
   质量门、internal/foundation/graph 的 Louvain 社区检测、概念内聚度接入
   study 的 ready 判定）是在没有 Go 工具链的环境里写的，从未实际编译/测试
   过。如果基线不绿，先把基线修绿、报告修了什么，再开始本任务，不要在红
   的基线上叠加新代码。
2. 已修改但可能有编译期问题的文件集中在：
   internal/wiki/{service.go,store.go,types.go,handler.go,service_test.go,testhelpers_test.go}
   internal/study/{service.go,store.go,types.go,service_test.go}
   internal/foundation/graph/{louvain.go,louvain_test.go}（新文件）
   internal/foundation/config/config.go、config/config.yml
   cmd/server/main.go（study.NewService 调用点新增了 CohesionConfig 参数）
   config/prompts/{wiki_compile.md,wiki_claim_verify.md}
3. 仓库里还有一批与本任务无关、更早的未提交改动
   （internal/activation、internal/concept、internal/domain 等，domain/concept
   管理相关）。不要动它们，也不要把它们当成本任务基线的一部分去改。
```

## 现状（P0 已实现，作为本任务的起点）

```text
qualifying KP 定义、citation 白名单三道闸门、四节结构（## 稳定结论 /
## 展开说明 / ## 待验证点 / ## 依赖来源）—— 不变，本任务只在此基础上加
第五节「## 摘要」，四节校验逻辑不删除任何一条，只追加。

已存在、可以直接复用的构件：
  internal/foundation/graph.Communities / .Modularity / .LargestShare
    —— 带权 Louvain 社区检测，纯函数，已有单测，本任务的切面聚类直接调用它，
    不要重新实现一遍聚类算法。
  internal/study/store.go Store.PairSignals(pointIDs, wRel, wCooc, coocSat)
    —— 目前只有 KPN 关系 + 共现两路信号，供 study 侧概念内聚度判定使用。
    本任务需要给 wiki 包也建一个类似的边构造函数，但要补全第三路信号
    （ActivationLink observed_conditions 的 intent Jaccard）和第四路
    （同 KU 加成）——wiki-generation.md 2.1 节的完整边权公式，不是
    study.PairSignals 的简化版。两处实现可以共享 graph.Edge 类型，
    不需要合并成一个函数（study 算的是"概念内聚度"这个标量，wiki 算的是
    "切面怎么分组"这个结构，用途不同）。
  internal/wiki/store.go Store.ConceptAliases / ConfidentQuestionsForPoints
    —— 已经是程序化取数，直接沿用，不要改回 LLM 生成。
  internal/wiki/service.go Service.verifyClaims / Selfcheck / uncitedSentenceRate
    —— P0 的支持度核验与质量门，本任务重构 compileContent 之后这两处的
    调用方式要跟着调整（claims 从「整页 claims」变成「按 section 归属的
    claims」），但两个方法本身的逻辑不用重写，只需确认调用点仍然正确传参。
```

## 实现顺序（严格按序，每步 `go test ./...` 全绿后再进入下一步）

```text
1. Migration（wiki-generation.md 6.2/6.4）
     wiki_pages 增 summary（TEXT NOT NULL DEFAULT ''）、
     aspects（TEXT NOT NULL DEFAULT '[]'）；
     新建 wiki_page_sections 表（见 6.4 的 CREATE TABLE，含
     section_id/ordinal/heading/purpose/aspect_ids/point_ids/claims/
     answers_questions/body/degraded）。
     migration 编号取当前最大值 + 1（P0 用到了 039，这里从 040 起）。

2. 切面聚类（wiki-generation.md 阶段 B，2.1-2.4）
     internal/wiki 包内新增边构造函数（读 qualifying KP 的 KPN 关系、
     question_kp_cooccurrence 共现、verified ActivationLink 的
     observed_conditions.intent Jaccard、同 unit_id），调用
     graph.Communities 得到切面划分；
     切面命名（2.3，程序给建议名，供提纲阶段 LLM 改写）；
     后处理：aspect_min_size/aspect_max_size 的合并/拆分/misc 兜底
     （2.2 最后一段）。
     单测：确定性（同输入同输出）、min/max size 后处理生效、
     misc 桶不单独成节。

3. 提纲生成替代 analyzeClaims（wiki-generation.md 阶段 C，3.1-3.4）
     新增 config/prompts/wiki_outline.md（替代 wiki_analyze.md，注意不是
     删除 wiki_analyze.md——概念页/主题页当前共享同一套 analyze/compile
     入口的历史包袱要先看清楚，wiki_analyze.md 是否还被别处引用，若无引用
     再删，否则保留并标注废弃）；
     AnalyzeResult 结构变更为 { title, lead, sections[], headline_claims[],
     tensions[] }（3.2 的 JSON 形状）——**这是 breaking change**，
     internal/wiki/types.go 的 Claim/AnalyzeResult 相关类型、
     handler.go 的 /wiki/compile/analyze 响应组装都要跟着改；
     提纲后校验（3.4）：白名单、跨节去重、min/max sections。
     单测：越界 point_id 被剔除、跨节重复 point_id 只保留首个、
     claims 全空时重试一次后失败。

4. 逐节生成替代 compileContent（wiki-generation.md 阶段 D + 阶段 F）
     新增 config/prompts/wiki_section_compile.md；
     compileContent 重写为按 section 并发调用（受
     wiki.section_concurrency 限流，见 4.1-4.3）；
     节级校验（4.4，越界标注剔除、空节重试一次后 degraded 而非整页失败）；
     装配阶段（6.1）：五节结构 —— ## 摘要 / ## 稳定结论 / ## 展开说明
     （下含各 section 的三级小节）/ ## 待验证点 / ## 依赖来源；
     hasRequiredSections 加一条「## 摘要」断言，其余四条不动；
     wiki_page_sections 落库（6.4）；wiki_pages.summary/aspects 落库；
     aliases/trigger_questions 沿用 P0 已有的程序化实现，不要改动。
     verifyClaims/Selfcheck 调用点跟进新的 claims 归属结构（见上面
     「现状」一节）。
     单测：正文包含五个固定小节标题；同一 point_id 不跨节重复引用；
     单节失败只重试该节，不影响其他节产出；LLM 调用次数 == section 数
     （可用 fake.Calls() 断言）。

5. Handler + 前端同步（wiki-generation.md 第 11 节）
     handler.go 的 analyze/compile 响应体跟随第 3 步的结构变更；
     web/index.html 里 Wiki 编译相关的确认/展示界面要跟着改
     （claims 列表 → sections + headline_claims 展示，新增 lead/摘要展示）；
     前端改动范围：先 grep web/index.html 里引用 wiki_analyze/wiki_compile
     响应字段（claims、tensions）的地方，逐处确认新字段名。
```

## 开工前必须向用户确认的两处（文档留白或有取舍，不要自行决定）

```text
A. wiki_analyze.md / wiki_compile.md 是否删除？
   如果这两个 prompt 文件除了本模块（概念页一阶编译）之外，还被主题页
   二阶编译或其他路径引用，删除前必须先确认调用方全部切到新 prompt；
   如果确认无其他引用，直接删除，不要留着造成两套并行实现。

B. LLM 调用预算从 2 次涨到约 10-12 次，HTTP 超时如何处理？
   wiki-generation.md 第 0 节写了"编译不在问答关键路径上，代价可接受"，
   但现有 POST /wiki/compile 是同步执行、超时上限 120s（见 wiki.md 步骤 2）。
   逐节并发（wiki.section_concurrency）能缓解但不一定够。建议方案：
   把超时上限放宽到 300s，或者把 compile 挪进异步队列（参考
   source_process/unit_extract 的 task 模式）——挪队列是更大的改动，
   建议先放宽超时验证可行性，队列化作为后续优化，但这个取舍要问用户，
   不要自己定。
```

## 硬约束（最容易被"顺手"违反的条目，逐条对照）

```text
四节固定结构（稳定结论/展开说明/待验证点/依赖来源）一条不能少，只能加
  第五节「摘要」——不要因为改成逐节生成就顺手调整既有四节的名称或顺序。

citation 白名单只能收窄，不能放松：节级白名单 ⊆ 提纲阶段该节的
  point_ids ⊆ 概念级 qualifying KP 全集，三层递进，不允许跳级放宽。

切面划分由程序算，不由 LLM 提议或调整结构本身——LLM 在提纲阶段只能
  「改写切面标题、合并高度重叠的切面、指出某切面不值得成节」，不能把
  一个切面的知识点拆散到多个小节（wiki_outline.md 的 prompt 约束原文）。

source 只作为材料标注（哪条论断有几个独立来源支持），不能拿来当分节依据
  —— 按 source 分节是本方案要修正的原始 bug，不要在重构里重新引入。

contradicts 关系在切面聚类的建图阶段计正权，不是负权（2.1 节，
  "互相矛盾的两条 KP 恰恰在讲同一件事"）——不要"直觉修正"成负权。

qualifying KP 定义、四节结构、编译需人工确认、无 draft→page 回写、
  KPN 只有 2 种关系、lifecycle 只有 3 种状态——这些既有约束一条不动，
  详见 CLAUDE.md「V1 关键设计决策」。

不属于本任务：阶段 H 增量重编译（P2）、主题页二阶编译改用 Louvain 与
  摘要输入（P3）——不要顺手把这两块也做了，各自的既有实现（连通分量、
  取成员全文）保持不变。
```

## 沿用的既有工程约定（CLAUDE.md）

```text
包结构 internal/<module>/；migration 按版本号追加；
所有 LLM 调用经 LLMClient interface，测试用 fake client，不发真实网络请求；
位置字段统一 line_start / line_end（1-based inclusive），禁止 char/byte offset；
Prompt 文件放 config/prompts/，Schema 写在 prompt 的 ## Schema 段内；
错误处理返回标准业务错误类型，不用裸 errors.New；
回复使用与提问相同的语言；
不确定问题根因或是否可改动设计时，先问用户，不要凭直觉补全。
```

## 完成标准

以 `docs/impl/v1/wiki-generation.md` 第 14 节「完成标准」的
「切面聚类」「提纲与逐节」两段为准，逐条可验证；另外补三条本任务特有的
回归断言：

```text
go test ./... 全绿，且 internal/wiki、internal/study 既有测试
  （尤其 P0 新增的 TestVerifyClaims_*、TestPublish_*Selfcheck*、
  TestService_WikiCandidates_LowCohesionSplitSignal）不因本次重构而失败；
citation 白名单三层递进关系可用测试断言（节级 ⊆ 提纲级 ⊆ 概念级）；
POST /wiki/compile/analyze 的响应结构变更后，web/index.html 对应界面
  能正常渲染 sections/headline_claims/lead，不留对旧 claims 字段的死引用。
```
