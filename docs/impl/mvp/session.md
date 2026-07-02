# Session 实现路径

## 职责

Session 模块负责在用户输入进入检索前，将其补全为一个明确的、可检索的问题上下文。核心目标是：

- 通过一次 LLM 调用完成**上下文感知的四元组解析**（参考对话式检索的 query rewriting / standalone question 模式）：
  参考上一轮对话（问题、回答结尾、解析结果），把当前输入补全为独立完整的问题（standalone_question），
  并提取 intent（意图）/ subject（主题）/ audience（对象）/ constraint（约束）四个字段；
- 省略式追问、指代、纠正、基于上一轮回答内容的追问，均由该次调用统一处理：
  未提到的字段从上一轮解析继承，新话题不继承；
- 所有可由规则生成的字段（topic、is_continuation、is_interrupt）由程序处理，不进入 LLM；
- 两层修复机制保证 LLM 输出质量：JSON 格式修复 + intent 单字段重试；
- 维持单线程任务连续性；响应系统级打断。

MVP 阶段不实现多话题备忘夹、主动召回、状态衰减算法、置信度评分或完整风险矩阵。

---

## 核心组件

```text
SessionState（内存结构，per-conversation）
  DialogueState    对话状态：topic、intent、subject、audience、constraint、
                   last_question、recent_subjects、clarification_log
  WorkingState     工作状态：current_task、current_subject、step_summary、continuable_action

SessionStore（内存 + 数据库两级）
  内存：map[session_id]*SessionState，热路径直接读写
  数据库：sessions.state_snapshot，持久化快照，服务重启或会话切换时恢复

InterruptDetector（规则）
  关键词匹配，识别系统级打断，最优先运行

ContinuationDetector（规则）
  关键词匹配，识别连续性输入（"继续"/"下一个"等），需 continuable_action 非空

SessionParser（LLM，每轮至多一次主调用）
  上下文感知解析：输入 = 上一轮问题 + 上一轮回答结尾 + 上一轮解析四元组 + 当前输入
  输出 = intent、subject、audience、constraint、standalone_question
  配套两层修复机制

GapDetector（规则）
  subject 与 current_subject 均为空 → gap: vague

ClarificationPlanner（规则）
  决定 retrieve / clarify / skip

QueryExpander（规则）
  拼装 ExpandedQuery；standalone_question 非空时覆盖 expanded_question

HTTP API
  GET    /sessions              会话列表
  POST   /sessions              新建会话
  DELETE /sessions/:id          删除会话
  GET    /sessions/:id/turns    获取会话轮次记录
  POST   /session/turn          处理自然语言输入
  POST   /session/clarify       处理页面点选结果
  POST   /session/working       更新工作状态
```

---

## 数据结构

### 数据库表

```sql
-- 会话元数据
CREATE TABLE sessions (
    session_id      TEXT PRIMARY KEY,
    title           TEXT NOT NULL DEFAULT '',
    -- 取最近一轮用户输入的前 30 字，由程序截取，不调 LLM
    state_snapshot  TEXT NOT NULL DEFAULT '{}',
    -- 需要持久化的 SessionState 字段，JSON 序列化：
    -- intent、subject、audience、constraint、last_question、recent_subjects、
    -- clarification_log、current_subject、step_summary、continuable_action
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 每轮对话记录
CREATE TABLE session_turns (
    turn_id      TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    turn_index   INTEGER NOT NULL,       -- 轮次序号，从 1 开始
    user_input   TEXT NOT NULL,
    action       TEXT NOT NULL,          -- retrieve / clarify / interrupted
    answer_id    TEXT,                   -- action=retrieve 时关联 answers.answer_id，Answer 完成后补填
    clarify_msg  TEXT,
    -- action=clarify 时的澄清问题文本；options 不持久化
    -- 切换回历史会话时，澄清轮次只展示问题文本和用户的选择结果（从 clarification_log 读取），
    -- 不还原选项按钮；这是 MVP 的已知取舍，不影响后续对话的正确性
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_session_turns_session_id ON session_turns(session_id);
```

`ON DELETE CASCADE`：删除 session 时 turns 自动清除，不需要业务层处理。

**turn_index 生成**：利用 SQLite 串行写的特性，在 INSERT 语句内用子查询原子生成，不需要应用层加锁：

```sql
INSERT INTO session_turns (turn_id, session_id, turn_index, ...)
VALUES (?, ?, (SELECT COALESCE(MAX(turn_index), 0) + 1
               FROM session_turns WHERE session_id = ?), ...)
```

### SessionState（内存 + 数据库快照）

```text
SessionState：
  dialogue：
    topic              string    由程序从 intent + subject/current_subject 拼装，不持久化
    intent             string    LLM 提取；持久化
    subject            string    LLM 提取，问题核心主题；持久化
    audience           string    LLM 提取，待遇/规则归属的角色；持久化
    constraint         string    LLM 提取，专有名称/地点/时间等限定词；持久化
    last_question      string    上一轮最终检索问题（standalone 补全后），下一轮解析的上下文；持久化
    recent_subjects    []string  最近 3 轮的 subject，按轮次倒序，澄清选项来源；持久化
    clarification_log  []ClarificationRecord  持久化

  working：
    current_subject    string    最近一次非空 subject，省略式追问的兜底主题；持久化
    current_task       string    不持久化（可从 step_summary 推断）
    step_summary       string    上一轮回答的结尾文本（前端在 Answer 完成后回传末尾 ≤300 字，
                                 结论通常在结尾，故从尾部截取）；持久化
    continuable_action string    列表延续场景的下一步动作；持久化
```

**持久化字段**序列化为 `state_snapshot` JSON 存入 `sessions` 表；`topic` 实时生成，不持久化。

```text
ClarificationRecord：
  question   string
  options    []string
  response   string    "selected:<ref>" / "refused" / "pending"
  turn       int
```

### SessionStore（内存 + 数据库两级读取）

```text
Get(sessionID) *SessionState：
  1. 内存中有 → 直接返回
  2. 内存中无（切换会话 / 服务重启）→ 查 sessions.state_snapshot → 反序列化写入内存 → 返回
  3. 数据库也无 → 返回空 SessionState（新会话首次访问）

Set(sessionID, state)：
  1. 写入内存
  2. 序列化持久化字段 → UPDATE sessions SET state_snapshot=?, updated_at=?

Delete(sessionID)：
  1. 清除内存
  2. DELETE FROM sessions WHERE session_id=?（级联删除 session_turns）
```

**写入时机**：每次 `/session/turn` 或 `/session/clarify` 返回前，调用 `Store.Set` 同步持久化。不做异步批量写入，保证切换会话时状态已落盘。

---

### API 数据结构

```text
TurnInput：
  session_id    string
  user_input    string

TurnResult：
  session_id         string
  action             string    "retrieve" / "clarify" / "interrupted"
  expanded_query     ExpandedQuery        action=retrieve 时有值
  clarification      ClarificationPrompt  action=clarify 时有值
  state_snapshot     SessionState         调试用

ClarifyInput：
  session_id    string
  selected_ref  string    空字符串表示用户跳过

ClarifyResult：
  action          string    "retrieve" / "skip"
  expanded_query  ExpandedQuery

ExpandedQuery：
  original_input       string
  expanded_question    string    standalone_question 非空时即为补全后的独立问题
  subject              string
  intent               string
  audience             string
  constraint           string
  default_assumptions  []string
  allow_retrieval      bool      始终为 true

ClarificationPrompt：
  question  string
  options   []Option

Option：
  ref    string
  label  string
```

Page 将 `expanded_question` 与 `subject` / `intent` / `audience` / `constraint` 一并传给
`POST /answer/stream`，四元组经 Retrieval 的 `QueryContext` 用于 source filter / outline
recall / rerank（见 retrieval.md）。

---

## 处理流程

### POST /session/turn

```text
1. InterruptDetector（规则）
   匹配打断词：停 / 停下 / 别说了 / 不用了 / 清空 / 重来 / 重置 / 从头 / 算了重来
   → 命中：清空 SessionState，返回 action=interrupted，流程结束

2. ContinuationDetector（规则）
   匹配连续词：继续 / 下一个 / 再看一个 / 按刚才 / 基于上面
   且 working.continuable_action 非空
   → 直接用 continuable_action 构造 ExpandedQuery，记录 last_question，
     返回 action=retrieve，跳过 LLM

3. SessionParser（LLM）
   见"SessionParser"章节
   输入：上一轮问题（dialogue.last_question）+ 上一轮回答结尾（working.step_summary，
         从尾部截取 ≤300 字）+ 上一轮解析四元组 + 当前输入
   输出：intent、subject、audience、constraint、standalone_question
   四元组输出的是"本轮解析后的完整结果"：省略式追问/纠正未提到的字段由 LLM 从上一轮
   继承，新话题不继承；程序直接整体覆盖 DialogueState，不做程序级合并

4. 更新状态
   intent/subject/audience/constraint → dialogue 对应字段（整体覆盖）
   subject 非空 → working.current_subject = subject，
                  追加到 recent_subjects 队尾（超过 3 条移除队首）
   程序生成 topic：intent + " - " + subject（subject 为空时用 current_subject）

5. GapDetector（规则）
   parsed.subject 为空 且 working.current_subject 为空 → gap: vague
   否则无缺口

6. ClarificationPlanner（规则）
   无缺口 → action=retrieve，subject = parsed.subject（为空时用 current_subject 兜底）
   gap: vague 且 recent_subjects 非空 → ClarificationPrompt，options=recent_subjects（最多 3 条），
                                        question="您想了解哪个主题？"，action=clarify
   gap: vague 且 recent_subjects 为空 → ClarificationPrompt，options=[]，
                                        question="请描述您想了解的内容"，action=clarify
                                        （用户输入文字，下一轮重走 /session/turn）
   澄清去重：clarification_log 最近一条 response=refused → 跳过澄清，action=retrieve（skip 路径）

7. QueryExpander（规则，action=retrieve / skip 时）
   构造 ExpandedQuery（见"问题补全"章节）；
   parsed.standalone_question 非空 → 覆盖 expanded_question；
   dialogue.last_question = 最终 expanded_question（供下一轮解析作上下文）

8. 更新 clarification_log（若 action=clarify）
9. 持久化 SessionState，写入 session_turns，返回 TurnResult
```

### POST /session/clarify

```text
selected_ref 非空：
  working.current_subject = selected_ref；dialogue.subject = selected_ref
  更新 clarification_log（response="selected:<ref>"）
  构造 ExpandedQuery（subject=selected_ref），记录 last_question，返回 action=retrieve

selected_ref 为空（用户跳过）：
  更新 clarification_log（response="refused"）
  working.current_subject 非空 → 默认绑定，返回 action=retrieve
  否则 → 返回 action=skip
```

---

## SessionParser

### 设计思路

对话中的追问大多不是自包含的：省略条件（"漠河呢"）、指代上一轮回答内容（"这个保护期怎么算"）、
纠正（"我问的是销售不是实施"）。业界对话式检索的通行方案是 **query rewriting**：用一次
LLM 前置调用，参考近期对话历史把当前输入改写为 standalone question，再交给单轮检索管线。

本模块把该模式嫁接到四元组范式上：**一次调用同时产出 standalone_question 和解析四元组**，
不增加调用次数。上下文窗口只取最近一轮（问题 + 回答结尾 + 解析结果）——全量历史反而劣化
小模型效果且浪费 token。

### 职责边界

LLM 负责程序无法替代的语义判断：

```text
standalone_question  补全省略/指代后的独立完整问题（用于检索 FTS 与 Answer）
intent               查询意图（动宾短语）
subject              核心主题（可检索的领域概念，保留动作语义）
audience             待遇/规则归属的角色（用户明确说出的优先，其次从动作推导）
constraint           专有名称/地点/时间等限定词
继承判断             省略式追问继承上一轮字段，新话题不继承
```

以下字段由程序生成，不进入 LLM：

```text
topic           由 intent + subject 拼装
is_continuation 由 ContinuationDetector 关键词匹配
is_interrupt    由 InterruptDetector 关键词匹配
```

### Prompt 文件

`config/prompts/session_parse.md`（v8）。设计约束：**目标模型为 14B–30B 小参数模型**，
规则区用短句条目，判断逻辑主要靠示例传达；示例的上下文块格式与真实 User 段完全同构。

System 段规则要点（完整内容以 prompt 文件为准）：

```text
- standalone_question：当前输入完整 → 照抄；有省略/指代 → 用上一轮问题、回答、解析补全
- intent：动宾短语 ≤20 字
- subject：核心主题，保留动作语义；专有名称/地点放 constraint
- audience：用户明确说出角色 → 直接采用（优先于推导）；
            未说 → 从 subject 动作推导默认执行者（"实施"→"实施人员"）；
            纯概念名词或规则不因角色而异 → 空
- constraint：产品名/地点/时间等限定词；已进 audience 的角色词不重复
- 继承：追问（替换条件/追问细节/纠正）→ 未提到字段从上一轮解析继承；
        新话题 → 全部重新解析；模糊指代 → subject 留空
```

示例覆盖（8 个）：完整问题、省略式替换（换地点其余继承）、基于上一轮回答内容的追问、
用户明确说出角色优先于推导、纠正式追问、新话题不继承、纯延续追问全继承、模糊指代。
示例一律使用泛化占位实体（如"星火系统"），不绑定具体业务数据。

User 段：

```text
上一轮问题：{{last_question}}
上一轮回答（结尾部分）：{{last_answer}}
上一轮解析：{{last_parse}}

当前输入：
{{user_input}}
```

### 上下文变量控制

```text
{{last_question}}  dialogue.last_question，截断至 100 字；为空填"（无）"
{{last_answer}}    working.step_summary，从尾部截取 300 字（truncateTail，结论在结尾）；
                   为空填"（无）"
{{last_parse}}     上一轮四元组的 JSON 单行（formatLastParse）；全空填"（无）"
{{user_input}}     原始用户输入，截断至 200 字
```

---

## 解析修复机制

SessionParser 返回后经过两层处理，再进入 GapDetector。

### 第一层：JSON 格式修复（程序，不调用 LLM）

```text
1. 提取 JSON 块
   用正则从模型输出中提取第一个 {...}，去除前后多余文字

2. 字段截断
   intent > 50 字 → 截断；subject > 100 字 → 截断；
   audience > 50 字 → 截断；standalone_question > 200 字 → 截断

3. 有效性判定
   intent 非空 → 通过；否则进入第二层重试
```

### 第二层：intent 单字段重试（LLM，最多 1 次）

Prompt 文件 `config/prompts/session_retry_intent.md`，只输出意图文本。
失败或为空 → intent="" 降级。

### 降级行为

```text
intent=""            → 保持空，流程继续
subject=""           → GapDetector 按 current_subject 兜底判断是否 gap: vague
standalone_question="" → 不覆盖 expanded_question，走 QueryExpander 规则拼装
```

两层处理后，Session 流程始终继续，不因 LLM 输出质量问题中断。

---

## 问题补全（QueryExpander）

纯规则拼装，不调用 LLM：

```text
ContinuationDetector 命中且 continuable_action 非空：
  expanded_question = continuable_action

否则：
  built = subject + " " + intent（intent 已含 subject 时只用 intent）
  用户输入比 built 短（省略式）→ expanded_question = built
  否则 → expanded_question = 用户输入原文

之后（handler 层）：
  parsed.standalone_question 非空 → 覆盖 expanded_question（优先级最高）

评估角度未明确时 → default_assumptions = ["综合角度"]（不追问）

topic 更新：
  dialogue.topic = intent + " - " + subject（subject 为空时用 current_subject）
```

---

## HTTP API

### GET /sessions

返回会话列表，按 `updated_at` 倒序。

```text
响应体（JSON）：
  sessions[]：
    session_id   string
    title        string    最近一轮用户输入前 30 字
    updated_at   string
    created_at   string
```

### POST /sessions

新建会话，生成 session_id，写入 `sessions` 表（state_snapshot 为空 JSON `{}`）。

```text
响应体（JSON）：
  session_id   string
  created_at   string
```

### DELETE /sessions/:id

删除会话及其全部轮次记录（级联删除）。清除内存中对应 SessionState。

响应：204 No Content

### GET /sessions/:id/turns

返回指定会话的全部轮次记录，按 `turn_index` 升序。

```text
响应体（JSON）：
  turns[]：
    turn_id      string
    turn_index   int
    user_input   string
    action       string    retrieve / clarify / interrupted
    answer_id    string    可能为空
    clarify_msg  string    可能为空
    created_at   string
```

Page 切换到历史会话时调用此接口渲染对话记录，`answer_id` 非空时调用 `GET /answers/:id` 补充回答内容。

### POST /session/turn

处理自然语言输入。规则路径（打断、连续性）不调 LLM；其余情况调用 SessionParser（含修复机制），最多 2 次 LLM 调用（主调用 + intent 重试）。

返回前：持久化 SessionState → 写入 session_turns（answer_id 留空，由 Answer 服务端补填）。

### POST /session/clarify

处理页面点选，纯规则，不调 LLM。

返回前：持久化 SessionState。

### POST /session/working

```text
请求体：
  session_id          string
  step_summary        string    上一轮回答正文的末尾 ≤300 字（前端 slice(-300) 截取）
  continuable_action  string

响应：204 No Content
```

Answer 完成后由 Page 调用，**只负责更新 WorkingState**（step_summary、continuable_action），
不承担 answer_id 补填职责。`step_summary` 是下一轮 SessionParser 的
`{{last_answer}}` 上下文来源——**必须传回答正文（末尾截取），不能传占位文本**，
否则"基于上一轮回答内容的追问"（如"这个保护期怎么算"）无法解析。
若未调用，下一轮 `{{last_answer}}` 为"（无）"，降级为无回答上下文的解析。

**answer_id 补填由 Answer 模块在服务端完成**：`POST /answer/stream` 接收 `session_id` 参数，Answer 写库成功后直接执行：

```sql
UPDATE session_turns
SET answer_id = ?
WHERE rowid = (SELECT rowid FROM session_turns
               WHERE session_id = ? AND answer_id IS NULL
               ORDER BY turn_index DESC LIMIT 1)
```

这样即使前端在 Answer 返回前切换会话或关闭页面，answer_id 也不会丢失。

---

## LLM 调用汇总

| 场景 | 调用次数 | 说明 |
|------|---------|------|
| 打断输入 | 0 | InterruptDetector 规则命中 |
| 连续性输入且 continuable_action 非空 | 0 | ContinuationDetector 规则命中 |
| 正常自然语言输入，LLM 输出格式正确 | 1 | SessionParser 主调用 |
| 正常输入，主调用格式错误 | 最多 2 | 主调用 + intent 单字段重试 |
| 页面点选（/session/clarify） | 0 | 纯规则 |

每轮正常对话最多 1 次 Session LLM 调用，叠加 Retrieval + Answer，总调用数比原有流程 +1。
上下文感知解析不增加调用次数，只增加输入 token（约 400–500 字）。

---

## 与其他模块的边界

```text
Session → Retrieval / Answer：
  Session 输出 ExpandedQuery，Page 取 expanded_question + 四元组传给 POST /answer/stream，
  Session 不直接调用 Retrieval。四元组经 QueryContext 进入 Retrieval（见 retrieval.md）。

Answer → Session：
  Answer 完成后 Page 调用 POST /session/working 回传回答结尾（step_summary）；
  answer_id 由 Answer 服务端直接补填 session_turns。

Session → Trace / Study：
  Session 不触发 Trace 或 Study。澄清、打断等操作不产生学习信号。

Session → Foundation：
  依赖 SQLite 存储 sessions / session_turns 表；
  依赖 LLM client 调用 SessionParser；
  依赖配置加载 prompt 文件路径。
```

---

## 实现步骤

### 步骤 1：定义 SessionState，实现两级存储

`internal/session/state.go`：定义所有结构体（SessionState、ClarificationRecord、ParseResult 等）及 `stateSnapshot`（持久化字段的序列化结构）。

`internal/session/store.go`：实现 SessionStore，提供 Get / Set / Delete，Get 时先查内存，miss 则从 DB 反序列化恢复。

### 步骤 2：实现 InterruptDetector 和 ContinuationDetector

`internal/session/interrupt.go`：`DetectInterrupt(input string) bool`，关键词列表硬编码。

`internal/session/continuation.go`：`DetectContinuation(input string, state *SessionState) bool`，匹配连续词且校验 `continuable_action` 非空。

### 步骤 3：实现 SessionParser 及修复机制

`internal/session/parser.go`：

```text
Parse(ctx, input string, state *SessionState) ParseResult
  1. 构造 User 段变量（last_question 截断、last_answer 尾部截取、last_parse 拼装）
  2. 调用 LLM client，加载 config/prompts/session_parse.md
  3. 第一层修复（JSON 提取、字段截断、intent 非空判定）
  4. intent 为空 → session_retry_intent.md 单字段重试
  5. 返回 ParseResult{Intent, Subject, Audience, Constraint, StandaloneQuestion}
```

编写 `config/prompts/session_parse.md`（见上文 Prompt 章节）。

### 步骤 4：实现 GapDetector

`internal/session/gap.go`：

```go
type GapKind string
const (
    GapVague GapKind = "vague"
)
```

`DetectGaps(parsed ParseResult, state *SessionState, userInput string) []GapKind`：
subject 与 current_subject 均为空时返回 GapVague。

### 步骤 5：实现 ClarificationPlanner

`internal/session/planner.go`：`Plan(gaps []GapKind, parsed ParseResult, state *SessionState) PlanResult`。

### 步骤 6：实现 QueryExpander

`internal/session/expander.go`：`Expand(state *SessionState, plan PlanResult, input string) ExpandedQuery`，纯拼装；`UpdateTopic(state)` 更新 `dialogue.topic`。

### 步骤 7：实现 HTTP handler

`internal/session/handler.go`：注册七个路由，串联上述组件。

每次 `/session/turn` 和 `/session/clarify` 返回前：
1. 调用 `Store.Set` 持久化 SessionState
2. INSERT INTO session_turns（action=interrupted 时也写入记录）

### 步骤 8：集成到主流程

`cmd/server/main.go` 注册 Session 路由。`web/index.html`：
- 启动时调用 `GET /sessions` 渲染会话列表
- 发送问题前先调用 `POST /session/turn`，根据 action 决定展示澄清选项或进入检索
- Answer 完成后调用 `POST /session/working` 回传回答正文末尾 300 字（`slice(-300)`）
- 切换会话时调用 `GET /sessions/:id/turns` 渲染历史记录

### 步骤 9：测试

```text
单元测试（无 LLM）：
  InterruptDetector / ContinuationDetector：触发词覆盖
  第一层修复：JSON 提取、字段截断、standalone_question 提取
  truncateTail：空值 / 短文本 / 尾部截取
  GapDetector：vague 缺口及 current_subject 兜底
  ClarificationPlanner：各分支
  QueryExpander：拼装结果
  SessionStore.Get：内存命中 / miss 后从 DB 恢复 / 新会话空状态
  快照持久化：audience / constraint / last_question 序列化往返

SessionParser 测试（fake LLM client）：
  正常输出 → 直接通过
  格式错误（前后多余文字）→ 第一层修复后通过
  intent 缺失 → 重试后填入
  standalone_question 输出 → 覆盖 expanded_question，last_question 更新

集成测试（多轮对话）：
  完整问题 → retrieve
  省略式追问（"漠河呢"）→ 四元组继承 + standalone 补全 → retrieve
  基于上一轮回答内容的追问 → 从 last_answer 解析出主题 → retrieve
  subject 为空且无 current_subject → clarify → 点选 → retrieve
  用户拒绝澄清（selected_ref 为空）→ 默认绑定 current_subject 或 skip
  连续性输入（"继续"）→ 跳过 LLM → retrieve
  打断输入 → 清空状态 → interrupted
  服务重启后切换到旧会话 → 从 DB 恢复 SessionState → 继续对话
  删除会话 → session_turns 级联删除
  Answer 完成后 session_turns.answer_id 已补填（即使前端未调用 /session/working）
```

---

## 不实现的内容（MVP 范围外）

```text
置信度评分机制
多话题备忘夹
搁置任务主动召回
信息鲜活度衰减算法
复杂风险矩阵
Session → Trace / Study 的自动学习触发
长期澄清历史管理（跨 session 记忆）
跨多轮的深层指代消解（上下文窗口只取最近一轮）
结构化回答要点摘要（当前为回答正文尾部截取；若截断丢失关键信息再演进）
```
