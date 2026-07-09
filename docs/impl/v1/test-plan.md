# V1 真实数据测试方案

## 目标

本方案用于验证 V1 的重点功能是否形成闭环：

```text
真实文档导入
  -> KU / KP / KPN / Concept 形成
  -> 问答产生 Trace 与 Learning Event
  -> Study 执行学习动作
  -> ActivationLink / Wiki / lifecycle 反过来改变检索与回答行为
```

测试必须使用真实 Markdown 文档，数据目录：

```text
/Users/jxu/Code/wiki-brain/data/sources/markdown
```

V1 的通过标准不是单个接口可用，而是系统能在真实知识库上证明：

- 过期知识不会被召回；
- 反复出现的问题能沉淀为 ActivationLink；
- verified ActivationLink 能让同类问题走快路径；
- 片段级证据能被原文校验并被 citation 使用；
- 跨 Source KPN 能扩展证据范围；
- Wiki 页面能被编译、发布、命中、失效重编译；
- 用户纠正能影响学习信号；
- Page 能展示和操作上述学习结果。

## 测试数据集

### 数据分组

从真实 Markdown 中按主题拆成 4 组测试集。

| 组 | 目标 | 真实文档 |
| --- | --- | --- |
| Oracle RAC 组 | ActivationLink、快路径、证据挖掘、Wiki、跨 Source KPN | `047d27a8-517f-4c14-8f44-a38de7979a00.md`、`07732b5d-a9e2-4979-9f0c-4c18fd9b9eec.md`、`1da847a0-81f2-4b10-a8c1-79b732530b6a.md` |
| 数据库高可用组 | 跨 Source KPN、概念区分、慢路径回退 | `099033f7-562d-408e-bfac-f95c9e99f500.md`、`3b6534ae-9b21-4661-aef3-130c40efea62.md` |
| 制度流程组 | 生命周期、片段级引用、跨制度概念对齐、Wiki | `d62dc60b-e6f6-4a4c-b10e-6fdf076f7b36.md`、`1763efad-2a18-4c76-bdab-ce6a8f171e9c.md`、`290e6756-dddb-4948-8478-87ddacf046b8.md`、`501c9039-3067-45b0-b0e5-7c11358ae912.md`、`526cbf3c-1481-4ba6-8757-697f65341251.md`、`199edbd6-128e-47c8-82d9-5dcc44805e3c.md` |
| 财务报销组 | 精确问答、片段摘选、相似制度区分 | `4fe3541b-b91e-4c8f-b32c-01af64fe0b35.md`、`501c9039-3067-45b0-b0e5-7c11358ae912.md` |

### 基线数据要求

执行测试前导入上述文档，确认：

```text
sources.status = completed
knowledge_units.lifecycle = current
knowledge_points.lifecycle = current
points / units index 可检索
每个 source 至少产生 1 个 KU 和 1 个 KP
```

若某个文档因真实内容质量导致部分 unit_extract 失败，可保留失败结果，但必须记录：

- 失败 Source；
- 失败阶段；
- 失败 KU 数；
- 后续测试是否避开该 Source。

## 测试环境

建议准备 3 套配置。

### A. Fake LLM 回归环境

用途：稳定验证状态机、生命周期、事件处理、回退逻辑。

要求：

```yaml
study:
  schedule_interval: "manual"
  auto_promote: false
  promote_success_min: 2
  promote_distinct_min: 2
  weaken_failure_min: 2
  weaken_ratio_min: 0.5

retrieval:
  fast_path: true
  fast_path_fallback: true

evidence:
  enabled: true
```

### B. 真实 LLM 验收环境

用途：验证真实文档上的语义质量、片段摘选质量、Wiki 编译质量。

配置使用生产默认阈值，只允许降低 Study 调度间隔，不降低晋升、降权、Wiki 阈值。

### C. 对照评估环境

用途：比较快路径与完整链路。

要求支持：

```text
POST /retrieval { force_full: true }
retrieval.fast_path = true / false 可切换
日志记录每次 LLM 调用次数
traces.path_type 写入 fast / full / wiki
```

## 验收指标

| 指标 | 目标 |
| --- | --- |
| 导入成功率 | 选定真实文档的 Source 完成率 >= 90% |
| 生命周期过滤 | superseded / deprecated 的 KU、KP 不出现在候选、证据、Wiki 输入中 |
| ActivationLink 学习 | 达标共现或 activation_gap 能创建 candidate；人工确认后变 verified |
| 快路径调用数 | verified 命中问题的 LLM 调用数 <= 2 |
| 快路径质量 | 同一问题集 fast 与 force_full 的 direct_point_ids 命中核心 KP 一致或可解释 |
| 证据挖掘 | mined=true 的 content 必须是原 Markdown 原文子串；幻构片段不进入 EvidenceSet |
| Study 审计 | 每个 create_candidate / promote / weaken / wiki_candidate 都有 learning_result 和 reason |
| Wiki 直答 | 已发布 Wiki 命中后 path_type=wiki，LLM 调用数 <= 1，citation 可回链 KP / KU / Source |
| 用户纠正 | user_correction 能按 correction_weight 影响后续降权或重新验证 |
| Page 可操作 | ActivationLink 确认/驳回、Wiki 发布/重编译、Learning Result 回溯可用 |

## 测试阶段

### 阶段 1：真实文档导入与基线检索

目的：确认 V1 后续测试的真实知识库可用。

步骤：

1. 清空测试库，导入测试数据集中的真实 Markdown。
2. 等待 source_process、unit_extract、KPN、Concept match 完成。
3. 查询 Source、KU、KP、Concept、KPN 计数。
4. 对每组文档执行 3 个基线问题，强制 `force_full=true`。

建议问题：

| 问题 | 期望来源 |
| --- | --- |
| Oracle RAC 安装前需要准备哪些网络地址？ | Oracle RAC 安装部署文档 |
| Oracle RAC 如何开启归档？ | Oracle RAC 开启归档文档 |
| Weblogic 通过 SCAN IP 连接 RAC 报错如何处理？ | Oracle RAC 问题汇总 |
| 无合同立项需要哪些审批？ | 无合同立项申请与审批规范 |
| 无合同项目延期签约需要提前多久申请？ | 无合同立项申请与审批规范 |
| 出差乘坐飞机什么情况下可以申请？ | 差旅费报销制度 |

通过标准：

```text
回答有 citation；
EvidenceSet.path_type = full；
direct evidence 指向真实 Source；
非相关主题文档不进入 direct evidence。
```

### 阶段 2：Lifecycle

目的：验证 reupload、delete 与检索过滤。

用例 L1：reupload 成功换血

1. 选择 `d62dc60b-e6f6-4a4c-b10e-6fdf076f7b36.md` 作为原 Source。
2. 复制该文档生成测试新版，将“预计签约日期红线”附近内容改成明显可检索的新表述。
3. 调 `POST /sources/:id/reupload` 上传新版。
4. 处理期间查询原问题“无合同项目延期签约要求是什么？”。
5. 等待影子 Source 处理完成。
6. 再次查询同一问题。

通过标准：

```text
处理期间旧 KU/KP 仍为 current 且可回答；
换血完成后旧 KU/KP 为 superseded；
新 KU/KP 为 current；
检索结果只引用新 current 内容；
影子 Source 不出现在 GET /sources 与 Source 过滤候选中。
```

用例 L2：reupload 失败不影响原 Source

1. 上传一个无法正常处理的损坏 Markdown 或空文件作为新版。
2. 查询原问题。
3. 调 `/sources/:id/reupload/retry`。

通过标准：

```text
影子 Source status=failed；
原 Source 的 KU/KP 仍为 current；
原问题仍可回答；
retry 只续跑影子，不创建可见重复 Source。
```

用例 L3：删除 Source

1. 删除财务报销组中的 `4fe3541b-b91e-4c8f-b32c-01af64fe0b35.md` 对应 Source。
2. 查询“日常费用报销期限是什么？”。

通过标准：

```text
该 Source 全部 KU/KP 为 deprecated；
不再被 units / points / outline / KPN 召回；
历史 answer 的 evidence_snapshot 仍可反查原文。
```

### 阶段 3：证据挖掘

目的：验证 V1 citation 从 KU 级收敛到片段级。

用例 E1：真实片段摘选

问题：

```text
Oracle RAC 开启归档需要执行哪些核心步骤？
```

期望来源：`1da847a0-81f2-4b10-a8c1-79b732530b6a.md`

通过标准：

```text
EvidenceItem.mined = true；
content 是 Markdown 原文子串；
source_ref.line_start / line_end 能定位到原文；
fact_id 是片段级，不是整段 KU 级；
Answer citation 引用 mined fact_id。
```

用例 E2：表格和制度条款摘选

问题：

```text
无合同立项申请需要提供哪些关键信息？
```

期望来源：`d62dc60b-e6f6-4a4c-b10e-6fdf076f7b36.md`

通过标准：

```text
摘选内容覆盖项目信息、客户情况、预计签约信息、竞争情况、立项依据；
不把整章制度全文作为 direct evidence；
行号覆盖连续原文片段。
```

用例 E3：幻构拦截

在 Fake LLM 环境让 evidence_mine 返回一段原文不存在的片段。

通过标准：

```text
不存在片段被丢弃；
direct 候选整段回退 mined=false；
supporting 候选被丢弃；
回答链路不中断。
```

### 阶段 4：ActivationLink 与 Study 学习闭环

目的：验证事件积累能转化为 ActivationLink，并改变检索行为。

用例 A1：activation_gap 创建 candidate

1. 清空 activation_links 与相关 learning_events。
2. 连续用不同问法询问 Oracle RAC 归档：

```text
Oracle RAC 怎么开启归档？
RAC 数据库启用归档模式的步骤是什么？
在 Oracle RAC 中如何验证归档已经开启？
```

3. 首轮无 verified 链接，应走 full path。
4. 确认 direct evidence 命中 `1da847a0-81f2-4b10-a8c1-79b732530b6a.md`。
5. 手动触发 `POST /study/run`。

通过标准：

```text
产生 activation_gap 或共现统计；
Study 创建 candidate ActivationLink；
learning_results.action = create_candidate；
reason 包含 confident_count / ratio 或 gap 触发来源；
candidate 不参与正式快路径。
```

用例 A2：人工确认晋升 verified

1. 继续用至少 2 个不同 question_hash 命中同一 candidate 的目标 KP。
2. 运行 Study。
3. 在 Page 或 API 中确认 pending promote。

通过标准：

```text
learning_results.action = promote 且 status=pending_confirm；
confirm 后 ActivationLink.status = verified；
confirmed_by = manual；
状态迁移写入 learning_results；
candidate -> verified 以外非法迁移被拒绝。
```

用例 A3：快路径生效

1. 使用与 A2 相似但非完全相同的问题：

```text
RAC 要开启归档，先关库还是直接 alter database archivelog？
```

2. 不设置 `force_full`。

通过标准：

```text
path_type = fast；
activation_hits 包含 verified link_id；
LLM 调用数 <= 2；
直接证据来自目标 KP 所属 KU；
answer citation 有片段级证据；
trace 产生 activation_success。
```

用例 A4：快路径失败回落与降权

1. 构造一个同主题但约束不同的问题，例如要求 SQL Server AlwaysOn 归档或备份策略。
2. 若 ActivationLink 错误命中，观察回落。
3. 多次产生 activation_failure 后运行 Study。

通过标准：

```text
快路径回答失败后只回落一次 full path；
最终 trace.path_type = full；
activation_hits 保留原命中；
产生 activation_failure；
达到阈值后 verified -> weakened；
weakened 不再参与 Match。
```

### 阶段 5：跨 Source KPN

目的：验证不同 Source 的相关 KP 能建立关系，并在检索时扩展 evidence。

用例 K1：Oracle RAC 跨文档关联

1. 确认 Oracle RAC 安装文档、问题汇总、归档文档均已导入。
2. 对任一 Oracle RAC Source 手动触发：

```text
POST /sources/:id/kpn-cross
```

3. 查询任一 Oracle RAC KP 的 relations。

通过标准：

```text
产生 scope=cross 的 related 关系；
重复触发不产生重复关系；
非 current KP 不参与配对；
GET /points/:id/relations 可按 scope=cross 查询。
```

用例 K2：跨源 supporting evidence

问题：

```text
Oracle RAC 维护时如何判断集群和 ASM 状态？
```

通过标准：

```text
direct evidence 来自 RAC 安装或维护片段；
supporting evidence 可经 cross KPN 扩展到其他 Oracle RAC Source；
EvidenceSet 中 role=direct/supporting 区分正确。
```

用例 K3：contradicts 报告

在 Fake LLM 环境让跨源匹配返回一条 contradicts。

通过标准：

```text
关系写入 relation_type=contradicts, scope=cross；
下一期 Study 报告出现 cross_source_conflicts；
不自动影响回答或生命周期。
```

### 阶段 6：Wiki 编译、发布、直答与重编译

目的：验证 Wiki 从候选到检索接入的最小闭环。

用例 W1：Wiki 候选

1. 对 Oracle RAC 组重复执行归档、安装准备、SCAN IP、ASM 状态等问题。
2. 运行 Study。

通过标准：

```text
Study 对稳定 KP 簇产生 wiki_candidate；
learning_results.object_type = wiki_page；
status = pending_confirm；
reason 可追溯触发 KP / event。
```

用例 W2：编译与发布

1. 对 W1 的候选调用 `POST /wiki/compile`。
2. 检查 draft 页面。
3. 调 `POST /wiki/pages/:id/publish`。

通过标准：

```text
页面包含固定四节：稳定结论 / 展开说明 / 待验证点 / 依赖来源；
content 中 [point_id] 均来自输入白名单；
wiki_revisions 写入首版；
published 页面进入 wiki index。
```

用例 W3：Wiki 直答

问题：

```text
总结一下 Oracle RAC 运维里最常见的安装、归档和连接问题。
```

通过标准：

```text
命中 wiki index；
path_type = wiki；
LLM 调用数 <= 1；
citations 使用页面中的 point_id；
evidence_snapshot 可从 point_id 回链到 KU / Source；
Wiki sufficient=false 时能回落激活层或慢路径。
```

用例 W4：底层知识变化触发重编译

1. reupload Wiki 依赖的任一 Oracle RAC Source。
2. 等待 lifecycle 换血完成。

通过标准：

```text
旧依赖 KP 变 superseded；
已发布 Wiki status = needs_recompile；
页面从 wiki index 删除；
相同问题不再走旧 Wiki 直答；
人工 recompile 后生成新 revision 并可重新发布。
```

### 阶段 7：用户反馈通道

目的：验证 user_correction 是学习加速信号。

用例 F1：有用反馈

1. 对已 verified 的 Oracle RAC 快路径问题提交“有用”。
2. 运行 Study。

通过标准：

```text
反馈写入 learning_events；
关联 answer_id 与采用 KP；
不会单次直接改变 ActivationLink 状态。
```

用例 F2：纠正反馈触发降权

1. 对同一 verified 链接的回答提交纠正说明，例如“这个回答引用的是归档步骤，但我问的是 SCAN IP 连接问题”。
2. 重复到达到 correction_weight 后的降权阈值。
3. 运行 Study。

通过标准：

```text
user_correction 计入 failure_n；
learning_results.reason 体现 correction_weight；
达到阈值后 verified -> weakened；
后续相似问题不再走该链接快路径。
```

### 阶段 8：Page 与审计视图

目的：验证人工监督闭环不是只停留在 API。

检查项：

```text
ActivationLink 管理视图按 candidate / verified / weakened / deprecated 分列；
candidate 可人工 confirm / reject；
每个链接能看到 question_terms、条件字段、目标 KP、adopt/fail 统计、Learning Reason；
Wiki 视图可确认候选、查看 draft、发布、查看 needs_recompile、查看 revision；
学习动作审计视图可从 learning_result 回溯到 event_ids；
问答界面展示 path_type：wiki / fast / full；
问答界面可提交有用 / 纠正反馈。
```

通过标准：

```text
Page 操作结果与 API / DB 状态一致；
刷新后状态不丢失；
非法操作有清晰错误提示，例如 confirmed link 不能 reject。
```

## 回归测试矩阵

| 模块 | Fake LLM | 真实 LLM | 必测真实数据 |
| --- | --- | --- | --- |
| Lifecycle | 必测 | 必测 reupload 成功路径 | 制度流程组、财务报销组 |
| Evidence Mining | 必测幻构、失败回退 | 必测片段质量 | Oracle RAC 组、制度流程组 |
| ActivationLink | 必测状态机 | 必测真实问法沉淀 | Oracle RAC 组 |
| Retrieval 快路径 | 必测 fallback | 必测调用数和质量 | Oracle RAC 组 |
| Study | 必测阈值与审计 | 必测真实学习结果 | Oracle RAC 组、制度流程组 |
| KPN Cross | 必测去重和 contradicts | 必测 related 质量 | Oracle RAC 组、数据库高可用组 |
| Wiki | 必测校验失败 | 必测编译质量和直答 | Oracle RAC 组、制度流程组 |
| Feedback | 必测权重 | 抽测 | Oracle RAC 组 |
| Page | 必测操作流 | 必测人工确认流 | 全部 |

## 测试产物

每次完整测试需保存以下产物：

```text
docs/impl/v1/test-runs/<date>/import-summary.json
docs/impl/v1/test-runs/<date>/questions.jsonl
docs/impl/v1/test-runs/<date>/answers.jsonl
docs/impl/v1/test-runs/<date>/traces.jsonl
docs/impl/v1/test-runs/<date>/learning-results.json
docs/impl/v1/test-runs/<date>/activation-links.json
docs/impl/v1/test-runs/<date>/wiki-pages.json
docs/impl/v1/test-runs/<date>/metrics.md
```

`questions.jsonl` 建议字段：

```json
{
  "case_id": "A3",
  "question": "RAC 要开启归档，先关库还是直接 alter database archivelog？",
  "expected_sources": ["1da847a0-81f2-4b10-a8c1-79b732530b6a.md"],
  "expected_path_type": "fast",
  "force_full": false
}
```

`metrics.md` 至少包含：

```text
导入 Source 数、完成数、失败数；
KU / KP / Concept / KPN 计数；
learning_events 按类型计数；
learning_results 按 action / status 计数；
activation_links 按状态计数；
full / fast / wiki path 分布；
平均 LLM 调用数；
mined evidence 比例；
片段 citation 比例；
快路径 vs force_full 对照结果；
失败用例清单与原因。
```

## 退出标准

V1 版本可进入验收的最低条件：

```text
阶段 1-5 全部通过；
阶段 6 Wiki 至少在 Oracle RAC 组通过；
阶段 7 用户纠正可在 Fake LLM 环境稳定触发降权；
阶段 8 Page 人工确认和审计链路可用；
所有失败用例均有明确归因：实现缺陷、真实数据质量、LLM 输出不稳定或阈值需校准；
不存在 superseded / deprecated 知识被正式回答引用的阻断问题；
不存在 citation 指向无法回链原文的阻断问题；
不存在 verified 快路径无法回落导致错误空答的阻断问题。
```
