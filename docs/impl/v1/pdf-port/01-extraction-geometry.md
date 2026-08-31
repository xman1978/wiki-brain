# PDF 移植 Part 1：元素提取与几何计算

来源：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/PdfToMarkdown.java`（5215 行，已通读全文）。

本文档只覆盖「元素提取与几何」这一半职责：把 Aspose（Go 端为 docmill）暴露的定位文字/表格几何数据，
组装成排好序的 `TextBlock`/`TableBlock` 列表。标题识别、样式聚类分级、短语式列表识别、正文渲染、目录清洗、
收尾清理等属于 Part 2（及后续 Part）范围，本文档中提到这些函数时只给出调用点和用途，不展开算法。

## 覆盖范围

顶层编排：
- `convertDocument`（只描述到「排序好的元素列表」为止的前半段管线）

页眉/页脚：
- `detectHeaderFooterFilter`、`buildHeaderFooterLineCandidate`、`headerFooterSignature`
- `isHeaderOrFooter`、`isInHeaderFooterBand`（两个重载）、`shouldDropAsHeaderFooterLine`、`unionFragmentBBox`
- `isPageNumberBlock`
- 数据类：`HeaderFooterFilter`、`HeaderFooterLine`

表格提取：
- `extractTableBlocks`、`clusterAbsorbedTables`、`dropContainerDuplicates`、`isSingleCellTable`、
  `unionRectangleExcluding`、`rectangleContains`、`unionRect`、`buildMergedTableBlock`、
  `absorbOrphanFragments`、`partitionRowsToLabels`、`pointInRect`、`intervalIndex`、`clusterBoundaries`、
  `nearestBoundaryIndex`、`extractCellText`、`buildSimpleTableBlock`、`extractCellLines`、
  `hasRaggedRowCellCounts`、`shellRegionOf`、`rectanglesAdjacentOrOverlapping`、`findRoot`/`unionSets`

文本提取：
- `extractTextBlocks`、`buildRawTextBlocks`、`groupFragmentsIntoLines`、`verticalOverlapRatio`、
  `LineGroup`、`buildLineBlock`、`splitLineFragmentsByEmbeddedOrderedListMarker`、
  `shouldBreakBeforeEmbeddedOrderedListMarker`、`splitTextBlockByEmbeddedOrderedMarkers`、
  `splitTailOrgDateIfNeeded`

行合并成段：
- `mergeLines`、`estimatePageRightEdge`、`shouldMerge`（两个重载）、`merge`、`mergeText`、
  以及 `shouldMerge` 依赖的判定子函数：`shouldBlockMergeAtChapterHeadingBoundary`、
  `isChapterPrefixWithTitleNamePair`、`hasTypographicBoundary`/`styleDifferent`、
  `isFullWidthHardWrapLeadLine`、`isFullWidthBodyLine`、`layoutBreak`、`isAcrossTable`、
  `isShortLabel`、`isNumericHierarchyHardWrapContinuation`、`shouldBlockLeftAlignedDateLineMerge`、
  `endsWithSentenceTerminator`、`endsWithSemanticBreak`、`isForcedContinuation`、
  `isInlineTailContinuation`、`startsWithContinuationPunctuation`、`shouldDropDuplicatedBoundary`、
  `needSpace`、`headingLastTopRef`

跨页/装饰性合并：
- `mergeCrossPageTables`（含 `CROSS_PAGE_TABLE_X_TOLERANCE_PT`）、`appendTableRows`、`firstRowTextsEqual`
- `demoteDecorativeSingleCellTables`（含 `DECORATIVE_SINGLE_CELL_MAX_LINES`）、
  `isSingleCellTableCandidate`、`convertRunIfDecorative`、`tableToTextBlock`
- `mergeCrossPageParagraphBlocks`、`shouldMergeCrossPageParagraph`

页级统计：
- `estimateBodyFontMode`

文本规范化与几何小工具（被上述函数广泛依赖，纳入本 Part）：
- `normalizeText`（两个重载）、`normalizeTableCellText`、`removeCharacterDoubling`、
  `mergeBrokenEnglishWords`、`mergeSingleDigitRuns`、`parseDigitFragment`、`isCommonShortWord`
- `shouldInsertSpaceByGeometry`、`isInsideAnyTable`、`overlapRatio`、`topDistanceFromPage`
- `firstNonSpaceChar`、`secondNonSpaceChar`、`lastNonSpaceChar`、`isChinese`、`isAsciiLetterOrDigit`
- `detectHeadingPrefixStyleMismatch`、`dominantStyle`（用于识别"编号前缀加粗、正文常规字重"的行内样式错配，
  是 `merge`/`shouldMerge` 用来避免虚假排版边界的关键信号）
- `isListItem`、`isHeadingByRegex`（这两个虽然名字听起来像"标题判定"，但 `shouldMerge`/`extractTextBlocks`
  路径本身直接调用它们做否决判定，此处按其实现给出，供 Part 2 复用同一份逻辑，不要重复实现）

不在本 Part、仅记调用点的函数（Part 2 及以后）：`isHeading`、`resolveHeadingLevel`、
`buildHeadingStyleProfile`、`HeadingStyleProfile`/`StyleClusterRole`/`StyleCluster`、
`ListGuideHeuristics`、`ShortPhraseListRunHeuristics`、`HeadingSequenceConsistencyHeuristics`、
`HeadingPatternQualityHeuristics`、`ChapterTocLineRemover`、`ChapterReferenceHeuristics`、
`HeadingSuppressHeuristics`、`HeadingLevelPrefixHeuristics`、`mergeWrappedHeadingLines`、
`canMergeHeadingPair`、`appendTextAsMarkdown`/`appendTextBodyAsMarkdown`、`renderTableMarkdown`、
`appendSingleCellTableAsText`、`fallbackMergeMarkdown`、`removeTocFromMarkdown`、`cleanOutput`。

## 常量与阈值

| 常量名 | 值 | 用途 |
|---|---|---|
| `TABLE_GEOMETRY_EPSILON_PT` | 1.5 | 表格聚类时相邻/重叠矩形的容差（pt），`clusterAbsorbedTables`/`rectangleContains`/`clusterBoundaries` 共用 |
| `CROSS_PAGE_TABLE_X_TOLERANCE_PT` | 6.0 | 跨页表左右边界对齐容差（pt） |
| `DECORATIVE_SINGLE_CELL_MAX_LINES` | 2.5 | 装饰性 1×1 表判定：run 内每个表 bbox 高度不超过相邻正文行高的这个倍数 |
| `PARAGRAPH_CONTINUATION_X_TOLERANCE_EM` | 3.6 | 段落延续时缩进/行首 X 偏移容差（em，按字号换算），覆盖中文首行缩进/悬挂缩进 |
| `FULL_WIDTH_RIGHT_GAP_TOLERANCE_EM` | 2.5 | 判定"铺满行宽"时右边缘距右边界的容差（em） |
| `CENTERED_HEADING_PAGE_RATIO` | 0.15（Part 2 用，列出备查） | 版心居中容差（相对页宽） |
| `Config.yMergePt` | 默认 3.0 | 行分组基础 Y 容差（pt），`groupFragmentsIntoLines` 里再乘 1.6 |
| `Config.fontSizeDeltaPt` | 默认 0.5 | 判定"排版边界"的字号差阈值（`styleDifferent`），也用于标题判定（Part 2） |
| `Config.indentThresholdPt` | 默认 8.0 | `layoutBreak` 缩进变化阈值 |
| `Config.xOffsetThresholdPt` | 默认 10.0 | `layoutBreak` 行首 X 偏移阈值；也用于 `shouldBlockLeftAlignedDateLineMerge` 的倍数基准 |
| `Config.lineSpacingMultiplier` | 默认 2.4 | `layoutBreak` 行距阈值的行高倍数（段落延续时最少放宽到 2.8） |
| `Config.tableOverlapRatio` | 默认 0.15 | `isInsideAnyTable` 判定文字碎片是否落入表格区域的重叠比例阈值 |
| `Config.headerTopRatio` | 默认 0.12 | 页顶页眉带高度占页高比例 |
| `Config.footerBottomRatio` | 默认 0.88 | 页底页脚带起始位置占页高比例（超过此比例视为页脚带） |
| `Config.headerPageNumberRatio` | 默认 0.12 | 页码块专用页顶阈值（`isPageNumberBlock`） |
| `Config.footerPageNumberRatio` | 默认 0.88 | 页码块专用页底阈值 |
| `Config.removePageNumbers` | 默认 true | 是否剔除页码块 |
| `Config.emitTraceComments` / `emitHeadingTrace` | 默认 false | 调试用 trace 注释开关（Part 2 渲染用，此处仅存在于 Config） |
| `Config.maxHeadingLength` | 默认 80 | 标题长度上限（Part 2 用） |
| `Config.headingMergeFontDeltaPt` / `headingMergeCenterTolerancePt` / `headingMergeMaxGapMultiplier` | 1.2 / 24.0 / 2.2 | 跨行标题合并阈值（Part 2 `canMergeHeadingPair` 用，本 Part 不涉及） |
| `Config.mergeWrappedHeadings` | 默认 true | 是否执行跨行标题合并（Part 2） |
| `Config.styleClusterHeadingEnabled` | 默认 true | 是否启用样式聚类（Part 2） |
| `Config.shortStopwords` (`EN_SHORT_STOPWORDS`) | 见下 | `mergeBrokenEnglishWords` 白名单，避免把常见英文短词误粘合 |
| 页眉页脚重复判定：`minRepeatedPages` | `max(2, ceil(pageCount * 0.30))` | `detectHeaderFooterFilter` 内联计算，非独立常量字段 |
| `overlapRatio` 判定阈值 | 0.30 | `isHeaderOrFooter` 中判断候选行是否落入某页"重复页眉/页脚区域" |
| 重复签名规范化中的最短长度 | 2（字符） | `headerFooterSignature` 归一化后长度 < 2 视为无效签名，返回空串 |
| `shouldInsertSpaceByGeometry` 基础间隙阈值 | 0.8 pt | 小于等于此值不插入空格（字符间距过小，视为同一视觉块内的正常字距） |
| `shouldInsertSpaceByGeometry` 数字-数字间隙阈值 | 3.0 pt | 两侧都是数字时，gap < 3.0 不插入空格（避免把 "1 234" 拆开的数字硬插空格） |
| `shouldInsertSpaceByGeometry` 字母-字母间隙阈值 | 2.0 pt | 两侧都是字母时，gap < 2.0 不插入空格 |
| `buildLineBlock` 重复碎片抑制重叠比例 | ≥ 0.5 | 同文本、同位置（矩形重叠比例）的碎片视为重复渲染，跳过（防止"招招标标"字符倍增） |
| `removeCharacterDoubling` 最小成对数 | 2 对 | 字符两两相同的对数 < 2 时不处理（避免"等等"这种合法叠字被破坏） |
| `styleClusterKeyOf` 分桶步长（Part 2 用，备查） | 字号 0.5pt / 缩进 8.0pt | `STYLE_CLUSTER_FONT_BUCKET_PT` / `STYLE_CLUSTER_INDENT_BUCKET_PT` |
| `estimateBodyFontMode` 默认桶 | 24（对应 12pt，`bucket = round(fontSize*2)`） | 直方图为空时的兜底众数字号 |
| `isShortLabel` 长度阈值 | < 8 字符 | 极短行（如字段标签）默认不与下一行合并，除非有其它放行信号 |
| `INLINE_TAIL_TOKEN` 放行的行距倍数 | ≤ 2.5 × maxLine | `isInlineTailContinuation` 判定内联 ASCII/数字尾巴（如 "AI+" "2025"）是否算同段续行 |

## 正则表达式

| 正则名 | 模式（原样复制） | 匹配的文本形态说明 |
|---|---|---|
| `TITLE_CN_NUM` | `^[一二三四五六七八九十百千万]+[、．.\s].*` | 中文数字编号起首，后跟顿号/间隔号/点/空白（如"一、"、"十二．"） |
| `TITLE_CN_PAREN` | `^[（(][一二三四五六七八九十百千万]+[)）][、．.\s]?.*` | 括号包裹的中文数字编号（如"（一）"、"(十二)、"） |
| `TITLE_CHAPTER` | `^第\s*([一二三四五六七八九十百千万零廿卅]+|\d+)\s*(章|节|纲|目|条).*` | "第 X 章/节/纲/目/条"结构化标题（X 为中文数字或阿拉伯数字） |
| `TITLE_NUM_SIMPLE` | `^\d+[.．、，](?!\d)\s*.*` | 纯阿拉伯数字编号 + 单层分隔符（"1." "2、" "3，"），分隔符后不能紧跟数字（避免把小数"98.5%"误判） |
| `TITLE_NUM_MULTI` | `^\d+(\.\d+)+\.?(?!\d|\.|\%|％).*$` | 多级数字编号（"1.2" "1.2.3."），结尾不得紧跟数字/点/百分号 |
| `TITLE_NUM_PAREN` | `^[（(]\s*\d+\s*[)）]\s*.*` | 括号包裹阿拉伯数字编号（"(1)" "（ 2 ）"） |
| `TITLE_NUM_SUFFIX` | `^\d+[)）】]\s*.*` | 数字后跟右括号/右方括号类后缀（"1)" "2）" "3】"） |
| `TITLE_ROMAN` | `^[IVXLCDMivxlcdm]+\.\s*.*` | 罗马数字编号 + 点（"I." "iv."） |
| `TITLE_ALPHA` | `^[A-Za-z]\.\s*.*` | 单个英文字母编号 + 点（"A." "b."） |
| `HEADING_PREFIX_ONLY` | `^\s*(?:[（(][一二三四五六七八九十百千万]+[)）]|[一二三四五六七八九十百千万]+[、．.]|第\s*(?:[一二三四五六七八九十百千万零廿卅]+|\d+)\s*(?:章|节|纲|目|条)|\d+(?:\.\d+)*[\.、)）】]?|[（(]\s*\d+\s*[)）]|[IVXLCDMivxlcdm]+\.|[A-Za-z][.．])` | 匹配"仅编号前缀本身"（不要求后续内容），用于定位一行文本里编号前缀的结束位置（`detectHeadingPrefixStyleMismatch` 用它切分前缀/正文两段做样式比较） |
| `OFFICIAL_DOCUMENT_TITLE_TAIL` | `.*的(?:通知|决定|决议|公告|通告|议案|报告|请示|批复|意见|函|纪要|命令|条例|规定|办法)$` | 公文标题收束词，如"……的通知"、"……的请示" |
| `ADDRESSEE_SALUTATION_LINE` | `^[\p{IsIdeographic}、，,\s]{1,40}[：:]$` | 主送机关/称谓行：1~40 个表意文字/顿号/逗号/空白，以中/英文冒号收尾（如"市政府:"） |
| `SENTENCE_BOUNDARY_PUNCT` | `[。.!！?？]` | 句末终止标点（计数用） |
| `CLAUSE_DENSE_PUNCT` | `[，、；：,:;]` | 逗顿分句类高密度标点（计数用，识别长段枚举说明） |
| `LIST_NUM_PREFIX` | `^(?:\d+[\.、)）\]](?!\d)|[（(]\s*\d+\s*[)）])\s*(.*)$` | 有序列表数字前缀 + 捕获组抓取正文（"1." "1、" "1)" "（1）"，避免把"98.5"当列表） |
| `ORDERED_LIST_MARKER_PREFIX` | `^(?:\d+[\.、)）\]](?!\d)|[（(]\s*\d+\s*[)）]).*` | 同上但不捕获，仅判断整行是否以有序列表前缀起首 |
| `EMBEDDED_ORDERED_LIST_MARKER` | `(?<!\d)(?:\d+[\.、)）\]](?!\d)|[（(]\s*\d+\s*[)）])` | 在文本中间查找嵌入的有序列表标记出现位置（前面不能是数字，避免匹配到"2.5.1"中间） |
| `DATE_AT_LINE_END` | `(\d{4}年\d{1,2}月\d{1,2}日)\s*$` | 行尾中文日期（捕获组抓日期本身），用于落款行拆分 |
| `STANDALONE_CHINESE_DATE_LINE` | `^\s*\d{4}年\d{1,2}月\d{1,2}日\s*$` | 整行仅为中文日期（纯落款日期行） |
| `ORG_SUFFIX_AT_END` | `([一-龥]{4,}(?:分局|人民政府|委员会|支队|大队|局))\s*$` | 行尾机构名后缀（≥4 个汉字 + 机构类型后缀词），用于落款拆分识别机构名 |
| `ATTACHMENT_ITEM_KEYWORDS`（字符串数组，非正则） | `{"统计表","示意图","现状照片","照片","附件"}` | 附件条目关键词（Part 2 使用，此处仅记录） |
| `LIST_BULLET` | `^[-+*•●○■□►→★☆]\s*.*` | 项目符号列表起首字符 |
| `CJK_SPACE` | `(?<=[一-龥])\s+(?=[一-龥])` | 两个中文字符之间的空白（用于收敛掉 CJK 间多余空格） |
| `CJK_TO_ASCII` | `(?<=[一-龥])(?=[A-Za-z])` | 中文紧邻拉丁字母的边界（零宽，插入空格用） |
| `ASCII_TO_CJK` | `(?<=[A-Za-z])(?=[一-龥])` | 拉丁字母紧邻中文的边界 |
| `NUM_UNIT` | `(?<=\d)(?=[a-zA-Z])` | 数字紧邻字母的边界（如"98kg"中间插空格变"98 kg"，注意方向与 CJK_TO_ASCII 不同） |
| `DIGIT_TO_CJK_SPACE` | `(?<=\d)\s+(?=[一-龥])` | 数字与中文之间的空白（收敛掉） |
| `CJK_TO_DIGIT_SPACE` | `(?<=[一-龥])\s+(?=\d)` | 中文与数字之间的空白（收敛掉） |
| `MULTI_SPACE` | `\s{2,}` | 连续 2+ 空白（压缩为单空格） |
| `ZERO_WIDTH` | `[​-‍﻿]` | 零宽字符（含 BOM），全部去除 |
| `ALPHA_TOKEN` | `^[A-Za-z]+$` | 纯字母 token（`mergeBrokenEnglishWords` 判定用） |
| `SINGLE_DIGIT_FRAGMENT` | `^(\D*)(\d)(\D*)$` | "前缀(非数字) + 单个数字 + 后缀(非数字)"的 token 形态，`mergeSingleDigitRuns` 用来把被拆散的多位数字重新拼接 |
| `CONTINUATION_PUNCTUATION`（字符串，非正则） | `，。；：、！？,.!?;:）)]】》` | 续行强制合并信号：下一行以这些标点开头，或与上一行末字符相同+第二字符属于此集合 |
| `INLINE_TAIL_TOKEN` | `^[A-Za-z0-9+\-_/().]{2,16}$` | 短小的 ASCII/数字尾巴 token（如"AI+"、"2025"），常被误判为独立新行 |
| `EN_SHORT_STOPWORDS`（集合，非正则） | `{a,an,am,as,at,be,by,do,go,he,i,in,is,it,me,my,no,of,on,or,so,to,up,us,we}` | 常见英文短词白名单，防止 `mergeBrokenEnglishWords` 把合法短词错误粘合 |
| `PAGE_NUMBER_BLOCK` | `^\s*(?:第\s*\d{1,5}\s*(?:页)?(?:\s*[-/|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?|(?:(?:共)?\s*\d{1,5}\s*(?:页)?)(?:\s*[-/|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?|[—]\s*\d{1,5}\s*[—])\s*$` | 页码块整行匹配：覆盖"第 3 页"、"第3页/共10页"、"3/10"、"共10页"、"—3—"等常见页码排版 |

> 注：原 Java 源码里 `PAGE_NUMBER_BLOCK` 的空白字符用的是 `\s` 但个别位置写成非转义的 `\s`（Java 源文本字面就是
> `\s`，某些位置疑似手误写成裸 `\s`——已核对第 129 行原文，实际都是 Java 正则里合法的 `\s`）；Go 端用
> `regexp`（RE2 语法）搬运时逐字符核对空白类即可，不需要按 PCRE 特殊处理。RE2 不支持 lookahead/lookbehind
> （`(?<=...)`/`(?!...)`），下面「与其他 Part 的接口」前的 gap 说明里给出替代实现思路。

## 数据结构

```go
package pdfconv

import "github.com/ivanvanderbyl/docmill/v2/pkg/geom"

// GeometricElement 是 TextBlock 与 TableBlock 的公共几何视图，供排序与跨类型合并使用。
// Java 原型是 abstract class GeometricElement，Go 用接口 + 内嵌 struct 表达。
type GeometricElement interface {
    ID() string
    PageNo() int
    BBox() *geom.Box // nil 表示无有效包围盒（Java 中 bbox 可为 null）
    TopDistance() float64
    Left() float64
}

// baseElement 内嵌进 TextBlock/TableBlock，提供 GeometricElement 的默认实现。
type baseElement struct {
    id          string
    pageNo      int
    bbox        *geom.Box
    topDistance float64 // 距页顶距离；pageHeight - box.URY（TOPLEFT 语义，见下方 gap 说明）
    left        float64 // bbox.LLX（Java Rectangle 的 LLX）
}

// TextBlock 对应 Java 的 TextBlock（一整条视觉行，或多行合并后的段落）。
type TextBlock struct {
    baseElement

    Text         string
    FontSizeMean float64
    FontFamily   string  // 归一化前的原始 BaseFont 名（docmill: page.TextCell.FontName）
    FontWeight   int     // 400=常规, 700=粗体（取行内 fragments 的最大值）
    Italic       bool
    MonoFont     bool
    LineHeight   float64
    IndentLeft   float64 // 等同 Left，历史遗留的重复字段（Java 里 indentLeft 与 left 同值同来源）
    TableID      int     // -1 表示非表格块（此处的 TableBlock 均设为 -1；仅 tableToTextBlock 转换后可能继承来源表格）
    BodyFontMode float64 // 当页正文众数字号，来自 estimateBodyFontMode

    // 以下四个字段服务于「跨行标题合并」（Part 2 mergeWrappedHeadingLines/canMergeHeadingPair），
    // 但在本 Part 的 merge()/buildLineBlock() 中就已产生和传递，故在此一并定义：
    HeadingLastLineTop    float64 // 链式合并后最后一条原始行的 topDistance；NaN 表示未合并（单行）
    HeadingTrailingLeft   float64 // 链式合并后最后一条原始行的 left；NaN 表示未设置
    HeadingTrailingText   string  // 链式合并后最后一条原始行的规范化文本；空串表示未设置
    PageWidth             float64
    HeadingPrefixStyleMismatch bool // 编号前缀与正文字体/字重/字号不一致（detectHeadingPrefixStyleMismatch）
}

// WithText 返回替换 Text 后的副本，其余字段原样复制（对应 Java TextBlock.withText）。
func (t TextBlock) WithText(newText string) TextBlock { t.Text = newText; return t }

// TableCellData 对应 Java TableCellData（record 风格的不可变值对象）。
type TableCellData struct {
    Row, Col         int
    RowSpan, ColSpan int
    Text             string
}

// TableBlock 对应 Java 的 TableBlock。
type TableBlock struct {
    baseElement

    RowCount, ColCount int
    Cells              []TableCellData
    // 1 行 1 列表格按视觉行还原的文本（保留原始换行，供渲染代码块/SQL 用）；nil 表示非单格表。
    SingleCellLines []string
}

// LineGroup 对应 Java 的 private LineGroup（groupFragmentsIntoLines 内部聚类累加器）。
type lineGroup struct {
    topMean, yMin, yMax float64
    n                   int
    items               []page.TextCell
}

// StyleToken 对应 Java StyleToken：记录一行文本某个字符区间 [start,end) 的样式来源。
type styleToken struct {
    start, end int
    fontFamily string
    fontWeight int
    fontSize   float64
}

// StyleSignature 对应 Java StyleSignature：dominantStyle 的返回值形状。
type styleSignature struct {
    fontFamily string
    fontWeight int
    fontSize   float64
}

// headerFooterLine / headerFooterFilter 对应 Java 同名内部类。
type headerFooterLine struct {
    pageNo    int
    bbox      geom.Box
    signature string
}

type headerFooterFilter struct {
    pageCount         int
    repeatedZonesByPage map[int][]geom.Box
    repeatedSignatures  map[string]struct{}
}
```

字段与 docmill 源数据的对应关系：

- `page.TextCell.Text` → 碎片文本（Java `TextFragment.getText()`）。
- `page.TextCell.Box`（`geom.Box`）→ 碎片矩形（Java `TextFragment.getRectangle()`）。注意 docmill 的
  `geom.Box` 有显式 `Origin`（`TOPLEFT`/`BOTTOMLEFT`），而 Aspose 的 `Rectangle` 坐标系固定是 PDF 原生的
  **左下角为原点、Y 向上**（等价于 docmill 的 `BOTTOMLEFT`）。所有几何计算（`getLLX/getLLY/getURX/getURY`）
  移植前必须先把 docmill 的 `Box` 统一转换/校验到 `BOTTOMLEFT` 语义，否则 `topDistanceFromPage`、
  `groupFragmentsIntoLines` 的 Y 排序方向会整体颠倒。建议在提取层入口写一个
  `toBottomLeft(box geom.Box, pageHeight float64) geom.Box` 归一化函数，本文档后续所有"矩形"字段一律按
  `BOTTOMLEFT` 语义描述（`LLX/LLY/URX/URY` 对应 Java `getLLX()/getLLY()/getURX()/getURY()`）。
- `page.TextCell.FontSize` → `TextFragment.getTextState().getFontSize()`。
- `page.TextCell.FontName` → `Font.getFontName()`（Java 端还额外判断了 `font != null` 才取值，docmill 应保证非空或提供合理默认 "unknown"）。
- `page.TextCell.FontWeight` → Java 用 `state.getFontStyle() & FontStyles.Bold` 做的是**布尔**判断（非 0/700 二值），
  docmill 给的是连续 OpenType 权重值；移植时按 `FontWeight >= 700` 映射到 Java 的"Bold"语义，
  由此得到的 `fontWeightMax`/`partWeight` 逻辑不变（取行内最大值，>=700 记为 700，否则 400）。
- `page.TextCell.FontFlags` bit6（值 64）→ 斜体，对应 Java `(style & FontStyles.Italic) != 0`。
- Mono 字体判断（`courier`/`consolas`/`mono` 出现在字体名小写形式中）不依赖 Aspose 专有能力，直接照搬字符串匹配。
- Aspose 的 `AbsorbedTable`/`AbsorbedRow`/`AbsorbedCell`/`TableAbsorber` 是表格专用识别器，**docmill 没有直接
  等价物**——docmill 的 `pkg/table`（OTSL/无边框表格识别）解决的是同一问题但算法路径不同。本文档第 4 节
  「算法：表格提取」在每个依赖 Aspose 表格模型字段的地方标注了 gap 和基于 `page.TextCell`/`geom.Box` 的
  替代实现思路；是否直接复用 `pkg/table` 而非移植这套算法，由人工决策，不在本文档内下结论。

## 与其他 Part 的接口

（见文末章节，先给出全部算法节。）

## 算法：convertDocument（仅提取阶段部分）

对应 Java：`PdfToMarkdown.convertDocument`（第 198-280 行）。本 Part 只覆盖到第 232 行（排序 + 三次合并处理）
为止；`elements` 中此后被过滤/渲染的部分（`ListGuideHeuristics`、`HeadingStyleProfile`、标题序列一致性、
`appendTextAsMarkdown` 及以后）属于 Part 2/3。

步骤：

1. 对整份文档跑一次 `detectHeaderFooterFilter`，得到全文级的 `headerFooterFilter`（页眉/页脚重复签名 + 区域）。
2. 对每一页（`pageNo` 从 1 到 `pageCount`）：
   a. 取页高 `pageHeight`。
   b. `extractTableBlocks(page, pageNo, pageHeight, config)` → 得到该页 `tables []TableBlock`。
   c. `estimateBodyFontMode(page, pageNo, pageHeight, config, headerFooterFilter)` → 得到该页正文众数字号 `bodyFontMode`。
   d. `extractTextBlocks(page, pageNo, pageHeight, tables, bodyFontMode, config, headerFooterFilter)` → 得到 `textBlocks []TextBlock`（已合并成段，已排除表格覆盖区、页眉页脚）。
   e. 若 `config.RemovePageNumbers`，用 `isPageNumberBlock` 过滤掉页码块。
   f. 把 `tables`、`textBlocks` 都追加进全局 `elements []GeometricElement`。
3. 全局排序：`elements` 按 `(PageNo, TopDistance, Left)` 三元组升序排序（page 优先、同页按距页顶距离、
   再按左边距）——这是"阅读顺序"排序，Go 用 `sort.SliceStable` 加三元比较函数实现，需保持稳定排序语义
   （Java `Comparator.thenComparing` 链式比较是稳定的）。
4. `elements = mergeCrossPageTables(elements)`。
5. `elements = demoteDecorativeSingleCellTables(elements)`。
6. `elements = mergeCrossPageParagraphBlocks(elements, config)`。
7. 此后（Part 2 起）：过滤非等宽 `TextBlock` 得到 `orderedTextBlocks`、跑列表/标题样式识别、渲染 Markdown、
   收尾清理。本 Part 到第 6 步为止，产出 `elements []GeometricElement`（已排序、已完成三种跨块合并）。

## 算法：detectHeaderFooterFilter

对应 Java 第 4011-4057 行。目标：在全文档扫描一遍，找出"在多页上、相同规范化文本、且落在页眉/页脚几何带内"
的行，把它们的签名和逐页出现区域记下来，供后续逐行判定复用（避免逐页孤立判断导致把偶然落在页顶/页底的
正文误删，也避免遗漏没有严格对齐但文本相同的页眉/页脚）。

1. `pageCount` = 文档页数；若 ≤ 0，直接返回空 `headerFooterFilter{pageCount, 空map, 空集合}`。
2. 对每一页：
   a. 用文字碎片提取器拿到该页全部文字碎片（保留 `Box != nil` 的）。
   b. 按 `groupFragmentsIntoLines`（见后）把碎片聚类成视觉行。
   c. 对每一行调用 `buildHeaderFooterLineCandidate`：
      - 按 `LLX` 升序排列该行碎片；
      - 用 `shouldInsertSpaceByGeometry` 决定碎片间是否插空格，拼接成整行文本；同时维护该行的并集包围盒（`LLX/LLY/URX/URY` 分别取所有碎片对应值的 min/min/max/max）；
      - 若拼接文本为空或包围盒无效，返回 nil（不是候选）；
      - 若该行包围盒不落在页眉/页脚几何带内（`isInHeaderFooterBand`），返回 nil；
      - 用 `headerFooterSignature` 计算该行文本的规范化签名；若签名为空串，返回 nil；
      - 否则返回 `{pageNo, bbox, signature}`。
   d. 把候选行加入全局候选列表，并把 `signature -> {出现过的 pageNo 集合}` 累积到 `signaturePages`。
3. 计算 `minRepeatedPages = max(2, ceil(pageCount * 0.30))`。
4. 遍历 `signaturePages`：出现页数 ≥ `minRepeatedPages` 的签名加入 `repeatedSignatures`。
5. 再遍历一次全部候选行：签名属于 `repeatedSignatures` 的，把其包围盒累加进 `zonesByPage[pageNo]`。
6. 返回 `{pageCount, zonesByPage, repeatedSignatures}`。

**headerFooterSignature**（对应 Java 第 4111-4120 行）：
1. 用 `normalizeText` 规范化文本并转小写。
2. 把所有连续数字串替换为单个 `#`（抹平页码等易变部分，如"第 3 页"和"第 4 页"变成同一签名）。
3. 把所有标点符号（Unicode Punctuation 类）替换为空格。
4. 压缩连续空白为单空格并去首尾空白。
5. 若结果长度 < 2，返回空串（视为无效签名，不参与重复统计）。

## 算法：isInHeaderFooterBand / isHeaderOrFooter

对应 Java 第 3959-4009 行。

`isInHeaderFooterBand(topDistance, pageHeight, config)`：
- `topDistance < pageHeight * config.HeaderTopRatio` 或 `topDistance > pageHeight * config.FooterBottomRatio` 则返回 true。
- 重载版本先用 `topDistanceFromPage(rect, pageHeight)` 算出 `topDistance` 再调用上面这个。

`isHeaderOrFooter(rect, pageNo, pageHeight, config, filter, lineText)`（判定某一行是否应作为页眉/页脚剔除）：
1. **重复签名优先**：若 `filter != nil` 且该行落在页眉/页脚几何带内（`isInHeaderFooterBand`）且
   `headerFooterSignature(lineText, config)` 属于 `filter.RepeatedSignatures`，直接返回 true（剔除）。
   —— 这一步的用意：即使这行文本形态上很像标题（例如一个纯中文公司名），只要它在多页同一带内重复出现，
   仍然判定为真页眉/页脚。
2. **标题保护**：调用 `ChapterTocLineRemover.shouldPreserveInHeaderFooterBand(normalizeText(lineText, config))`
   （Part 2 范围的函数，此处只记调用点——用于识别"看起来像章节标题、不应被当页眉页脚删掉"的行），
   若为 true，返回 false（保留，不剔除）。注意这一步在第 1 步之后，即重复签名的判定优先级更高。
3. 若 `filter != nil`：
   a. 取该页已知的重复页眉/页脚区域列表 `filter.RepeatedZonesByPage[pageNo]`；若非空，遍历这些区域，
      只要该行矩形与某个区域的 `overlapRatio >= 0.30`，返回 true；遍历完都不满足则返回 false。
      —— 即：一旦文档里发现过重复签名（哪怕只在别的页/别的位置），该页的判定就完全依赖"是否落入已知重复区域"，
      不再退化到纯几何带判断。
   b. 若该页列表为空（即全文都没发现任何重复页眉/页脚），且 `filter.PageCount <= 1`（单页文档，重复检测天然
      无法工作），退化为纯几何带判断 `isInHeaderFooterBand(rect, pageHeight, config)`。
   c. 否则（多页文档但确实全文都没有重复页眉/页脚特征），直接返回 false —— 不做任何几何带剔除，
      避免把每页顶部第一段正文误删。
4. 若 `filter == nil`（理论上不会发生，因为总是先构建 filter），退化为纯几何带判断。

## 算法：shouldDropAsHeaderFooterLine

对应 Java 第 3913-3934 行。在整行拼接完成后（而非碎片级）判定，因为标题保护逻辑需要整行文本。

1. 若 `block` 为空或其文本为空白，返回 false。
2. 规范化文本；若规范化后为空，返回 false。
3. 用该行全部碎片的并集矩形（`unionFragmentBBox`）；若无法算出有效矩形（无碎片/矩形为 nil）：
   退化判断——若 `ChapterTocLineRemover.shouldPreserveInHeaderFooterBand(text)` 为 false 且
   `isInHeaderFooterBand(block.TopDistance, pageHeight, config)` 为 true，则剔除。
4. 否则调用上面的 `isHeaderOrFooter(bbox, pageNo, pageHeight, config, filter, text)`。

`unionFragmentBBox`：对一组碎片取 `LLX/LLY/URX/URY` 的 min/min/max/max 得到并集矩形；任一维度算不出有限值
（碎片列表为空或矩形全为 nil）则返回 nil。

## 算法：isPageNumberBlock

对应 Java 第 4152-4159 行。

1. `block` 为空返回 false。
2. 取其规范化前的原始文本（注意：Java 这里直接用 `block.text.trim()`，**没有**先跑 `normalizeText`）。
3. 用 `PAGE_NUMBER_BLOCK` 全字符串匹配（`matches`，不是 `find`）；不匹配返回 false。
4. 取 `block.TopDistance`；若 `< pageHeight * config.HeaderPageNumberRatio` 或
   `> pageHeight * config.FooterPageNumberRatio`，返回 true，否则 false。
   —— 页码专用阈值与页眉页脚过滤阈值是两组独立配置（`headerPageNumberRatio`/`footerPageNumberRatio` vs
   `headerTopRatio`/`footerBottomRatio`），默认值相同（0.12/0.88）但可独立调整。

## 算法：extractTableBlocks

对应 Java 第 1277-1301 行。

1. 用表格识别器（Aspose: `TableAbsorber`；docmill: 见下方 gap 说明）扫描整页，得到该页全部 `AbsorbedTable` 列表。
2. `clusterAbsorbedTables` 把这些表按几何相邻/重叠关系聚类成簇（同一簇代表被拆散的同一张逻辑表）。
3. 对每个簇：
   a. `dropContainerDuplicates(cluster)` 去掉"把整表文字揉成一坨的外壳重复表" → 得到 `effective` 列表；若为空，跳过该簇。
   b. `shellRegionOf(cluster, effective)` 计算被丢弃的外壳表并集区域（无外壳时为 nil）。
   c. 判定 `simpleUnmergedTable = (effective 只有 1 个 && shellRegion == nil && !hasRaggedRowCellCounts(effective[0]))`。
   d. 若 `simpleUnmergedTable`：调用 `buildSimpleTableBlock`（按下标直接映射行列，路径更简单更快）；
      否则：调用 `buildMergedTableBlock`（按真实几何坐标重建网格，处理合并单元格/被拆散的多子表）。
   e. 若构建结果非 nil，加入结果列表，`index++`（`index` 用于生成块 ID）。
4. 返回 `TableBlock` 列表。

**Aspose → docmill gap**：Aspose 的 `TableAbsorber` 是专门的表格边框/网格检测器，直接产出
`AbsorbedTable → AbsorbedRow → AbsorbedCell` 层级结构，`AbsorbedCell` 自带矩形范围和内部文字碎片列表。
docmill 没有暴露同名概念；候选方案二选一（留给人工决策）：
- **方案 A**：直接使用 docmill 的 `pkg/table`（OTSL/无边框表格识别）产出表格结构，绕开本节全部
  `AbsorbedTable`/`Cluster`/`Shell`/`Orphan` 算法，用 `pkg/table` 自己的合并单元格/网格重建逻辑。
- **方案 B**：完整移植本节算法，此时"表格边框检测"这一步需要自己实现——从 `page.TextCell` 无法直接得到
  Aspose 检测出的表格线/边框，必须退回到"用文字碎片的几何分布聚类出网格"这条路（即本节 `buildMergedTableBlock`
  + `absorbOrphanFragments` 这条纯几何路径本身就是这样做的，只是 Aspose 版本先用真实表格线做了一次初步分组，
  Go 版本可能需要一开始就用全页文字碎片走 `buildMergedTableBlock` 的网格重建逻辑，而不是"先有 AbsorbedTable
  再合并"）。docmill 若不提供任何表格边框检测能力，方案 B 需要新增一个"从纯文字碎片直接聚类出候选表格区域"
  的前置步骤，这一步 Java 源码里完全没有对应实现（因为 Aspose 免费提供了这一步），是本文档能指出的最大 gap，
  具体算法需要另行设计，不在移植范围内直接给出。

## 算法：hasRaggedRowCellCounts

对应 Java 第 1310-1322 行。

1. 取该表全部行中"单行最多单元格数" `maxCells`。
2. 若存在任意一行单元格数 `!= maxCells`，返回 true（说明存在合并单元格，行内单元格数量不整齐）。
3. 否则返回 false。

用途：`hasRaggedRowCellCounts == true` 时不能用"数组下标即列号"的简单假设（`buildSimpleTableBlock`），
必须走 `buildMergedTableBlock` 的几何重建路径。

## 算法：shellRegionOf

对应 Java 第 1325-1340 行。

1. 遍历 `cluster` 中每个表 `t`：若 `t` 不在 `effective` 列表中（按引用相等判断，Go 用指针比较或索引标记），
   累加进 `region`（初值 nil，首次赋值即为该表矩形，之后用 `unionRect` 累加）。
2. 返回 `region`（可能为 nil，表示簇内没有被丢弃的外壳表）。

## 算法：buildSimpleTableBlock

对应 Java 第 1342-1377 行。

1. `rowCount` = 表的行数；`colCount` = 各行单元格数的最大值（正常情况下应处处相等，因为已保证非 ragged）。
2. 逐行逐列（下标 `r`, `c` 直接作为 `Row`/`Col`）取每个 `AbsorbedCell`，`rowSpan=colSpan=1`，
   文本 = `normalizeTableCellText(extractCellText(cell), config)`，组装成 `TableCellData` 列表。
3. 若 `cells` 为空，返回 nil。
4. `bbox` = 该表整体矩形（Aspose: `AbsorbedTable.getRectangle()`；docmill 侧需自行按参与构建该表的所有文字碎片的并集矩形计算，因为没有边框检测器直接给出的"表格矩形"这个概念——若采用 docmill `pkg/table`，其应有等价字段）。
5. 若该表是 1 行 1 列（`isSingleCellTable`），额外用 `extractCellLines` 按视觉行切出 `singleCellLines`；否则为 nil。
6. 组装并返回 `TableBlock{id: "TableBlock_{pageNo}_{index}", pageNo, bbox, topDistance: topDistanceFromPage(bbox, pageHeight), left: bbox.LLX, rowCount, max(colCount,1), cells, singleCellLines}`。

## 算法：clusterAbsorbedTables（含 findRoot/unionSets）

对应 Java 第 1421-1461 行。经典并查集（Union-Find）几何聚类。

1. `n` = 该页识别出的表格数；`parent[i] = i` 初始化（每个表自成一簇）。
2. 双重循环 `i < j`：若 `rectanglesAdjacentOrOverlapping(tables[i].rect, tables[j].rect, TABLE_GEOMETRY_EPSILON_PT)`
   为真，执行 `unionSets(parent, i, j)`。
3. `findRoot(parent, i)`：路径压缩查找根（`parent[i] = parent[parent[i]]` 后继续跳，直到 `parent[i]==i`）。
4. `unionSets(parent, a, b)`：找两者的根，若不同则把 a 的根指向 b 的根（无按秩合并，简单版）。
5. 按根节点分组（用有序 map/slice 保持插入顺序，Go 中用 `map[int][]int` + 记录首次出现顺序或用切片+索引法保序），
   每组即为一个簇，返回 `[][]AbsorbedTable`。

**rectanglesAdjacentOrOverlapping(a, b, eps)**：
```
!(a.LLX - eps > b.URX || b.LLX - eps > a.URX || a.LLY - eps > b.URY || b.LLY - eps > a.URY)
```
即两矩形在 X 和 Y 方向都不存在"间隙超过 eps"的情况——等价于"矩形重叠或间隙 ≤ eps"。

## 算法：dropContainerDuplicates

对应 Java 第 1467-1485 行。

1. 若簇内只有 1 个表，原样返回。
2. 遍历簇内每个候选表 `candidate`：
   - 若不是 1 行 1 列表（`!isSingleCellTable`），保留。
   - 否则，计算除它以外其余表的并集矩形 `othersUnion`（`unionRectangleExcluding`）；若 `othersUnion` 非空
     且 `candidate` 的矩形以容差 `TABLE_GEOMETRY_EPSILON_PT` 包住 `othersUnion`（`rectangleContains`），
     则丢弃这个候选（它是把其余碎片表文字揉成一坨的外壳重复表）；否则保留。
3. 若过滤后为空（异常情况的兜底），返回原始簇而非空列表。

**isSingleCellTable**：行数为 1 且该行单元格数为 1。

**unionRectangleExcluding(cluster, excluded)**：遍历簇内除 `excluded` 外的每个表，累加 `unionRect`；全部被排除时返回 nil。

**rectangleContains(outer, inner, eps)**：`outer.LLX-eps <= inner.LLX && outer.LLY-eps <= inner.LLY && outer.URX+eps >= inner.URX && outer.URY+eps >= inner.URY`。

**unionRect(a, b)**：`{LLX: min, LLY: min, URX: max, URY: max}` 逐维取值。

## 算法：buildMergedTableBlock

对应 Java 第 1529-1626 行。这是表格提取里最复杂的部分：把可能被识别成多个碎片表的同一张逻辑表，
用所有单元格的真实几何坐标重建统一网格。

1. 收集参与合并的全部 `tables`（即 `effective` 列表）中，每个表每行每个单元格的矩形与文本（`extractCellText`
   + `normalizeTableCellText`），存入平行数组 `cellRects`/`cellTexts`；同时累计所有表的并集矩形 `union`。
2. 若 `cellRects` 为空或 `union` 为 nil，返回 nil。
3. 若存在 `shellRegion`（被丢弃的外壳表并集），把它并入 `union`。
4. 收集所有单元格矩形的 `LLX`/`URX` 到 `xs`，`LLY`/`URY` 到 `ys`；若有 `shellRegion`，额外把
   `shellRegion` 的 `LLX/URX/LLY/URY` 也加入——这是为了给外壳超出碎片并集范围的行/列（如整条未被识别出网格的
   标签列、表头带）补上分界线。
5. `xBounds = clusterBoundaries(xs, TABLE_GEOMETRY_EPSILON_PT)`，`yBounds` 同理。
   `colCount = len(xBounds)-1`，`rowCount = len(yBounds)-1`；任一 < 1 则返回 nil。
6. 初始化 `covered [rowCount][colCount]bool`（全 false）、`colHasKeptCell [colCount]bool`（全 false）、
   `byGridOrigin map[(row,col)]TableCellData`（用 `row*100000+col` 编码 key，Go 可直接用 `struct{row,col int}` 做 map key，无需该编码技巧）。
7. 遍历每个已知单元格矩形 `rect`（与其文本 `text`）：
   a. `leftIdx = nearestBoundaryIndex(xBounds, rect.LLX)`，`rightIdx = nearestBoundaryIndex(xBounds, rect.URX)`。
   b. `bottomIdx = nearestBoundaryIndex(yBounds, rect.LLY)`，`topIdx = nearestBoundaryIndex(yBounds, rect.URY)`。
   c. `col = leftIdx`；`row = rowCount - topIdx`（因为行号按"自上而下"编号，而 Y 坐标是自下而上的，
      `topIdx` 越大代表越靠近页面顶部意味着 Y 值越大，`rowCount - topIdx` 把它换算成从上往下数的行号）。
   d. 若 `row`/`col` 越界（`<0` 或 `>=` 对应 count），跳过该单元格。
   e. `colSpan = clamp(max(1, rightIdx-leftIdx), 1, colCount-col)`；`rowSpan` 同理用 `topIdx-bottomIdx`。
   f. 若该格子位置 `(row,col)` 尚未记录，或已记录但文本为空而新文本非空，则写入/覆盖
      `byGridOrigin[(row,col)] = {row, col, rowSpan, colSpan, text}`。
   g. 把 `[row, row+rowSpan) × [col, col+colSpan)` 范围内的 `covered` 全部标记 true；
      把 `[col, col+colSpan)` 范围内的 `colHasKeptCell` 标记 true。
8. 若 `byGridOrigin` 为空，返回 nil。
9. 若存在 `shellRegion`，调用 `absorbOrphanFragments` 用全页文字碎片（不局限于已识别单元格内的碎片）
   补齐外壳区域内未覆盖的格子。
10. 组装 `cells = values(byGridOrigin)`，返回
    `TableBlock{id, pageNo, bbox: union, topDistance: topDistanceFromPage(union, pageHeight), left: union.LLX, rowCount, colCount, cells}`
    （无 `singleCellLines`，因为走到这条路径说明不是简单单格表）。

**注意行号方向**：这是 PDF 坐标系（Y 向上）与"从上往下数第几行"语义之间的转换点，Go 端在这里最容易出 bug，
移植时应写单元测试覆盖"3 行 2 列，跨越合并单元格"的构造用例验证 `row`/`col` 换算方向正确。

## 算法：absorbOrphanFragments（含 partitionRowsToLabels）

对应 Java 第 1633-1815 行。目的：把落在外壳区域内、但不属于任何已识别单元格的"孤儿"文字碎片
（常见于纵向合并的分类标签列、未被识别出网格的表头带整行），按几何位置吸附进网格。

**第一部分：定位孤儿碎片并按格子分组**（1633-1681 行）：
1. 用文字碎片提取器重新扫描整页全部碎片（不再局限于已知单元格内的碎片）。
2. 对每个碎片：若矩形或文本为空跳过；算出碎片中心点 `(cx, cy)`；若中心点不落在 `shellRegion` 内跳过。
3. 若中心点落在任一已知单元格矩形（`cellRects`）内，视为"已属于某个已识别单元格"，跳过（不是孤儿）。
4. 否则：`col = intervalIndex(xBounds, cx)`；`rowFromBottom = intervalIndex(yBounds, cy)`；
   `row = rowCount - 1 - rowFromBottom`（同样是 Y 向上坐标到"从上往下行号"的转换，这里因为
   `intervalIndex` 返回的是区间下标（0-based，从下往上数），转换公式与 `nearestBoundaryIndex` 场景略有不同，
   要按代码逐字核对，不要自行"化简"）。
5. 若 `row`/`col` 越界跳过；否则加入 `orphanByGrid[(row,col)]`。

**第二部分：把每个格子内的孤儿碎片拼接成文本**（1682-1722 行）：
6. 若 `orphanByGrid` 为空，直接返回。
7. 对每个 `(row, col)` 分组：若该格已被 `covered` 标记，跳过（说明后来被其它逻辑占用，理论上不会发生但要防御）。
8. 组内碎片先按 `-URY` 升序（即 Y 从高到低，从上往下）、再按 `LLX` 升序排序（先按行、再按列的自然阅读顺序）。
9. 依次拼接（用 `shouldInsertSpaceByGeometry` 决定是否插空格），同时累加碎片中心 Y 值之和 `sumCy`。
10. 若拼接结果非空：`byGridOrigin.putIfAbsent((row,col), {row,col,1,1,text})`（**不覆盖**已存在的值——
    但走到这里 `covered[row][col]` 已在第 7 步确认为 false，所以这个格子理论上一定不存在，`putIfAbsent`
    只是防御性写法），标记 `covered[row][col]=true`，并记录 `placed = append(placed, {row, col, centerY: sumCy/count, text})`。

**第三部分：纵向合并标签列的连续划分**（1723-1751 行）：
11. 对每一列 `c`：若 `colHasKeptCell[c]` 为真（该列有真实识别出的单元格），跳过——只处理"整列都靠孤儿碎片撑起来"的列（典型场景：纵向合并单元格的分类标签，如"出勤分/结果分/……"这种跨多行的标签列）。
12. 收集该列所有 `placed` 项作为 `labels`，若为空跳过。
13. 按 `-centerY` 排序（Y 从高到低，从上往下）。
14. 调用 `partitionRowsToLabels(labels 的 centerY 数组, yBounds)` 得到每行应归属的标签下标 `rowLabel[]`。
15. 对该列每一行 `r`：若 `covered[r][c]` 已为 true 跳过；否则用 `labels[rowLabel[r]].text` 填入
    `byGridOrigin[(r,c)] = {r,c,1,1,label.text}`，标记 `covered[r][c]=true`。

**partitionRowsToLabels(labelCentersY, yBounds)**（1758-1815 行）：把 `rowCount` 行（自上而下）连续划分给
`k = len(labelCentersY)` 个标签（`labelCentersY` 已按自上而下/Y 降序排列），最小化"每段行带几何中心与其
标签中心的绝对偏差之和"。

- 若 `k >= rowCount`：退化为逐行就近分配——对每行取其几何中心 Y（`(yBounds[rowCount-1-r] + yBounds[rowCount-r]) / 2`），
  在全部标签里找与之最接近的下标。
- 否则用动态规划求最优连续划分：
  - `segCenter[a][b]` = 把（自上而下第 a 到第 b 行，含）视作一段时，该段的几何中心
    `(yBounds[rowCount-a] + yBounds[rowCount-1-b]) / 2`。
  - `dp[i][r]` = 把前 `r` 行分给前 `i` 个标签的最小总偏差；`dp[0][0]=0`，其余初始化为 `+Inf`。
  - 转移：`dp[i][r] = min over s in [i-1, r) of dp[i-1][s] + |segCenter[s][r-1] - labelCentersY[i-1]|`，
    同时记录最优切分点 `choice[i][r] = s`。
  - 循环边界：`i` 从 1 到 `k`；`r` 从 `i` 到 `rowCount-(k-i)`（保证剩余标签有足够行可分）；`s` 从 `i-1` 到 `r-1`。
  - 回溯：从 `(i=k, r=rowCount)` 开始，每步取 `s=choice[i][r]`，把 `[s, r)` 这段行标记为标签 `i-1`，
    然后 `r=s`，`i--`，直到 `i=0`。
- 返回 `rowLabel[rowCount]`，每个元素是该行（自上而下第几行）对应的标签下标。

## 算法：pointInRect / intervalIndex / clusterBoundaries / nearestBoundaryIndex

对应 Java 第 1817-1858 行。均为纯几何/数值小工具：

- **pointInRect(x, y, rect)**：`x in [rect.LLX, rect.URX] && y in [rect.LLY, rect.URY]`（闭区间）。
- **intervalIndex(ascendingBounds, value)**：从 `len(bounds)-2` 递减扫到 1，返回第一个满足
  `value >= bounds[i]` 的下标 `i`；扫不到则返回 0。即"value 落在哪个 `[bounds[i], bounds[i+1])` 区间"，
  越界值钳制到边界区间。
- **clusterBoundaries(raw, tolerance)**：把一组坐标值排序后，相邻两个差值 `> tolerance` 才算新的分界线，
  否则视为同一条线合并掉（去重）。返回升序去重后的分界线数组。
- **nearestBoundaryIndex(ascending, value)**：线性扫描找与 `value` 差的绝对值最小的下标（非二分，
  因为分界线数组通常很短）。

## 算法：extractCellText

对应 Java 第 1860-1885 行。

1. 取该单元格全部文字碎片。
2. 按 `-URY` 升序、`LLX` 升序排序（先按 Y 从高到低分行、再按 X 从左到右——但这里**没有真正按行分组**，
   只是简单排序，因此多行单元格的文字会被拼接成一整行，换行信息丢失——这正是 `extractCellLines` 存在的原因）。
3. 依次拼接文本，用 `shouldInsertSpaceByGeometry` 决定是否插空格。
4. 返回拼接结果（未经 `normalizeTableCellText`，调用方负责规范化）。

## 算法：extractCellLines

对应 Java 第 1384-1416 行。与 `extractCellText` 的区别：先用 `groupFragmentsIntoLines` 把单元格内碎片
按 Y 距离正确聚类成视觉行，再逐行拼接，从而保留原始换行结构（用于渲染代码块/SQL 等需要保留分行的场景）。

1. 取单元格全部有效矩形的碎片。
2. `groupFragmentsIntoLines(fragments, pageHeight, config)` 得到分行结果。
3. 对每一行：按 `LLX` 排序后用 `shouldInsertSpaceByGeometry` 拼接文本，再 `normalizeText` 规范化。
4. 跳过规范化后为空白的行，其余按顺序加入结果列表。

## 算法：groupFragmentsIntoLines（含 verticalOverlapRatio、LineGroup）

对应 Java 第 2068-2146 行。核心几何算法之一：把碎片按 Y 距离聚类成视觉行，用聚类而非简单取整分桶，
避免把同一行里字号不同、基线略有差异的短小碎片（如上标、"AI+""2025"这类 ASCII 尾巴）误判为独立新行。

1. 若无碎片，返回空。
2. 复制并排序碎片：先按 `topDistanceFromPage(rect, pageHeight)` 升序（越靠页顶越先），再按 `LLX` 升序。
3. `yTol = max(1.0, config.YMergePt) * 1.6`。
4. 顺序遍历排序后的碎片，对每个碎片 `f`（矩形 `r`，`top = topDistanceFromPage(r, pageHeight)`）：
   a. 遍历现有的所有 `LineGroup`，找 `|top - g.topMean| <= yTol` 且
      `verticalOverlapRatio(r, g.yMin, g.yMax) >= 0.25` 的组中，`dy` 最小的一个作为 `best`。
      （**注意**：是遍历全部现有分组找最佳匹配，不是只看最后一个分组——因为排序主键是 top，
      但仍可能有微小抖动导致最佳匹配不是最近插入的分组。）
   b. 若无匹配组，新建一个 `LineGroup{topMean: top, yMin: r.LLY, yMax: r.URY}`，把碎片加入；
      否则把碎片加入 `best`，并调用 `best.update(top, r.LLY, r.URY)` 更新累计均值和 Y 范围。
5. 按 `topMean` 升序排序所有分组，返回各组的碎片列表（顺序即视觉行从上到下的顺序）。

**verticalOverlapRatio(r, gMinY, gMaxY)**：
```
inter = min(r.URY, gMaxY) - max(r.LLY, gMinY)
if inter <= 0: return 0.0
h = max(1e-6, r.URY - r.LLY)
return inter / h
```
即"碎片矩形与分组已知 Y 范围的交集高度 / 碎片自身高度"，衡量碎片有多大比例落在这个分组的纵向范围内。

**LineGroup.update(top, yMin, yMax)**：`topMean` 用增量平均更新（`(topMean*n + top) / (n+1)`），
`yMin`/`yMax` 分别取更小值/更大值扩展范围，`n++`。

## 算法：buildRawTextBlocks

对应 Java 第 1911-1946 行。

1. 提取整页全部文字碎片；过滤掉矩形为空的，以及落在任一表格区域内的（`isInsideAnyTable`，用于避免表格内文字被重复当正文提取）。
2. `groupFragmentsIntoLines` 分行。
3. `blockIndex = 0`；对每个视觉行：
   a. 按 `LLX` 升序排序该行碎片。
   b. `splitLineFragmentsByEmbeddedOrderedListMarker` 把该行按"行内再次出现的有序编号前缀"切成若干子行
      （防止"2....3...."这种一行内塞了多个列表项的情况被当成一条列表正文）。
   c. 对每个子行：`buildLineBlock` 组装成 `TextBlock`（`blockIndex++`）；
      若 `shouldDropAsHeaderFooterLine` 判定应剔除，跳过；
      否则用 `splitTextBlockByEmbeddedOrderedMarkers` 进一步按嵌入的有序列表标记拆分该块，
      把拆分结果全部加入 `lines`。
4. 返回 `lines`。

## 算法：splitLineFragmentsByEmbeddedOrderedListMarker

对应 Java 第 1965-2009 行。逐个碎片顺序累积拼接文本，在检测到"应在此处断行"的信号时把当前累积的碎片
切成一个子行、重新开始累积。

1. 维护 `current []TextFragment`、`assembled strings.Builder`（累积文本，用于判断规则）、`prevRect`、`prevText`。
2. 对每个碎片（矩形非空、trim 后文本非空）：
   a. 若 `current` 非空且 `shouldBreakBeforeEmbeddedOrderedListMarker(assembled.String(), part)` 为真：
      把 `current` 存入结果 `out`，重置 `current`/`assembled`/`prevRect`/`prevText`。
   b. 若 `current` 非空且 `shouldInsertSpaceByGeometry(prevText, part, prevRect, rect)`，往 `assembled` 追加空格。
   c. 追加 `part` 到 `assembled`，把碎片加入 `current`，更新 `prevRect`/`prevText`。
3. 循环结束把剩余 `current`（若非空）加入 `out`。
4. 若 `out` 为空（异常兜底），返回 `[原始 lineFragments 整体作为一个子行]`。

**shouldBreakBeforeEmbeddedOrderedListMarker(assembledLineText, nextPart)**：
- 若累积文本或下一片段为空，返回 false。
- 若累积文本（trim 后）不满足 `isListItem`（即当前还不是一个列表项），返回 false。
- 否则返回 `ORDERED_LIST_MARKER_PREFIX.matches(nextPart trim 后)`——即"当前已经是列表项，且下一个碎片本身
  单独看就像一个新的有序列表编号"，此时应该断行。

## 算法：buildLineBlock

对应 Java 第 2155-2266 行。把同一视觉行的碎片拼接成一个 `TextBlock`，同时统计样式特征。

1. 初始化累加器：`text`（拼接文本）、`fontSizeSum=0`、`fontWeightMax=400`、`fontFamily="unknown"`、
   `italic=false`、`mono=false`、`left=+Inf`、`right=-Inf`、`bottom=+Inf`、`topY=-Inf`、`top=+Inf`、
   `lineHeight=0`、`styleTokens []styleToken`、`textCursor=0`。
2. 遍历 `fragments`（**注意：这里没有先按 LLX 排序**——调用方 `buildRawTextBlocks` 在调用前已经排过序，
   `estimateBodyFontMode` 直接传入的 `lineFragments` 来自 `groupFragmentsIntoLines` 的分组结果，此处未再排序，
   移植时应确认调用点是否都已排好序，若不确定应在 `buildLineBlock` 入口自行按 LLX 排序以保证确定性）：
   对每个碎片：
   a. 矩形为空则跳过；文本 trim 后为空则跳过。
   b. **重复碎片抑制**：若存在上一个碎片，且当前文本与上一个碎片文本相同，且两个矩形的 `overlapRatio >= 0.5`，
      跳过这个碎片（同坐标重复渲染同文本，是某些 PDF 的已知问题，如"招招标标文文件件"）。
   c. 若 `text` 非空且 `shouldInsertSpaceByGeometry(prevText, part, prevRect, rect)`，追加空格并 `textCursor++`。
   d. 追加 `part` 到 `text`。
   e. 取该碎片的字号 `fs`、字体名 `partFontFamily`（有效字体名则用，否则 "unknown"）、
      字重 `partWeight`（Bold 标志位非 0 则 700 否则 400）。
   f. 记录 `styleTokens = append(styleTokens, {textCursor, textCursor+len(part), partFontFamily, partWeight, fs})`
      （注意：这里的 `start`/`end` 是按**字符数**还是**UTF-16 code unit**要看 Java `String.length()` 语义——
      Java `part.length()` 是 UTF-16 code unit 数；Go 移植若统一用 rune 或 byte 索引，必须保证前后端拼接文本用
      同一种单位索引 `styleTokens`，避免中文字符（非 BMP 或多字节）导致偏移错位。建议 Go 端统一用 rune 计数）。
   g. `textCursor += len(part)`；更新 `prevRect`/`prevText`。
   h. 累加 `fontSizeSum += fs`；`lineHeight = max(lineHeight, rect.Height())`；
      `left = min(left, rect.LLX)`；`right = max(right, rect.URX)`；
      `bottom = min(bottom, rect.LLY)`；`topY = max(topY, rect.URY)`；
      `top = min(top, topDistanceFromPage(rect, pageHeight))`。
   i. 若字体名有效：更新 `fontFamily`；若小写后的字体名包含 "courier"/"consolas"/"mono"，置 `mono=true`。
   j. 若该碎片 Bold 标志位非 0：`fontWeightMax = max(fontWeightMax, 700)`。若 Italic 标志位非 0：`italic=true`。
3. 若从未更新过 `left`（无有效碎片），置 `left=0`；若从未更新过 `top`，置 `top=0`。
4. `bbox`：若 `right >= left && topY >= bottom`，构造 `{left, bottom, right, topY}`；否则为 nil。
5. `avgFont = fragments为空 ? bodyFontMode : fontSizeSum / max(1, len(fragments))`
   （注意分母是碎片总数，**不是**跳过空白后的有效碎片数——若行内混有空白碎片会拉低均值，属于原始行为，
   移植时按原样保留，不要"修正"）。
6. `normalized = normalizeText(text, config)`。
7. `headingPrefixStyleMismatch = detectHeadingPrefixStyleMismatch(text原始未规范化文本, styleTokens)`。
8. 组装并返回 `TextBlock`：
   - `id = "TextBlock_{pageNo}_{index}"`
   - `bbox`, `topDistance=top`, `left`
   - `text=normalized`, `fontSizeMean=avgFont`, `fontFamily`, `fontWeight=fontWeightMax`, `italic`, `monoFont=mono`
   - `lineHeight = lineHeight>0 ? lineHeight : avgFont*1.2`
   - `indentLeft=left`（与 `left` 同值）
   - `tableId=-1`
   - `bodyFontMode`
   - `headingLastLineTop=NaN`, `headingTrailingLeft=NaN`, `headingTrailingText=""`（未合并状态）
   - `pageWidth`
   - `headingPrefixStyleMismatch`

## 算法：detectHeadingPrefixStyleMismatch（含 dominantStyle）

对应 Java 第 3467-3510 行。判断"一行文本里，编号前缀部分的字体/字重/字号"与"前缀之后正文部分"是否不一致
——不一致时说明整行的 `fontWeightMax`（取自 fragments 最大值）可能被前缀污染，导致误判为标题/排版边界。

1. 用 `HEADING_PREFIX_ONLY` 在原始（未规范化）文本上 `find`（不是 `matches`，只需要匹配开头一段）；
   找不到返回 false。
2. `prefixEnd = matcher.end()`；若 `prefixEnd <= 0` 或 `>= len(rawText)`（前缀为空或占满整行），返回 false。
3. `rest = rawText[prefixEnd:].trim()`；若为空返回 false。
4. `prefixSig = dominantStyle(tokens, 0, prefixEnd)`；`restSig = dominantStyle(tokens, prefixEnd, len(rawText))`；
   任一为 nil 返回 false。
5. 比较三项：字体族（先各自 `normalizePdfFontFamilyForCompare` 再比较）是否不同、字重是否跨越 600 阈值
   （`>=600` 视为粗体，两侧粗体性不同）、字号差是否 `>= 0.8`。
6. 三项中只要有 ≥1 项不同，返回 true。

**dominantStyle(tokens, start, end)**：在字符区间 `[start, end)` 内，按"每个 token 与该区间重叠的字符数"
加权，找出重叠字符数最多的样式签名（`fontFamily(归一化)|b/n|fontSize保留一位小数`），返回该签名对应的原始
`{fontFamily, fontWeight, fontSize}`。没有任何重叠 token 时返回 nil。

## 算法：splitTextBlockByEmbeddedOrderedMarkers / splitTailOrgDateIfNeeded

对应 Java 第 2011-2059 行。在整行拼接完、且已规范化的 `TextBlock` 上（区别于上面碎片级的
`splitLineFragmentsByEmbeddedOrderedListMarker`），进一步按行内出现的编号标记拆分。

**splitTextBlockByEmbeddedOrderedMarkers**：
1. 用 `EMBEDDED_ORDERED_LIST_MARKER` 在文本中查找全部匹配起始位置 `starts`。
2. 若匹配数 `< 2`（0 或 1 个编号标记，不构成"同一行塞了多个列表项"的情况），直接对整块调用
   `splitTailOrgDateIfNeeded` 并返回其结果（可能是 1 个块，也可能因为落款拆分而变成 3 个）。
3. 否则按 `starts` 切分：
   - 若第一个标记不在开头（`starts[0] > 0`），把 `[0, starts[0])` 作为独立的 "head" 块（若非空）。
   - 对每个标记区间 `[starts[i], starts[i+1] 或 文本末尾)`：trim 后若非空，构造子块，
     并对每个子块调用 `splitTailOrgDateIfNeeded`（结果可能进一步拆分）后加入输出列表。
4. 若最终输出为空（异常兜底），返回 `[原始块]`。

**splitTailOrgDateIfNeeded**：处理"列表项 + 落款机关名 + 日期"粘连在同一行的情况
（如 "3.……长江航务管理局：2024年5月1日"）：
1. 若不是列表项（`isListItem`），原样返回 `[block]`。
2. 用 `DATE_AT_LINE_END` 在文本里 `find` 行尾日期；找不到原样返回。
3. `date = 捕获组1`；`dateStart = 捕获组1起始位置`；`beforeDate = text[:dateStart].trim()`；
   若 `beforeDate` 或 `date` 为空，原样返回。
4. 用 `ORG_SUFFIX_AT_END` 在 `beforeDate` 里 `find` 行尾机构名；找不到原样返回。
5. `org = 捕获组1`；`orgStart = 捕获组1起始位置`；`listBody = beforeDate[:orgStart].trim()`；
   若 `listBody` 或 `org` 为空，原样返回。
6. 返回三个块：`[block.WithText(listBody), block.WithText(org), block.WithText(date)]`
   （拆成"列表正文"、"机关名"、"日期"三段独立的块，各自沿用原块的其余样式字段）。

## 算法：mergeLines（含 estimatePageRightEdge）

对应 Java 第 2268-2312 行、2294-2307 行。

**mergeLines(lines, bodyFontMode, config)**：
1. 若 `lines` 为空，原样返回。
2. `pageRightEdge = estimatePageRightEdge(lines)`。
3. 顺序链式合并：
   - `current = lines[i]`（累积块），`lastLine = current`（用于几何判定的"真实最后一条原始行"）。
   - 内层循环：只要 `i+1 < len(lines)`，取 `next = lines[i+1]`；若
     `shouldMerge(current, lastLine, next, bodyFontMode, config, pageRightEdge)` 为真，
     执行 `current = merge(current, next, config)`，`lastLine = next`，`i++`，继续内层循环；
     否则 `break` 内层循环。
   - 把 `current` 加入结果，`i++`，继续外层循环。
4. 返回结果列表。

**estimatePageRightEdge(lines)**：
1. 遍历所有非等宽（`!MonoFont`）、`Bbox != nil` 的行，取 `Bbox.URX` 的最大值 `max`；同时取遇到过的最大
   `PageWidth`。
2. 若 `max` 有效且 `pageWidth > 0` 且 `max < pageWidth * 0.6`（全页只有短行，最长的行也不到页宽 60%），
   返回 `NaN`（不可靠，调用方应回退到对称页边距估计）。
3. 否则返回 `max`（`NaN` 表示遍历后仍未找到任何有效行，等价于第 1 步 `max` 初值即 `NaN` 未被更新）。

## 算法：shouldMerge（行合并的核心否决式判定）

对应 Java 第 2407-2511 行（另有直接调用 Part 2 `isHeading` 的部分，仅记调用点不展开其内部实现）。
策略是"否决优先"（rejection-first）：任意一条否决规则命中就不合并；全部规则都不命中才合并。

签名：`shouldMerge(a, lastLine, b TextBlock, bodyFontMode float64, config Config, pageRightEdge float64) bool`
（单参数重载 `shouldMerge(a, b, bodyFontMode, config)` 等价于 `shouldMerge(a, a, b, bodyFontMode, config, NaN)`）。

步骤（严格按此顺序执行，因为部分规则的"否决"判断本身依赖前面步骤计算出的布尔量）：

1. `na = a.WithText(normalizeText(a.Text, config))`，`nb = b.WithText(normalizeText(b.Text, config))`。
2. 若 `na.Text` 或 `nb.Text` 为空，返回 false。
3. 若 `a.PageNo != b.PageNo`，返回 false（跨页由专门的 `mergeCrossPageParagraphBlocks` 处理，不在这里合并）。
4. `chapterTitleContinuation = isChapterPrefixWithTitleNamePair(na.Text, nb.Text)`（Part 2 范围函数，
   判断"第一章"+"招标公告"这类章节前缀单独一行、题名紧随其后的组合；此处只记调用签名和用途，
   不展开 `ChapterTocLineRemover` 内部实现）。
5. 若 `isCenteredStructuralChapterHeading(na)`（Part 2 范围）且不是 `chapterTitleContinuation`，返回 false。
6. 若不是 `chapterTitleContinuation` 且 `shouldBlockMergeAtChapterHeadingBoundary(na, nb, config)`
   （Part 2 范围，依赖 `ChapterTocLineRemover`），返回 false。
7. 若 `isOfficialDocumentTitleTail(na.Text)`（本 Part 内定义，见正则表）且
   `isAddresseeSalutationLine(nb.Text)`（本 Part 内定义），返回 false —— 公文标题之后的主送机关行永远独立成行。
8. `aList = isListItem(na.Text)`，`bList = isListItem(nb.Text)`，`listToContinuation = aList && !bList`。
9. 若 `ChapterReferenceHeuristics.isNumberedClauseContinuation(na.Text, nb.Text)`（Part 2 范围），返回 true（强制合并）。
10. 若 `!endsWithSentenceTerminator(na.Text)` 且 `ChapterReferenceHeuristics.isBodyChapterReference(nb.Text)`
    （Part 2 范围），返回 true。
11. 若 `isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)`（本 Part 内定义），返回 true。
12. 若 `!listToContinuation` 且 `isHeading(na, bodyFontMode, config, false, a)`（**Part 2 范围，仅记调用点**）
    且 `!isFullWidthHardWrapLeadLine(na, nb, config, pageRightEdge)`，返回 false
    （上一行本身被判定为标题，且不是"铺满行宽的硬断行首行"这种例外情况，则不合并）。
13. 若 `isHeading(nb, bodyFontMode, config, false, na)`（Part 2 范围）且
    `!ChapterReferenceHeuristics.isBodyChapterReference(nb.Text)`（Part 2 范围），返回 false
    （下一行是标题则不合并，除非它其实是正文里的条款引用）。
14. 若 `listToContinuation` 且 `startsWithCnArticleHeading(nb.Text)`（本 Part 内，委托
    `HeadingSuppressHeuristics.startsWithCnArticleHeading`，Part 2 范围的实现细节，仅记调用），返回 false。
15. 若 `(aList && bList) || (!aList && bList)`，返回 false（列表边界策略：list→list 不合并、
    普通→list 不合并；只有 list→普通续行允许合并）。
16. 若 `!listToContinuation` 且 `endsWithSentenceTerminator(na.Text)`，返回 false（上一行已句终，非列表续行场景不合并）。
17. 若 `!chapterTitleContinuation` 且 `!na.HeadingPrefixStyleMismatch` 且
    `hasTypographicBoundary(na, nb, config)`（即 `styleDifferent`，见下），返回 false。
18. 若 `isInlineTailContinuation(na, nb)`，返回 true（强制合并，短小 ASCII/数字尾巴）。
19. `paragraphContinuation = !endsWithSentenceTerminator(na.Text) && !isHeading(nb,...) && !isListItem(nb.Text)`
    （`isHeading` 调用同样是 Part 2 范围）。
20. `spacingMultiplier = paragraphContinuation ? max(config.LineSpacingMultiplier, 2.8) : config.LineSpacingMultiplier`。
21. 若 `layoutBreak(lastLine非nil?lastLine:na, nb, config, spacingMultiplier, paragraphContinuation, pageRightEdge)`
    为真，返回 false。
22. 若 `isAcrossTable(na, nb)`，返回 false。
23. 若 `shouldBlockLeftAlignedDateLineMerge(na, nb, config)`，返回 false。
24. 若 `HeadingSuppressHeuristics.isStandaloneNumericHierarchyLine(nb.Text)`（Part 2 范围）且
    `!isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)`，返回 false。
25. 若同上条件但检查的是 `na.Text`（`isStandaloneNumericHierarchyLine(na.Text)` 且非硬换行续行），返回 false。
26. 若 `isShortLabel(na.Text)` 且 `!endsWithSentenceTerminator(na.Text)` 且
    `!isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)`，返回 false。
27. 若 `endsWithSemanticBreak(na.Text)`，返回 false（"如下""包括"等语义断点后另起一段，通常后面接列表/表格）。
28. 若 `isForcedContinuation(na.Text, nb.Text)`，返回 true。
29. 全部否决规则都未命中：返回 true。

### shouldMerge 依赖的子判定

**hasTypographicBoundary(a, b, config) = styleDifferent(a, b, config)**：
```
if |a.FontSizeMean - b.FontSizeMean| > config.FontSizeDeltaPt: return true
if a.FontWeight != b.FontWeight: return true
return false   // 字体族名不同不算排版边界（同一视觉字体常被拆成多个内嵌资源，命名不同但渲染相同）
```

**isFullWidthHardWrapLeadLine(a, b, config, pageRightEdge)**：
```
if a==nil || b==nil: return false
if endsWithSentenceTerminator(a.Text) || endsWithSemanticBreak(a.Text): return false
if isCenteredStructuralChapterHeading(a): return false   // Part 2 范围，仅记调用
if styleDifferent(a, b, config): return false
return isFullWidthBodyLine(a, b, pageRightEdge)
```

**isFullWidthBodyLine(line, next, pageRightEdge)**：
```
if line==nil || line.Bbox==nil || line.PageWidth<=0: return false
tolerance = FULL_WIDTH_RIGHT_GAP_TOLERANCE_EM * max(line.FontSizeMean, 1.0)
if pageRightEdge 有效且 > 0:
    return line.Bbox.URX >= pageRightEdge - tolerance
rightGap = line.PageWidth - line.Bbox.URX
nextLeft = next!=nil ? next.Left : line.Left
leftMarginEstimate = max(min(line.Left, nextLeft), 0.0)
return rightGap <= leftMarginEstimate + tolerance
```
（优先用当页实测右边界；未知时用"右边距 ≈ 左边距对称估计"近似。）

**layoutBreak(lastLine, b, config, spacingMultiplier, paragraphContinuation, pageRightEdge)**：
```
indentChange = |lastLine.IndentLeft - b.IndentLeft|
spacing = |b.TopDistance - lastLine.TopDistance|
maxLine = max(lastLine.LineHeight, 1.0)
xOffset = |lastLine.Left - b.Left|
indentTolerance = config.IndentThresholdPt
xTolerance = config.XOffsetThresholdPt
if paragraphContinuation && lastLine.Bbox!=nil && lastLine.PageWidth>0:
    if isFullWidthBodyLine(lastLine, b, pageRightEdge):
        fontTolerance = PARAGRAPH_CONTINUATION_X_TOLERANCE_EM * max(lastLine.FontSizeMean, b.FontSizeMean, 1.0)
        indentTolerance = max(indentTolerance, fontTolerance)
        xTolerance = max(xTolerance, fontTolerance)
    else:
        return true   // 未铺满行宽却句子未完：字段行（"采购人：X"），当段落边界处理，直接判定布局断裂
return indentChange > indentTolerance || spacing > maxLine*spacingMultiplier || xOffset > xTolerance
```

**isAcrossTable(a, b)**：`a.TableID < 0 && b.TableID < 0 → false`；否则 `a.TableID != b.TableID`。

**isShortLabel(text)**：`len(trim(text) 的字符数) < 8`（注意用字符/rune 计数，不是字节数——中文场景下这个区分至关重要）。

**isNumericHierarchyHardWrapContinuation(left, right)**：
```
a, b = trim(left), trim(right)
if a或b为空: return false
if a 不匹配 `^\d+(?:\.\d+)*\.$`（即形如 "2.5." 的纯数字层级前缀+结尾点）: return false
if b 首字符是数字:
    glued = a + b
    return isStandaloneNumericHierarchyLine(glued)（Part 2）|| isHeadingByRegex(normalizeText(glued))
if len(b)<=18 且 b 不含 [，、；：,.!?;:] 任一标点:
    return isStandaloneNumericHierarchyLine(a+b)（Part 2）
return false
```

**shouldBlockLeftAlignedDateLineMerge(a, b, config)**：
```
if a==nil||b==nil||config==nil: return false
if !isStandaloneChineseDateLine(b.Text): return false
leftDelta = b.Left - a.Left
tol = config.XOffsetThresholdPt
if leftDelta < -tol: return false
return leftDelta <= tol*3.0
```

**endsWithSentenceTerminator(text)**：`text` trim 后取最后一个非空白字符，是否属于集合
`。.!！?？:：;；）)`。

**endsWithSemanticBreak(text)**：trim 后是否以 "如下"、"如下："、"包括"、"如下所示" 结尾（字面量精确匹配，非正则）。

**isForcedContinuation(a, b) = startsWithContinuationPunctuation(b) || shouldDropDuplicatedBoundary(a, b)**：
- `startsWithContinuationPunctuation(text)`：第一个非空白字符是否属于 `CONTINUATION_PUNCTUATION` 集合。
- `shouldDropDuplicatedBoundary(a, b)`：`a` 的最后一个非空白字符 == `b` 的第一个非空白字符，且
  `b` 的第二个非空白字符属于 `CONTINUATION_PUNCTUATION`（典型场景：硬换行时重复了边界字，如"之一"+"一，在"）。

**isInlineTailContinuation(a, b)**：
```
compact = 去掉 b.Text 中全部空白后的字符串
if !INLINE_TAIL_TOKEN.matches(compact): return false
spacing = |b.TopDistance - a.TopDistance|
maxLine = max(a.LineHeight, b.LineHeight)
return spacing <= maxLine * 2.5
```

## 算法：merge / mergeText

对应 Java 第 2333-2359 行、2389-2398 行。`merge(a, b, config)` 是链式合并累加器的核心：

```
mergedText = mergeText(a.Text, b.Text)
lastTop = max(headingLastTopRef(a), b.TopDistance)
trailNorm = normalizeText(b.Text, config)
return TextBlock{
    ID: a.ID, PageNo: a.PageNo, Bbox: a.Bbox,               // 沿用 a 的 id/page/bbox（bbox 不重新计算并集！）
    TopDistance: min(a.TopDistance, b.TopDistance),
    Left: min(a.Left, b.Left),
    Text: normalizeText(mergedText, config),
    FontSizeMean: (a.FontSizeMean + b.FontSizeMean) / 2.0,   // 简单平均，非加权
    FontFamily: a.FontFamily,                                 // 沿用 a 的字体，不比较/合并
    FontWeight: max(a.FontWeight, b.FontWeight),
    Italic: a.Italic || b.Italic,
    MonoFont: a.MonoFont || b.MonoFont,
    LineHeight: max(a.LineHeight, b.LineHeight),
    IndentLeft: min(a.IndentLeft, b.IndentLeft),
    TableID: a.TableID,
    BodyFontMode: a.BodyFontMode,
    HeadingLastLineTop: lastTop,
    HeadingTrailingLeft: b.Left,
    HeadingTrailingText: trailNorm,
    PageWidth: a.PageWidth,
    HeadingPrefixStyleMismatch: a.HeadingPrefixStyleMismatch || b.HeadingPrefixStyleMismatch,
}
```

**重要**：`Bbox` 直接沿用 `a.Bbox`，合并后不会扩展成两行的并集包围盒——这是有意为之还是遗留简化，
Java 源码没有注释说明；移植时按原样保留（不要"修正"为并集），因为后续 Part 2 的居中判断
（`headingCenterForMergePair`）专门用 `HeadingTrailingLeft`/`HeadingTrailingText` 来补偿这个问题
（当 `Bbox` 不能反映合并后的真实宽度时，改用最后一行的文本+左边距去估算视觉中心）。

**headingLastTopRef(t)**：`t==nil → 0.0`；否则 `IsNaN(t.HeadingLastLineTop) ? t.TopDistance : t.HeadingLastLineTop`
（未合并过的块用自身 `TopDistance`；已合并过的块用其记录的"最后一条原始行"的位置）。

**mergeText(a, b)**：
```
left, right = a或""，b或""
if shouldDropDuplicatedBoundary(left, right):
    right = right[1:]   // 去掉重复的边界字符（按 rune，非 byte）
return needSpace(left, right) ? left+" "+right : left+right
```

**needSpace(a, b)**：
```
left = a 的最后一个非空白字符；right = b 的第一个非空白字符
if left==0 || right==0: return false
if isAsciiLetterOrDigit(left) && isAsciiLetterOrDigit(right): return true
if isChinese(left) && isAsciiLetterOrDigit(right): return true
if isAsciiLetterOrDigit(left) && isChinese(right): return true
return false
```

## 算法：字符/几何小工具

**firstNonSpaceChar(s) / lastNonSpaceChar(s)**：分别从头/从尾扫描，返回第一个 `!unicode.IsSpace(c)` 的字符；
找不到返回 `0`（Go 用 `rune(0)` 表示"无"，与 Java `char 0` 语义一致）。

**secondNonSpaceChar(s)**：扫描全部非空白字符，返回第 2 个；不足 2 个返回 `0`。

**isChinese(c)**：`c in [0x4E00,0x9FFF] || c in [0x3400,0x4DBF]`（仅覆盖基本汉字+扩展A，未覆盖更大的
Unicode 扩展区，按原样移植不要扩展范围）。

**isAsciiLetterOrDigit(c)**：`a-z`、`A-Z`、`0-9`。

**overlapRatio(a, b)**：
```
i = a.Intersect(b)
if i==nil || i.IsEmpty(): return 0.0
inter = i.Width * i.Height
area = max(1.0, a.Width * a.Height)
return inter / area
```
即"交集面积 / a 的面积"（**不对称**，以 `a` 为分母基准，调用方需注意参数顺序——`isInsideAnyTable` 传入
`(碎片矩形, 表格矩形)`，衡量的是"碎片有多大比例落入表格"；`isHeaderOrFooter` 传入 `(行矩形, 已知重复区域矩形)`，
含义同理）。docmill 的 `geom.Box` 若无内置 `Intersect`，需自行实现：
`interLLX=max(a.L,b.L), interLLY=max(a.B,b.B)`（注意 BOTTOMLEFT 语义下"下边"是较小的 Y）……即标准矩形求交，
交集非法（宽或高 <=0）时返回"空"。

**topDistanceFromPage(rect, pageHeight)**：`pageHeight - rect.URY`（BOTTOMLEFT 坐标系下，`URY` 是矩形的
顶边 Y 坐标，用页高减去它得到"距页面顶部的距离"，Y 轴越靠上此值越小）。

**shouldInsertSpaceByGeometry(leftText, rightText, leftRect, rightRect)**：
```
if !needSpace(leftText, rightText): return false
if leftRect==nil || rightRect==nil: return true
gap = rightRect.LLX - leftRect.URX
if gap <= 0.8: return false
left = lastNonSpaceChar(leftText); right = firstNonSpaceChar(rightText)
if isDigit(left) && isDigit(right) && gap < 3.0: return false
if isLetter(left) && isLetter(right) && gap < 2.0: return false
return true
```
（`isDigit`/`isLetter` 用 Unicode 通用分类，等价 Java `Character.isDigit`/`Character.isLetter`，Go 用
`unicode.IsDigit`/`unicode.IsLetter`。）

**isInsideAnyTable(rect, tables, config)**：遍历 `tables`，若与任一 `table.Bbox` 的 `overlapRatio >= config.TableOverlapRatio`，返回 true；全部不满足返回 false。

## 算法：normalizeText / normalizeTableCellText（含子步骤）

对应 Java 第 3625-3689 行 + 3691-3786 行辅助函数。文本规范化管线，**顺序敏感**，移植时必须保持完全相同的执行顺序。

**normalizeText(text, config)**（用于正文/标题）：
1. `trim`。
2. 去零宽字符（`ZERO_WIDTH` 替换为空）。
3. `removeCharacterDoubling`（去字符级倍增，见下）。
4. 去 CJK 字符间空白（`CJK_SPACE` 替换为空）。
5. `mergeBrokenEnglishWords`（修复被拆散的短英文单词，见下）。
6. `mergeSingleDigitRuns`（合并被拆散的多位数字，见下）。
7. 去"数字-空白-CJK"边界空白（`DIGIT_TO_CJK_SPACE` 替换为空）。
8. 去"CJK-空白-数字"边界空白（`CJK_TO_DIGIT_SPACE` 替换为空）。
9. CJK 与 ASCII 字母边界插入空格（`CJK_TO_ASCII` 替换为单空格）。
10. ASCII 字母与 CJK 边界插入空格（`ASCII_TO_CJK` 替换为单空格）。
11. 数字与字母边界插入空格（`NUM_UNIT` 替换为单空格）。
12. 压缩连续空白为单空格并 `trim`（`MULTI_SPACE`）。

**normalizeTableCellText(text, config)**（表格单元格用，**没有** 第 9/10 步的 CJK↔ASCII 边界插空格）：
1. `trim`。
2. 去零宽字符。
3. `removeCharacterDoubling`。
4. 去 CJK 字符间空白。
5. `mergeBrokenEnglishWords`。
6. `mergeSingleDigitRuns`。
7. 去"数字-空白-CJK"边界空白。
8. 去"CJK-空白-数字"边界空白。
9. 数字与字母边界插入空格（`NUM_UNIT`）。
10. 压缩连续空白为单空格并 `trim`。

**removeCharacterDoubling(s)**：
```
if len(s) < 4: return s
统计 "位置 i, i+1 步长2" 上 s[i]==s[i+1] 的对数 pairCount（即只看偶数下标起始的相邻对，i=0,2,4,...）
if pairCount < 2: return s   // 避免把"等等"这种合法叠字破坏掉
从头扫描：若 s[i]==s[i+1]（任意 i，非仅偶数步长），输出一个字符并跳过下一个；否则原样输出该字符
```
注意：**检测**阶段只看偶数步长的对（`i += 2`），但**替换**阶段是任意相邻位置都检测去重（`i++` 逐位扫描，
检测到相邻相等就吃掉一个）。这是原始 Java 实现的一个不对称之处，移植时按原样复刻，不要"统一"成一种扫描方式。

**mergeBrokenEnglishWords(text, config)**：按空白切分 token；相邻两个 token 若都是纯字母（`ALPHA_TOKEN`）
且长度都 `<=2` 且都不在 `config.ShortStopwords`（小写比较）里，则合并成一个 token（跳过 2 个）；否则原样保留、
移动 1 个 token。用空格重新拼接返回。

**mergeSingleDigitRuns(text)**：按空白切分 token；用 `SINGLE_DIGIT_FRAGMENT`（`^(\D*)(\d)(\D*)$`，
即恰好一个数字字符夹在非数字前后缀之间）解析每个 token 为 `{prefix, digit, suffix}`；解析失败的 token 原样保留。
解析成功后，只要当前 fragment 的 `suffix` 为空，就继续尝试合并紧随其后的、`prefix` 也为空的下一个数字 fragment
（把它们的 `digit` 拼接起来），直到遇到 `suffix` 非空的 fragment 为止或无法继续解析。若最终拼出的数字串长度
`>= 2`，输出 `prefix + digits + suffix` 作为一个合并 token；否则该 token 原样保留（不构成"被拆散的多位数字"）。

## 算法：跨页表格合并（mergeCrossPageTables / appendTableRows / firstRowTextsEqual）

对应 Java 第 342-358 行、500-545 行。

**mergeCrossPageTables(sorted)**：顺序遍历已排序的 `elements`，维护输出列表 `out`：
```
for element in sorted:
    prev = out 的最后一个元素（若 out 为空则 nil）
    if prev 是 TableBlock t1 且 element 是 TableBlock t2
       且 t2.PageNo == t1.PageNo + 1
       且 t1.ColCount == t2.ColCount
       且 |t1.Bbox.LLX - t2.Bbox.LLX| <= CROSS_PAGE_TABLE_X_TOLERANCE_PT
       且 |t1.Bbox.URX - t2.Bbox.URX| <= CROSS_PAGE_TABLE_X_TOLERANCE_PT:
        out 的最后一个元素替换为 appendTableRows(t1, t2)   // 原地替换，不是追加新元素
    else:
        out = append(out, element)
return out
```
因为是逐元素替换 `out` 的最后一项，天然支持链式合并 3 页及以上（合并后的结果继续作为下一次比较的 `t1`）。

**appendTableRows(t1, t2)**：
```
skipRows = firstRowTextsEqual(t1, t2) ? 1 : 0
cells = copy(t1.Cells)
for c in t2.Cells:
    if c.Row < skipRows: continue   // 跳过重复表头行
    cells = append(cells, {Row: c.Row - skipRows + t1.RowCount, Col: c.Col, RowSpan: c.RowSpan, ColSpan: c.ColSpan, Text: c.Text})
return TableBlock{
    ID: t1.ID, PageNo: t1.PageNo, Bbox: t1.Bbox, TopDistance: t1.TopDistance, Left: t1.Left,
    RowCount: t1.RowCount + t2.RowCount - skipRows,
    ColCount: t1.ColCount,
    Cells: cells,
}
```
（沿用 `t1` 的 id/page/bbox/位置，`Bbox` 同样不重新计算并集——与 `merge(TextBlock)` 的行为一致。）

**firstRowTextsEqual(t1, t2)**：分别收集 `t1`/`t2` 中 `Row==0` 的单元格，按 `Col` 建立 `map[int]string`
（文本 trim 后）。若两个 map 的 key 集合或值不完全相等，返回 false。若相等但全部值都是空字符串
（`hasContent==false`），也返回 false——即"两边首行都是空白单元格"不算重复表头，避免误跳过真实数据行。

## 算法：装饰性单格表还原（demoteDecorativeSingleCellTables 及辅助）

对应 Java 第 381-450 行。把"用矩形边框圈住的正文/小标签"这类被表格识别误判为 1×1 表格的元素，
在满足条件时还原为普通文本块。

**isSingleCellTableCandidate(element)**：`element` 是 `TableBlock` 且 `RowCount==1 && ColCount==1 && Bbox!=nil`。

**demoteDecorativeSingleCellTables(sorted)**：
```
out = copy(sorted)
i = 0
while i < len(out):
    if !isSingleCellTableCandidate(out[i]):
        i++; continue
    end = i+1
    while end < len(out) && isSingleCellTableCandidate(out[end]):
        end++
    convertRunIfDecorative(out, i, end)   // 原地可能修改 out[i:end]
    i = end
return out
```
即把连续的 1×1 候选表聚成一个 run 整体判定（流程图里连续多个方框节点常见），而非逐个孤立判断。

**convertRunIfDecorative(out, start, end)**：
1. 若 `start-1 < 0` 或 `end >= len(out)`（run 前后没有元素），直接返回，不转换。
2. 若 `out[start-1]` 不是非等宽 `TextBlock`（即前一个元素不是正文文本块，或是等宽字体），返回不转换。
3. 若 `out[end]` 不是非等宽 `TextBlock`，返回不转换。
   （——真表格通常前后至少一侧也是表格，或紧邻标题，不会被夹在两段正文之间；这是识别"这是装饰框 run"
   而非"这是真表格"的核心几何/上下文信号。）
4. `lineHeight = max(before.LineHeight, after.LineHeight)`；若 `<=0`，返回不转换。
5. `maxHeight = lineHeight * DECORATIVE_SINGLE_CELL_MAX_LINES`（2.5）。
6. 先做一次只读检查：遍历 `[start, end)` 每个表的 `Bbox` 高度（`URY - LLY`），只要有一个 `> maxHeight`，
   直接返回、整个 run 都不转换（要求 run 内**全部**表格都矮，不是部分转换）。
7. 检查通过后，再遍历一次 `[start, end)`，把每个 `TableBlock` 用 `tableToTextBlock(table, before)` 转换，
   若转换结果非 nil，原地替换 `out[k] = converted`。

**tableToTextBlock(table, styleSource)**：
```
text = table.Cells 为空 ? "" : table.Cells[0].Text
if text 为空或全空白: return nil
return TextBlock{
    ID: table.ID, PageNo: table.PageNo, Bbox: table.Bbox, TopDistance: table.TopDistance, Left: table.Left,
    Text: text,
    FontSizeMean: styleSource.FontSizeMean, FontFamily: styleSource.FontFamily,
    FontWeight: styleSource.FontWeight, Italic: styleSource.Italic,
    MonoFont: false,
    LineHeight: styleSource.LineHeight,
    IndentLeft: table.Left,
    TableID: -1,
    BodyFontMode: styleSource.BodyFontMode,
    HeadingLastLineTop: NaN, HeadingTrailingLeft: NaN, HeadingTrailingText: "",
    PageWidth: styleSource.PageWidth,
    HeadingPrefixStyleMismatch: false,
}
```
（样式字段全部沿用 run 前一个正文块 `styleSource`，只有文本/几何位置取自表格本身，
这样后续标题/列表判定会把它当正文对待。）

## 算法：跨页段落合并（mergeCrossPageParagraphBlocks / shouldMergeCrossPageParagraph）

对应 Java 第 461-498 行。

**mergeCrossPageParagraphBlocks(sorted, config)**：与 `mergeCrossPageTables` 同样的"原地替换最后一项"模式：
```
for element in sorted:
    prev = out 的最后一个元素
    if prev 是 TextBlock t1 且 element 是 TextBlock t2 且 shouldMergeCrossPageParagraph(t1, t2, config):
        out 的最后一项替换为 merge(t1, t2, config)
    else:
        out = append(out, element)
```

**shouldMergeCrossPageParagraph(t1, t2, config)**：
1. `t2.PageNo != t1.PageNo + 1` → false（只处理相邻两页）。
2. `t1.MonoFont || t2.MonoFont` → false（代码块不参与）。
3. `t1.TableID >= 0 || t2.TableID >= 0` → false（表格转换来的文本块不参与）。
4. `left = normalizeText(t1.Text, config)`，`right = normalizeText(t2.Text, config)`；任一为空 → false。
5. `leftTail = (t1.HeadingTrailingText 为空或空白) ? left : t1.HeadingTrailingText`
   （若 `t1` 是链式合并后的累积块，用它记录的"最后一行文本"判断句子是否说完，而不是用拼接后的整块文本——
   拼接文本的末尾就是最后一行，这里其实和直接看 `left` 的尾部等价，但显式使用 `HeadingTrailingText`
   是为了在某些字段未同步更新时也保证语义正确，按原样移植）。
6. 若 `endsWithSentenceTerminator(leftTail) || endsWithSemanticBreak(leftTail)` → false（上一页末句已完）。
7. 若 `isHeadingByRegex(right) || isListItem(right)` → false（下一页首行本身是标题/列表，不是续行）。
8. 若 `isHeading(t1.WithText(left), t1.BodyFontMode, config, false, nil)`（Part 2 范围，仅记调用）→ false。
9. 若 `isStandaloneChineseDateLine(right) || isStandaloneChineseDateLine(left)` → false（纯日期行不参与）。
10. 若 `!t1.HeadingPrefixStyleMismatch && styleDifferent(t1, t2, config)` → false
    （**注意**：当 `t1.HeadingPrefixStyleMismatch` 为真时，跳过这条样式差异检查，直接放行——因为 `t1`
    的整体字重是被编号前缀污染的粗体，与次页常规字重续行比较会产生虚假边界）。
11. 全部未命中，返回 true（允许合并）。

## 算法：estimateBodyFontMode

对应 Java 第 3870-3907 行。统计当页正文的"众数字号"，用于后续标题/合并判定的基准。

1. 提取整页全部有效矩形的文字碎片；`groupFragmentsIntoLines` 分行。
2. 对每一行：用 `buildLineBlock(lineFragments, pageNo, pageHeight, pageWidth, 12.0, 0, config)` 构建一个
   **临时** `TextBlock`（`bodyFontMode` 参数写死传 `12.0`，`index` 写死传 `0`——这个临时块只是为了复用
   `buildLineBlock` 内部的拼接逻辑，从而调用 `shouldDropAsHeaderFooterLine` 需要的整行文本/矩形，
   并不代表真实的字号统计对象）。
3. 若 `shouldDropAsHeaderFooterLine(lineFragments, line, pageNo, pageHeight, config, headerFooterFilter)`
   判定应剔除，跳过该行（页眉页脚行不参与正文字号统计）。
4. 否则，对该行每个碎片：`bucket = round(fragment.FontSize * 2)`（0.5pt 精度分桶），
   `hist[bucket]++`。
5. 遍历直方图，取计数最大的桶 `bestBucket`（初值 24，对应默认 12pt；若直方图非空且有更高计数的桶则更新）；
   若出现计数相同的多个桶，保留**先遍历到的**那个（Java `HashMap`/`LinkedHashMap` 遍历顺序 = 插入顺序，
   Go 移植若用 `map[int]int` 遍历顺序不确定，为保证结果确定性应改用"先按 bucket 排序再遍历"或用有序结构
   记录插入顺序，避免并列时结果随机跳动）。
6. 返回 `bestBucket / 2.0`。

## Config 字段清单（与本 Part 相关部分）

Go 端 `Config` struct 建议镜像 Java 字段名（转 CamelCase）与默认值（`Config.defaults()`，第 4495-4525 行）：

| 字段 | 类型 | 默认值 |
|---|---|---|
| YMergePt | float64 | 3.0 |
| FontSizeDeltaPt | float64 | 0.5 |
| IndentThresholdPt | float64 | 8.0 |
| XOffsetThresholdPt | float64 | 10.0 |
| LineSpacingMultiplier | float64 | 2.4 |
| TableOverlapRatio | float64 | 0.15 |
| HeaderTopRatio | float64 | 0.12 |
| FooterBottomRatio | float64 | 0.88 |
| HeaderPageNumberRatio | float64 | 0.12 |
| FooterPageNumberRatio | float64 | 0.88 |
| EmitTraceComments | bool | false |
| EmitHeadingTrace | bool | false |
| FallbackMergeMarkdown | bool | true |
| MergeWrappedHeadings | bool | true |
| RemoveToc | bool | true |
| RemovePageNumbers | bool | true |
| MaxHeadingLength | int | 80 |
| HeadingMergeFontDeltaPt | float64 | 1.2 |
| HeadingMergeCenterTolerancePt | float64 | 24.0 |
| HeadingMergeMaxGapMultiplier | float64 | 2.2 |
| StyleClusterHeadingEnabled | bool | true |
| ShortPhraseNumberedRunMin | int | 3 |
| ShortPhraseNumberedRunMaxGap | int | 3 |
| ShortPhraseNumberedRunMaxBodyLines | int | 1 |
| ShortPhraseNumberedRunSeqQualityMin | float64 | 0.8 |
| ShortPhraseNumberedBodyMaxLen | int | 18 |
| ShortStopwords | map[string]struct{} | `EN_SHORT_STOPWORDS`（见上表） |

后 5 项（`ShortPhrase*`）是 Part 2（短语式列表识别）专用配置，本 Part 不使用，仅为完整起见列出。

## 与其他 Part 的接口

本 Part 的最终产出是 `elements []GeometricElement`（`convertDocument` 步骤 6 之后的状态）：
- 已按 `(PageNo, TopDistance, Left)` 排好全局阅读顺序；
- 每个元素是 `*TextBlock` 或 `*TableBlock`；
- `TextBlock` 已完成同页内的行合并（`mergeLines`）与跨页段落合并（`mergeCrossPageParagraphBlocks`），
  即同一段落被硬换行/跨页打散的多行已重新拼接为一个块；
- `TableBlock` 已完成跨页表格合并（`mergeCrossPageTables`），且原本被误判为表格的装饰性方框已按
  `demoteDecorativeSingleCellTables` 还原为 `TextBlock`；
- 每个 `TextBlock` 携带完整样式统计（`FontSizeMean`/`FontWeight`/`FontFamily`/`Italic`/`MonoFont`/
  `IndentLeft`/`LineHeight`/`BodyFontMode`/`HeadingPrefixStyleMismatch`）以及供 Part 2 做"多行合并标题"
  居中判断用的 `HeadingLastLineTop`/`HeadingTrailingLeft`/`HeadingTrailingText`/`PageWidth` 字段；
- 每个 `TableBlock` 携带 `RowCount`/`ColCount`/`Cells`（含 `RowSpan`/`ColSpan`）与可能非空的 `SingleCellLines`。

Part 2（标题识别/样式聚类/渲染）据此：
1. 过滤出非等宽 `TextBlock` 得到 `orderedTextBlocks`，在其上跑 `ListGuideHeuristics`、
   `buildHeadingStyleProfile`（样式聚类）、`ShortPhraseListRunHeuristics`、
   `HeadingSequenceConsistencyHeuristics`、`HeadingPatternQualityHeuristics` 等，产出一组
   "该按标题处理/该降级为正文/该按列表处理"的块 ID 集合。
2. 对排序后的 `elements` 做最终渲染遍历（`appendTextAsMarkdown`/表格渲染），渲染阶段还会再调用本 Part
   定义的 `isHeadingByRegex`/`isListItem`/`normalizeText` 等函数——Part 2 不应重新实现这些函数，
   应直接复用本 Part 产出的同一份实现（Go 端建议放在同一个内部包，如 `internal/pdfconv/geometry.go`，
   Part 2 的标题/渲染逻辑放在同包的其它文件里导入使用，而不是各自维护一份正则/规范化逻辑）。
3. `isHeading` 函数本身（Part 2 定义）会读取本 Part 产出的 `TextBlock` 字段做判断，但其判断结果又反过来
   影响本 Part 内 `shouldMerge`/`shouldMergeCrossPageParagraph` 的行为——这是一个**双向依赖**：
   `shouldMerge` 在合并阶段就需要调用 `isHeading` 来判定"上一行/下一行是否已经是标题，不应被吞并"。
   这意味着 Go 移植时 `isHeading` 与本 Part 的合并逻辑必须放在同一个包内互相调用（不能把 Part 1/Part 2
   拆成有严格单向依赖的两个 Go 包），或者 Part 2 需要先实现一个不依赖样式聚类结果的"轻量版 isHeading"
   提前给 Part 1 使用——具体切分方式建议由实现者在写代码时根据 Java 原始调用图（`isHeading` 的完整签名
   `isHeading(TextBlock, bodyFontMode, config, inListGuideScope, prevBlock, headingStyleProfile,
   shortPhraseListRunBlockIds, headingSequenceDemoteBlockIds)`——注意 `shouldMerge` 内调用的是**四参数**
   重载 `isHeading(view, bodyFontMode, config, inListGuideScope, prevBlock)`，即不依赖样式聚类/短语列表/
   序列一致性的"基础版"）决定，此处只指出这个耦合点，不代为决策包结构。
