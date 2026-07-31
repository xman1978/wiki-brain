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
| 7 | Wiki 初版（候选→确认→编译→检索命中→底层变化→待重编译） | P8 |
| 8 | 反馈生效（user_correction 加速链接信号） | P9 |

另覆盖三项不在成功标准列表但属 V1 交付范围的能力：跨 Source KPN（P7）、激活条件的对象/约束硬性守门（F 组，`activation.md` 步骤 2——技术域"同问法不同产品"是守门失效最容易暴露的场景）、subject 同义词归一化（P11，2026-07-24 新增——P3 M3 当时记录的"覆盖靠积累，不靠模糊匹配"观测项，现已在 Match 侧落地为 `subject_synonyms` 表 + `SynonymResolver`，见 `activation.md` 附属表与步骤 3a）。

第四项：Wiki 两层架构扩展（P12，2026-07-30 新增，`docs/impl/v1/two-tier-task-brief.md`）——页面关系派生、主题页候选与二阶编译、检索骨架注入、写作草稿、回流防护，均为 P8 单层闭环之上的扩展能力，不改变标准 7 本身的判定口径。

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
| 绩效管理制度 | 人力/绩效 | 与培训积分、项目考核构成"绩效"概念簇，供 Wiki 候选与概念测试 |
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

`config.yml` 测试期建议值（目的：把"反复问答天数"压缩到单日可完成，不改变判定逻辑）：

```yaml
study:
  schedule_interval:       "24h"   # 调大，统一用 POST /study/run 手动触发，保证时序可控
  candidate_confident_min: 3       # 默认 5，压低共现门槛
  promote_success_min:     3       # 保持默认
  promote_distinct_min:    2       # 保持默认（要求 ≥2 个不同问法，测试集已备变体）
  weaken_failure_min:      3
  auto_promote:            false   # 必须保持 false，验证人工确认流
retrieval:
  fast_path:               true
  fast_path_fallback:      true
evidence:
  enabled:                 true
```

**观察面**：

- API：`POST /answer`（响应 `path_type`）、`GET /traces`（`path_type`/`activation_link_ids`）、`GET /activation-links`、`GET /study/results`、`GET /learning-events`、`GET /wiki/pages`、`GET /units/:id`（lifecycle）；
- DB：`learning_events`、`activation_links`、`learning_results`、`traces`、`knowledge_units.lifecycle`、`knowledge_point_relations.scope`、`wiki_pages`；
- 日志：LLM 调用次数按 `LLMClient` 调用日志逐问计数（这是标准 2 的核心指标，若当前无此日志需先补一行 slog）。

## 4. 问答测试集

期望答案以文档原文为准；「期望证据」指引用片段应落在的原文位置。每题标注用途。A/T/G 三组同时内嵌于 `test/mvp/mvp-acceptance-test-plan.md` 第 4 节（准确率测试集），两处同源，修改须同步。

### 4.1 A 组 · 单文档事实类（学习信号主力，每题含 2-3 个变体问法）

| ID | 问题（主问法 / 变体） | 期望答案要点 | 期望证据来源 |
|----|---------------------|-------------|-------------|
| A1 | 招待费用报销期限是多久？ / 请客吃饭的发票多久内要报掉？ / 业务招待费超过多长时间不能报销？ | 费用实际发生之日起 45 天内；逾期财务不受理、费用个人承担 | 报销规定·第一条 |
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

### 4.2 T 组 · 技术单文档事实类（技术域学习信号主力，每题含 2-3 个变体问法）

| ID | 问题（主问法 / 变体） | 期望答案要点 | 期望证据来源 |
|----|---------------------|-------------|-------------|
| T1 | Docker Swarm 集群管理端口是哪个？ / Swarm 防火墙要开哪些端口？ | 2377/TCP 集群管理；7946/TCP 节点通讯；4789/UDP overlay | Docker Swarm·环境准备 |
| T2 | Swarm 里 Manager 和 Worker 节点分别干什么？ | Manager 负责集群管理、调度和控制（仅一个 Leader）；Worker 运行容器并上报状态 | Docker Swarm·架构 |
| T3 | K8S 集群最少需要几台服务器？ / 单机能装 K8S 吗？ | 高可用至少 3 台（1 Master 2 Node）；测试可单机部署、移除污点后跑应用 | K8S·部署要求 |
| T4 | K8S 1.24 之后默认容器引擎是什么？ / 现在还用 dockershim 吗？ | 默认集成 containerd，不再需要 dockershim，调用链更短更稳定 | K8S·容器引擎 |
| T5 | 用 crictl 前要先配置什么？ | `crictl config runtime-endpoint unix:///run/containerd/containerd.sock` | K8S·容器引擎 |
| T6 | MySQL 主从复制的原理？ / binlog 在主从同步里怎么流转？ | 主库写 binlog → 从库经同步账户读入 replaylog → 从库重放执行 | MySQL·第 1 节 |
| T7 | MySQL 从库怎么指向主库同步位？ | `change master to master_host=... master_log_file/master_log_pos`，取值与主库 `show master status` 一致 | MySQL·2.2 |
| T8 | Oracle RAC 怎么开启归档？ / RAC 库开归档的步骤？ | srvctl stop → start -o mount → `alter database archivelog` → 重启实例 → `archive log list` 验证 | RAC 开启归档 |
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
| D1 | 迟到早退怎么扣分？ | 「迟到/早退累计2次 -1」所在表行 | 引用不应包含整张积分规则表 |
| D2 | 考试成绩多少分要扣分？ | 「考试成绩低于80分（含80分）-1」 | 同上 |
| D3 | 历史回款里 2021 到 2022 年签的合同催收周期？ | 「2021年至2022年（含2022年）……周期为6个月」句 | 不应把 3 个月/9 个月档一并引入 direct |
| D4 | 平台奖励方案的免费条件？ | 「累计10个及以上，免除当年全部使用费用，有效期一年」 | — |
| D5 | 达梦 MAX_OS_MEMORY 建议值？ | 「MAX_OS_MEMORY 90 ……推荐为90%」表行 | 不应把整张 5.4 内存参数表（13 行）全部作为 direct 证据 |
| D6 | 神通 FILE_IO_OPTION 的 BUFFER 和 DIRECT 怎么选？ | 「BUFFER……批量写入优于 DIRECT；DIRECT……大量随机写入更好」表行 | — |
| D7 | 达梦 DIRECT_IO 在龙芯服务器上建议设多少？ | 「龙芯服务器建议使用 2 为操作系统 NATIVE IO」片段 | 长表行内的定语提取，摘选粒度重点样本 |
| D8 | 万相公文伙伴提货 5000 用户以上什么价？ | 「5000 以上 按照 4 折提货（800）」表行 | — |

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

### P0 导入与提取验收（MVP 链路回归 + lifecycle 初始状态)

1. 前置检查：`preset/domains.json` 必须覆盖技术域（数据库/容器/运维类 Domain 与 Concept），否则 Domain 预过滤会把技术问题全部错杀在入口——缺失先补 preset 再开测；
2. 依次上传 21 份文档 → 等 `source_process`/`unit_extract` 完成；
3. 验证：21 个 source 状态 ready；每份文档 KU/KP 数量 > 0 且抽查 KU 的 `line_start/line_end` 切片能对上原文（技术文档必抽：K8S、达梦、AlwaysOn 三篇长文档各 2 个 KU，重点看代码块/表格是否被切断——命令步骤被从中间切开属提取期缺陷）；所有 KU/KP `lifecycle=current`；`GET /sources` 无影子行。
4. 通过标准：无提取失败；制度域关键事实（45 天、-5 分、256 元、6%、25%）与技术域关键事实（2377/TCP、containerd、MAX_SESSION_STATEMENT 20000、MAX_CONNECTIONS 128、srvctl 归档五步、chmod u+s）均能在某个 KP/KU 中找到。

### P1 慢路径基线（标准 4 前半 + P3 的对照组）

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

### P2 学习转化：candidate 形成与人工晋升（标准 1 前半 + 标准 3）

1. 培养清单（制度域 6 题 + 技术域 5 题，覆盖两域）：A1、A2、A4、A9、A11、A12 + T8、T12、T15、T13，以及 F1 前置问题「达梦怎么查询会话执行情况」；每题追加提问 2 个变体问法（保证 distinct question_hash ≥ 2 且 confident）。靶子约束：A4 必须入选（P6 制度侧靶子）、T15 必须入选（P6 技术侧靶子）、T8 必须入选（P5 技术侧靶子）；
2. `POST /study/run`；
3. 验证 candidate 创建：`GET /activation-links?status=candidate` 出现上述问题对应链接；每条链接有 `learning_results(action=create_candidate, status=applied)`，reason 含 confident_count/ratio/触发来源事件 id，能 JOIN 回 `learning_events`；技术域链接的激活条件应带对象/约束字段（如"达梦"），这是 F 组守门的前提，字段为空要记录（将走回退匹配路径）；
4. 再各问 1-2 次（累计 success_n≥3、distinct≥2）→ `POST /study/run` → 验证出现 `action=promote, status=pending_confirm` 的 learning_result，链接状态未变（auto_promote=false 不得自动晋升）；
5. 在 Page（或直接 `POST /activation-links/:id/confirm`）确认 A1、A4、A9、A12、T8、T12、T15 及 F1 前置链接晋升，A2、T13 执行 reject；
6. 验证：confirm 的链接 status=verified 且有对应 learning_result 落库；reject 的链接不参与后续召回。
7. 通过标准：candidate 不经确认绝不出现在 verified 列表；每次迁移都能从 learning_result → reason → event_ids 完整回溯（标准 3）。

### P3 快路径生效（标准 1 后半 + 标准 2）+ 对象守门 + ActivationLink 可用性验证

**2026-07-19 改版说明**：`activation.md` 步骤 2 已从"打分+阈值"改为"四元组归一化后完全匹配"（不再有 `activation_match_min` / `activation_match_min_fallback` 阈值），并新增步骤 2a 快路径证据充分性校验（`fast_path_verify`，1 次 LLM）。旧版"换第三种问法验证仍能命中"的前提（打分容忍改写）不再成立——完全匹配下，改写后的问题是否命中，取决于 Session Parser 对 subject/intent 抽取标签的稳定性，这是待观测的效果指标，不是本次要验收的正确性标准。本节按两个轴重新设计：**轴一验证"链接是否被正确找到"（匹配正确性），轴二验证"命中后证据是否真能完整回答问题"（步骤 2a 校验）**。

**轴一：匹配正确性**

1. M1 精确复现：对 A1、A9、A12、T8、T12、T15 六条已 verified 链接，各用**培养时的主问法原句**重问 3 次；
   验证：`path_type=fast`，trace 的 `activation_link_ids` 命中对应链接，Page 显示快路径徽标；
2. M2 归一化容差：六题各任选 1 题，只调整词序或增删多余空白/标点（不改变用词），重问 1 次；
   验证：仍然 `path_type=fast`——证明归一化没有引入不必要的字面依赖；
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
5. M5 对象/约束错配排除（F 组，判定口径不变）：F1、F2、F3 各 3 次，验证前置 verified 链接不被同句式异对象问题错误激活；同时 E3 追问一轮，确认补全后的问题也不串台；
6. M6/M7 四元组缺失回退——**回退分支在真实数据里无法自然培养出**（试跑已证实：Session Parser 对任意真实问题都会抽出非空 subject/audience/constraint，即使是 A11 这种纯数字条件的问题也不例外；回退分支存在的意义是兼容"存量迁移链接"或"本轮 Session 解析异常降级"，两者都不是健康链路的自然产物），改为直接改库模拟存量链接：
   - 前置：任选一条已 verified 的链接（如 F1_PRE），`UPDATE activation_links SET subject_terms='', intent_terms='', audience='', constraint_terms='' WHERE link_id=?`，模拟其为存量迁移链接；`POST /study/run` 或等价方式确认 Matcher 缓存能反映改动（必要时重启触发缓存重载）；
   - M6 精确复现：用该链接培养时的原始问句原样重问 → 应命中（`path_type=fast`）；
   - M7 改写不命中：同一链接，问题换一种表述重问 → 不命中（`path_type=full`）——回退分支不再有包含度阈值兜底改写，这是新行为；
   - 收尾：测试后如需保留该链接供后续阶段使用，需手工把四个字段改回原值（或重新培养）。
7. M8 状态过滤（2026-07-22 修订）：未晋升的 A2、T13 问题仍走 `path_type=full`（candidate 可 Match 记信号，但不走快路径）；weakened/deprecated 或目标 KP 非 current 的链接不参与匹配（与 P2/P5 的验证点呼应，本阶段不重复造场景，仅复核一次现状）。

**轴二：证据充分性校验（步骤 2a，`fast_path_verify`）**

8. V1 正常充分：M1 六题的快路径回答本身就是正向样本——额外核对 trace 中可观察到 `fast_verify` 这次 LLM 调用发生过（若无独立日志字段，退化为核对总调用次数从旧基线的 2 次变为 3 次：证据挖掘 1 + 校验 1 + Answer 1）；
9. V2 内容变窄后校验拦截：见 P6 附加步骤（依赖 reupload 换血机制，安排在 P6 而非本阶段，避免打断 P3 的数据依赖顺序）；
10. V3 校验异常的保守回落：LLM 层面的畸形返回/超时无法在真实数据验收里可靠复现，已改为 Go 单测覆盖（`internal/retrieval/fastpath_test.go`），本阶段不测；
11. V4 灰度关闭对照：`fast_path_verify=false` 时行为应等同旧版快路径（不校验直接采纳）——用 M1 的六题之一，临时改配置重跑 1 次，验证仍为 `path_type=fast` 且不因关闭校验而报错。

**通过标准**：M1 18 次、M2 6 次、M4 1 次（超集排除）、M5 9 次（F 组）+ E3 1 次、M6/M7 各 F1_PRE 3 次全部满足验证点；M3 不设通过标准，仅记录观测数据；V1 通过标准并入 M1；V4 1 次验证通过。

### P4 证据挖掘与幻构拦截专项（标准 4）

1. 重跑 D 组 + 新增 3 个长表格题（积分规则表内任选），核验片段子串性质与行号定位（`GET /units/:id` 对照 line_start/line_end）；
2. 幻构拦截验证（二选一）：
   - 若有 fake LLM 注入手段：让挖掘 prompt 返回一条不存在的片段，验证被原文校验丢弃并留下重试/丢弃日志；
   - 纯黑盒：统计真实运行的挖掘校验日志，确认「校验不通过→重试或回退」路径至少被观察到一次（多跑长表格题提高触发概率），且任何回答的引用片段无一例外通过子串核验脚本。
3. 通过标准：核验脚本 0 例外；回退（mined=false）在 Page 解释抽屉可见「整段」标记。

### P5 自我修正与删除生命周期（标准 5 + 标准 6 删除侧，两域各删一个靶子）

1. 前置：A1（报销规定）、T8（RAC 开启归档）两条 verified 链接已就位（P2）；
2. `DELETE /sources/:id` 删除《日常费用报销期限管理规定》和《Oracle RAC 开启归档》；
3. 立即验证生命周期：两个 source 全部 KU/KP `lifecycle=deprecated`；Bleve 查询不再返回；
4. 重问 A1、T8 的各变体各 3-4 次：
   - 期望回答不再引用已删除文档的任何 KP（标准 6：旧知识退出回答）；
   - 快路径行为：链接目标 KP 已非 current，反查为空 → 回落慢路径（`path_type=full`），产生 `activation_failure`（reason=not_cited/answer_gap）；
   - 回答应表现为知识缺口而非引用残留内容；技术侧注意区分：T8 删除后若回答从《Oracle RAC 问题汇总》等其他 RAC 文档拼凑出归档步骤属幻构（那些文档没有归档内容），必须计缺陷；A2（差旅费 3 个月）此时也应只剩《差旅费报销制度》一个来源，顺带验证同主题另一文档不受牵连；
5. `POST /study/run` → 验证 A1、T8 两链接 verified→weakened 的 learning_result（failure_n≥3 且比值达标），weakened 后不再参与召回；
6. 通过标准：删除后 0 次回答引用 deprecated KP；两域降权迁移均自动发生且可审计；快路径自动回落无 5xx。

### P6 Reupload 换血（标准 6 更新侧，Shadow Source 机制，两域各换一个靶子）

1. 制作衍生文件（两份）：《培训积分管理办法》修改版——「旷课-5」改「旷课-10」；《神通数据库优化》修改版——「MAX_CONNECTIONS 默认 128」改「默认 256」；
2. 分别 `POST /sources/:id/reupload` 上传修改版；
3. 处理期间验证：`GET /sources` 不出现影子行；此时问 A4 仍答「-5」、问 T15 仍答「128」（旧 KU 仍 current）；
4. 完成后验证：旧 KU/KP `lifecycle=superseded`；问 A4 答「-10」、问 T15 答「256」且引用新 KU；superseded KU 不进入任何回答；
5. 依赖旧 KP 的 ActivationLink 停止强化：重问后 `POST /study/run`，确认 A4、T15 链接无 adopt_count 增长、无晋升/reverify（文档规则：目标 KP 非 current 只降不升）；
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

### P8 Wiki 初版闭环（标准 7，两域各一个主题）

**前提说明（2026-07-29 修订，qualifying KP 口径变更，见 `docs/design/wiki-compilation.md`"反复激活、多次验证、持续采用不是命中次数"）**：qualifying KP 现在同时要求 (a) `confident_count ≥ wiki_confident_min`（不变）、(b) 对应 ActivationLink 已 `status=verified`（新增，复用晋升机制而非另立次数口径）、(c) 概念级还额外要求 `days_active ≥ wiki.qualifying_min_days_active`（默认 7，衡量"持续采用"）。参照 P11 的既有拆分方式，本阶段也拆成两半：**轴一是确定性的（verified 链接门槛按代码逻辑生效，可直接验收），轴二是观测性的（days_active 门槛能否在单次脚本运行的自然日窗口内自然达标）**，不要求轴二必须达标才算通过——脚本单次运行大概率仍停在 `needs_more_data`（仅因 days_active 不够），这不算失败，只要能证明"verified 门槛生效 + 门槛逻辑本身正确"即可。真正要观测 days_active 生效，需要跨真实自然日多次运行（`--skip-cultivate` 续跑）或临时调低 `config.yml` 的 `qualifying_min_days_active` 冒烟测试，两者都不在本阶段的必过范围内。

编译流程也改为两步（`docs/impl/v1/wiki.md` 步骤 2）：`POST /wiki/compile/analyze` 先产出拟采用的论断结构（claims/tensions，不落库），人工确认后把该结构原样（或编辑后）带回 `POST /wiki/compile`；跳过 analyze 直接调用 compile 时服务端会自动内部跑一遍分析，效果等价，仍应验收。

**生成质量链路新增核验（2026-07-31，`docs/impl/v1/wiki-generation.md` 简化版，P0 已实现 + P1 切面聚类已实现）**：概念页编译内部现在会先把 qualifying KP 按「切面」分组（Louvain 社区检测，程序计算、不调 LLM）、生成后做支持度核验（判断每条结论是否真被引用材料支持，不只是引用的 point_id 合法）、发布前跑质量门回放（用该页 KP 曾被慢路径答对过的真实问法重新考一遍页面）；`aliases`/`trigger_questions` 不再由 LLM 生成，改为程序查 `subject_synonyms` 表与真实 confident 问法取样。这些都不改变 `analyze`/`compile` 的外部契约（仍是扁平 `claims[]`/`tensions[]`，只是 claim 可能带一个可选的 `aspect_id`），因此不新增编译步骤，但本阶段核验范围要新增：
- 正文结构：应含"## 摘要"一节，且"## 展开说明"下按切面出现多个"### "三级标题（不强制断言具体切分数量，真实 LLM 输出允许合理浮动）；
- 支持度核验：`wiki_claim_checks` 表应有与 claims 数量匹配的核验行，`verdict` 落在 supported/partial/unsupported 三者之一；
- `aliases`/`trigger_questions`：`aliases` 应能在 `subject_synonyms`（`status='active'`）表里查到来源，`trigger_questions` 每一条都应是 `traces.question` 里真实出现过的原文，不应有 LLM 现编的问法；
- 发布前质量门：先显式调 `POST /wiki/pages/:id/selfcheck` 看 `metrics`/`passed`，再走 `publish`；若质量门未过（真实 LLM 生成的页面大概率直接过闸，未过属观测性结果不算失败）会返回 409，用 `force=true` 覆盖后核对 `wiki_quality_checks` 最新一行 `forced=1`；
- 概念级"ready"判定新增第五项内聚度门槛（`wiki_candidate` 的 `reason` 字段文本会带上"内聚度 X.XX"，观测记录即可，不设通过/失败判定——本阶段两个测试话题都是"围绕同一批文档密集问答"构造出来的，天然应该内聚，不预期触发 `concept_split_signals`）。

以上几项里，正文结构、支持度核验落库、aliases/trigger 真实性三项计入本阶段通过标准（结构性核验，"机制是否运行"，不是"LLM 写得好不好"）；质量门 passed/force 与内聚度数值仅供观测记录，不设通过/失败判定（与既有"轴一/轴二"拆分惯例一致）。

**口径说明（已按现有代码定稿，`docs/impl/v1/wiki-generation.md` 第 7.4/11 节已同步改写，非待确认落差）**：`GET /wiki/pages/:id` 只返回 `summary`/`aspects`，不返回 `aliases`/`trigger_questions`/`claim_checks[]`/`latest_quality_check`——这几项和 `learning_events` 一样，属于"API 之外的观察面"，本阶段改为直接读 `wiki_pages`/`wiki_claim_checks`/`wiki_quality_checks` 表核验（脚本已按此方式实现，见 `v1_common.py` 的 `db_wiki_page_row`/`db_wiki_claim_checks`/`db_wiki_quality_checks`）。同理，`force=true` 覆盖发布只把 `wiki_quality_checks.forced` 置 1，不写 `learning_results` 事件、不进学习报告——force 是编译/发布链路内部的一次性人工决定，不是 Study 要追踪的学习动作，本阶段只核对 `forced=1`。

**两层架构收紧（`docs/impl/v1/two-tier-task-brief.md`，本阶段脚本已同步修正）**：`POST /wiki/compile`、`POST /wiki/compile/analyze` 的 `page_type` 参数现在**只接受 `concept`**——主题页（`page_type=topic`）不再由这两个端点产出，而是完全由二阶编译端点 `POST /wiki/pages/:id/topic/analyze`、`POST /wiki/pages/:id/topic/compile` 负责（详见 P12）。本阶段每个领域培养的仍是单一 concept（一个业务话题对应一个 KP 聚簇），`page_type` 传 `concept` 即可，不要再传 `topic`。

1. 制度域主题「销售回款管理」：围绕应收账款文档密集问答（A9、A10、A11 及其变体，凑足 wiki_confident_min 与 wiki_kp_min）；技术域主题「Oracle RAC」：围绕 T10、T11、T18、B4 密集问答（11g/19c/问题汇总三篇文档的 KP 同 Concept 聚簇，天然满足多 KU 依赖，比单文档主题更能检验编译的综合能力）；
2. `POST /study/run` → 对每个被密集问答覆盖的 point_id，`GET /activation-links?point_id=...&status=candidate` 应能找到候选链接 → 逐个 `POST /activation-links/:id/confirm` 促成 `verified`（轴一，必过：这一步验证"多次验证"门槛确实复用了既有晋升状态机，而不是另造一套次数口径）；
3. 再次 `POST /study/run` → 验证两域各出现 `action=wiki_candidate, status=pending_confirm`（若因 days_active 未达标仍是 `needs_more_data`，按前提说明记为观测性缺口，不判失败，但要核实 `Reason`/`Stats` 中 `qualifying_kp_count`、`kpn_connection_count` 已经达标，只差 `days_active`）；
4. 若拿到 `pending_confirm`：`POST /wiki/compile/analyze`（concept_id + result_id）→ 核对返回的 claims 均引用白名单内 point_id、tensions 结构合理；Page 确认后把该结构带回 `POST /wiki/compile` → 页面 draft：验证要素齐全（稳定结论、KP/KU/source_ref 回链、待验证点、更新时间、依赖 KU 列表），正文引用通过白名单校验（不引用 analyze 阶段未确认的 point_id）；
5. `POST /wiki/pages/:id/publish` → 重问该主题问题：`path_type=wiki`，回答基于页面并附证据回链，且不产生激活类事件（wiki 直答不经激活层）；
6. 底层变化：制度页对应收账款 source、技术页对 19c RAC source 各做一次 reupload 换血（微改任一数值）→ `POST /study/run` → 对应页面被标记 `needs_recompile`（不得自动重编译）；
7. 人工 `POST /wiki/pages/:id/recompile` → 新版本生成（内部自动走分析→生成两步，不额外暴露 analyze 预览接口），revisions 可查旧版，编译记录可追溯到触发的 Learning Event。
8. 通过标准：readme 描述的完整生命周期「候选→确认→编译→检索命中→底层变化→待重编译」逐环节成立，任何环节自动越过人工确认即为失败；verified 链接门槛（轴一）必须验证到位，days_active 门槛（轴二）达标与否不影响通过判定；新增（生成质量链路）：正文五节+切面三级标题结构齐全、`wiki_claim_checks` 落库行数与 claims 数匹配且 verdict 合法、`aliases`/`trigger_questions` 均可溯源到 `subject_synonyms`/真实 `traces.question`（不得是 LLM 现编）——三项均为必过；quality gate 的 passed/force 与内聚度数值仅记录，不影响本阶段通过判定。

### P9 用户反馈通道（标准 8）

1. 对一条 verified 链接支撑的快路径回答，连续提交 2 次「纠正」反馈（`POST /traces/:id/feedback, type=correction`）；
2. 验证：`learning_events` 出现 `user_correction` 且 payload 含 `link_ids`；
3. `POST /study/run`：按 correction_weight=2，2 次纠正折算 4 次 failure，应直接触发降权（对照组：仅 2 次自然 activation_failure 不足 weaken_failure_min=3 不降权）——以此证明「加速」作用可观察；
4. 「有用」反馈路径：提交 positive 不改变链接状态，仅入报告；
5. 通过标准：纠正加速效果在 learning_result 的 reason 统计数字中可见。

### P10 审计与报告总核（标准 3 收口）

1. 拉取全量 `GET /study/results`：每条 ActivationLink 状态迁移（create/promote/weaken/reverify/deprecate）、每次 Wiki 动作都有记录，`object_id`、`reason`、关联 event_ids 三要素齐全；
2. 随机抽 5 条 result 反向核对：reason 中的统计数字（success_n/failure_n/distinct_n）与 `learning_events` 按窗口重算一致；
3. 学习报告包含：本周期学习动作清单、kpn_citation_rate（MVP 链路未被破坏）、知识缺口清单（含 C 组两题、P4 的「命中但挖不出片段」类缺口若出现）。

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

### P12 Wiki 两层架构扩展（2026-07-30 新增，`docs/impl/v1/two-tier-task-brief.md`）

**前提**：依赖 P8 已跑通并至少一个领域 publish 成功（脚本从 `test/v1/results/v1_p8_wiki_*.jsonl` 读取最近一次结果里的 `page_id`/`concept_id`，不重新培养信号）。

**范围边界（对照 `docs/impl/v1/wiki.md` 硬约束，逐条不越界）**：页面关系只有 `related`/`contradicts`/`contains` 三种，不引入 broader/narrower；主题页命中后不产生 `answer_wiki` 调用；主题页只聚合概念页，不聚合主题页；`uncovered_points` 只作字段不进正文；`member_roles` 必须结构化落库；`wiki_pages.content` 只由编译产生，不存在任何 draft → page 写回接口；回流三条防护（来源标记/祖先关系跳过/统计排除）缺一不可。

同 P8/P11 的既有拆分方式，本阶段也分两条轴，**只有轴一计入通过/失败判定**：

**轴一（确定性，必过）：新增端点/字段的契约行为**

1. `POST /wiki/compile`、`POST /wiki/compile/analyze` 传 `page_type=topic` 必须被拒绝（非 200）——一阶编译端点收紧为仅接受 `concept`，主题页只能走二阶编译端点（`docs/impl/v1/wiki.md` 步骤 8）；
2. `GET /wiki/pages/:id/relations` 对 P8 已发布的两个页面各查一次，响应结构含 `relation_type`/`other_page_id`/`derived_from`/`evidence`（P8 制度/技术两页分属不同领域，天然不会有 KPN 关系，返回空列表是预期，不是失败——本步骤只验证接口契约，不验证是否非空）；
3. 写作草稿全生命周期：`POST /wiki/pages/:id/drafts`（mode=page）→ `GET /wiki/drafts/:id`（`evidence_index` 非空、`stale` 字段存在）→ `PATCH /wiki/drafts/:id` 改写标题与正文 → 重新 `GET /wiki/pages/:id` 核对页面正文与标题**完全不变**（代码中不存在 draft → page 写回路径的行为化验收）→ `DELETE /wiki/drafts/:id`；
4. 回流来源标记：`POST /sources` 附带 `origin=wiki_draft`、`origin_page_id=<P8 页面 id>`（multipart form 字段）导入一份最小文件，DB 直接核对 `sources.origin`/`sources.origin_page_id` 落库正确（自体祖先排除本身的匹配逻辑已由 Go 单测 `internal/unit/kpn_reflow_test.go::TestCrossSourceKPN_SkipsSelfAncestorEdges` 覆盖，本步骤只测 API 入口的字段透传）；
5. `POST /study/run` 后 `GET /study/reports/latest` 响应包含 `question_complexity` 板块（`{"groups": [...]}` 结构本身，不要求 `groups` 非空——阈值未定标前 `complexity_hint` 恒为 `null`，见 `docs/impl/v1/study.md` 步骤 7）。

**轴二（观测性，只记录，不判定）：页面关系与主题候选的自然形成情况**

6. 读 `knowledge_point_relations`/`wiki_page_relations` 总行数（P7 阶段的跨 Source fixture 可能已产生可供派生的关系）；
7. `GET /study/results?action=topic_page_candidate&status=pending_confirm` 数量——P8 每个领域目前只发布了 1 个概念页，远低于 `topic_member_min`（默认 3），单次运行大概率为 0，属预期观测性缺口；要真正观测主题页候选自然形成，需要在同一领域内再培养并 publish 至少两个额外的、与已有页面 KP 存在 `related` 关系的概念页，这不在本阶段自动化范围内（成本模型同 P8 的 `days_active` 轴二）。

**通过标准**：轴一 1-5 步全部成立（`page_type=topic` 被拒绝、relations 接口结构正确、草稿生命周期与写回防护、回流字段落库、报告新板块存在）即判 PASS；轴二 6-7 步仅记录数字，不影响判定。

## 6. 量化验收指标汇总

| 指标 | 目标 | 采集阶段 |
|------|------|---------|
| 事实类问答正确率（慢路径） | 制度域（A/B1-B2/D1-D4/G1-G24）与技术域（T/B3-B5/D5-D7/G25-G48）**各自** ≥90% | P1 |
| 快路径 LLM 调用次数 | ≤2 次/题（基线 ≥4），两域同标准 | P3 |
| 快路径耗时下降 | ≥40%（同题对比） | P3 |
| 快路径 direct 命中率 | ≥ 慢路径同题水平，两域分开统计 | P3 |
| 对象守门失效次数 | 0（F 组 9 次 + E3） | P3 |
| 片段子串核验 | 0 例外 | P1、P4 |
| 挖掘回退率（mined=false） | ≤30%，且回退可见（技术域代码块类 KU 单独统计回退率，作 V2 输入） | P1、P4 |
| 删除后 deprecated KP 引用次数 | 0（两域靶子各计） | P5 |
| 跨 Source 关系合理率 | 抽查 ≥80%，类型仅 related/contradicts | P7 |
| 状态迁移审计完整率 | 100%（迁移必有 result+reason+events） | P10 |
| 缺口题幻构率 | 0（C 组不得编造引用，技术域 C3/C4 重点盯） | P1 |
| 两层架构契约行为通过率 | 100%（page_type 拒绝、relations 结构、草稿生命周期与写回防护、回流字段落库、report 新板块，5 项全过） | P12 |
| Wiki 生成质量链路结构通过率 | 100%（正文五节+切面三级标题结构、`wiki_claim_checks` 落库匹配、aliases/trigger_questions 可溯源三项全过；质量门 passed/force、内聚度数值仅记录不判定） | P8 |

## 7. 执行注意事项

- **时序控制**：全程手动 `POST /study/run`，禁止依赖 Ticker，否则事件 processed 状态不可控；
- **变体问法是硬要求**：promote_distinct_min=2 要求不同 question_hash，同一字面重复问只累计 success_n 不累计 distinct_n，晋升永远不达标；
- **阶段间不清库**：P2-P12 依赖 P1 积累的事件；靶子文档已按域错开（P5 删报销规定+RAC 归档、P6 改培训积分+神通、P7 新增两份 contradicts fixture、P8 改应收账款+19c RAC、P11 复用 F1_PRE 且不得在 P11 前调整其四元组字段、P12 直接读 P8 落盘结果不重新培养信号），执行时勿调换；P12 必须排在 P8 之后。
- **真实 LLM 的波动**：正确率类指标按题判要点命中而非逐字比对（技术域例外：命令与参数名必须逐字对）；单题失败先重跑一次排除 LLM 抖动，复现两次才计为缺陷；
- **缺陷归因**：每个失败点先区分「提取期缺陷（KU/KP 就没有该事实）」与「检索/回答期缺陷」，前者不属于 V1 目标范围但需记录；
- **技术文档的特有风险**：代码块/长表格密集，KU 按行切片可能把命令截断（P0 抽查覆盖）；LLM 对通用技术知识有强先验，容易"不看文档也答对"或"用先验覆盖文档细节"——凡技术题必须核验引用片段确实来自对应文档，答对但引用为空/错源一律计缺陷；
- **不要用的事实**：《Oracle 11g RAC》文内网段表述自相矛盾（网络表私有网段 222.222.222.0/24，服务器表却是 172.16.1.x），勿以此设题，也不要当成系统缺陷。
