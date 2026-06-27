# Session 实现路径

## 职责

Session 模块负责在用户输入进入检索前，将其补全为一个明确的、可检索的问题上下文。核心目标是：

- 通过一次 LLM 调用完成意图提取和指代解析，只让模型做程序无法替代的语义判断；
- 所有可由规则生成的字段（topic、is_continuation、is_interrupt）由程序处理，不进入 LLM；
- 对象唯一时直接绑定进入检索；对象多个时给页面选项，用户点选返回结构化结果；
- 两层修复机制保证 LLM 输出质量：JSON 格式修复 + 单字段重试；
- 维持单线程任务连续性；响应系统级打断。

MVP 阶段不实现多话题备忘夹、主动召回、状态衰减算法、置信度评分或完整风险矩阵。

---

## 核心组件

```text
SessionState（内存结构，per-conversation）
  DialogueState    对话状态：topic、intent、confirmed_objects、candidate_objects、recent_objects、clarification_log
  WorkingState     工作状态：current_task、current_object、step_summary、continuable_action

SessionStore（内存 + 数据库两级）
  内存：map[session_id]*SessionState，热路径直接读写
  数据库：sessions.state_snapshot，持久化快照，服务重启或会话切换时恢复

InterruptDetector（规则）
  关键词匹配，识别系统级打断，最优先运行

ContinuationDetector（规则）
  关键词匹配，识别连续性输入（"继续"/"下一个"等）

SessionParser（LLM，每轮至多一次主调用）
  只提取两个语义字段：intent、resolved_objects
  配套两层修复机制

GapDetector（规则）
  根据 resolved_objects 判断缺口类型

ClarificationPlanner（规则）
  决定 retrieve / clarify / skip

QueryExpander（规则）
  拼装 ExpandedQuery，topic 由程序生成

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
    -- 取第一轮用户输入的前 30 字，由程序截取，不调 LLM
    state_snapshot  TEXT NOT NULL DEFAULT '{}',
    -- 需要持久化的 SessionState 字段，JSON 序列化：
    -- intent、confirmed_objects、recent_objects、clarification_log、
    -- current_object、continuable_action、step_summary
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
    -- action=clarify 时的澄清问题文本；options 不持久化（candidate_objects 为中间态）
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
    topic              string               由程序从 intent + current_object 拼装，不由 LLM 生成
    intent             string               LLM 提取；持久化
    confirmed_objects  []Object             用户明确确认的对象；持久化
    candidate_objects  []Object             LLM 解析出的多个候选，等待用户点选；不持久化（中间态）
    recent_objects     []string             最近 3 轮提到或绑定的对象 ref，按轮次倒序；持久化
    clarification_log  []ClarificationRecord  持久化

  working：
    current_task       string               不持久化（可从 step_summary 推断）
    current_object     string               持久化
    step_summary       string               持久化
    continuable_action string               持久化
```

**持久化字段**序列化为 `state_snapshot` JSON 存入 `sessions` 表；`topic` 和 `candidate_objects` 不持久化，前者实时生成，后者为澄清中间态。

```text
Object：
  ref     string    对象引用
  source  string    "user_explicit" 或 "system_inferred"（由程序在解析后填入，不来自 LLM）

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
  state_snapshot     SessionSnapshot      调试用

ClarifyInput：
  session_id    string
  selected_ref  string    空字符串表示用户跳过

ClarifyResult：
  action          string    "retrieve" / "skip"
  expanded_query  ExpandedQuery

ExpandedQuery：
  original_input       string
  expanded_question    string
  confirmed_objects    []string
  intent               string
  default_assumptions  []string
  allow_retrieval      bool      始终为 true

ClarificationPrompt：
  question  string
  options   []Option

Option：
  ref    string
  label  string
```

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
   → 直接用 continuable_action 构造 ExpandedQuery，返回 action=retrieve，跳过 LLM

3. SessionParser（LLM）
   见"SessionParser"章节
   输出：intent、resolved_objects（[]string）

4. 程序生成 topic
   topic = intent（若 current_object 非空则拼接：intent + " - " + current_object）

5. 更新 DialogueState
   intent → dialogue.intent
   topic  → dialogue.topic
   resolved_objects → dialogue.candidate_objects（source=system_inferred）
   将本轮 resolved_objects 追加到 recent_objects 队尾，超过 3 条移除队首

6. GapDetector（规则）
   resolved_objects 为空 且 working.current_object 非空 → 无缺口（默认绑定 current_object）
   resolved_objects 为空 且 working.current_object 为空 → gap: no_object
   resolved_objects 多个                                → gap: multiple_objects
   resolved_objects 唯一                                → 无缺口
   intent 为空或泛化词（了解一下 / 看看 / 随便）         → gap: no_intent
   多个缺口同时存在：优先级 no_object > multiple_objects > no_intent

7. ClarificationPlanner（规则）
   无缺口 → 绑定对象到 confirmed_objects，构造 ExpandedQuery，action=retrieve
   gap: multiple_objects（候选 ≤ 3）→ 构造 ClarificationPrompt，options=resolved_objects，action=clarify
   gap: multiple_objects（候选 > 3）→ 取前 3 个构造 ClarificationPrompt，action=clarify
   gap: no_object，recent_objects 非空 → 构造 ClarificationPrompt，options=recent_objects（最多 3 条），
                                         question="您是指哪个文档？"，action=clarify
   gap: no_object，recent_objects 为空 → 构造 ClarificationPrompt，options=[]，
                                         question="请告诉我您想操作哪个文档"，action=clarify
                                         （用户只能输入文字，下一轮重走 /session/turn，不走 /session/clarify）
   gap: no_intent → 构造 ClarificationPrompt，options=[]，
                    question="您想了解它的哪个方面？例如设计思路、实现细节还是使用方法？"，action=clarify
                    （同上，用户输入文字，下一轮重走 /session/turn）
   澄清去重：本轮缺口与 clarification_log 最近一条相同且 response=refused
             → 跳过澄清，带默认假设 retrieve（无法给默认假设则 action=skip）

8. 更新 clarification_log（若 action=clarify）
9. 返回 TurnResult
```

### POST /session/clarify

```text
selected_ref 非空：
  写入 confirmed_objects（source=user_explicit）
  清空 candidate_objects
  更新 clarification_log（response="selected:<ref>"）
  构造 ExpandedQuery，返回 action=retrieve

selected_ref 为空（用户跳过）：
  更新 clarification_log（response="refused"）
  working.current_object 非空 → 默认绑定，返回 action=retrieve
  否则 → 返回 action=skip
```

---

## SessionParser

### 职责边界

LLM 只负责两件程序无法替代的事：

```text
intent          从自然语言中提取用户意图（一句话描述）
resolved_objects 识别代词 / 指示词所指向的对象，从上下文候选中绑定
```

以下字段由程序生成，不进入 LLM：

```text
topic           由 intent + current_object 拼装
is_continuation 由 ContinuationDetector 关键词匹配
is_interrupt    由 InterruptDetector 关键词匹配
source          Object.source 由程序填入 system_inferred
```

### Prompt 文件

`config/prompts/session_parse.md`

````markdown
---
version: v1
---

## System

你是对话状态解析助手。从用户输入中提取意图和操作对象。

按以下格式输出，不输出任何其他内容：
{"intent":"用户意图，不超过20字","resolved_objects":["对象1","对象2"]}

规则：
- intent：用动宾短语描述用户要做什么，例如"分析设计缺陷"、"查询实现方案"
- resolved_objects：从用户输入或上下文中识别出的操作对象（文件名、模块名等）
  - 用户明确提到 → 直接提取原文
  - 使用代词（它/这个/那个）→ 从上下文候选列表中选择最匹配的，填入其原始 ref
  - 无法确定 → 输出空数组 []
- 不要输出候选列表中不存在的对象名
- 不要猜测，不确定时用空数组

示例1（明确对象）：
上下文候选：["retrieval.md","session.md"]，当前对象：retrieval.md
用户输入：分析它的设计缺陷
输出：{"intent":"分析设计缺陷","resolved_objects":["retrieval.md"]}

示例2（无法绑定代词）：
上下文候选：[]，当前对象：（空）
用户输入：分析它
输出：{"intent":"分析","resolved_objects":[]}

示例3（多个对象）：
上下文候选：["retrieval.md","session.md"]，当前对象：（空）
用户输入：对比这两个文档
输出：{"intent":"对比文档","resolved_objects":["retrieval.md","session.md"]}

示例4（明确提到新对象，不在候选列表）：
上下文候选：["retrieval.md"]，当前对象：（空）
用户输入：帮我看看 answer.md
输出：{"intent":"查看文档","resolved_objects":["answer.md"]}

反例（不要这样输出）：
用户输入：分析它，但上下文候选为空
错误输出：{"intent":"分析","resolved_objects":["retrieval.md"]}  ← 不能凭空猜测对象
正确输出：{"intent":"分析","resolved_objects":[]}

## User

上下文候选（最近提到的对象）：{{recent_objects}}
当前操作对象：{{current_object}}

用户输入：
{{user_input}}

## Schema

```json
{
  "type": "object",
  "required": ["intent", "resolved_objects"],
  "properties": {
    "intent":            {"type": "string"},
    "resolved_objects":  {"type": "array", "items": {"type": "string"}}
  }
}
```
````

### 上下文变量控制

```text
{{recent_objects}}   recent_objects 列表，最多 3 条，每条 ref 截断至 60 字，逗号分隔
{{current_object}}   working.current_object，截断至 60 字；为空时填"（空）"
{{user_input}}       原始用户输入，截断至 200 字
```

User 段总长度上限约 400 字，System 段约 500 字，合计控制在 900 字以内，远低于 2000 字限制。

---

## 解析修复机制

SessionParser 返回后经过两层处理，再进入 GapDetector。

### 第一层：JSON 格式修复（程序，不调用 LLM）

按顺序尝试以下修复：

```text
1. 提取 JSON 块
   用正则从模型输出中提取第一个 {...}，去除前后多余文字

2. 字段类型修复
   intent 为 null         → 替换为 ""
   resolved_objects 为 null → 替换为 []
   resolved_objects 为字符串（模型输出单个对象不加数组）→ 包装为 ["<string>"]
   布尔值为字符串 "true"/"false" → 转换（本版本已无布尔字段，保留逻辑备用）

3. 字段截断
   intent 超过 50 字 → 截断至 50 字
   resolved_objects 单个 ref 超过 100 字 → 丢弃该条目

4. Schema 校验
   用 jsonschema 校验修复后的结果
   校验通过 → 进入第二层检查
   校验失败 → 进入第二层重试
```

### 第二层：必填字段重试（LLM，按字段单独重试）

Schema 校验失败时，只对缺失或非法的必填字段发起重试，最多重试 1 次。

**intent 缺失重试**

Prompt（直接作为 user message，不加载文件）：

```text
用户说了什么？用一句动宾短语描述用户意图，不超过20字，只输出意图文本，不输出其他内容。

用户输入：{{user_input}}
```

返回值为纯文本，程序直接赋值给 intent。失败或为空 → intent="" 降级。

**resolved_objects 缺失或类型错误重试**

Prompt（直接作为 user message）：

```text
从以下内容中找出操作对象名称（文件名、模块名等），只输出JSON数组。
找到则输出如：["retrieval.md"]
找不到则输出：[]
不要输出其他内容。

上下文候选：{{recent_objects}}
用户输入：{{user_input}}
```

返回值用正则提取 `[...]`，解析为字符串数组。失败 → resolved_objects=[] 降级。

### 降级行为

```text
intent=""          → GapDetector 识别为 gap: no_intent → 触发澄清，不报错
resolved_objects=[] → GapDetector 根据 current_object 是否存在决定缺口类型
```

两层处理后，Session 流程始终继续，不因 LLM 输出质量问题中断。

---

## 问题补全（QueryExpander）

纯规则拼装，不调用 LLM：

```text
ContinuationDetector 命中且 continuable_action 非空：
  expanded_question = continuable_action

否则：
  主体 = confirmed_objects[0].ref
  动词短语 = intent
  评估角度未明确时 → default_assumptions = ["综合角度"]（不追问）
  示例："分析 retrieval.md 的设计缺陷（综合角度）"

topic 更新：
  dialogue.topic = intent（若 current_object 非空：intent + " - " + current_object）
```

---

## ref 合法性校验

在 GapDetector 之前，对 resolved_objects 中的每个 ref 做合法性检查：

```text
合法条件（满足任一）：
  ref 出现在 recent_objects 中
  ref 出现在 confirmed_objects 中
  ref 是用户本轮输入中明确出现的字符串子串

不合法：
  ref 不满足以上任一条件（LLM 幻构）→ 从 resolved_objects 中移除
```

移除后若 resolved_objects 为空，等同于 LLM 未返回对象，走 no_object 缺口逻辑。

---

## HTTP API

### GET /sessions

返回会话列表，按 `updated_at` 倒序。

```text
响应体（JSON）：
  sessions[]：
    session_id   string
    title        string    第一轮用户输入前 30 字
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

处理自然语言输入。规则路径（打断、连续性）不调 LLM；其余情况调用 SessionParser（含修复机制），最多 2 次 LLM 调用（主调用 + 单字段重试）。

返回前：持久化 SessionState → 写入 session_turns（answer_id 留空，由 Answer 服务端补填）。

### POST /session/clarify

处理页面点选，纯规则，不调 LLM。

返回前：持久化 SessionState。

### POST /session/working

```text
请求体：
  session_id          string
  step_summary        string
  continuable_action  string

响应：204 No Content
```

Answer 完成后由 Page 调用，**只负责更新 WorkingState**（step_summary、continuable_action），不再承担 answer_id 补填职责。若未调用，ContinuationDetector 步骤中 `continuable_action` 为空，降级走 SessionParser 正常路径。

**answer_id 补填由 Answer 模块在服务端完成**：`POST /answer` 接收 `session_id` 参数，Answer 写库成功后直接执行：

```sql
UPDATE session_turns
SET answer_id = ?
WHERE session_id = ? AND answer_id IS NULL
ORDER BY turn_index DESC
LIMIT 1
```

这样即使前端在 Answer 返回前切换会话或关闭页面，answer_id 也不会丢失。

---

## LLM 调用汇总

| 场景 | 调用次数 | 说明 |
|------|---------|------|
| 打断输入 | 0 | InterruptDetector 规则命中 |
| 连续性输入且 continuable_action 非空 | 0 | ContinuationDetector 规则命中 |
| 正常自然语言输入，LLM 输出格式正确 | 1 | SessionParser 主调用 |
| 正常输入，主调用格式错误，单字段重试 | 最多 2 | 主调用 + 1 次字段重试 |
| 页面点选（/session/clarify） | 0 | 纯规则 |

每轮正常对话最多 1 次 Session LLM 调用，叠加 Retrieval + Answer，总调用数比原有流程 +1。

---

## 与其他模块的边界

```text
Session → Retrieval / Answer：
  Session 输出 ExpandedQuery，Page 取 expanded_question 传给 POST /answer，Session 不直接调用 Retrieval。

Answer → Session：
  Answer 完成后 Page 调用 POST /session/working 更新 WorkingState 并补填 answer_id。

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

`internal/session/state.go`：定义所有结构体（SessionState、Object、ClarificationRecord 等）及 `StateSnapshot`（持久化字段的序列化结构）。

`internal/session/store.go`：实现 SessionStore，提供 Get / Set / Delete，Get 时先查内存，miss 则从 DB 反序列化恢复。

### 步骤 2：实现 InterruptDetector 和 ContinuationDetector

`internal/session/interrupt.go`：`Detect(input string) bool`，关键词列表硬编码。

`internal/session/continuation.go`：`Detect(input string, state *SessionState) bool`，匹配连续词且校验 `continuable_action` 非空。

### 步骤 3：实现 SessionParser 及修复机制

`internal/session/parser.go`：

```text
Parse(ctx, input string, state *SessionState) ParseResult
  1. 构造 User 段变量（recent_objects 截断、current_object 截断）
  2. 调用 LLM client，加载 config/prompts/session_parse.md
  3. 第一层修复（JSON 提取、字段类型修复、截断、Schema 校验）
  4. 校验失败 → 单字段重试（intent / resolved_objects 分别处理）
  5. 返回 ParseResult{Intent, ResolvedObjects}
```

编写 `config/prompts/session_parse.md`（见上文 Prompt 章节）。

### 步骤 4：实现 ref 合法性校验

`internal/session/refcheck.go`：`FilterValid(refs []string, state *SessionState, input string) []string`。

### 步骤 5：实现 GapDetector

`internal/session/gap.go`：

```go
type GapKind string
const (
    GapNoObject       GapKind = "no_object"
    GapMultipleObjects GapKind = "multiple_objects"
    GapNoIntent       GapKind = "no_intent"
)
```

`Detect(parsed ParseResult, state *SessionState) []GapKind`，按优先级返回缺口列表。

### 步骤 6：实现 ClarificationPlanner

`internal/session/planner.go`：`Plan(gaps []GapKind, state *SessionState) PlanResult`。

### 步骤 7：实现 QueryExpander

`internal/session/expander.go`：`Expand(state *SessionState, plan PlanResult) ExpandedQuery`，纯拼装，同时更新 `dialogue.topic`。

### 步骤 8：实现 HTTP handler

`internal/session/handler.go`：注册七个路由，串联上述组件。

每次 `/session/turn` 和 `/session/clarify` 返回前：
1. 调用 `Store.Set` 持久化 SessionState
2. INSERT INTO session_turns（action=interrupted 时也写入记录）

### 步骤 9：集成到主流程

`cmd/server/main.go` 注册 Session 路由。`web/index.html`：
- 启动时调用 `GET /sessions` 渲染会话列表
- 发送问题前先调用 `POST /session/turn`，根据 action 决定展示澄清选项或进入检索
- Answer 完成后调用 `POST /session/working` 更新工作状态（answer_id 由 Answer 服务端直接写入 session_turns）
- 切换会话时调用 `GET /sessions/:id/turns` 渲染历史记录

### 步骤 10：测试

```text
单元测试（无 LLM）：
  InterruptDetector / ContinuationDetector：触发词覆盖
  第一层修复：各类格式错误的修复逻辑
  ref 合法性校验：幻构 ref 过滤
  GapDetector：三种缺口及优先级
  ClarificationPlanner：各分支
  QueryExpander：拼装结果
  SessionStore.Get：内存命中 / miss 后从 DB 恢复 / 新会话空状态

SessionParser 测试（fake LLM client）：
  正常输出 → 直接通过
  格式错误（前后多余文字、null 字段、字符串而非数组）→ 第一层修复后通过
  intent 缺失 → 重试后填入
  resolved_objects 缺失 → 重试后填入
  两个字段均缺失 → 各自重试，均降级

集成测试（多轮对话）：
  代词指代 → 唯一绑定 → retrieve
  代词指代 → 多候选 → clarify（options 非空）→ 点选 → retrieve
  no_object 且 recent_objects 非空 → clarify（options=recent_objects）→ 点选 → retrieve
  no_object 且 recent_objects 为空 → clarify（options=[]，纯文字提示）→ 用户输入文字 → 下一轮 /session/turn
  no_intent → clarify（options=[]，纯文字提示）→ 用户补充意图 → 下一轮 /session/turn
  用户拒绝澄清（selected_ref 为空）→ 默认绑定 current_object 或 skip
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
```
