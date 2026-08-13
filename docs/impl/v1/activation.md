# ActivationLink 实现路径（V1）

## 职责

ActivationLink 是「问题条件 → KnowledgePoint」的正式激活路径，V1 的核心新增对象。本模块提供：数据模型与存储、置信度与服务分档（2026-08-13 起替代原状态机，见下）、激活条件匹配器（供检索激活层调用）、人工确认 API。

**2026-08-13 起的机制基准**：本文档此前描述一套 candidate/verified/weakened/deprecated 四态状态机，由 Study 攒够阈值触发跳变。`docs/design/activation-convergence.md` 指出这套机制有结构性死锁——`weakened` 一旦进入就再也无法产生驱动"重新验证"所需的信号，合法的转移图纸画着，实际不可达（该文档第 1 节）——以及自证循环风险（第 2 节），已改为连续置信度机制：信任的单位从"一条链接"下沉到"链接下每一条具体的观测条件"，每条条件用 Beta 分布对自己的成功/失败证据打分，查询时按分数决定服务档位，不再有攒阈值触发的离散跳变。本文档以下内容按此机制重写；`status` 列保留，但含义改为从条件分数派生的缓存摘要，不再是真相源，见「状态机」一节。状态迁移不再是一个需要"合法性约束"去把关的动作——没有跳变，也就没有非法跳变需要拒绝；本模块与 Study 的分工相应调整为：本模块负责置信度的计算与更新入口，Study 的职责收窄为报告收敛趋势与执行剪枝，见 `study.md`。

> **熟路指针（2026-08-11，设计层面，非实现变更）**：`docs/impl/v1/activation-bundle.md`
> 提出的 ActivationBundle（熟路）是本模块之上的组合激活层——「一组知识点合在一起
> 对同一类问题管不管用」，不是本模块的「单个知识点」职责的替代或扩展。该文档
> 建议存储与匹配器代码直接放在本包（`internal/activation`）内新增文件，复用本
> 模块的 `observed_conditions` 结构、Match 语义、置信度计算逻辑（2026-08-13
> 更新措辞：原"状态机迁移表"已随本文档「状态机」一节的连续置信度改写不再
> 存在，见下；本条指针的落点相应改为置信度公式与分档逻辑，指针本身指向的
> 决策——封装位置是否认可——不受这次措辞更新影响），而不是新建顶层模块；
> 是否认可这个封装位置尚未定案，见 `activation-bundle.md`「依赖」一节。
> 本文档尚未据此改动。

## 数据结构

```sql
CREATE TABLE activation_links (
    link_id           TEXT PRIMARY KEY,
    question_terms    TEXT NOT NULL,
    -- 创建/最近一次刷新时使用的代表性问法（归一化问题关键词，排序后空格拼接，
    -- 生成规则与 traces.question_terms 完全一致）；仅用于展示与回退匹配
    -- （见步骤 2），不再是去重键（去重键是 point_id，见下方 UNIQUE 约束）
    subject_terms     TEXT NOT NULL DEFAULT '',
    -- 兼容投影：最新一组 observed_conditions 的 Terms(subject)；不再参与 Match
    intent_terms      TEXT NOT NULL DEFAULT '[]',
    -- 兼容投影：最新一组的单元素数组；不再作并集白名单
    audience          TEXT NOT NULL DEFAULT '[]',
    constraint_terms  TEXT NOT NULL DEFAULT '[]',
    observed_conditions TEXT NOT NULL DEFAULT '[]',
    -- Match 唯一真相源：JSON 数组，元素为观测条件（ObservedCondition）：
    -- {subject,intent,audience,constraint,question_terms,
    --  first_seen_at,last_seen_at,
    --  success_count,failure_count,
    --  audited_success_count,audited_failure_count,
    --  known_question_terms}
    -- 组内四门全过才算该组命中，组间 OR（见步骤 2）。
    -- success_count/failure_count（2026-08-13 替代 hit_count，见
    -- docs/design/activation-convergence.md）是这条条件收到的全部证据
    -- （自证 + 独立核实）；audited_success_count/audited_failure_count 是
    -- 其中专门来自独立核实试验的子集（见「状态机」置信度公式），只用来
    -- 判定是否够格进最高档「trusted」，不单独算一个展示给用户的分数。
    -- 不再有单独的 confidence/mean 列——置信度按需在读取时用公式现算，
    -- 计算结果的新鲜度靠 Matcher 现有的 loadCache() 内存缓存机制保证，
    -- 不落库（见「状态机」「置信度计算与缓存」）。
    -- known_question_terms（原 migration 047 加在本表的独立列；本节此前
    -- 未同步补上该列，是遗留的文档缺口，这次一并补齐）2026-08-13 起从
    -- 表级列下沉为每条条件自己的字段：归属这条条件的字面问题命中集，
    -- 用于步骤 2 的字面问题捷径，见「字面问题捷径与置信度档位」。
    scene             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL DEFAULT '',
    -- V1 不写入，预留 V2 认知化字段（触发条件 / 认知条件，见 docs/impl/v2/readme.md）
    point_id          TEXT NOT NULL REFERENCES knowledge_points(point_id),
    status            TEXT NOT NULL DEFAULT 'candidate',
    -- candidate / verified / deprecated（2026-08-13 修订，见
    -- docs/design/activation-convergence.md：weakened 整体退休，不再是
    -- 本列的可能取值，理由见「状态机」；conflicted 预留枚举维持不变，
    -- V1 不产生）。本列现在是从条件分数派生、写回落库的缓存字段，
    -- 不是真相源，见「状态机」。
    adopt_count       INTEGER NOT NULL DEFAULT 0,
    -- 累计 activation_success 次数（跨全部条件的展示用汇总值，由
    -- RecordOutcome/RecordAuditOutcome 顺带维护，见步骤 1；不驱动任何
    -- 判定——判定看每条条件自己的 success_count/failure_count）
    fail_count        INTEGER NOT NULL DEFAULT 0,
    -- 累计 activation_failure 次数（同上，展示用汇总值）
    last_used_at      DATETIME,
    -- 最近一次被激活层命中的时间（Retrieval 异步更新，不阻塞请求）
    created_from      TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：创建该链接所依据的 learning_event event_id 列表
    -- （与 learning_results.event_ids 同源；不再写入 link_candidate id）
    status_changed_at DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_al_status ON activation_links(status);
CREATE UNIQUE INDEX idx_al_point_id ON activation_links(point_id);
-- 每个 point 至多一条链接（同一 KP 被不同问法反复命中时刷新已有链接的条件，
-- 不创建新链接，见 study.md 步骤 2 tryCreateLink）
```

与 MVP `link_candidates` 表的关系：link_candidates 保留，仍作为 Study 共现扫描的暂存快照；Study 从达标的 link_candidates 创建或刷新 activation_links（新建时 status=candidate），创建后该 candidate 行保留（用于报告展示），不迁移不删除。activation_links 才是参与系统行为的正式对象。

### Migration 与回填（2026-08-13 新增，随连续置信度设计一并落地）

```sql
-- observed_conditions 内每个元素补三个字段，hit_count 更名 success_count；
-- known_question_terms 从表级列下沉为每条条件自己的字段。JSON 内字段
-- 迁移，不是新增表级列，用一次性数据迁移（读出全部行、按下面规则重写
-- observed_conditions、写回），不是纯 ALTER TABLE：
--   success_count          = 旧 hit_count（原样承接，不清零重来）；
--   failure_count           = 0；
--   audited_success_count  = 0；
--   audited_failure_count  = 0；
--   known_question_terms   = 迁移前 activation_links.known_question_terms
--                             （表级列）的值，原样复制进该链接名下的
--                             每一条条件；

ALTER TABLE activation_links DROP COLUMN known_question_terms;
-- 内容已下沉进 observed_conditions，表级列不再有语义，一并清理，避免
-- 留一个不再被任何代码路径读取的死列。
```

**已知的数据缺口（迁移时明确接受，不追溯修复，同「迁移时已知代价」这一贯做法）**：

```text
failure_count 从 0 开始：迁移前系统从未按"条件"这个粒度记录过失败，只有
  链接整体的 fail_count 展示计数。迁移后，存量条件在最初一段时间里
  mean 会显得比它实际应有的更乐观（等价于假设"这条条件此前从未失败
  过"）——这是本次设计明确接受的乐观先验，不打算靠回溯挖掘历史 trace
  补算 failure_count：那批 trace 里"这次回答算不算这条具体条件的失败"
  从未被结构化记录过，事后重建的准确性没有保障，不值得为一次性迁移
  新增一条挖掘链路；

known_question_terms 整体下沉、不做精确归属：理由同上——迁移前的表级列
  从未记录"这个字面问题当初命中的是哪一条 observed_conditions"，只能
  把整条链接的已知问题集合复制给它名下的每条条件。这意味着迁移后短期
  内，字面问题捷径可能会把一次命中错误关联到该链接实际上不相关的某条
  条件上（见「字面问题捷径与置信度档位」）——影响范围局限于"参考哪条
  置信度"这一步，不影响是否命中本身，后果轻微，且会随新证据（迁移后
  新的归属都按精确条件写入）自然收敛，不需要专门补救。
```

### 附属表：subject_synonyms（2026-07-24 新增）

```sql
CREATE TABLE subject_synonyms (
    synonym_id   TEXT PRIMARY KEY,
    domain_id    TEXT REFERENCES domains(domain_id),
    term         TEXT NOT NULL,       -- 归一化后的原始措辞（短语级，不分词）
    canonical    TEXT NOT NULL,       -- 归一化后收敛到的规范措辞
    source       TEXT NOT NULL DEFAULT 'manual',  -- preset / gap_mined / manual
    status       TEXT NOT NULL DEFAULT 'active',  -- active / candidate / rejected
    created_from TEXT NOT NULL DEFAULT '[]',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_subject_synonyms_term_active ON subject_synonyms(term) WHERE status = 'active';
CREATE INDEX idx_subject_synonyms_status ON subject_synonyms(status);
```

只归一化 subject 一个维度，intent/audience/constraint 的精确匹配语义不变。`source=preset` 的行来自 `preset/domains.json` 每个 concept 的 `aliases` 字段（启动时随 domains/entries 一并 UPSERT，`status=active` 无需确认）；`source=gap_mined` 的行来自 Study 对 `subject_synonym_gap` 学习事件的聚合，**2026-08-12 起默认 `status=active` 直接生效，不经人工确认**（`study.synonym_auto_promote` 默认改为 `true`，理由见 study.md 步骤 2a；`false` 时保留原有 `status=candidate` + 人工 confirm 流程，作灰度回退）。这条挖掘链路本身不冻结——它把模型辅助匹配已经判断过的等价关系沉淀成表，让同一措辞下次能在 Match 第一轮免费命中，价值独立于模型辅助匹配是否存在，见 study.md 步骤 2a。设计动机、挖掘触发条件、Match 算法调整详见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`；`domain_id` 列 V1 仅作展示，Match 不按 domain 过滤。

## 状态机

**2026-08-13 改判，取代此前离散四态状态机（candidate/verified/weakened/deprecated 攒阈值触发跳变）**：详见 `docs/design/activation-convergence.md`。信任的单位从链接整体下沉到每一条 `ObservedCondition`；`status` 列不再由一张"合法迁移表"驱动，改为从条件分数直接算出来的派生/缓存字段。这不是在旧状态机上再叠一层置信度，是替换——不再存在 `TransitionLink` 这个通用的、带合法性校验的迁移入口，见下方「与旧状态机的映射」。

### 置信度：每条观测条件自己的 Beta 后验

```text
mean(cond) = (cond.success_count + 1) / (cond.success_count + cond.failure_count + 2)
```

标准的 Laplace 平滑速率（等价于 Beta(success+1, failure+1) 后验的均值）：新条件（0 成功 0 失败）从 mean=0.5 开始——不预设"新条件默认可信"或"默认不可信"，第一条证据一到就立即向对应方向移动。`audited_mean(cond)` 用同一公式，只是把 `success_count`/`failure_count` 换成 `audited_success_count`/`audited_failure_count`；只用于下面的档位判定，不单独作为一个展示给用户的独立分数。

### 服务分档（替代原「候选池」筛选与 Match 的 verified/candidate 二元判断）

```text
mean(cond) = (success_count+1) / (success_count+failure_count+2)

若 mean(cond) < retrieval.serving_confidence_min（配置，见 retrieval.md）：
    tier = exploring —— 不直接服务；本轮仍有 explore_rate_low（配置）的
    概率被选中当作一次真实试探（绝不彻底出局——这正是堵住原「weakened
    一旦进入就再也攒不到新证据」死锁的地方，见 activation-convergence.md
    第 1 节）；

否则若 (audited_success_count+audited_failure_count) ≥ audit_sample_min（配置）
     且 audited_mean(cond) ≥ serving_confidence_min：
    tier = trusted —— 直接服务；额外有 explore_rate_trusted（配置，较低，
    定期抽查防漂移）的概率被同时安排一次独立核实试验；

否则：
    tier = self_graded —— 直接服务；额外有 explore_rate_self_graded
    （配置，高于 explore_rate_trusted）的概率被同时安排一次独立核实试验。
```

三档的判定与"本轮是否试探/核实"的抽样，均由 `Matcher.Match()` 在同一次调用内完成（见步骤 2）；本模块不再对外暴露"候选池"这个中间概念——Match 的输入是"这条链接下全部非 `deprecated` 的条件"，输出已经是每条命中条件的档位与本轮是否服务的结果。

**刻意选择固定分档探索率，不做完整 Thompson 采样**：三个 `explore_rate_*` 是配置常量，不是按后验分布动态计算的采样概率。多臂老虎机问题有更"统计最优"的解法（Thompson 采样、UCB 等），但那类方法需要在每次决策时对完整后验做采样计算，多一层不透明的统计机制。本系统一贯的取向是简单、可解释优先于统计最优（同 `study.md` 一贯的谨慎风格——宁可阈值不够精细，也不引入难以对着一条具体决策讲清楚"为什么是这个概率"的机制）。这是一个明确的工程取舍，不是权宜之计：三个固定概率配置项，任何人都能读出"探索档 N% 概率试一次、自证档 M% 概率抽查、信任档 K% 概率抽查"，不需要理解贝叶斯采样就能核对、调整这套行为。

### 与旧状态机的映射（status 列现在如何取值）

```text
verified   ⟺ 该链接下存在至少一条 tier ∈ {self_graded, trusted} 的条件
              （"够格直接服务"，不区分自证/已核实——那是"该不该再抽查"
              的问题，不是"够不够格服务"的问题）；
candidate  ⟺ 不满足 verified，且 KP 仍 lifecycle=current
              （含：全部条件都在 exploring 档；以及 observed_conditions
              为空——新建但还没收集到任何证据的链接，或全部条件被
              study.md 收敛剪枝清空后的链接，两种情况都没有可判断的
              条件，candidate 是没有更具体信息时的默认落点，不是一个
              新增的特殊状态）；
deprecated ⟺ 目标 KP.lifecycle != current
              （与置信度系统无关，含义不变，见「数据结构」status 列
              注释；这是本次改判唯一保留的"由外部事实驱动的状态"，因为
              它回答的是"这个知识点还在不在"，不是"这条使用路径可信
              不可信"）。

weakened 整体退休，不再是 status 的可能取值（本文档在此明确写出推理，
  design 文档未逐字提及这一具体后果）：旧状态机里 weakened 表示"曾经
  verified、后来被 repeated_failure 拉低"，是 verified 和 candidate
  之间专门用来标记"这条路径正在变差"的一个中间态。连续置信度下，这个
  信息已经被 mean(cond) 本身连续地表达——一条条件从高分跌到低于
  serving_confidence_min，它的派生 status 就从 verified 变回
  candidate，不需要一个额外的中间标签去标记"它曾经更好过"；这正是
  activation-convergence.md 第 1 节要拆除的那类"离散状态机自己制造
  死区"的同一个病灶——多一个中间态，就多一处需要单独定义"怎么进、
  怎么出"的地方。status 列因此只需三个取值即可完整表达当前系统状态，
  比旧状态机少一个，不是遗漏。
```

**候选加载不再按 status 过滤**：`ListMatchableLinksForCurrentKP()`（见依赖）现有实现——按 `JOIN knowledge_points` 过滤 `lifecycle=current`——不需要改动、也不应该再叠加 `status IN (...)` 条件：status 本身是这次要计算的派生结果，拿它当过滤条件会变成"用结论过滤输入"的循环判断。deprecated 在新模型下等价于"KP lifecycle 非 current"，现有的 lifecycle JOIN 已经精确达成这个过滤效果，不需要另外读 status 列。

### 置信度计算与缓存

mean/tier 不是持久化列，是纯函数 `conditionMean(successCount, failureCount)` 的计算结果——公式见上。计算成本是几次算术运算，不值得单独再包一层缓存；它复用的是 `Matcher` 已有的 `loadCache()` 内存缓存机制带来的数据新鲜度保证，不是自己另建一套失效逻辑（见 `internal/activation/matcher.go` 现有的 `loadCache`/`InvalidateCache` 实现）：`loadCache()` 把 `activation_links` 整表（含每条 `observed_conditions` 的 `success_count`/`failure_count`）读进内存缓存，`valid` 标志位为真期间，同一批数据反复被多次 `Match()` 调用读取、反复算出同样的 mean/tier，直到某次写操作调 `InvalidateCache()`（沿用现有约定：`CreateLink` / `AppendObservedCondition` / lifecycle 通知 / synonym confirm-reject，加上本次新增的 `RecordOutcome` / `RecordAuditOutcome`，均需在写入后调用）使 `valid=false`，下一次 `Match()` 触发的 `loadCache()` 才重新查库、重新算出新的 mean/tier。

`status` 列不同：它是"派生但落库"的缓存（供 `GET /activation-links?status=verified` 这类只读列表查询不用反查每条条件就能筛选展示）——`RecordOutcome` / `RecordAuditOutcome` 写入新的 `success_count`/`failure_count` 后，在同一次 Store 调用里按「与旧状态机的映射」的规则重算并写回该链接的派生 `status`，而不是等下次 `Match()` 才间接得出；这保证管理界面按 status 筛选时不会长期落后于真实数据。deprecated 值的写回不经过这条路径（它不依赖 success/failure 计算），由 lifecycle 变更通知直接写入，见步骤 1。

> **verified 的含义（2026-08-12 修订，取代 2026-08-11「verified 的含义收窄」）**：2026-08-11 曾判断晋升改为默认自动后，`status=verified` 只表示"够格走 Retrieval 快路径"，另需 `study.md` 步骤 6 新增的 Wiki 材料确认（`learning_results(action=wiki_material_confirm)`）单独把关才算"够格作为 Wiki 材料"。2026-08-12 改判：该人工确认关卡整体废弃（见 `docs/design/wiki.md`「2026-08-12 改判」）——脱离具体 Wiki 主题语境，人工看着一条孤立的 KP 判断"值不值得沉淀"并不比程序多掌握信息，真正能做这个判断的时机是 Wiki 编译时（主题范围已定，编译时的整体判断——广度/连贯/稳定——自然回答了这个问题）。`status=verified` 的含义因此重新收拢为同时表示"这条路径够格走 Retrieval 快路径"**和**"这个 KP 够格作为 Wiki 一阶编译材料"，两者不再分开判断，见 `wiki.md` 步骤 3 qualifying 定义（现在只要求 verified，无第二道关卡）。
>
> **2026-08-13 编注（随 `docs/design/activation-convergence.md` 的连续置信度设计一并同步）**：以上一段沿用了当时的表述习惯，把 `candidate → verified` 称为一次"晋升"（触发式状态跳变）。这个动作现在已经不存在——`verified` 是"该链接下至少一条条件的置信度越过服务门槛"这一持续判断的实时派生结果，不再有一次单独的、需要 `auto_promote` 开关决定"自动还是等人工确认"的跳变时刻（`study.auto_promote` 配置项随之作废，见 `study.md`）。上文"verified 现在收拢为同时表示够格走快路径、也够格作为 Wiki 材料"这一结论不受影响——它讨论的是 verified 这个标签在下游（Wiki qualifying）该被如何解读，与背后是离散跳变还是连续分数无关；`wiki.md` 的 qualifying 判据因此照旧引用"verified"，只是这个标签现在的产生方式变了，见本文档「置信度：每条观测条件自己的 Beta 后验」与「服务分档」。

## 配置项（config.yml: retrieval 节扩展，与 activation_match_top 同节，理由见下）

```yaml
retrieval:
  serving_confidence_min:   0.7    # 服务门槛：mean(cond) 达到此值才算
                                    # self_graded/trusted，够格直接服务
  audit_sample_min:         5      # 够格评估 trusted 档所需的独立核实
                                    # 样本量下限（audited_success_count+
                                    # audited_failure_count）
  explore_rate_low:         0.15   # exploring 档：本轮仍被选中试探的概率
  explore_rate_self_graded: 0.10   # self_graded 档：本轮被抽样安排一次
                                    # 独立核实的概率
  explore_rate_trusted:     0.03   # trusted 档：本轮被抽样安排一次独立
                                    # 核实（定期复查防漂移）的概率
```

以上 5 项建议默认值未经真实数据校准，同本文档其余新增阈值的一贯做法（先给出合理起点，等 `study.md` 新增的收敛趋势报告积累出真实分布后再回填）。完整 yaml 块与既有 `retrieval:` 节其余键（含 `activation_match_top`）一并维护在 `retrieval.md`——这 5 项本质上和 `activation_match_top` 是同一类参数：都在配置 `Matcher.Match()` 的实时服务行为，只是恰好实现在 `internal/activation` 包内，历史上就没有为这类参数单独开一个 `activation:` 顶层配置命名空间，这次延续同一个惯例，不新增命名空间。本节只解释这 5 项在 Match() 算法里各自的语义（公式与分档定义见「状态机」），完整 yaml 块以 `retrieval.md` 为准。

**移除的旧配置项（2026-08-13，随离散状态机一起废弃，映射关系见下，供读者对照 diff）**：

```text
旧键（原属 study: 节）                替代方式
────────────────────────────────────  ──────────────────────────────────
promote_success_min / promote_distinct_min
                                       不再需要——没有"晋升"这个离散判定，
                                       mean(cond) 越过 serving_confidence_min
                                       即直接体现为 verified（见「状态机」）
weaken_failure_min / weaken_ratio_min
                                       不再需要——没有"降权"这个离散判定，
                                       持续失败会让 mean(cond) 自然下滑，
                                       跌破 serving_confidence_min 即回落
                                       candidate（不再经过 weakened 中间态）
reverify_success_min                  不再需要——没有"重新验证"这个离散
                                       判定，mean(cond) 回升越过门槛即
                                       直接重新体现为 verified
auto_promote                          不再需要——没有晋升动作，也就没有
                                       "自动晋升 vs 人工确认晋升"这个开关
```

这 4 组键从 `study.md` 的 `study:` 节移除，映射为上面 5 个新键（`retrieval:` 节）+「状态机」一节描述的连续计算逻辑，不是简单的一对一改名——旧键量的是"离散事件计数是否达标"，新键量的是"连续分数落在哪个区间、要不要抽样"，是两套不同的判断方式，见 `docs/design/activation-convergence.md`。字面 yaml 层面的移除标注（保留在原位置的注释，供 diff 工具比对）见 `study.md` 配置项一节。

## 实现步骤

### 步骤 1：存储与内部接口

```text
CreateLink(questionTerms, cond LinkCondition, pointID, createdFrom) → link
  status=candidate；LinkCondition = { subject_terms, intent_terms,
  audience, constraint_terms }，由 Study 的 computeLinkCondition 从该 point
  全部确证信号归纳生成（见 study.md 步骤 2）；(question_terms, point_id)
  UNIQUE 已改为 point_id UNIQUE，冲突时返回已存在链接（幂等），若已存在链接
  为 deprecated 则拒绝创建（同 point 被淘汰过，需新累积信号显式复活：
  deprecated 链接保持不动，拒绝原因写入日志，防止候选被反复自动重建）；
UpdateConditions(linkID, cond)：Study 刷新已有链接的条件（同一归纳算子，
  见 study.md 步骤 2），不写 learning_results；写入后按「与旧状态机的
  映射」重算并持久化派生 status，并调用 InvalidateCache；

RecordOutcome(linkID, subject, intent, audience, constraint string,
              success bool, questionTerms string, eventID string) error
  —— 2026-08-13 新增，取代原 UpdateStats 的职责。定位 linkID 下
  conditionKey(subject,intent,audience,constraint) 相等的那一条
  observed_conditions；success=true → 该条件 success_count++，
  false → failure_count++；同时按既有语义维护链接级 adopt_count/
  fail_count 展示汇总（等价于原 UpdateStats 的效果，UpdateStats 作为
  独立方法名不再保留，职责并入本方法）；questionTerms 非空时一并合入
  该条件的 known_question_terms（同现有 maxKnownQuestionTerms=200 上限
  与去重逻辑，见 store.go；已存在则是幂等的重复登记，不报错）——这是
  字面问题捷径的登记入口，成功或失败都登记（捷径解决的是"路由到哪条
  条件"，不是"这条条件可不可信"，可信与否已经由 mean/tier 单独回答，
  见「字面问题捷径与置信度档位」）；写入后按「与旧状态机的映射」重算
  并持久化该链接的派生 status；调用 InvalidateCache；定位不到匹配条件
  时（理论上不应发生——本方法只应在 Match() 刚返回过命中之后被调用）
  记录 warn 日志，不写入、不向调用方报错——宁可丢一次学习信号，也不让
  一次统计更新失败拖垮整条回答链路的收尾；由 Trace 在处理
  activation_success/failure 事件与 user_correction 反馈时直接调用
  （不再由 Study 批量扫描调用，见下方「与 Study 的分工变化」）；

RecordAuditOutcome(linkID, subject, intent, audience, constraint string,
                   agree bool, eventID string) error
  —— 2026-08-13 新增，命名与 RecordOutcome 对称。定位同上；agree=true
  （快慢路径独立核实结论一致）→ 该条件 success_count++ 且
  audited_success_count++；agree=false（不一致，以慢路径结论为准）→
  failure_count++ 且 audited_failure_count++（audited_* 永远是
  success_count/failure_count 的子集，见「数据结构」，两对计数同一次
  调用内一起更新，不会出现 audited_success_count 高于 success_count
  的情况）；写入后同样重算并持久化派生 status、调用 InvalidateCache；
  定位不到匹配条件时的处理同 RecordOutcome；由 Trace 处理
  activation_audit_success/failure 事件时调用，见 trace.md；

TouchLastUsed(linkIDs)：Retrieval 命中后异步更新 last_used_at（不变）。
```

**与 Study 的分工变化（2026-08-13）**：原 `TransitionLink(linkID, to, reason, eventIDs)` 已移除——它的角色是"校验一次离散跳变合法、执行、写 learning_results"，新模型下 candidate/verified 不再有跳变这个动作（见「状态机」），没有对象可供这个方法操作。deprecated 的写入不经过任何通用迁移入口：目标 KP 的 lifecycle 变化直接触发对该 point_id 对应链接行的一次 `status='deprecated'` 直接写入（同 `wiki.md` 现有 `MarkNeedsRecompile` 那种"外部事实变化 → 直接同步一次缓存字段"的简单模式，不需要合法性校验——它不是在多个"可信"状态之间选择，只是把一个客观事实同步进来），由 lifecycle 模块调用（见依赖）。

原 `UpdateStats(linkID, adoptDelta, failDelta)` 由 Study 周期批量调用的模式同样移除：Beta 后验的增量更新不需要攒一个窗口再统一处理，`RecordOutcome`/`RecordAuditOutcome` 在 Trace 产生对应学习事件的同一步直接调用（`trace_write` 异步任务内，不阻塞回答；`POST /traces/:id/feedback` 处理 `user_correction` 时同样直接调用，携带 `study.correction_weight` 作为一次性 failure 权重——见 trace.md 步骤 3/4）。Study 周期扫描不再读 `activation_success`/`activation_failure`/`user_correction` 事件做计数或跳变判定；它的新职责（收敛趋势报告、收敛剪枝）改为直接读 `activation_links` 当前状态做 SQL 聚合，不需要重放事件，见 `study.md`。

### 步骤 2：激活条件匹配器

供检索激活层调用。**2026-08-12 改判，取代 2026-08-11「改为两级、可调用模型」的口径**：Match 恢复为单级、纯程序计算，不调用 LLM。`subject`/`intent`/`audience`/`constraint` 四个字段同一层级精确匹配，不再有字段享受模糊匹配或模型辅助的特殊待遇。改判理由：抽取抖动不是 subject 独有的问题，intent/audience/constraint 同样会抖，原先"subject 模糊、其余三项硬 gate"的不对称设计一旦承认这点就站不住；与其把模型辅助匹配扩展到更多字段（调用量滚雪球），不如把 Match 整体收回纯精确匹配，抽取抖动交给别的手段处理（例如收紧抽取阶段的受控词表，另一项尚未排期的独立调查）。

**输入基准**：匹配输入是 Session 产出的 `ExpandedQuery`（standalone 补全后的 expanded_question + 主题/意图/对象/约束四元组，见 mvp session.md），不是用户原始输入。省略式追问（"漠河呢"）的原始输入不含完整词项，必须用补全后的问题匹配。链接创建侧的 traces.question 记录的同样是 expanded_question（Page 传给 POST /answer 的问题），两侧文本基准一致。

**匹配语义：观测条件组（组内精确、组间 OR）**。激活链接是"精确命中的缓存"：每组是历史上一起出现过的 `(subject,intent,audience,constraint)`；禁止跨问法并集交叉拼接。详见 `docs/superpowers/specs/2026-07-22-activation-observed-conditions-design.md`。

```text
Match(query ExpandedQuery, cfg MatchConfig) →
  []LinkMatch{link, score, matchedBy, tier, mean, auditSampled}

预处理：
  querySubject / Qi / Qa / Qc / Qq = 与组内字段同一归一化规则
  （2026-08-12 改判：querySubject 不再经同义词归一化，见下方
  「Subject 同义词归一化」小节；BuildQueryConditionTerms 现在
  只做 text.Normalize，不接收 resolver 参数）

候选加载：全部 KP lifecycle=current 的链接（2026-08-13 改判：不再按
  status 过滤，理由见「状态机」「候选加载不再按 status 过滤」——status
  本身是本次要算出来的派生结果，拿它做过滤条件是循环判断）

字面问题捷径（先于硬性过滤执行，命中判断本身不变，新增档位判断，
  见「字面问题捷径与置信度档位」）：
  Qq 命中某条件的 known_question_terms → 该条件即"归属条件"，跳过
  硬性过滤与精确匹配，直接进入下面「置信度分档判定」；

硬性过滤（程序，逐组执行；未经字面问题捷径命中的条件才进入这一步）：
  observed_conditions 非空的组中，Qa==cond.audience 且 Qc==cond.constraint
    的组才进入候选组；不满足的组直接排除；

精确匹配（唯一一轮，纯程序、免费，2026-08-12 改判取代此前的
两轮结构）：
  候选组中任一组 cond.Subject == querySubject 且 cond.Intent == Qi
    （四个字段同一层级精确字符串相等，subject 不再单独享受
    子串/overlap 判断）→ 命中该条件，进入下面「置信度分档判定」；

置信度分档判定（2026-08-13 新增；命中条件——无论经由字面问题捷径还是
  精确匹配——都统一在这一步决定本轮是否服务，公式见「状态机」）：
  mean = (success_count+1)/(success_count+failure_count+2)；
  mean < serving_confidence_min → tier=exploring；掷一次
    Bernoulli(explore_rate_low)：命中 → 本轮服务，score=1.0；
    未命中 → 本轮不产出该条件的 LinkMatch（等价于未命中，不进入
    下面的排序与截断）；
  否则 audited_success_count+audited_failure_count ≥ audit_sample_min
    且 audited_mean ≥ serving_confidence_min → tier=trusted；直接
    服务，score=1.0；另掷 Bernoulli(explore_rate_trusted) 决定
    auditSampled 标志（是否安排一次独立核实，由 Retrieval 消费，
    见 retrieval.md 步骤 2）；
  否则 → tier=self_graded；直接服务，score=1.0；另掷
    Bernoulli(explore_rate_self_graded) 决定 auditSampled 标志；

回退（observed_conditions 为空）：仅当从未观测非空 audience/constraint
  时，Qq == question_terms 才命中（逐字相等的字面去重）——这是早于
  置信度设计已经存在的兼容路径，服务尚未积累出任何 observed_conditions
  的遗留数据；命中后直接判定 score=1.0，不经过
  置信度分档（没有可归属的具体条件可供算 mean，不为这一条兼容路径
  另造一个虚拟条件）。study.md「收敛剪枝」清空一条链接的全部条件后，
  必须同步调用既有的 ProjectLegacyFields 刷新 question_terms 等展示
  字段（同 UpdateConditions 今天已经在做的事）——这样一条被剪枝判定为
  收敛低分的链接，不会因为一个没跟着清空的旧展示字段，反而继续靠这条
  字面回退命中；

排序：按 score 与 last_used_at，截断 activation_match_top
```

matchable 链接数量（不再局限于 verified）在 V1 规模下（预计 <10^4）全量内存匹配足够；不建 Bleve 索引。Match 全程不发起任何外部调用，纯同步内存计算——分档判定与探索/审计抽样都是内存里的算术与随机数生成，不额外增加任何 I/O 或调用。

### 字面问题捷径与置信度档位（2026-08-13，展开上面「置信度分档判定」引用的推理）

migration 047 引入的字面问题捷径（`link.KnownQuestionTerms`，见 `internal/activation/matcher.go` 现有实现）原先是一条完全独立于四元组匹配、独立于状态机之外的近路：只要归一化后的问题原文在这个集合里，不看四元组、不看链接状态，直接判定命中、score=1.0。这在离散状态机下没有代价——命中即直接用，最多是把一条本该走慢路径的问题错误地送进快路径，由 fast_path_verify 兜底。

**新机制下这条近路不能继续原样保留为"永远直接命中、绕开置信度"**：如果字面问题命中之后仍然绕开分档判断，系统里就会并存两条互不兼容的信任来源——一条是持续被证据校准的连续分数，一条是"曾经命中过一次就永远直接放行、不再纳入置信度体系"的特权通道。后者会让"每一次学习最终都汇入同一套信任机制"这个设计目标出现漏洞：一条条件哪怕置信度已经跌到很低（大量后续失败），只要曾经被同一句字面问题命中过，仍然会被无条件放行。

**采纳的做法**：字面问题命中不再是独立于置信度之外的永久放行通道，而是"这条证据的先验极强"——直接跳到它归属的那条条件当前所在的服务档位去检查，而不是跳过检查（见上面「置信度分档判定」）。这样处理仍然保留了字面问题捷径原本要解决的问题（四元组抽取抖动不应让同一句话反复无法命中同一条使用路径），只是"命中之后要不要直接服务"现在统一交给同一套分档逻辑回答，不再有第二套判定标准。

**owning condition 的可判定性**：要从"命中的字面问题"反查到"是哪一条 observed_conditions 收留了这个问法"，要求 `known_question_terms` 是条件级字段而不是链接级字段——这正是「数据结构」把它从表级列下沉到每条条件字段的原因，不是顺手整理。字面问题捷径的本意是"同一句话问两次，即使四元组抽取抖动出不同结果，也应该命中同一条使用路径"——这个"同一条使用路径"指的就是某条具体的 observed_conditions，下沉之后"归属条件"从设计上就是良定义的，不需要另外发明一套反查规则。新写入按精确归属记录（`AppendObservedCondition` 命中/新建哪条条件，就把这次的字面问题追加进哪条条件的 known_question_terms）；迁移期间的存量数据用"复制到全部条件"的方式回填（见「Migration 与回填」），是一次性、明确接受的粗粒度近似，不影响新数据的精确性。

**Subject 同义词归一化——2026-08-12 改判，Match 不再消费（取代 2026-08-11「角色收窄」的口径）**：`MatchConditionGroups`/`BuildQueryConditionTerms` 不再对 subject 做 `SynonymResolver.Canonicalize`，四个字段一律走 `text.Normalize` 后的精确相等，subject 与其余三项同层级对待。`subject_synonyms` 表、挖掘链路（`gap_mined`）、preset 别名导入本身**不受影响，继续存在并正常运行**——只是它们判断"这算不算已知同义词"的结果不再喂给 Match 的第一轮，而是被 `Matcher.SubjectOnlyMiss`（Trace 的 `subject_synonym_gap` 近似检测消费方）内联使用：`SubjectOnlyMiss` 现在自己做同义词归一化比较（原来依赖 `BuildQueryConditionTerms` 顺带做这件事，该函数改为纯精确匹配后不能再借用，`SubjectOnlyMiss` 已相应改为内联实现），继续用于诊断"哪些问法只差 subject 没对上"，与查询时的 Match 判定完全分离。

**`gap_mined` 挖掘链路定案保留、默认策略仍是自动（2026-08-12）**：`study.synonym_auto_promote` 默认 `true`（原 `false`）的决定不变——一条同义词对判断错了，最坏后果是下次同一措辞的 Match 精确匹配仍然吃不到这条链接、回落慢路径，`fast_path_verify` 仍会把关，不需要事前逐对人工审阅。但挖掘链路存在的意义需要重新表述：2026-08-11 曾经把它定位为"给模型辅助匹配沉淀免费规则"，随着模型辅助匹配本身被撤销，这条链路现在只服务 Trace 的诊断可见性（`SubjectOnlyMiss` 近似检测、Study 步骤 2a 的候选聚合）——它不再是任何查询时判定路径的输入，纯粹是"这条问法的 subject 措辞变体值得被记录下来观察"的信号收集，不改变 Match 的实际命中结果。`source=preset` 部分同样不受影响，继续零维护成本生效，只是同样不再被 Match 消费。

### 步骤 3a：同义词候选确认 API（2026-07-24 新增，2026-08-12 reject 范围扩大）

与步骤 3 的 confirm/reject 同构，作用对象是 `subject_synonyms` 而非 `activation_links`。`synonym_auto_promote` 默认 `true` 后，`confirm` 不在默认路径上（多数行直接落地为 `active`，不产生待确认候选）；`false` 时是唯一的 candidate → active 入口，作灰度回退：

```text
GET    /subject-synonyms
  查询参数：status、limit（默认 50）、offset
  响应：[{ synonym_id, domain_id, term, canonical, source, status, created_at, updated_at }]

GET    /subject-synonyms/:id
  响应：完整字段 + created_from 关联的问法列表

POST   /subject-synonyms/:id/confirm
  仅对 status=candidate 生效（synonym_auto_promote=false 时才会产生这个
  状态）；status → active；写 learning_results
  (action=synonym_candidate 对应行 status=applied)；调用 Matcher.InvalidateCache
  使新映射立即生效。

POST   /subject-synonyms/:id/reject
  对 status ∈ {candidate, active} 生效（2026-08-12 修订：新增 active 可
  拒绝——默认自动生效意味着多数行从未经过人工审阅，事后发现某条同义词
  对判断错了，需要能直接撤销一条已经在生效的映射，不能只对还没生效的
  candidate 生效）；status → rejected；调用 Matcher.InvalidateCache 使
  撤销立即生效；不自动复活，需人工在 UI 显式重新提交。
```

候选来源（Study 聚合 `subject_synonym_gap` 事件产生 `pending_confirm`）见 `study.md`；挖掘触发条件见 `trace.md` 步骤 3；完整设计见 `docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md`。

### 步骤 3：人工确认 API（2026-08-13 大幅改写，理由见下）

`study.auto_promote` 与其产生的 `pending_confirm` 晋升确认流程已经不存在——见「状态机」的 2026-08-13 编注：`verified` 现在是一个连续、实时的派生结果，没有一次单独的"晋升"动作，也就没有对象可供人工"确认"。原本这组 API 里与晋升相关的部分（`POST /activation-links/:id/confirm`、晋升 `pending_confirm` 相关字段）随之移除；`GET` 系列（列表/详情/问法/学习动作时间线）与 `POST /activation-links/:id/reject` 保留，但语义相应调整：

```text
GET    /activation-links
  查询参数：status、point_id、limit（默认 50）、offset
  响应：[{ link_id, question_terms, point_id, point_summary, unit_center,
           status, adopt_count, fail_count, last_used_at, created_at }]
  （point_summary / unit_center 由 JOIN knowledge_points / knowledge_units 补充；
   status 现在是「状态机」一节定义的派生值，candidate/verified/deprecated
   三态之一）

GET    /activation-links/:id
  响应：完整字段 + created_from；不再有 pending_promote_reason（没有
        待确认晋升这回事了）；不含 learning_results（改走下方懒加载接口）；
        新增 conditions: [{ subject, intent, audience, constraint,
        success_count, failure_count, audited_success_count,
        audited_failure_count, mean, tier, last_seen_at }]——按条件展示
        置信度明细，是本次改写新增的核心可观测数据（page.md 步骤 2 消费）

GET    /activation-links/:id/questions
  响应：{ matched: [{question, trace_id, created_at, path_type, retrieval_quality}],
          created_from: [同上] }
  matched：traces.activation_link_ids 含本 link 的问答原文；
  created_from：链接 created_from 事件关联的 traces 原文（创建燃料）

GET    /activation-links/:id/learning-results
  响应：[{ result_id, action, status, reason, created_at, ... }]
  这条链接过往被人工/自动剪枝的时间线，供详情对话框折叠区按需加载
  （不再有晋升/降权/重新验证类 action，这些 action 常量随旧状态机一并
  停止产生，见 study.md「learning_results action 枚举变化」）

POST   /activation-links/:id/reject
  对任意 status 的链接生效（不再要求 status=candidate——一条已经
  verified 的链接同样可能被人工判断证据不可信；对已经因 KP lifecycle
  而 deprecated 的链接调用是无害的空操作，反正不参与匹配）。
  语义变化（2026-08-13）：原先是 candidate → deprecated 的状态跳变；
  新模型下 deprecated 只由 KP lifecycle 驱动（见「状态机」），不能再被
  人工操作直接设置，否则会和"deprecated 只表示知识点还在不在"这条简化
  后的定义自相矛盾。reject 现在意味着"人工判断这条链接当前全部观测证据
  都不可信，清空重来"——对 linkID 的 observed_conditions 执行一次全量
  剪枝（等价于 study.md「收敛剪枝」对该链接下每一条条件都判定为收敛
  低分，只是触发方是人工而非 Study 的周期扫描），清空后按「回退路径」
  调用既有的 ProjectLegacyFields 同步展示字段；派生 status 相应回落到
  candidate（空条件默认落点，见「与旧状态机的映射」）。链接不会真的
  消失——后续若又出现确证信号，会重新走 AppendObservedCondition 从零
  积累证据，这与它此前是否被人工驳回过无关。这是有意的行为变化，不是
  疏漏：旧机制里 deprecated 是终态、不允许任何新证据复活；新机制里
  "清空重来"不是终态——旧终态本意是防止被淘汰的候选反复自动重建，这个
  目的现在由「同 point_id 已存在 deprecated 链接则拒绝创建」这条规则
  在真正的 deprecated（即 KP lifecycle 变化）场景下继续把守；人工的
  主观判断"这批证据不可信"不该永久锁死一个知识点，因为知识点本身依然
  current，后续问法完全可能证明这次人工判断过于保守；
  写 learning_results(action=prune_condition, object_type=activation_link,
  reason="manual_reject", status=applied)——与 study.md「收敛剪枝」的
  自动剪枝共用同一个 action 名，只是 reason/confirmed_by 标注来源不同；
  响应：{ link_id, status: "candidate", pruned_conditions: N }
```

确认/驳回与 Study 的关系（2026-08-13 改写）：Study 不再产生任何"待人工确认"的晋升信号（`pending_confirm` 相关的晋升流程整体消失，见上）；`POST /activation-links/:id/reject` 与 Study 的自动收敛剪枝，是同一个底层操作（清空/部分清空 `observed_conditions`）的两个触发入口——一个由人工发起、覆盖该链接全部条件；一个由 Study 周期扫描自动发起、逐条件判定，见 `study.md`「收敛剪枝」。

## 依赖

```text
基础设施：SQLite（migration）、结构化日志、HTTP 框架
LLM：     不依赖（2026-08-11 曾为 Match 步骤 2 引入 LLMClient 依赖用于
          模型辅助匹配，2026-08-12 改判撤销：`activation_match_judge.md`
          已删除，`Matcher`/`BundleMatcher` 不再持有 llmClient 字段，
          `Service.SetLLMClient` 移除，Match 恢复为纯同步内存计算、
          零外部调用）
Trace：   复用问题归一化 / 分词 / 停用词代码（提取为 foundation 层共享包，
          避免 Trace 与 Activation 两份实现漂移）；subject_synonym_gap 学习
          事件的产生方（本模块不产生，只消费其聚合结果，经 Study 中转）
Lifecycle：匹配器 JOIN knowledge_points.lifecycle 过滤；SetUnitLifecycle 直接
          调用本模块把受影响 point_id 对应链接行的 status 写为 deprecated
          （2026-08-13 新增的直接写入路径，取代原先"Study 感知后生成降权
          信号"的间接机制，见「状态机」「与 Study 的分工变化」）
Study：   RecordOutcome / RecordAuditOutcome 不再由 Study 调用（2026-08-13
          起改由 Trace 在产生 activation_success/failure/
          activation_audit_success/failure 学习事件的同一步直接调用，
          见 trace.md）；Study 的新调用面是「收敛剪枝」——对判定为收敛
          低分的条件调用剪枝接口清空/移除，见 study.md；
          subject_synonyms 候选生成的唯一调用方（本模块提供 CRUD/confirm/reject，
          Study 只负责聚合 subject_synonym_gap 事件并写候选行，这条链路
          与本次置信度改写无关，原样保留）
Foundation preset：LoadPresetData 解析 domains.json concept.aliases 写入
          subject_synonyms（source=preset），本模块只读该表
```

## 完成标准

```text
migration 建表成功，UNIQUE(point_id) 约束生效（每个 point 至多一条链接）；
CreateLink 幂等；对 deprecated 同 point 链接拒绝重建并记录日志；
迁移与回填：存量 observed_conditions 的 hit_count 正确重命名为
  success_count（数值原样保留）、failure_count/audited_success_count/
  audited_failure_count 初始化为 0；存量表级 known_question_terms 正确
  复制进该链接名下每一条条件、原表级列被 DROP；
置信度公式：mean(cond) 严格实现 (success+1)/(success+failure+2)；新条件
  （0/0）mean=0.5；audited_mean 用同一公式套用 audited_* 计数；
分档判定：mean < serving_confidence_min → exploring；否则
  audited_success_count+audited_failure_count ≥ audit_sample_min 且
  audited_mean ≥ serving_confidence_min → trusted；否则 self_graded
  （测试用例：分别构造三档边界值，断言 tier 判定正确）；
探索/审计抽样：exploring 档按 explore_rate_low 概率产出 LinkMatch，
  self_graded/trusted 档必产出 LinkMatch 且各自按 explore_rate_self_graded/
  explore_rate_trusted 概率置位 auditSampled（fake 环境下可注入固定
  随机源断言抽样边界）；
RecordOutcome / RecordAuditOutcome：正确定位归属条件、正确增量对应
  计数；audited_* 的增量必然伴随同方向的 success_count/failure_count
  同步增量（测试用例断言 audited_success_count ≤ success_count、
  audited_failure_count ≤ failure_count 恒成立）；写入后该链接的派生
  status 立即反映新计数（不需要等下一次 Match）；调用后 InvalidateCache
  生效，下一次 Match 读到新数据；定位不到归属条件时不报错、记录 warn；
派生 status：verified ⟺ 存在 ≥1 条 tier∈{self_graded,trusted} 的条件；
  candidate ⟺ 不满足 verified 且 KP lifecycle=current（含
  observed_conditions 为空的情况）；deprecated ⟺ KP lifecycle !=
  current，与置信度计数无关（测试用例：把某条件的 failure_count 推到
  mean 远低于服务门槛，该条件降回 exploring，链接若无其他更高档条件则
  status 从 verified 回落 candidate，不经过 weakened——因为 weakened
  已不存在）；
字面问题捷径：命中后正确路由到归属条件（known_question_terms 现在挂在
  条件上），按该条件当前档位处理，不再无条件直接命中（测试用例：归属
  条件被大量失败拖到 exploring 档时，字面问题命中改按 explore_rate_low
  抽样，不再是无条件 score=1.0）；
匹配器：输入为 ExpandedQuery；
        subject 与 intent/audience/constraint 同层级精确字符串相等
        （2026-08-12 改判：不再对 subject 做 overlap/子串判断，取代
        此前"linkCore ⊆ Qs 越短越容易命中"的语义；测试用例：链接
        subject_terms 是问题 subject 的真子集但不完全相等时不命中）；
        任一维度不命中即不命中，即使其余三维全同且词项高度重合
        （测试用例：链接约束集合 {"产品A"}、问题约束"产品B" → 不命中）；
        四项任一侧全空时走回退：链接从未观测到非空 audience/constraint
        且 question_terms 逐字节相等才命中，命中后不经过分档判定；
        指向非 current KP 的链接不参与匹配（deprecated 的唯一来源）；
        candidate 状态本身不再是一个额外的匹配排除条件——是否参与匹配
        看每条条件的档位，不看链接的派生 status（见「候选加载不再按
        status 过滤」）；
        Match 全程不发起模型调用（2026-08-12 改判撤销此前的第二轮模型
        辅助匹配；`activation_match_judge.md`、`matchCandidate`、
        `nearMissGroups`、`judgeRound2`、`ModelMatchEnabled` 均已从
        代码移除，fake 环境无需再断言"候选组为空/已命中时不触发模型
        调用"这类用例，因为已经不存在触发模型调用的路径；探索/审计抽样
        用的是本地随机数生成，同样不发起任何外部调用）；
条件刷新：已有链接的 point 出现新确证信号时，computeLinkCondition 重新
        归纳并通过 UpdateConditions 写回（不产生新链接、不写 learning_results，
        见 study.md 步骤 2）；
缓存失效：条件计数变更、KP lifecycle 变更、或人工 reject 剪枝后，下一次
        Match 反映新状态；
reject API：对任意 status 的链接生效；执行后该链接 observed_conditions
        清空、展示字段（question_terms 等）同步清空、派生 status 回落
        candidate、写 learning_results(action=prune_condition)；
fake 环境下全部分档路径、抽样路径（含固定随机源用例）、匹配路径、
  条件刷新路径、reject 剪枝路径测试稳定运行；
subject_synonyms：preset alias 正确加载为 active 行，重跑 preset 刷新 canonical
  不新增重复行、不覆盖 gap_mined 行；2026-08-12 改判后 Match 本身不再
  消费这张表（subject 与其余三项一样走精确匹配），该表继续供
  `SubjectOnlyMiss` 诊断比较与 Trace `subject_synonym_gap` 事件使用；
  confirm/reject 正确迁移状态并触发 Matcher.InvalidateCache；
  synonym_auto_promote=true（默认）时 gap_mined 候选直接落地为 active，
    不产生 pending_confirm；reject 对 active 行同样生效，拒绝后
    Matcher.InvalidateCache 立即生效、该措辞下次不再免费命中
  （本节完全不受本次置信度改写影响，原样保留）。
```
