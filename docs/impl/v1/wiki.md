# Wiki 编译实现路径（V1 初版）

## 职责

实现主题页 / 概念页的最小闭环：Study 产出的 Wiki 候选经人工确认后触发 LLM 编译，生成带证据回链的页面；人工发布后进入独立 Bleve 索引，作为检索的 Wiki 直答层；底层 KU/KP 状态变化时标记待重编译。

方法页 / 经验页 / 问题页 / 决策页与视角化编译推迟到 V3；Claim 双产物与防固化要素补齐属 V2（见 docs/impl/v2/readme.md）。

## 数据结构

```sql
CREATE TABLE wiki_pages (
    page_id          TEXT PRIMARY KEY,
    page_type        TEXT NOT NULL,
    -- topic / concept（V1 两种；编译输入相同，区别在标题组织，见步骤 3）
    concept_id       TEXT REFERENCES concepts(concept_id),
    title            TEXT NOT NULL,
    content          TEXT NOT NULL,
    -- 页面正文 Markdown（含防固化要素，见步骤 3）
    status           TEXT NOT NULL DEFAULT 'draft',
    -- draft / published / needs_recompile / archived
    source_point_ids TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：页面依赖的 KP（编译输入的 qualifying KP）
    source_unit_ids  TEXT NOT NULL DEFAULT '[]',
    source_link_ids  TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：编译时 source_point_ids 上已存在的 verified ActivationLink id
    -- （migration 028）。纯依赖回链元数据，不进编译输入/prompt——呼应
    -- docs/design/wiki-compilation.md 防固化要素"依赖的 ActivationLink"，
    -- 用于页面详情展示和未来生命周期追溯，不驱动重编译判断。
    aliases          TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：概念别名/缩写/口语叫法（migration 029，编译时 LLM 生成，
    -- 只进 wiki index 作检索字段，不属于正文，不参与 citation 白名单）
    trigger_questions TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：该页面能回答的典型问法（migration 029，同上，5-10 条），
    -- 用于弥合用户措辞与页面用词的词汇鸿沟（Wiki = Answer + Retrieval Index）
    compiled_from    TEXT NOT NULL DEFAULT '[]',
    -- JSON 数组：触发编译的 learning_result / report 标识
    prompt_version   TEXT NOT NULL,
    model_name       TEXT NOT NULL,
    compiled_at      DATETIME,
    published_at     DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_status  ON wiki_pages(status);
CREATE INDEX idx_wiki_concept ON wiki_pages(concept_id);

CREATE TABLE wiki_revisions (
    revision_id  TEXT PRIMARY KEY,
    page_id      TEXT NOT NULL REFERENCES wiki_pages(page_id),
    content      TEXT NOT NULL,
    -- 该版本完整正文快照
    reason       TEXT NOT NULL,
    -- 编译/重编译原因（含触发来源描述）
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_rev_page ON wiki_revisions(page_id);
```

Bleve 新增 `wiki` 索引（使用 `wiki_brain` analyzer）：

```text
写入字段：page_id、title、content、aliases、trigger_questions、concept_id、status
（aliases / trigger_questions 拼接为文本参与分词检索，放宽召回入口；
  误命中由直答阶段的 sufficient 判断兜底，见步骤 4）
只索引 status=published 的页面；发布时写入，archived / needs_recompile 时删除。
```

## 实现步骤

### 步骤 1：候选产生（Study 侧，已定义）

Study 报告的 wiki_candidates（recommendation=ready）写入 `learning_results(action=wiki_candidate, status=pending_confirm, object_id=concept_id)`，见 study.md 步骤 6。

### 步骤 2：人工确认与编译触发

```text
POST /wiki/compile
  请求：{ "concept_id": "...", "page_type": "topic|concept",
          "result_id": "..." }
  处理：
    1. result_id 对应的 pending_confirm wiki_candidate 置 applied
       （confirmed_by=manual）；无 result_id 时允许直接指定 concept_id
       编译（调试用途，记录 warn）；
    2. 同 concept_id 已有非 archived 页面 → 拒绝（409），
       重编译走步骤 5 流程；
    3. 收集编译输入（见步骤 3），同步执行编译（编译时长可接受，
       不进异步队列；HTTP 超时上限相应放宽到 120s）；
    4. 成功 → 页面 status=draft，写首条 wiki_revisions。
  响应：{ page_id, status: "draft", title }
```

### 步骤 3：编译输入与 Prompt

**输入收集**：

```text
qualifying KP：该 concept 下 confident_count ≥ study.wiki_confident_min
  且 lifecycle=current 的 KP（与 Study 候选口径一致）；
KU 正文：qualifying KP 所属 KU 按行号切片（单页输入合计 ≤
  wiki.compile_max_chars，默认 12000 rune，超出按 confident_count
  降序截取 KP）；
KPN 关系：qualifying KP 之间的 relations（含 cross）；
knowledge_gaps：question_terms 与该 concept 名称/KP 内容有词项重合的
  gap 条目（作为"待验证点"素材）。
```

**Prompt 文件**：`config/prompts/wiki_compile.md`

```
根据以下知识点和原文材料，编译一个主题的 Wiki 页面。

要求：
1. 只使用提供的材料，不引入材料之外的信息；
2. 页面结构固定为四节：## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源；
3. 稳定结论中每条论断末尾以 [point_id] 标注依据的知识点，
   只能使用材料中出现的 point_id；
4. 材料之间存在张力或 gap 列表非空时，写入"待验证点"，不要强行调和；
5. "依赖来源"列出所用知识点所属的知识单元主题；
6. 额外输出检索触发信息：aliases（该概念的别名、缩写、常见口语叫法）
   与 trigger_questions（这个页面能够直接回答的 5-10 个典型问法，
   用提问者的自然措辞而非页面正文用词，覆盖不同问法角度）。

概念：{{concept_name}}（{{concept_description}}）
知识点与原文材料：
{{materials}}
相关知识缺口：
{{gaps}}

按以下 JSON Schema 输出，不输出任何其他内容：
{{json_schema}}
```

示例 JSON：

```json
{ "title": "页面标题", "content": "Markdown 正文", "cited_point_ids": ["..."],
  "aliases": ["..."], "trigger_questions": ["..."] }
```

**编译后校验**（程序执行，同 citation 校验思想）：

```text
cited_point_ids ⊆ 输入 KP 集合，越界的剔除并记录 warn；
content 中的 [point_id] 标注逐一提取，不在白名单内的替换为删除并 warn；
content 非空且包含四个固定小节标题，缺节 → 编译失败按 LLM 失败处理
  （重试一次，仍失败返回 500，不产生页面）；
aliases / trigger_questions 为空或缺失不算失败（记录 warn，存空数组）——
  它们只影响召回宽度，不影响页面正确性；条数超过 wiki.trigger_questions_max
  截断保留前 N 条。重编译时随正文一起重新生成、整体覆盖。
校验通过后：source_point_ids = content 中实际引用的 point_id 并集，
source_unit_ids 反查填入，source_link_ids = 这些 point_id 中 status=verified
的 activation_links.link_id 集合（无对应 verified 链接的 point_id 不计入，
查询失败降级为空数组并记录 warn，不影响编译成功）。
```

**调用参数**：reasoning 模型（页面编译是 V1 唯一的长文生成任务），temperature 0.3，记录 prompt_version / model_name。

`page_type=concept` 与 `topic` 的差别仅在 title 生成提示（concept 页以概念名为题，topic 页允许模型按材料聚合主题命名），prompt 通过 `{{page_type_hint}}` 变量区分，不用两份文件。

### 步骤 4：发布与检索接入

```text
POST /wiki/pages/:id/publish
  仅对 draft / needs_recompile 生效；status=published、
  published_at=now()、写入 wiki index。
  响应：{ page_id, status: "published" }

检索接入（retrieval.md 第 0 层）——直答候选采集，两个入口，均不调 LLM：

  a. 词法入口：对问题分词后查询 wiki index（title/content/aliases/
     trigger_questions 均参与打分；TermQuery status=published 已由索引
     写入策略保证），取分数 ≥ retrieval.wiki_min_score 的页面按分数降序；
  b. 概念入口：问题分词结果与 concepts 表的概念名称做词法匹配
     （精确/包含，不调 LLM），命中概念存在 published 页面
     （wiki_pages.concept_id）→ 该页面直接进入候选，不看 Bleve 分数。

  两入口合并去重：词法命中按分数排序在前，仅概念命中的页面追加在后；
  截取前 retrieval.wiki_max_candidates 个（默认 3）作为直答候选序列。

  直答尝试（按候选顺序逐个执行，最多 wiki_max_candidates 次）：

  Prompt 文件：config/prompts/answer_wiki.md
    输入：question + 页面 title/content；
    要求：只依据页面内容回答，引用页面中的 [point_id] 标注作为 citations；
    输出：{ "content": "...", "citations": ["point_id..."], "sufficient": true|false }

  sufficient=false（该页面覆盖不了此问题）→ 尝试下一候选页面；
    全部候选耗尽仍 false → 回落激活层/慢路径继续；
  sufficient=true → 停止尝试，组装 AnswerResult：path_type=wiki，
    citations 经该页面 source_point_ids 白名单校验，
    evidence_snapshot 记录 { wiki_page_id, cited_point_ids }
    （point_id 可继续反查 KU 与 source_ref，证据回链完整）；
  Wiki 直答典型 1 次 LLM 调用，最坏 wiki_max_candidates 次
    （召回加宽后"正确页面排第二被首名挡住"是主要漏答模式，
    多试代价是轻量调用，漏答代价是整条慢路径，取前者）；
  trace 照常写入（path_type=wiki），不产生激活类事件（见 trace.md 步骤 3）。

  设计取向（对应本方案的准确率分工）：路由层（trigger/概念入口）只负责
  召回宽度，允许误召；准确率由三道既有闸门保证——编译输入的 qualifying KP
  口径（事实层）、sufficient 弃权判断（覆盖层）、citation 白名单（回链层）。
  放宽召回不得以削弱任何一道闸门为交换。
```

### 步骤 5：重编译

```text
标记（两个来源，最终都调 MarkNeedsRecompile(pageID, reason)）：
  a. lifecycle 传导：SetUnitLifecycle 执行后，扫描 published 页面的
     source_point_ids 含受影响 KP → needs_recompile（见 lifecycle.md 步骤 4）；
  b. Study 周期扫描：published 页面的依赖 KP 出现新增 qualifying KP
     （数量比编译时增加 ≥ wiki.recompile_new_kp_min，默认 2）
     → needs_recompile + learning_results(action=recompile_flag)；

MarkNeedsRecompile：status=needs_recompile、从 wiki index 删除
  （旧结论可能失效，宁可回落慢路径也不用可疑页面直答）、记录原因日志；

重编译执行：人工在 Page 确认后 POST /wiki/pages/:id/recompile →
  重新收集输入（口径同步骤 3，qualifying KP 按当前数据重算）→
  编译 → 校验 → 写新 wiki_revisions（reason 含触发来源）→ status=draft，
  等待再次 publish。每次编译均可经 compiled_from / revisions
  追溯到触发它的 learning_result 或 lifecycle 变更。
```

### 步骤 6：HTTP API 汇总

```text
POST /wiki/compile               步骤 2
POST /wiki/pages/:id/publish           步骤 4
POST /wiki/pages/:id/recompile         步骤 5
POST /wiki/pages/:id/archive           status=archived，从索引删除
GET  /wiki/pages                 查询参数：status、concept_id、limit
                                 响应：[{ page_id, page_type, title, status,
                                          concept_id, compiled_at, published_at }]
GET  /wiki/pages/:id             完整字段 + revisions 元信息列表
GET  /wiki/pages/:id/revisions/:rev  单版本正文
```

## 配置项（config.yml: wiki 节，新增）

```yaml
wiki:
  compile_max_chars:      12000
  recompile_new_kp_min:   2
  trigger_questions_max:  10   # trigger_questions / aliases 各自的条数上限
```

## 依赖

```text
基础设施：SQLite、Bleve（新增 wiki 索引）、LLM client（reasoning 模型）
Study：   wiki_candidate 确认流、recompile_flag（study.md 步骤 6）
Lifecycle：SetUnitLifecycle → MarkNeedsRecompile 联动
Retrieval / Answer：Wiki 直答层接入（retrieval.md 第 0 层；
  answer_wiki 路径产出标准 AnswerResult，Trace 无感知差异）
Unit / Trace：编译输入的 KP / KU / 共现 / gap 数据（只读）
```

## 完成标准

```text
候选确认 → 编译 → draft 的链路可走通，页面含四个固定小节；
cited_point_ids 白名单校验生效，越界标注被剔除并记录；
publish 后页面进入 wiki index（含 aliases / trigger_questions 字段），
  同主题问题走 Wiki 直答（典型 1 次 LLM 调用），
  citations 可经 point_id 反查 KU 与来源位置；
措辞与页面正文不重合、但命中 trigger_questions 或概念名的问题
  也能进入直答候选（词汇鸿沟场景）；
首个候选 sufficient=false 时正确尝试下一候选，
  全部候选耗尽后正确回落后续检索层；
依赖 KP 被 superseded 后页面自动 needs_recompile 并退出索引，
  同主题问题回落慢路径；
recompile → 新 revision → 再 publish 的完整生命周期可走通，
  每次编译可追溯触发来源；
archive 后页面退出索引且不可再编译；
fake LLM 下编译、校验失败、直答、回落、重编译路径测试稳定运行。
```
