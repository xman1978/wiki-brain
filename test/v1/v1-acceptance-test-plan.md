# Wiki-Brain V1 验收测试方案（真实数据）

用 `test/markdown/` 下 21 份真实文档（10 篇管理制度 + 11 篇技术文档）验证 V1 是否达成 `docs/impl/v1/readme.md`「V1 成功标准」的 8 项目标，并验证目标在**制度、技术两个差异极大的领域**下均成立。测试为黑盒验收：通过 HTTP API 操作，通过 API 响应、SQLite 表、日志三个观察面核对。

## 1. 测试目标与成功标准映射

| # | V1 成功标准 | 对应测试阶段 |
|---|------------|------------|
| 1 | 学习转化闭环（gap/共现 → candidate → 人工晋升 verified → 快路径命中） | P2、P3 |
| 2 | 检索行为改变（熟悉问题 LLM 调用 1-2 次，延迟下降，direct 命中率不降） | P3 |
| 3 | 学习可审计（状态迁移可回溯 Learning Result / Reason / Event） | P2、P5、P9（贯穿核对） |
| 4 | 引用精确（片段级引用，幻构被原文校验拦截，回退可观察） | P1、P4 |
| 5 | 自我修正（人为制造失败 → repeated_failure → 降权 → 快路径回落） | P5 |
| 6 | 生命周期生效（Source 更新/删除后旧 KU/KP 退出回答） | P5、P6 |
| 7 | Wiki 单层编译（人工指定 entry_ids→编译→检索命中→底层变化→待重编译） | P8 |
| 8 | 反馈生效（user_correction 加速链接信号） | P9 |

另覆盖三项不在成功标准列表但属 V1 交付范围的能力：跨 Source KPN（P7）、激活条件的对象/约束硬性守门（F 组，`activation.md` 步骤 2——技术域"同问法不同产品"是守门失效最容易暴露的场景）、subject 同义词归一化（P11，2026-07-24 新增——P3 M3 当时记录的"覆盖靠积累，不靠模糊匹配"观测项，现已在 Match 侧落地为 `subject_synonyms` 表 + `SynonymResolver`，见 `activation.md` 附属表与步骤 3a）。

第四项：Wiki 页面关系 / 写作草稿 / 回流防护（P12，2026-08-19 重写，取代此前"两层架构扩展"，设计/实现见 `docs/impl/v1/wiki.md`）——页面关系派生（`related`/`contradicts` 两种）、写作草稿、回流防护，均为 P8 编译闭环之上的扩展能力，不改变标准 7 本身的判定口径。单层架构下不再有主题页候选/二阶编译/检索骨架注入这些能力，无 Wiki 预览 / cold-start 路径。

第五项：问题四元组归一化（P13，2026-08-12 新增，config-gated 默认关闭，见 `internal/activation/tuplenorm.go`、`docs/impl/v1/retrieval.md` 步骤 2）——`tryFastPath` 里、送入 Matcher/BundleMatcher/Wiki 四元组直答入口之前的归一化层，吸收 LLM 抽取的措辞抖动，不改变这三处入口本身"纯精确匹配"的判据。

第六项：ActivationBundle 跨 unit 歧义仲裁（P14，2026-08-12 部分落地，见 `internal/retrieval/fastpath_helpers.go`）——ActivationLink 快路径命中跨多个 knowledge_unit 时，先 consult ActivationBundle（不冲突则合并核心成员继续快路径，无覆盖则实时新建/加强 candidate Bundle），是"阶段 2"里率先接通的一个入口，不是完整阶段 2（`bundle_hits[]`、Trace 的 `bundle_success`/`bundle_failure`、成员置信度随线上流量自然收敛仍未接线，P14 轴二用人工种子验证仲裁分支本身）。

## 2. 测试数据画像

### 2.1 制度域（10 篇）

| 文档 | 主题域 | 特点（对测试的价值） |
|------|--------|--------------------|
| 培训积分管理办法 | 人力/培训 | 积分规则表格密集（旷课-5、课件输出+3 等），适合片段级证据与表行摘选测试 |
| 应收账款管理制度 | 财务/销售回款 | 数值规则多且有层次（延期≤3个月、催款函 90/180/270 天、提成 100%/50%/25%、历史回款 3/6/9 个月），适合高频问答积累信号 |
| 日常费用报销期限管理规定 | 财务/报销 | 篇幅短、事实清晰（招待费 45 天、其他费用 3 个月），适合做删除实验的靶子 |
| 差旅费报销制度 | 财务/报销 | 与报销期限规定同主题互补（超期需总经理签字），天然跨文档 related |
| 大模型开发测试基础平台使用暂行管理办法 | IT/平台 | 计费数字明确（A800 256 元/GPU Day、0.15 元/千 token） |
| 项目考核与激励制度 | 项目管理/激励 | 公式与系数表（绩效系数 1.1/1/0.8/0.5/0、奖金系数 6%~1%），与应收账款存在"回款→奖金发放"的天然跨文档关联 |
| 万相公文销售奖励制度 | 营销/激励 | 与项目考核（万相公文项目奖金系数 1%）跨文档关联；阶梯提货价表适合表行摘选 |
| 绩效管理制度 | 人力/绩效 | 与培训积分、项目考核构成"绩效"词条簇，供 Wiki 候选与一阶页测试 |
| 考勤管理管理制度 | 人力/考勤 | 修订类短文（9:30 前不记迟到、取消满勤奖），适合验证"制度修订"语义 |
| 无合同立项申请与审批规范 | 项目管理/流程 | 与项目考核弱关联 |

### 2.2 技术域（11 篇）

| 文档 | 主题域 | 特点（对测试的价值） |
|------|--------|--------------------|
| Docker Swam 集群部署 | 容器/编排 | 端口事实明确（2377/7946/4789），概念+操作混合 |
| K8S部署 | 容器/编排 | 架构概念 + 命令步骤 + YAML，长文档提取压力测试；与 Swarm 同属容器编排概念簇 |
| MYSQL 主从热备 | 数据库/高可用 | 原理（binlog→replaylog）+ 配置 + 故障处理，命令密集 |
| Oracle 11g RAC 集群安装部署维护环境 | 数据库/高可用 | 与 19c 文档同主题不同版本（RHEL 6.5 / 11.2.0.3 / ISCSI），跨文档对比测试主力 |
| Oracle 19c RAC 集群安装部署维护环境 | 数据库/高可用 | OL 7.7 / 19.3.0.0 / UDEV+AFD，与 11g 构成版本对照 |
| Oracle RAC 开启归档 | 数据库/运维 | 极短、纯命令步骤文档（srvctl 五步），适合做删除实验的技术侧靶子 |
| Oracle RAC 问题汇总 | 数据库/故障 | 症状→原因→处理结构（VKTM 高 CPU、TNS-12518 粘连位），适合追问与片段摘选 |
| SQL Server AlwaysOn 安装配置 | 数据库/高可用 | 端口（1433/5022）、域环境事实 |
| 达梦数据库优化 | 国产数据库/调优 | 参数表极密集（MAX_SESSION_STATEMENT、BUFFER 等 30+ 参数），表行摘选与信号积累主力 |
| 金仓数据库优化 | 国产数据库/调优 | 与达梦/神通构成"国产数据库优化"概念簇 |
| 神通数据库优化 | 国产数据库/调优 | 参数默认值明确（MAX_CONNECTIONS 128/65535），适合做 reupload 换血的技术侧靶子 |

### 2.3 跨文档关联预期（P7 验收依据）

- 制度域：应收账款《回款提成》↔ 项目考核《5.3 项目奖金发放》；培训积分《年终绩效奖金应用》↔ 项目考核《奖金计算办法》；日常费用报销期限 ↔ 差旅费报销制度；万相公文销售奖励 ↔ 项目考核（万相公文项目）→ 均预期 `related`（scope=cross）；
- 技术域：Oracle 11g RAC ↔ 19c RAC（同主题不同版本）；Oracle RAC 问题汇总/开启归档 ↔ 两篇 RAC 部署文档；达梦 ↔ 金仓 ↔ 神通（国产数据库优化，均有"查耗时 SQL、统计信息、内存参数"对应节）；Docker Swarm ↔ K8S（容器编排概念）→ 均预期 `related`；
- 21 篇文档间无天然矛盾，`contradicts` 用衍生文档制造，制度域、技术域各一组（见 P7 的 fixture 说明）。

## 3. 环境准备与配置

```text
前置：go build 通过、go test ./... 全绿、真实 LLM key 配置就绪
数据库：全新空库（删除旧 .db 与 Bleve 索引目录）
```

`config.yml` 测试期建议值（目的：把"反复问答天数"压缩到单日可完成，不改变判定逻辑）。**2026-08-13 起离散状态机（`candidate_confident_min`/`promote_success_min`/`promote_distinct_min`/`weaken_failure_min`/`auto_promote` 等）已随连续置信度机制整体废弃，以下按 `internal/foundation/config/config.go` + `config/config.yml` 当前实际字段重写**：

```yaml
study:
  schedule_interval:       "24h"   # 调大，统一用 POST /study/run 手动触发，保证时序可控
  create_confidence_min:   0.3     # 默认 0.55，压低创建门槛（mean_pre，见 study.md 步骤 1）
  create_width_max:        1.0     # 默认 0.03，放宽宽度门槛，配合上面压低的 min 让创建更容易触发
  correction_weight:       2       # 默认值，user_correction 关联链接按 N 次 failure 计
  observed_conditions_max: 50      # 默认值，ActivationLink 观测条件组上限
  prune_mean_max:          0.3     # 默认值
  prune_width_max:         0.02    # 默认值
  prune_sample_min:        8       # 默认值
  prune_idle_days:         30      # 默认值
  prune_stale_days:        90      # 默认值
retrieval:
  fast_path:               true
  fast_path_fallback:      true
  serving_confidence_min:  0.7     # 默认值：mean(cond) 达到此值才算 self_graded/trusted
  audit_sample_min:        5       # 默认值：够格评估 trusted 档所需的独立核实样本量
  explore_rate_low:        1.0     # 测试期调到 1.0：exploring 档必被抽中，避免探索概率把命中埋进随机性里
  explore_rate_self_graded: 0.10   # 默认值
  explore_rate_trusted:     0.03   # 默认值
evidence:
  enabled:                 true
wiki:
  synthesis_audit_rate:    0.05    # 默认值，纯观测性（不进 needs_recompile/selfcheck/publish 判据）
  # 2026-08-19：单层架构下 qualifying_min_days_active / topic_cluster_min_*
  # 等两层架构专属配置项已随改造删除，无需再调
```

`explore_rate_low` 测试期建议临时调到 `1.0`：连续置信度机制下，即使一条链接的某个条件已跨过 `serving_confidence_min` 进入 `exploring`（等等——`exploring` 档是"未跨过"`serving_confidence_min` 的档，本身就概率抽样服务，见下方术语说明），是否本轮真的走快路径仍按 `explore_rate_*` 概率抽样，默认 `0.15` 会让 M1/M2 这类"重复同一问法应稳定命中"的测试引入不必要的随机噪声；调到 `1.0` 消除这层噪声，不改变被测的匹配/服务逻辑本身。

**术语对照（新旧状态机映射，供全文档阅读时心里换算，来源 `docs/impl/v1/activation.md`「状态机」节 + `internal/activation/confidence.go`）**：

- 不再有全局的"打分是否命中"阈值判断；`activation_links.observed_conditions` 每条观测条件（唯一键为 subject/intent/audience/constraint 四元组）各自独立累计 `success_count`/`failure_count`（自证，来自每次快路径服务后的结果回写 `RecordOutcome`）与 `audited_success_count`/`audited_failure_count`（独立核实，来自后台按 `explore_rate_*` 概率抽样触发的慢路径对照校验 `RecordAuditOutcome`）；
- `mean(cond) = (success_count+1)/(success_count+failure_count+2)`（Beta(success+1, failure+1) 后验均值，新条件 0/0 时为 0.5）；
- 三档服务分层（`Tier`，由 `conditionTier` 计算，只在 `GET /activation-links/:id` 响应的 `conditions[].tier` 中可读，`status` 字段本身不区分档位）：`exploring`（`mean < serving_confidence_min`，仅按 `explore_rate_low` 概率被抽中试探）、`self_graded`（`mean ≥ serving_confidence_min` 但独立核实样本 `audited_success_count+audited_failure_count < audit_sample_min`，或核实均值未达标）、`trusted`（`mean ≥ serving_confidence_min` 且独立核实样本数 ≥ `audit_sample_min` 且核实均值也 ≥ `serving_confidence_min`）；
- `status` 字段是从 `observed_conditions` 派生的缓存摘要（`deriveAndPersistStatus`），**不是判断依据**：只要任一条件达到 `self_graded`/`trusted` 档，`status=verified`；若目标 KP `lifecycle != current`，无论条件档位如何强制 `status=deprecated`（生命周期覆盖优先于置信度，且**直接**到 `deprecated`，中间不经过任何"降权"中间态——`weakened` 状态已整体废弃）；否则 `status=candidate`；
- 没有任何"人工确认晋升"的端点。`POST /activation-links/:id/reject` 是唯一的人工干预端点，语义是"清空该链接全部观测条件、重新开始"（不是"驳回一个待确认的晋升提案"），任何状态下都可调用，调用后 `status` 重新派生（现有 KP 为 current 时回到 `candidate`）；
- 创建门槛同样换成 Beta 均值/宽度公式：`mean_pre`/`width_pre` 由 `question_kp_cooccurrence.confident_count`/`hit_count` 算出，`mean_pre ≥ create_confidence_min` 且 `width_pre ≤ create_width_max` 才创建 `candidate` 链接（`internal/study/service.go` `createCandidates`，替代旧的 `candidate_confident_min`/`candidate_ratio_min` 原始计数/比例门槛）；
- 清理机制是"剪枝"（`PruneCandidateConditions`，Study 步骤 3）而不是"降权转移状态"：低置信度或长期未见的单条 observed_condition 被直接从数组里删除（不是整条链接被标记 `weakened`），`learning_results` 记 `action=prune_condition`，`reason` 区分 `manual_reject`（人工）与自动剪枝原因（低分/idle/stale）。

**观察面**：

- API：`POST /answer`（响应 `path_type`）、`GET /traces`（`path_type`/`activation_link_ids`）、`GET /activation-links`（列表，字段含 `status`/`adopt_count`/`fail_count`，均为派生/遗留聚合值，不是判断依据）、`GET /activation-links/:id`（详情，字段含 `conditions[]`，每项 `{subject, intent, audience, constraint, success_count, failure_count, audited_success_count, audited_failure_count, mean, tier, last_seen_at}` ——本方案后续步骤判定优先读这里而非顶层 `status`）、`GET /activation-bundles`、`GET /activation-bundles/:id`（字段含 `members[]`/`core_member_point_ids`）、`GET /study/results`、`GET /learning-events`、`GET /wiki/pages`、`GET /units/:id`（lifecycle）；
- DB：`learning_events`、`activation_links`（`observed_conditions` 列为 JSON，是 `conditions[]` 的落库来源）、`learning_results`、`traces`、`knowledge_units.lifecycle`、`knowledge_point_relations.scope`、`wiki_pages`（含 `synthesis_success_count`/`synthesis_failure_count`/`synthesis_audited_success_count`/`synthesis_audited_failure_count` 四个纯观测计数列）；
- 日志：LLM 调用次数按 `LLMClient` 调用日志逐问计数（这是标准 2 的核心指标，若当前无此日志需先补一行 slog）；后台独立核实（`RecordAuditOutcome`）与 Wiki 合成审计（`wiki_synthesis_audit_*`）会异步追加慢路径对照调用，统计"某一题的 LLM 调用次数"时应只数该题同步返回前发生的调用，不要把异步抽样触发的核实调用也算进单题预算。

## 4. 问答测试集

期望答案以文档原文为准；「期望证据」指引用片段应落在的原文位置。每题标注用途。A/T/G 三组同时内嵌于 `test/mvp/mvp-acceptance-test-plan.md` 第 4 节（准确率测试集），两处同源，修改须同步。

### 4.1 A 组 · 单文档事实类（学习信号主力，每题含 1-2 个变体问法；2026-08-15 修订：原标注"2-3 个"与实际题库不符，多数题仅 1-2 个问法已足够验证匹配泛化，A1 由 3 变体裁剪为 2，省 1 次会话+回答往返，不影响 P2/P3 判据——P2 步骤 4 对 A1 的额外复现走独立的 `--extra-phrasing-file`，不依赖本表变体数）

| ID | 问题（主问法 / 变体） | 期望答案要点 | 期望证据来源 |
|----|---------------------|-------------|-------------|
| A1 | 招待费用报销期限是多久？ / 请客吃饭的发票多久内要报掉？ | 费用实际发生之日起 45 天内；逾期财务不受理、费用个人承担 | 报销规定·第一条 |
| A2 | 差旅费报销期限是多久？ / 出差发票几个月内有效？ | 发票开具之日起 3 个月内（办公、市内交通、通讯、差旅同此） | 报销规定·第二条 |
| A3 | 发票能跨年报销吗？ | 原则上禁止跨年报销 | 报销规定·第四条 |
| A4 | 培训旷课一次扣几分？ / 不请假缺席培训怎么扣分？ | 每旷课 1 次 -5 分 | 培训积分·第五条积分规则表 |
| A5 | 培训积分能累计到明年吗？ / 积分跨年清零吗？ | 不跨年累计，次年自动清零 | 培训积分·第六条 |
| A6 | 达不到年度积分基准线有什么后果？ | 年终绩效奖金按档内最低系数、次年无调薪资格、无职级晋升资格、无年度评优资格 | 培训积分·第七条 |
| A7 | A800 GPU 用一天多少钱？ / A800 的 GPU Day 收费标准？ | 256 元/天（L40 为 85 元/天，不足 1 天按 1 天算） | 平台办法·5.2 |
| A8 | GPT 服务怎么计费？ / 调大模型接口的 token 费用？ | 按 token 计费，0.15 元/千 token | 平台办法·5.2 |
| A9 | 客户逾期 90 天没付款怎么办？ / 回款逾期三个月的催收动作？ | 发出第一封《催款函》并电话询问（180 天第二封、考虑停加密狗；270 天第三封、准备委托律师） | 应收账款·第十条（二）4 |
| A10 | 回款延期申请最多可以几次、每次多久？ | 两次机会；每次原则上 ≤3 个月，两次合计 ≤6 个月 | 应收账款·第九条 |
| A11 | 延迟九个月以上回款，销售提成怎么算？ | 提成比例 ×25%；强制转催款专员后专员回款成功按 ×75% | 应收账款·第十二条 |
| A12 | 项目考核优秀的标准和绩效系数？ | T≥100 为优秀，绩效系数 1.1 | 项目考核·4.1 表 |
| A13 | 战略项目的奖金系数是多少？ | 6%（A 类 5%，B/C/D 类 4%，E 类 3%，万相公文/无纸化会议 1%） | 项目考核·5.1 表 |
| A14 | 员工当年离职，项目奖金还发吗？ | 不予发放 | 项目考核·3.2 第 10 条 |

### 4.2 T 组 · 技术单文档事实类（技术域学习信号主力，每题含 1-2 个变体问法；2026-08-15 修订：同 A 组标注不符实情，题库本身已偏精简，未作内容改动，仅更正描述）

| ID | 问题（主问法 / 变体） | 期望答案要点 | 期望证据来源 |
|----|---------------------|-------------|-------------|
| T1 | Docker Swarm 集群管理端口是哪个？ / Swarm 防火墙要开哪些端口？ | 2377/TCP 集群管理；7946/TCP 节点通讯；4789/UDP overlay | Docker Swarm·环境准备 |
| T2 | Swarm 里 Manager 和 Worker 节点分别干什么？ | Manager 负责集群管理、调度和控制（仅一个 Leader）；Worker 运行容器并上报状态 | Docker Swarm·架构 |
| T3 | K8S 集群最少需要几台服务器？ / 单机能装 K8S 吗？ | 高可用至少 3 台（1 Master 2 Node）；测试可单机部署、移除污点后跑应用 | K8S·部署要求 |
| T4 | K8S 1.24 之后默认容器引擎是什么？ / 现在还用 dockershim 吗？ | 默认集成 containerd，不再需要 dockershim，调用链更短更稳定 | K8S·容器引擎 |
| T5 | 用 crictl 前要先配置什么？ | `crictl config runtime-endpoint unix:///run/containerd/containerd.sock` | K8S·容器引擎 |
| T6 | MySQL 主从复制的原理？ / binlog 在主从同步里怎么流转？ | 主库写 binlog → 从库经同步账户读入 replaylog → 从库重放执行 | MySQL·第 1 节 |
| T7 | MySQL 从库怎么指向主库同步位？ | `change master to master_host=... master_log_file/master_log_pos`，取值与主库 `show master status` 一致 | MySQL·2.2 |
| T8 | Oracle RAC 怎么开启归档？ / Oracle RAC 开归档的步骤是什么？ | srvctl stop → start -o mount → `alter database archivelog` → 重启实例 → `archive log list` 验证 | RAC 开启归档 |
| T9 | 怎么删除 7 天前的归档日志？ | rman 中 `delete archivelog until time 'sysdate-7'` | RAC 开启归档 |
| T10 | Oracle RAC 上 VKTM 进程 CPU 占用高怎么处理？ | 虚拟化环境常见；改 `_high_priority_processes='LMS*'` scope=spfile（ORACLE 与 ASM 实例都要改）；因参考时间不可用，不建议生产环境 | RAC 问题汇总·问题 1 |
| T11 | 客户端连 RAC 报 TNS-12518 hand off 错误是什么原因？ | oracle 程序文件缺失粘连位；`chmod u+s .../bin/oracle` 解决（metalink 1069517.1） | RAC 问题汇总·问题 2 |
| T12 | 达梦报"语句句柄个数超过上限或系统内存不足"怎么解决？ | dm.ini 调大 MAX_SESSION_STATEMENT（建议 20000）；根因是程序未关闭 statement，建议从程序解决 | 达梦·5.3 |
| T13 | 达梦 BUFFER 参数建议配多大？ | 物理内存的 60%~80%；MAX_BUFFER 配置为与 BUFFER 相等 | 达梦·5.4 表 |
| T14 | 达梦 WORKER_THREADS 怎么配？ | 与 CPU 核数相等或其 2 倍（1~64）；TASK_THREADS 与之相等 | 达梦·5.5 表 |
| T15 | 神通数据库最大连接数默认是多少、上限多少？ | MAX_CONNECTIONS 默认 128，最大 65535 | 神通·并发连接数限制表 |
| T16 | 神通怎么开启自动统计信息？ | oa.conf 中 `ENABLE_AUTO_STAT = true` | 神通·计算统计信息 |
| T17 | SQL Server AlwaysOn 需要开放哪些防火墙端口？ | 1433/TCP、5022/TCP | AlwaysOn·环境准备 e |
| T18 | Oracle 19c RAC 有哪些磁盘绑定方式？ | UDEV 映射；或 12c 及以后提供的 AFD（asmcmd afd_label） | 19c RAC·磁盘映射 |

### 4.3 B 组 · 跨文档综合类（KPN 扩展与跨 Source 关系）

| ID | 问题 | 期望答案要点 | 期望证据 |
|----|------|-------------|---------|
| B1 | 项目回款延迟对个人收入有哪些影响？ | 回款提成打折（6 个月 50%、9 个月 25%）+ 项目奖金第二次发放需等验收款回款 | 应收账款·第十二条 + 项目考核·5.3 |
| B2 | 哪些制度会影响员工的年度奖金？ | 培训积分（基准线不达标按档内最低系数）+ 项目考核（绩效系数、奖金包） | 培训积分·第七条 + 项目考核·五 |
| B3 | 达梦、金仓、神通数据库分别怎么查耗时 SQL？ | 达梦：v$ 视图查耗时语句；金仓：TOP SQL / 记录耗时 SQL；神通：v$sqlstat 按 elapsed_time 排序 + MIN_SQL_TRACE_TIME | 三篇优化文档各自章节 |
| B4 | Oracle 11g 和 19c RAC 的部署环境有什么区别？ | OS（RHEL 6.5/SUSE 11 SP3 vs OL 7.7）、DB 版本（11.2.0.3 vs 19.3.0.0）、存储绑定（ISCSI vs UDEV/AFD） | 两篇 RAC 部署文档 |
| B5 | 国产数据库优化都需要做统计信息处理吗？ | 达梦：更新统计信息脚本；金仓：计算统计信息；神通：ANALYZE_SCHEMA / ENABLE_AUTO_STAT | 三篇优化文档 |

### 4.4 C 组 · 缺口类（knowledge_gap，不应幻构）

| ID | 问题 | 期望行为 |
|----|------|---------|
| C1 | 公司年假有多少天？ | 明确表示知识库无相关内容，不得引用任何 KP 编造；knowledge_gap 事件仅在检索证据全空（direct+supporting 均空）时产生——有证据即不归 gap（无法机器判断证据是否真与问题无关，2026-07-19 定案），事件是否出现不计失败，主判定是回答不幻构 |
| C2 | 报销单在 OA 里怎么填？ | 同上（报销规定只讲期限，不讲操作步骤） |
| C3 | OpenGauss 数据库怎么优化？ | 同上（只有达梦/金仓/神通/Oracle，不得把其他数据库的参数张冠李戴——技术域幻构风险高于制度域，重点观察） |
| C4 | K8S 集群怎么升级版本？ | 同上（部署文档无升级章节） |

### 4.5 D 组 · 片段级证据专项（表行/单句摘选）

| ID | 问题 | 期望引用片段（必须是原文子串） | 反例检查 |
|----|------|------------------------------|---------|
| D1 | 培训积分里，迟到早退怎么扣分？ | 「迟到/早退累计2次 -1」所在表行 | 引用不应包含整张积分规则表 |
| D2 | 培训积分规则中，考试成绩多少分要扣分？ | 「考试成绩低于80分（含80分）-1」 | 同上 |
| D3 | 历史回款里 2021 到 2022 年签的合同催收周期？ | 「2021年至2022年（含2022年）……周期为6个月」句 | 不应把 3 个月/9 个月档一并引入 direct |
| D4 | 大模型开发测试基础平台的奖励方案，免费条件是什么？ | 「累计10个及以上，免除当年全部使用费用，有效期一年」 | — |
| D5 | 达梦 MAX_OS_MEMORY 建议值？ | 「MAX_OS_MEMORY 90 ……推荐为90%」表行 | 不应把整张 5.4 内存参数表（13 行）全部作为 direct 证据 |
| D6 | 神通 FILE_IO_OPTION 的 BUFFER 和 DIRECT 怎么选？ | 「BUFFER……批量写入优于 DIRECT；DIRECT……大量随机写入更好」表行 | — |
| D7 | 达梦 DIRECT_IO 在龙芯服务器上建议设多少？ | 「龙芯服务器建议使用 2 为操作系统 NATIVE IO」片段 | 长表行内的定语提取，摘选粒度重点样本 |
| D8 | 万相公文销售奖励制度里，SaaS 版伙伴代理提货价是多少？ | 「伙伴代理提货价按 200 元/年/用户」句 | 不应把最终用户成交限价 500 或市场指导价 800 当作提货价 |

### 4.6 E 组 · 会话追问类（验证 expanded_question 与激活匹配同源）

| ID | 会话 | 期望 |
|----|------|------|
| E1 | 先问 A1「招待费报销期限？」，追问「差旅费呢？」 | 追问被补全为差旅费报销期限问题，正确回答 3 个月；后续该补全问题参与共现/激活统计 |
| E2 | 先问 A12，追问「待改进呢？」 | 补全后回答 70≤T<85、系数 0.5 |
| E3 | 先问 T13「达梦 BUFFER 配多大？」，追问「神通呢？」 | 补全应指向神通的数据缓冲区参数（BUF_DATA_BUFFER_PAGES），不得沿用达梦参数名回答 |
| E4 | 先问 B4，追问「那 19c 的存储绑定具体怎么做？」 | 补全后回答 UDEV rules 或 asmcmd afd_label 步骤 |

### 4.7 F 组 · 对象守门专项（同问法不同产品不得串台）

技术域三篇国产数据库文档结构高度相似（都有"查会话、查耗时 SQL、内存参数"），是激活条件对象/约束守门（`activation.md` 步骤 2 硬性守门）最严苛的考场。测法：先把某产品的问题培养成 verified 链接，再用**同样句式问另一个产品**，验证链接不被错误激活。

| ID | 前置 verified 链接 | 守门问题 | 期望 |
|----|------------------|---------|------|
| F1 | 「达梦怎么查询会话执行情况」（P2 培养） | 神通数据库怎么查询会话执行情况？ | 不激活达梦链接（对象不同被守门拦截）；走慢路径或神通自己的链接；回答引用神通 V$SESSION 查询而非达梦的 |
| F2 | 「达梦报句柄超上限怎么解决」（T12） | 金仓数据库报内存不足怎么处理？ | 不激活 T12 链接；不得把 MAX_SESSION_STATEMENT 安到金仓头上 |
| F3 | 「Oracle RAC 怎么开归档」（T8） | MySQL 怎么开启 binlog？ | 不激活 T8 链接；回答引用 MySQL 文档 log-bin 配置 |

F 组每题跑 3 次，任何一次错误激活（trace 的 activation_link_ids 含前置链接且其 KP 进入 direct）即计守门失效一次。

### 4.8 G 组 · 准确率扩展题库（48 题，制度 24 + 技术 24）

单轮提问即可，不承担学习信号任务；计入 P1（及 MVP 方案 M3/M4）的正确率统计，与 A/T 组合并后按制度域、技术域分开计算。覆盖 A/T 组未触及的文档（差旅费、绩效管理、无合同立项、万相公文）与已有文档的深层条款。

**制度类（G1-G24）**

| ID | 问题 | 期望答案要点 | 来源 |
|----|------|-------------|------|
| G1 | 出差住宿 A 类城市限额多少？ | 350 元（B 类 280、C 类 220、D 类 200，限额内实报实销） | 差旅费·第五条 |
| G2 | 去杭州出差住宿标准是多少？ | 杭州属 B 类（新一线）城市，280 元 | 差旅费·第五条（四） |
| G3 | 出差什么情况下可以坐卧铺？ | 夜间乘车 4 小时以上或白天 10 小时以上（夜间=22 时至次日 6 时）；高铁/动车仅二等座 | 差旅费·第三条（一） |
| G4 | 什么条件下出差可以坐飞机？ | 紧急赶赴客户现场（走"出差乘坐飞机申请流程"）；或机票总费用（含保险+往返机场交通）低于高铁二等座价/标准报价，就低原则，行程单为凭证 | 差旅费·第三条（五） |
| G5 | 出差投亲靠友住宿怎么补贴？ | 按出差地住宿标准的 40% 补贴 | 差旅费·第八条（三） |
| G6 | 住宿费没花完有奖励吗？ | 单人间奖励节约部分的 50%；标准间节约部分平均分配 | 差旅费·第五条（六） |
| G7 | 出差直辖市或省会城市伙食补贴一天多少？ | 住酒店 80 元/天、住宿舍 120 元/天（其他地市 50/70，区县 40/60） | 差旅费·第七条表 |
| G8 | 去哪些省份出差伙食补贴上浮？ | 新疆、西藏、黑龙江、吉林、辽宁、云南、青海、宁夏、甘肃、内蒙的非省会地区上浮 50%；就地招聘人员省内出差不上浮 | 差旅费·第七条（三） |
| G9 | 当天往返的出差有补贴吗？ | 超 12 小时 40 元/次；不足 12 小时 30 元/次 | 差旅费·第八条（九） |
| G10 | 有卧铺不坐有奖励吗、怎么算？ | 按（同程卧铺价−同程座铺价）×60% 奖励 | 差旅费·第十三条 |
| G11 | 出差打车报销对票据有什么要求？ | 机打或电子发票（定额发票无效）、时间与出差吻合、背面注明起止点/单位全称/事由；虚报的对主管按 2-5 倍罚款 | 差旅费·第六条（三） |
| G12 | 什么时间的车可以报销车站往返打车费？ | 交通工具 9 点前出发或 21 点后到达 | 差旅费·第六条（三）1 |
| G13 | 同部门同性别同行出差怎么住？ | 必须标准间、两两优先入住，各开房间取消所有补贴并视情节罚款 | 差旅费·第五条（五） |
| G14 | 绩效考核分几个等级、S 级比例多少？ | S/A/B/C/D 五级；S 占 0-5%，A 占 10%-15%，B 占 60%，C、D 合计不低于 20% | 绩效管理·考核等级表 |
| G15 | 绩效多少分能评 S？ | 95（含）-100 分 | 绩效管理·考核等级表 |
| G16 | 年度绩效系数各等级是多少？ | S=2、A=1.5、B=0.8-1、C=0-0.5、D=0 | 绩效管理·绩效奖金节 |
| G17 | P 序列绩效奖金里公司和个人系数权重怎么分？ | 公司绩效系数 20% + 个人年度绩效系数 80%（M 序列为 30%/70%） | 绩效管理·权重节 |
| G18 | 连续两次绩效 C 有什么后果？ | 本年度可酌情不予调薪或职级晋升；D 按月考察、整改不超 3 个月，仍不合格调岗直至解除劳动关系 | 绩效管理·绩效整改 |
| G19 | 季度绩效考核什么时候完成？ | 各季度结束后一月内；年度考核次年春节前 | 绩效管理·考核周期 |
| G20 | 无合同立项要经过哪些审批？ | 营销中心总经理 → 总经理 → 分管交付副总经理；任一环节不通过升级总经办临时会议决策 | 无合同立项·第四、五条 |
| G21 | 无合同项目赶不上签约红线怎么办？ | 提前 5 个工作日提交"无合同立项延期申请流程"调整签约红线与成本红线 | 无合同立项·第六条 |
| G22 | 无合同立项里的成本红线指什么？ | 合同签订前允许发生的实施成本和采购成本的总和 | 无合同立项·第四条（一） |
| G23 | 卖万相公文 SaaS 版有什么奖励？ | 签约人员获有效合同额 30% 奖金，按回款比例回款当月结算 | 万相公文·（一） |
| G24 | 万相公文私有化部署奖励的评奖条件？ | 净合同额 ≥15 万、毛利率 ≥40%、打包合同中须明确体现且实施要求一致；按有效到款比例兑现 | 万相公文·（二）2 |

**技术类（G25-G48）**

| ID | 问题 | 期望答案要点 | 来源 |
|----|------|-------------|------|
| G25 | Swarm 里怎么拿到 Worker 节点的加入命令？ | 主 Manager 上执行 `docker swarm join-token worker`，输出含 token 的 join 命令 | Swarm·加入节点 |
| G26 | Swarm 节点 Drain 状态是什么意思？ | 不分配新任务，且已有任务会被调度到 Active 节点上 | Swarm·节点状态 |
| G27 | 怎么把 Swarm Worker 升级成 Manager？ | `docker node promote nodename`（降级用 demote） | Swarm·管理节点 |
| G28 | 服务器有多个 IP 时初始化 Swarm 要注意什么？ | `docker swarm init --advertise-addr <IP>` 指定地址 | Swarm·Manager 部署 |
| G29 | kube-scheduler 负责什么？ | 监视新创建的 Pod 并分配给合适的 Node | K8S·架构 |
| G30 | etcd 在 K8S 里存什么？ | 分布式键值存储，保存集群配置信息和状态信息 | K8S·架构 |
| G31 | K8S 里 Pod 是什么？ | 一个或多个容器的集合，部署调度的最小单位；容器共享网络与存储，可经 localhost 互通 | K8S·相关概念 |
| G32 | 单机测试 K8S 时怎么让 Master 也能跑应用？ | 移除污点 | K8S·部署要求/步骤 5 |
| G33 | 怎么判断 MySQL 主从同步配置成功？ | `show slave status` 中 Slave_IO_Running 与 Slave_SQL_Running 均为 Yes | MySQL·2.2 |
| G34 | 从库是主库克隆的，无法同步怎么办？ | 修改 data 目录下 auto.cnf 的 server-uuid（uuid 相同导致无法同步） | MySQL·2.2 尾注 |
| G35 | MySQL 主主热备怎么避免自增主键冲突？ | auto_increment_offset 分别设 1/2，auto_increment_increment=2 | MySQL·3.1 |
| G36 | Oracle 11g RAC 部署用什么操作系统？ | RHEL 6.5 或 SUSE Linux Enterprise Server 11 SP3（对照：19c 用 Oracle Linux 7.7） | 11g RAC·软件表 |
| G37 | RAC 心跳线能不能两台服务器直连？ | 不能，必须通过交换机连接 | 两篇 RAC·网络表 |
| G38 | Oracle AFD 磁盘绑定从哪个版本开始提供？ | 12c 及以后版本 | 19c RAC·AFD 节 |
| G39 | 怎么删除全部归档日志？ | rman 中 `delete archivelog all` | RAC 开启归档 |
| G40 | Oracle RAC 两个实例必须保持一致的参数有哪些？ | cluster_database、cluster_database_instances、db_block_size、control_files、compatible 等（举例即可） | RAC 问题汇总·问题 3 |
| G41 | AlwaysOn 部署时域控上建共享目录干什么用？ | 用于集群仲裁和数据库备份 | AlwaysOn·环境准备 b |
| G42 | 达梦 MAX_SESSION 建议配多少？ | 500；实际生效值还受 LICENSE 限制，取两者较小值 | 达梦·5.6 表 |
| G43 | 达梦 OLTP 应用 OLAP_FLAG 应该设几？ | OLTP 设 2，OLAP 设 1 | 达梦·5.7 表 |
| G44 | 金仓怎么查看当前活动会话和阻塞源？ | 查 sys_stat_activity 视图（含 sys_blocking_pids(pid) 看阻塞进程） | 金仓·1 |
| G45 | 金仓怎么开启 TOP SQL 统计？ | kingbase.conf 配 `shared_preload_libraries='sys_stat_statements'` + `create extension sys_stat_statements`，查同名视图 | 金仓·2 |
| G46 | 金仓 shared_buffers 建议配多大？ | 物理内存的 25%~40% | 金仓·6 表 |
| G47 | 改了 kingbase.conf 怎么让参数生效？集群环境有什么额外注意？ | `select sys_reload_conf()`（部分参数需重启）；集群须所有节点都改且追加在文件末尾，原位置修改重启会被还原 | 金仓·5、集群管理 3 |
| G48 | 神通数据库怎么强杀一个会话？ | `kill session sid abort` | 神通·3 |

## 5. 测试阶段

按序执行，前一阶段是后一阶段的数据基础。每阶段列出操作、验证点、通过标准。

### P1 慢路径基线（标准 4 前半 + P3 的对照组）

**前置（原 P0 导入与提取验收，已合并/删除独立测试脚本）**：`preset/domains.json` 必须覆盖技术域（数据库/容器/运维类 Domain 与 Concept），否则 Domain 预过滤会把技术问题全部错杀在入口；依次上传 21 份文档并等 `source_process`/`unit_extract` 完成；确认 21 个 source 状态 ready、`GET /sources` 无影子行、所有 KU/KP `lifecycle=current` 后再进入本阶段——本方案不再对导入/提取本身设独立验收步骤与脚本（`v1_p0_ingest_test.py` 已删除），提取质量改为在 P1 逐题作答过程中随事实命中情况一并核验。

1. 逐题执行 A 组 + T 组全部主问法 + B 组 + C 组 + D 组 + G 组（准确率扩展题库 48 题，单轮提问），每题记录：`path_type`、回答是否含期望要点、引用 fact_id 数、LLM 调用次数、耗时；
2. 验证点：
   - 所有回答 `path_type=full`（此时无 verified 链接）；
   - A/T/B/D/G 组回答要点正确率 ≥ 90%（制度域约 45 题、技术域约 50 题，**分开统计**，两边各自 ≥90%——单边达标不算过，V1 目标要求跨领域成立），错误题记录原因（提取缺失 or 检索未召回 or 回答错引）；
   - 技术域重点核查：C3 不得把达梦/神通参数安到 OpenGauss 头上；B3/B5 的引用不得张冠李戴（引用的 KP 所属 source 必须与回答中提到的产品一致）；
   - D 组每条 `mined=true` 的证据 content 必须是对应 KU 原文的子串（脚本核验 `content in 原文`）；出现 `mined=false` 整段回退时数量记录在案，回退率 ≤ 30%；
   - C 组不产生虚假引用（回答明确说明无相关内容，不编造）；knowledge_gap 事件仅当该题检索证据全空时出现，partial（有 supporting）不出现且不计失败（2026-07-19 定案，见 4.4 节）；
   - A 组答对的题在 `learning_events` 中产生 `activation_gap`（path=full 且 confident 时应写入，payload 含 question_terms 与 direct_point_ids）；
   - 慢路径 LLM 调用次数记录为基线（预期 ≥4 次/题）。
3. 通过标准：以上全部成立。activation_gap 一条都没有 → 标准 1 的燃料链路断裂，先修再继续。

### P2 学习转化：candidate 形成与置信度收敛（标准 1 前半 + 标准 3）

**2026-08-13 改判（取代此前"人工晋升 verified"这整套设计，本节全文重写）**：ActivationLink 不再有 candidate→verified 的人工确认步骤——`POST /activation-links/:id/confirm` 端点已从代码中移除（`internal/activation/handler.go` 路由表中不存在）。置信度改为连续自收敛：每条 `observed_conditions` 独立累计 `success_count`/`failure_count`，`mean = (success+1)/(success+failure+2)` 达到 `serving_confidence_min` 即视为 `self_graded` 档，`status` 自动派生为 `verified`——**没有任何人工确认动作参与这个过程**。人工唯一能做的是 `POST /activation-links/:id/reject`（清空条件、打回 `candidate`，见第 3 节术语对照），语义是"推倒重来"而不是"驳回一个待确认的提案"。

本节沿用旧版遗留的 "Matcher 已是四元组完全匹配" 与 "按 point_id 归属" 两条实测经验，均仍成立：

- Matcher 是四元组完全匹配：变体问法**不保证**落到同一条 ActivationLink，因此「再问变体 → 同一 link 的 success_count 自然凑满」不作为全清单硬门槛。
- Study 按 `point_id` 建/刷新链接：邻近主题（如 A9↔A11 回款簇、T12↔F1_PRE 达梦运维簇）可能共享 `point_id` 或串进同一 link。脚本必须以**题号独占归属**判定观察/对照组，禁止把「凡出现过该题 direct evidence 的 point」一锅端到多题上。

1. 培养清单（制度域 6 题 + 技术域 5 题，覆盖两域）：A1、A2、A4、A9、A11、A12 + T8、T12、T15、T13，以及 F1 前置问题「达梦怎么查询会话执行情况」；每题至少 2 种不同问法（题库变体 + `--extra-phrasing-file`），保证共现侧 `distinct question_hash ≥ 2` 且 confident。靶子约束：A4 必须入选（P6 制度侧靶子）、T15 必须入选（P6 技术侧靶子）、T8 必须入选（P5 技术侧靶子）；A11 入选但**刻意少问几轮**（只问 1 轮、不追加复现），专作"欠采样条件应停留在 exploring 档、`mean` 不跨过 `serving_confidence_min`"的对照组——它不再是"未被人工确认"的对照，而是"证据量不足以自然收敛"的对照；
2. `POST /study/run`；
3. 验证 candidate 创建：培养清单各题在**独占归属**下至少能关联到一条 `status=candidate` 的链接（共享 point 的串台记入观测，不阻塞）；每条归属链接有 `learning_results(action=create_candidate, status=applied)`，reason 含 `mean_pre`/`width_pre`/触发来源事件 id（替代旧版的 confident_count/ratio 措辞，字段来自 `question_kp_cooccurrence.confident_count`/`hit_count` 换算），能 JOIN 回 `learning_events`；技术域归属链接的激活条件应带对象/约束字段（如"达梦"/"神通"/"oracle rac"）；
4. 再培养一轮（全清单变体各再问一遍；对收敛演示题 A1 **额外**用主问法原句 + 至少 2 个不同问法反复复现，让同一四元组的 `success_count` 尽量往上堆，目标是让它先于其他题跨过 `serving_confidence_min`）→ `POST /study/run`：
   - **硬门槛**：至少 **1** 条归属链接的某个观测条件 `mean` 跨过 `serving_confidence_min`（GET 详情响应 `conditions[].tier` 为 `self_graded` 或 `trusted`），对应 `GET /activation-links/:id` 顶层 `status=verified`——这条链接**没有经过任何人工确认动作**，验证的正是"自动收敛，无需人工晋升"这一新设计；预期来自 A1（第 4 步刻意集中复现的题）；
   - **观测**：其余归属链接各自的 `conditions[].mean`/`success_count`/`failure_count`/`tier` 分布，精确匹配下多数题的观测条件可能仍停留在 `exploring` 档（`mean < serving_confidence_min`），属预期，写入报告不判 FAIL；
   - **对照**：A11 归属链接（第 1 步刻意欠采样）此时 `conditions[].tier` 应仍为 `exploring`，`status` 仍为 `candidate`——用以证明"收敛需要真实证据积累，不是问一次就自动 verified"；
5. 人工动作：仅对 A2、T13 的归属链接调用 `POST /activation-links/:id/reject`（不再有"确认集"人工操作——A1/A4/A9/A12/T8/T12/T15/F1_PRE 全部依赖第 4 步的自然收敛，不主动干预）；
6. 验证：A2、T13 归属链接 reject 后 `observed_conditions` 被清空（`GET .../id` 响应 `conditions` 为空数组）、`status` 重新派生为 `candidate`（KP 仍 current，不会落到 `deprecated`——`deprecated` 只由 KP lifecycle 触发，reject 本身不产生 deprecated）、且不参与后续快路径召回（下一轮问该题应 `path_type=full`，因为没有任何达标条件可服务）；`learning_results` 记 `action=prune_condition, reason=manual_reject, status=applied`；A11 独占归属链接仍非 `verified`；
7. 通过标准：
   - 至少 1 条归属链接**未经人工确认**、仅凭 `RecordOutcome` 自然回写即达到 `self_graded`/`trusted` 档并使 `status` 派生为 `verified`（标准 1 前半，验证的是"自动收敛"而非"晋升流程"）；
   - A11 对照组在同一轮 Study 后仍未跨过 `serving_confidence_min`（观测证据量与置信度正相关，不是问一次就收敛）；
   - reject → 条件清空 → `status` 重新派生为 `candidate`（不是字面 `deprecated`，也不是旧版文档写的 `rejected`）；
   - 每次条件变化都能从 `learning_result`（`action=create_candidate` 或 `action=prune_condition`）→ `reason` → `event_ids` 完整回溯（标准 3）；
   - 题号↔链接按独占归属判定，邻近主题串台只记观测。

### P3 快路径生效（标准 1 后半 + 标准 2）+ 对象守门 + ActivationLink 可用性验证

**2026-07-19 改版说明**：`activation.md` 步骤 2 已从"打分+阈值"改为"四元组归一化后完全匹配"（不再有 `activation_match_min` / `activation_match_min_fallback` 阈值），并新增步骤 2a 快路径证据充分性校验（`fast_path_verify`，1 次 LLM）。旧版"换第三种问法验证仍能命中"的前提（打分容忍改写）不再成立——完全匹配下，改写后的问题是否命中，取决于 Session Parser 对 subject/intent 抽取标签的稳定性，这是待观测的效果指标，不是本次要验收的正确性标准。本节按两个轴重新设计：**轴一验证"链接是否被正确找到"（匹配正确性），轴二验证"命中后证据是否真能完整回答问题"（步骤 2a 校验）**。

**2026-08-07 口径修订（准确优先）**：四元组精确匹配刻意偏保守——宁可漏召回、不可误激活。产品取舍定为：**准确（守门/不串台）为硬门槛；已培养问法的快路径召回率合计 ≥70% 即可**，不再要求 M1 18/18、M2 6/6 全过。Session Parser 同题抖动导致的偶发 `full` 记入报告，不单独判失败。不引入向量匹配放宽四元组。

**轴一：匹配正确性**

1. M1 精确复现：对 A1、A9、A12、T8、T12、T15 六条已 verified 链接，各用**培养时的主问法原句**重问 3 次；
   记录：每次是否 `path_type=fast` 且 `activation_link_ids` 非空；逐题命中明细与耗时写入报告。
   **判定**：与 M2 合并计算召回率（见通过标准），不要求每题每次全过；`activation_success` 事件是否落库仅观测（异步竞态不挡通过）。
2. M2 归一化容差：六题各任选 1 题，只调整词序或增删多余空白/标点（不改变用词），重问 1 次；
   记录：是否仍 `path_type=fast`；与 M1 合并计入召回率。
3. M3 改写观察（原"换第三种问法"保留，改为观测指标，不设通过/失败判定）：六题各换 1 种未出现过的自然表述（沿用 `--extra-phrasing-file` 机制）重问 3 次；
   记录：命中率、以及 `GET /activation-links?point_id=...` 是否新增了一条对应新问法的 `candidate` 链接（慢路径 confident 时应产生新 candidate，验证"覆盖靠积累，不靠模糊匹配"这条设计假设）；**此项不计入 P3 通过标准**，结果写入报告；本项记录的"是否需要 Study 侧 subject 归一化"已在 2026-07-24 落地为 `subject_synonyms` 机制，收敛效果验收见 P11（P11 依赖本步骤积累的 candidate 链接，勿在本阶段清库）；
4. M4 约束不对称已取消——超集不再放行（**已试跑确认可行问法**，见下方"试跑记录"）：复用 F1_PRE 链接（P2 已培养、已确认为 verified），核心问法追加一个 F1_PRE 未覆盖的环境限定词重问：
   ```
   基线（F1_PRE 培养问法）：达梦怎么查询会话执行情况
     → subject=数据库会话监控 intent=查询会话执行情况 audience="" constraint=达梦
   M4 探针：            达梦在Windows环境下怎么查询会话执行情况？
     → subject=数据库会话监控 intent=查询会话执行情况 audience="" constraint=达梦,Windows环境
   ```
   subject/intent/audience 与培养时完全一致，constraint 从"达梦"变成"达梦,Windows环境"（干净超集，不改变其余三维）——正是验证对称语义所需的最小变量控制；
   验证：`path_type=full`（不命中）——这是本次设计变更后的新行为，取代旧版"问题多出的限定不拦截"；
5. M5 对象/约束错配排除（F 组，判定口径不变）：F1、F2、F3 各 3 次，验证前置 verified 链接不被同句式异对象问题错误激活；
6. E3 追问不串台：会话内先问「达梦 BUFFER 配多大？」，再问「神通呢？」；
   验证：展开问句含「神通」；第二轮不得把达梦答案原样当成神通答案（诚实缺口「暂无相关材料」等算通过）。**不要求**库中必有神通 BUFFER 材料或回答正文必现「神通」——缺材料时如实缺口优于串台编造；
7. M6/M7 四元组缺失回退——**回退分支在真实数据里无法自然培养出**（试跑已证实：Session Parser 对任意真实问题都会抽出非空 subject/audience/constraint，即使是 A11 这种纯数字条件的问题也不例外；回退分支存在的意义是兼容"存量迁移链接"或"本轮 Session 解析异常降级"，两者都不是健康链路的自然产物），改为直接改库模拟存量链接：
   - 前置：任选一条已 verified 的链接（如 F1_PRE）。Matcher 主路径读的是 `observed_conditions`，因此必须同时清空观测组，不能只清 denorm 四列。推荐：
     ```sql
     UPDATE activation_links
     SET subject_terms='', intent_terms='', audience='', constraint_terms='',
         observed_conditions='[]',
         question_terms=<基线问句对应的 traces.question_terms>
     WHERE link_id=?
     ```
     （`question_terms` 设为基线问句的归一化结果，保证回退分支 `Qq == question_terms` 可比对；测完恢复全部字段含 `observed_conditions`。）必要时重启以刷新 Matcher 缓存；
   - M6 精确复现：用该链接基线问句原样重问 → 应命中该 `link_id`（`path_type=fast`）；
   - M7 改写不命中：同一链接，问题换一种表述重问 → **该 `link_id` 不得出现在 `activation_link_ids`**（回退仅认 `question_terms` 逐字相等；若其它未改动的链接命中导致整体仍为 `fast`，不判本项失败）；
   - 隔离（防测试互相污染，2026-08-07）：
     - **M4 前**：从 F1_PRE 链接的 `observed_conditions` 剔除 constraint 含 Windows 的历史组（前次超集探针经慢路径 Enrich 写回会导致本次误命中），并重启刷新 Matcher 缓存；
     - **M6/M7**：任一轮 `path_type=full` 会触发 `EnrichFromConfidentFullPath` 写回观测组；下一轮及进入 M7 前必须再次 `observed_conditions='[]'` 并重启，否则测到的不是回退分支。
8. M8 服务分层过滤（2026-08-13 改判后重写）：P2 已将 A2、T13 **reject → 条件清空 → status=candidate**（不是字面 `deprecated`），重问应走 `path_type=full` 且不得激活这两条链接（没有任何达标条件可服务，不是因为状态被禁用）；另抽 A11（P2 中刻意欠采样、其观测条件仍停留在 `exploring` 档）确认它可以被 Match 找到并记一次观测信号（`RecordOutcome` 仍会写入 `exploring` 条件的 `success_count`/`failure_count`），但本轮是否真正提供服务取决于 `explore_rate_low` 抽样（测试期已把该值调到 `1.0`，见第 3 节，因此预期本轮会被抽中试探，但试探本身不代表"晋升"，只是多了一次观测数据）；`status=deprecated`（KP lifecycle 非 current）的链接不参与匹配——本阶段不重复造场景，复用 P5 会产生的现状即可（若 P5 尚未跑，此半句只记观测）。

**轴二：证据充分性校验（步骤 2a，`fast_path_verify`）**

9. V1 正常充分：M1 中实际走快路径的样本即为正向样本——额外核对可观察到 `fast_verify`（若无独立日志字段，退化为耗时/调用次数对照）；不因个别题未命中快路径而判 V1 失败；
10. V2 内容变窄后校验拦截：见 P6 附加步骤（依赖 reupload 换血机制，安排在 P6 而非本阶段，避免打断 P3 的数据依赖顺序）；
11. V3 校验异常的保守回落：LLM 层面的畸形返回/超时无法在真实数据验收里可靠复现，已改为 Go 单测覆盖（`internal/retrieval/fastpath_test.go`），本阶段不测；
12. V4 灰度关闭对照：`fast_path_verify=false` 时行为应等同旧版快路径（不校验直接采纳）——用 M1 的六题之一，临时改配置重跑 1 次，验证仍为 `path_type=fast` 且不因关闭校验而报错。

**通过标准**（准确硬门槛 + 召回软门槛）：

| 类别 | 项 | 标准 |
|------|----|------|
| 召回（软） | M1+M2 | 合计命中率 ≥70%（命中 = `path_type=fast` 且 `activation_link_ids` 非空；M1 18 次 + M2 6 次共 24 次） |
| 准确（硬） | M4 | 1 次超集排除 → `path_type=full` |
| 准确（硬） | M5 | F 组 9 次守门失效 = 0 |
| 准确（硬） | E3 | 展开含「神通」且不串用达梦答案 |
| 准确（硬） | M6/M7 | 各 3 次：空观测组下精确复现命中该链接 / 改写不命中该链接 |
| 准确（硬） | M8 | A2/T13 → `full` |
| 准确（硬） | V4 | 1 次关闭校验仍 `fast` 且无报错 |
| 观测 | M3、耗时下降、activation_success 落库 | 写入报告，不挡通过 |

M3 不设通过标准；V1 并入快路径正向样本观测。

### P4 证据挖掘与幻构拦截专项（标准 4）

1. 重跑 D 组 + 新增 3 个长表格题（积分规则表内任选），核验片段子串性质与行号定位（`GET /units/:id` 对照 line_start/line_end）；
2. 幻构拦截验证（二选一）：
   - 若有 fake LLM 注入手段：让挖掘 prompt 返回一条不存在的片段，验证被原文校验丢弃并留下重试/丢弃日志；
   - 纯黑盒：统计真实运行的挖掘校验日志，确认「校验不通过→重试或回退」路径至少被观察到一次（多跑长表格题提高触发概率），且任何回答的引用片段无一例外通过子串核验脚本。
3. 通过标准：核验脚本 0 例外；回退（mined=false）在 Page 解释抽屉可见「整段」标记。

### P5 自我修正与删除生命周期（标准 5 + 标准 6 删除侧，两域各删一个靶子）

**2026-08-13 改判后重写**：`verified→weakened` 这条迁移已不存在（`weakened` 状态整体废弃）。生命周期驱动的降权改为**直接到 `deprecated`**：`deriveAndPersistStatus` 每次都会先查目标 KP 的 lifecycle，非 `current` 时无论 `observed_conditions` 算出的置信度如何，强制 `status=deprecated`，单次触发、不等待任何失败次数或比值累积窗口（见 `docs/impl/v1/lifecycle.md` 步骤 4、`internal/activation/service.go` `deriveAndPersistStatus`/`NotifyPointsLifecycleChanged`）。

1. 前置：A1（报销规定）、T8（RAC 开启归档）两条链接此时 `status=verified`（P2 自然收敛产生，非人工确认）；
2. `DELETE /sources/:id` 删除《日常费用报销期限管理规定》和《Oracle RAC 开启归档》；
3. 立即验证生命周期：两个 source 全部 KU/KP `lifecycle=deprecated`；Bleve 查询不再返回；**同一时刻**（删除应触发 `NotifyPointsLifecycleChanged` 回调，不需要等 `POST /study/run`）A1、T8 对应链接 `GET /activation-links/:id` 的 `status` 应已变为 `deprecated`——这是本阶段与旧版最大的行为差异：迁移不再需要累积 `failure_count` 或跑 Study，是 lifecycle 变化的即时副作用；
4. 重问 A1、T8 的各变体各 3-4 次：
   - 期望回答不再引用已删除文档的任何 KP（标准 6：旧知识退出回答）；
   - 快路径行为：链接 `status=deprecated`，不参与 Match → 直接回落慢路径（`path_type=full`），产生 `activation_failure`（reason=not_cited/answer_gap，或因链接已不参与匹配而直接不出现在 `activation_hits` 中，两者都算通过，以实际观测记录为准）；
   - 回答应表现为知识缺口而非引用残留内容；技术侧注意区分：T8 删除后若回答从《Oracle RAC 问题汇总》等其他 RAC 文档拼凑出归档步骤属幻构（那些文档没有归档内容），必须计缺陷；A2（差旅费 3 个月）此时也应只剩《差旅费报销制度》一个来源，顺带验证同主题另一文档不受牵连；
5. `POST /study/run` → 核对 A1、T8 两链接此刻的 `learning_results`：第 3 步的即时 `deprecated` 迁移本身不写 `learning_result`（`deriveAndPersistStatus` 只是状态派生，不经过 `InsertLearningResult`），可审计性来自 `activation_failure`/`activation_gap` 这类 `learning_events`（能查到该链接在 KP 变更后的匹配尝试）与 `GET /activation-links/:id` 当前 `status=deprecated` 本身；不应再出现任何针对这两条链接的新增 `create_candidate`/`prune_condition` learning_result（说明它们此时确实"只降不升"，已退出学习循环）；
6. 通过标准：删除后 0 次回答引用 deprecated KP；两域降权迁移均在删除时即时自动发生（`status=deprecated`）且可通过「删除前 verified→删除后立即 deprecated」这一状态对照 + 后续无新增学习动作两点审计；快路径自动回落无 5xx。

### P6 Reupload 换血（标准 6 更新侧，Shadow Source 机制，两域各换一个靶子）

1. 制作衍生文件（两份）：《培训积分管理办法》修改版——「旷课-5」改「旷课-10」；《神通数据库优化》修改版——「MAX_CONNECTIONS 默认 128」改「默认 256」；
2. 分别 `POST /sources/:id/reupload` 上传修改版；
3. 处理期间验证：`GET /sources` 不出现影子行；此时问 A4 仍答「-5」、问 T15 仍答「128」（旧 KU 仍 current）；
4. 完成后验证：旧 KU/KP `lifecycle=superseded`；问 A4 答「-10」、问 T15 答「256」且引用新 KU；superseded KU 不进入任何回答；
5. 依赖旧 KP 的 ActivationLink 停止强化：重问后 `POST /study/run`，确认 A4、T15 链接的 `conditions[]` 中 `success_count`/`failure_count`/`mean` 均无增长（`adopt_count`/`fail_count` 这两个顶层聚合字段同理不再增长，仅作参考不作判据），且 `status` 已因 KP 非 current 强制为 `deprecated`（旧规则"目标 KP 非 current 只降不升"在新机制下体现为：`deriveAndPersistStatus` 检测到 lifecycle 非 current 时直接锁定 `deprecated`，不再计算任何条件的置信度，因此不存在"继续晋升"的可能）；
6. 失败分支：任选一个 source 再造一次必失败的 reupload（如上传空文件/触发 LLM 失败），验证原 Source 与旧 KU/KP 完全不受影响，影子 status=failed，`POST /sources/:id/reupload/retry` 可续跑。
7. 通过标准：两域换血原子性均成立（要么全新要么全旧，无中间态暴露）；新旧答案切换准确。

8. **附加步骤 V2（P3 轴二遗留项）：内容变窄后步骤 2a 校验拦截**——验证"命中≠答得对"这条防线（`docs/impl/v1/retrieval.md` 步骤 2a）在真实换血场景下生效，须在第 4 步（T15 已完成 128→256 换血、新 KU 仍完整覆盖"默认值+上限"两个事实）之后进行：
   1. 对已完成第一次换血的《神通数据库优化》再制作第二份衍生文件：仅删除"最大连接数上限 65535"这一句，保留"默认 256"及其余内容不变（制造客观可判定的缺失——T15 原问法"神通数据库最大连接数默认是多少、上限多少？"明确问了两个数字，新文档只对得上一个）；
   2. `POST /sources/:id/reupload` 上传该版本，等待换血完成；
   3. 重问 T15 原句：
      - 验证 `fast_path_verify=true`（默认配置）时，步骤 2a 判 `sufficient=false`，最终 `path_type=full`，`activation_hits` 保留原 T15 链接（产生 `activation_failure`，非 `activation_success`）；
      - 回答内容不得凭空编出"上限 65535"（旧值）或臆造新上限——验证幻构未发生；
   4. 通过标准：重问 3 次全部触发步骤 2a 拦截并正确回落，无一次把缺失的上限事实当作已知信息回答。

### P7 跨 Source KPN（两域）

1. `POST /sources/:id/kpn-cross`，制度域对项目考核制度触发，技术域对 Oracle 19c RAC 和达梦优化触发；
2. 验证 related：`knowledge_point_relations` 出现 `scope=cross` 的行；制度域至少命中「回款提成 ↔ 项目奖金发放」，技术域至少命中「11g RAC ↔ 19c RAC」与「达梦 ↔ 金仓/神通」概念簇各一组；重复触发不产生重复关系（幂等）；
3. contradicts fixture（两域各一）：制度域把 P6 的修改版积分办法作为**新 Source**（改名）导入与原版并存；技术域制作《达梦数据库优化（新版）》——「MAX_SESSION_STATEMENT 建议 20000」改「建议 5000」——同样作为新 Source 导入 → 触发 kpn-cross → 期望「-5/-10」「20000/5000」两组 KP 标记 `contradicts`，只标记并进入学习报告提示，不做消解、不改 lifecycle；
4. B1、B2、B4、B5 重问：KPN 扩展应把跨 Source 邻居带入 supporting 证据（B4 的 direct 在一篇 RAC 文档时，另一篇应经 cross 关系进入 supporting）；
5. 通过标准：cross 关系类型只出现 related/contradicts 两种，direction 均为 bidirectional；技术域的 related 不得跨产品乱连（达梦 BUFFER ↔ 神通 BUF_DATA_BUFFER_PAGES 语义对应算对，达梦 BUFFER ↔ Docker 端口算错，抽查 10 条 cross 关系人工判定合理率 ≥80%）。

### P8 Wiki 单层编译闭环（标准 7，两域各一个主题）

**2026-08-19 全文重写，取代此前"qualifying KP（confident_count/verified/days_active 三项门槛）+ wiki_candidate 自动候选 + 一阶 concept/fact 二阶 topic 两层架构"的全部口径**：`docs/impl/v1/wiki.md` 已于 2026-08-18 整体改判为单层架构（`docs/design/wiki-single-tier-revision.md`）。核心变化：

- Wiki 只有一种页面（`page_type` 恒为 `topic`），一次编译请求直接对人工指定的 `entry_ids`（数组，可跨多个 entry）产出成品页面，**不存在"页面聚合页面"**，两层架构下的一阶/二阶区分整体消失（原 P12 覆盖的二阶编译不再有实现对象，见下方 P12 章节改写）。
- **不存在任何自动候选识别入口**——没有 Study 主题聚类、没有 qualifying KP 自动标记、没有 `wiki_candidate` learning_result 生产方。因此本阶段不再有"轴一 verified 收敛 / 轴二 days_active"这套拆分，**不再需要任何培养到置信度门槛的步骤**。
- 材料准入判据收敛为一条：Core 展开只读取「entry 直属 KP 且 `lifecycle=current`」，**不要求 verified ActivationLink**——人工指定 entry_id 这个动作本身就是准入信号。编译材料从"整块塞给 LLM 的 qualifying KP 列表 + 切面聚类分组"换成 Core/Context/Conflict 子图（`buildKnowledgeSubgraph`，见 `docs/impl/v1/wiki.md`「编译材料」）：Core = entry 直属 current KP；Context = Core 中 KP 的一跳 related；Conflict = Core 中 KP 的一跳 contradicts；citation 白名单 = Core∪Context∪Conflict 覆盖的全部 point_id。
- 编译请求体是 `{"entry_ids": ["...", ...]}`（数组），**不再有 `page_type` 请求参数**——服务端不区分 concept/fact/topic。`POST /wiki/compile/analyze` → `POST /wiki/compile` 两步分析-生成链路本身不变（analyze 产出 claims/tensions 供人工确认，跳过 analyze 直接调 compile 时服务端内部自动跑一遍分析，效果等价）。

**生成质量链路（`docs/impl/v1/wiki-generation.md`，单层化不影响其机制，仅材料来源从"切面分组"换成"Core/Context/Conflict 分组"）**：
- 正文结构：五节标题齐全（"## 摘要"/"## 稳定结论"/"## 展开说明"/"## 待验证点"/"## 依赖来源"）——**不再核验"展开说明"下的切面三级标题**，Core/Context/Conflict 是材料组织方式，不是正文强制排版要求；
- 支持度核验：`wiki_claim_checks` 表应有与 claims 数量匹配的核验行，`verdict` 落在 supported/partial/unsupported 三者之一；
- `aliases`/`trigger_questions`：`aliases` 应能在 `subject_synonyms`（`status='active'`）表里查到来源，`trigger_questions` 每一条都应是 `traces.question` 里真实出现过的原文，不应有 LLM 现编的问法；
- 发布前质量门：先显式调 `POST /wiki/pages/:id/selfcheck` 看 `metrics`/`passed`，再走 `publish`；若质量门未过（真实 LLM 生成的页面大概率直接过闸，未过属观测性结果不算失败）会返回 409，用 `force=true` 覆盖后核对 `wiki_quality_checks` 最新一行 `forced=1`；
- 内聚度五项 ready 判据（`wiki_candidate.reason` 带"内聚度"字样）随自动候选识别一并删除，**不再核验**。

以上正文结构、支持度核验落库、aliases/trigger 真实性三项计入本阶段通过标准；质量门 passed/force 仅供观测记录，不设通过/失败判定。

**口径说明**：`GET /wiki/pages/:id` 只返回 `summary`/`aspects`（`aspects` 单层化后恒 `'[]'`，遗留字段），不返回 `aliases`/`trigger_questions`/`claim_checks[]`/`latest_quality_check`，本阶段改为直接读 `wiki_pages`/`wiki_claim_checks`/`wiki_quality_checks` 表核验（`v1_common.py` 的 `db_wiki_page_row`/`db_wiki_claim_checks`/`db_wiki_quality_checks`）。`force=true` 覆盖发布只把 `wiki_quality_checks.forced` 置 1，不写 `learning_results` 事件、不进学习报告。

**needs_recompile 自动标记（`docs/impl/v1/wiki.md`「重编译标记」，2026-08-18 收尾重新接线）**：来源只剩两条——a. lifecycle 传导（`SetUnitLifecycle` 影响 Core 归属的 entry）；新增 entry_id 归属变化（KU 被重新分类进/出某 entry）。原设计的 b（Study 周期扫描新增 qualifying KP）与 d（ActivationLink 越过服务门槛）已明确拍板不恢复（依赖问答置信度积累，与"编译准入不再依赖置信度"的方向冲突）。本阶段仍通过 (a) 触发 reupload 换血验证。

流程（对应 `test/v1/v1_p8_wiki_test.py`）：
1. 制度域主题「销售回款管理」：围绕应收账款文档密集问答（A9、A10、A11 及其变体）；技术域主题「Oracle RAC」：围绕 T10、T11、T18、B4 密集问答（11g/19c/问题汇总三篇文档的 KP 同 entry 聚簇）——问答目的只是产出足够的 current KP 内容供编译，**不追求任何置信度/次数门槛**；
2. 直接 `POST /wiki/compile/analyze`（`entry_ids` = 该域命中的全部 entry_id，无 `result_id`）→ 核对返回的 claims 均引用 Core/Context/Conflict 白名单内 point_id、tensions 结构合理 → 原样带回 `POST /wiki/compile` → 页面 draft：验证要素齐全（稳定结论、KP/KU/source_ref 回链、待验证点、更新时间、依赖 KU 列表），正文引用通过白名单校验，`page_type=topic`；
3. `POST /wiki/pages/:id/selfcheck` → `POST /wiki/pages/:id/publish`（未过质量门则 `force=true` 覆盖）；
4. 重问该主题问题：`path_type=wiki`，回答基于页面并附证据回链，且不产生激活类事件（wiki 直答不经激活层）；
5. 底层变化：制度页对应收账款 source、技术页对 19c RAC source 各做一次 reupload 换血（微改任一数值）→ 对应页面经 lifecycle 传导被标记 `needs_recompile`（不得自动重编译）；
6. 人工 `POST /wiki/pages/:id/recompile` → 新版本生成，revisions 可查旧版。
7. 通过标准：`compile → draft → selfcheck/publish → 检索命中 → reupload 触发 needs_recompile → recompile` 逐环节成立，任何环节自动越过人工确认即为失败；生成质量链路三项（正文五节结构、`wiki_claim_checks` 落库行数与 claims 数匹配且 verdict 合法、`aliases`/`trigger_questions` 可溯源）均为必过；quality gate 的 passed/force 仅记录，不影响本阶段通过判定。

### P9 用户反馈通道（标准 8）

**2026-08-13 改判后重写**：`weaken_failure_min`/`weaken_ratio_min` 及"降权"这一离散动作均已废弃，`correction_weight` 机制本身保留（`docs/impl/v1/trace.md`：一次 `user_correction` 关联到某链接时，按 `study.correction_weight`（默认 2）次数直接计入该链接对应观测条件的 `failure_count`），但触发点从"越过一个固定次数阈值就跳变到 weakened"改为"failure_count 增长把 `mean(cond)` 拉低，进而可能使该条件的 tier 从 self_graded/trusted 掉回 exploring，`status` 才可能相应从 verified 掉回 candidate"——是否真的掉回取决于这条条件在纠正前积累了多少 `success_count`（mean 公式是比例关系，不是简单计数阈值），因此"加速"这件事现在体现为**同样两次纠正，对 mean 的拉低幅度是自然 failure 的 `correction_weight` 倍**，而不是"直接跳变状态"。

1. 找一条 `status=verified` 的链接支撑的快路径回答（复用 P2/P3 中已自然收敛的链接，如 A1），记录纠正前该链接 `GET /activation-links/:id` 响应中命中条件的 `success_count`/`failure_count`/`mean`/`tier`；
2. 对该回答连续提交 2 次「纠正」反馈（`POST /traces/:id/feedback, type=correction`）；
3. 验证：`learning_events` 出现 `user_correction` 且 payload 含 `link_ids`；
4. `POST /study/run` 后重新 `GET /activation-links/:id`：对照纠正前后同一条件的 `failure_count` 应增长 `correction_weight`（默认 2）× 2 = 4（而不是单次自然 `activation_failure` 那样只 +1），`mean` 相应下降；对照组：另找一条同样起点的链接，只让它经历 2 次自然的 `activation_failure`（不提交纠正），`failure_count` 只 +2，`mean` 下降幅度明显更小——以两者 `mean` 下降斜率的差异证明"加速"效果，而不是去看是否跨过某个固定的 weaken 次数阈值；若纠正样本恰好把 `mean` 拉低到 `serving_confidence_min` 以下，`status` 会相应从 `verified` 变回 `candidate`，一并记录但不是本阶段唯一判据；
5. 「有用」反馈路径：提交 positive 不改变 `failure_count`，仅入报告；
6. 通过标准：两组（纠正 vs 自然失败）在同样 2 次事件后的 `failure_count` 增量与 `mean` 降幅存在可观察的倍数差异，且能从 `learning_events(user_correction)` payload 回溯到具体链接与 `correction_weight` 折算依据。

### P10 审计与报告总核（标准 3 收口）

**2026-08-13 改判后重写**：`promote`/`weaken`/`reverify`/`deprecate` 四个离散动作已不存在于 `learning_results.action` 词表中（`internal/activation/types.go` 当前完整枚举：`create_candidate`/`prune_condition`/`gap_flag`/`wiki_candidate`/`recompile_flag`/`entry_add_candidate`/`entry_merge_candidate`/`entry_add`/`entry_merge`/`topic_page_candidate`）。`prune_condition` 一个动作同时承担了旧版 `promote`/`weaken`/`reverify`/`deprecate` 里"链接观测条件发生了变化"的记录职责（`reason`/`confirmed_by` 区分是人工 reject 还是 Study 自动剪枝触发）——"晋升"这件事（`status` 从 candidate 派生为 verified）不再单独写一条 learning_result，它是 `create_candidate` 之后条件持续累积、`deriveAndPersistStatus` 每次写入时静默重算的结果，唯一能审计到的落点是 `GET /activation-links/:id` 当前的 `status`/`conditions[]`，而不是某一条历史 learning_result。

1. 拉取全量 `GET /study/results`：核对出现的 `action` 值均落在上述词表内，不应再看到 `promote`/`weaken`/`reverify`/`deprecate`/`confirm` 字样；每条 `create_candidate`/`prune_condition` 都有 `object_id`（link_id）、`reason`、关联 `event_ids` 三要素齐全；每次 Wiki 动作（`wiki_candidate`/`recompile_flag`/`topic_page_candidate`）同样齐全；
2. 随机抽 5 条 `create_candidate` result 反向核对：reason 中的 `mean_pre`/`width_pre` 与 `question_kp_cooccurrence` 表当时的 `confident_count`/`hit_count` 按公式重算一致；随机抽 5 条 `prune_condition` result，区分 `reason=manual_reject`（对应人工 `POST /activation-links/:id/reject` 调用）与自动剪枝原因，二者数量与 P2/P9 实际触发的操作次数对得上；
3. 补充审计"状态收敛"本身（因为它不再产生 learning_result）：随机抽 3 条当前 `status=verified` 的链接，核对其 `conditions[]` 中至少一条 `mean ≥ retrieval.serving_confidence_min`，且该 `mean` 用 `(success_count+1)/(success_count+failure_count+2)` 公式能从同一响应里的 `success_count`/`failure_count` 重算出来——这是本节标准 3「可审计」在新机制下的对应验收点，审计对象从"一次迁移记录"变成"当前派生状态与底层计数的一致性"；
4. 学习报告包含：本周期学习动作清单（`created_candidates`/`synonym_candidates_created`/`pruned_conditions`，取代旧版 `promoted`/`pending_promotions`/`weakened`/`reverified` 字段——`internal/study/types.go` `LearningActionsSummary` 当前结构）、kpn_citation_rate（MVP 链路未被破坏）、知识缺口清单（含 C 组两题、P4 的「命中但挖不出片段」类缺口若出现）。

### P11 Subject 同义词收敛（2026-07-24 新增，`activation.md` 附属表 + 步骤 3a）

**前提说明（对齐 P3 M3/P8 的既有措辞）**：`subject_synonym_gap` 事件要求同一 ActivationLink 的 intent/audience/constraint 全部匹配、仅 subject 未通过 `coreContained`——这个条件能否被真实 Session Parser 自然产出，和 P3 M3、P8 头部注释描述的"subject 抖动不可控"是同一个不确定性来源。本阶段因此拆成两半：**轴一是确定性的（API/状态机），轴二是观测性的（能否自然攒够阈值）**，不要求轴二必须达标才算通过。

**轴一：候选生命周期与 Match 生效（确定性，直接验收）**

1. 前置：复用 P2 已 verified 的 F1_PRE 链接（`达梦怎么查询会话执行情况`，`constraint=达梦`，是当前题库里唯一非空约束字段的链接，最适合控制变量）；
2. 用 F1_PRE 原问法的 intent/constraint 保持不变、仅替换 subject 措辞的变体连续问 `synonym_gap_min`（默认 3，见 `config/config.yml`）轮以上，每轮换一种不同表述（保证 `distinct_n ≥ synonym_gap_distinct_min`，默认 2）；
3. `POST /study/run` → 若自然达标：`GET /subject-synonyms?status=candidate` 应出现一条 `source=gap_mined` 的行，`learning_results(action=synonym_candidate, status=pending_confirm)` 可回溯到支撑的 `subject_synonym_gap` 事件；
4. `POST /subject-synonyms/:id/confirm` → 验证 `status=active`；`GET /subject-synonyms/:id` 的 `canonical`/`term` 符合"hit_count 更高一侧为 canonical，相同则取字符序靠前"的规则；
5. confirm 后立即重问「触发 gap 时用过的原始变体问法」→ 验证现在命中 `path_type=fast` 且落在 F1_PRE 同一 `link_id`（confirm 必须触发 `Matcher.InvalidateCache`，否则会读到旧缓存仍不收敛）；
6. reject 分支：另跑一条注定不该收敛的候选（如故意让某支撑事件語义确实不同源），`POST /subject-synonyms/:id/reject` → 验证 `status=rejected` 且重问原变体依旧 `path_type=full`（不因 reject 被误收敛）、且不会自动复活（重复触发同 pair 不再新增候选）。
7. 通过标准（轴一）：4/5/6 三步涉及的状态迁移与 Match 生效全部成立；这是本阶段唯一计入通过/失败的部分。

**轴二：自然达标观测（非确定性，只记录不判定）**

8. 记录第 2 步实际用了几轮/几种变体才使 `hit_count`、`distinct_n` 达标（或始终未达标），以及 Study 判定的 canonical 方向是否符合直觉；
9. 若第 2 步多轮后仍未自然产生候选（Session Parser 把 intent 也解析漂移，导致条件组根本对不上），改为直接在测试库里手工插入一条满足阈值的 `subject_synonym_gap` 事件集（仅用于验证轴一步骤 3-7 的状态机本身，不代表真实收敛概率），并在报告中如实注明"轴一验收改走人工造数据路径"；
10. 结果写入报告供后续判断 `synonym_gap_min`/`synonym_gap_distinct_min` 默认值是否需要调整，**本步骤不设通过/失败判定**。

### P12 Wiki 页面关系 / 写作草稿 / 回流防护（2026-08-19 全文重写，取代"两层架构扩展"）

**改判说明**：`docs/impl/v1/wiki.md` 已于 2026-08-18 整体改判为单层架构，本阶段原标题"两层架构扩展"（一阶概念/事实页 + 二阶主题页）随之失效，**整体删除**以下两项：`page_type=topic` 拒绝校验（单层化后 `page_type` 恒为 `topic`，不再有一阶端点拒绝该值这回事）；`topic_page_candidate` 主题候选观测（`POST /wiki/topics`、Study 主题四元组聚类均已随两层架构删除，没有对应产物）。本阶段保留、按新契约调整的是 P8 编译产物之上仍然成立的能力：页面关系派生、写作草稿、回流防护、Study 报告板块。

**前提**：依赖 P8 已跑通并至少一个领域 publish 成功（脚本从 `test/v1/results/v1_p8_wiki_*.jsonl` 读取最近一次结果里的 `page_id`，不重新培养信号）。

**范围边界（对照 `docs/impl/v1/wiki.md` 现行约束）**：页面关系只有 `related`/`contradicts` 两种（`contains` 已随二阶编译删除）；单层化后**所有已发布页面都参与关系派生**，不再有"跳过主题页"的例外；`wiki_pages.content` 只由编译产生，不存在任何 draft → page 写回接口；`wiki_drafts.source_page_ids` 单层化后恒为 `[page_id]`（原"组装模式"随 `contains` 一起废弃）；回流防护（来源标记/祖先关系跳过/统计排除）不受影响。

同 P8/P11 的既有拆分方式，本阶段也分两条轴，**只有轴一计入通过/失败判定**：

**轴一（确定性，必过）：现有端点/字段的契约行为**

1. `GET /wiki/pages/:id/relations` 对 P8 已发布的两个页面各查一次，响应结构含 `relation_type`（只能是 `related`/`contradicts`）/`other_page_id`/`derived_from`/`evidence`（P8 制度/技术两页分属不同领域，天然不会有 KPN 关系，返回空列表是预期，不是失败——本步骤只验证接口契约，不验证是否非空）；
2. 写作草稿全生命周期：`POST /wiki/pages/:id/drafts`（`mode=page`）→ `GET /wiki/drafts/:id`（`evidence_index` 非空、`stale` 字段存在、`source_page_ids` 恒为 `[page_id]`）→ `PATCH /wiki/drafts/:id` 改写标题与正文 → 重新 `GET /wiki/pages/:id` 核对页面正文与标题**完全不变**（代码中不存在 draft → page 写回路径）→ `DELETE /wiki/drafts/:id`；
3. 回流来源标记：`POST /sources` 附带 `origin=wiki_draft`、`origin_page_id=<P8 页面 id>`（multipart form 字段）导入一份最小文件，DB 直接核对 `sources.origin`/`sources.origin_page_id` 落库正确（自体祖先排除本身的匹配逻辑已由 Go 单测 `internal/unit/kpn_reflow_test.go::TestCrossSourceKPN_SkipsSelfAncestorEdges` 覆盖，本步骤只测 API 入口的字段透传）；
4. `POST /study/run` 后 `GET /study/reports/latest` 响应包含 `question_complexity` 板块（`{"groups": [...]}` 结构本身，不要求 `groups` 非空）。

**轴二（观测性，只记录，不判定）：页面关系自然形成情况**

5. 读 `knowledge_point_relations`/`wiki_page_relations` 总行数（P7 阶段的跨 Source fixture 可能已产生可供派生的关系）。

**通过标准**：轴一 1-4 步全部成立（relations 接口结构正确、草稿生命周期与写回防护、回流字段落库、报告新板块存在）即判 PASS；轴二第 5 步仅记录数字，不影响判定。

### P13 问题四元组归一化（2026-08-12 新增，config-gated，见 `internal/activation/tuplenorm.go`、`docs/impl/v1/retrieval.md` 步骤 2）

**前提说明**：`question_tuple_norm_enabled`（及子开关 `vector_match_enabled`）默认 `false`，本阶段临时改写 `config/config.yml` 并 `run.sh restart` 使其生效（同 P3 `set_fast_path_verify`/`restart_server` 的既有做法），跑完必须恢复默认值并再次重启——不应该在验收结束后把这两个开关遗留为打开状态。脚本 `test/v1/v1_p13_tuplenorm_test.py` 已实现该开-跑-关三段式，异常路径也会执行恢复。

1. 打开 `question_tuple_norm_enabled=true`（可选加 `--enable-vector` 同时打开 `vector_match_enabled`，需要 `config.yml` 的 `vector_model_dir` 指向真实已下载的 goformer 权重目录，否则 Tier2.5 优雅降级为跳过，不影响其余判定）；
2. 用同一潜在问题的两种不同措辞（`--variants-file` 提供，默认内置一对示例）分别通过独立 session 提问；
3. 核对第一次问法后 `question_tuple_norms` 表按 `domain_id` 各插入一行新的 canonical 记录（Tier4：全部未命中）；
4. 核对第二次问法后 `question_tuple_norms` **没有**新增行（说明 Tier1/2/2.5/3 之一命中了第一次的 canonical，四元组被替换后再送入 Matcher）；
5. 核对两次问法命中的 `knowledge_points` 存在交集，且交集里每个 point 在 `question_kp_cooccurrence` 上体现为**同一 `question_terms` 分组的 `hit_count` 增长**，而不是分裂出一个新分组——这是归一化要解决的"抖动导致学习信号碎片化"问题（同 MEMORY.md「V1 test root causes」记录的现象）本身是否被吸收的直接证据。

**通过标准**：3/4/5 三步全部成立即判 PASS。不直接断言 `path_type=fast`——归一化命中只解决"落到同一 canonical 四元组"，是否已经收敛到能服务快路径是 ActivationLink 自身的置信度收敛曲线（P2 覆盖），本阶段不重复验证。

### P14 ActivationBundle 跨 unit 歧义仲裁（2026-08-12 部分落地，见 `internal/retrieval/fastpath_helpers.go` `resolveBundleForAmbiguousHits`/`formCandidateBundle`）

**前提**：需要两条已达到 self_graded/trusted 服务档的 ActivationLink（`--link-id-a`/`--link-id-b`），分属不同 `knowledge_unit`，且在同一探测问法（`--probe-question`）上都能被 Tier1 精确匹配命中——可从 P2/P3 已培养的链接中挑选语义相近但归属不同文档/单元的一对（如「两篇 RAC 部署文档」场景，见 `test/v1/v1_common.py` `SOURCE_ABBREV_TO_TITLES` 的 `两篇 rac`），或专门为本阶段培养一对。

**已知实现现状（决定了轴二只能用人工种子验证）**：Bundle 成员的 `RecordMemberOutcome` 尚未接入真实 Trace 回写路径（`grep RecordMemberOutcome` 只命中方法定义与其自身单测），即"成员置信度随线上使用继续收敛"这条闭环写了原语、还没接线，无法端到端自然培养到 serving 阈值。

**轴一（确定性，直接验收，端到端可跑）**：

1. 用 `--probe-question` 提问，验证 `direct_point_ids` 同时包含两条链接各自的 `point_id`（说明真的触发了跨 unit 歧义，而不是被别的分支提前拦截）；
2. 验证本轮 `path_type != fast`（没有 verified Bundle 覆盖，正确回落慢路径）；
3. 验证 `activation_bundles` 表出现（或被追加了 observed condition 的已有）一条 `status=candidate`、`member_point_ids` 同时覆盖两个 point_id 的行。

**轴二（人工种子，只验证仲裁分支本身，不代表真实收敛概率，`--seed-member-confidence` 触发）**：

4. 若两个 point 之间已存在 KPN `contradicts` 关系：把轴一形成的 candidate Bundle 全部成员 `success_count` 人工摆到远超 `retrieval.serving_confidence_min`（默认 0.7）对应阈值的水平后重问，验证仍然 `path_type != fast`（冲突不应被仲裁合并）；
5. 若不存在 `contradicts`：同样人工摆高成员置信度后重问，验证 `path_type=fast` 且 `direct_point_ids` 仍同时包含两点（`bundlesConflict` 判空、`CoreMemberPointIDs` 正确合并）。

**通过标准**：轴一 1-3 步全部成立即判 PASS（可独立于轴二判定）；轴二 4/5（二选一，取决于两点间是否已有 `contradicts` 关系）作为附加判定，用于确认仲裁分支代码路径本身正确，不计入"真实收敛"相关的任何量化指标。

## 6. 量化验收指标汇总

| 指标 | 目标 | 采集阶段 |
|------|------|---------|
| 事实类问答正确率（慢路径） | 制度域（A/B1-B2/D1-D4/G1-G24）与技术域（T/B3-B5/D5-D7/G25-G48）**各自** ≥90% | P1 |
| 快路径 LLM 调用次数 | 命中快路径时 ≤3 次/题（含 `fast_path_verify`；旧基线无校验时约 2）；写入报告，不单列为硬失败 | P3 |
| 快路径耗时下降 | 相对 P1 同题下降情况写入报告；目标参考 ≥40%，**不挡 P3 通过**（准确优先） | P3 |
| 已培养问法快路径召回率 | M1+M2 合计 ≥70%（`fast` 且有 `activation_link_ids`） | P3 |
| 对象守门失效次数 | 0（F 组 9 次）；E3 不串台 | P3 |
| 片段子串核验 | 0 例外 | P1、P4 |
| 挖掘回退率（mined=false） | ≤30%，且回退可见（技术域代码块类 KU 单独统计回退率，作 V2 输入） | P1、P4 |
| 删除后 deprecated KP 引用次数 | 0（两域靶子各计） | P5 |
| 跨 Source 关系合理率 | 抽查 ≥80%，类型仅 related/contradicts | P7 |
| 状态迁移审计完整率 | 100%（迁移必有 result+reason+events） | P10 |
| 缺口题幻构率 | 0（C 组不得编造引用，技术域 C3/C4 重点盯） | P1 |
| Wiki 页面关系/草稿/回流契约行为通过率 | 100%（relations 结构、草稿生命周期与写回防护、回流字段落库、report 新板块，4 项全过） | P12 |
| Wiki 生成质量链路结构通过率 | 100%（正文五节结构、`wiki_claim_checks` 落库匹配、aliases/trigger_questions 可溯源三项全过；质量门 passed/force 仅记录不判定） | P8 |

## 7. 执行注意事项

- **时序控制**：全程手动 `POST /study/run`，禁止依赖 Ticker，否则事件 processed 状态不可控；
- **变体问法是硬要求（共现侧）**：Study 创建候选链接看的是 `question_kp_cooccurrence` 换算出的 `mean_pre`/`width_pre`（`create_confidence_min`/`create_width_max`），同一字面重复问只累计同一 `question_hash` 下的计数，对 `mean_pre` 的抬升有限；P2 培养清单必须备 ≥2 问法。但自四元组精确匹配起，变体**不保证**命中同一 ActivationLink——P2 的收敛硬门槛只要求至少跑通 1 条归属链接自然到达 `self_graded`/`trusted` 档（见 P2 步骤 4），其余观测条件的 `mean`/`tier` 分布作观测；
- **P2 链接归属**：确认/驳回/对照组按题号独占归属，勿用「题的 direct point_id 并集」做多题共享判定（A9↔A11、T12↔F1_PRE 邻近簇会误伤）；
- **阶段间不清库**：P2-P12 依赖 P1 积累的事件；靶子文档已按域错开（P5 删报销规定+RAC 归档、P6 改培训积分+神通、P7 新增两份 contradicts fixture、P8 改应收账款+19c RAC、P11 复用 F1_PRE 且不得在 P11 前调整其四元组字段、P12 直接读 P8 落盘结果不重新培养信号），执行时勿调换；P12 必须排在 P8 之后。
- **P13/P14 排序与库依赖**：两者都是纯增量能力（config-gated 开关 + 新表 + 新的仲裁分支），不依赖也不破坏 P1-P12 的既有信号，理论上可在 P2（已有 verified/self_graded 链接可复用）之后的任意时点插入,不要求清库或重新导入文档。惟一的硬顺序要求是各自内部：P13 必须先把 `question_tuple_norm_enabled` 改回 `true` 并重启（脚本自动做，见 P13 说明），跑完必须恢复默认值再重启，避免污染后续阶段（P13 之后如果还要跑别的阶段，务必确认脚本的 `finally` 块确实执行了恢复，非正常中断——如 kill -9——需要人工核对 `config.yml` 是否被卡在 `true`）；P14 轴二依赖轴一先产出 candidate Bundle 的 `bundle_id`，不能单独跑。两阶段之间无先后依赖，可按任意顺序执行，也可以反复重跑（P13 每次都会重新走一遍开-测-关，P14 的 `formCandidateBundle` 对同一核心成员集合是幂等追加，不会因重跑而产生重复行）。
- **真实 LLM 的波动**：正确率类指标按题判要点命中而非逐字比对（技术域例外：命令与参数名必须逐字对）；单题失败先重跑一次排除 LLM 抖动，复现两次才计为缺陷；
- **缺陷归因**：每个失败点先区分「提取期缺陷（KU/KP 就没有该事实）」与「检索/回答期缺陷」，前者不属于 V1 目标范围但需记录；
- **技术文档的特有风险**：代码块/长表格密集，KU 按行切片可能把命令截断（导入完成后应抽查 K8S、达梦、AlwaysOn 三篇长文档各 2 个 KU 的 `line_start/line_end` 切片是否对应原文完整片段，重点看代码块/表格是否被切断）；LLM 对通用技术知识有强先验，容易"不看文档也答对"或"用先验覆盖文档细节"——凡技术题必须核验引用片段确实来自对应文档，答对但引用为空/错源一律计缺陷；
- **不要用的事实**：《Oracle 11g RAC》文内网段表述自相矛盾（网络表私有网段 222.222.222.0/24，服务器表却是 172.16.1.x），勿以此设题，也不要当成系统缺陷。
