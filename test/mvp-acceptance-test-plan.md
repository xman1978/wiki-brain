# Wiki-Brain MVP 验收测试方案（真实数据）

用 `test/markdown/` 下 21 份真实文档验证 MVP 是否达成 `docs/impl/mvp/readme.md`「MVP 成功标准」的 8 项目标，即核心假设：知识可组织、检索可命中、回答可追溯、检索信号可积累。测试为黑盒验收：经 HTTP API 与 Page 控制台操作，经 API 响应、SQLite 表、Bleve 查询、日志四个观察面核对。

准确率测试题库（A/T/G 组共 80 题）内嵌于本方案第 4 节，可独立执行；B/C/D/E 组（跨文档、缺口、片段、追问共 19 题）复用 `test/v1-acceptance-test-plan.md` 第 4 节（F 组为 V1 激活守门专项，MVP 无 ActivationLink 匹配器，不适用）。两份方案的题库同源，修改任一处须同步。

## 1. 测试目标与成功标准映射

| # | MVP 成功标准 | 测试阶段 |
|---|-------------|---------|
| 1 | 知识导入：稳定导入 Markdown/PDF/Word，大纲与摘要正确 | M1 |
| 2 | 知识提取：KU/KP/KPN 按预期生成，Bleve 可查 | M2 |
| 3 | 检索：Rerank 后返回含 direct evidence 的 EvidenceSet | M3 |
| 4 | 回答：引用 fact_id 可追溯到具体 KU 和来源位置 | M4 |
| 5 | Trace：分级正确（confident/partial/gap），共现按问题去重累积 | M5 |
| 6 | Study：报告生成，signal_purity/activation_breadth 统计符合预期 | M6 |
| 7 | Page：走通"上传→问答→证据→报告"闭环 | M7（贯穿 M1-M6 操作） |
| 8 | 人工核查：ActivationLink 候选语义相关性可接受 | M8 |

## 2. 测试数据与环境

数据画像见 V1 方案第 2 节（21 篇 = 10 制度 + 11 技术，跨域验证同样适用于 MVP）。

**格式转换 fixture**：标准 1 要求 PDF/Word 导入。取《考勤管理管理制度》（最短）另存为 .docx 与 .pdf 各一份，测三种格式产出的规范化 Markdown 语义一致。FileView 服务不可用时此项记 blocked，不判失败。

`config.yml` 测试期建议值：

```yaml
study:
  schedule_interval:       "24h"  # 手动 POST /study/run 触发，保证时序可控
  candidate_confident_min: 3      # 默认 5；压低以缩短信号积累轮次
  candidate_ratio_min:     0.6    # 保持默认
  wiki_kp_min:             3      # 默认 4；配合真实数据规模压低
  wiki_confident_min:      3      # 默认 8；同上（阈值改动只影响触发难度，不影响统计公式正确性验证）
  gap_hit_threshold:       3
llm:
  max_retries: 1
```

## 3. MVP 专项补充题（M 组）

| ID | 题目 / 操作 | 验证目标 |
|----|------------|---------|
| M1 | 用 `deep=true` 重问 B1（回款延迟对收入的影响） | deep 路径：存在证据时强制走结构化推理，回答含变量识别、推理路径、证据缺口三要素（answer_deep prompt 产物） |
| M2 | 「结合考核和回款制度，推测一个项目从验收到奖金发完的完整时间线」 | 天然 deep 倾向题：direct 可能为空但 supporting 非空 → path=deep |
| M3 | C1（年假天数）观察响应 | none 路径：direct+supporting 均空 → 固定文本、**不调 LLM**（日志核对无 Answer 调用） |
| M4 | 停掉 LLM（改 base_url 指向黑洞）后问 A1 | error 路径：重试 1 次后返回降级文本，error 日志落盘，服务不崩 |
| M5 | 连问三遍字面完全相同的 A7（A800 价格） | 共现去重：`question_kp_cooccurrence.hit_count` 对该 (question_terms, point_id) 只 +1（`cooccurrence_question_dedup` 生效） |
| M6 | 问「招待费报销期限是多少？」和「报销期限，招待费的，是多少？」 | 词项相同、字面不同：question_hash 不同 → 各自计数（readme「什么是锁/锁是什么」规则） |
| M7 | 问 A9 后立即查 `GET /answers/:id` 的 evidence_snapshot | 快照完整性：citations 中每个 fact_id 都能在 snapshot 内找到对应 EvidenceItem 及 source_ref |

## 4. 问答准确率测试集（80 题，制度域 38 + 技术域 42）

用于 M3（direct 命中率）与 M4（回答正确率）。单轮提问，逐题记录 direct 是否命中、回答要点是否正确、citation 是否落在期望来源；制度域、技术域分开统计正确率，各自 ≥90% 达标。

### 4.1 A 组 · 制度域基础事实（14 题）

| ID | 问题 | 期望答案要点 | 期望证据来源 |
|----|------|-------------|-------------|
| A1 | 招待费用报销期限是多久？ | 费用实际发生之日起 45 天内；逾期财务不受理、费用个人承担 | 报销规定·第一条 |
| A2 | 差旅费报销期限是多久？ | 发票开具之日起 3 个月内（办公、市内交通、通讯、差旅同此） | 报销规定·第二条 |
| A3 | 发票能跨年报销吗？ | 原则上禁止跨年报销 | 报销规定·第四条 |
| A4 | 培训旷课一次扣几分？ | 每旷课 1 次 -5 分 | 培训积分·第五条积分规则表 |
| A5 | 培训积分能累计到明年吗？ | 不跨年累计，次年自动清零 | 培训积分·第六条 |
| A6 | 达不到年度积分基准线有什么后果？ | 年终绩效奖金按档内最低系数、次年无调薪资格、无职级晋升资格、无年度评优资格 | 培训积分·第七条 |
| A7 | A800 GPU 用一天多少钱？ | 256 元/天（L40 为 85 元/天，不足 1 天按 1 天算） | 平台办法·5.2 |
| A8 | GPT 服务怎么计费？ | 按 token 计费，0.15 元/千 token | 平台办法·5.2 |
| A9 | 客户逾期 90 天没付款怎么办？ | 发出第一封《催款函》并电话询问（180 天第二封、考虑停加密狗；270 天第三封、准备委托律师） | 应收账款·第十条（二）4 |
| A10 | 回款延期申请最多可以几次、每次多久？ | 两次机会；每次原则上 ≤3 个月，两次合计 ≤6 个月 | 应收账款·第九条 |
| A11 | 延迟九个月以上回款，销售提成怎么算？ | 提成比例 ×25%；强制转催款专员后专员回款成功按 ×75% | 应收账款·第十二条 |
| A12 | 项目考核优秀的标准和绩效系数？ | T≥100 为优秀，绩效系数 1.1 | 项目考核·4.1 表 |
| A13 | 战略项目的奖金系数是多少？ | 6%（A 类 5%，B/C/D 类 4%，E 类 3%，万相公文/无纸化会议 1%） | 项目考核·5.1 表 |
| A14 | 员工当年离职，项目奖金还发吗？ | 不予发放 | 项目考核·3.2 第 10 条 |

### 4.2 T 组 · 技术域基础事实（18 题）

| ID | 问题 | 期望答案要点 | 期望证据来源 |
|----|------|-------------|-------------|
| T1 | Docker Swarm 集群管理端口是哪个？ | 2377/TCP 集群管理；7946/TCP 节点通讯；4789/UDP overlay | Docker Swarm·环境准备 |
| T2 | Swarm 里 Manager 和 Worker 节点分别干什么？ | Manager 负责集群管理、调度和控制（仅一个 Leader）；Worker 运行容器并上报状态 | Docker Swarm·架构 |
| T3 | K8S 集群最少需要几台服务器？ | 高可用至少 3 台（1 Master 2 Node）；测试可单机部署、移除污点后跑应用 | K8S·部署要求 |
| T4 | K8S 1.24 之后默认容器引擎是什么？ | 默认集成 containerd，不再需要 dockershim，调用链更短更稳定 | K8S·容器引擎 |
| T5 | 用 crictl 前要先配置什么？ | `crictl config runtime-endpoint unix:///run/containerd/containerd.sock` | K8S·容器引擎 |
| T6 | MySQL 主从复制的原理？ | 主库写 binlog → 从库经同步账户读入 replaylog → 从库重放执行 | MySQL·第 1 节 |
| T7 | MySQL 从库怎么指向主库同步位？ | `change master to master_host=... master_log_file/master_log_pos`，取值与主库 `show master status` 一致 | MySQL·2.2 |
| T8 | Oracle RAC 怎么开启归档？ | srvctl stop → start -o mount → `alter database archivelog` → 重启实例 → `archive log list` 验证 | RAC 开启归档 |
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

### 4.3 G 组 · 准确率扩展题库（48 题）

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

### M1 知识导入（标准 1）

1. 经 Page 文件抽屉逐一上传 21 份 md + 2 份转换 fixture，轮询至全部 ready；
2. 验证（每篇抽查，长文档 K8S/达梦/AlwaysOn/应收账款必查）：
   - `source_outlines` 大纲与原文标题层级一致；纯命令短文档（RAC 开启归档）也能产出可用大纲或触发语义 outline 兜底；
   - 摘要非空且主题正确（LLM 失败时应看到一级标题关键词拼接的兜底摘要，不是空串）；
   - Domain 匹配合理：制度文档归人力/财务类 domain，技术文档归 IT 类 domain；`preset/domains.json` 覆盖不到的允许 `domain_id=null`（null 不影响后续检索，M3 一并验证）；
   - `data/sources/markdown/<id>.md` 规范化产物行号连续，表格未被破坏；
   - docx/pdf fixture 三格式产出语义一致。
3. 通过标准：21+2 全部 ready，无 failed；抽查项全过。

### M2 知识提取（标准 2）

1. Unit 提取随导入自动完成，逐 source 验证：
   - 每篇 KU>0、KP>0；KU 的 `line_start/line_end`（1-based inclusive）切片对得上规范化 Markdown 原文——技术文档重点抽命令块与参数表，制度文档重点抽条款；
   - 关键事实落点抽查（同 V1 方案 P0 清单：45 天/-5 分/256 元/6%/25% + 2377 端口/containerd/MAX_SESSION_STATEMENT 20000/MAX_CONNECTIONS 128/srvctl 五步/chmod u+s）；
   - Concept 匹配：抽 10 个 KU 核对 concept 归属合理（达梦优化 KU 不应挂到"报销"概念）；
   - KPN：`knowledge_point_relations` 关系类型**只有** related/contradicts 两种，direction 全部 bidirectional（出现第三种类型或 directed 即失败——这是 CLAUDE.md 明确的设计决策）；MVP 只做单 Source 内关系，不应出现跨 source 的 KP 对；
   - Bleve：units/points 索引用「归档」「报销期限」「MAX_CONNECTIONS」各查一次有命中，outlines 索引查「奖金计算办法」命中项目考核的节点。
2. 通过标准：全部成立；提取遗漏的关键事实记录清单（M3/M4 对应题目预期将失败，归因提取期）。

### M3 检索（标准 3）

1. 逐题执行第 4 节全部 80 题（A + T + G 组）主问法，观察 EvidenceSet（调试模式或 `POST /retrieval` 直查）：
   - direct evidence 命中率：含正确 direct KU 的题目占比制度域、技术域各 ≥85%；
   - Rerank 分类抽查 5 题：direct/supporting/irrelevant 的划分人工判定合理；
   - Domain 预过滤不串台：问 A7（GPU 计费）时日志中被过滤进入召回的 source 不应包含数据库/容器文档；`domain_id=null` 的 source（若 M1 有产生）始终在列；
   - Outline FTS fallback：挑一个 FTS 低分场景（如概念改述题 T4「现在还用 dockershim 吗」）验证评分 <0.5 时触发 LLM fallback 且并发受 max_concurrency 约束（日志观察）；
   - KPN 扩展：B1/B4 类跨条款题的 EvidenceSet 中出现经 KPN 邻居补充的 supporting 证据。
2. 通过标准：以上各点成立；每题记录 LLM 调用次数，验证正常问答 ≥4 次（Domain 过滤+Source 过滤+Rerank+Answer），此数据同时是 V1 快路径的对照基线。

### M4 回答（标准 4 + 四路径）

1. short 路径：第 4 节 80 题 + V1 方案 D 组，回答要点正确率制度域（38 题+）、技术域（42 题+）各 ≥90%（技术域命令与参数名逐字核对）；
2. 追溯性（D 组为主）：每条 citation 的 fact_id → evidence_snapshot → KU → `source_ref{source_id, line_start, line_end}` → 按行号截取原文，人工确认回答内容确实由该位置支撑。MVP 证据是 KU 整段（无 V1 片段挖掘），预期粒度为条款/段落级，D 组「反例检查」列在 MVP 不适用；
3. 四路径专项：M1（deep 强制）、M2（deep 自然触发）、M3（none 不调 LLM）、M4（error 降级）逐一执行；
4. Citation 校验：统计被过滤的幻构 fact_id 次数（日志）——出现过滤记录说明校验在工作，最终回答中的 fact_id 必须 100% 合法；
5. 通过标准：四路径行为与 answer.md 路径表完全一致；追溯链 0 断裂。

### M5 Trace（标准 5）

1. 分级验证：取 M4 已跑题目的 traces——答对且引用 direct 的题应为 confident；C 组应为 gap 且即时产生 knowledge_gap 事件；构造 partial：问一个只有弱相关的题（如 M2 类推理题），确认 direct 未被引用时记 partial；
2. 去重与计数：执行 M5、M6 专项题，核对 cooccurrence 表行为；
3. 共现正确性：任选 3 个 KP，人工按 traces 重算 hit_count/confident_count 与表值一致；
4. 通过标准：三种分级无错分抽样；去重规则与 readme 描述逐条吻合。

### M6 Study（标准 6）

1. 信号积累：对 A1、A9、A12、T8、T12、T15 各用 ≥3 种不同问法提问（凑 candidate_confident_min=3 与 ratio≥0.6，跨两域）；
2. `POST /study/run` → 验证：
   - `link_candidates` 出现上述 KP，未达阈值的 KP 不出现；
   - 报告四块齐全：质量分布 / ActivationLink 候选 / Wiki 候选 / 知识盲区；
   - **统计公式核对**（标准 6 的核心）：抽 3 条候选，人工 SQL 重算 signal_purity（confident/hit）、activation_breadth（distinct question_terms）、short_path_rate（confident traces 中含该 KP 且 path=short 占比）、has_kpn_neighbors，与报告值一致；strong/candidate 分级符合「purity≥0.7 且 breadth≥3 且 short_rate≥0.6」规则；
   - Wiki 候选：围绕「应收账款回款」或「Oracle RAC」概念密集问答后应出现候选，附 kpn_connection_count 与 ready/needs_more_data 结论；
   - 知识盲区：C1 重复问 3 次（达 gap_hit_threshold）后进入高频 gap 列表；
   - 报告生成不调 LLM（日志核对）；report_max_keep 生效。
3. 通过标准：全部成立，统计 0 偏差。

### M7 Page 端到端闭环（标准 7）

M1-M6 全程用 Page 操作即覆盖大部分；额外专项：

1. 历史抽屉按 answer_id 重载旧回答，trace_id 异步补查成功；
2. 解释抽屉三标签页（概览/来源/缺口）与 M4 追溯数据一致，点击来源能定位 KU；
3. 调试模式展示共现统计、学习事件、原始 JSON；
4. 空状态与错误透传：清库首启无伪造数据；制造一次 500（如 M4 的 LLM 黑洞期间）确认页面展示 status 与响应体原文；
5. deep / require_evidence 开关生效（M1 经开关触发）。

### M8 人工核查（标准 8）

1. 从 M6 报告抽全部 ActivationLink 候选（预期 6-10 条），逐条人工判定「该问题词项 → 该 KP」语义相关性，相关比例 ≥80%；
2. 特别检查跨域污染：技术问题词项的候选不得指向制度 KP，反之亦然（MVP 无对象守门，此处是纯共现质量的检验，结果供 V1 的 F 组对照）；
3. 通过标准：达标则 readme 的收尾判断成立——「核心设计具有工程可行性，可进入 V1」。

## 6. 量化验收指标汇总

| 指标 | 目标 | 阶段 |
|------|------|------|
| 导入成功率 | 23/23（21 md + 2 fixture） | M1 |
| direct 命中率 | 两域各 ≥85% | M3 |
| 回答要点正确率（short） | 两域各 ≥90% | M4 |
| 每次问答 LLM 调用 | ≥4 次（记录为 V1 基线） | M3 |
| fact_id 追溯链断裂 | 0 | M4 |
| 最终回答非法 fact_id | 0（校验过滤 100% 生效） | M4 |
| none 路径 LLM 调用 | 0 | M4 |
| Trace 分级错分（抽样） | 0 | M5 |
| 共现/报告统计与人工重算偏差 | 0 | M5、M6 |
| 候选语义相关率（人工） | ≥80%，跨域污染 0 | M8 |

## 7. 执行注意事项

- 与 V1 方案共用的约束同样生效：手动触发 study、变体问法要求、真实 LLM 波动处理、缺陷归因（提取期 vs 检索/回答期）、技术文档先验风险与 11g 文档内部矛盾不设题（见 V1 方案第 7 节）；
- **先 MVP 后 V1**：本方案在同一空库上先行执行，M3 的 LLM 调用计数与 M4 的正确率即 V1 方案 P1 所需基线，两方案连跑时 V1 的 P0/P1 可直接复用 M1-M4 结果；
- MVP 引用粒度是 KU 整段，不要用 V1 的片段子串标准来判 MVP 的 D 组——MVP 只要求 fact_id 能落到正确 KU 与行号区间；
- M4 的 error 路径测试放在当日问答类测试全部结束后做，避免污染其他题目的 trace 数据。
