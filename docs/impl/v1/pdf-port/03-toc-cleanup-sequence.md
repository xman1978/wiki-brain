# PDF 移植 Part 3：目录剔除、同前缀连坐判定与最终清理

来源文件：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/PdfToMarkdown.java`（5215 行）及其直接依赖的独立小工具类（均在同包 `com.fileview.convert.markdown` 下）。

## 覆盖范围

PdfToMarkdown.java 内直接归属本 Part 的函数（按文件出现顺序）：

1. `PATTERN_DEMOTE_INCLUDED_KEYS`（常量，L299）
2. `detectDisqualifiedPatternDemoteBlockIds`（L306）
3. `detectDisqualifiedPatternDemoteBlockIdsForTest`（L330，纯转发，@visibleForTesting）
4. `detectHeadingSequenceConsistencyDemoteBlockIds`（L547，PdfToMarkdown 内的薄封装；核心算法在独立类 `HeadingSequenceConsistencyHeuristics` 中，见下）
5. `TOC_CHAPTER_PAGED_ENTRY`（常量，L1049 —— **未被任何函数引用，死代码**，见「潜在遗漏」）
6. `removeTocFromMarkdown`（L1055）
7. `isChapterTocOrphanPageOnlyBlock`（L1092）
8. `stripChapterTocLinesFromBlock`（L1103）
9. `isEntireBlockTocEntries`（L1119）
10. `isTocPagedLine`（L1131）
11. `isChapterTocPagedEntry`（L1140）
12. `BlockKind`（枚举，L1145）
13. `BlockParts`（内部类，L1147）
14. `splitMarkdownBlocks`（L1157）
15. `splitTracePrefix`（L1163）
16. `classifyBlock`（L1179）
17. `shouldMergeMarkdownPlainBlocks`（L1191）
18. `plainLineRejectedForListContinuation`（L1222）
19. `shouldMergeListWithPlainContinuation`（L1233）
20. `hasEmbeddedOrderedListMarker`（L1255）
21. `fallbackMergeMarkdown`（L975，逻辑上在上面之前，依赖 13-19）
22. `isPageNumberBlock` + `PAGE_NUMBER_BLOCK`（L128, L4152）
23. `cleanOutput` / `cleanOutputForTest`（L4225 / L4221）
24. `isMarkdownTableRowLine`（L4240）
25. `splitConcatenatedOrderedListLines`（L4246）
26. `splitSingleLineByOrderedMarkersAndTail`（L4256）
27. `splitStructuralHeadingSegments`（L4303）
28. `splitTailOrgDateInListLine`（L4307）
29. `splitListBodyAndOrgTail`（L4324）
30. `SplitPair`（内部类，L4346）
31. `mergeWrappedListContinuationLines`（L4356）
32. `isLikelyListContinuationLine`（L4384）
33. `isOrderedMarkerInsideNumericHierarchyPrefix`（L3157，被 27 使用；被 Part2 的 `isNumericHierarchyHardWrapContinuation` 也调用 —— 共享，见「潜在遗漏」）

依赖的独立工具类（`PdfToMarkdown` 之外的 `.java` 文件，本文档只精确规格化被上述函数**实际调用到**的方法；这些类整体上是跨 Part/跨转换路径（Word/MPP）共用的，详见文末重叠说明）：

- `HeadingSequenceConsistencyHeuristics`：`detectPdfBlocksToDemote`、`parseSequenceLine`（及其 `PATTERN_DEFS`/中文数字解析/罗马数字解析）、`isSequentialIndex`、`findMixedSequenceBodyLineIds`、`continuesSegment`、`shouldDemoteMixedSegment`、`colonLabelSiblingsDominateSegment`、`isParallelEnumerationSibling`
- `HeadingPatternQualityHeuristics`：`detectDisqualifiedPatternKeys`、`clearlyFailsHeadingQuality`、`isColonTerminatedSectionFieldLabel`、`overlongPrefixTitleOnlyFailure`、以及其内部字符统计小函数
- `HeadingLevelPrefixHeuristics`：`classifyPrefixKey`（及 `PREFIX_DEFS` 表、`normalizeForHeadingPrefixMatch`）
- `ChapterReferenceHeuristics`：`isBodyChapterReference`
- `ChapterTocLineRemover`：`stripFromMarkdown`、`isChapterTocOnlyBlock`、`isChapterTocLine`、`isChapterTocOrphanPageSuffixLine`、`isStructuralChapterHeading`（其余方法为 Word/MPP 路径专用，未被 PdfToMarkdown 调用，不在本 Part 范围）
- `HeadingSuppressHeuristics`：`isStandaloneHeadingLine`、`stripHashes`、`looksLikeCnArticleBodyParagraphLead`、`looksLikeCnArticleBodySentence`、`startsWithCnArticleHeading`、`isStandaloneNumericHierarchyLine`
- `MarkdownStructureRules`：`isChapterTableOfContentsEntry`、`hasEmbeddedPipeTable`、`splitEmbeddedPipeTableLines`、`isOrderedListItemLine`
- `InlinePipeTableSplit`：`splitLineIfNeeded`、`splitMarkdownLines`、`findInlinePipeTableStart`

不在本 Part 范围（明确排除，供人工核对）：`ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine`（被 `isParallelEnumerationSibling` 调用，但其定义/权威归属为 Part 2 的样式聚类家族）；`ListGuideHeuristics`；`HeadingStyleProfile`/`StyleClusterRole`；所有几何/表格抽取/`appendTextAsMarkdown`/`isHeading` 家族函数。

## 常量与正则

| 名称 | 值/模式（原样复制） | 用途 |
|---|---|---|
| `PATTERN_DEMOTE_INCLUDED_KEYS` | `Set.of("TITLE_CHAPTER_FIVE")` | 全文级同前缀连坐仅对「第 X 条」前缀生效 |
| `TOC_CHAPTER_PAGED_ENTRY` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*章.*(?:\.{1,}\|…{1,}\|·{1,}\|⋯{1,}\|\s{2,}\|\t).*\d{1,4}\s*$` | **死代码**：仅定义未被引用（见下）。移植时保留常量文本供比对，但不接线到任何调用点，除非人工确认应恢复使用 |
| `PAGE_NUMBER_BLOCK` | `^\s*(?:第\s*\d{1,5}\s*(?:页)?(?:\s*[-/\|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?\|(?:(?:共)?\s*\d{1,5}\s*(?:页)?)(?:\s*[-/\|]\s*(?:共)?\s*\d{1,5}\s*(?:页)?)?\|[—]\s*\d{1,5}\s*[—])\s*$` | 页码块识别（`isPageNumberBlock`） |
| `CONTINUATION_PUNCTUATION`（依赖，Part2 定义） | `"，。；：、！？,.!?;:）)]】》"` | `startsWithContinuationPunctuation`/`shouldDropDuplicatedBoundary`（fallbackMergeMarkdown/shouldMergeListWithPlainContinuation 用到，值在 Part1/2 范围定义，此处仅引用） |
| `EMBEDDED_ORDERED_LIST_MARKER`（依赖） | `(?<!\d)(?:\d+[\.、)）\]](?!\d)\|[（(]\s*\d+\s*[)）])` | `hasEmbeddedOrderedListMarker`、`splitSingleLineByOrderedMarkersAndTail`、`isOrderedMarkerInsideNumericHierarchyPrefix` |
| `ORDERED_LIST_MARKER_PREFIX`（依赖） | `^(?:\d+[\.、)）\]](?!\d)\|[（(]\s*\d+\s*[)）]).*` | `plainLineRejectedForListContinuation` |
| `LIST_BULLET`（依赖） | `^[-+*•●○■□►→★☆]\s*.*` | `plainLineRejectedForListContinuation` |
| `DATE_AT_LINE_END`（依赖） | `(\d{4}年\d{1,2}月\d{1,2}日)\s*$` | `splitTailOrgDateInListLine` |
| `ATTACHMENT_ITEM_KEYWORDS`（依赖） | `{"统计表", "示意图", "现状照片", "照片", "附件"}` | `splitListBodyAndOrgTail`（数组，按声明顺序 `lastIndexOf` 遍历，取第一个命中且能两侧非空切分的关键词） |
| `ORG_SUFFIX_AT_END`（依赖） | `([一-龥]{4,}(?:分局\|人民政府\|委员会\|支队\|大队\|局))\s*$` | `splitListBodyAndOrgTail` 兜底（关键词未命中时） |
| `INLINE_TABLE_DELIM`（InlinePipeTableSplit） | `\|\s*\|\s*:?-{2,}` | 内嵌管道表定界识别 |
| `CHAPTER_TOC_LINE`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*(?:\.{1,}\|…{1,}\|·{1,}\|⋯{1,}\|\s{2,}\|\t).*\d{1,4}\s*$` | `isChapterTocLine` 分支 1 |
| `CHAPTER_TOC_DASH_PAGE`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}.*-\d+-\s*$` | `isChapterTocLine` 分支 2 |
| `CHAPTER_TOC_DASH_PAGE_TRUNCATED`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}-\s*$` | `isChapterTocLine` 分支 3（硬断行截断） |
| `CHAPTER_TOC_ORPHAN_DASH_PAGE`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?\d+-\s*$` | `isChapterTocOrphanPageSuffixLine` |
| `CHAPTER_TOC_EMBEDDED_PAGE`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}\d{1,4}\p{IsIdeographic}.*` | `isChapterTocLine` 分支 4 |
| `CHAPTER_HEADING`（ChapterTocLineRemover） | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*` | `isStructuralChapterHeading` |
| `TITLE_CHAPTER`（PdfToMarkdown，依赖，Part1/2 已定义） | `^第\s*([一二三四五六七八九十百千万零廿卅]+\|\d+)\s*(章\|节\|纲\|目\|条).*` | `normalizeCnStructuralHeadingLevel`（Part2）间接影响，本 Part 不直接用，仅供交叉核对 `TITLE_CHAPTER_FIVE`（=「条」）与 `TITLE_CHAPTER_ONE`（=「章」）的语义来源 |

### HeadingLevelPrefixHeuristics.PREFIX_DEFS（`classifyPrefixKey` 用，按声明顺序，第一个 `matches()` 命中即返回该 key）

| key | 正则 |
|---|---|
| `TITLE_CHAPTER_ONE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*章.*` |
| `TITLE_CHAPTER_TOW` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*节.*` |
| `TITLE_CHAPTER_THREE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*纲.*` |
| `TITLE_CHAPTER_FOUR` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*目.*` |
| `TITLE_CHAPTER_FIVE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*条.*` |
| `TITLE_CN_PAREN` | `^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）].*` |
| `TITLE_CN_NUM` | `^([一二三四五六七八九十百千万]+)[、．.\s].*` |
| `TITLE_NUM_FIVE` | `^(\d+(?:\.\d+){4})(?:[.．])?(?![.．\d-])\s*.*` |
| `TITLE_NUM_FOUR` | `^(\d+(?:\.\d+){3})(?:[.．])?(?![.．\d-])\s*.*` |
| `TITLE_NUM_THREE` | `^(\d+(?:\.\d+){2})(?:[.．])?(?![.．\d-])\s*.*` |
| `TITLE_NUM_TOW` | `^(\d+(?:\.\d+){1})(?:[.．])?(?![.．\d-])\s*.*` |
| `TITLE_NUM_DOT` | `^(\d+)[.．](?!\d\|-)\s*.*` |
| `TITLE_NUM_DUNHAO` | `^(\d+)、\s*.*` |
| `TITLE_NUM_SUFFIX` | `^(\d+)[)）]\s*.*` |
| `TITLE_NUM_PAREN` | `^[（(]\s*(\d+)\s*[)）]\s*.*` |
| `TITLE_ROMAN` | `^([IVXLCDM]+)\.\s*.*`（大小写不敏感） |
| `TITLE_ALPHA` | `^([A-Za-z])[.．]\s*.*` |

`classifyPrefixKey(title)`：先对 `title` 调用 `normalizeForHeadingPrefixMatch`（内部依赖 `MarkdownPipelineLineUtils.normalizeLine` + 剥离行首 ★ 等优先级标记，仅当剥离后能匹配上表某个「层级标题前缀」——即 key 不属于 `{TITLE_NUM_DOT,TITLE_NUM_DUNHAO,TITLE_NUM_SUFFIX,TITLE_NUM_PAREN,TITLE_ROMAN,TITLE_ALPHA,TITLE_NUM_TOW,TITLE_NUM_THREE,TITLE_NUM_FOUR,TITLE_NUM_FIVE}` 这些"列举编号"类 key——才采用剥离结果，否则用原文），再按上表顺序匹配。`title` 为 `null`/blank 时返回 `null`。**这一函数本身在 Part2 的标题判定链路中也被调用，属共享依赖，此处只需按上表精确复刻，不必重新设计。**

### HeadingSequenceConsistencyHeuristics.PATTERN_DEFS（`parseSequenceLine` 用，独立于上表，返回 `(patternKey, index[])`）

| key | 正则 | 索引解析 |
|---|---|---|
| `TITLE_NUM_FIVE` | `^(\d+(?:\.\d+){4})\.?\s*.*` | 按 `.` 拆分成整数数组 |
| `TITLE_NUM_FOUR` | `^(\d+(?:\.\d+){3})\.?\s*.*` | 同上 |
| `TITLE_NUM_THREE` | `^(\d+(?:\.\d+){2})\.?\s*.*` | 同上 |
| `TITLE_NUM_TOW` | `^(\d+(?:\.\d+){1})\.?\s*.*` | 同上 |
| `TITLE_CN_PAREN` | `^[（(]\s*([一二三四五六七八九十百千万]+)\s*[)）].*` | 中文数字转整数（见下），解析失败（返回 null）则本行不算命中该模式，继续尝试后续 PatternDef |
| `TITLE_CN_NUM` | `^([一二三四五六七八九十百千万]+)[、．.\s].*` | 同上 |
| `TITLE_NUM_DOT` | `^(\d+)\.\s*.*` | 单整数 |
| `TITLE_NUM_DUNHAO` | `^(\d+)、\s*.*` | 单整数 |
| `TITLE_NUM_SUFFIX` | `^(\d+)[)）]\s*.*` | 单整数 |
| `TITLE_NUM_PAREN` | `^[（(]\s*(\d+)\s*[)）]\s*.*` | 单整数 |
| `TITLE_ROMAN` | `^([IVXLCDM]+)\.\s*.*`（不敏感） | 罗马数字转整数（标准减法规则，从右向左扫描），解析失败返回 null |
| `TITLE_ALPHA` | `^([A-Za-z])[.．]\s*.*` | `toUpperCase(ch) - 'A' + 1` |

按声明顺序尝试，第一个 `matches()` 命中且索引解析成功（非 null 非空数组）即返回；否则该行不是序列行（`parseSequenceLine` 返回 `null`）。

中文数字解析算法（`parseChineseNumber`，供 Go 移植参考）：
1. 先用 `[^一二三四五六七八九十百千万零]` 过滤只保留有效字符；为空返回 null。
2. 逐字符扫描：数字字符（零一二三四五六七八九）赋给 `number`；单位字符按 `十=10,百=100,千=1000,万=10000`；非法字符（不是数字也不是单位）返回 null。
3. 遇到 `万`：`section = (section + max(number,1)) * 10000; total += section; section = 0; number = 0`。
4. 遇到 `十/百/千`：`section += max(number,1) * unit; number = 0`。
5. 结束后 `result = total + section + number`；`result <= 0` 返回 null，否则返回 `result`。

## 数据结构

```go
// BlockKind 对应 Java 的 private enum BlockKind
type blockKind int

const (
	blockPlain blockKind = iota
	blockHeading
	blockList
	blockTable
	blockCode
	blockOther
)

// blockParts 对应 Java 的 private static final class BlockParts
// prefix: 保留的 TRACE 注释行（可能为空字符串）；body: 去掉 TRACE 前缀后的正文（已 trim）
type blockParts struct {
	prefix string
	body   string
}

// splitPair 对应 Java 的 private static final class SplitPair（splitListBodyAndOrgTail 用）
type splitPair struct {
	left  string
	right string
}
```

`BlockKind`/`BlockParts` 在 Java 中均为 `PdfToMarkdown` 私有内部类型，仅在 `fallbackMergeMarkdown`/`removeTocFromMarkdown`（本 Part）范围内使用；Go 移植可放在同一内部包的 unexported 类型，无需导出。

## 算法：`detectDisqualifiedPatternDemoteBlockIds`

「同前缀连坐」——仅对 `TITLE_CHAPTER_FIVE`（「第 X 条」）前缀，若同一前缀下**全文任一行**明显不合格，则该前缀在全文一律降级为正文（不作为层级标题渲染）。

1. 入参 `blocks []TextBlock`, `config Config`；`blocks` 为空则返回空 set。
2. 逐块构建归一化文本数组 `lines`：对每个 block，`norm = normalizeText(block.text, config)`；若 `ChapterReferenceHeuristics.isBodyChapterReference(norm)` 为真，则该行的 `lines[i]` 置为空字符串 `""`（即：正文中的章节引用不参与判定，也不会被后续误伤）。
3. 调用 `HeadingPatternQualityHeuristics.detectDisqualifiedPatternKeys(lines)` 得到全文范围内「明显不合格」的前缀 key 集合 `badKeys`；`badKeys.retainAll(PATTERN_DEMOTE_INCLUDED_KEYS)`（即只保留 `TITLE_CHAPTER_FIVE`）。
4. 若 `badKeys` 为空，直接返回空 set。
5. 否则遍历 `lines`，对每个非空行调用 `HeadingLevelPrefixHeuristics.classifyPrefixKey(lines[i])` 得到 `pk`；若 `pk != null && badKeys.contains(pk)`，把 `blocks[i].id` 加入结果集。
6. 返回结果集（block id 集合）。

`detectDisqualifiedPatternKeys(lines)`（HeadingPatternQualityHeuristics）子算法：

1. 加载 `maxLen = loadMaxHeadingLength()`（读取 `pdf2md.maxHeadingLength` 配置项，默认 `80`，读取失败或非法值同样回退默认；结果不低于 `8`）。
2. 对每一行 `i`：
   a. `norm = normalizeForScan(lines[i])`：`strip()` → 若以 `#` 开头去掉 1~6 个 `#` 及紧随空白 → 若整体被 `**...**` 包裹（长度 ≥4）去掉两端 `**` → 全角 NBSP(` `) 替换为普通空格 → 再 `strip()`。空则跳过本行。
   b. `pk = HeadingLevelPrefixHeuristics.classifyPrefixKey(norm)`；`pk == null` 跳过。
   c. `!PdfToMarkdown.isHeadingByRegex(norm)` 跳过（正则族见 Part1/2，本 Part 只引用）。
   d. `isColonTerminatedSectionFieldLabel(norm)` 为真则跳过（不计入合格性判定，见下）。
   e. `overlongPrefixTitleOnlyFailure(norm, maxLen)` 为真则跳过（仅因超长/非独立成行导致的失败，不连坐）。
   f. 否则若 `clearlyFailsHeadingQuality(norm, maxLen)` 为真，标记 `hasFailure[pk] = true`。
3. 返回 `hasFailure` 的 key 集合。

`isColonTerminatedSectionFieldLabel(norm)`：

1. `t = stripHashes(norm)`（去 `#` 前缀）；若不以 `：`或`:` 结尾，返回 false。
2. `pk = classifyPrefixKey(t)`；为 null 返回 false。
3. `pk` 属于 `{TITLE_NUM_TOW, TITLE_NUM_THREE, TITLE_NUM_FOUR, TITLE_NUM_FIVE, TITLE_NUM_DOT, TITLE_NUM_DUNHAO, TITLE_NUM_SUFFIX, TITLE_NUM_PAREN}` 之一则返回 true，否则 false。

`overlongPrefixTitleOnlyFailure(norm, maxLen)`：

1. 若 `!clearlyFailsHeadingQuality(norm, maxLen)`，返回 false（本身就合格，谈不上"仅因超长失败"）。
2. `t = stripHashes(norm).strip()`。
3. 依次检查——只要命中其一即返回 **false**（说明失败原因不止是超长，仍应连坐）：`t` 以 `：`/`:` 结尾；`CN_PAREN_COLON_GUIDE` 匹配（`^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）][^：:]{0,40}[：:].*`）；`hasMiddleChinesePeriod(t)`（句中出现「。」且不在末尾）；`isBodyLikeHeadingSentence(t)`；`HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead(t)`；`HeadingSuppressHeuristics.looksLikeCnArticleBodySentence(t)`。
4. 否则返回 `classifyPrefixKey(t) != null`（即：纯粹因长度/标点密度超标，且仍能识别出前缀模式）。

`clearlyFailsHeadingQuality(text, maxHeadingLength)`：

1. `t = stripHashes(text).strip()`；空返回 false。
2. `ChapterTocLineRemover.isChapterTocLine(t)` 为真 → false（目录行不参与此判定，由 TOC 剔除单独处理）。
3. `MarkdownStructureRules.isChapterTableOfContentsEntry(t)` 为真 → false（同上，委托同一实现）。
4. `HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead(t)` → true。
5. `HeadingSuppressHeuristics.looksLikeCnArticleBodySentence(t)` → true。
6. `t` 以 `：`或`:` 结尾 → true。
7. `CN_PAREN_COLON_GUIDE.matches(t)` → true。
8. `hasMiddleChinesePeriod(t)`（`。` 出现在非末尾位置）→ true。
9. `countNonSpaceChars(t) > 80`（`HARD_MAX_NON_SPACE_LEN`）→ true。
10. `len(t) > maxHeadingLength && countSentenceEnds(t) >= 1` → true（`SENTENCE_BOUNDARY_PUNCT = [。；！？!?]`，计数命中次数）。
11. `len(t) > maxHeadingLength * 2` → true。
12. `len(t) > maxHeadingLength && countClausePunct(t) >= 4` → true（`CLAUSE_DENSE_PUNCT = [，、,;；]`）。
13. `isBodyLikeHeadingSentence(t)` → true（见下）。
14. `t` 以 `及` 结尾且 `countNonSpaceChars(t) >= 40`（`INCOMPLETE_TAIL_MIN_LEN`）→ true。
15. `!HeadingSuppressHeuristics.isStandaloneHeadingLine(t) && classifyPrefixKey(t) != null && countNonSpaceChars(t) > maxHeadingLength` → true。
16. 否则 false。

`isBodyLikeHeadingSentence(text)`：
1. `nonSpaceLen = countNonSpaceChars(text)`；`> 80` 直接 true。
2. `< 35`（`PREFIX_BODY_LIKE_MIN_LEN`）返回 false。
3. `punct = countSentencePunctuation(text)`（统计 `，。；：、,.!?;:` 出现次数）；`< 2`（`PREFIX_BODY_LIKE_MIN_PUNCT`）返回 false。
4. `density = punct / max(1, nonSpaceLen)`；返回 `density >= 0.015`（`PREFIX_BODY_LIKE_MIN_PUNCT_DENSITY`）。

辅助计数函数：`countNonSpaceChars`（非空白字符数）、`countSentenceEnds`/`countClausePunct`（正则 `find()` 计数循环）、`countSentencePunctuation`（遍历字符判定是否在给定标点集合内）——均为直译型逐字符/正则循环，无特殊算法。

字符串长度计算注意：Java `String.length()` 按 UTF-16 code unit 计数；中文字符属于 BMP，单字符=1 code unit，与 Go 用 `[]rune` 计数结果一致（本文档所有长度阈值按"字符数"理解即可，不涉及非 BMP 字符如 emoji，PDF 正文一般不含）。

## 算法：`detectHeadingSequenceConsistencyDemoteBlockIds`

PdfToMarkdown 内的薄封装：

1. `orderedTextBlocks` 为空返回空 set。
2. 调用 `HeadingSequenceConsistencyHeuristics.detectPdfBlocksToDemote(orderedTextBlocks, matcherFn)`，其中 `matcherFn(idx, block)` 调用 `recognizesBlockAsHeadingForSequenceConsistency(block, config, profile, shortPhraseListRunBlockIds)`（**该判定函数本身属于 Part2 范围**——依赖 `HeadingStyleProfile`/`StyleClusterRole`，逻辑为：`parseSequenceLine(normalizeText(block.text)) == null` 则不算标题候选；若 `profile.isReliable()` 且 `profile.roleOf(block)` 属于 H1/H2 直接判真；否则回退 `block.fontWeight >= 600 && block.fontSizeMean > block.bodyFontMode + 0.5`）。
3. Part3 拿到的最终结果是 `Set<String>`（block id），与 `detectDisqualifiedPatternDemoteBlockIds` 的结果在 `convertDocument` 主流程中 `addAll` 合并成一个 `headingSequenceDemoteBlockIds`（见「执行顺序」），两者语义等价、平级合并，渲染循环只认这一个合并后的 set，不区分来源。

`HeadingSequenceConsistencyHeuristics.detectPdfBlocksToDemote(orderedBlocks, isRecognizedAsHeading)` 核心算法：

1. 遍历 `orderedBlocks`，跳过 `null`/`monoFont` 块；对其余块 `norm = normalizeText(block.text, Config.defaults())`（**注意**：这里固定用 `Config.defaults()`，不是调用方传入的 `config`——移植时须保留这个细节，不能误用外部 config）。
2. `parsed = parseSequenceLine(norm)`；为 `null` 跳过（不构成序列条目）。
3. 命中则构造 `SequenceEntry{lineId: i, patternKey: parsed.patternKey, index: parsed.index, recognizedAsHeading: isRecognizedAsHeading(i, block)}`，加入 `entries` 列表（`i` 是 `orderedBlocks` 中的下标，不是过滤后的连续序号）。
4. 调用 `findMixedSequenceBodyLineIds(entries, lines=null)` 得到应降级的下标集合 `lineIds`（`lines` 传 `null`，意味着 PDF 路径没有全局 `lines` 数组，`isParallelEnumerationSibling`/`colonLabelSiblingsDominateSegment` 在 `lines==null` 时的行为需特别注意——见下）。
5. 把下标转回 `orderedBlocks[idx].id` 集合返回。

`findMixedSequenceBodyLineIds(entries, lines)`：

1. `entries.size() < 2`（`MIN_SEGMENT_SIZE`）直接返回空集。
2. 按 `lineId` 升序排序（PDF 路径本就按阅读顺序追加，等价于已排序，但仍需排序以防万一）。
3. 用双指针把 `entries` 切成"连续段"：`i=0`；内层 `j` 从 `i+1` 起，只要 `continuesSegment(entries[j-1], entries[j])` 为真就 `j++`；`entries[i..j)` 成为一段。
4. `continuesSegment(prev, next)`：`patternKey` 相等 **且** `isSequentialIndex(prev.index, next.index)`（多级编号除最后一级外前缀必须逐位相等，末位 `next == prev + 1`；维度不同/为空返回 false）**且** `next.lineId - prev.lineId < 20`（`MAX_LINE_GAP`）。
5. 对每一段调用 `shouldDemoteMixedSegment(seg, lines)`：为真则把段内所有 `lineId` 加入结果集。
6. `shouldDemoteMixedSegment(seg, lines)`：
   a. `seg.size() < 2` → false。
   b. 统计段内 `recognizedAsHeading` 为真/假的条目是否都存在（`anyHeading`, `anyNonHeading`）；若只有单一状态（全真或全假）→ false（没有"部分识别部分未识别"的混排，不需要连坐）。
   c. 对每个 `recognizedAsHeading==false` 的条目，若 `isParallelEnumerationSibling(lines, e.lineId)` 为真，**立即**返回 true（并列列举同伴信号足以触发整段降级）。
   d. 若步骤 c 未触发，返回 `colonLabelSiblingsDominateSegment(seg, lines)`。
7. `isParallelEnumerationSibling(lines, lineId)`：**`lines == null`（PDF 路径固定如此）时直接返回 true**——即 PDF 路径下，只要段内存在识别状态不一致（步骤 b 通过），就必然在步骤 c 于第一个未识别条目上返回 true，`colonLabelSiblingsDominateSegment` 分支在 PDF 路径下永远不会被触发（因为 `lines==null` 时它也直接 `return false`，但由于 c 已经短路返回，实际不影响结果——只是这条路径在 PDF 场景下是死代码，移植时如实保留即可，无需简化，因为同一份代码要为 Word/MPP 路径（`lines != null`）服务）。
8. 结论（PDF 路径专用简化版，供实现校验用，**不要用来替代上面通用算法**，仅用于单元测试自查）：一段内若 pattern/序号连续、行距 <20，且段内既有被判定为标题的条目又有未被判定为标题的条目，则整段全部降级为正文。

`isSequentialIndex(ia, ib)`：长度不等或为空返回 false；除最后一维外必须逐位相等；最后一维必须 `ib[last] == ia[last] + 1`。

## 算法：`removeTocFromMarkdown`

剔除「目录 / CONTENTS / 图目录 / 表目录」标题块及其后续目录条目整段。

1. 空/blank 输入返回空字符串。统一换行符为 `\n`。
2. `blocks = splitMarkdownBlocks(normalized)`（按空行分块，见下）。
3. 维护布尔状态 `inToc = false`，逐块处理：
   a. 空白块：`!inToc` 时原样保留（含空块本身，保留段落间隔），`inToc` 时跳过不输出。
   b. `parts = splitTracePrefix(block)`；`body = parts.body.trim()`；`visible = body` 去掉开头的 `#+\s*` 前缀（若有）再 trim。
   c. 若 `visible` 整体匹配 `^(目\s*录|CONTENTS|图目录|表目录)$`：置 `inToc = true`，本块整体跳过（不输出），continue 下一块。
   d. 若 `inToc == true`：若 `isEntireBlockTocEntries(body)` 为真，跳过本块（仍处于目录区）；否则 `inToc = false`（目录区结束，本块继续走下面的正常处理，不 continue）。
   e. 若 `ChapterTocLineRemover.isChapterTocOnlyBlock(body)` 为真：整块跳过。
   f. 若 `isChapterTocOrphanPageOnlyBlock(body)` 为真：整块跳过。
   g. 否则 `body = stripChapterTocLinesFromBlock(body)`（逐行剔除目录行，保留正文行）；若结果为空白，跳过本块；否则重新拼接 `prefix`（若有）+ `\n` + `body`，加入输出列表。
4. 最终 `String.join("\n\n", out).trim()`，再交给 `ChapterTocLineRemover.stripFromMarkdown(...)` 做一次全局逐行扫描（补充剔除步骤 3 未处理到的裸目录行——见该函数说明），返回结果。

`isChapterTocOrphanPageOnlyBlock(body)`：取块内**第一条非空行**（`trim()` 后去掉 `#+\s*` 前缀），若该行匹配 `ChapterTocLineRemover.isChapterTocOrphanPageSuffixLine`（即整行仅为 `\d+-` 形式，如 `1-`），返回 true；否则（含空块）返回 false。**只看第一条非空行，不遍历全部行**。

`stripChapterTocLinesFromBlock(body)`：按行遍历（`split("\n", -1)` 保留空行），对每行 `v = trim` 后去 `#+\s*` 再 trim；若 `v` 非空且（`isChapterTocPagedEntry(v)` 或 `isChapterTocOrphanPageSuffixLine(v)`）为真，剔除该行（不加入 `kept`）；否则保留原始行（含原始前导空白，未 trim）。处理完毕后去掉 `kept` 首尾的空白行，`join("\n")` 再 `trim()`。

`isEntireBlockTocEntries(body)`：遍历每行，跳过空行；只要有一行不满足 `isTocPagedLine(v)` 立即返回 false；统计非空行数 `n`；返回 `n > 0`（全部非空行都是目录条目，且至少一行）。

`isTocPagedLine(visible)`：
1. `vLine = visible.trim()`。
2. `endsWithPageNo = vLine 匹配 .*\d+$`。
3. `hasLeaderDots = vLine 匹配 .*(\.{2,}|…{2,}|·{2,}|⋯{2,}).*\d+$`。
4. `hasAlignedSpaces = vLine 匹配 .*(\t|\s{2,}).*\d+$`。
5. 返回 `endsWithPageNo && (hasLeaderDots || hasAlignedSpaces || isChapterTocPagedEntry(vLine))`。

`isChapterTocPagedEntry(visible)` = `ChapterTocLineRemover.isChapterTocLine(visible.trim())`。

### `ChapterTocLineRemover.isChapterTocLine(line)`（4 分支或，任一命中即 true）

`t = line.strip()`；空返回 false；依次匹配 `CHAPTER_TOC_LINE`、`CHAPTER_TOC_DASH_PAGE`、`CHAPTER_TOC_DASH_PAGE_TRUNCATED`（均 `.matches()` 全串匹配）、`CHAPTER_TOC_EMBEDDED_PAGE`；任一 `.matches()` 为真即返回 true。四个正则见「常量与正则」表。

### `ChapterTocLineRemover.isChapterTocOrphanPageSuffixLine(line)`

`t = line.strip()`；非空且匹配 `CHAPTER_TOC_ORPHAN_DASH_PAGE`（`^(?:#{1,6}\s*)?\d+-\s*$`）即返回 true。

### `ChapterTocLineRemover.isChapterTocOnlyBlock(body)`

逐行（`split("\n")`，不保留空行占位）`strip()`，跳过空行；只要有一行 `!isChapterTocLine(t)` 立即返回 false；统计命中行数 `n`；返回 `n >= 1`（`MIN_CHAPTER_TOC_LINES_IN_BLOCK`）。

### `ChapterTocLineRemover.isStructuralChapterHeading(line)`

`t = line.strip()` 去掉开头 `#+\s*` 再 `trim()`；非空 且 匹配 `CHAPTER_HEADING`（`^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*`） 且 `!isChapterTocLine(t)`（正文章标题必须先排除目录行）。

### `ChapterTocLineRemover.stripFromMarkdown(markdown)`

统一换行后按行切分（`split("\n", -1)` 保留末尾空行），调用 `stripLines(lines)`，`join("\n")` 返回。

`stripLines(lines)` = `stripLinesInternal(stripBareTocChapterRuns(lines))`——**注意执行顺序：先跑"裸目录锚点+连续章条目"清除，再跑逐行目录识别清除**。

`stripBareTocChapterRuns(lines)`（处理 OCR 场景下丢失引导点/页码、仅剩「目录」锚点 + 连续裸「第 X 章」标题行的情形；PDF 抽取一般不会命中此路径，但需完整移植以保持行为一致）：

1. 逐行扫描，识别"锚点行"：整行匹配 `TOC_MARKER_LINE`（`^(?:#{1,6}\s*)?(目\s*录|CONTENTS|目\s*次|图目录|表目录)\s*$`，忽略大小写）；或匹配 `TRAILING_TOC_TABLE_CELL`（`^(.*\|)\s*(目\s*录|CONTENTS|目\s*次)\s*\|\s*$`，表格末列吞并锚点的情形，此时记录替换值 `anchorReplacement = 捕获组1 + "  |"`，即把该列内容清空为两个空格但保留表格列结构）。
2. 非锚点行原样保留，`i++`。
3. 锚点行：从下一行开始向后扫描连续的"目录内容行"——`TOC_CONTENT_LINE`（整行由一个或多个 `TOC_CHAPTER_ENTRY` 拼接而成，即多条目可能被 OCR/几何聚类合并进同一行）。空行跳过继续扫描（不打断）。命中内容行时用 `TOC_CHAPTER_NUMERAL` 提取本行内出现的所有「第 X 章」章号；若某章号此前已在本次扫描的 `seenChapterNumerals` 中出现过，说明已越过目录区进入正文重复标题，本行**不计入**目录区，扫描在此中止（`break`，不消费本行）；否则把新章号计入 `seenChapterNumerals`，`chapterHits` 累加本行命中的章号数，`consumeUntil` 前移到本行之后。遇到不匹配 `TOC_CONTENT_LINE` 的非空行也中止扫描。
4. 扫描结束后：若 `chapterHits >= 2`（`MIN_BARE_TOC_CHAPTER_RUN`），判定为裸目录区——若有 `anchorReplacement` 则输出该替换行（保留表格列结构），锚点行本身（非表格情形）不输出；`consumeUntil` 之前扫描到的所有内容行全部丢弃；游标跳到 `consumeUntil` 继续外层循环。
5. 若 `chapterHits < 2`，判定不是目录区：锚点行原样保留输出，游标只前进 1（不消费后续已扫描的行，下一轮外层循环会重新逐行处理它们）。

`stripLinesInternal(lines)`：

1. 维护 `inFence`（代码围栏状态，见到 ` ``` ` 开头行翻转，原样保留围栏行本身，围栏内所有行原样保留不做任何目录判定/清洗）。
2. 非围栏内，若当前行 `isChapterTocLine(trimmed)` 为真：跳过该行本身，继续向后扫描——只要后续行是空行、`isChapterTocLine` 命中或 `isChapterTocOrphanPageSuffixLine` 命中，就持续跳过（空行也跳过但不中止扫描）；遇到围栏起始行或都不满足的行则停止扫描，游标跳到该处继续外层循环（该行不属于本轮跳过范围，留给外层重新判定）。
3. 非围栏内，若当前行本身单独命中 `isChapterTocOrphanPageSuffixLine`（未被前一步的向后扫描消费到，即前面不是目录行开头的孤立页码残留），单独跳过该行。
4. 其余非围栏行：调用 `normalizeGluedStructuralChapterHeading(line)` 规范化后保留输出（**这不是删除操作，是对粘连「第X章题名」正文行插入空格规范化**——`^(#{0,6}\s*)(第\s*[一二三八...]+\s*章)([\p{IsIdeographic}（）()·\s]{2,60})$` 形式且题名不含数字、且整行不含目录页码后缀时，在「章」与题名之间插入一个空格；否则原样返回）。
5. 围栏内行原样保留。

## 数据结构与算法：`splitMarkdownBlocks` / `splitTracePrefix` / `classifyBlock`

`splitMarkdownBlocks(markdown)`：
1. `cleaned = markdown.replaceAll("\n{3,}", "\n\n").trim()`（三个以上连续换行先压缩为两个）。
2. 为空返回空列表。
3. 按 `\n\n+`（一个或多个连续"空行分隔"）切分，返回列表（**注意**：这里的分隔正则是 `\n\n+`，即两个及以上连续换行都算一个分隔符，切出来的每个 block 内部不应再含有连续空行——因为上一步已经把 3+ 压缩成 2）。

`splitTracePrefix(block)`：
1. `trimmed = block.trim()`；为空返回 `{prefix:"", body:""}`。
2. 按 `\n` 切成行数组。
3. 从头开始，只要当前行 `trim()` 后以 `<!-- TRACE` 开头，就纳入 `prefix`（多行用 `\n` 连接，每行都 `trim()` 后存入），`idx++`。
4. 剩余行（`idx` 之后）用 `\n` 拼接、`trim()`，作为 `body`。
5. 返回 `{prefix, body}`。

`classifyBlock(body)`：
1. `body == null` → `OTHER`。
2. `t = body.stripLeading()`（**只去左侧空白，不 trim 右侧**）；为空 → `OTHER`。
3. `t.startsWith("```")` → `CODE`。
4. `t.startsWith("#")` → `HEADING`。
5. `t.startsWith("|")` 或 `MarkdownStructureRules.hasEmbeddedPipeTable(t)` → `TABLE`。
6. `isListItem(t)`（Part2 定义，本 Part 仅引用）→ `LIST`。
7. `t.startsWith(">")` → `OTHER`。
8. 否则 → `PLAIN`。

**判定顺序即优先级顺序，不可调换**（例如同时是代码块围栏和标题符号开头的边界情形按代码块优先）。

## 算法：`fallbackMergeMarkdown`

兜底断句合并：把渲染阶段因坐标合并遗漏而拆成"一行一段"的纯正文段落，在最终 Markdown 文本层面再做一次保守合并；仅合并 `PLAIN`/`LIST` 类型的相邻块，标题/表格/代码块严格跳过。

1. 输入为空/blank 返回空字符串。统一换行符为 `\n`。
2. `lines = normalized.split("\n", -1)`；调用 `MarkdownStructureRules.splitEmbeddedPipeTableLines(lines)` 先把行内嵌管道表拆开（Part2/共享逻辑，见 `InlinePipeTableSplit`）；重新 `join("\n")`。
3. `blocks = splitMarkdownBlocks(normalized)`；`out = []`；`i = 0`。
4. 主循环，`while i < blocks.size()`：
   a. `block = blocks[i]`；若 blank，`i++`，continue（跳过空块，不计入输出——因为最终用 `"\n\n".join(out)` 重建段落间隔）。
   b. `cur = splitTracePrefix(block)`；`kind = classifyBlock(cur.body)`。
   c. **`kind == LIST` 分支**：
      - `mergedPrefix = cur.prefix`；`mergedBody = cur.body.trim()`。
      - 内层循环：只要 `i+1 < blocks.size()`：取 `nextBlock = blocks[i+1]`；若 blank，`i++` 并 `continue`（跳过空块继续找下一个非空块，**不中止**内层循环）；否则 `next = splitTracePrefix(nextBlock)`，`nextKind = classifyBlock(next.body)`；若 `nextKind != PLAIN`，`break`（列表只吸收紧随其后的 PLAIN 块，不吸收另一个 LIST/HEADING/TABLE/CODE）；若 `!shouldMergeListWithPlainContinuation(mergedBody, next.body)`，`break`；否则 `mergedBody = normalizeText(mergeText(mergedBody, next.body.trim()), config)`（`mergeText` 是 Part1/2 定义的智能拼接函数，本 Part 仅调用），`i++`。
      - 合并结果 `merged = (mergedPrefix空? "" : mergedPrefix+"\n") + mergedBody`；`out.add(merged.trim())`；`i++`；`continue` 回主循环。
   d. **`kind != PLAIN` 且非 LIST**（即 HEADING/TABLE/CODE/OTHER）：`out.add(block.trim())` 原样保留，`i++`，continue。
   e. **`kind == PLAIN` 分支**（与 c 结构对称，但合并条件函数不同）：
      - `mergedPrefix = cur.prefix`；`mergedBody = cur.body.trim()`。
      - 内层循环：同样跳过空块（`i++; continue`，不中止）；`nextKind != PLAIN` 则 `break`；`!shouldMergeMarkdownPlainBlocks(mergedBody, next.body, config)` 则 `break`；否则合并、`i++`。
      - 结果 `out.add(merged.trim())`；`i++`。
5. 返回 `String.join("\n\n", out).trim() + "\n"`（**注意末尾强制补一个换行符**，与 `cleanOutput` 最终的 `trim()` 不同）。

## 算法：`shouldMergeMarkdownPlainBlocks(aBody, bBody, config)`

判断两个连续 PLAIN 段落是否应合并为一段：

1. `a = aBody.trim()`, `b = bBody.trim()`；任一为空返回 false。
2. 若 `ChapterTocLineRemover.isStructuralChapterHeading(a) && HeadingSuppressHeuristics.isStandaloneHeadingLine(a)`（`a` 本身是独立成行的正文章节标题）→ 返回 false（章节标题不与下一段合并）。
3. `endsWithSentenceTerminator(a)`（`a` 末尾非空白字符属于 `。.!！?？:：;；）)`）→ 返回 false（已完整收束）。
4. `isStandaloneChineseDateLine(b)`（`b` 整行是 `\d{4}年\d{1,2}月\d{1,2}日` 形式的落款日期）→ 返回 false。
5. `isShortLabel(a)`（`a.trim().length() < 8`）且 `!endsWithSentenceTerminator(a)` → 返回 false（短标签行，如字段名，不与下一段强行拼接，即便未终止）。
6. `endsWithSemanticBreak(a)`（`a` 以 `如下`/`如下：`/`包括`/`如下所示` 结尾）→ 返回 false（引导语后面通常接列表/表格，不应并入同一段正文）。
7. `startsWithContinuationPunctuation(b)`（`b` 首个非空白字符属于 `CONTINUATION_PUNCTUATION`）或 `shouldDropDuplicatedBoundary(a, b)`（`a` 末字符与 `b` 首字符相同、且 `b` 第二个字符属于续接标点集合，典型是硬断行在标点处被重复抄写）→ **立即返回 true**（强合并信号）。
8. 以上均未命中：兜底返回 **true**（两段都是纯文本，且 `a` 未以终止符结束，默认合并）。

**判定顺序即优先级，前面任一条件命中即短路返回，不可调换。**

## 算法：`plainLineRejectedForListContinuation` / `shouldMergeListWithPlainContinuation` / `hasEmbeddedOrderedListMarker`

`plainLineRejectedForListContinuation(plainBody)`：
1. 为空/blank 返回 false。
2. `b = plainBody.stripLeading()`；为空返回 false。
3. `b` 以 `#`/`>`/`|` 开头 → true。
4. `b` 以 ` ``` ` 开头 → true。
5. `ORDERED_LIST_MARKER_PREFIX.matches(b)` → true。
6. `LIST_BULLET.matches(b)` → true。
7. 否则返回 `isHeadingByRegex(b) || isListItem(b)`（Part2 共享函数）。

`shouldMergeListWithPlainContinuation(listBody, plainBody)`：
1. `a = listBody.trim()`, `b = plainBody.trim()`；任一为空返回 false。
2. `!isListItem(a) || plainLineRejectedForListContinuation(plainBody)` → false（`a` 本身不是列表项，或 `b` 命中拒绝续接的结构特征）。
3. `hasEmbeddedOrderedListMarker(a)` → false（`a` 单行内已含 ≥2 个有序标记，结构本身已破损，不再吸收后续 plain 行）。
4. `endsWithSemanticBreak(a)` → false。
5. `startsWithContinuationPunctuation(b) || shouldDropDuplicatedBoundary(a, b)` → **立即返回 true**。
6. `end = lastNonSpaceChar(a)`：若 `end` 属于 `。.!！?？` → 返回 false（列表项已完整收束）；若属于 `，,、；;：:`（软标点）→ 返回 true（允许续接）。
7. 否则返回 `!endsWithSentenceTerminator(a)`（保守：`a` 若未以任何终止符结束则允许续接）。

`hasEmbeddedOrderedListMarker(text)`：`t = text.trim()`；为空返回 false；用 `EMBEDDED_ORDERED_LIST_MARKER` 在 `t` 上 `find()` 循环计数，命中 ≥2 次即返回 true（一次遍历，找到第二个即可提前返回）。

## 算法：`isPageNumberBlock`

```
isPageNumberBlock(block, pageHeight, config):
    if block == null: return false
    t = block.text.trim()  // null 视为空串
    if !PAGE_NUMBER_BLOCK.matches(t): return false
    top = block.topDistance
    return top < pageHeight * config.headerPageNumberRatio
        || top > pageHeight * config.footerPageNumberRatio
```

调用点：`convertDocument` 主流程中，`config.removePageNumbers == true` 时，对每页抽取出的 `textBlocks` 做 `filter(!isPageNumberBlock(b, pageHeight, config))`（发生在表格抽取和跨页合并**之前**，逐页独立过滤）。`headerPageNumberRatio`/`footerPageNumberRatio` 是 `Config` 字段（页面高度占比阈值），具体默认值请在 `Config.defaults()` / `config.properties` 中核对（Part1 范围，此处仅引用字段名）。

## 算法：`cleanOutput`

最终收尾清理，`convertDocument` 返回前的最后一步（在 `fallbackMergeMarkdown` 和 `removeTocFromMarkdown` **之后**执行）。

1. 统一换行符为 `\n`。
2. 按行切分（`split("\n", -1)`），调用 `MarkdownStructureRules.splitEmbeddedPipeTableLines(lines)` 拆行内嵌管道表，重新 `join("\n")`。
3. `normalized = splitConcatenatedOrderedListLines(normalized)`（见下）。
4. `normalized = mergeWrappedListContinuationLines(normalized)`（见下）。
5. 依次应用三个正则替换并返回：
   a. `replaceAll("\n{3,}", "\n\n")`（压缩 3+ 连续换行为 2）。
   b. `replaceAll("^\n+", "")`（去掉开头的换行）。
   c. `.trim()`（去掉首尾空白）。

**执行顺序严格如上，步骤 3、4 均在正则清理之前。**

### `splitConcatenatedOrderedListLines(markdown)`

把一行内粘连的多个有序列表编号拆成多行（PDF 硬断行/几何合并常把 `1.xxx2.yyy3.zzz` 拼进同一行）。

1. 按 `\n` 切分（`-1` 保留末尾空行）。
2. 对每一行调用 `splitSingleLineByOrderedMarkersAndTail(line)`，把返回的多行结果全部 `addAll` 进输出列表。
3. `join("\n")` 返回。

`splitSingleLineByOrderedMarkersAndTail(line)`：

1. `raw = line`（null 视为空串）；`t = raw.trim()`；为空返回 `[raw]`。
2. **表格行保护**：若 `isMarkdownTableRowLine(t)`（`t.length() >= 2 && t.charAt(0)=='|' && t.charAt(last)=='|'`），直接返回 `[raw]`（GFM 表格行内容即便粘连多个编号也绝不拆分，防止破坏表格语法）。
3. 用 `EMBEDDED_ORDERED_LIST_MARKER` 在 `raw` 上 `find()` 循环，收集所有匹配起始位置 `starts[]`，但排除 `isOrderedMarkerInsideNumericHierarchyPrefix(raw, start)` 为真的位置（即该编号标记其实是 `2.5.1.` 这种多级数字层级前缀的一部分，不算独立列表项起点——判定方式：`raw.substring(0, start)` 匹配 `.*\d+(?:\.\d+)*\.$`）。
4. 若 `starts.size() < 2`（不足两个独立编号标记，视为无需按编号拆分）：
   - 先调用 `splitTailOrgDateInListLine(raw)` 尝试拆出「列表正文 + 落款机构 + 日期」三段（见下）；
   - 对返回的每一段再调用 `splitStructuralHeadingSegments(part)`（即 `ChapterTocLineRemover.splitEmbeddedCnSectionHeadings(part)`，把行内嵌入的「二、」类小节标题拆成独立行——**递归**：拆出的右半部分若还含嵌入小节会继续拆）；
   - 把所有段落 `addAll` 汇总；若最终为空返回 `[raw]`。
5. 若 `starts.size() >= 2`：
   - 若 `starts[0] > 0`，取 `head = raw.substring(0, starts[0]).trim()`；非空则对 `head` 同样跑 `splitStructuralHeadingSegments` 并入结果（**注意 head 段不走 `splitTailOrgDateInListLine`**，因为它不是以编号开头的列表段）。
   - 遍历 `starts[k]`：`seg = raw.substring(starts[k], (k+1<len? starts[k+1] : raw.length())).trim()`；非空则先 `splitTailOrgDateInListLine(seg)` 再对每段 `splitStructuralHeadingSegments`，全部并入结果。
   - 结果为空返回 `[raw]`。

`splitTailOrgDateInListLine(line)`（把「N.现场勘察地点……某某局 2024年5月1日」这类"列表正文+机构落款+日期"粘连行拆成三段）：

1. `t = line.trim()`；`!isListItem(t)` 直接返回 `[line]`（原样不处理非列表行）。
2. `DATE_AT_LINE_END` 在 `t` 上 `find()`；未命中返回 `[t]`。
3. `date = 捕获组1.trim()`；`dateStart = 捕获组1起始位置`；`beforeDate = t.substring(0, dateStart).trim()`；`beforeDate` 或 `date` 为空返回 `[t]`。
4. `pair = splitListBodyAndOrgTail(beforeDate)`；为 `null` 返回 `[t]`。
5. `listBody = pair.left`, `org = pair.right`；任一为空返回 `[t]`。
6. 否则返回 `[listBody, org, date]`（三行）。

`splitListBodyAndOrgTail(beforeDate)`：

1. 为空/blank 返回 null。
2. 按 `ATTACHMENT_ITEM_KEYWORDS` 数组声明顺序（`统计表, 示意图, 现状照片, 照片, 附件`）依次尝试：`idx = beforeDate.lastIndexOf(keyword)`；未找到跳过该关键词；找到后 `boundary = idx + keyword.length()`；若 `boundary >= beforeDate.length()`（关键词恰好在末尾、右侧无内容）跳过该关键词继续下一个；否则 `left = beforeDate.substring(0, boundary).trim()`, `right = beforeDate.substring(boundary).trim()`；两者都非空则**立即返回** `{left, right}`。
3. 若关键词全部未命中：用 `ORG_SUFFIX_AT_END`（`[一-龥]{4,}(?:分局|人民政府|委员会|支队|大队|局)\s*$`）在 `beforeDate` 上 `find()`；未命中返回 null。
4. `org = 捕获组1.trim()`；`orgStart = 捕获组1起始位置`；`listBody = beforeDate.substring(0, orgStart).trim()`；两者任一为空返回 null；否则返回 `{left: listBody, right: org}`。

### `mergeWrappedListContinuationLines(markdown)`

把因硬换行拆散、且中间可能夹一个多余空行的"列表项 + 续行"重新合并为一行。

1. 按 `\n` 切分（保留空行）。
2. 用下标 `i` 从 0 到 `size-2` 遍历（**这是一个会在循环体内修改数组长度、且 `i` 会回退的循环，需用 `while` 而非 `range` 移植**）：
   a. `cur = lines[i].trim()`, `next = lines[i+1].trim()`；`cur` 为空跳过（`i++`，continue 到下一轮，实际是 for 循环自增，不特殊处理）。
   b. `!isListItem(cur)` 跳过本轮（不是列表行，不尝试合并）。
   c. **隔空行合并**：若 `next` 为空 **且** `i+2 < size`：取 `afterBlank = lines[i+2].trim()`；若 `isLikelyListContinuationLine(afterBlank) && shouldMergeListWithPlainContinuation(cur, afterBlank)`：`lines[i] = normalizeText(mergeText(cur, afterBlank), Config.defaults())`；依次 `remove(i+2)` 再 `remove(i+1)`（**先删除下标大的**，避免下标错位）；`i = max(-1, i-1)`（**回退一步以便重新检查合并后的新行是否还能继续吸收后面的内容**）；`continue`（跳过本轮剩余步骤，直接进入下一轮循环——注意 for 循环的自增仍会执行，所以实际下一轮是 `i-1+1=i`，即真的会重新停在同一位置检查）。
   d. **紧邻行直接合并**：若步骤 c 未触发或条件不满足，检查 `!isLikelyListContinuationLine(next)` → 不满足跳过本轮；`!shouldMergeListWithPlainContinuation(cur, next)` → 不满足跳过本轮；否则 `lines[i] = normalizeText(mergeText(cur, next), Config.defaults())`；`remove(i+1)`；`i = max(-1, i-1)`（同样回退重试）。
3. 返回 `join("\n", lines)`。

**移植要点**：这是原地修改 + 索引回退的循环模式，Go 实现建议用 `for i := 0; i+1 < len(lines); i++` 配合 slice 的删除操作，并在合并发生后执行 `i--`（等价于 Java 的 `i = max(-1, i-1)`，因为 for 循环末尾还会 `i++`，两者效果一致：合并后重新停留在同一 `i` 位置）。

`isLikelyListContinuationLine(line)`：为空/blank 返回 false；`t = line.stripLeading()`；`t` 以 `#`/`|`/`>`/`` ``` `` 开头返回 false；`plainLineRejectedForListContinuation(line)` 为真返回 false；否则返回 `!isListItem(t)`（必须本身不是另一个列表项，纯粹是延续文本）。

## 执行顺序

在 `PdfToMarkdown.convertDocument` 主流程中，本 Part 相关步骤的精确插入点（承接 Part1 的几何抽取/排序/跨页合并、Part2 的样式聚类与渲染循环）：

```
1. [Part1] 逐页抽取 TableBlock/TextBlock；config.removePageNumbers 时对每页 textBlocks
   过滤 isPageNumberBlock（本 Part 函数，但发生在几何抽取阶段，早于全局排序）
2. [Part1] 全局按 page->top->left 排序；跨页表格/段落合并；剔除 monoFont 得到 orderedTextBlocks
3. [Part2] listGuideScopeBlockIds = ListGuideHeuristics.detectListGuideScopeBlockIds(orderedTextBlocks)
4. [Part2] headingStyleProfile = buildHeadingStyleProfile(...)（若 config.styleClusterHeadingEnabled）
5. [Part2] shortPhraseListRunBlockIds = ShortPhraseListRunHeuristics.detectPdfShortPhraseListRuns(...)
6. [Part3] headingSequenceDemoteBlockIds = new HashSet(
       detectHeadingSequenceConsistencyDemoteBlockIds(orderedTextBlocks, config, headingStyleProfile, shortPhraseListRunBlockIds))
   headingSequenceDemoteBlockIds.addAll(
       detectDisqualifiedPatternDemoteBlockIds(orderedTextBlocks, config))
   —— 两个 Part3 函数的结果在此合并为同一个 set，供下一步渲染循环统一消费
7. [Part2] 主渲染循环：遍历 elements，表格走 renderTableMarkdown/appendSingleCellTableAsText；
   文本块走 appendTextAsMarkdown(..., headingSequenceDemoteBlockIds)——该 set 作为「即便正则/样式
   判定为标题也必须强制按正文渲染」的黑名单，具体消费点在 isHeading 内部（Part2 范围）
8. [Part3] if config.fallbackMergeMarkdown: markdown = fallbackMergeMarkdown(markdown, config)
9. [Part3] if config.removeToc: markdown = removeTocFromMarkdown(markdown)
10. [Part3] return cleanOutput(markdown)
```

**关键衔接点**：
- 步骤 6 产出的 `headingSequenceDemoteBlockIds` 是 Part3 → Part2 的唯一数据流出口（Part3 的判定结果影响 Part2 渲染循环的标题/正文取舍），必须在渲染循环开始前完成计算。
- 步骤 8/9/10 是纯粹的字符串后处理，输入是步骤 7 渲染循环产出的完整 Markdown 字符串（`StringBuilder out` 的 `toString()`），彼此之间是严格串行依赖（`fallbackMergeMarkdown` 的输出是 `removeTocFromMarkdown` 的输入，其输出又是 `cleanOutput` 的输入），且分别受各自独立的 `config.fallbackMergeMarkdown`/`config.removeToc` 开关控制（`cleanOutput` 无开关，总是执行）。
- 步骤 1 的 `isPageNumberBlock` 过滤发生在几何阶段（每页独立），与步骤 8-10 的字符串阶段清理是两个完全独立的时间点，互不依赖，Go 实现时不要试图合并这两处逻辑。

## 潜在遗漏 / 与其他 Part 的重叠

1. **`TOC_CHAPTER_PAGED_ENTRY`（L1049）是死代码**：全文搜索确认没有任何函数引用这个 Pattern 常量。移植时建议：保留其正则文本作为注释/测试夹具参考（防止未来误删有用信息），但**不要**接线到任何调用点。如果人工核对后发现这是历史重构遗留（曾被某处使用后来改用了 `ChapterTocLineRemover.CHAPTER_TOC_LINE` 等价逻辑），也不需要恢复，两者语义已被 `isChapterTocLine` 覆盖。

2. **`isOrderedMarkerInsideNumericHierarchyPrefix`（L3157）被两处调用**：一处在本 Part 的 `splitSingleLineByOrderedMarkersAndTail`（cleanOutput 链路），另一处在 `isNumericHierarchyHardWrapContinuation`（L3139，硬断行判定，属于 Part1/2 的跨行合并范围）。这是一个真正的共享叶子函数，建议 Go 移植时放在两个 Part 都能访问的公共内部包（如 `internal/pdfport/textutil`），不要在两个 Part 各自重复实现一份，否则未来行为漂移风险高。

3. **大量文本工具函数是三方共享叶子**（`normalizeText`、`isHeadingByRegex`、`isListItem`、`mergeText`、`needSpace`、`endsWithSentenceTerminator`、`endsWithSemanticBreak`、`isShortLabel`、`isStandaloneChineseDateLine`、`startsWithContinuationPunctuation`、`shouldDropDuplicatedBoundary`、`lastNonSpaceChar`/`firstNonSpaceChar`/`secondNonSpaceChar`）：本文档只在调用点说明了它们的语义与调用方式，**没有重新规格化其内部实现**——这些函数的权威定义应以 Part1/2 的规格文档为准（它们同样被 `appendTextAsMarkdown`/`isHeading`/跨页合并大量使用）。人工合并三份文档时，请确认这些函数**只被规格化一次**，且三份文档对同一函数的行为描述互相一致；若发现不一致，以直接读取源码（行号已在本文档标注）为准重新核对。

4. **独立工具类文件（`HeadingSequenceConsistencyHeuristics`/`HeadingPatternQualityHeuristics`/`HeadingLevelPrefixHeuristics`/`ChapterReferenceHeuristics`/`ChapterTocLineRemover`/`HeadingSuppressHeuristics`/`MarkdownStructureRules`/`InlinePipeTableSplit`）本身不是 `PdfToMarkdown.java` 的一部分，是否也要拆进 Part1/2/3 的移植范围、还是整体作为「共享工具层」单独立项，取决于人工合并三份 Part 文档时的判断**。本文档采用的原则是：只精确规格化**被本 Part 明确调用到的方法**，对同一文件里明显只服务 Word/MPP 路径的方法（如 `HeadingSequenceConsistencyHeuristics.detectMarkdownLinesToDemote`、`HeadingPatternQualityHeuristics.detectMixedRecognitionPatternKeys`/`buildInferDisqualifiedPatternKeys`/`filterHitsAndDemoteLines`/`demoteDisqualifiedPatternLines`、`HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency`、`ChapterTocLineRemover` 中一堆 `recoverFirstChapterFromBlock`/`normalizeGluedStructuralChapterHeading`/`splitGluedChapterHeadingFromFollowingMarker`/`stripChapterHeadingTrailingNoiseChar` 等）**未规格化**——这些要么完全不被 PDF 路径调用，要么只在 Part3 范围内的某个函数（如 `stripBareTocChapterRuns` 内部用到 `normalizeGluedStructuralChapterHeading`，本文档已给出）中间接用到。如果 Word/MPP 转换器也要移植（不在本次任务范围内），这些方法需要额外规格化，但**不应该**归入本次 PDF 三份 Part 的任何一份。

5. **`HeadingSuppressHeuristics.isStandaloneHeadingLine`/`looksLikeCnArticleBodyParagraphLead`/`looksLikeCnArticleBodySentence`/`startsWithCnArticleHeading`/`isStandaloneNumericHierarchyLine`**：这些方法同时被 Part2 的 `isHeading`/`shouldSuppressHeading` 家族（`HeadingSuppressHeuristics.shouldSuppressHeading` 两个重载）和本 Part 的 `clearlyFailsHeadingQuality`/`shouldMergeMarkdownPlainBlocks` 调用。本文档已给出这几个具体方法的完整正则与判定逻辑（见「算法：`detectDisqualifiedPatternDemoteBlockIds`」小节内的 `clearlyFailsHeadingQuality` 展开），Part2 文档大概率也会规格化同一批方法（因为 `shouldSuppressHeading` 直接调用它们）——**这是明确的重叠点，人工合并时保留一份即可，两份描述若有出入以本文档给出的源码行号重新核对**。

6. **`ChapterReferenceHeuristics.isNumberedClauseContinuation`**（L51-67，未在本文档规格化，仅规格化了 `isBodyChapterReference`）：被 L2443 附近的跨行合并逻辑调用，属于 Part1/2 的跨页/跨行段落合并范围，不在本 Part。

7. **`ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine`**：被 `HeadingSequenceConsistencyHeuristics.isParallelEnumerationSibling`（本 Part 算法链路的一部分）调用，但其定义文件 `ShortPhraseListRunHeuristics.java` 整体属于 Part2 的样式聚类家族（`detectPdfShortPhraseListRuns` 是 Part2 显式列出的函数）。**这是一个反向依赖**：本 Part 的 TOC/连坐算法在 `lines==null`（PDF 路径固定值）时实际上不会执行到这一分支（见「算法：`detectHeadingSequenceConsistencyDemoteBlockIds`」小节第 7 点的分析——`isParallelEnumerationSibling(lines=null, ...)` 直接返回 true，短路了对 `looksLikeSectionTitleNumberedLine` 的调用），所以**移植 Go 版本时，PDF 路径下这个函数可以不接线**，但建议保留调用桩（即便传参恒为 nil 路径），并在代码注释里注明"仅 Word/MPP 路径需要，PDF 路径因 lines 恒为 nil 而不可达"，以防未来行为变更时被误删导致静默漂移。

8. **`normalizeGluedStructuralChapterHeading`**（`ChapterTocLineRemover`，本文档在 `stripBareTocChapterRuns`/`stripLinesInternal` 说明中已给出简要行为描述，但未展开其完整正则 `GLUED_CHAPTER_HEADING`/`CHAPTER_TOC_PAGE_SUFFIX`）：由于它只在 `stripLinesInternal` 的"其余非围栏行"分支被调用（即 `ChapterTocLineRemover.stripFromMarkdown` 链路，本 Part 范围内），建议移植时按本文档给出的行为摘要 + 直接读取源码 L319-334 实现，不需要额外征询，风险较低（纯粹是往「第X章题名」中插入一个空格的格式规范化，不影响内容取舍）。
