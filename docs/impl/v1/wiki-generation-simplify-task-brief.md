# 编码任务指令：收缩 Wiki 编译链路（撤回提纲/逐节，恢复两次整页调用）

> 这是一份交给编码会话的任务说明，不是设计文档。设计与实现口径**一律以
> `docs/impl/v1/wiki-generation.md`（已改为简化版）与 `docs/design/wiki.md`
> 为准**，本文件只负责给出现状盘点、改动范围、步骤顺序与验收门。文档与本文件冲突时以
> 文档为准；文档本身有歧义时**停下来问用户**，不要按直觉补全。
>
> 本文件取代 `docs/impl/v1/wiki-generation-p1-task-brief.md`（该文件已过时并于
> 2026-08-03 文档清理时删除）。

## 任务背景（先理解，再动手）

之前一轮编码会话已经按 `wiki-generation-p1-task-brief.md`（重架构版）把 Wiki 概念页
编译实现成了「切面聚类 → 提纲合成（1 次 LLM）→ 逐节生成（N 次 LLM，含并发/重试/降级）
→ 装配」，并新增了 `wiki_page_sections` 持久化表、`OutlineResult`/`OutlineSection`
breaking 类型、`web/index.html` 的对应改动。

复核后确认这套重架构测试门槛过高（单测要覆盖 N 次调用的并发/重试/降级组合状态，而
不是简单的"给定一次 LLM 响应，装配对不对"）、收益（citation 白名单从"整页"收窄到
"本节"）已经被同样已实现的支持度核验（阶段 E，判断结论是否真被材料支持）大部分覆盖。
方案改为：**保留切面聚类这一层结构计算（已实现且质量好，不动），把写作调用收缩回两次
整页 LLM 调用（analyze + compile），材料按切面分组喂给这两次调用，产出的每条结论可选
携带 aspect_id，正文里"展开说明"按切面分三级小标题——但生成本身仍是一次性、一整页**。

这不是从零重写，是从已经跑通的重架构里**拆掉一层**，同时保留最有价值的那一层（切面
聚类）。整个改动应该比原本的 P1 任务小得多。

**必读文档**（先全部读完再动手）：

```text
docs/impl/v1/wiki-generation.md   全文，重点第 2-4、6、8、10-14 节
docs/design/wiki.md               「编译内部三个阶段、两次 LLM 调用」
                                  「为什么不做逐块独立生成」「各层对象的职责区分」
docs/impl/v1/wiki.md              步骤 2、3、5（现行 topic 页走的就是这套扁平两次调用，
                                  可以直接抄它的模式）
CLAUDE.md                         「V1 关键设计决策」全部条目
```

## 第 0 步：确认基线

跑一遍 `go build ./... && go test ./...`，记录当前通过/失败状态（应该全绿，因为上一轮
会话交付时是绿的）。改动过程中如果发现基线本身有失败的测试，停下来向用户确认，不要
顺手一起改掉。

## 现状盘点（已核对，直接引用文件/函数名，不要重新探索一遍）

**保留、不动**（这些代码只做结构计算或与"逐节 vs 整页"无关）：

```text
internal/foundation/graph/louvain.go                 Louvain 通用实现，不动
internal/wiki/aspect.go                               全部保留：buildAspects /
  BuildAspectEdges / ClusterAspects / SuggestAspectName / splitOversized /
  splitByUnit / mergeUndersized / edgeKeyPair / jaccard 等
internal/wiki/service.go 中这些函数：gatherMaterials / matchingGaps /
  filterClaims / filterTensions / claimsWhitelist / hasRequiredSections /
  filterContentTags / sourceUnitsForPoints / marshalUncoveredPoints /
  marshalConditions / verifyClaims（阶段 E）/ Selfcheck / publish（阶段 G）/
  Archive / MarkNeedsRecompile / cascadeToParentTopics /
  GetActivePageByEntryID / ScanForNewQualifyingKP /
  NotifyPointsLifecycleChanged / TryDirectAnswer 及其调用链 / indexPage /
  readUnitContent 等——这些已经是 page-type 无关的共享工具，topic.go 的扁平
  一次性编译（AnalyzeTopic/CompileTopic）本来就在用同一批函数，本次改动只是
  让概念页也走同一模式，不需要新写这些工具函数。
internal/wiki/topic.go                                完全不动，作为本次改动的
  参照模板（AnalyzeTopic/analyzeTopicClaims/CompileTopic/compileTopicContent/
  hasRequiredTopicSections 就是"扁平两次调用"已经在跑的实现）
internal/wiki/store.go 中除 PageSection 相关外的全部函数                不动
config/prompts/wiki_claim_verify.md                   不动（阶段 E）
config/prompts/answer_wiki.md                          不动（阶段 G 复用）
config/prompts/wiki_topic_analyze.md / wiki_topic_compile.md            不动
internal/foundation/db/migrations/039_wiki_quality.sql                  不动
internal/study/*                                       内聚度门槛部分不动
  （study 侧只消费 aspect.go 的 Louvain 结果，与 outline/section 无关）
```

**需要删除**（提纲/逐节这层架构的产物）：

```text
internal/wiki/outline.go                     整个文件删除
internal/wiki/outline_test.go                整个文件删除
internal/wiki/sectioncompile.go              整个文件删除
internal/wiki/sectioncompile_test.go         整个文件删除
config/prompts/wiki_outline.md               删除
config/prompts/wiki_section_compile.md       删除
internal/wiki/store.go 中：ReplacePageSections / ListPageSections
  两个函数，及其操作的 SQL（DELETE/INSERT/SELECT ... wiki_page_sections）
internal/wiki/types.go 中：OutlineResult / OutlineSection / PageSection
  三个类型定义整体删除
```

**需要修改**：

```text
internal/foundation/db/migrations/040_wiki_sections.sql
  删除 CREATE TABLE wiki_page_sections 与其 CREATE INDEX idx_wps_page 两条语句，
  保留两条 ALTER TABLE wiki_pages ADD COLUMN（summary、aspects）不变。
  注意：这个 migration 文件目前还没有随产品对外发布过（V1 仍在实现中），
  直接编辑文件内容、不新增一个"回滚" migration——与仓库里其它 V1 阶段的
  migration 编辑方式一致。

internal/wiki/types.go
  - CompileRequest.Outline *OutlineResult 字段删除，改回：
      Claims   []Claim   `json:"claims,omitempty"`
      Tensions []Tension `json:"tensions,omitempty"`
    （与 AnalyzeResult 的字段名/形状对齐，CompileRequest 和 AnalyzeResult
    从此可以合并成同一个类型也可以分开，选哪个不重要，选实现起来改动最小的）
  - AnalyzeResult 上原有注释提到"concept 页用 OutlineResult"，这句删掉，
    改为 AnalyzeRequest/AnalyzeResult 现在对 concept 和 topic 页统一使用。
  - Claim 结构体新增一个 *可选* 字段：
      AspectID string `json:"aspect_id,omitempty"`
    这是本次唯一对现有 JSON 形状的新增字段，非破坏性。
  - PageAspect 结构体保留（用于 wiki_pages.aspects 列），但去掉它依赖
    "从 OutlineSection 生成"的假设——改由 aspect.go 的 Aspect
    {AspectID, SuggestedName, PointIDs} 直接映射：
      Heading = SuggestedName（或 LLM 在 claims 里对该切面的描述，取更简单的
      那种实现，不强求 LLM 参与切面标题最终定稿）
      QuestionTypes = 该 aspect PointIDs 对应的真实 confident 问法，按
      wiki.aspect_questions_max 截断（复用阶段 A 已有的取样逻辑，不需要
      新查询——outline.go 删除前它已经算过一次类似的 observed_questions，
      抄那段逻辑到新的装配函数里即可）。

internal/wiki/service.go
  - Analyze(ctx, req AnalyzeRequest) (*OutlineResult, error)
    改回 Analyze(ctx, req AnalyzeRequest) (*AnalyzeResult, error)，
    内部不再调用 s.generateOutline，改为调用一个新的
    s.analyzeClaims(ctx, conceptID) ([]Claim, []Tension, error)
    —— 模仿 topic.go:analyzeTopicClaims 的结构，但：
      材料不是 gatherMaterials 的平铺文本，而是先调用 aspect.go 的
      buildAspects(qualifying) 得到 []Aspect，再把材料按切面分组渲染成
      文本（outline.go 删除前的 renderAspectsText 函数值得抄一份逻辑过来，
      不必一字不改，但不要重新发明格式）；
      Prompt 换成 config/prompts/wiki_analyze.md（新建，见下）；
      输出的 claims[] 直接用（帶 aspect_id），不再有 sections[] 这一层；
      aspect_id 的兜底纠正逻辑（3.4 节）在这一步做：LLM 给的 aspect_id
      不在切面集合里 -> 按 cited_point_ids 落在哪个切面里做兜底。
  - flattenOutlineClaims 函数删除（不再需要，claims 本身就是扁平的）。
  - Compile(ctx, req CompileRequest) (*Page, error)
    不再处理 req.Outline，改为处理 req.Claims/req.Tensions（缺省时内部跑
    s.analyzeClaims，与 wiki.md 步骤 2 现状约定一致：
    "先 analyze 再原样确认"）。
    内部不再调用 compileSections，改为调用一个新的
    s.compileContent(ctx, conceptID, pageType string, claims []Claim,
      tensions []Tension) (*compiledContent, error)
    —— 模仿 topic.go:compileTopicContent 的结构（单次 LLM 调用产出整页
    Markdown），但：
      输入除 claims/tensions 外，还要把切面清单（阶段 B 重新算一遍，或者
      从 analyzeClaims 阶段透传下来，两种做法都可以，选实现起来更简单的
      那种——注意 Compile 允许 req.Claims 非空、跳过 s.analyzeClaims 的路径，
      这种情况下切面清单需要重新计算一次，不能假设一定有上一步的中间结果）；
      Prompt 换成 config/prompts/wiki_compile.md（新建，见下）；
      LLM 直接输出整页 Markdown 正文（不是 JSON），复用 hasRequiredSections
      做五节校验（新增"## 摘要"断言）、filterContentTags 做白名单收敛
      （白名单 = 全部 qualifying KP，不按切面收窄）；
      "## 摘要" 一节的正文内容，程序从生成结果里摘出来存入 Page.Summary
      （字符串匹配 "## 摘要" 到下一个 "## " 之间的文本，trim 空白）；
      aliases/trigger_questions 仍然程序化（6.3 节，已实现的部分照抄，
      这块 sectioncompile.go 里已经有实现，迁移过来即可）；
      aspects 字段：按上面 types.go 那条说明，从 aspect.go 的 []Aspect
      映射出 []PageAspect 存入 Page.Aspects。
    返回的 compiledContent 结构体可以直接复用 sectioncompile.go 里
    compiledContent 的字段定义（拷贝过来，去掉 sections []compiledSection
    这个字段，因为不再有 per-section 产物）。
  - sectionsToRows 函数删除（不再需要）。
  - Compile / Recompile 里所有 s.store.ReplacePageSections(...) 调用删除。
  - Recompile(ctx, pageID, reason, compiledFrom) 里概念页分支：
    outline, err := s.generateOutline(...) + s.compileSections(...)
    改为 claims, tensions, err := s.analyzeClaims(...) +
    s.compileContent(ctx, conceptID, page.PageType, claims, tensions)。
    行为口径不变（wiki.md 步骤 5：整页重新生成，不做按节增量——见
    wiki-generation.md 第 8 节，这条已经在文档里明确不做了，不需要额外处理）。
  - verifyClaims 的调用点：claims 参数直接传 analyzeClaims 返回的
    []Claim（不再需要 flattenOutlineClaims），其余逻辑不变。

internal/wiki/handler.go
  - 逐个检查 analyze()/compile() 两个 HTTP handler：它们目前大概率只是
    json.Unmarshal 请求体到 AnalyzeRequest/CompileRequest、调用 service、
    json.Marshal 响应，本身不太可能包含 Outline 专属逻辑。改完 types.go/
    service.go 后重新编译，把编译器报出的类型不匹配错误逐个修掉即可，
    不需要预先假设 handler.go 里有什么要主动改的地方。

config/prompts/wiki_analyze.md（新建，替代已删除的 wiki_outline.md）
  按 wiki-generation.md 第 3.3 节给出的内容创建，输出 schema 严格对齐
  types.go 的 AnalyzeResult（claims[]：summary/cited_point_ids/aspect_id，
  tensions[]：description/related_point_ids）。格式约定（frontmatter +
  ## System / ## User / ## Schema 三段）比照现存的
  config/prompts/wiki_topic_analyze.md 抄。

config/prompts/wiki_compile.md（新建，替代已删除的 wiki_section_compile.md）
  按 wiki-generation.md 第 4.2 节给出的内容创建。这是产出 Markdown 正文
  （不是 JSON）的 prompt，格式比照 config/prompts/wiki_topic_compile.md
  （现存文件，先读一遍它的 System/User 段与变量占位写法，两者定位一致：
  都是"给定已确认的结论，一次性写成整页正文"）。

web/index.html
  - analyzeWikiCandidate() 函数（约第 4090-4111 行）：
    r.data 目前按 OutlineResult 解析（o.sections / o.headline_claims）。
    改回按 AnalyzeResult 解析（o.claims / o.tensions），渲染成一个平铺的
    结论列表 + 张力列表即可（可以顺带展示 claim.aspect_id，不强制）。
    S.wikiOutlineCache 缓存机制保留，但缓存的是 {claims, tensions} 而不是
    整个 outline 对象——变量名可以顺手改成 wikiAnalyzeCache 更准确，
    不改也不影响功能。
  - compileWikiCandidate() 函数（约第 4113-4128 行）：
    POST /wiki/compile 的请求体从 { ..., outline } 改为
    { ..., claims, tensions }（从缓存里取）。
  这是本次唯一必须同步改的前端逻辑；页面详情渲染（renderWikiPageDetail /
  pageBodyHtml 等）不依赖 sections[]，不用动。
```

## 测试改造

```text
internal/wiki/testhelpers_test.go
  setupTestService 里：
    fake.SetResponse("wiki_outline.md", ...) 和
    fake.SetResponse("wiki_section_compile.md", ...) 两行改成
    fake.SetResponse("wiki_analyze.md", ...) 和
    fake.SetResponse("wiki_compile.md", ...)；
    响应内容 validOutlineOutput / validSectionCompileOutput 两个常量
    改造成扁平的 AnalyzeResult JSON（claims/tensions，可选 aspect_id）与
    一段直接 Markdown 正文（含五节标题、[p1]/[p2] 标注），仿照
    internal/wiki/topic.go 相关测试里对 wiki_topic_analyze.md /
    wiki_topic_compile.md 的假响应写法；
    config.WikiConfig{...} 里删掉 OutlineMinSections/OutlineMaxSections/
    SectionMaxChars/SectionConcurrency 四个字段（config.go 里对应字段
    也要删，见下）。

internal/foundation/config/config.go
  WikiConfig 结构体删除：OutlineMinSections / OutlineMaxSections /
  SectionMaxChars / SectionConcurrency 四个字段及其注释块。
  AspectQuestionsMax 等阶段 B 字段保留不动。

config/config.yml
  删除 wiki 节下的 outline_min_sections / outline_max_sections /
  section_max_chars / section_concurrency 四行（连同它们的注释块）。

internal/wiki/service_test.go
  搜索所有引用 outline/section 相关符号的测试（generateOutline /
  compileSections / OutlineResult / OutlineSection / validOutlineOutput /
  validSectionCompileOutput / missingSectionsOutlineOutput /
  hallucinatedCiteSectionCompileOutput 等），按新的
  analyzeClaims/compileContent 语义重写断言，不要跳过或删除测试用例本身
  代表的场景（例如"claims 全空重试一次仍失败 500"这类场景要保留，
  只是不再通过 outline 相关类型触发）。

回归检查（这些测试预期完全不受影响，改完后应仍然通过，不需要修改）：
  internal/wiki/topic_test.go（topic 页逻辑本次未改动）
  internal/study/*_test.go（内聚度门槛只消费 aspect.go 结果）
```

## 验收标准

```text
go build ./... 与 go test ./... 全绿；
grep -rn "OutlineResult\|OutlineSection\|wiki_page_sections\|compileSections\|generateOutline" internal/ web/ 无匹配（确认清干净）；
POST /wiki/compile/analyze 响应体是 { claims: [...], tensions: [...] }
  （claims[] 每项可能带 aspect_id），不是 { sections: [...] }；
POST /wiki/compile 请求体接受 { claims, tensions }（可选），不接受 outline；
Compile/Recompile 全链路每次概念页编译固定发起 2 次 LLM 调用
  （analyze 1 次 + compile 1 次），可以在测试里用 fake LLM 的调用计数断言；
生成的正文包含五节标题（## 摘要 / ## 稳定结论 / ## 展开说明 / ## 待验证点 /
  ## 依赖来源），且"展开说明"下按切面分出多个 ### 三级标题；
阶段 E（claim verify）、阶段 G（selfcheck/publish 质量门）行为不变，
  对应测试无需改动即可通过；
wiki_pages.summary / wiki_pages.aspects 两列仍然正常写入。
```

## 完成后

跑一遍 `go vet ./...`，并把这次改动的 diff 摘要（新增/删除文件清单、核心函数改名
对照表）贴回来，我会据此更新 `docs/impl/v1/wiki-generation.md` 第 13 节的落地状态
（从"待收缩"改成"已实现"）。
