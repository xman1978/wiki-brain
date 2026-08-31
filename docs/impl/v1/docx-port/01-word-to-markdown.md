# DOCX → Markdown 移植规格：`WordToMarkdown.java`

对应 `docs/impl/v1/local-file-convert.md` 第 5 节（DOCX → Markdown）的细化文档，方法与第 6 节 PDF 移植一致：**逻辑到逻辑移植**，不是重新设计。本文档是后续写 Go 代码时的第一手依据，比 `local-file-convert.md` 第 5 节优先。

## 0. 待用户确认的架构分歧（先读这一节）

`local-file-convert.md` 第 5 节当前写的方案是"调用 `ieshan/go-ooxml` 的 `Document.Markdown()` 方法直接产出 Markdown"——把标题识别、表格渲染、后处理全部委托给这个第三方库。

核实 FileView 实际实现（`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/WordToMarkdown.java`，553 行）后发现，FileView **不是**简单调用 Aspose Words 自带的"另存为 Markdown"能力，而是：
1. 自己实现了一套标题识别/层级判定/跨段合并算法（第 2-5 节），复用了 PDF 移植（`pdf-port/04-toplevel-heuristics.md`）里已经文档化的多个顶层启发式类（`HeadingLevelPrefixHeuristics`/`HeadingSequenceConsistencyHeuristics`/`HeadingPatternQualityHeuristics`/`ChapterTocLineRemover`/`ShortPhraseListRunHeuristics`）；
2. 自己实现了表格→GFM pipe table 的转换，含合并单元格展开（第 6 节）；
3. `WordToMarkdown.convert()` 产出的**仍不是最终输出**——`ConvertWorker.java:434` 显示，Word 转换结果之后还要再走一遍与 PDF **共用同一套** `MarkdownPostProcessorPipeline`（即 `docs/impl/v1/pdf-port/05-mpp-heading-stack.md`/`06-mpp-merge-cleanup.md` 描述的 `MarkdownHeadingStage`/`MarkdownBodyMergeStage`/`MarkdownWeakMergeHeuristics`/噪声清理阶段），才是 FileView 实际吐给下游的内容。

这意味着"调用 go-ooxml 的 `Markdown()` 方法"与 FileView 的实际转换保真度差距，比 PDF 一开始设想的"docmill 默认 pipeline"与 FileView 的差距还要大——PDF 至少还用 docmill 做纯粹的元素解析、组装层完全自己写；而 go-ooxml 的 `Markdown()` 是它自己一套独立的标题/表格判定逻辑，与 FileView 的判据无任何对应关系。

本文档按**完整移植** `WordToMarkdown.java` 的方法撰写（第 1-8 节），并给出这一移植方案下的 Go 包结构建议（第 9 节）。**是否要从"调用 go-ooxml 内置转换"改为"完整移植 WordToMarkdown.java + 复用 PDF 已移植的 mpp 管线"，需要用户在开工前确认**——如果确认按此路线走，`local-file-convert.md` 第 5 节需要同步改写（参照第 6 节 PDF 部分的写法）；如果用户仍然只想要 go-ooxml 内置转换的 MVP 方案，本文档第 1-8 节可作为后续升级到完整移植时的参考保留，但不纳入当前实现范围。

## 1. 总体流程：`WordToMarkdown.convert(inputPath, markdownPath)`

```text
1. 用 LoadOptions 打开文档，默认编辑语言设为 zh-CN（LCID 2052）。
2. doc.updateListLabels()：刷新 Word 自动编号列表的 label 缓存（编号可能因内容变化而未及时刷新）。
3. doc.updateTableLayout()：刷新表格布局（合并单元格等信息在某些文档里需要显式触发才准确）。
4. doc.getRevisions().acceptAll()：接受全部修订（跟踪变更），产出最终定稿文本，不保留批注式的增删痕迹。
5. 移除文档中所有 Field（doc.getRange().getFields()，逐个 field.remove()）：域代码（如目录域、页码域）本身不是正文内容。
6. blocks = collectBodyBlocks(doc)（第 2 节）。
7. markdown = cleanOutput(renderBodyBlocks(blocks))（第 5、8 节）。
8. 写入 markdownPath（UTF-8）。
```

**Go 移植对应关系**：步骤 1-5 是 Aspose Words 特有的文档预处理，第 10 节逐项给出 go-ooxml 的对应能力核实清单；步骤 6-8 是纯算法逻辑，与底层库无关，可以照搬。

## 2. Body Block 收集：`collectBodyBlocks(doc)`

遍历文档所有子节点（`doc.getChildNodes(0, true)`，含表格内部节点，深度优先展开），按节点类型分派：

```text
对每个 node：
  若 node 是 TABLE：
    先 flush 当前 pendingHeading（若有）
    追加一个 BodyBlock{ line: convertTableToMarkdown(node) + "\n", heading: false }
  若 node 是 PARAGRAPH 且不在表格单元格内（!paragraph.isInCell()）：
    text = paragraph.getText().trim()
    若 text 为空，或等于「目录」，或 paragraph.isEndOfHeaderFooter()：
      flush pendingHeading；跳过该段落（不产出任何 block）
    否则若 isWordHeadingFragment(paragraph, text)（第 3 节）：
      headingText = headingTextWithListLabel(paragraph, text)
      level = resolveWordHeadingLevel(paragraph, headingText)
      若 pendingHeading 为空：pendingHeading = {headingText, level}
      否则若 shouldMergeWordHeadingFragments(pendingHeading.text, pendingHeading.level, headingText, level)（第 4 节）：
        合并：pendingHeading.text = joinHeadingFragments(pendingHeading.text, headingText)
              pendingHeading.level = min(pendingHeading.level, level)
      否则：flush 当前 pendingHeading，另起一个新的 pendingHeading = {headingText, level}
      跳过该段落的其余处理（不产出正文 block）
    否则（普通正文段落）：
      flush pendingHeading
      listLabel = paragraph.isListItem() && listLabel!=null ? listLabel.getLabelString() : null
      对 expandSoftBreakPlainLines(listLabel, text)（第 6 节）返回的每一行，各追加一个 BodyBlock{ line, heading:false }
遍历结束后，flush 最后一个 pendingHeading（若有）
```

`BodyBlock` 是 `(line string, heading bool, headingLevel int)` 的记录类型，Go 移植可用同名字段的 struct。

`flushPendingHeadingBlock`：若 `pending` 为 `nil` 或其 `text` 为空白，返回 `nil`（不产出 block）；否则追加 `BodyBlock{ text.trim(), heading:true, level }`，返回 `nil`（清空 pending）。

**「目录」跳过是硬编码的精确字符串匹配**，不是正则/包含匹配——只跳过整段文本恰好等于「目录」两个字的段落（Word 里常见的目录小节标题独立成段的情况），不影响正文中出现"目录"二字的其他句子。

## 3. 标题片段判定：`isWordHeadingFragment` / `isWordHeadingCandidate`

```text
isWordHeadingFragment(paragraph, text):
  若 paragraph 为空或 text 为空白，返回 false
  styleLevel = resolveWordHeadingStyleLevel(paragraph)          // 第 3.1 节
  centered = paragraph.alignment == CENTER
  boldAndLarge = isBoldAndLarge(paragraph, 14.0)                // 段内任一 run 同时 bold 且字号 >= 14
  返回 isWordHeadingCandidate(text, paragraph.isListItem(), styleLevel, centered, boldAndLarge)

isWordHeadingCandidate(text, listItem, styleLevel, centered, boldAndLarge):
  若 text 为空白，返回 false
  若 text 长度 > 64（Go 按 rune 计长度，理由见 pdf-port 第 6.2 节「(6)」UTF-16 vs rune 说明），返回 false
  若 text 以终止标点结尾（endsWithTerminalPunctuation，见下），返回 false
  若 styleLevel > 0（有 Word 内置标题样式），返回 true          // 样式优先，跳过后续启发式
  若 listItem 为真，返回 false                                  // 关键：list item 不允许被启发式（居中/加粗）判定为标题
  返回 centered || boldAndLarge
```

`endsWithTerminalPunctuation(text)`：末字符是否属于 `。！？；：，、.,!?;:` 这个字符集合（中英文全套句读标点，不止句号问号——逗号顿号也算，因为一段以逗号结尾大概率是长句被截断，不是标题）。

`isBoldAndLarge`：遍历段落所有 `Run`，只要有一个 run 的字体同时 `bold=true` 且 `size>=14.0`（pt），即返回真；空 run / 无字体信息的 run 跳过不计。

### 3.1 标题层级解析：`resolveWordHeadingLevel` / `resolveWordHeadingStyleLevel(FromName)`

```text
resolveWordHeadingLevel(paragraph, headingText):
  fromStyle = resolveWordHeadingStyleLevel(paragraph)
  若 fromStyle > 0，返回 fromStyle
  fromPrefix = HeadingLevelPrefixHeuristics.naturalLevelForTitle(headingText)   // 见 pdf-port/04
  若 fromPrefix > 0，返回 fromPrefix
  返回 HEURISTIC_HEADING_LEVEL（常量 = 2）

resolveWordHeadingStyleLevel(paragraph):
  若 paragraph / paragraphFormat / style 任一为空，返回 0
  返回 resolveWordHeadingStyleLevelFromName(style.getName())

resolveWordHeadingStyleLevelFromName(styleName):
  若 styleName 为空白，返回 0
  lower = styleName 转小写并 strip
  用正则 (?:heading|标题|标题样式)\s*(\d) 匹配（find，不要求整串匹配）
    命中：level = 捕获的数字，clamp 到 [1,6]，返回
  否则若 lower 包含 "heading" 或 "标题"（子串匹配，不含数字后缀，如样式名就叫「标题」或「Heading」不带编号）：
    返回 HEURISTIC_HEADING_LEVEL（2）
  否则返回 0
```

**优先级链**：Word 内置样式名（`Heading N`/`标题 N`/`标题样式 N`，大小写不敏感，取数字定层级）> 无编号的"标题"类样式名（固定按 2 级）> `HeadingLevelPrefixHeuristics.naturalLevelForTitle`（文本前缀模式，如「第 X 条」→2、「（一）」→3、「1.」→4，规则详见 `pdf-port/04-toplevel-heuristics.md` 的 `HeadingLevelPrefixHeuristics` 一节）> 兜底 2 级（仅当前三者都判不出层级、但已经因为居中/加粗被判定为标题候选时才会走到这一步）。

## 4. 连续标题片段合并：`shouldMergeWordHeadingFragments`

Word 里同一个视觉标题常被人工换行拆成多个段落（尤其封面大标题、多行章节名）。`collectBodyBlocks` 在遇到连续的标题候选段落时，用这个函数判断是否应该合并成一个 `BodyBlock`：

```text
shouldMergeWordHeadingFragments(left, leftLevel, right, rightLevel):
  若 right 为空白，返回 false
  若 left 为空白，返回 true                                     // 左侧还没内容，直接吸收
  若 isWordHeadingTitleContinuationPair(left, right)，返回 true   // 「第一章」+「总则」这类前缀行+题名续行，见下
  若 leftLevel != rightLevel，返回 false                         // 不同层级（如 H1 后紧跟 H2）不合并
  若 right 有可识别的编号前缀模式（classifyPrefixKey != null），返回 false  // 右侧自带独立编号，是新标题
  若 left 有可识别的编号前缀模式，返回 true                       // 左侧已经是"第 X 条"类编号行，右侧续行属于同一条
  返回 leftLevel == HEURISTIC_HEADING_LEVEL（2）                  // 两侧都是纯启发式（居中/加粗、无编号前缀）判定的片段，按经验合并
```

`isWordHeadingTitleContinuationPair(left, right)`（私有）：判断「章节前缀行 + 独立成段的题名」这种典型模式（如 `第一章` 单独一段，下一段是 `总则`）：

```text
若 left 或 right 为空白，返回 false
若 left 或 right 本身是目录行（ChapterTocLineRemover.isChapterTocLine），返回 false     // 目录条目不参与合并
若 right 不像"章节题名行"（!ChapterTocLineRemover.isLikelyChapterTitleNameLine(right)），返回 false
若 left 是"纯章节前缀"（ChapterTocLineRemover.isChapterPrefixOnlyLine(left)，如单独一行「第一章」），返回 true
若 left 是"结构化章节标题"（ChapterTocLineRemover.isStructuralChapterHeading(left)）：
  afterChapter = left 去掉开头的 "第 X 章" 前缀（正则 ^第\s*[一二三四五六七八九十百千万零\d]+\s*章\s* 替换为空）后 trim
  若 afterChapter 为空（即 left 整行只是"第 X 章"，没有紧跟的章节名），返回 true
t = left.trim()
返回 t 匹配 ^[一二三四五六七八九十百千万]+[、．.]\s*$（纯中文数字编号行，如「一、」）
   或 t 匹配 ^[（(][一二三四五六七八九十百千万]+[)）]\s*$（纯括号中文数字编号行，如「（一）」）
```

`ChapterTocLineRemover` 的这几个方法（`isChapterTocLine`/`isLikelyChapterTitleNameLine`/`isChapterPrefixOnlyLine`/`isStructuralChapterHeading`）已在 `pdf-port/04-toplevel-heuristics.md` 的 `ChapterTocLineRemover` 一节逐个给出算法，Word 移植直接复用同一份 Go 实现，不要重新写一遍。

`joinHeadingFragments(a, b)`：`left`/`right` 分别 trim；任一为空直接返回另一个；否则看 `left` 末字符与 `right` 首字符是否都是字母或数字（`Character.isLetterOrDigit`，Go 用 `unicode.IsLetter(r) || unicode.IsDigit(r)`）——都是则用空格连接（避免英文/数字粘连成一个词），否则直接拼接（中文之间不加空格）。

## 5. 标题降级复核：`renderBodyBlocks`

`collectBodyBlocks` 阶段的标题判定是**逐段局部判断**，没有看全文一致性。渲染前需要一次全文复核，把局部判断出的"标题"中不符合全文模式一致性的降级为正文：

```text
renderBodyBlocks(blocks):
  若 blocks 为空，返回 ""
  lines = blocks 每个 block 的 line 字段组成的列表
  demoteIndexes = HeadingSequenceConsistencyHeuristics.detectMarkdownLinesToDemote(
                      lines, i -> blocks[i].heading)
  demoteIndexes ∪= HeadingPatternQualityHeuristics.detectLineIndexesToDemoteAsNonHeading(
                      lines, i -> blocks[i].heading)
  对每个 block（按索引 i）：
    若 block.heading 为真 且 i 不在 demoteIndexes 中：
      输出 "#"*level + " " + line.trim() + "\n\n"     // toMarkdownHeadingLine，level clamp 到 [1,6]
    否则：
      输出 line + "\n"
```

这两个复核函数都已在 `pdf-port/04-toplevel-heuristics.md` 详细文档化（`HeadingSequenceConsistencyHeuristics` 一节、`HeadingPatternQualityHeuristics` 一节），**调用签名与 Word 这里完全一致**（`detectMarkdownLinesToDemote(lines, isRecognizedAsHeading)` 就是文本/Word/WPS/HTML 后处理共用入口；`detectLineIndexesToDemoteAsNonHeading(lines, isCurrentlyHeading)` 见该文档 623 行）——Go 移植不需要为 Word 场景另写一套，直接调用 PDF 移植已经实现的同一份函数。

## 6. 软换行展开：`expandSoftBreakPlainLines`

Word 段落内部的软换行（Shift+Enter，字符 ``，即 vertical tab）不是新段落，但如果整段是"标题行 + 若干子项"的结构（如某个编号标题下用软换行罗列几条短语），需要拆行才能被后续的 MPP 管线正确识别为独立的行结构：

```text
expandSoftBreakPlainLines(listLabel, text):
  若 text 为空或全空白，返回 [text.trim() 或 ""]（单元素列表）
  若 text 不含 ，返回 [renderPlainLineText(listLabel, text)]（单元素列表，直接走 listLabel 拼接）

  按  切分（保留空 trailing 段，Java split(..., -1)），逐段 trim 后过滤掉空段 → segments
  若 segments 为空（全部是空白段），返回 [renderPlainLineText(listLabel, text)]

  firstLine = renderPlainLineText(listLabel, segments[0])
  prefixKey = HeadingLevelPrefixHeuristics.classifyPrefixKey(firstLine)
  norm = MarkdownPipelineLineUtils.normalizeLine(firstLine)

  若 prefixKey 为空 或 !ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(prefixKey, norm, nil)：
    // 首行不像"编号节标题行"——退化处理
    若 MarkdownLineClassifier.looksLikePreformattedBlock(segments)：
      返回 [firstLine] ++ segments[1:]（按原分段保留，不再走 listLabel 拼接、不合并空格）
    否则返回 [renderPlainLineText(listLabel, text)]（放弃拆分，整段按原始 text 走一次 listLabel 拼接、 保留在文本里由后续管线处理——注意这一分支返回的是用**原始未拆分的 text**渲染，不是用 segments 拼接）

  否则（首行像编号节标题行）：
    返回 [firstLine] ++ segments[1:]（拆行输出，首行走 listLabel 拼接，其余段原样各自一行）
```

`renderPlainLineText(listLabel, text)`：`trimmed = text.trim()`；`listLabel` 为空则直接返回 `trimmed`；否则返回 `composeHeadingTextWithListLabel(listLabel, trimmed)`（第 7 节，与标题的 list label 拼接逻辑复用同一个函数——普通正文段落若是自动编号列表项，也会把编号前缀拼进这一行文本）。

`MarkdownPipelineLineUtils.normalizeLine`、`MarkdownLineClassifier.looksLikePreformattedBlock`、`ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine` 均已在 `pdf-port/05-mpp-heading-stack.md`（`MarkdownPipelineLineUtils` 一节）、`pdf-port/06-mpp-merge-cleanup.md`（`MarkdownLineClassifier` 一节）、`pdf-port/04-toplevel-heuristics.md`（`ShortPhraseListRunHeuristics` 一节）中文档化，直接复用。

## 7. 自动编号列表 label 拼接

```text
headingTextWithListLabel(paragraph, text):
  若 text 为空，返回 ""
  trimmed = text.trim()
  若 paragraph 为空 或 !paragraph.isListItem()，返回 trimmed
  label = paragraph.listLabel == null ? "" : paragraph.listLabel.getLabelString()
  返回 composeHeadingTextWithListLabel(label, trimmed)

composeHeadingTextWithListLabel(listLabel, text):
  若 text 为空白，返回 text.trim() 或 ""
  trimmed = text.trim()
  若 listLabel 为空白，返回 trimmed
  label = listLabel.strip()
  若 startsWithListLabel(trimmed, label)，返回 trimmed              // 正文已经手打了同样的编号前缀，不重复拼接
  返回 label + " " + trimmed

startsWithListLabel(text, label):
  text/label 任一为 nil，返回 false
  t = text.strip()；l = label.strip()
  l 为空 或 t 长度 < l 长度，返回 false
  t 的前 len(l) 个字符（区分大小写）与 l 不相等，返回 false
  若 t 长度 == l 长度，返回 true
  next = t 第 len(l) 个字符（紧跟在 label 之后的字符）
  若 next 是空白字符，返回 true
  返回 l 以 "." 结尾 且 next 不是数字        // 「1.总则」：label 本身是"1."，正文紧跟"总则"没有空格，仍算已包含
```

**Word 自动编号（`ListLabel`）与手打编号是两回事**：Word 文档里常见"用户在正文里手打了『1.』然后又设置了自动编号列表样式"的重复编号情况，`startsWithListLabel` 就是为了去重这种情况——只有当正文文本还**没有**手打同样前缀时，才把 `ListLabel` 拼接上去。

## 8. 表格转换

### 8.1 单格表格特判：`convertSingleCellTableToText`

`table.rows.count == 1 && table.rows[0].cells.count == 1` 时不生成 pipe table：

```text
逐段落取文本（\r/\n 替换为空格、\f 删除、trim），过滤空段落 → paragraphLines
若 paragraphLines 为空，返回 ""
若 MarkdownLineClassifier.looksLikePreformattedBlock(paragraphLines)（代码/SQL/命令行/日志/配置块特征）：
  返回 CodeFenceWriter.wrap(paragraphLines)     // ``` 围栏包裹，按原分段保留换行，见 pdf-port/04
否则：
  返回 paragraphLines 用单空格拼接成一行 + "\n"
```

这类单格表格常见于 Word 里"用一个只有一格的表格框住一段代码/公式/独立说明"的排版习惯——装饰性质，不是真正的表格语义，与 PDF 移植第 1 节（`pdf-port/01-extraction-geometry.md`）"装饰性单格表还原"处理的是同一类问题，但触发条件和处理方式是 Word 场景独有的（按段落而非几何位置判断），不能直接复用 PDF 那部分代码。

### 8.2 多格表格转 GFM pipe table：`convertTableToMarkdown`

```text
（先排除 8.1 的单格特例）
table.convertToHorizontallyMergedCells()：
  // docx 原始存储里，横向合并格常存为「一个 Cell 带 gridSpan 属性，覆盖多列」，Row.getCells() 里不会出现
  // 覆盖到的那些"虚拟延续列"，逐行取 cells 会导致各行列数不一致、列与列错位。
  // 调用这个 API 后，横向合并会转换为「每一列都有一个实际 Cell，覆盖列标 HorizontalMerge=FIRST，
  // 其余覆盖列标 HorizontalMerge=PREVIOUS」的统一表示，之后按列索引对齐才是可靠的。

prevRow = nil
对每一行 row：
  cols = []
  对每个 cell（此时每行的 cell 数已因上面的转换而与实际列数一致）：
    cellText = cell 内所有段落文本，\r/\n 替换空格、\f 删除、trim 后用空格拼接（段落间只有非空才插入分隔空格）
    text = cellText
    colIdx = cols 当前长度（即将写入的列号）
    若 text 为空 且 cell.verticalMerge == PREVIOUS（纵向合并延续格）：
      若 prevRow 非空 且 colIdx < prevRow 长度：text = prevRow[colIdx]      // 取上一行同列内容
    否则若 text 为空 且 cell.horizontalMerge == PREVIOUS（横向合并延续格）：
      若 cols 非空：text = cols 最后一个元素                                // 取本行左侧相邻列内容
    cols.append(text)
  rows.append(cols)
  prevRow = cols

若 rows 为空，返回 ""
rows = mergeDuplicateHeaderRows(rows)                    // 见 8.3
colCount = rows 中各行长度的最大值
对每行不足 colCount 列的，右侧补空字符串补齐

渲染（**2026-08-31 改判，取代下面被划掉的"按列宽对齐补空格"口径**）：
  ~~colWidths[i] = 各行第 i 列文本长度的最大值（用于对齐补空格，纯视觉美化，非语义必需）~~
  ~~第一行作为表头行：| 各列 pad 到 colWidths[i] |~~
  ~~分隔行：| 各列 colWidths[i] 个 "-" |~~
  ~~其余行依次渲染，同样按 colWidths 补空格对齐~~
  改为极简 GFM 格式，不做列宽对齐：表格块前先输出一个空行；第一行表头 `| 单元格 | 单元格 | ...`（每个单元格左右各一个空格，不 pad）；分隔行固定 `| --- | --- | ... |`（每列都是三个 `-`，与列宽无关）；其余行依次渲染，格式同表头行。
  改判原因：与实际测试样本（`data/sources/markdown/` 下的参考 Markdown）逐字节比对后确认，参考输出统一是不做列宽对齐的极简格式（且表格前留一个空行），按列宽对齐补空格这个"纯视觉美化"选择在这批参考样本里并不成立；对应 Go 实现见 `docx_table.go` `convertTableToMarkdown`。
```

**合并单元格拆分为独立单元格填充内容，而不是留空**——理由是 GFM 表格语法不支持 rowspan/colspan，若延续格留空，渲染出来会显得内容缺失；这个选择直接影响下游 Unit 抽取阶段读到的表格内容是否完整，**不要**在 Go 移植时为了"更简洁"改成留空。

### 8.3 表头去重：`mergeDuplicateHeaderRows` / `normalizeRow`

```text
mergeDuplicateHeaderRows(rows):
  若 rows 少于 2 行，原样返回
  first = normalizeRow(rows[0])
  removeCount = 0
  从第 2 行开始，只要 normalizeRow(rows[i]) 与 first 相等就 removeCount++，一旦不相等立即停止（不是全表扫描去重，只去**连续从第 2 行开始、与表头完全相同**的那一段）
  若 removeCount > 0，返回去掉这 removeCount 行之后的 rows；否则原样返回

normalizeRow(row):
  每列：trim，全角空格「　」替换为半角空格，连续空白折叠为单个空格
```

这处理的是 Word 里"跨页表格每页顶部自动重复表头行"这种排版产物——转成 Markdown 后同一份数据里会连续出现好几行内容完全相同的"表头"，只保留第一行。**只去连续重复段，不做全表任意位置的重复检测**，避免误删表格正文里恰好连续两行数据相同的合法情况。

## 9. 输出清理：`cleanOutput`

```text
cleanOutput(text):
  若 text 为空白，返回 ""
  否则依次：
    \r → \n
    删除 \f（换页符）
    删除 （软换行，展开阶段已处理过的残留）
    连续 （响铃符，Word 有时用作特殊分隔）替换为单个 \n
    删除   （其他控制字符）
    去掉开头的连续 \n
    整体 trim
```

## 10. go-ooxml API 能力核实清单（开工前必须逐项验证，不能假设成立）

`ieshan/go-ooxml`（`github.com/ieshan/go-ooxml`）README 自述支持段落样式（`SetStyle`）、run 级粗体（`SetBold`）、表格、Markdown 双向转换等**写入/构造**能力，但本移植需要的是**读取**既有 `.docx` 时能拿到多细的底层信息——这部分 README 未明确说明，GitHub star 数为 0，活跃度极低，**接入前必须用真实代码验证以下每一项，不能照抄本表就当作已确认可行**：

| 需要读到的信息 | Aspose Words API | go-ooxml 是否有等价读取能力 |
| --- | --- | --- |
| 段落样式名（判断 `Heading N`/`标题 N`） | `paragraph.getParagraphFormat().getStyle().getName()` | **待核实** |
| 段落对齐方式（居中判断） | `paragraph.getParagraphFormat().getAlignment() == CENTER` | **待核实** |
| Run 级粗体 + 字号 | `run.getFont().getBold()` / `run.getFont().getSize()` | **待核实** |
| 段落是否列表项 + 自动编号 label 文本 | `paragraph.isListItem()` / `paragraph.getListLabel().getLabelString()` | **待核实（高风险项——自动编号 label 的渲染值通常需要文档引擎自己维护编号状态机，不是简单读 XML 属性就能拿到，是否需要自己实现一套 numbering.xml 解析+编号状态追踪，待确认 go-ooxml 是否已内置）** |
| 段落是否在表格单元格内 | `paragraph.isInCell()` | **待核实** |
| 段落是否页眉页脚结尾标记 | `paragraph.isEndOfHeaderFooter()` | **待核实（若无等价能力，可能需要改为按 section 的 header/footer part 单独排除，而非逐段判断）** |
| 表格横向合并展开 | `table.convertToHorizontallyMergedCells()` | **待核实（关键 API，直接决定 8.2 节算法能否照搬；若无等价能力需要自己解析 `w:gridSpan`）** |
| 单元格纵向/横向合并延续标记 | `cell.getCellFormat().getVerticalMerge()/getHorizontalMerge() == PREVIOUS` | **待核实（对应 OOXML `w:vMerge`/`w:hMerge` 属性，需确认库是否暴露）** |
| 修订（track changes）批量接受 | `doc.getRevisions().acceptAll()` | **待核实（若无等价能力，需要判断是否要求用户手动"接受修订"后再上传，或自己解析 `w:ins`/`w:del`）** |
| 域代码批量移除 | `field.remove()` | **待核实（对应正文中 `w:fldSimple`/`w:fldChar` 结构，需确认是否已被库当作纯文本节点处理、还是需要显式跳过）** |
| 列表标签刷新 | `doc.updateListLabels()` | **待核实，含义同"自动编号 label"一项** |

**若上表任一"高风险项"（尤其自动编号 label、合并单元格标记）在 go-ooxml 中确认不可行**，需要回到用户面前重新确认技术路线（换库、或在 go-ooxml 之上自己解析 `numbering.xml`/`document.xml` 的相关 OOXML 结构），不要在文档/代码里静默降级（例如"自动编号识别不到就干脆不拼 label"）——那样会让第 7 节描述的行为在 Go 版本里悄悄失效而没有任何记录。

## 11. Go 包结构建议

延续 `local-file-convert.md` 第 2 节的包结构，`docx.go` 拆分为：

```text
internal/source/localconvert/
    docx.go              // 入口：打开文档、预处理（对应第 1 节步骤 1-5）、调用下面的函数、写出结果
    docx_blocks.go        // collectBodyBlocks / BodyBlock（第 2 节）
    docx_heading.go        // 第 3、4、7 节：标题判定、层级解析、片段合并、list label 拼接
    docx_softbreak.go      // expandSoftBreakPlainLines（第 6 节）
    docx_table.go           // convertTableToMarkdown / convertSingleCellTableToText / mergeDuplicateHeaderRows（第 8 节）
```

`docx_heading.go`/`docx_softbreak.go` 会导入 `pdfconv` 包（`internal/source/localconvert/pdfconv/`，见 `pdf-port/` 系列文档第 6.1 节的包结构）里的 `HeadingLevelPrefixHeuristics`/`HeadingSequenceConsistencyHeuristics`/`HeadingPatternQualityHeuristics`/`ChapterTocLineRemover`/`ShortPhraseListRunHeuristics`/`MarkdownLineClassifier`/`MarkdownPipelineLineUtils`/`CodeFenceWriter` 等函数——这些函数在 Java 里也是 PDF 与 Word 两条转换路径共用的同一份实现，Go 移植同理不应该为 Word 复制一份。若这些函数在 `pdfconv` 包内是未导出（小写开头）的，需要在实现 PDF 移植时就把它们设计成包内可跨文件调用、且允许 `docx*.go` 所在的包导入——**具体是把 `docx*.go` 也放进 `pdfconv` 包内，还是把这些共用函数提到更上一层的公共包，是需要在实现阶段做的包结构决策，本文档不预先下结论**（比 PDF 移植的"单一 Go 包不做严格分层"决策晚一步，因为这里涉及跨"格式"而不是跨"Part"的共用）。

## 12. 架构决策：Word 输出是否复用 PDF 的 MPP 后处理管线

对应第 0 节提出的分歧。若确认走完整移植路线，`docx.go` 的入口函数产出的 Markdown（第 1-9 节的结果）**不是最终输出**，还需要送入与 PDF 共用的后处理管线（对应 `pdf-port/05-mpp-heading-stack.md` 的 `MarkdownHeadingStage`、`pdf-port/06-mpp-merge-cleanup.md` 的 `MarkdownBodyMergeStage`/`MarkdownWeakMergeHeuristics`/噪声清理阶段），如 FileView `ConvertWorker.java:434` 所示：

```text
WordToMarkdown.convert(...)  →  原始 Markdown（本文档第 1-9 节的范围）
                              →  MarkdownPostProcessorPipeline.processFile(...)  →  最终产出
```

这意味着 `client.go`（第 2 节包结构里的入口分发文件）调用完 `docx.go` 的转换函数后，还需要再调用一次 `pdfconv` 包里实现的 MPP 管线入口（该入口函数在 `pdf-port/` 系列文档里的确切名字，以实现 PDF 移植时的实际代码为准）——**不要**把 MPP 管线设计成只服务 PDF 输入的内部细节、对 Word 路径不可见。

**2026-08-31 改判，取代上一段"完整复用"口径中的第 3 步（`mergeWrappedBodyLines`/`MarkdownBodyMergeStage`）**：MPP 管线其余三步（TOC 行剔除、`ApplyHeadingStage` 标题识别、控制字符清理）继续对 Word 路径完整生效；唯一例外是 `mergeWrappedBodyLines`——这一步在 PDF 侧存在的前提是"页宽度换行导致一句话被拆成多个物理行，需要靠启发式把它们粘回同一段"，而 Word 路径的每一"行"直接来自 OOXML 树里真实的 `<w:p>` 段落边界（`docx_blocks.go` 逐段读取），根本不存在"换行artifact"这个问题。用同一套按内容形状判断"该不该粘"的启发式（`shouldBlockBodyMergePair` 等）套用到 Word 段落上，实测会不一致：两份实际测试样本里出现过 XML 结构完全相同（都是多个独立 `<w:p>`，含一个空段落分隔）但预期输出一个要合并、一个不合并的情况，说明"内容读起来像不像标题/像不像话说完了"这类启发式本身不足以复现两边都满意的结果。改判结论：**Word 路径的段落边界（`<w:p>`）一律不合并，无条件保留原始段落切分**，不再尝试用内容启发式判断是否该跨 `<w:p>` 粘接。`docx.go` 因此改为调用 `pdfconv.RunMarkdownPipelineNoBodyMerge`（管线其余步骤不变），PDF 路径继续调用完整的 `pdfconv.RunMarkdownPipeline`（含 `mergeWrappedBodyLines`），互不影响。

## 13. 已知限制（与 `local-file-convert.md` 第 3 节一致，不重复展开）

- 仅支持 `.docx`（OOXML）；`.doc`（旧二进制格式）go-ooxml 无法解析，直接返回明确错误。
- 批注（comments）不进入正文——本移植沿用 FileView 的做法，`WordToMarkdown.java` 本身就没有读取批注内容拼进 Markdown 的逻辑（批注处理是 `go-ooxml` 层面 `IncludeComments` 选项，但既然改为完整移植 FileView 算法，批注问题不复存在：FileView 的路径压根不涉及批注读取）。
- `.rtf`/`.wps` 虽然在 FileView 里也走 `WordToMarkdown`（同用 Aspose Words 打开），但 go-ooxml 是否支持这两种格式的读取需另行核实——`fileViewWhitelist` 覆盖 `.wps`/`.rtf`，若 go-ooxml 打不开这两种格式，`localconvert` 的支持范围仍只能是 `.docx`，与 `local-file-convert.md` 第 3 节表格保持一致，不额外承诺。

## 14. 测试与验证策略

参照 `local-file-convert.md` 第 4.4 节（Excel 测试策略）与第 6.4 节（PDF 借用 FileView 测试资源）的做法：

1. 先确认 `/Users/jxu/Code/fileview` 是否有可复用的 Word 测试资源（回归测试目录、`test/` 下是否有 `.docx` 样例 + 对应参考 `.md` 输出——`DocumentToMarkdownConverter.java` 所在的 `regression` 测试包指向的测试数据目录需要单独核实，本文档未确认其存在）；若有则复用同一套"自动化 diff 参考文件"策略（第 6.4 节的思路：允许断行边界级差异，不允许标题层级/表格结构/正文内容差异）。
2. 若无现成 Word 回归测试资源，至少准备：含 Word 内置标题样式的文档、纯启发式（居中+加粗、无样式）标题的文档、自动编号列表（含标题和正文两种场景）、合并单元格表格（横向/纵向/两者都有）、单格装饰表格、跨页重复表头的长表格、含修订痕迹的文档、含批注的文档、含域代码（目录域等）的文档，逐份人工核对输出。
3. 第 10 节"待核实"清单中的每一项，都需要在写测试前先用一份最小样例文档验证 go-ooxml 的实际读取行为，作为决定"照搬移植"还是"需要绕过实现"的依据。

## 15. 完成标准

```text
配置切换（回归项，与 local-file-convert.md 第 8 节一致）：
  fileview.mode 缺省/为空/为 "remote" -> 行为与改动前完全一致；
  fileview.mode: "local" -> Service 使用 LocalConvertClient，DOCX 走本文档描述的转换路径。

标题识别：
  Word 内置 Heading/标题 N 样式 -> 层级与样式编号一致；
  无样式、纯居中或加粗+大字号 -> 识别为 2 级标题，除非命中前缀模式给出更细层级；
  自动编号列表项（isListItem=true）-> 不会被居中/加粗启发式误判为标题（除非本身也有 Word 标题样式）；
  跨段人工换行拆分的同一标题（如封面大标题）-> 合并为单个标题块；
  全文标题模式不一致（部分识别部分未识别、编号序列断裂）-> 按 HeadingSequenceConsistencyHeuristics /
    HeadingPatternQualityHeuristics 的判据整体降级为正文，不出现部分保留部分降级。

表格：
  含横向合并单元格 -> 展开后各列内容正确对齐，不出现列错位；
  含纵向合并单元格 -> 延续格填充上一行同列内容，不留空；
  跨页重复表头 -> 只保留第一份表头，不出现连续多行相同表头；
  单格装饰性表格 -> 按段落文本或代码围栏处理，不产出只有一格的 pipe table。

自动编号 list label：
  标题/正文中的自动编号列表项 -> label 正确拼接到文本前，且不与正文已有的手打编号重复。

后处理管线（若采纳第 12 节的架构决策）：
  Word 转换的原始 Markdown 送入与 PDF 共用的 MPP 管线后，标题精修/正文弱合并/噪声清理与 PDF 路径行为一致
    （复用同一份 Go 实现，不是另外写一套）。

已知限制（回归项）：
  .doc（旧二进制格式）-> 返回明确 unsupported 错误；
  批注 -> 不出现在正文 Markdown 中；
  域代码（如目录域）-> 被移除，不以域代码原始形式出现在输出里；
  修订痕迹 -> 视为已接受，不保留增删标记。
```
