# PDF 移植 Part 6：MPP 噪声清理、正文弱合并与行分类

本文覆盖 Java 源码包 `com.fileview.convert.markdown.mpp` 中除 `MarkdownHeadingStage` 与 TOC/heading-validation 类（Part 5 负责）之外的全部类：

- `MarkdownPostProcessorPipeline`（编排入口）
- `MarkdownNoiseCleanupStage`（阶段 1：噪声清理）
- `MarkdownBodyMergeStage`（阶段 3：正文弱合并，主控）
- `MarkdownWeakMergeHeuristics`（阶段 1、3 共用的弱合并判据库）
- `RepeatedBoilerplateLineRemover`（扫描件重复页眉页脚剔除）
- `MarkdownLineClassifier`（行分类：BLANK/FENCE/HEADING/LIST_ITEM/DATE/TABLE/QUOTE_OR_RULE/PREFORMATTED/NATURAL_TEXT）

依赖但由 Part 5 规范其内部结构的类型：`MarkdownPipelineContext`、`MarkdownLineRange`、`MarkdownLineKind`、`MarkdownTitlePattern`、`MarkdownPipelineLineUtils`、`MarkdownPipelineStage`、`MarkdownHeadingStage`（仅用到 `demoteHashHeadingToBold`）。依赖 Part 4 的 `HeadingLevelPrefixHeuristics`（仅用到 `classifyPrefixKey`）。依赖同顶层包（非 mpp 子包，可能属于其它 Part，需人工核对，下文按“外部依赖”单独列出）的 `ChapterTocLineRemover`、`ChapterTocCatalog`、`HeadingSuppressHeuristics`、`ListGuideHeuristics`、`ChapterReferenceHeuristics`、`HeadingPatternQualityHeuristics`、`PdfToMarkdown.isStandaloneChineseDateLine`。

---

## Go regexp 兼容性预警

Go 标准库 `regexp`（RE2 引擎）不支持环视（lookahead `(?=...)`、negative lookahead `(?!...)`、lookbehind `(?<=...)`、negative lookbehind `(?<!...)`）。以下是本 Part 范围内所有使用环视的正则，逐条给出替代实现思路。

### 1. `RepeatedBoilerplateLineRemover.LIST_ITEM_PREFIX`
```java
Pattern.compile("^[(（]?\\d+([)）、]|[.．](?!\\d))\\s*")
```
- 用途：识别有序列表行首标记（如 `1、` `3)` `（4）` `1. `），但要排除小数点式多级编号（如 `1.2.2`），即点号后不能紧跟数字。
- 环视点：`[.．](?!\\d)` —— 点号后面不是数字才匹配。
- Go 替代方案：拆成两步。
  1. 用不含 negative lookahead 的正则匹配前缀候选：`^[(（]?\d+([)）、]|[.．])\s*`，并分别捕获匹配到的分隔符是 `)`/`）`/`、` 还是 `.`/`．`。
  2. 若分隔符是 `.`/`．`，额外检查该分隔符之后紧跟的字符（用 Go 字符串索引，而非正则）：若该字符是 ASCII 或全角数字，则判定为不匹配（视为小数点编号，跳过 boilerplate 剥离逻辑），否则视为匹配成功。
  - 实现上建议直接手写一个小函数 `matchListItemPrefix(s string) (matched bool, prefixLen int)`，用 `regexp` 找到 `^[(（]?\d+[)）、.．]` 的候选，再用普通字符判断代替 lookahead，比硬凑 RE2 更清晰可靠。

### 2. `MarkdownLineClassifier` 中不含环视的正则较多，但需要重点核对以下几个是否隐藏环视——逐一检查后确认：
- `SHELL_FLAG`、`PATH_OR_DEVICE`、`KEY_VALUE`、`IP_OR_PORT`、`VERSION_PACKAGE`、`HASH_OR_UUID`、`PERMISSION_LINE`、`LOG_LINE`、`STACK_LINE`、`SQL_START`、`CODE_START`、`CONFIG_LINE`、`NATURAL_PREFIX`、`COMMAND_LEAD`、`COLUMN_GAP`、`IPV4_HOST_PORT_PREFIX`、`SENTENCE_FINAL_PUNCTUATION`、`PROSE_FUNCTION_WORDS_EN`、`PROSE_SENTENCE_END` —— 均**不含**环视语法，均可直接照抄到 Go `regexp`（注意 Go 语法上 `\\b`、`\\p{ASCII}` 等元字符 RE2 均支持，`Pattern.CASE_INSENSITIVE` 用 Go 里的 `(?i)` 前缀或 `regexp.MustCompile("(?i)...")`）。
- `isNumericOutlineBoundaryLine` 中的正则：
  ```java
  s.matches("^\\d+(?:[.．]\\d+)+(?:[.．])?(?![.．\\d-]).*")
  ```
  含 negative lookahead `(?![.．\\d-])`。
  - 用途：判断字符串形如 `1.2.3` 这种多级数字大纲编号是否已经结束（后面不能再紧跟 `.`/`．`/数字/`-`，否则说明还没到编号边界，例如 `1.2.3-4` 或 `1.2.34` 不应在此处切断)。
  - Go 替代方案：先用正则匹配不带环视的前缀部分 `^\d+(?:[.．]\d+)+[.．]?`，拿到匹配结束位置 `end`；再手动检查 `s[end]`（若存在）是否属于集合 `{'.', '．', '-'}` 或是一个数字字符（ASCII 或全角 `０-９`）——如果是，则整体判定为不匹配；否则判定为匹配（等价于原来的 `.*` 部分总能匹配剩余任意内容，因此只需保证之前的前缀匹配 + 边界字符检查即可，不需要真正跑 `.*`）。

### 3. `MarkdownWeakMergeHeuristics` 正则一览
逐条核查以下正则均不含环视，可直接照搬：`LIST_SCOPE_ITEM_CANDIDATE`、`INVALID_SLUG_CHARS`、`DASH_RUN`、`COLON_SPLIT`、`VERB_SINGLE_END`、`TOC_HEADING`、`TOC_MD_LINK_LINE`、`TOC_PAGED_LINE`、`HORIZONTAL_RULE`、`OCR_MARGIN_ATTACHMENT_LABEL`、`OCR_DOC_REFERENCE_YEAR`、`OCR_DOC_REFERENCE_CN_SEQ`、`OCR_DOC_REF_SEQ_FRAGMENT`、`OCR_COPY_NUMBER`、`OCR_GOVERNMENT_TITLE_TERMINAL`、`OCR_RECIPIENT_ORG_CHAIN`、`OCR_BODY_OPENING`、`ATTACHMENT_ITEM_LINE`、`DOCUMENT_COVER_TITLE_LINE`。其中 `LIST_ITEM_LINE` 引用了 `MarkdownPipelineLineUtils.NUMERIC_DOTTED_OUTLINE_PREFIX` / `NUMERIC_OUTLINE_BOUNDARY`（Part 5 定义，需人工核对 Part 5 文档中这两个片段是否含环视——若含，随同一并应用相同的“拆分匹配+手动边界检查”策略）。

**注意**：`MarkdownPipelineLineUtils`（Part 5 负责）里可能存在的 `ATTACHMENT_ANCHOR`、`ATTACHMENT_ITEM_LINE`（本文件里另有一份同名局部字段，见下方"数据结构"一节区分）、`HEADING_LINE`、`TABLE_SEPARATOR`、`NUMERIC_DOTTED_OUTLINE_PREFIX`、`NUMERIC_OUTLINE_BOUNDARY`、`EMBEDDED_LEVEL2_PREFIX`、`NUM_LEVEL2_PREFIX`、`COLON_SPLIT` 等正则的环视情况本文档不做规范（Part 5 负责），但**本 Part 的 Go 实现在调用它们时必须使用 Part 5 提供的 Go 版本**，不要自行重新定义。

### 汇总
本 Part 范围内共发现 **2 处**需要 lookahead/lookbehind 工作绕过的正则：
1. `RepeatedBoilerplateLineRemover.LIST_ITEM_PREFIX`（negative lookahead `(?!\d)`）
2. `MarkdownLineClassifier.isNumericOutlineBoundaryLine` 内联正则（negative lookahead `(?![.．\d-])`）

两处思路一致：正则只匹配到环视之前的部分，环视判定改为对匹配结束位置之后的单个字符做手动字符集合检查。

---

## MarkdownPostProcessorPipeline（编排入口）

### 职责
四阶段流水线的顶层入口，管理 `MarkdownPipelineContext` 生命周期，提供多种便捷重载（内存字符串处理、文件处理）。

### 数据结构
- 无本类特有类型；持有 `List<MarkdownPipelineStage> stages`（不可变，`List.copyOf`）。
- Go 对应：`type Pipeline struct { stages []PipelineStage }`，`stages` 用普通 slice（Go 没有强制不可变，构造时复制一份即可）。

### 算法：`defaults()`
1. 创建长度为 4 的阶段列表。
2. 依次追加：`NoiseCleanupStage{}`、`HeadingStage{}`（Part 5）、`BodyMergeStage{}`、`TocEmitStage{}`（Part 5）。
3. 返回包装后的 `Pipeline`。

Go 对应函数签名：`func Defaults() *Pipeline`。

### 算法：`run(markdown, generateToc, scannedSource)`
1. 调用 `MarkdownPipelineContext.create(generateToc, scannedSource)` 创建上下文（Part 5 提供的构造函数）。
2. 调用 `context.initLinesFromMarkdown(markdown)`（Part 5）按换行符切分输入为行列表并存入 context。
3. 若 `context.inputBlank()`（Part 5，判断输入是否全为空白），直接返回空字符串 `""`。
4. 依次对 `stages` 中的每一个阶段调用 `stage.apply(context)`（阶段按顺序执行，后一阶段读取前一阶段写入 context 的结果，通过 `context` 进行副作用式传递，不使用函数返回值传递）。
5. 调用 `context.resultMarkdown()`（Part 5）取回处理后的字符串。
6. 若结果为 `nil`/`null`，返回 `""`；否则返回该结果。

Go 对应函数签名：`func (p *Pipeline) Run(markdown string, generateToc bool) string` 与三参数重载 `RunScanned(markdown string, generateToc, scannedSource bool) string`（Go 无重载，需要用不同函数名或可选参数模式，例如统一为 `Run(markdown string, opts RunOptions) string`）。

### 算法：`processFile` 系列（文件 I/O 便捷方法）
1. 三个重载最终都归约到 4 参数版本 `processFile(input, output, generateToc, scannedSource)`。
2. 校验 `input`、`output` 非空（Go 中用普通 nil/空字符串检查或直接省略，因为 Go 习惯上路径类型不为指针）。
3. 用 UTF-8 读取 `input` 路径的全部内容为字符串 `raw`。
4. 调用 `process(raw, generateToc, scannedSource)` 得到结果字符串。
5. 用 UTF-8 把结果写入 `output` 路径（覆盖写）。
6. 出错（I/O 错误）向上传播（Java 用 `throws IOException`；Go 用返回 `error`）。

Go 对应：`func ProcessFile(input, output string, generateToc, scannedSource bool) error`。

### 算法：`process(markdown, generateToc, scannedSource)`（静态便捷方法）
1. 调用 `Defaults()` 构造一次性流水线实例。
2. 调用其 `Run` 方法并返回结果。

Go 对应：`func Process(markdown string, generateToc, scannedSource bool) string`。

### 与后续阶段的耦合说明
- `MarkdownPostProcessorPipeline` 本身不含业务逻辑，只是把 4 个阶段串起来。移植时应保持这个顺序**严格不变**：NoiseCleanup → Heading → BodyMerge → TocEmit。
- Go 移植建议：让 `MarkdownPipelineContext` 成为一个可变的结构体指针，各阶段的 `Apply(ctx *PipelineContext)` 方法直接修改其字段（`ctx.Lines`、`ctx.ChapterTocCatalog` 等），与 Java 版本的“通过 context 副作用传递”语义完全一致，不要重构成纯函数式传递返回值（否则各阶段之间大量的“上一阶段产出的作用域/索引集合”会不好对齐字段名）。

---

## MarkdownNoiseCleanupStage

### 职责
标题识别（Heading 阶段）之前的第一遍净化，涵盖：
1. 解包 ` ```markdown ` 围栏（模型直接原样吐出一层多余围栏包裹全文的情况）。
2. 扫描件来源时剥离重复页眉页脚（委托 `RepeatedBoilerplateLineRemover`）。
3. 剔除页码行。
4. 规范化嵌入行内的二级编号断行（`splitEmbeddedLevel2AfterChapter`）。
5. 合并被拆断的公文/机构标题续行（`mergeChapterTitleContinuationLines`）。
6. 规范化粘连的章节标题行（`normalizeGluedChapterHeadingLines`）。
7. 拆分嵌入的中文分节标题（委托外部类 `ChapterTocLineRemover.splitEmbeddedCnSectionHeadings`）。
8. 解析并剥离 PDF 目录页行，写入 `ChapterTocCatalog` 快照。
9. 探测“附件清单”“列举引导”等非标题作用域，压平其中的 `#` 标记为加粗。
10. 对被 `HeadingSuppressHeuristics` 判定应降级的标题行做降级处理。

### 常量与正则

| 名称 | 定义 | 说明 |
|---|---|---|
| `LEVEL2_BOUNDARY_CHARS_BEFORE` | 字符串常量 `"。！？；：，、）)】]"` | “1.2”级编号只应紧跟在这些边界字符（或行首）之后才可信，避免把 `月工资/21.75` 这类数值误判为编号起点 |

本类没有独立 `Pattern` 常量字段；主要复用 `MarkdownPipelineLineUtils` 提供的 `EMBEDDED_LEVEL2_PREFIX`、`NUM_LEVEL2_PREFIX`、`HEADING_LINE`、`PAGE_NUMBER_LINE`、`ATTACHMENT_ANCHOR`、`ATTACHMENT_ITEM_LINE`、`ATTACHMENT_LIST_MAX_SCOPE_LINES`、`COLON_SPLIT`（Part 5 规范）。

### 数据结构
```java
public record PageNumberRemovalResult(List<String> lines, Set<Integer> removedOutputAnchors) {}
```
- Go 对应：
```go
type PageNumberRemovalResult struct {
    Lines               []string
    RemovedOutputAnchors map[int]struct{} // 或 IntSet 类型
}
```
`removedOutputAnchors` 记录的是**输出行列表**（剔除页码行之后）中，页码行原本所在位置的锚点索引集合——即 `out.size()`（当前累积的输出行数）在剔除该页码行时刻的值。用于后续阶段判断“这个位置曾经是页码/分页边界”，从而允许跨这个边界做特殊的空行合并判断（见 `MarkdownBodyMergeStage.isPageBoundaryGap`）。

### 算法：`apply(context)`（主流程）
1. `context.setLines(unwrapPairedMarkdownFences(context.lines()))` —— 解包成对的 ` ```markdown ` 围栏。
2. `lines := context.lines()`。
3. 若 `context.scannedSource()` 为真：`lines = RepeatedBoilerplateLineRemover.strip(lines)`。
4. `pageRemoval := removePageNumberLines(lines)`；`lines = pageRemoval.lines()`。
5. `lines = normalizeStructuralLineBreaks(lines)`。
6. `lines = mergeChapterTitleContinuationLines(lines)`。
7. `lines = normalizeGluedChapterHeadingLines(lines)`。
8. `lines = ChapterTocLineRemover.splitEmbeddedCnSectionHeadings(lines)`（外部依赖，非本 Part 范围，直接调用其 Go 版本）。
9. `chapterTocCatalog := ChapterTocCatalog.parse(lines)`（外部依赖）。
10. `lines = ChapterTocLineRemover.stripLines(lines)`（外部依赖）。
11. `attachmentScopesForMerge := detectAttachmentListScopes(lines)`。
12. `listGuideScopes := detectListGuideScopes(lines)`。
13. `attachmentListScopes := detectAttachmentListScopes(lines)`（**注意**：Java 源码在这里对同一份 `lines` **重复调用了一次** `detectAttachmentListScopes`，产生与步骤 11 内容相同但对象不同的第二份列表；步骤 11 的结果单独保存进 `context.attachmentScopesForMerge`，而这里的第二份结果只用于合并进 `nonHeadingScopes`。移植时应保留这个“调用两次”的行为，不要自作主张合并成一次调用共享结果——虽然语义上等价，但为保证行为 1:1 可核对，按原样调用两次，或至少在注释中说明这是刻意的重复调用）。
14. `nonHeadingScopes := append(listGuideScopes, attachmentListScopes...)`（先加入全部 `listGuideScopes`，再加入全部 `attachmentListScopes`）。
15. `flattenHeadingMarkersInScopes(lines, nonHeadingScopes)`（原地修改 `lines`）。
16. `demoteSuppressedHeadingLines(lines)`（原地修改 `lines`）。
17. 把最终 `lines` 写回 `context.setLines(lines)`。
18. `context.setPageRemovedOutputAnchors(pageRemoval.removedOutputAnchors())`。
19. `context.setChapterTocCatalog(ChapterTocCatalog.entriesOnly(chapterTocCatalog.entries()))`（外部依赖，仅取 entries 部分再包装）。
20. `context.setNonHeadingScopes(nonHeadingScopes)`。
21. `context.setAttachmentScopesForMerge(attachmentScopesForMerge)`（用的是步骤 11 的第一份结果）。

### 算法：`unwrapPairedMarkdownFences(lines)`
目的：模型输出有时会把整份 Markdown 包一层 ` ```markdown ... ``` ` 围栏；剥离这层包裹，仅保留内部内容，不影响内部真正的代码块围栏。

1. 若 `lines` 为空，返回空列表。
2. 遍历游标 `i` 从 0 开始：
   a. 取 `trimmed := stripEdgeWhitespace(lines[i])`。
   b. 若 `isOpeningMarkdownFenceLine(trimmed)` 为真（即这一行是形如 ` ```markdown ` 的开栏行，允许前导多个反引号≥3、其后跟空白+`markdown`关键字大小写不敏感+仅剩空白）：
      - 从 `j := i+1` 开始向后找第一个满足 `isClosingBacktickOnlyFenceLine` 的行（纯反引号≥3+尾随空白，不含语言名）。
      - 若找到（在 `j < len(lines)` 范围内）：把 `i+1 .. j-1`（闭区间前开后闭）之间的所有原始行原样追加到输出；游标跳到 `j+1`；跳出内层查找循环，继续外层 while。
      - 若找不到闭合行（`j >= len(lines)`）：把开栏行本身原样加入输出（不剥离，因为没有匹配的收尾），游标 `i++`。
   c. 否则（非开栏行）：原样加入输出，`i++`。
3. 返回输出列表。

辅助函数 `isOpeningMarkdownFenceLine(t)`：
1. `t` 长度需 ≥3 且首字符为反引号，否则 false。
2. 统计开头连续反引号个数 `p`；若 `p < 3` 返回 false。
3. 跳过 `p` 之后的空格/tab。
4. 检查从当前位置起是否**不区分大小写**匹配字符串 `"markdown"`（8 个字符）；不匹配返回 false。
5. 跳过 `markdown` 之后的空格/tab。
6. 若跳过之后正好到达字符串末尾（没有多余内容），返回 true；否则 false。

辅助函数 `isClosingBacktickOnlyFenceLine(t)`：
1. `t` 长度需 ≥3，否则 false。
2. 统计开头连续反引号个数 `p`；若 `p < 3` 返回 false。
3. 跳过之后的空格/tab；若跳到字符串末尾，返回 true；否则 false（即闭合行只能是纯反引号+可选尾随空白，不能带语言名等其它字符）。

### 算法：`normalizeStructuralLineBreaks(lines)`
1. 若 `lines` 为空，原样返回。
2. 对每一行调用 `splitEmbeddedLevel2AfterChapter(line)`，把返回的（可能多行）结果全部追加到输出列表。
3. 返回输出列表。

### 算法：`splitEmbeddedLevel2AfterChapter(line)`
目的：一行文本内部混杂了一个真正应独立成行的“1.2”级编号起点（如某段正文里紧跟着嵌入了下一条编号项，没有真正的换行），需要把它切成两行(或更多行)。

1. `raw := line`（`null` 视为空串）。
2. 若 `raw` 全空白或不包含字符 `"章"`，直接返回 `[raw]`（单元素列表）——**性能短路**：只有含“章”字的行才需要这项处理，因为该功能主要针对“第X章节内嵌下一条编号”的场景。
3. 循环处理 `remain := raw`：
   a. 用 `EMBEDDED_LEVEL2_PREFIX` 正则（Part 5）在 `remain` 上找所有匹配，遍历这些匹配位置 `m.start()`：
      - 跳过 `m.start() <= 0` 的匹配（不能在字符串开头切）。
      - 跳过落在**未闭合 HTML 注释**（`<!-- ... -->`）内的匹配位置（调用 `isInsideOpenHtmlComment`）。
      - 对通过前两项检查的候选位置，调用 `looksLikeEmbeddedLevel2Start(remain, m.start())` 做进一步验证；第一个验证通过的位置记为 `splitAt`，跳出匹配遍历循环。
   b. 若没有找到合法的 `splitAt`（`splitAt <= 0`）：把 `remain.trim()` 追加到输出，跳出整个 while 循环。
   c. 否则：把 `remain[0:splitAt].trim()`（若非空）追加到输出；把 `remain = remain[splitAt:].trim()`；继续循环（`remain` 可能还包含下一个嵌入编号，继续切）。
4. 若输出为空（异常情况），返回 `[raw]`；否则返回输出列表。

辅助函数 `isInsideOpenHtmlComment(text, idx)`：
1. 找到 `idx` 之前最近的 `<!--` 出现位置 `lastOpen`（用 `text.lastIndexOf("<!--", idx-1)`）；若不存在返回 false。
2. 在 `lastOpen` 之后找 `-->` 的位置 `closeAfterOpen`。
3. 若找不到 `-->`，或找到的位置 `>= idx`（说明注释还没在 `idx` 之前闭合），返回 true（`idx` 落在未闭合注释内）；否则返回 false。

辅助函数 `looksLikeEmbeddedLevel2Start(text, idx)`：
1. 若 `text == null` 或 `idx <= 0` 或 `idx >= text.length()`，返回 false。
2. `prev := text[idx-1]`；若 `prev` 是数字、空白、或 `.`，返回 false（说明这是数字/编号内部的位置，不是真正的起点）。
3. 若 `!hasLevel2BoundaryBefore(text, idx)`，返回 false。
4. 取 `suffix := text[idx:]`；用 `NUM_LEVEL2_PREFIX`（Part 5，形如 `\d+\.\d+...` 的多级编号前缀正则）对 `suffix` 做**全串匹配**（`matches`，非 `find`）；返回该匹配结果。

辅助函数 `hasLevel2BoundaryBefore(text, idx)`：
1. `p := idx`；只要 `p>0` 且 `text[p-1]` 是空白字符，`p--`（跳过 `idx` 之前的连续空白，找到真正紧邻的非空白字符）。
2. 若 `p == 0`（跳到了字符串开头），返回 true（行首本身就是边界）。
3. 否则检查 `text[p-1]` 是否属于常量 `LEVEL2_BOUNDARY_CHARS_BEFORE` 中的字符集合，返回该判断结果。

### 算法：`normalizeGluedChapterHeadingLines(lines)`
目的：修复「`# 第一章`」与紧随、无分隔粘连的下一级标题/正文首条被 OCR/解析器错误合并进同一行的情况，或反过来把该断的行重新规整。

1. 若 `lines` 为空返回空列表。
2. 维护 `inFence := false` 状态（跨越 ``` 代码围栏）。
3. 对每一行 `line`：
   a. `trimmed := stripEdgeWhitespace(line)`。
   b. 若 `trimmed` 以 ` ``` ` 开头：翻转 `inFence`，原样加入输出，跳到下一行。
   c. 若当前处于围栏内（`inFence == true`）：原样加入输出，跳到下一行。
   d. 否则（正文态）：
      - 调用外部依赖 `ChapterTocLineRemover.splitGluedChapterHeadingFromFollowingMarker(line)`，得到一个 List；若返回结果长度 >1，说明这一行被成功拆分为多行（章标题 + 后续标记内容各自独立），把这些行全部加入输出，跳到下一行。
      - 否则：调用外部依赖 `ChapterTocLineRemover.stripChapterHeadingTrailingNoiseChar(line)` 去掉行尾噪声字符，得到 `denoised`；再调用外部依赖 `ChapterTocLineRemover.normalizeGluedStructuralChapterHeading(denoised)` 做进一步规整，把结果加入输出。
4. 返回输出列表。

（这两个 `ChapterTocLineRemover.*` 方法属于外部依赖，本 Part 只需保证调用点、参数、返回值语义与上面描述一致；具体实现由负责该类的 Part 规范。）

### 算法：`mergeChapterTitleContinuationLines(lines)`
目的：把被拆成两行的“机关名+关于……”式标题、或“章节前缀+章节名”式标题重新合并为一行。

1. 若 `lines` 为空返回空列表。
2. 维护 `inFence := false`。
3. 用索引 `i` 从 0 遍历到 `lines.size()-1`：
   a. `raw := lines[i]`；`trimmed := stripEdgeWhitespace(raw)`。
   b. 若 `trimmed` 以 ` ``` ` 开头：翻转 `inFence`，加入输出，`i++`，继续。
   c. **规则 A（无 `#` 前缀的“机关名+关于”式标题续行早合并）**：若不在围栏内，且存在下一行（`i+1 < len`）：
      - `nextTrimmed := stripEdgeWhitespace(lines[i+1])`。
      - 若 `trimmed` 不以 `#` 开头、`nextTrimmed` 也不以 `#` 开头，且 `MarkdownBodyMergeStage.shouldEarlyMergeWrappedPlainTitle(trimmed, nextTrimmed)` 为真，且 `nextTrimmed` **不**是 `MarkdownWeakMergeHeuristics.isListLikeLine`，且 `trimmed`、`nextTrimmed` 都**不**是 `MarkdownWeakMergeHeuristics.isDocumentCoverTitleMetadataLine`：
        - 把 `MarkdownBodyMergeStage.mergeWrappedTitleContinuationLines(raw, lines[i+1])` 的结果加入输出；`i += 2`（`i++` 后 for 循环再 `i++`，等效跳过两行）；继续外层循环（`continue`，本轮结束）。
   d. **规则 B（“第 X 章”孤立前缀行 + 独立成行的章节名行 合并）**：若不在围栏内，且存在下一行，且 `ChapterTocLineRemover.isChapterPrefixOnlyLine(trimmed)`（外部依赖，判断本行是否**仅**是“第X章”这样的裸前缀、没有携带章节名）：
      - `nextTrimmed := stripEdgeWhitespace(lines[i+1])`。
      - 若 `ChapterTocLineRemover.isLikelyChapterTitleNameLine(nextTrimmed)`（外部依赖：下一行像是一个章节名），且 `nextTrimmed` **不**匹配 `HEADING_LINE`（Part 5，即下一行本身不是已经带 `#` 前缀的独立标题），且 `nextTrimmed` **不是** `MarkdownWeakMergeHeuristics.isListLikeLine`：
        - 用 `HEADING_LINE` 尝试匹配 `trimmed`；若匹配成功，`prefix := group(1) + " "`（`#` 级别标记+空格），否则 `prefix := ""`。
        - `chapter := normalizeLine(匹配成功时取 group(2)，否则原样 trimmed)`。
        - `title := normalizeLine(nextTrimmed)`。
        - 把 `prefix + chapter + " " + title` 加入输出；`i += 2` 效果；继续外层循环。
   e. 若规则 A、B 均未触发：把 `raw` 原样加入输出，`i++`。
4. 返回输出。

注意：Java 源码里规则 A 和规则 B 都用 `i++` + `continue`（配合 for 循环自身的 `i++`）实现“跳过两行”；Go 实现用 while 循环手动 `i += 2` 即可，语义等价。

### 算法：`demoteSuppressedHeadingLines(lines)`（原地修改）
1. 若 `lines` 为空，直接返回。
2. 对每个索引 `i`：
   a. `raw := lines[i]`；`trimmed := stripEdgeWhitespace(raw)`。
   b. 若 `trimmed` 以 `#` 开头，跳过（已有 `#` 前缀的标题走另一条“抽取已有标题+反证降级”的路径，不在此处理，那条路径属于 Heading 阶段/Part 5）。
   c. 调用外部依赖 `HeadingSuppressHeuristics.shouldSuppressHeadingLine(lines, i)`；若为 false，跳过本行。
   d. 若为 true（该行应被抑制/降级为普通文本）：
      - 若 `trimmed` 匹配 `HEADING_LINE`（Part 5 正则，即使没有 `#` 前缀但形状像标题模式，例如“第一章 XXX”这种纯文本纲要格式）：`norm := normalizeLine(raw)`；若非空，`lines[i] = norm`；继续下一个 `i`。
      - 否则：同样执行 `norm := normalizeLine(raw)`；若非空，`lines[i] = norm`。
   （注：这两个分支的逻辑实际相同，都是 `normalizeLine` 后写回；Java 源码里写成两个 if 块是历史遗留的重复代码，Go 实现可以合并为一次判断，但为避免遗漏隐藏差异，建议保留两个分支的注释说明其语义一致，便于对照原始代码审查。）

### 算法：`flattenHeadingMarkersInScopes(lines, scopes)`（原地修改）
目的：在“附件清单”“列举引导”等非标题作用域内，任何看起来像标题的行（无论是否带 `#`）都应被压平为普通加粗文本，不参与标题层级识别。

1. 若 `lines`、`scopes` 任一为空，直接返回。
2. 对每个索引 `i`：
   a. 若 `!isInAnyScope(i, scopes)`，跳过。
   b. `raw := lines[i]`；`trimmed := stripEdgeWhitespace(raw)`。
   c. 若 `trimmed` 以 `#` 开头，或匹配 `HEADING_LINE`：
      - `norm := normalizeLine(raw)`。
      - 若 `norm` 非空：`lines[i] = "**" + norm + "**"`（转为加粗文本）。
      - 否则：`lines[i] = MarkdownHeadingStage.demoteHashHeadingToBold(raw)`（Part 5 提供的兜底降级函数，处理 `normalizeLine` 归一化后变空的边界情况，例如整行都是标点/空白）。
      - 继续下一个 `i`。
   d. 否则（本行不带 `#` 也不匹配 HEADING_LINE，但可能是纯文本形态的标题模式，如“一、XXX”）：
      - `norm := normalizeLine(raw)`。
      - 若 `norm` 非空 且 `MarkdownTitlePattern.matchFirst(norm) != nil`（Part 5：判断是否匹配任一标题模式）：`lines[i] = norm`（仅归一化写回，不加粗——因为它本来就不带 `#`，只是把编号形式规范化，压平的意义在于后续 Heading 阶段不会再对它做进一步的层级标题提升，因为它已经不再是原始未归一化形式，具体压制机制由 Heading 阶段的字符串匹配决定，本 Part 只需保证这个写回动作发生）。

### 算法：`isInAnyScope(lineId, scopes)`
1. 遍历 `scopes` 中每个 `MarkdownLineRange r`：
2. 若 `lineId >= r.startLine && lineId < r.endLine`，返回 true。
3. 遍历完无匹配，返回 false。
（区间语义：`[startLine, endLine)`，左闭右开。）

### 算法：`detectListGuideScopes(lines)`
1. 若 `lines` 为空，返回空列表。
2. 依次调用外部依赖（属于 `ListGuideHeuristics` 类，不在本 Part 范围，需人工核对负责该类的 Part）：
   - `ListGuideHeuristics.detectListGuideScopes(lines)` —— 每个结果 `int[]{start, end}` 转成 `MarkdownLineRange(start, end)` 加入结果列表。
   - `ListGuideHeuristics.detectChapterListGuideScopes(lines)` —— 同样转换后追加。
   - `ListGuideHeuristics.detectChapterOutlineScopes(lines)` —— 同样转换后追加。
3. 返回合并后的作用域列表（三种来源的作用域全部拼接，不去重不排序）。

### 算法：`detectAttachmentListScopes(lines)`
目的：识别“附件：\n 1. xxx\n 2. xxx”这种应视为正文列表、不应参与标题识别的作用域。

1. 若 `lines` 为空，返回空列表。
2. 对每个索引 `i`：
   a. `anchor := normalizeLine(lines[i])`。
   b. 若 `anchor` 不匹配 `ATTACHMENT_ANCHOR`（Part 5：判断是否是“附件” /「附件：」这类锚点行），跳过。
   c. 初始化 `end := i+1`。
   d. `hasItem := ATTACHMENT_ITEM_LINE.matcher(anchor).matches() || isInlineAttachmentAnchorWithSingleName(anchor)`（锚点本身携带一个内联条目，或本身就格式化为一个清单项）。
   e. `limit := min(len(lines), i + ATTACHMENT_LIST_MAX_SCOPE_LINES)`（Part 5 提供的最大扫描窗口常量）。
   f. 对 `j` 从 `i+1` 到 `limit-1`：
      - `s := normalizeLine(lines[j])`。
      - 若 `s` 为空，`continue`（跳过空行，不中断扫描）。
      - 若 `s` 匹配 `ATTACHMENT_ITEM_LINE`：`hasItem = true`；`end = j+1`；`continue`。
      - 否则（遇到非空、非清单项的行）：若 `hasItem` 已经为真，`break`（作用域到此为止）；否则继续扫描（还没确认过任何条目，容忍锚点后紧跟非清单行——但注意这个分支下 `end` 不会被更新，若循环耗尽也不会产生更长的作用域）。
   g. 若最终 `hasItem` 为真：把 `MarkdownLineRange(i, max(end, i+1))` 加入结果。
3. 返回结果。

### 算法：`isInlineAttachmentAnchorWithSingleName(normalizedAnchor)`
1. 若入参空，返回 false。
2. 用 `COLON_SPLIT`（Part 5，匹配 `:` 或 `：`）在 `normalizedAnchor` 上 `find()`；若找不到冒号，返回 false。
3. `tail := normalizedAnchor[m.end():].trim()`；若 `tail` 为空，返回 false。
4. 返回 `!ATTACHMENT_ITEM_LINE.matcher(tail).matches()`（即冒号后面跟着的内容本身**不**是一个格式化的清单项前缀，说明锚点行内联了一个单独的文件名，如“附件：投标文件模板.docx”）。

### 算法：`removePageNumberLines(lines)`
1. 若 `lines` 为空，返回 `PageNumberRemovalResult([], {})`。
2. 遍历每一行 `line`：
   a. 若 `line` 匹配 `PAGE_NUMBER_LINE`（Part 5 正则）：把当前 `out.size()`（尚未加入本行前的输出长度）加入 `removedOutputAnchors` 集合；不把该行加入输出（丢弃）。
   b. 否则：把 `line` 加入输出。
3. 返回 `PageNumberRemovalResult(out, removedOutputAnchors)`。

### 与外部依赖类的接口清单（本类调用点）
| 外部类型/方法 | 调用处 | 期望语义 |
|---|---|---|
| `ChapterTocLineRemover.splitEmbeddedCnSectionHeadings(lines)` | `apply` 步骤 8 | 拆分嵌入的中文分节标题 |
| `ChapterTocCatalog.parse(lines)` / `.entries()` / `.entriesOnly(entries)` | `apply` 步骤 9、19 | 解析并重建目录快照对象 |
| `ChapterTocLineRemover.stripLines(lines)` | `apply` 步骤 10 | 剥离已解析的目录页行 |
| `ChapterTocLineRemover.splitGluedChapterHeadingFromFollowingMarker(line)` | `normalizeGluedChapterHeadingLines` | 拆分粘连的章标题+后续标记 |
| `ChapterTocLineRemover.stripChapterHeadingTrailingNoiseChar(line)` | 同上 | 去除章标题尾部噪声字符 |
| `ChapterTocLineRemover.normalizeGluedStructuralChapterHeading(denoised)` | 同上 | 规整粘连结构化章标题 |
| `ChapterTocLineRemover.isChapterPrefixOnlyLine(trimmed)` | `mergeChapterTitleContinuationLines` | 判断是否仅为“第X章”裸前缀 |
| `ChapterTocLineRemover.isLikelyChapterTitleNameLine(nextTrimmed)` | 同上 | 判断下一行是否像章节名 |
| `HeadingSuppressHeuristics.shouldSuppressHeadingLine(lines, i)` | `demoteSuppressedHeadingLines` | 判断该行标题是否应被抑制 |
| `ListGuideHeuristics.detectListGuideScopes/detectChapterListGuideScopes/detectChapterOutlineScopes(lines)` | `detectListGuideScopes` | 探测列表引导/章节列表引导/章节大纲作用域 |
| `MarkdownHeadingStage.demoteHashHeadingToBold(raw)` | `flattenHeadingMarkersInScopes` | 兜底：把 `#` 标题行降级为加粗文本 |

---

## RepeatedBoilerplateLineRemover

### 职责
仅在扫描件（OCR）来源时调用，用“全文重复次数”信号剔除页眉页脚（因为扫描件没有可用的版心 bbox 信息按位置过滤）。支持整行完全重复匹配，也支持“重复文本作为行首前缀反复出现、和下一段正文粘连成一行”的部分匹配场景。算法核心是按归一化文本构建字符 Trie，统计每个前缀深度上经过的**不同行数**，达到阈值即视为重复页眉页脚候选，剔除该前缀、保留剩余正文。支持多轮迭代（处理链式粘连的多个页眉页脚）。

### 常量与正则

| 名称 | 值 | 说明 |
|---|---|---|
| `MIN_OCCURRENCES` | `3` | 整行完全重复匹配的最低出现次数阈值 |
| `MIN_OCCURRENCES_PARTIAL` | `MIN_OCCURRENCES + 1 = 4` | 部分匹配（前缀之后仍留有正文）需要更强证据，阈值多 1 |
| `MIN_NORMALIZED_LENGTH` | `6` | 归一化后最短参与匹配的前缀长度 |
| `MAX_NORMALIZED_LENGTH` | `60` | Trie 插入/查询截断的最大深度 |
| `MAX_ROUNDS` | `5` | 最多迭代轮数 |
| `LIST_ITEM_PREFIX` | `^[(（]?\d+([)）、]|[.．](?!\d))\s*`（**含 negative lookahead，见兼容性预警第 1 条**） | 有序列表行首标记，命中的行不参与统计/剔除 |

### 数据结构
```java
private static final class TrieNode {
    final Map<Character, TrieNode> children;
    int count;
}
private static final class Key {
    final String normalized;
    final int[] rawOffsets; // rawOffsets[k] = 归一化第k个字符消费到的原始文本结束偏移（0-based length）
    int rawOffsetAt(int normalizedDepth); // normalizedDepth<=0 返回0，否则 rawOffsets[normalizedDepth-1]
}
```
Go 对应：
```go
type trieNode struct {
    children map[rune]*trieNode
    count    int
}
type lineKey struct {
    normalized string   // []rune 或 string，注意按字符（rune）而非字节处理
    rawOffsets []int
}
func (k *lineKey) rawOffsetAt(depth int) int {
    if depth <= 0 { return 0 }
    return k.rawOffsets[depth-1]
}
```
**注意**：由于中文字符是多字节 UTF-8，Go 实现必须以 **rune** 为单位处理 `normalized` 字符串的索引与 Trie 的 key，而 `rawOffsets` 记录的是**原始 `raw` 字符串中的字节偏移量**（因为最终要做 `raw.substring(rawConsumed)`）——在 Go 中对应 byte offset，用于对原始 `string` 做切片。移植时务必在原始文本按字符遍历时同时维护 rune 位置与 byte 偏移两套索引。

### 算法：`strip(lines)`（对外唯一入口）
1. 若 `lines` 为空，原样返回。
2. `current := lines`。
3. 循环 `round` 从 0 到 `MAX_ROUNDS-1`：
   a. `next := stripOneRound(current)`。
   b. 若 `next` 与 `current`（逐元素）相等，`break`（收敛，不再变化）。
   c. 否则 `current = next`，继续下一轮。
4. 返回 `current`。

### 算法：`stripOneRound(lines)`
1. 对每一行调用 `computeKey(raw, inFenceHolder)`（`inFenceHolder` 是跨行共享的可变布尔标志，用于跳过代码围栏内容），得到 `keys` 列表（元素可能为 `nil`，表示该行不参与统计，如表格行/标题行/围栏内/空行/纯分隔线/列表项行）。
2. 构建 Trie 根节点；对每个非 `nil` 的 `key`，调用 `insert(root, key.normalized)`。
3. 对每一行 `i`：
   a. 若 `keys[i]` 为 `nil`：原样输出该行。
   b. 否则：调用 `matchDepth := deepestConfirmedDepth(root, keys[i].normalized)`。
      - 若 `matchDepth <= 0`：原样输出该行。
      - 否则：`rawConsumed := keys[i].rawOffsetAt(matchDepth)`；`remainder := raw[rawConsumed:].strip()`（Go: `strings.TrimSpace`）；
        - 若 `remainder` 为空：**丢弃**该行（整行都是重复的页眉页脚）。
        - 否则：把 `remainder` 作为该行的新内容输出（剥离了重复前缀，保留剩余正文）。
4. 返回输出行列表。

### 算法：`insert(root, normalized)`
1. `node := root`；`limit := min(len(normalized), MAX_NORMALIZED_LENGTH)`（按字符/rune 计数）。
2. 对 `i` 从 0 到 `limit-1`：
   - `node = node.children[normalized[i]]`（不存在则新建）。
   - `node.count++`（**注意**：每次调用 `insert` 时，同一个字符路径上的所有深度节点的 `count` 都会 +1，即 `count` 表示“有多少条已插入的行，其归一化文本前 depth 个字符恰好等于这个 Trie 路径”——这是一个前缀计数，不是去重计数，两行文本前缀相同就会分别 +1，允许同一行内容出现多次都计数）。

### 算法：`deepestConfirmedDepth(root, normalized)`
1. `node := root`；`limit := min(len(normalized), MAX_NORMALIZED_LENGTH)`；`best := 0`。
2. 对 `i` 从 0 到 `limit-1`：
   a. `child := node.children[normalized[i]]`；若不存在，`break`（沿 Trie 路径走不下去了）。
   b. `node = child`；`depth := i+1`。
   c. `fullLineMatch := (depth == len(normalized))`（这个深度正好覆盖了整行归一化文本，说明整行都被剥离后会清空）。
   d. `threshold := 若 fullLineMatch 则 MIN_OCCURRENCES 否则 MIN_OCCURRENCES_PARTIAL`。
   e. 若 `node.count >= threshold` 且 `depth >= MIN_NORMALIZED_LENGTH`：`best = depth`（记录满足条件的**最深**位置，继续尝试更深）。
3. 返回 `best`。

**重要语义**：这个函数在遍历过程中不断刷新 `best`，即使某个中间深度不满足阈值，只要后续更深的深度重新满足，仍会覆盖为更深的值；只要 Trie 路径本身还能往下走（即该行归一化文本的这个前缀确实有其它行共享），就会一直尝试到 `limit`。最终返回的是**满足计数阈值的最大深度**（不要求路径上所有深度都满足阈值，只需要最终选中的那个深度本身满足）。

### 算法：`computeKey(raw, inFenceHolder)`
1. `trimmed := (raw==nil ? "" : raw).strip()`。
2. 若 `trimmed` 以 ` ``` ` 开头：翻转 `inFenceHolder[0]`；返回 `nil`（围栏起止行本身不参与统计）。
3. 若当前 `inFenceHolder[0]==true`（在围栏内）：返回 `nil`。
4. 若 `trimmed` 为空，或以 `|` 开头（表格行），或以 `#` 开头（标题行）：返回 `nil`。
5. 若 `trimmed` 整行匹配正则 `^[-=*_\s]{2,}$`（纯分隔线，如 `---`、`___`、`***`，允许中间夹杂空白）：返回 `nil`。
6. 若 `LIST_ITEM_PREFIX.matcher(trimmed).find()`（本行**包含**——不要求整行匹配，用 `find` 语义——一个列表行首前缀）：返回 `nil`（见兼容性预警第 1 条的 Go 替代实现）。
7. 计算 `leadingWs := len(raw) - len(stripLeading(raw))`（原始行前导空白字符数，用于后续 offset 换算成相对整个原始行而非仅 `trimmed` 部分的位置）。
8. 遍历 `trimmed` 的每个字符位置 `i`（从 0 到 `n-1`，`n=len(trimmed)`）构建归一化字符串 `norm` 与位置映射列表 `offsets`：
   a. 若当前字符是空白：跳过（`i++`，不加入 `norm`，不记录 offset——即空白被完全折叠删除，不是替换成单个空格）。
   b. 若当前字符是数字：往后扫描连续的数字字符直到 `j`（不含）；把单个字符 `'#'` 追加到 `norm`；把 `leadingWs + j` 追加到 `offsets`（即这个 `#` 归一化字符对应的原始行内结束位置，是整个连续数字串的结束偏移，不是数字串内部逐字符对应——**这是数字归一化为单字符 `#` 的关键**：无论原始数字串多长，归一化后只占 1 个字符位置，但其 offset 直接跳到数字串结束）；`i = j`（跳过整个数字串）。
   c. 否则（普通字符）：把该字符追加到 `norm`；`i++`；把 `leadingWs + i`（此时 `i` 已经自增，即消费到当前字符为止的结束偏移）追加到 `offsets`。
9. 若 `norm` 为空，返回 `nil`。
10. 构造 `rawOffsets` 数组（直接从 `offsets` List 转换），返回 `Key(norm, rawOffsets)`。

辅助函数 `stripLeading(s)`：跳过字符串开头的连续空白字符，返回剩余部分（等价于 Go 的 `strings.TrimLeft(s, " \t\n\r...")`，需要与 Java `Character.isWhitespace` 语义对齐，包含中文全角空格 `　` 等，具体空白字符集合应与 Part 5 `MarkdownPipelineLineUtils.stripEdgeWhitespace` 使用的空白定义保持一致）。

### 移植要点提醒
- `LIST_ITEM_PREFIX` 用的是 `find()`（子串搜索），不是整行匹配。Go 中用 `regexp.FindStringIndex` 或 `MatchString`（但 MatchString 对应 Java 的 `matches()`，需要用 `FindStringIndex != nil` 来对应 Java 的 `find()`）。
- `computeKey` 中的数字归一化逻辑（整段数字→单字符 `#`）是本类的核心去抖动机制，用于抹平页码差异（如 `-2-` / `-3-` 归一化后变成相同的 `-#-`）；实现时注意 offsets 记录的是**结束**偏移而非起始偏移。
- 该类整体只在 `context.scannedSource()==true` 时被 `MarkdownNoiseCleanupStage.apply` 调用，普通（非扫描件）PDF 转换路径完全不触发。

---

## MarkdownLineClassifier

### 职责
把每一行归类为九种 `MarkdownLineKind`（Part 5 定义该枚举本身及其 `blocksBodyMerge()` 方法）之一：`BLANK`/`FENCE`/`HEADING`/`LIST_ITEM`/`DATE`/`TABLE`/`QUOTE_OR_RULE`/`PREFORMATTED`/`NATURAL_TEXT`。这是弱合并判断（`MarkdownBodyMergeStage`/`MarkdownWeakMergeHeuristics`）的基础输入信号之一。同时提供“一组行整体是否像预格式化代码块”（`looksLikePreformattedBlock`，用于表格单元格内的多段文本、软换行拆分场景）的整块判定。

### 常量与正则

| 名称 | 定义 | 说明 |
|---|---|---|
| `SHELL_FLAG` | `(^|\s)-{1,2}[A-Za-z0-9][A-Za-z0-9_-]*(?:[=\s]|$)` | shell 命令行参数标记，如 `-v`、`--config=` |
| `PATH_OR_DEVICE` | `(^|\s)(?:/[^\s]+|[A-Za-z]:\\[^\s]+)` | Unix 绝对路径 或 Windows 盘符路径 |
| `KEY_VALUE` | `(^|\s)[A-Za-z_][A-Za-z0-9_{}.-]*(?:==|=|:=|=>).+` | key=value / key:=value / key=>value 形式 |
| `IP_OR_PORT` | `\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b` | IPv4 地址（含可选端口） |
| `VERSION_PACKAGE` | `\b[A-Za-z0-9_+.-]+-\d+(?:\.\d+){1,}[A-Za-z0-9_+.-]*\b` | 形如 `foo-1.2.3` 的软件包版本标识 |
| `HASH_OR_UUID` | `\b(?:[0-9a-fA-F]{16,}|[0-9a-fA-F]{8}-[0-9a-fA-F-]{13,})\b` | 长十六进制串或 UUID |
| `PERMISSION_LINE` | `^[bcdlps-]?[rwx-]{9}[+.@]?\s+\d+\s+\S+\s+\S+\s+.*` | `ls -l` 风格权限行 |
| `LOG_LINE` | `^(?:\d{4}[-/]\d{2}[-/]\d{2}|\d{2}:\d{2}:\d{2}|\[?(?:TRACE|DEBUG|INFO|WARN|ERROR|FATAL)\]?).*`（大小写不敏感） | 日志行起手日期/时间/级别标记 |
| `STACK_LINE` | `^(?:at\s+[\w.$]+\(.+\)|Traceback \(most recent call last\):|File "[^"]+", line \d+.*|Caused by: .*)$` | 异常堆栈行 |
| `SQL_START` | `^(?:select|with|insert|update|delete|create|alter|drop|merge|from|where|join|left\s+join|right\s+join|inner\s+join|group\s+by|order\s+by|having|values|set)\b.*`（大小写不敏感） | SQL 语句起手关键字 |
| `CODE_START` | `^(?:import|package|class|interface|enum|public|private|protected|static|final|def|function|func|if|else|for|while|switch|case|return|try|catch|throw|const|let|var)\b.*` | 通用编程语言关键字起手 |
| `CONFIG_LINE` | `^(?:[A-Za-z_][A-Za-z0-9_.-]*\s*[:=]\s*.+|[A-Za-z_][A-Za-z0-9_.-]*\s*:\s*|[A-Z_{}][A-Z0-9_{}.-]*(?:==|=|\+=).+|<[/A-Za-z][^>]*>|[{}\[\]],?)$` | YAML/INI/XML 标签/JSON 花括号等配置行形状 |
| `NATURAL_PREFIX` | `^(?:执行|通过|使用|可以|需要|例如|如果|当|为了|运行|输入|查看|配置|修改|将|把|在|然后|请|The\b|This\b|You\b|We\b|It\b).*` | 自然语言引导词起手 |
| `COMMAND_LEAD` | `^(?:sudo\s+)?[A-Za-z_][A-Za-z0-9_.+-]{1,40}(?:\s+.+)?$` | 命令行起手词（可选 `sudo`） |
| `COLUMN_GAP` | `\S {2,}\S` | 内部（非行首）连续 ≥2 空格间隔，等宽分栏排版信号 |
| `IPV4_HOST_PORT_PREFIX` | `^((?:25[0-5]|2[0-4]\d|1?\d{1,2})(?:\.(?:25[0-5]|2[0-4]\d|1?\d{1,2})){3}):\d+.*` | 合法 IPv4（每段 0-255）紧跟冒号端口 |
| `SENTENCE_FINAL_PUNCTUATION` | `.*[。！？!?]$` | 真正的自然语言句末标点（不含 `:`/`;`） |
| `PROSE_FUNCTION_WORDS_CJK` | 见下方列表 | 中文虚词/连接词闭集，子串匹配 |
| `PROSE_FUNCTION_WORDS_EN` | `\b(?:the|is|are|was|were|of|and|or|to|in|on|at|for|with|this|that|these|those|it|be|as|by|an)\b`（大小写不敏感） | 英文虚词闭集，词边界匹配 |
| `PROSE_SENTENCE_END` | `.*[。！？，、.!?,]$` | 自然语言句末标点（含逗号顿号，用于「桥接候选」判断），不含 `:`/`;` |
| `PROSE_RATIO_CEILING` | `0.15` | 整块排除法：像自然语言的行占比上限 |
| `STRUCTURED_RATIO_FLOOR` | `0.5` | 整块排除法：已展现结构化特征的行占比下限 |
| `PROSE_RATIO_NEAR_ZERO` | `0.05` | 整块排除法：几乎无自然语言行的占比阈值 |
| `PROSE_RATIO_NEAR_ZERO_MIN_LINES` | `3` | 上一条阈值生效所需的最少行数 |
| `STRUCTURAL_BRIDGE_MIN_PREFORMATTED` | `3` | 结构化数据块内允许吸收弱判定 LIST_ITEM/QUOTE_OR_RULE 行所需的最少 PREFORMATTED 强信号行数 |

`PROSE_FUNCTION_WORDS_CJK` 完整列表（按原文顺序，子串包含匹配，无需正则）：
```
的 了 是 在 和 与 但 因为 所以 如果 可以 需要 通过 执行
使用 将 把 然后 请 当 为了 例如 这个 那个 我们 你们 他们
并且 或者 虽然 不过 由于 根据 对于 关于 建议 应该 可能 一般
通常 主要 还是
```

### 数据结构
无本类独有的结构体/记录类型；全部是静态无状态函数。Go 实现建议为 `package mpp`（或按项目命名）下的一组包级函数，`classify` 系列函数返回 Part 5 定义的 `MarkdownLineKind` 枚举值。

### 算法：`classify(lines, index)`
1. 若 `lines` 为 `nil`，或 `index < 0`，或 `index >= len(lines)`：返回 `BLANK`。
2. `prev := nonBlankAt(lines, index, -1, 1)`（前一个非空行）。
3. `prev2 := nonBlankAt(lines, index, -1, 2)`（前两个非空行）。
4. `next := nonBlankAt(lines, index, 1, 1)`（后一个非空行）。
5. `next2 := nonBlankAt(lines, index, 1, 2)`（后两个非空行）。
6. 返回 `classifyText(lines[index], prev, prev2, next, next2)`。

### 算法：`classifyText(line)`（单参重载，供无上下文场景调用）
等价于 `classifyText(line, nil, nil, nil, nil)`。

### 算法：`nonBlankAt(lines, index, direction, hop)`
1. `found := 0`；`i := index`。
2. 循环：`i += direction`；若 `i` 越界（`<0` 或 `>=len(lines)`），返回 `nil`（未找到）。
3. `t := stripEdgeWhitespace(lines[i])`；若非空：`found++`；若 `found == hop`，返回 `t`。
4. 否则继续循环（跳过空行，继续朝 `direction` 方向找第 `hop` 个非空行）。

### 算法：`classifyText(line, prev, prev2, next, next2)`（核心分类逻辑，按顺序短路判断，命中即返回）
1. `t := stripEdgeWhitespace(line)`；若为空，返回 `BLANK`。
2. 若 `t` 以 ` ``` ` 开头，返回 `FENCE`。
3. 若 `t` 匹配 `HEADING_LINE`（Part 5）或以 `#` 开头，返回 `HEADING`。
4. 若 `MarkdownWeakMergeHeuristics.isListLikeLine(t)` 或 `isNumericOutlineBoundaryLine(t)` 为真，返回 `LIST_ITEM`。
5. 若 `MarkdownBodyMergeStage.isChineseDateLine(t)` 为真，返回 `DATE`。
6. 若 `isTableLikeLine(t)`（以 `|` 开头，或匹配 `TABLE_SEPARATOR`），返回 `TABLE`。
7. 若 `isQuoteOrRuleLine(t)`（以 `>` 开头，或匹配 `HORIZONTAL_RULE`），返回 `QUOTE_OR_RULE`。
8. 若 `looksPreformatted(t, prev, prev2, next, next2)` 为真，返回 `PREFORMATTED`。
9. 否则返回 `NATURAL_TEXT`（兜底默认值）。

### 算法：`isNumericOutlineBoundaryLine(t)`
1. 若 `t` 为空白，返回 false。
2. `s := t.strip()`。
3. 若 `s` 匹配 `IPV4_HOST_PORT_PREFIX`（如 `10.10.1.1:3260` 这种 host:port），返回 **false**（不是编号行，是网络地址）。
4. 否则返回 `s` 是否整体匹配 `^\d+(?:[.．]\d+)+(?:[.．])?(?![.．\d-]).*`（**含 negative lookahead，见兼容性预警第 2 条**）——即形如 `1.2`、`1.2.3.`（多级数字大纲编号，编号后不能紧跟另一个 `.`/数字/`-`）。

### 算法：`shapeScore(t)`（预格式化“形状打分”，累加型评分函数）
1. 若 `t` 为空白，返回 0。
2. `s := t.strip()`；`core := commentStrippedPrefix(s)`（去掉行内 `#`/`--` 注释后缀，见下方定义）。
3. `score := 0`，依次累加（**全部条件独立判断，不互斥，可同时命中多条**）：
   - `SQL_START.matches(s)` → `+4`
   - `CODE_START.matches(s)` → `+4`
   - `CONFIG_LINE.matches(s)` → `+4`
   - `PERMISSION_LINE.matches(s)` → `+5`
   - `LOG_LINE.matches(s)` → `+4`
   - `STACK_LINE.matches(s)` → `+5`
   - `COMMAND_LEAD.matches(core) 且 asciiTokenRatio(core) >= 0.65` → `+2`
   - `asciiTokenRatio(core) >= 0.95 且 2 <= tokenCount(core) <= 10` → `+2`
   - `SHELL_FLAG.find(s)` → `+3`
   - `PATH_OR_DEVICE.find(s)` → `+3`
   - `KEY_VALUE.find(s)` → `+3`
   - `IP_OR_PORT.find(s)` → `+2`
   - `VERSION_PACKAGE.find(s)` → `+2`
   - `HASH_OR_UUID.find(s)` → `+2`
   - `s` 含任一字符 `|`、`>`、`<`、或含子串 `&&`、`||` → `+2`
   - `s` 含子串 `::`、`->`、`=>`、`==`、`!=` → `+2`
   - `core` 以 `{`、`;`、`\`、`,` 结尾 → `+2`
   - `symbolRatio(core) >= 0.18 且 asciiTokenRatio(core) >= 0.70` → `+2`
   - `columnGapCount(s) >= 2` → `+4`
4. 返回累加后的 `score`。

### 算法：`naturalLanguageScore(t)`（自然语言反证打分）
1. `score := 0`。
2. 若 `NATURAL_PREFIX.matches(t)`，`score += 3`。
3. `core := commentStrippedPrefix(t)`。
4. 若 `cjkRatio(core) >= 0.30`，`score += 2`。
5. 若 `core` 匹配 `.*[。！？；，].*` 且 `symbolRatio(core) < 0.12`，`score += 2`。
6. 返回 `score`。

### 算法：`looksPreformatted(t, prev, prev2, next, next2)`
1. `score := shapeScore(t)`；若 `score <= 0`，返回 false（短路优化）。
2. 若 `naturalLanguageScore(t) >= 3` 且 `score < 6`，返回 false（自然语言反证优先于中等强度的形状分）。
3. 若 `t` 匹配 `SQL_START`、`CODE_START`、`PERMISSION_LINE`、`STACK_LINE` 或 `LOG_LINE` 中任意一个（整行匹配 `matches`），直接返回 true（强信号短路，无需看上下文）。
4. 计算 `contextual`（上下文辅助信号，任一为真即为 true）：
   - `contextIntroducesPreformatted(prev)`（上一行以“如下”等引导语收尾/包含特定关键词）；或
   - `shapeScore(next) >= 4`；或
   - `samePreformattedFamily(t, next)`；或
   - `samePreformattedFamily(prev, t)`；或
   - `isWeakPreformattedBridgeLine(next) 且 shapeScore(next2) >= 4`；或
   - `isWeakPreformattedBridgeLine(prev) 且 shapeScore(prev2) >= 4`
5. 返回 `score >= 6 || (score >= 2 && contextual)`。

### 算法：`isWeakPreformattedBridgeLine(s)`
1. 若 `s` 为空白，返回 false。
2. `x := s.strip()`；若 `len(x) > 24`，返回 false。
3. 若 `cjkRatio(x) > 0`，返回 false（含任何中文字符即不算弱桥接行）。
4. 返回 `!endsWithTerminalPunctuation(x)`（Part 5 提供的判断函数：不以句末终止标点结尾）。

### 算法：`contextIntroducesPreformatted(prev)`
1. 若 `prev` 为空白，返回 false。
2. `p := normalizeLine(prev)` 转小写（`Locale.ROOT`，即普通 ASCII 小写规则，不做特殊语言 casing）。
3. 返回 `p` 是否满足以下任一条件（原文顺序）：
   - 以 `如下` 结尾
   - 以 `如下：` 结尾
   - 以 `如下:` 结尾
   - 包含 `以下命令`
   - 包含 `下列命令`
   - 包含 `执行命令`
   - 包含 `启动`
   - 包含 `安装`
   - 包含 `服务`
   - 包含 `目标盘`
   - 包含 `配置文件`
   - 包含 `sql`
   - 包含 `代码`
   - 包含 `日志`
   - 包含 `输出`
   - 包含 `示例`

### 算法：`samePreformattedFamily(a, b)`
1. 若 `a`、`b` 任一为空白，返回 false。
2. `left := a.strip()`；`right := b.strip()`。
3. 若两者都整行匹配 `SQL_START`，返回 true。
4. 若两者都整行匹配 `PERMISSION_LINE`，返回 true。
5. 若两者都整行匹配 `CONFIG_LINE`，返回 true。
6. 若两者都能 `find` 到 `IP_OR_PORT`，返回 true。
7. 否则返回：`asciiTokenRatio(left) >= 0.75 且 asciiTokenRatio(right) >= 0.75 且 (SHELL_FLAG.find(left) 或 SHELL_FLAG.find(right) 或 PATH_OR_DEVICE.find(left) 或 PATH_OR_DEVICE.find(right))`。

### 算法：`commentStrippedPrefix(s)`
1. `idx := indexOfWhitespacePrecededMarker(s, "#")`。
2. `dashIdx := indexOfWhitespacePrecededMarker(s, "--")`。
3. 若 `dashIdx >= 0` 且（`idx < 0` 或 `dashIdx < idx`），令 `idx = dashIdx`（取更靠前的注释标记位置，`#` 与 `--` 谁先出现取谁）。
4. 若 `idx > 0`，返回 `s[0:idx].strip()`；否则返回 `s` 原样（`idx <= 0` 包括“没找到”和“标记恰好在行首”两种情况都不裁剪——行首即 `#`/`--` 的整行注释由块级软行桥接处理，此函数不处理）。

### 算法：`indexOfWhitespacePrecededMarker(s, marker)`
1. `idx := s.indexOf(marker)`（首次出现位置）。
2. 循环：只要 `idx > 0`：
   - 若 `s[idx-1]` 是空白字符，返回 `idx`。
   - 否则 `idx = s.indexOf(marker, idx+1)`（继续找下一次出现）。
3. 循环结束（`idx <= 0`，含未找到 `-1` 与恰好在开头 `0` 两种情况），返回 `-1`。
（语义：找到第一个**前面紧邻空白字符**的 marker 出现位置，行首本身出现的 marker 不算——因为那种是整行注释而非行内注释。）

### 算法：`columnGapCount(s)`
1. 用 `COLUMN_GAP`（`\S {2,}\S`）在 `s` 上反复 `find`，注意重叠处理：每次找到匹配后，`from = m.end() - 1`（**回退 1 个字符**再继续搜索，这样连续多处间隔可以被正确地各自计数，即使两处间隔共享边界字符）。
2. 统计命中次数 `count`，循环直到找不到匹配为止。
3. 返回 `count`。

Go 实现提示：`regexp.FindAllStringIndex` 默认不支持"从某个起点继续搜索并允许重叠"，需要手写循环：`from := 0; for { loc := re.FindStringIndex(s[from:]); if loc == nil { break }; count++; from = from + loc[1] - 1 }`（注意 UTF-8 情况下 `loc` 是字节索引，`-1` 应改为回退到最后一个匹配字符的字节起点，需要用 rune 长度换算，不能直接减 1 字节——建议用 `utf8.RuneLen` 计算最后一个字符的字节宽度后回退）。

### 算法：`cjkRatio(s)` / `asciiTokenRatio(s)` / `tokenCount(s)` / `symbolRatio(s)` / `isCjk(ch)`
- `cjkRatio(s)`：遍历字符（跳过空白），统计属于 CJK Unicode 区块（`CJK_UNIFIED_IDEOGRAPHS`、`EXTENSION_A`、`EXTENSION_B`、`COMPATIBILITY_IDEOGRAPHS` 四个区块——**注意本类版本的 `isCjk` 只覆盖这 4 个区块，比 `MarkdownBodyMergeStage.isCjkUnifiedIdeograph` 少 C/D/E/F 扩展区**，两处判断范围略有差异，Go 实现应分别对应两个不同的辅助函数，不要合并）的字符数占比；总数为 0 返回 0.0。
- `asciiTokenRatio(s)`：按空白切分 `s.strip()` 得到 tokens；统计每个 token 是否整体为纯 ASCII 字符（正则 `[\p{ASCII}]+` 整体匹配）；返回 ASCII token 数 / 总 token 数；无 token 返回 0.0。
- `tokenCount(s)`：`s.strip().split("\\s+")` 后的数组长度；空白返回 0。
- `symbolRatio(s)`：遍历字符（跳过空白），统计属于符号集合 `"-_/:=\\\"'.,|<>[]{}()$*+@#;&"`（Java 字符串字面量，注意其中 `\\\"` 是转义后的双引号 `"`，`\\\\` 是反斜杠 `\`；完整符号集合按字符列举为：`- _ / : = " ' . , | < > [ ] { } ( ) $ * + @ # ; &`）的字符数占比；总数为 0 返回 0.0。
- `isCjk(ch)`：单字符是否属于上述 4 个 CJK Unicode 区块。

### 算法：`looksLikeBareTechnicalToken(t)`
1. 若 `t` 为空白，返回 false。
2. 若 `cjkRatio(t) > 0`（含任何中文字符），返回 false（不算纯技术 token，因为可能是合法中文续行）。
3. 否则返回 `!looksLikeProseLine(t)`。
（用途：识别形如容器镜像引用 `registry.k8s.io/kube-apiserver:v1.27.3` 或裸 UUID 这类完全不含中文、也不像自然语言的技术 token，即使形状上碰巧命中标题续行的宽松兜底分支，也不能被当作标题续行的另一半。）

### 算法：`looksLikeProseLine(t)`（包内可见，供 `MarkdownBodyMergeStage` 复用）
1. 若 `t` 为空白，返回 false。
2. 若 `t` 整体匹配 `SENTENCE_FINAL_PUNCTUATION`（以 `。！？!?` 结尾），返回 true。
3. 否则返回 `containsProseFunctionWord(t) && symbolRatio(t) < 0.12`。

### 算法：`containsProseFunctionWord(t)`
1. 遍历 `PROSE_FUNCTION_WORDS_CJK` 列表，若 `t` 包含其中任一子串，返回 true。
2. 否则返回 `PROSE_FUNCTION_WORDS_EN.find(t)`（词边界匹配英文虚词）。

### 算法：`looksLikePreformattedBlock(lines)`（对外主要入口之一）
目的：判断一组行（如表格单元格内的多段文本、软换行拆出的多段）整体是否为代码/SQL/命令行/日志/配置/YAML 等结构化数据块。

1. 若 `lines` 为 `nil`，返回 false。
2. 收集所有非空白行的原始索引到 `nonBlankIdx` 列表。
3. 若 `len(nonBlankIdx) < 2`，返回 false（少于两行不构成"块"）。
4. 初始化布尔数组 `accepted[len(nonBlankIdx)]`，全 false；`preformattedCount := 0`。
5. 对 `nonBlankIdx` 中每个位置 `k`（对应原始行索引 `i`）：
   - `t := stripEdgeWhitespace(lines[i])`；`kind := classify(lines, i)`。
   - 若 `kind == PREFORMATTED`：`accepted[k] = true`；`preformattedCount++`。
   - 否则若 `kind == HEADING 且 t 以 # 开头`（**注意**：仅当真的是 `#` 前缀标题时才接受，形状像标题模式但无 `#` 的不算）：`accepted[k] = true`（**但不计入 `preformattedCount`**）。
6. 调用 `bridgeWeakGaps(lines, nonBlankIdx, accepted, preformattedCount >= STRUCTURAL_BRIDGE_MIN_PREFORMATTED)` 原地修改 `accepted`（吸收夹在已接受行之间/位于数组首尾且与已接受行相邻的连续弱判定行游程）。
7. 检查 `accepted` 是否全部为 true（`allAccepted`）。
8. 若 `allAccepted && preformattedCount >= 2`，返回 true。
9. 否则调用并返回 `looksStructuredByProseExclusion(lines, nonBlankIdx)` 的结果（兜底：整块自然语言排除法）。

### 算法：`bridgeWeakGaps(lines, nonBlankIdx, accepted, allowStructuralKinds)`（原地修改 `accepted`）
1. `n := len(accepted)`；`i := 0`。
2. 循环直到 `i >= n`：
   a. 若 `accepted[i]` 为 true，`i++`，continue。
   b. 否则：记录 `start := i`；继续推进 `i` 直到 `accepted[i]` 为 true 或 `i>=n`；记录 `end := i`（此时 `[start, end)` 是一段连续未接受的游程）。
   c. `anchored := (start > 0) || (end < n)`（游程不是覆盖整个数组——至少一侧有边界，即数组首/尾游程只需单侧锚定，中间游程天然两侧都有锚点）。
   d. 若 `anchored` 且 `isBridgeableRun(lines, nonBlankIdx, start, end, allowStructuralKinds)`：把 `accepted[start..end)` 全部置为 true。
3. 返回（`accepted` 已原地修改）。

### 算法：`isBridgeableRun(lines, nonBlankIdx, fromInclusive, toExclusive, allowStructuralKinds)`
1. 对 `k` 从 `fromInclusive` 到 `toExclusive-1`：
   - `i := nonBlankIdx[k]`；`t := stripEdgeWhitespace(lines[i])`。
   - 若 `!isBridgeCandidate(classify(lines, i), t, allowStructuralKinds)`，返回 false。
   - 若 `HeadingLevelPrefixHeuristics.classifyPrefixKey(t) != nil`（Part 4 依赖：该行带有明确的层级标题前缀标记），返回 false（哪怕形状像可吸收的自然文本，只要带有结构化标题前缀标记就不允许被当作桥接行吸收）。
2. 全部通过，返回 true。

### 算法：`isBridgeCandidate(kind, t, allowStructuralKinds)`
1. 若 `kind == NATURAL_TEXT`，返回 true（自然文本始终可吸收）。
2. 若 `!allowStructuralKinds`，返回 false。
3. 若 `kind != LIST_ITEM && kind != QUOTE_OR_RULE`，返回 false。
4. 若 `t` 以 `>` 开头（真正的 Markdown 引用符），返回 false。
5. 返回 `!PROSE_SENTENCE_END.matches(t)`（不以中/英文句末标点、逗号、顿号结尾——放行 YAML 等以 `:`/`;` 收尾的结构化行，排除真正的自然语言列表/引用句）。

### 算法：`looksStructuredByProseExclusion(lines, nonBlankIdx)`（整块自然语言排除法，兜底判定）
1. `total := len(nonBlankIdx)`；`proseLines := 0`；`structuredLines := 0`。
2. 对每个 `k`（对应行索引 `i`）：
   - `t := stripEdgeWhitespace(lines[i])`；`kind := classify(lines, i)`。
   - `hashPrefixed := (kind == HEADING && t 以 # 开头)`。
   - 若 `!hashPrefixed && looksLikeProseLine(t)`：`proseLines++`（起手 `#` 的行不参与“像不像自然语言”的统计，即使内容本身含大量中文虚词）。
   - 若 `kind == PREFORMATTED || hashPrefixed || isBridgeCandidate(kind, t, true)`（**注意**：此处调用 `isBridgeCandidate` 时 `allowStructuralKinds` 固定传 `true`，不使用外层的条件判断）：`structuredLines++`。
3. `proseRatio := proseLines / total`（浮点除法）。
4. 若 `proseRatio <= PROSE_RATIO_NEAR_ZERO (0.05)` 且 `total >= PROSE_RATIO_NEAR_ZERO_MIN_LINES (3)`：返回 true（情形 2：几乎没有一行像自然语言，即使没有命中任何已知语法关键字，也判定为结构化块）。
5. `structuredRatio := structuredLines / total`。
6. 返回 `proseRatio <= PROSE_RATIO_CEILING (0.15) && structuredRatio >= STRUCTURED_RATIO_FLOOR (0.5)`（情形 1）。

---

## MarkdownBodyMergeStage

### 职责
标题已定稿之后的阶段 3：合并被 PDF/OCR 拆断的正文行。包含三个子流程：
1. `splitGluedOrgLineAndChineseDateLine`：把“XX局2026年3月1日”这种机构名+日期粘连行拆开。
2. `mergeWrappedBodyLines`：贪心地把被硬换行/单空行拆断的正文行、标题续行合并回一行（主合并循环，处理跨空行、跨分页边界的场景）。
3. `mergeWeakBodyLinesOutsideFences`：在 `mergeWrappedBodyLines` 之后，对代码围栏外的行再做一轮更保守的"弱合并"（处理两行/三行短句模式）。

### 常量与正则

| 名称 | 定义 | 说明 |
|---|---|---|
| `OFFICIAL_DOCUMENT_TITLE_TAIL` | `.*的(?:通知|决定|决议|公告|通告|议案|报告|请示|批复|意见|函|纪要|命令|条例|规定|办法)$` | 公文标题收束词（“关于……的通知”等），用 `.*` 前缀而非 `startsWith("关于")`，容忍标题前面已粘连其它行 |
| `ADDRESSEE_SALUTATION_LINE` | `^[\p{IsIdeographic}、，,\s]{1,40}[：:]$` | 主送机关/称谓行（如“市政府:”），短促+仅含机构名/列举+冒号收束 |
| `MAX_HEADING_LENGTH` | `MarkdownPipelineLineUtils.loadMaxHeadingLength()`（Part 5 提供的可配置加载函数） | 标题最大长度阈值，跨空行合并时用于短路判断左行是否过长而不像标题 |
| 内联正则（`splitGluedOrgLineAndChineseDateLine` 局部变量 `glued`） | `^(.*[局处部司厅])((?:[0-9０-９]{4}年).+)$` | 匹配“机构名（局/处/部/司/厅结尾）+紧跟中文年份日期”的粘连行 |

### 数据结构
```java
public record BodyMergeContext(
    Set<Integer> hierarchyLineIndexes,
    Set<String> disqualifiedPatternKeys,
    Set<Integer> sourceMarkdownHeadingLineIndexes) {}
```
Go 对应：
```go
type BodyMergeContext struct {
    HierarchyLineIndexes         map[int]struct{}
    DisqualifiedPatternKeys      map[string]struct{}
    SourceMarkdownHeadingLineIndexes map[int]struct{}
}
```
三个字段均来自 `MarkdownPipelineContext`（Part 5）：
- `hierarchyLineIndexes`：已定稿为层级标题的行索引集合（`context.hierarchyLineIndexes()`）。
- `disqualifiedPatternKeys`：已被最终判定为“不合格标题模式”的模式 key 集合（`context.finalDisqualifiedPatternKeys()`）。
- `sourceMarkdownHeadingLineIndexes`：源文档中本来就带 `#` 的（非本管线生成的）标题行索引集合（`context.sourceMarkdownHeadingLineIndexes()`）。

### 算法：`apply(context)`（主流程）
1. `lines := splitGluedOrgLineAndChineseDateLine(context.lines())`。
2. 构造 `bodyMergeCtx := BodyMergeContext(context.hierarchyLineIndexes(), context.finalDisqualifiedPatternKeys(), context.sourceMarkdownHeadingLineIndexes())`。
3. `lines = mergeWrappedBodyLines(lines, context.pageRemovedOutputAnchors(), context.attachmentScopesForMerge(), bodyMergeCtx)`。
4. `lines = mergeWeakBodyLinesOutsideFences(lines, bodyMergeCtx)`。
5. `context.setLines(lines)`。

### 算法：`mergeBodyLinesForTest(lines)`（测试专用入口，公开方法）
1. 构造空上下文 `ctx := BodyMergeContext({}, {}, {})`（三个集合均为空）。
2. `wrapped := mergeWrappedBodyLines(lines, {}, [], ctx)`。
3. 返回 `mergeWeakBodyLinesOutsideFences(wrapped, ctx)`。
（移植提示：Go 版本应同样暴露一个不依赖真实 `PipelineContext` 的测试辅助函数，便于单元测试。）

### 算法：`splitGluedOrgLineAndChineseDateLine(lines)`
1. 若 `lines` 为空，原样返回。
2. 正则 `glued := ^(.*[局处部司厅])((?:[0-9０-９]{4}年).+)$`（局部编译，注意 `[0-9０-９]` 同时接受 ASCII 数字与全角数字）。
3. 对每一行 `raw`（`nil` 跳过）：
   a. `trimmed := stripEdgeWhitespace(raw)`。
   b. 用 `glued` 对 `trimmed` 做**整行匹配**（`matches`）：
      - 若匹配成功：`head := group(1).strip()`；`date := group(2).strip()`。
        - 若 `trimmed` 以 `#` 开头：再用 `HEADING_LINE` 匹配 `trimmed`；若也匹配成功：把 `group(1) + " " + head` 加入输出（保留原有的 `#` 级别前缀，标题部分是机构名），再把 `date`（不带 `#` 前缀）单独加入输出，`continue`。
        - 否则（不带 `#`，或 `#` 匹配但 `HEADING_LINE` 未匹配——理论上不太会发生，但按原文逻辑走到这里）：把 `head` 加入输出，把 `date` 加入输出，`continue`。
      - 若不匹配：把 `raw` 原样加入输出。
4. 返回输出列表。

### 算法：`mergeWrappedBodyLines(lines, pageBoundaryAnchors, attachmentListScopes, mergeContext)`（贪心正向扫描主合并循环）

这是本 Part 中最核心、最复杂的合并算法。逐行扫描，对每个起始行尝试尽可能多地把后续行并入同一段。

1. 若 `lines` 为空，返回空列表。
2. `anchors := pageBoundaryAnchors`（若为 nil 则视为空集合）。
3. `columnMinLenHint := MarkdownWeakMergeHeuristics.weakMergeMinColumnNormalizedLenHint(lines)`（预先算好的“版心最小行长”提示，用于弱合并阈值动态收窄）。
4. 维护 `inFence := false`；`i := 0`。
5. **外层循环**（`i < len(lines)`）：
   a. `current := lines[i]`；`currentTrimmed := stripEdgeWhitespace(current)`。
   b. 若 `isFenceLine(currentTrimmed)`：翻转 `inFence`，原样加入输出，`i++`，continue。
   c. 若 `inFence` 或 `currentTrimmed` 为空：原样加入输出，`i++`，continue。
   d. 否则进入**内层贪心扩展循环**，试图把尽可能多的后续行并入 `merged`（初始为 `current`），`cursor := i`：
      - **内层循环体**（`while true`）：
        1. `next := cursor + 1`；`acrossPageGap := false`。
        2. 若 `next >= len(lines)`，`break`（内层循环结束，没有更多行可并）。
        3. `nextTrimmed := stripEdgeWhitespace(lines[next])`。
        4. **子分支：下一行是空行（`nextTrimmed` 为空）**——尝试“跨单空行合并”：
           - `candidate := next + 1`；若 `candidate >= len(lines)`，`break`（外层内层循环都跳出，因为没有可跨越的候选行）。
           - 若 `lines[candidate]` 也是空行（连续两个空行），`break`（不跨越双空行）。
           - 若 `!isPageBoundaryGap(next, anchors)`（这个空行**不是**页码剔除产生的分页边界）：
             - `candTrimmed := stripEdgeWhitespace(lines[candidate])`。
             - 校验三个条件，**任一失败则 `break`**（跳出整个内层扩展循环）：
               a. `canMergeAcrossSingleBlankLine(stripEdgeWhitespace(merged), candTrimmed, lines, i, candidate, mergeContext)` 必须为 true；
               b. `!shouldBlockWrappedMergePair(stripEdgeWhitespace(merged), candTrimmed, lines, i, candidate, attachmentListScopes, columnMinLenHint, mergeContext)`；
               c. `canMergeWrappedPair(merged, lines[candidate], candTrimmed, lines, i, candidate, mergeContext)` 必须为 true。
             - 全部通过：`next = candidate`；`nextTrimmed = candTrimmed`。
           - 否则（**是**页码分页边界的空行）：直接允许跨越，`next = candidate`；`acrossPageGap = true`；`nextTrimmed = stripEdgeWhitespace(lines[next])`（重新取值，等价于 candTrimmed）。
        5. **中文日期行阻断**：若 `isChineseDateLine(nextTrimmed)` 或 `isChineseDateLine(stripEdgeWhitespace(merged))`，`break`（日期行永远独立成行，不参与合并）。
        6. **正式的阻断判断**：若 `shouldBlockWrappedMergePair(stripEdgeWhitespace(merged), nextTrimmed, lines, i, next, attachmentListScopes, columnMinLenHint, mergeContext)` 为 true，`break`。
        7. **正式的可合并判断**：若 `!canMergeWrappedPair(merged, lines[next], nextTrimmed, lines, i, next, mergeContext)`，`break`。
        8. **再次中文日期行/机构名+年份阻断**（对已经通过前面校验的 `next` 行再做一次保险检查）：若 `isChineseDateLine(nextTrimmed)`，或（`stripEdgeWhitespace(merged)` 以 `局` 结尾 且 `nextTrimmed` 匹配 `^[0-9０-９]{4}年.*`），`break`。
        9. **执行合并**：若 `MarkdownWeakMergeHeuristics.looksLikeWrappedTitleContinuation(stripEdgeWhitespace(merged), nextTrimmed)`：`merged = mergeWrappedTitleContinuationLines(merged, lines[next])`（走标题续行专用合并，保留/处理 `#` 前缀）；否则：`merged = joinWrappedLines(merged, lines[next])`（普通正文续行合并）。
        10. `cursor = next`；（`acrossPageGap` 标志目前只用于源码内的一条空注释，无实际逻辑分支，Go 实现可以省略这个变量或保留仅作调试标记）。
        11. 回到内层循环开头（继续尝试扩展下一行）。
   e. 内层循环结束后：把 `merged` 加入输出；`i = cursor + 1`（跳到已消费的最后一行之后）。
6. 返回输出列表。

**极为重要的实现顺序细节**：步骤 5.4 的“跨空行”分支和步骤 5.6-5.9 的“直接相邻”分支，两者的阻断/合并判断函数调用**参数不完全相同**——跨空行分支额外调用了 `canMergeAcrossSingleBlankLine`（这是相邻行分支没有的），且两个分支各自独立调用了一遍 `shouldBlockWrappedMergePair` 和 `canMergeWrappedPair`（跨空行分支内联在子分支里调用一次，随后无论是否跨了空行，主循环体第 6、7 步又会对（可能已经被替换成 `candidate` 的）`next`/`nextTrimmed` **再调用一次**同样的两个函数）。这不是代码冗余而是有意为之：跨空行分支先做一轮"是否允许跨越空行本身"的校验，紧接着无论走了哪条路径，都要对最终确定的 `next` 行再走一遍统一的合并判断。Go 移植时必须保留这个双重调用结构，不要为了"去重"合并成一次调用，否则会改变阻断的判断时机（例如 `shouldBlockWrappedMergePair` 内部会调用 `isImmediatelyAfterAttachmentListScope` 等依赖具体行索引的函数，两次调用传入的行索引参数不同）。

### 算法：`canMergeWrappedPair(current, next, nextTrimmed, lines, leftLineIndex, nextLineIndex, mergeContext)`
判断两行是否**允许**合并（正向条件，越过所有否决点才返回 true）。

1. `left := stripEdgeWhitespace(current)`；`right := nextTrimmed 非空则用之，否则 stripEdgeWhitespace(next)`。
2. 若 `left` 或 `right` 为空，返回 false。
3. 若 `left` 或 `right` 是围栏行（`isFenceLine`），返回 false。
4. **规则 1（标题续行优先通道）**：若 `MarkdownWeakMergeHeuristics.looksLikeWrappedTitleContinuation(left, right)` 为真：返回 `!shouldBlockBodyMergePair(lines, leftLineIndex, nextLineIndex, left, right, mergeContext)`（即：只要不是被明确禁止的场景，标题续行直接放行，不再走后面的正文合并规则）。
5. 若 `!canEnterBodyMerge(lines, leftLineIndex, nextLineIndex, left, right)`（基于行分类的黑名单检查），返回 false。
6. 若 `left` 以 `关于` 开头且 `len(left) >= 12`，返回 false（长“关于……”句式本身通常是完整的标题起句，不应再往后粘连成正文段）。
7. 若 `shouldBlockBodyMergePair(lines, leftLineIndex, nextLineIndex, left, right, mergeContext)` 为真，返回 false。
8. 若 `right` 是列表项（`isListLikeLine`），或（`left` 是列表项 且 `!isListItemBodyContinuation(left, right)`），返回 false（列表项本身不能被吞并进上一段，除非它是列表项之后的正文续行）。
9. 若 `left` 或 `right` 是表格行（`isTableLikeLine`），返回 false。
10. 若 `left` 或 `right` 是引用/分隔线行（`isQuoteOrRuleLine`），返回 false。
11. 若 `left` 以句末终止标点结尾（`endsWithTerminalPunctuation`）且**不是**“截断的时间冒号”场景（`!endsWithTruncatedTimeColon(left, right)`），返回 false（句子已经完整结束，不应续接，除非是被截断的 `H:MM` 时间值特例）。
12. 若 `right` 以 ASCII/全角冒号结尾（`endsWithAsciiOrFullwidthColon`）且 `left` 是“短小无句读独立短语”（`isShortUnpunctuatedPhrase`），返回 false（短标签行 + 冒号收尾的右行，两者各自独立成段）。
13. 若 `isChineseDateLine(right)` 或 `isChineseDateLine(left)`，返回 false。
14. `first := right` 的首字符；若既不是字母数字也不是 CJK 表意字符，返回 false（右行开头必须是“正常文字”，排除标点符号开头等异常情况）。
15. 全部通过，返回 true。

### 算法：`joinWrappedLines(current, next)`
1. `left := stripEdgeWhitespace(current)`；`right := stripEdgeWhitespace(next)`。
2. 若 `left` 为空，返回 `right`；若 `right` 为空，返回 `left`。
3. `end := left` 最后一个字符；`begin := right` 第一个字符。
4. `addSpace := isLetterOrDigit(end) && isLetterOrDigit(begin) && !isCjkUnifiedIdeograph(end) && !isCjkUnifiedIdeograph(begin)`（**仅当**两端都是非中文的字母/数字时才插入空格分隔，中文之间/中英文交界处直接拼接不加空格）。
5. 返回 `addSpace ? left + " " + right : left + right`。

### 算法：`shouldEarlyMergeWrappedPlainTitle(left, right)`（公开方法，供 `MarkdownNoiseCleanupStage.mergeChapterTitleContinuationLines` 调用）
1. 若 `!MarkdownWeakMergeHeuristics.looksLikeWrappedTitleContinuation(left, right)`，返回 false。
2. `l := normalizeLine(stripEdgeWhitespace(left))`；`r := normalizeLine(stripEdgeWhitespace(right))`。
3. 若 `l` 包含 `：` 或 `:` 或 `签发人`，返回 false（左行已经是带冒号的完整称谓/元数据行，不应早合并）。
4. 若 `r` 以 `关于` 开头，或以 `印发` 开头，或以 `《` 开头，返回 true。
5. 否则返回 `len(l) <= 24 && (l 以 局/委/厅/公司 结尾)`。

### 算法：`mergeWrappedTitleContinuationLines(leftRaw, rightRaw)`（公开方法）
1. `left := stripEdgeWhitespace(leftRaw)`；`right := stripEdgeWhitespace(rightRaw)`。
2. 若 `left` 和 `right` 都匹配 `HEADING_LINE`（都带 `#` 前缀）：`prefix := group(1)`（取左行的 `#` 级别标记）；`body := joinWrappedLines(leftHeading.group(2), rightHeading.group(2))`（把两行去掉 `#` 前缀后的正文部分拼接）；返回 `prefix + " " + body`。
3. 否则：返回 `joinWrappedLines(leftRaw, rightRaw)`（普通拼接，注意这里传的是**原始**（未 trim）的 `leftRaw`/`rightRaw`，但 `joinWrappedLines` 内部会自己做 `stripEdgeWhitespace`，所以效果等价）。

### 算法：`peekTrimmedImmediateNextLine(lines, nextLineIndex)`
1. `j := nextLineIndex + 1`；若 `j >= len(lines)`，返回 `nil`。
2. `t := stripEdgeWhitespace(lines[j])`；若为空返回 `nil`，否则返回 `t`。
（注意：只窥视**紧邻**的下一行，不跳过空行——与前面某些函数“跳过空行找下一个非空行”的语义不同，需要严格区分。）

### 算法：`shouldBlockWrappedMergePair(leftTrimmed, rightTrimmed, lines, currentLineIndex, nextLineIndex, attachmentListScopes, columnMinLenHint, mergeContext)`
（判断两行是否应该**禁止**合并——OCR 正文续行专用的阻断判断，按顺序检查，命中任一即返回 true）

1. 若 `leftTrimmed` 或 `rightTrimmed` 为空，返回 false（空行不参与阻断判断，交由上层处理）。
2. 若 `shouldBlockBodyMergePair(lines, currentLineIndex, nextLineIndex, leftTrimmed, rightTrimmed, mergeContext)` 为真，返回 true。
3. 若 `isImmediatelyAfterInlineSingleAttachmentAnchor(lines, currentLineIndex, attachmentListScopes)` 为真，返回 true（当前行紧跟在“附件：单个文件名”这种内联附件锚点之后，禁止续拼——防止把附件名和后续内容粘连）。
4. `third := peekTrimmedImmediateNextLine(lines, nextLineIndex)`（窥视第三行）。
5. 若 `isImmediatelyAfterAttachmentListScope(lines, currentLineIndex, attachmentListScopes)` 为真 且 `third == nil`（没有第三行可看），返回 true。
6. 若 `isListItemBodyContinuation(leftTrimmed, rightTrimmed)` 为真，返回 **false**（这是一个提前放行的短路：只要判定为“列表项+正文续行”模式，直接允许合并，不再继续后面的阻断检查）。
7. 若 `MarkdownWeakMergeHeuristics.shouldBlockOcrWeakMergePair(leftTrimmed, rightTrimmed, third)` 为真，返回 true。
8. 若 `MarkdownWeakMergeHeuristics.weakMergeBlocksAsPreviousLine(leftTrimmed, rightTrimmed, columnMinLenHint)` 为真，返回 true。
9. 返回 `lineStartsWithStructuralHeadingOrListPrefixForBodyMerge(rightTrimmed, mergeContext)`（下一行本身像层级标题/列表前缀起手，禁止把它并入上一段）。

### 算法：`canMergeAcrossSingleBlankLine(leftTrimmed, rightTrimmed, lines, leftLineIndex, rightLineIndex, mergeContext)`
（判断能否跨越单个空行合并——比“直接相邻”场景更保守，要求更多正向条件）

1. 若任一为空，返回 false。
2. 若 `shouldBlockBodyMergePair(...)` 为真，返回 false。
3. 若 `leftTrimmed` 以句末终止标点结尾，返回 false（跨空行时左行绝不能已经是完整句子）。
4. 若 `looksLikeWrappedTitleContinuation(leftTrimmed, rightTrimmed)`，返回 true（标题续行场景放行，不再检查后续条件）。
5. 若 `!canEnterBodyMerge(lines, leftLineIndex, rightLineIndex, leftTrimmed, rightTrimmed)`，返回 false。
6. `leftNorm := normalizeLine(leftTrimmed)`；若 `len(leftNorm) > MAX_HEADING_LENGTH`，返回 false（左行太长，不像可以跨空行续接的短纲/短句）。
7. 若 `looksLikeDistinctParagraphBoundaryAcrossBlank(leftTrimmed, rightTrimmed)`，返回 false（判定为不同段落边界）。
8. 若 `!isShortUnpunctuatedPhrase(leftTrimmed) || !isShortUnpunctuatedPhrase(rightTrimmed)`，返回 false（跨空行合并要求**两行都必须是**“短小无句读独立短语”，这是比相邻行合并严格得多的限制）。
9. 若 `rightTrimmed` 是围栏行、列表项、表格行或引用/分隔线行，返回 false。
10. `first := rightTrimmed` 首字符；返回 `isLetterOrDigit(first) || isCjkUnifiedIdeograph(first)`。

### 算法：`isImmediatelyAfterAttachmentListScope(lines, currentLineIndex, attachmentListScopes)`
1. 若相关列表为空/`currentLineIndex` 越界，返回 false。
2. 对每个 `scope`：`p := max(0, scope.endLine)`；跳过 `p` 位置开始的连续空行（`while p<len(lines) && lines[p] trim 为空: p++`）；若 `p == currentLineIndex`，返回 true。
3. 全部不匹配，返回 false。

### 算法：`isImmediatelyAfterInlineSingleAttachmentAnchor(lines, currentLineIndex, attachmentListScopes)`
1. 若相关列表为空/`currentLineIndex` 越界，返回 false。
2. 对每个 `scope`（校验 `scope.startLine` 合法范围）：
   - `anchor := normalizeLine(lines[scope.startLine])`。
   - 若 `!MarkdownNoiseCleanupStage.isInlineAttachmentAnchorWithSingleName(anchor)`，`continue`（跳过这个 scope）。
   - `p := max(0, scope.endLine)`；跳过连续空行；若 `p == currentLineIndex`，返回 true。
3. 全部不匹配，返回 false。

### 算法：`isFenceLine(trimmed)` / `isHeadingLikeLine(trimmed[, lines, lineId])` / `isTableLikeLine` / `isQuoteOrRuleLine`（`MarkdownBodyMergeStage` 内的私有版本）
- `isFenceLine`：`trimmed` 非空且以 ` ``` ` 开头。
- `isHeadingLikeLine(trimmed)`：等价于 `isHeadingLikeLine(trimmed, nil, -1)`。
- `isHeadingLikeLine(trimmed, lines, lineId)`：
  1. 若 `trimmed` 为空，返回 false。
  2. 若 `lines != nil && lineId >= 0` 且 `HeadingSuppressHeuristics.shouldSuppressHeadingLine(lines, lineId)`，返回 false（外部依赖：该行已被判定应抑制标题识别）。
  3. 若 `ChapterReferenceHeuristics.isBodyChapterReference(trimmed)`（外部依赖：这其实是正文里对章节的**引用**，比如“详见第三章”，不是真标题），返回 false。
  4. 若匹配 `HEADING_LINE` 或以 `#` 开头，返回 true。
  5. `norm := normalizeLine(trimmed)`；若为空，返回 false。
  6. 返回 `MarkdownTitlePattern.matchFirst(norm) != nil`。
- `isTableLikeLine(trimmed)` / `isQuoteOrRuleLine(trimmed)`：与 `MarkdownWeakMergeHeuristics` 中同名方法逻辑完全一致（各自独立定义了一份，行为相同，属于历史代码重复，Go 实现可以提取为共享工具函数，但需要在两处都能访问到）。

### 算法：`shouldBlockBodyMergePair`（两个重载，核心的“定稱后是否禁止合并”判断——弱合并规则的第二大核心函数）

**重载 1**（3 参数简化版）：
```
shouldBlockBodyMergePair(leftLineId, rightLineId, leftTrimmed, rightTrimmed, ctx)
  = shouldBlockBodyMergePair(nil, leftLineId, rightLineId, leftTrimmed, rightTrimmed, ctx)
```

**重载 2**（完整版，带 `lines` 上下文）：
1. 若 `ctx == nil || leftTrimmed == nil || rightTrimmed == nil`，返回 false。
2. **规则 1（公文标题→称谓行强制断开）**：若 `isOfficialDocumentTitleTail(leftTrimmed)` 且 `isAddresseeSalutationLine(rightTrimmed)`，返回 true。
3. **规则 2（涉及标题行的特殊处理）**：若 `leftTrimmed` 或 `rightTrimmed` 匹配 `HEADING_LINE`：
   a. 取 `leftHeading := HEADING_LINE.matcher(leftTrimmed)`。
   b. 若 `leftHeading.matches()` 且 `rightTrimmed` **不**匹配 `HEADING_LINE`，且 `isListItemBodyContinuation(leftHeading.group(2), rightTrimmed)` 为真，且 `!isPreformattedClassifiedLine(lines, rightLineId, rightTrimmed)`：返回 **false**（左行是带 `#` 的列表式标题，右行是它的正文续行，允许合并——但右行若被分类为预格式化，则不允许，防止绕过代码块保护）。
   c. 否则：返回 `!isMultiLineSourceAtxTitlePair(leftTrimmed, rightTrimmed, leftLineId, rightLineId, ctx)`（除非两者是“同一个硬换行的多行源 ATX 标题”的特殊情况，否则涉及标题行的组合一律禁止合并）。
4. **规则 3**：若 `MarkdownWeakMergeHeuristics.isListLikeLine(rightTrimmed)`，返回 true（右行是列表项，禁止被并入上一行——注意这里不检查 `isListItemBodyContinuation` 特例，与其它地方处理列表续行的方式不同，这是"定稿后"这一层更严格的规则）。
5. **规则 4**：若 `isCommandOrConfigLikeLine(leftTrimmed)` 或 `isCommandOrConfigLikeLine(rightTrimmed)`，返回 true。
6. **规则 5（层级行保护）**：若 `isHierarchyLine(leftLineId, ctx)` 或 `isHierarchyLine(rightLineId, ctx)`（已定稿的层级标题行）：
   - 若 `isMultiLineSourceAtxTitlePair(...)` 为真，返回 false（同一硬换行标题的特殊豁免）。
   - 否则返回 true（层级行原则上不与正文合并）。
7. **规则 6（标题续行形状检查）**：若 `looksLikeWrappedTitleContinuation(leftTrimmed, rightTrimmed)`：
   a. 分别用 `lines`（若提供且索引合法）或纯文本方式对 `leftTrimmed`/`rightTrimmed` 调用 `MarkdownLineClassifier.classify`/`classifyText` 得到 `leftShapeCheck`/`rightShapeCheck`。
   b. 若满足以下任一条件，返回 true（禁止）：
      - `leftShapeCheck == PREFORMATTED` 或 `rightShapeCheck == PREFORMATTED`；
      - `leftShapeCheck == HEADING` 且 `leftTrimmed` **不**匹配 `HEADING_LINE`（即形似标题但不是真正的 `# ` 前缀标题）；
      - `rightShapeCheck == HEADING` 且 `rightTrimmed` **不**匹配 `HEADING_LINE`；
      - `MarkdownLineClassifier.looksLikeBareTechnicalToken(leftTrimmed)` 或 `looksLikeBareTechnicalToken(rightTrimmed)`。
   c. 否则：`wrapLeftCover := isDocumentCoverTitleMetadataLine(leftTrimmed)`；`wrapRightCover := isDocumentCoverTitleMetadataLine(rightTrimmed)`；若两者都不是封面元数据行，返回 false（放行）；若其中至少一个是封面元数据行，**不在此处返回**，继续往下走后续规则（即：标题续行 + 封面元数据行的组合不会被规则 6 直接放行，要接受规则 7 及之后的进一步检查）。
8. **规则 7（重新计算行分类，供后续规则使用）**：`leftKind`/`rightKind` = 对 `leftTrimmed`/`rightTrimmed` 的分类结果（与规则 6 里的计算逻辑相同，若规则 6 未触发提前返回则这里重新算一遍——Go 实现可考虑复用规则 6 里已经算好的值以避免重复计算，只要保证结果一致）。
9. 若 `blocksClassifiedBodyMerge(leftKind, rightKind, leftTrimmed, rightTrimmed)` 为真，返回 true。
10. **规则 8（源标题行保护）**：`leftSource := isSourceMarkdownHeadingLine(leftLineId, ctx)`；`rightSource := isSourceMarkdownHeadingLine(rightLineId, ctx)`；若两者任一为真：
    - 若 `isMultiLineSourceAtxTitlePair(...)` 为真，返回 false；
    - 否则返回 true。
11. **规则 9（封面元数据行组合）**：`leftCover`/`rightCover` = 是否为封面标题元数据行；
    - 若两者都是封面行，返回 true；
    - 若（`leftSource && rightCover`）或（`leftCover && rightSource`），返回 true。
12. **规则 10（红头元数据行）**：若（`isOcrRedHeaderMetadataLine(leftTrimmed)` 或 `isOcrRedHeaderMetadataLine(rightTrimmed)`）且 `!looksLikeWrappedTitleContinuation(leftTrimmed, rightTrimmed)`，返回 true。
13. **规则 11（日期行）**：若 `isChineseDateLine(rightTrimmed)` 或 `isChineseDateLine(leftTrimmed)`，返回 true。
14. 全部规则通过，返回 false（允许合并）。

**移植关键点**：这是一个**长串按序判断、一票否决**的规则链，Go 实现必须严格保持规则出现的顺序（因为部分规则之间存在提前返回，改变顺序会导致行为不一致，例如规则 6 的“形状检查”会在特定条件下直接返回 true，如果这一步被后移，规则 7-13 可能会先行错误放行）。

### 算法：`isHierarchyLine` / `isSourceMarkdownHeadingLine`
- `isHierarchyLine(lineId, ctx)`：`lineId >= 0 && ctx.hierarchyLineIndexes.contains(lineId)`。
- `isSourceMarkdownHeadingLine(lineId, ctx)`：`lineId >= 0 && ctx.sourceMarkdownHeadingLineIndexes.contains(lineId)`。

### 算法：`isCommandOrConfigLikeLine(trimmed)`
1. 若为空，返回 false。
2. `t := trimmed.strip()`。
3. 若整行匹配 `^(?:sudo\s+)?(?:rpm|yum|dnf|apt(?:-get)?|chkconfig|service|systemctl|iscsiadm|scsi_id|udevadm|multipath|oracleasm|start_udev|fdisk|lsblk|mount|umount|vi|vim|cat|grep)\b.*`，返回 true（此正则不含环视，可直接照搬）。
4. 若整行匹配 `^[A-Z_{}]+==.*` 或 `^[A-Z_{}]+\+?=.*`，返回 true。
5. 返回 `t` 以 `/etc/` 或 `/dev/` 开头。

### 算法：`isMultiLineSourceAtxTitlePair(leftTrimmed, rightTrimmed, leftLineId, rightLineId, ctx)`
1. 若 `!looksLikeWrappedTitleContinuation(leftTrimmed, rightTrimmed)`，返回 false。
2. 若 `leftTrimmed` 或 `rightTrimmed` 不匹配 `HEADING_LINE`（即不是两者都带 `#`），返回 false。
3. 返回 `isSourceMarkdownHeadingLine(leftLineId, ctx) && isSourceMarkdownHeadingLine(rightLineId, ctx)`（两行都必须是**源文档**本来就带的 `#`，不是本管线新生成的标题）。

### 算法：`isChineseDateLine(line)`（公开方法，被多处引用）
1. 若为空白，返回 false。
2. `t := stripEdgeWhitespace(line)`。
3. 返回 `PdfToMarkdown.isStandaloneChineseDateLine(t)`（外部依赖，非本 Part 范围）`|| t 匹配 ^[0-9０-９]{4}年.*`（简单兜底：以 4 位数字（含全角）+ `年` 开头即视为中文日期行）。

### 算法：`endsWithTruncatedTimeColon(left, right)`
1. 若任一为 `nil`，返回 false。
2. `l := left.strip()`；`r := right.strip()`；任一为空返回 false。
3. `last := l` 最后一个字符；若既不是 `:` 也不是 `：`，返回 false。
4. 若 `len(l) < 2` 或 `l[len(l)-2]` 不是数字，返回 false。
5. 返回 `r` 首字符是否为数字。

### 算法：`lineStartsWithStructuralHeadingOrListPrefixForBodyMerge(line, ctx)`
1. 若 `line` 为空白，返回 false。
2. 若 `ctx != nil` 且 `HeadingPatternQualityHeuristics.isLineDisqualifiedForHeadingMerge(line, ctx.disqualifiedPatternKeys)`（外部依赖：该行匹配的标题模式已被判定为“不合格模式”），返回 **false**（即：虽然形状像标题/列表前缀，但由于该模式已被证明不可靠，不阻断合并）。
3. 否则返回 `MarkdownWeakMergeHeuristics.lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(line)`。

### 算法：`mergeWeakBodyLinesOutsideFences(lines, ctx)`
（阶段 3 的第二遍合并，围栏外弱合并，处理两行/三行模式，与 `mergeWrappedBodyLines` 相比更保守，不做贪心多行扩展，每次只合并 2 或 3 行）

1. 若 `lines` 为空，原样返回。
2. `columnMinLenHint := weakMergeMinColumnNormalizedLenHint(lines)`。
3. 维护 `inFence := false`；`i := 0`。
4. 循环 `i < len(lines)`：
   a. `raw := lines[i]`；`t := stripEdgeWhitespace(raw)`。
   b. 若 `t` 以 ` ``` ` 开头：翻转 `inFence`，加入输出，`i++`，continue。
   c. 若 `inFence`：原样加入输出，`i++`，continue。
   d. **三行模式（`a` + 空行 + `c`）**：若 `i+2 < len(lines)` 且 `lines[i+1]` trim 后为空：
      - `a := t`；`c := stripEdgeWhitespace(lines[i+2])`；`thirdPeek := peekTrimmedImmediateNextLine(lines, i+2)`（即第 4 行，供三段短句规则使用）。
      - 若 `a`、`c` 均非空 且 `shouldJoinWeakBodyContinuation(a, c, columnMinLenHint, thirdPeek, ctx, lines, i, i+2)` 为真：
        - 输出 `stripTrailingWhitespaceForMerge(lines[i]) + c`（注意：这里对左行只去除**尾部**空白，不是完全 trim，保留左行内部可能有意义的前导空白；然后直接与右行拼接，**不插入任何分隔符**，也不经过 `joinWrappedLines` 的字母数字间加空格逻辑）。
        - `i += 3`；continue（跳过中间的空行和 `c` 行）。
   e. **两行模式（`a` + `b` 相邻）**：若 `i+1 < len(lines)`：
      - `a := t`；`b := stripEdgeWhitespace(lines[i+1])`；`thirdPeek := peekTrimmedImmediateNextLine(lines, i+1)`。
      - 若 `a`、`b` 均非空 且 `shouldJoinWeakBodyContinuation(a, b, columnMinLenHint, thirdPeek, ctx, lines, i, i+1)` 为真 且 `!isChineseDateLine(b)` 且 `!(a 以局结尾 且 b 匹配 ^[0-9０-９]{4}年.*)`（这两个额外条件是在通用判断函数之外的**双保险**，与函数内部已有的日期检查重复但保留原样）：
        - 输出 `a + b`（**直接拼接，不加空格，不经过 `joinWrappedLines`**——注意与三行模式不同，这里两行模式合并的是 `t`（trim 后的左行）而不是原始 `raw`）。
        - `i += 2`；continue。
   f. 若三行/两行模式都未触发：原样加入输出 `raw`，`i++`。
5. 返回输出列表。

**移植提醒**：三行模式合并时左行用的是 `stripTrailingWhitespaceForMerge(lines[i])`（只去尾部空白，保留原始形态），两行模式合并时左行用的是 `t`（`stripEdgeWhitespace` 后的结果，去掉首尾空白）——两种模式对左行的处理方式不同，务必分别对应，不要统一成一种预处理。

### 算法：`shouldJoinWeakBodyContinuation(a, b, columnMinLenHint, thirdLinePeek, ctx, lines, lineIndexA, lineIndexB)`
（弱合并的核心正向判断函数，按序检查，任一失败即返回 false，全部通过才返回 true）

1. 若 `isChineseDateLine(a)` 或 `isChineseDateLine(b)`，返回 false。
2. 若 `!canEnterBodyMerge(lines, lineIndexA, lineIndexB, a, b)`，返回 false。
3. 若 `a` 以 `局` 结尾 且 `b` 匹配 `^\d{4}年.*`（**注意**：这里只匹配 ASCII 数字，不含全角，与其它地方 `[0-9０-９]` 略有不同，属于原文的不一致，移植时应按原文精确复刻，不要"修正"成统一版本），返回 false。
4. 若 `a` 以 `关于` 开头 且 `len(a) >= 12`，返回 false。
5. 若 `b` 以 `关于` 开头 且（`isOcrDocumentReferenceNumberLine(a)` 或 `a` 以 `号` 结尾），返回 false。
6. 若 `endsWithTerminalPunctuation(a)`，返回 false。
7. 若 `isListLikeLine(b)` 或（`isListLikeLine(a) 且 !isListItemBodyContinuation(a,b)`），返回 false。
8. 若 `b` 以 `|`、`#`、`- `、`* ` 开头，返回 false。
9. 若 `shouldBlockBodyMergePair(lines, lineIndexA, lineIndexB, a, b, ctx)`，返回 false。
10. 若 `lines != nil` 且 `lineIndexA >= 0` 且 `lineIndexB > lineIndexA + 1`（即跨越了至少一行，说明是三行模式场景）且 `looksLikeDistinctParagraphBoundaryAcrossBlank(a, b)`，返回 false。
11. **重复检查**（与步骤 6 完全相同）：若 `endsWithTerminalPunctuation(a)`，返回 false（Java 源码原样重复了这一判断两次，移植时保留即可，不影响正确性但为保持 1:1 对照建议保留注释说明）。
12. 若 `endsWithAsciiOrFullwidthColon(b)` 且 `isShortUnpunctuatedPhrase(a)`，返回 false。
13. 若 `MarkdownWeakMergeHeuristics.shouldBlockOcrWeakMergePair(a, b, thirdLinePeek)`，返回 false。
14. 若 `MarkdownWeakMergeHeuristics.weakMergeBlocksAsPreviousLine(a, b, columnMinLenHint)`，返回 false。
15. 若 `lineStartsWithStructuralHeadingOrListPrefixForBodyMerge(b, ctx)`，返回 false。
16. 返回 `weakMergeLineHasIdeograph(a) && weakMergeLineHasIdeograph(b)`（**两行都必须含有表意汉字字符**才允许弱合并——这是弱合并区别于 `mergeWrappedBodyLines` 主合并的一个重要限制，纯 ASCII/无中文内容的短句对不会被这条规则合并）。

### 算法：`isPreformattedClassifiedLine(lines, lineId, trimmed)`
1. `kind := 若 lines 非空且 lineId 合法则 classify(lines, lineId) 否则 classifyText(trimmed)`。
2. 返回 `kind == PREFORMATTED`。

### 算法：`canEnterBodyMerge(lines, leftLineIndex, rightLineIndex, leftText, rightText)`
1. 分别计算 `leftKind`、`rightKind`（同上述模式：有 `lines`+合法索引则走 `classify`，否则走 `classifyText`）。
2. 返回 `!blocksClassifiedBodyMerge(leftKind, rightKind, leftText, rightText)`。

### 算法：`blocksClassifiedBodyMerge(leftKind, rightKind, leftText, rightText)`
1. 若 `leftKind == LIST_ITEM && rightKind == NATURAL_TEXT && isListItemBodyContinuation(leftText, rightText)`，返回 false（列表项+其正文续行的特例，不阻断）。
2. 若 `leftKind == HEADING`：用 `HEADING_LINE` 匹配 `stripEdgeWhitespace(leftText 或空串)` 得到 `leftHeading`；若 `rightKind == NATURAL_TEXT` 且 `leftHeading.matches()` 且 `isListItemBodyContinuation(leftHeading.group(2), rightText)`，返回 false（`#` 标题本身是列表式条目、右行是其续行的特例）。
3. 否则返回 `leftKind.blocksBodyMerge() || rightKind.blocksBodyMerge()`（`blocksBodyMerge()` 是 Part 5 定义在 `MarkdownLineKind` 枚举上的方法，需人工核对哪些 kind 返回 true——按常理应为 `FENCE`/`TABLE`/`QUOTE_OR_RULE`/`PREFORMATTED` 等结构性种类，具体以 Part 5 文档为准）。

### 算法：`isListItemBodyContinuation(left, right)`
（判断左行是否是“悬空列举引导句”，右行是否应作为其正文续行合并）

1. 若 `right` 为空白，返回 false。
2. `l := normalizeLine(left)`；`r := normalizeLine(right)`。
3. 若 `len(l) < 18`，返回 false（太短不像引导句）。
4. 若 `endsWithTerminalPunctuation(l)`，返回 false。
5. `hasUnclosedBookQuote := countOccurrences(l, '《') > countOccurrences(l, '》')`（左行有未闭合的书名号，说明引用还没写完，下一行大概率是引用内容的延续）。
6. 若 `!l.contains("：") && !l.contains(":") && !hasUnclosedBookQuote && MarkdownTitlePattern.matchFirst(l) != nil`：返回 false（左行本身就是一个完整的编号标题——如"四、提交投标文件……地点"，`、`只是标题自带的编号分隔符，不是悬空列举引导，除非行内还有冒号或未闭合书名号才继续往下判断）。
7. 若 `isListLikeLine(r)` 或 `r` 匹配 `HEADING_LINE` 或 `isTableLikeLine(r)` 或 `isQuoteOrRuleLine(r)`，返回 false。
8. 若 `MarkdownWeakMergeHeuristics.lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(r)`，返回 false（下一行本身以层级标题/列表前缀开头，不能被吞并）。
9. 返回 `l.contains("：") || l.contains(":") || l.contains("，") || l.contains("、")`（左行内部必须含有至少一种句读符号——冒号、逗号或顿号——才算合格的引导句）。

### 算法：`countOccurrences(text, ch)`
简单遍历计数字符出现次数（Go: `strings.Count`）。

### 算法：`stripTrailingWhitespaceForMerge(s)`
`s.replaceAll("[\\s\\u3000]+$", "")`（去除字符串末尾的常规空白字符及全角空格 `　`）。Go: 用 `regexp.MustCompile(` \s\x{3000}]+$`).ReplaceAllString` 或手写 `strings.TrimRight` 配合自定义判断函数（注意 Go 的 `\s` 在 UTF-8 正则中默认不含 `　`，需要显式加入）。

### 算法：`looksLikeDistinctParagraphBoundaryAcrossBlank(left, right)`
1. 若任一为 `nil`，返回 false。
2. `l := left.strip()`；`r := right.strip()`；若任一长度 `< 6`，返回 false。
3. 若 `endsWithTerminalPunctuation(l)`，返回 true。
4. 返回 `r` 是否匹配以下任一：
   - `^第[一二三四五六七八九十百千万\d]+段.*`
   - `^[一二三四五六七八九十百千万]+[、．.].*`
   - `^[（(][一二三四五六七八九十百千万]+[)）].*`

### 算法：`isPageBoundaryGap(blankIndex, pageBoundaryAnchors)`
1. 若 `blankIndex < 0` 或锚点集合为空，返回 false。
2. 返回 `pageBoundaryAnchors` 是否包含 `blankIndex-1`、`blankIndex` 或 `blankIndex+1` 中任意一个（**容忍 ±1 的偏移**，因为页码剔除后行号会整体偏移，具体偏移方向取决于剔除逻辑的实现细节，索性检查三个相邻位置）。

### 算法：`isCjkUnifiedIdeograph(ch)`（`MarkdownBodyMergeStage` 内部版本）
覆盖 7 个 Unicode 区块：`CJK_UNIFIED_IDEOGRAPHS`、`EXTENSION_A`、`EXTENSION_B`、`EXTENSION_C`、`EXTENSION_D`、`EXTENSION_E`、`EXTENSION_F`。**注意此版本比 `MarkdownLineClassifier.isCjk` 多出 C/D/E/F 四个扩展区**，两处判断范围不同，Go 移植时需要建两个不同的辅助函数（例如 `isCjkBasic`（4 区块，供 LineClassifier 用）与 `isCjkExtended`（7 区块，供 BodyMergeStage 用）），不要合并为一个统一函数。

### 弱合并判据优先级总结（供实现时整体核对顺序）

在 `mergeWrappedBodyLines` 的相邻行判断路径中，判定“能否合并两行”的完整顺序是：

1. 基本形状排除（空、围栏）
2. 标题续行形状（`looksLikeWrappedTitleContinuation`）→ 若命中，只再检查 `shouldBlockBodyMergePair`，其它规则全部跳过
3. 行分类黑名单（`canEnterBodyMerge`/`blocksClassifiedBodyMerge`）
4. “关于”长句起句排除
5. `shouldBlockBodyMergePair` 完整规则链（公文标题→称谓行、标题行组合、列表项、命令/配置行、层级行保护、封面元数据、红头元数据、日期行……见上方专门小节的 14 条规则）
6. 列表项/正文续行特例（`isListItemBodyContinuation`）
7. 表格/引用行排除
8. 终止标点排除（含截断时间冒号特例）
9. 冒号收尾+短语排除
10. 中文日期行排除
11. 右行首字符类型检查（必须是文字/数字）

`mergeWeakBodyLinesOutsideFences`（弱合并第二遍）在此基础上额外叠加：
12. 两行都必须含表意汉字（`weakMergeLineHasIdeograph`）
13. 三段短句规则（`shouldBlockTripleUnpunctuatedPhrasePair`，仅在有第三行窥视时启用）
14. 版心最小行长动态阈值（`weakMergeBlocksAsPreviousLine`，纲要短行判断是否为长条款起句）

---

## MarkdownWeakMergeHeuristics（补全上一节未展开的具体算法）

### 职责
（已在上方"数据结构"与常量表描述职责概览，此处补全具体算法。）

### 算法：`lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(nextLine)`
1. 若 `nextLine` 为空白，返回 false。
2. `edge := stripEdgeWhitespace(nextLine)`；若为空，返回 false。
3. `norm := normalizeLine(edge)`；若非空 且 `MarkdownTitlePattern.matchFirst(norm) != nil`，返回 true。
4. 否则返回 `LIST_ITEM_LINE.matches(edge) || LIST_SCOPE_ITEM_CANDIDATE.matches(edge)`（这里用的是 `MarkdownPipelineLineUtils.LIST_ITEM_LINE`/`LIST_SCOPE_ITEM_CANDIDATE`，Part 5 定义，注意区分本类内部同名局部正则 `LIST_ITEM_LINE`——本类自己也定义了一份 `LIST_ITEM_LINE` 常量，但此处调用的是 `MarkdownPipelineLineUtils.` 前缀限定的版本，两者可能定义不同，Go 实现要精确对应引用来源，不要混用）。

### 算法：`isOcrMarginAttachmentLabelLine(line)`
1. 若为空白，返回 false。
2. 返回 `OCR_MARGIN_ATTACHMENT_LABEL.matches(normalizeLine(stripEdgeWhitespace(line)))`（匹配纯"附件"或"附件 N"独占一行，无冒号）。

### 算法：`isOcrRedHeaderMetadataLine(line)`
1. 若为空白，返回 false。
2. `norm := normalizeLine(stripEdgeWhitespace(line))`。
3. 若 `norm` 包含 `签发人`，返回 true。
4. 若 `isOcrCopyNumberLine(norm)` 或 `isOcrReceiptStampLine(norm)`，返回 true。
5. 返回 `isOcrDocumentReferenceNumberLine(norm)`。

### 算法：`isOcrCopyNumberLine(norm)`
返回 `OCR_COPY_NUMBER.matches(norm)`（`^(?:份号[:：]?\s*)?\d{2,6}\s*$`）。

### 算法：`isOcrReceiptStampLine(norm)`
1. 若 `norm` 为空白或包含 `关于`，返回 false。
2. 返回 `OCR_DOC_REF_SEQ_FRAGMENT.find(norm)`（`第\s*[0-9一二三四五六七八九十百千万]+\s*号`，子串查找）。

### 算法：`isOcrGovernmentTitleLine(norm)`
1. 若为空白，返回 false。
2. 若 `norm` 含 `关于` 且 `OCR_GOVERNMENT_TITLE_TERMINAL.find(norm)`，返回 true。
3. 否则返回 `OCR_GOVERNMENT_TITLE_TERMINAL.find(norm) && len(norm) <= 120`。

### 算法：`isOcrRecipientBlockLine(norm)`
1. 若为空白，返回 false。
2. 若 `endsWithAsciiOrFullwidthColon(norm)` 且 `len(norm) >= 12` 且 `countChar(norm,'、') >= 1` 且 `!endsWithDigitBeforeColon(norm)`，返回 true。
3. 若 `countChar(norm,'、') >= 3` 且 `!endsWithTerminalPunctuation(norm)`，返回 true。
4. 返回 `OCR_RECIPIENT_ORG_CHAIN.lookingAt(norm)`（**注意**：`lookingAt` 是 Java 特有语义——从字符串**开头**尝试匹配，不要求匹配到字符串末尾，但也不同于 `find`（可以从任意位置开始）。Go `regexp` 没有直接等价方法，需要用 `re.FindStringIndex(norm)` 并检查返回结果的起始位置是否为 0（`loc[0] == 0`）来模拟 `lookingAt` 语义）。

### 算法：`endsWithDigitBeforeColon(norm)`
1. 若 `len(norm) < 2`，返回 false。
2. 返回 `norm` 倒数第二个字符是否为数字（说明冒号前紧跟数字，是被截断的时间值如"8:"，不是真正的称谓收束冒号）。

### 算法：`isDocumentCoverTitleMetadataLine(line)`
1. 若为空白，返回 false。
2. `norm := normalizeLine(stripEdgeWhitespace(line))`；若为空，返回 false。
3. 若 `isOcrRedHeaderMetadataLine(norm)`，返回 true。
4. 若 `norm` 匹配 `.*[。！？].*`（含中文句末标点），返回 **false**（含句末标点几乎必是完整正文句而非封面标签行，提前排除）。
5. 若 `norm` 含 `编制` 且（含 `审核` 或 `批准` 或 `时间`），返回 true。
6. 若 `norm` 含 `审核时间` 或 `批准时间`，返回 true。
7. 若 `DOCUMENT_COVER_TITLE_LINE.find(norm)`（`编制|审核|批准|技术有限公司|总经理办公会`），返回 true。
8. 返回 `norm` 含 `报销制度` 且匹配 `.*[A-Za-z]{2,}[-/][A-Za-z0-9./-]+.*`（形似文档编号如 `ABC-001/v2`）。

### 算法：`looksLikeWrappedTitleContinuation(left, right)`
（判断两行是否构成标题的硬换行续行——用于**非定稿层级标题**的场景，也用于两行均为源 ATX 标题时判断是否属于同一硬换行标题）

1. 若任一为空白，返回 false。
2. `leftRaw := stripEdgeWhitespace(left)` 去掉开头的 `#{1,6}\s*` 前缀（正则替换，`^#{1,6}\s*` → 空），再 trim；`rightRaw` 同理。
3. 若 `leftRaw` 含 `印发` 且（`rightRaw == "的通知"` 或 `rightRaw` 以 `的通知` 开头），返回 true。
4. 若（`leftRaw` 以 `局`/`委`/`厅` 结尾）且 `rightRaw` 以 `关于` 开头，返回 true。
5. `l := normalizeLine(stripEdgeWhitespace(left))`；`r := normalizeLine(stripEdgeWhitespace(right))`；若任一为空，返回 false。
6. 若 `endsWithTerminalPunctuation(l)`，返回 false（左行已是完整句子，不是续行场景）。
7. 若 `endsWithAsciiOrFullwidthColon(r)` 且 `isShortUnpunctuatedPhrase(l)`，返回 false。
8. 若 `r` 匹配 `^(?:时间|编制|审核|批准|准)\s*[：:].*`，返回 false（右行是封面栏位标签本身，不是续接）。
9. 若 `isDocumentCoverTitleMetadataLine(r)` 且 `len(r) < 72` 且 `!r.startsWith("《")`，返回 false。
10. 若 `r` 以 `《`、`（` 或 `(` 开头，返回 true。
11. 若 `r` 以 `关于` 或 `印发` 开头：
    - 若 `l` 含 `签发人`，或 `isOcrRedHeaderMetadataLine(l)`，或 `isOcrDocumentReferenceNumberLine(l)`，返回 false。
    - 否则返回 `l` 以 `局`/`委`/`厅`/`公司`/`政府`/`中心` 结尾，或 `len(l) >= 10`。
12. 若 `l` 以 `关于` 开头 且 `r` **不**以 `关于` 或 `印发` 开头 且 `len(l) >= 12`，返回 false。
13. 若 `len(l) >= 12`：`c := r` 首字符；返回 `isIdeographic(c) || isLetterOrDigit(c)`。
14. 若 `len(l) >= 4`：`c := r` 首字符；`orgLikeTail := l 以 局/委/厅/公司/中心/政府 结尾`；返回 `orgLikeTail && isIdeographic(c)`。
15. 否则返回 false。

### 算法：`isOcrDocumentReferenceNumberLine(norm)`
1. 若为空，返回 false。
2. 若 `len(norm) > OCR_DOC_REFERENCE_MAX_LEN(40)` 或 `< 6`，返回 false。
3. 若 `!norm.endsWith("号")`，返回 false。
4. 若 `norm` 含 `印发`，或以 `的通知` 结尾，或含 `关于`，返回 false。
5. 若 `OCR_DOC_REFERENCE_YEAR.find(norm)`（含年份括号），返回 true。
6. 返回 `OCR_DOC_REFERENCE_CN_SEQ.find(norm)`（无年份的顺序文号）。

### 算法：`shouldBlockOcrWeakMergePair(prevLine, nextLine, thirdLinePeek)`
1. 若 `prevLine` 或 `nextLine` 为空白，返回 false。
2. `a := stripEdgeWhitespace(prevLine)`；`b := stripEdgeWhitespace(nextLine)`；任一为空返回 false。
3. 若 `isOcrMarginAttachmentLabelLine(a)`，返回 true。
4. 若 `isOcrRedHeaderMetadataLine(a)` 或 `isOcrRedHeaderMetadataLine(b)`，返回 true。
5. 若 `shouldBlockGovernmentHeaderWeakMerge(a, b)`，返回 true。
6. 若 `b` 以 `关于` 开头 且（`isOcrDocumentReferenceNumberLine(a)` 或 `a` 以 `号` 结尾），返回 true。
7. 若 `!isShortUnpunctuatedPhrase(a) || !isShortUnpunctuatedPhrase(b)`，返回 **false**（两行都不是短语，交由其它规则处理，本函数不阻断）。
8. `third := thirdLinePeek 若非空白则 trim，否则 nil`。
9. 若 `third == nil`：
   - 若 `isChineseDateLine(b)` 且 `a` 以 `局`/`委`/`厅`/`分局` 结尾，返回 true；
   - 否则返回 false。
10. 否则返回 `shouldBlockTripleUnpunctuatedPhrasePair(a, b, third)`。

### 算法：`shouldBlockGovernmentHeaderWeakMerge(a, b)`
1. `aNorm := normalizeLine(a)`；`bNorm := normalizeLine(b)`；任一为空返回 false。
2. 若 `isOcrCopyNumberLine(aNorm)` 或 `isOcrCopyNumberLine(bNorm)`，返回 true。
3. 若 `isOcrReceiptStampLine(aNorm)` 或 `isOcrReceiptStampLine(bNorm)`，返回 true。
4. 若 `isOcrDocumentReferenceNumberLine(aNorm)` 或 `isOcrDocumentReferenceNumberLine(bNorm)`，返回 true。
5. 若 `isChineseDateLine(aNorm)` 或 `isChineseDateLine(bNorm)`，返回 true（此处调用的是 `MarkdownBodyMergeStage.isChineseDateLine`，跨类调用）。
6. 若 `isOcrGovernmentTitleLine(aNorm)` 且 `!isOcrGovernmentTitleLine(bNorm)`，返回 true。
7. 若 `isOcrRecipientBlockLine(aNorm)` 或 `isOcrRecipientBlockLine(bNorm)`，返回 true。
8. 若 `OCR_BODY_OPENING.lookingAt(bNorm)`（`^根据|^现将|^为落实|^按照|^特此`，Java `lookingAt` 语义——从头部开始匹配即可，同样需用 Go 手动模拟“匹配位置为 0”），返回 true。
9. 若（`isOcrReceiptStampLine(aNorm)` 或 `isOcrCopyNumberLine(aNorm)` 或 `isOcrDocumentReferenceNumberLine(aNorm)`）且（`bNorm` 含 `关于` 或以 `省林业局` 开头），返回 true。
10. 否则返回 false。

### 算法：`isShortUnpunctuatedPhrase(line)`
1. 若为空白，返回 false。
2. `edge := stripEdgeWhitespace(line)`；若为空返回 false。
3. 若 `isFenceLine(edge)` 或 `isHeadingLikeLine(edge)` 或 `isListLikeLine(edge)` 或 `isTableLikeLine(edge)` 或 `isQuoteOrRuleLine(edge)`，返回 false（结构性行都不算“短语”）。
4. `norm := normalizeLine(edge)`；若为空或 `len(norm) > TRIPLE_SHORT_PHRASE_MAX_LEN(40)`，返回 false。
5. 若 `endsWithTerminalPunctuation(norm)`，返回 false。
6. `punct := countWeakMergeClausePunctInBodyText(norm)`；若 `punct >= 2`，返回 false。
7. 若 `len(norm) >= 24 && punct >= 1`，返回 false。
8. 否则返回 true。

### 算法：`shouldBlockTripleUnpunctuatedPhrasePair(leftTrimmed, rightTrimmed, third)`
1. 若任一为空，返回 false。
2. 若 `!isShortUnpunctuatedPhrase(leftTrimmed) || !isShortUnpunctuatedPhrase(rightTrimmed)`，返回 false。
3. 若 `third` 是围栏行/标题行/列表项/表格行/引用分隔线（任一），返回 true。
4. 若 `endsWithAsciiOrFullwidthColon(third)`，返回 true。
5. 返回 `!endsWithTerminalPunctuation(third)`（**三段短句判定的核心**：如果第一、二行都是短语，而第三行既不是结构性行、也不以冒号收尾、但同时也**不以句末标点结尾**，则说明这三行更可能是三段独立的短纲/标题片段而非应该合并的正文——因此阻断前两行的合并）。

### 算法：`weakMergeMinColumnNormalizedLenHint(lines)`
1. 若 `lines` 为空，返回 0。
2. 维护 `inFence := false`；`min := +∞`。
3. 对每一行：
   - `t := stripEdgeWhitespace(raw)`；若以 ` ``` ` 开头，翻转 `inFence`，`continue`；若在围栏内，`continue`。
   - `norm := normalizeLine(t)`；若 `len(norm) < WEAK_MERGE_COLUMN_HINT_MIN_PARTICIPANT_LEN(12)`，`continue`。
   - 若 `!weakMergeLineHasIdeograph(norm)`，`continue`（不含中文表意字符的行不参与版心宽度统计）。
   - `min = min(min, len(norm))`。
4. 若 `min == +∞`（没有任何行参与统计），返回 0；否则返回 `min`。

### 算法：`weakMergeLineHasIdeograph(norm)`
遍历字符，若任一字符 `Character.isIdeographic` 为真，返回 true；否则遍历完返回 false。
（**注意**：Java `Character.isIdeographic` 是一个通用的"表意文字"判断，覆盖范围比单纯的 CJK Unicode 区块判断更广（包含如彝文、纳西东巴文等其它表意文字系统，但实践中主要命中场景是中文）。Go 标准库没有直接等价函数，需要用 `unicode.Is(unicode.Ideographic, r)`——Go 的 `unicode` 包提供了 `Ideographic` range table，语义对应 Unicode `Ideographic` 属性，应与 Java `Character.isIdeographic` 高度一致，可直接使用。)

### 算法：`weakMergeResolveEffectiveBodyMinLen(columnHint, pairMin)`
1. `cap := WEAK_MERGE_TITLE_STUB_BODY_MIN_LEN(18)`；`slack := WEAK_MERGE_COLUMN_HINT_SLACK(2)`；`floor := WEAK_MERGE_TITLE_STUB_BODY_DYNAMIC_FLOOR(12)`。
2. `candidate := cap`。
3. 若 `columnHint >= WEAK_MERGE_COLUMN_HINT_MIN_PARTICIPANT_LEN(12)`：`candidate = min(candidate, max(floor, columnHint - slack))`。
4. 若 `pairMin > 0`：`candidate = min(candidate, max(floor, pairMin - slack))`。
5. 返回 `candidate`。

### 算法：`weakMergeAltMinLenForLongClause(effectiveBodyMinLen)`
1. `cap := 18`；`baseAlt := WEAK_MERGE_TITLE_STUB_BODY_ALT_MIN_LEN(28)`。
2. 返回 `max(baseAlt - (cap - effectiveBodyMinLen), effectiveBodyMinLen + 8)`。

### 算法：`weakMergeBlocksAsPreviousLine(prevLine, nextLine[, columnMinNormalizedLenHint])`
（单参数重载等价于 `columnMinNormalizedLenHint=0`）

1. 若 `prevLine` 为空白，返回 false。
2. 若 `!lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(prevLine)`，返回 false（本函数只处理"上一行本身是纲要/列表前缀起手"的场景，否则不阻断）。
3. `normA := normalizeLine(stripEdgeWhitespace(prevLine))`；`lenA := len(normA)`。
4. `lenB := 若 nextLine 非空白则 normalizeLine(stripEdgeWhitespace(nextLine)) 的长度，否则 0`。
5. `pairMin := 若 lenA==0 或 lenB==0 则 0，否则 min(lenA, lenB)`。
6. `effectiveMinLen := weakMergeResolveEffectiveBodyMinLen(columnMinNormalizedLenHint, pairMin)`。
7. 若 `!headingPrefixLineLooksLikeLongClauseForWeakMerge(prevLine, nextLine, effectiveMinLen)`，返回 **true**（不够长/不够像长条款，判定为短纲，阻断合并）。
8. 若 `nextLine` 为空白，返回 false（没有下一行可判断额外条件，放行）。
9. `b := stripEdgeWhitespace(nextLine)`；若为空返回 false。
10. 若 `lineStartsWithStructuralHeadingOrListPrefixForWeakMerge(b)`，返回 true。
11. 若 `b` 最后一个字符是 `:` 或 `：`，返回 true。
12. 否则返回 false。

### 算法：`countWeakMergeClausePunctInBodyText(body)`
1. 若为空，返回 0。
2. 对 `body` 做 NFKC 规范化（Unicode 标准化，Go 用 `golang.org/x/text/unicode/norm` 包的 `norm.NFKC.String(body)`）。
3. 遍历规范化后的字符，统计属于常量 `WEAK_MERGE_BODY_SENTENCE_PUNCT`（`，、。；：！？…,.;:!?､`，注意 `､` 是半角句读点 `｡` 的变体，需要精确包含在 Go 字符集合中）中的字符数，返回计数。

### 算法：`weakMergeBodyAfterStructuralTitlePrefix(norm)`
1. 若为空白，返回空串。
2. `tp := MarkdownTitlePattern.matchFirst(norm)`；若为 `nil`，返回 `norm` 原样。
3. `rest := MarkdownTitlePattern.stripBodyEnumerationPrefix(norm, tp)`（Part 5 提供）。
4. 若 `rest != norm`（确实被剥离了内容）或 `MarkdownTitlePattern.isBodyEnumerationPattern(tp)`（Part 5 提供，判断是否属于"正文枚举"类模式），返回 `rest`。
5. 否则按 `tp` 类型分别用正则剥离对应的标题前缀（`switch`/`case`）：
   - `TITLE_CN_NUM` → 剥离 `^([一二三四五六七八九十百千万]+)[、.．\s]+`
   - `TITLE_CN_PAREN` → 剥离 `^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）]\s*`
   - `TITLE_CHAPTER_ONE`/`TOW`/`THREE`/`FOUR`/`FIVE`（五个章节级别枚举值合并处理）→ 剥离 `^第\s*[一二三四五六七八九十百千万零\d]+\s*(?:章|节|纲|目|条)\s*`
   - `TITLE_NUM_FIVE`/`FOUR`/`THREE`/`TOW`（四个数字级别枚举值合并处理）→ 剥离 `^(\d+(?:\.\d+){1,4})\.?\s*`
   - `TITLE_ROMAN` → 剥离 `^[IVXLCDMivxlcdm]+[.．]\s*`
   - `TITLE_ALPHA` → 剥离 `^[A-Za-z][.．]\s*`
   - `default`（其它未列举的枚举值）→ 返回 `norm` 原样不剥离
   （**注意**：这里的 `MarkdownTitlePattern` 枚举完整取值范围由 Part 5 定义，本文档仅列出这个 switch 用到的分支，需要 Part 5 文档确认枚举值全集，确保没有遗漏未处理的枚举值会走到哪个分支——Java `switch` 表达式若不含 `default` 且未覆盖所有枚举值会编译报错，但此处**含** `default` 分支，故未覆盖到的枚举值会走 `default` 原样返回。）

### 算法：`headingPrefixLineLooksLikeLongClauseForWeakMerge(prevLine, nextLine, effectiveBodyMinLen)`
1. `edge := stripEdgeWhitespace(prevLine)`；若为空，返回 false。
2. `norm := normalizeLine(edge)`；`len := len(norm)`；若 `len < effectiveBodyMinLen`，返回 false。
3. `body := weakMergeBodyAfterStructuralTitlePrefix(norm)`。
4. `clausePunct := countWeakMergeClausePunctInBodyText(body)`。
5. `altMinLen := weakMergeAltMinLenForLongClause(effectiveBodyMinLen)`。
6. 若 `clausePunct <= 0`：
   - 返回 `looksLikeWrappedTitleContinuation(prevLine, nextLine)`（无句读时退回通用"换行续写"判定，不依赖具体词汇表，注释解释了这是为了兼容合同、技术手册等非公文类文本）。
7. `ratio := clausePunct / float64(len)`。
8. 若 `clausePunct >= WEAK_MERGE_TITLE_STUB_BODY_MIN_PUNCT(2)` 且 `ratio >= WEAK_MERGE_TITLE_STUB_BODY_MIN_PUNCT_RATIO(0.04)`，返回 true。
9. 若 `len >= altMinLen` 且 `clausePunct >= 1` 且 `ratio >= WEAK_MERGE_TITLE_STUB_BODY_ALT_MIN_PUNCT_RATIO(0.03)`，返回 true。
10. 若 `clausePunct >= 2`，返回 true。
11. 若 `clausePunct >= 1` 且 `len >= max(WEAK_MERGE_TITLE_STUB_BODY_MIN_LEN+2(20), effectiveBodyMinLen+2)`，返回 true。
12. 否则返回 `len >= effectiveBodyMinLen && clausePunct >= 1 && body 含有字符 《`。

### 算法：`matchFirstPattern` / `stripBodyEnumerationPrefix` / `isBodyEnumerationPattern`（私有转发方法）
均为直接转发调用 `MarkdownTitlePattern` 上的同名静态方法（Part 5），无额外逻辑，Go 实现可省略这层转发，直接调用 Part 5 提供的函数。

`isBodyEnumerationPattern` 判定集合（本文件内**另有一份**局部实现，与转发到 `MarkdownTitlePattern.isBodyEnumerationPattern` 的调用点不同——本类第 671-677 行定义了自己的版本）：
```
pattern in { TITLE_NUM_DUNHAO, TITLE_NUM_DOT, TITLE_NUM_SUFFIX, TITLE_NUM_PAREN, TITLE_CN_PAREN }
```
**移植提醒**：本类文件末尾还定义了 `isFenceLine`/`isHeadingLikeLine`/`isListLikeLine`/`isTableLikeLine`/`isQuoteOrRuleLine` 五个 **public** 方法（第 679-702 行），与文件顶部的 **private** 版本 `isListLikeLine`（第 877 行起，在 `MarkdownBodyMergeStage.java` 中，不是本类！）容易混淆——请务必核对：`MarkdownWeakMergeHeuristics.isListLikeLine`（public，本类）直接是 `LIST_ITEM_LINE.matches(trimmed)`（只用一个正则，不含 `LIST_SCOPE_ITEM_CANDIDATE`），而 `MarkdownBodyMergeStage.isListLikeLine`（private，另一个类）同时检查 `ATTACHMENT_ANCHOR`、`LIST_ITEM_LINE` 和 `LIST_SCOPE_ITEM_CANDIDATE` 三个条件。**两者判断范围不同，绝不能合并成一个函数**，Go 实现必须保留成两个独立的、语义不同的函数（建议明确命名区分，如 `WeakMergeIsListLikeLine` vs `BodyMergeIsListLikeLine`）。

---

## 与 Part 5 的接口

本 Part 依赖 `MarkdownPipelineContext` / `MarkdownLineRange` / `MarkdownLineKind` / `MarkdownTitlePattern` / `MarkdownPipelineLineUtils` / `MarkdownPipelineStage` / `MarkdownHeadingStage` 的以下具体字段与方法，供人工核对两份文档拼接时字段名/方法名/参数类型是否一致：

### MarkdownPipelineContext
| 成员 | 用途 |
|---|---|
| `create(generateToc bool, scannedSource bool) *Context`（静态构造） | 流水线入口创建上下文 |
| `initLinesFromMarkdown(markdown string)` | 按行切分输入并存入 context |
| `inputBlank() bool` | 判断输入是否全空白 |
| `lines() []string` / `setLines([]string)` | 当前行列表读写（各阶段间通过它传递） |
| `scannedSource() bool` | 是否扫描件来源 |
| `pageRemovedOutputAnchors() map[int]struct{}` / `setPageRemovedOutputAnchors(...)` | 页码剔除产生的输出锚点集合 |
| `setChapterTocCatalog(entries)` | 写入目录快照（entries-only 形式） |
| `setNonHeadingScopes([]MarkdownLineRange)` | 非标题作用域（附件清单+列表引导合并结果） |
| `setAttachmentScopesForMerge([]MarkdownLineRange)` / `attachmentScopesForMerge() []MarkdownLineRange` | 附件清单作用域（供 BodyMergeStage 读取） |
| `hierarchyLineIndexes() map[int]struct{}` | 已定稿层级标题行索引集合 |
| `finalDisqualifiedPatternKeys() map[string]struct{}` | 已判定不合格的标题模式 key 集合 |
| `sourceMarkdownHeadingLineIndexes() map[int]struct{}` | 源文档自带 `#` 标题的行索引集合 |
| `resultMarkdown() string` | 取回最终处理结果 |

### MarkdownLineRange
| 字段 | 类型/语义 |
|---|---|
| `startLine` | 起始行索引（含） |
| `endLine` | 结束行索引（不含，左闭右开区间） |
| 构造函数 `MarkdownLineRange(start, end)` | 两参数构造 |

### MarkdownLineKind
枚举值（本 Part 用到）：`BLANK`、`FENCE`、`HEADING`、`LIST_ITEM`、`DATE`、`TABLE`、`QUOTE_OR_RULE`、`PREFORMATTED`、`NATURAL_TEXT`。
方法：`blocksBodyMerge() bool`（需 Part 5 确认具体哪些枚举值返回 true——本 Part 逻辑假定至少 `FENCE`/`TABLE`/`QUOTE_OR_RULE`/`PREFORMATTED` 返回 true，`BLANK`/`HEADING`/`LIST_ITEM`/`DATE`/`NATURAL_TEXT` 的取值需人工核对，因为 `blocksClassifiedBodyMerge` 对 `HEADING`/`LIST_ITEM` 有特例豁免逻辑，说明它们默认也可能返回 true）。

### MarkdownTitlePattern
| 方法 | 用途 |
|---|---|
| `matchFirst(norm string) *MarkdownTitlePattern`（或返回可空枚举值） | 判断字符串是否匹配任一标题模式，返回匹配到的模式 |
| `stripBodyEnumerationPrefix(text string, pattern) string` | 剥离正文枚举前缀 |
| `isBodyEnumerationPattern(pattern) bool` | 判断是否为"正文枚举"类模式（本 Part 与 `MarkdownWeakMergeHeuristics` 内部各有一份局部实现，需核对是否与 Part 5 的定义完全一致） |
| 枚举值 | `TITLE_CN_NUM`、`TITLE_CN_PAREN`、`TITLE_CHAPTER_ONE`/`TOW`/`THREE`/`FOUR`/`FIVE`、`TITLE_NUM_FIVE`/`FOUR`/`THREE`/`TOW`、`TITLE_ROMAN`、`TITLE_ALPHA`、`TITLE_NUM_DUNHAO`、`TITLE_NUM_DOT`、`TITLE_NUM_SUFFIX`、`TITLE_NUM_PAREN`（本 Part 用到的全部取值，Part 5 应确认枚举全集及命名拼写，注意 `TOW` 疑似 `TWO` 的拼写笔误，但需按原始 Java 源码原样保留，不要"修正"） |

### MarkdownPipelineLineUtils
| 成员 | 用途 |
|---|---|
| `stripEdgeWhitespace(s string) string` | 去除首尾空白（含全角空格等） |
| `normalizeLine(s string) string` | 行内容归一化（用于跨函数一致地比较/统计） |
| `endsWithTerminalPunctuation(s string) bool` | 判断是否以句末终止标点结尾 |
| `endsWithAsciiOrFullwidthColon(s string) bool` | 判断是否以 `:`/`：` 结尾 |
| `HEADING_LINE` (正则，含 2 个捕获组：group(1)=`#` 级别标记，group(2)=标题正文) | 标准 ATX 标题行匹配 |
| `PAGE_NUMBER_LINE` (正则) | 页码行识别 |
| `TABLE_SEPARATOR` (正则) | 表格分隔行识别 |
| `HORIZONTAL_RULE` (正则) | 水平分隔线识别 |
| `ATTACHMENT_ANCHOR` (正则) | 附件锚点行识别 |
| `ATTACHMENT_ITEM_LINE` (正则) | 附件清单项行识别 |
| `ATTACHMENT_LIST_MAX_SCOPE_LINES` (int 常量) | 附件清单作用域最大扫描窗口 |
| `LIST_ITEM_LINE` (正则) | 通用列表项行识别（`MarkdownWeakMergeHeuristics.lineStartsWithStructuralHeadingOrListPrefixForWeakMerge` 中带此前缀调用的版本） |
| `LIST_SCOPE_ITEM_CANDIDATE` (正则) | 列表作用域候选项识别 |
| `NUMERIC_DOTTED_OUTLINE_PREFIX` / `NUMERIC_OUTLINE_BOUNDARY` (正则片段) | 供 `MarkdownWeakMergeHeuristics.LIST_ITEM_LINE` 拼装使用 |
| `EMBEDDED_LEVEL2_PREFIX` (正则) | 嵌入式二级编号前缀识别（`MarkdownNoiseCleanupStage.splitEmbeddedLevel2AfterChapter` 用） |
| `NUM_LEVEL2_PREFIX` (正则) | 二级编号前缀全串匹配 |
| `COLON_SPLIT` (正则) | 冒号分割 |
| `loadMaxHeadingLength() int` | 加载可配置的最大标题长度阈值 |

### MarkdownPipelineStage
接口，单方法：`apply(context *MarkdownPipelineContext)`（无返回值，通过修改 context 产生副作用）。本 Part 的 `MarkdownNoiseCleanupStage`、`MarkdownBodyMergeStage` 均实现此接口。

### MarkdownHeadingStage
本 Part 仅调用其 `demoteHashHeadingToBold(raw string) string`（Part 5 提供，兜底把 `#` 标题行降级为加粗文本）。

### 顶层包依赖（既非 Part 5 也非 Part 4，需人工确认归属于哪个 Part 或是否需要新增覆盖范围）
- `ChapterTocLineRemover`：`splitEmbeddedCnSectionHeadings`、`stripLines`、`splitGluedChapterHeadingFromFollowingMarker`、`stripChapterHeadingTrailingNoiseChar`、`normalizeGluedStructuralChapterHeading`、`isChapterPrefixOnlyLine`、`isLikelyChapterTitleNameLine`。
- `ChapterTocCatalog`：`parse(lines)`、`.entries()`、`entriesOnly(entries)`。
- `HeadingSuppressHeuristics`：`shouldSuppressHeadingLine(lines, lineId)`。
- `ListGuideHeuristics`：`detectListGuideScopes`、`detectChapterListGuideScopes`、`detectChapterOutlineScopes`。
- `ChapterReferenceHeuristics`：`isBodyChapterReference(trimmed)`。
- `HeadingPatternQualityHeuristics`：`isLineDisqualifiedForHeadingMerge(line, disqualifiedPatternKeys)`。
- `PdfToMarkdown`：`isStandaloneChineseDateLine(t)`。
- `HeadingLevelPrefixHeuristics`（Part 4）：`classifyPrefixKey(t)`。

这些依赖的具体算法本文档不规范，仅给出精确的方法签名与调用点，供拼接时核对。
