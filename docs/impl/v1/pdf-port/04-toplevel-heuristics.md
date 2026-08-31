# PDF 移植 Part 4：顶层文本启发式类（ChapterReference / TOC / HeadingXxx / ListGuide / ShortPhraseListRun / StructureRules / CodeFenceWriter）

来源：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/` 下 10 个顶层类，供 `PdfToMarkdown.java`（另行拆分移植）直接调用。本文档只描述这 10 个类自身的行为，不重复 `PdfToMarkdown.java` 或 `mpp` 子包的实现。

所有类均为纯文本/字符串逻辑，不依赖 PDF 几何信息，除个别方法直接接收 `PdfToMarkdown.TextBlock`/`Config`/`HeadingStyleProfile` 作为参数（这些类型由负责 `PdfToMarkdown.java` 的分片定义，此处按字段访问方式说明依赖）。

## 外部依赖字段/配置一览（非本文档定义，供实现者对照）

- `PdfToMarkdown.Config`（`Config.defaults()` 的实际取值，供阈值移植核对）：
  - `fontSizeDeltaPt = 0.5`
  - `maxHeadingLength = 80`
  - `shortPhraseNumberedRunMin = 3`
  - `shortPhraseNumberedRunMaxGap = 3`
  - `shortPhraseNumberedRunMaxBodyLines = 1`
  - `shortPhraseNumberedRunSeqQualityMin = 0.8`
  - `shortPhraseNumberedBodyMaxLen = 18`
- `PdfToMarkdown.TextBlock` 字段用到：`text`, `id`, `monoFont`, `fontSizeMean`, `fontWeight`
- `PdfToMarkdown.HeadingStyleProfile`：`isReliable()`, `roleOf(block)` 返回 `StyleClusterRole{H1,H2,BODY,NOISE,UNKNOWN}`
- `PdfToMarkdown.isHeadingByRegex(String)`, `PdfToMarkdown.isListItem(String)`, `PdfToMarkdown.normalizeText(String, Config)`, `PdfToMarkdown.looksLikeSectionTitleBody(String)`, `PdfToMarkdown.isBodyOrNoiseLikeForShortPhraseListRun(TextBlock, HeadingStyleProfile, Config)`
- `mpp.MarkdownTitlePattern.matchFirst(String)` / `.parseNum(String)`
- `mpp.MarkdownHeadingHit{level, titleRaw, lineIndex}`（可变字段，`level`/`lineIndex` 会被就地修改）
- `mpp.MarkdownPipelineLineUtils.normalizeLine(String)`, `.stripEdgeWhitespace(String)`, `.LEADING_HEADING_PRIORITY_MARKERS`（正则，匹配行首优先级标记如 ★）
- `mpp.ChapterTocCatalog`, `mpp.ChapterTocHeadingValidator.matchesAnyCatalogChapter(String, ChapterTocCatalog)`
- `InlinePipeTableSplit.findInlinePipeTableStart(String)`, `.splitLineIfNeeded(String)`, `.splitMarkdownLines(List<String>)`（未在本次读取范围内，属于其他文件，仅按调用签名记录）
- `MarkdownLineClassifier.looksLikePreformattedBlock(List<String>)`（同上，仅按调用签名记录）

---

## Go regexp 兼容性预警

Go 标准库 `regexp`（RE2 引擎）**不支持**零宽断言 `(?=...)` `(?!...)` `(?<=...)` `(?<!...)`。以下正则命中此限制，逐一列出替代实现思路：

| 出处 | 正则（原样） | 断言类型 | 匹配语义 | Go 替代思路 |
|---|---|---|---|---|
| `ChapterTocLineRemover.CHAPTER_HEADING_EMBEDDED_NOISE_CHAR_BEFORE_PAGE_NUM` | `(第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*[\p{IsIdeographic}（）()·]{1,30}[.．·…]{0,3})[A-Za-z0-9'’‘"”“](?=[ \t　]+(?:\d{1,4}\s*[-–—]|第\s*[一二三四五六七八九十百千万零\d]+\s*章))` | 正向先行 `(?=...)` | 「噪声字符」后必须紧跟「空白+数字页码-短横」或「空白+新的第X章」，否则不算噪声字符 | 用不含先行断言的正则匹配到「组1 + 噪声字符」为止的位置（`regexp.FindStringSubmatchIndex`），取匹配结束位置后的剩余子串，另用一个**行首锚定**的正则（`^[ \t　]+(?:\d{1,4}\s*[-\x{2013}\x{2014}]|第\s*[一二三四五六七八九十百千万零\d]+\s*章)`）对该剩余子串做 `MatchString` 校验；两步都通过才替换 |
| `HeadingLevelPrefixHeuristics.PREFIX_DEFS`：`TITLE_NUM_FIVE/FOUR/THREE/TOW` | `^(\d+(?:\.\d+){N})(?:[.．])?(?![.．\d-])\s*.*`（N=4/3/2/1） | 负向先行 `(?!...)` | 捕获的多级数字编号后不得紧跟 `.`、`．`、数字或 `-`（防止把「1.20-30」「1.2.3」这类更长编号/数值区间的前缀误判为该级编号） | 正则改为 `^(\d+(?:\.\d+){N})(?:[.．])?` 捕获后，用 `regexp.FindStringSubmatchIndex` 拿到捕获结束位置，若该位置存在下一个 rune 且属于 `.`/`．`/`0-9`/`-`，则视为不匹配（在 Go 代码里手工判断，不再退化为正则） |
| `HeadingLevelPrefixHeuristics.PREFIX_DEFS`：`TITLE_NUM_DOT` | `^(\d+)[.．](?!\d|-)\s*.*` | 负向先行 | 「N.」编号后不得紧跟数字或 `-`（防止把「1.2」「1-3」误判为一级序号「1.」） | 同上：先匹配 `^(\d+)[.．]`，再手工检查紧随字符是否为数字或 `-` |
| `HeadingSequenceConsistencyHeuristics.PATTERN_DEFS`：`TITLE_NUM_FIVE/FOUR/THREE/TOW`、`TITLE_NUM_DOT` | 同上四个/一个模式，但**不带**负向先行断言（如 `^(\d+(?:\.\d+){4})\.?\s*.*`） | 无 | 这是本文件里序列一致性检测专用的宽松版本，**故意不做**编号后缀排除——直接按 Go `regexp` 原样翻译即可，无需断言替代 | 无需处理，照抄 |
| `ShortPhraseListRunHeuristics.PATTERN_DEFS`：`TITLE_NUM_FIVE/FOUR/THREE/TOW`、`TITLE_NUM_DOT` | 与 `HeadingLevelPrefixHeuristics` 完全相同的带负向先行版本 | 负向先行 | 同 `HeadingLevelPrefixHeuristics` | 同上替代思路，且两处应共享同一个 Go 辅助函数（详见文末「Go 包组织建议」） |
| `ListGuideHeuristics.NUM_LEVEL2_PREFIX` | `^(\d+)\.(\d+)(?!\.)(?:\s*.*)$` | 负向先行 | 「N.M」二级编号后不得再跟一个 `.`（防止把「1.2.3」三级编号的前两段误判为二级） | 先匹配 `^(\d+)\.(\d+)`，手工检查紧随字符是否为 `.` |

**汇总**：以上共 **7 个正则定义**用到负向先行断言（`ChapterTocLineRemover` 1 个 + `HeadingLevelPrefixHeuristics` 5 个 + `ListGuideHeuristics` 1 个），另有 **5 个结构相同的重复定义**出现在 `ShortPhraseListRunHeuristics`（与 `HeadingLevelPrefixHeuristics` 的 5 个一一对应，字符级相同）。按「不同正则文本」去重计算，需要断言替代方案的正则共 **7 处**（`ShortPhraseListRunHeuristics` 的 5 处与 `HeadingLevelPrefixHeuristics` 的 5 处逻辑完全相同，建议在 Go 中只写一次辅助函数共用，见文末）。

无先行/后行断言，但值得注意的其他 Java 正则专有语法：

- `\p{IsIdeographic}`：Java 专有的 Unicode 二元属性写法（等价于 `\p{Is}` + Unicode Character Database 的 `Ideographic` 属性），用于 `ChapterTocLineRemover`（4 处）、`HeadingLevelPrefixHeuristics`（无）、`GLUED_TITLE_CHAR`。Go `regexp`（RE2）**不支持** `\p{IsIdeographic}` 这个属性名，只认识固定的一组 Unicode 分类/脚本名（如 `\p{Han}`、`\p{L}`）。移植时应替换为 `\p{Han}`（CJK 统一表意文字），如需更宽覆盖再补充具体 rune range（Java `Ideographic` 属性额外包含少量非 Han 字符如 〇 之类，量很小，实测中文商务文档基本用不到，可先用 `\p{Han}` 落地，如后续测试发现遗漏字符再补充自定义 range）。
- `Pattern.CASE_INSENSITIVE`（用于 `TITLE_ROMAN`、`TOC_MARKER_LINE`）：Go 中用内联标志 `(?i)` 前缀实现，等价。
- Java `String.isBlank()`/`.strip()`：对应 Go 需要手写「trim + 判断是否全为空白（含全角空格 `　`）」的辅助函数，`strip()` 用 `strings.TrimSpace`，但需注意 Go 的 `strings.TrimSpace` 是否覆盖 `　`（全角空格）——**不覆盖**，需要在 trim 函数里显式加入 `　` 作为可裁剪字符（Java `isWhitespace`/`isBlank` 对全角空格的处理需与原实现逐处核对，多处用 `\s` 正则或 `Character.isWhitespace` 时同理，Go 的 `unicode.IsSpace('　')` 返回 `true`，可放心使用 `unicode.IsSpace` 做逐字符判断，但 `strings.TrimSpace` 底层也用 `unicode.IsSpace`，其实**是覆盖的**，无需额外处理——这条仅作确认，不构成阻塞）。
- Java `record`：`Entry`、`ParsedSequence`、`PatternDef`、`SequenceEntry` 等均为不可变值类型，Go 用普通 `struct` 平替，无兼容性问题。

---

## ChapterReferenceHeuristics

### 职责
识别正文中对章节的「指称」（如"…见第五章《采购需求》，否则投标无效"），这类文字虽含"章"字但本身是叙述性正文，不应被误判为独立的章节标题行；并提供编号条款续行判定（如 `5.7` 标题后紧跟 `5.7.1` 正文）。

### 常量与正则

| 名称 | 值/模式（原样） | 说明 |
|---|---|---|
| `LEADING_MD_HASH` | `^\s{0,3}#{1,6}[\s　]*` | 匹配行首 0-3 个空格 + 1-6 个 `#` + 空白（含全角空格），用于剥离 Markdown 标题前缀 |
| `NUMBERED_PREFIX` | `^(\d+(?:\.\d+)+)` | 匹配行首形如 `5.7`、`5.7.1` 的多级数字编号并捕获整个编号串（要求至少一个 `.` 分隔，纯个位数字如 `5` 不匹配） |
| `CHAPTER_BOOK_TITLE_REF` | `第\s*[一二三四五六七八九十百千万零\d]+\s*章《[^》]{1,40}》` | 匹配「第 X 章《…》」形式的书名号章节指称，`X` 可为中文数字或阿拉伯数字，书名号内 1-40 个非 `》` 字符 |

### 算法：`stripHashes(line)`
1. `line` 为空/全空白返回空字符串。
2. `strip()` 去首尾空白。
3. 用 `LEADING_MD_HASH` 替换首次出现的匹配为空串（即去掉行首 `#` 前缀）。
4. 再次 `strip()` 后返回。

### 算法：`isBodyChapterReference(line)`
1. `line` 为空/全空白返回 `false`。
2. `t = stripHashes(line)`；若 `t` 为空或不含"章"字，返回 `false`。
3. 若 `ChapterTocLineRemover.isChapterTocLine(t)` 为真（是目录页行），返回 `false`（目录行由另一套规则处理，不算正文指称）。
4. 若 `t` 包含以下任一子串："否则投标无效"、"具体见"、"做出了响应"、"偏差和例外"、"说明所提供"，返回 `true`。
5. 若 `CHAPTER_BOOK_TITLE_REF` 在 `t` 中能找到匹配（`find`，非全串匹配），**且** `t` 同时包含 "）" 或 "), " 或 "，" 或 "," 中的任意一个，返回 `true`。
6. 若 `t` 以"第"开头、包含"《"、长度 > 18，且包含"；"或"："或"。"或"，"中任意一个，返回 `true`。
7. 否则返回 `false`。

### 算法：`isNumberedClauseContinuation(left, right)`
1. `left`/`right` 任一为 `null` 返回 `false`。
2. `a = stripHashes(left)`，`b = stripHashes(right)`；任一为空返回 `false`。
3. 用 `NUMBERED_PREFIX` 分别在 `a`、`b` 上 `find()`，任一未命中返回 `false`；取捕获组 `pa`、`pb`。
4. 判断 `pb` 是否为 `pa` 的"子编号延伸"：`pb.startsWith(pa + ".")`，或者（`pb.startsWith(pa)` 且 `pb.length() > pa.length()` 且 `pb` 在 `pa` 长度处的字符是 `.`）——两种写法等价，都是判断 `pb` 严格以 `pa + "."` 为前缀。不满足则返回 `false`。
5. 若 `right` 本身是独立成行的数字层级标题（`HeadingSuppressHeuristics.isStandaloneNumericHierarchyLine(b)` 为真，如「2.5.1发文管理」这种编号后紧跟短标题名的独立标题行），返回 `false`（说明 `right` 不是 `left` 的正文续段，而是它自己的标题）。
6. 否则返回 `true`。

### 调用方
`PdfToMarkdown.java`：
- `detectDisqualifiedPatternDemoteBlockIds`（约 311 行）：对每个文本块 `normalizeText` 后，若 `isBodyChapterReference` 命中就把该行文本置空（不参与后续标题模式质量扫描，避免正文里的章节指称被当成候选标题行影响同前缀模式判定）。
- 段落合并逻辑（约 2443-2457 行）：`isNumberedClauseContinuation(na.text, nb.text)` 命中时判定两个块应合并为同一段落（编号条款标题与其子编号续行）；`isBodyChapterReference` 命中时用于阻止/允许跨块合并（正文指称不应被当作断句边界处理，或反之阻止与后续内容错误合并）。

---

## ChapterTocLineRemover

### 职责
剔除 PDF 目录页误提取出的「第 X 章 + 标题 + 页码」行（目录条目不应被当成正文标题输出）；提供正文结构性章节标题/中文小节标题的判定；处理目录页整块识别、OCR 场景下丢失引导点的"裸目录"识别；修复标题粘连（噪声字符、章号与题名无空格粘连、章标题与后续内容粘连）等一系列与"章"相关的行级规整。

### 常量与正则

| 名称 | 值/模式（原样） | 说明 |
|---|---|---|
| `CHAPTER_TOC_LINE` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*(?:\.{1,}\|…{1,}\|·{1,}\|⋯{1,}\|\s{2,}\|\t).*\d{1,4}\s*$` | 目录行：「第X章」起始，中间任意内容，接引导符号（连续 `.`/`…`/`·`/`⋯`/2+空格/Tab）后跟任意内容再以 1-4 位数字页码结尾 |
| `CHAPTER_TOC_DASH_PAGE` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}.*-\d+-\s*$` | 目录行，页码用「-N-」形式（如"第一章总则............-1-"） |
| `CHAPTER_TOC_DASH_PAGE_TRUNCATED` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}-\s*$` | 目录行被硬断行，只留下引导点+悬空的 `-`（下一行才是页码数字，如"1-"） |
| `CHAPTER_TOC_ORPHAN_DASH_PAGE` | `^(?:#{1,6}\s*)?\d+-\s*$` | 与上面成对的孤立残留行，形如"1-" |
| `CHAPTER_TOC_EMBEDDED_PAGE` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\.{2,}\d{1,4}\p{IsIdeographic}.*` | 目录行：引导点后紧跟短页码，页码后又粘连了正文汉字（OCR 误合并多列） |
| `CHAPTER_TOC_PAGE_SUFFIX` | `(?:\.{1,}\|\s{2,})(?:\d{1,4}\|-\d+-)\s*$` | 用于从目录行**剥离**页码后缀（引导点+页码 或 引导点+`-N-`），恢复裸标题文本 |
| `GLUED_CHAPTER_HEADING` | `^(?:#{1,6}\s*)?(第\s*[一二三四五六七八九十百千万零\d]+\s*章)([\p{IsIdeographic}（）()·\s]{2,60})$` | 匹配"第X章"紧跟 2-60 个汉字/括号/空格构成的题名，整行无其他内容（正文中粘连无空格的章标题） |
| `CHAPTER_HEADING_TRAILING_NOISE_CHAR` | `(第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*[\p{IsIdeographic}（）()·]{1,30})[A-Za-z0-9'’‘"”“]$` | 章标题纯汉字题名后，行尾紧跟单个 OCR 噪声字符（英文字母/数字/引号），无空格分隔 |
| `CHAPTER_HEADING_EMBEDDED_NOISE_CHAR_BEFORE_PAGE_NUM` | 见上文 Go 兼容性表格 | 目录多条目合并成一行后，噪声字符嵌在题名与后续真页码/新条目之间（**含正向先行断言，需 Go 特殊处理**） |
| `GLUED_CHAPTER_PREFIX` | `^(?:#{1,6}\s*)?(第\s*[一二三四五六七八九十百千万零\d]+\s*章)` | 定位"第 X 章"前缀本身（用于逐字扩张探测题名边界） |
| `GLUED_TITLE_CHAR` | `[\p{IsIdeographic}（）()·]` | 单字符判定：是否为"题名字符"（纯汉字/括号/间隔号），数字与标点运算符不在此列 |
| `MAX_GLUED_TITLE_LEN` | `20` | 题名边界逐字扩张探测的最长字符数上限 |
| `MIN_CHAPTER_TOC_LINES_IN_BLOCK` | `1` | 段落块判定为"纯目录块"所需的最少目录行数 |
| `CHAPTER_HEADING` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章.*` | 泛化的"第X章"起始行匹配（不区分目录/正文） |
| `CN_SECTION_HEADING` | `^(?:#{1,6}\s*)?([一二三四五六七八九十百千万]+)[、.．\s].*` | 中文数字 + 顿号/点/空格 起始的行（如"一、"“二．”），捕获中文数字部分 |
| `HEADING_PREFIX_FRAGMENT` | `^\s*(?:第\s*[一二三四五六七八九十百千万零\d]+\s*章\|[一二三四五六七八九十百千万零]+[、.．])` | 行首是否为"第X章"或"中文数字+顿号/点"这类层级标题前缀片段 |
| `CHAPTER_PREFIX_ONLY` | `^(?:#{1,6}\s*)?第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*$` | 仅"第X章"，无标题名，整行结束 |
| `EMBEDDED_CN_SECTION_IN_LINE` | `\s+(?:#{1,6}\s*)?([一二三四五六七八九十百千万零]+)[、．.]` | 行内嵌入（非行首，前面必须有至少一个空白）的"二、"这类小节标题起始点 |
| `TOC_MARKER_LINE` | `^(?:#{1,6}\s*)?(目\s*录\|CONTENTS\|目\s*次\|图目录\|表目录)\s*$`（大小写不敏感） | 独立成行的目录锚点关键词（"目录"字之间允许空格，兼容英文 CONTENTS） |
| `TRAILING_TOC_TABLE_CELL` | `^(.*\|)\s*(目\s*录\|CONTENTS\|目\s*次)\s*\|\s*$`（大小写不敏感） | 目录锚点被表格末列吞并的形态，如`\| ... \| 总经办 \| 目录 \|` |
| `TOC_CHAPTER_ENTRY_BODY`（字符串，非独立编译） | `第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*[\p{IsIdeographic}A-Za-z、，,]{0,20}\s*[.．·…]{0,10}\s*(?:\d{1,4}\s*[-–—]\s*)?(?:<!--\s*REVIEW:[^>]*-->\s*)?` | 单条目录条目的通用形态：章号+0-20字短题名+可选引导点(0-10个)+可选"N-"页码+可选行内 REVIEW 批注注释 |
| `TOC_CHAPTER_ENTRY` | 同上编译为 `Pattern` | 单条目录条目匹配（`find`语义使用） |
| `TOC_CONTENT_LINE` | `^(?:` + `TOC_CHAPTER_ENTRY_BODY` + `)+$` | 整行由一条或多条 `TOC_CHAPTER_ENTRY_BODY` 拼接而成（PDF 行聚类可能把多条目录条目聚为一行） |
| `TOC_CHAPTER_NUMERAL` | `第\s*([一二三四五六七八九十百千万零\d]+)\s*章` | 提取行内每条目录项的章号数字/中文数字文本（用于跨行去重） |
| `MIN_BARE_TOC_CHAPTER_RUN` | `2` | 裸目录（无引导点/页码）判定所需的目录锚点后连续章条目最少数 |

### 算法：`stripFromMarkdown(markdown)`
1. 空/全空白输入返回空字符串。
2. 统一换行符：`\r\n`→`\n`，`\r`→`\n`。
3. 按 `\n` 切分为行列表（`split(..., -1)` 保留尾部空行）。
4. 调用 `stripLines(lines)`。
5. 用 `\n` 拼接返回。

### 算法：`isChapterTocOnlyBlock(body)`（包内可见）
1. 空/全空白返回 `false`。
2. 按 `\n` 切行，跳过空行；若任一非空行不满足 `isChapterTocLine`，立即返回 `false`。
3. 统计满足的行数 `n`，返回 `n >= MIN_CHAPTER_TOC_LINES_IN_BLOCK`（即 `n >= 1`）。

### 算法：`recoverFirstChapterFromBlock(body)`（包内可见）
1. 空/全空白返回 `null`。
2. 按 `\n` 切行，跳过空行，找到第一条满足 `isChapterTocLine` 的行，调用 `toStructuralChapterHeadingFromTocLine` 并返回其结果。
3. 全部不满足返回 `null`。

### 算法：`applyToFile(markdownPath)`
1. 读取文件全文（UTF-8）。
2. 调用 `stripFromMarkdown` 处理。
3. 写回原文件（UTF-8，覆盖）。

### 算法：`isChapterTocLine(line)`
1. `null` 返回 `false`；`strip()` 后为空返回 `false`。
2. 依次尝试 `CHAPTER_TOC_LINE`、`CHAPTER_TOC_DASH_PAGE`、`CHAPTER_TOC_DASH_PAGE_TRUNCATED`、`CHAPTER_TOC_EMBEDDED_PAGE` 四个模式做**全串匹配**（`matches()`），任一命中即返回 `true`。
3. 全部不命中返回 `false`。

### 算法：`isChapterTocOrphanPageSuffixLine(line)`
1. `null` 返回 `false`。
2. `strip()` 非空后用 `CHAPTER_TOC_ORPHAN_DASH_PAGE` 全串匹配，返回匹配结果。

### 算法：`isStructuralChapterHeading(line)`
1. `null` 返回 `false`。
2. `strip()`，用正则 `^#+\s*` 去掉行首井号前缀，再 `trim()`。
3. 非空 且 `CHAPTER_HEADING` 全串匹配 且 **不是** `isChapterTocLine`，返回 `true`；否则 `false`。

### 算法：`isStructuralCnSectionHeading(line)`（包内可见）
同上，但用 `CN_SECTION_HEADING` 替代 `CHAPTER_HEADING`。

### 算法：`isChapterPrefixOnlyLine(line)`
1. `null` 返回 `false`。
2. `strip()` 后用 `CHAPTER_PREFIX_ONLY` 全串匹配。

### 算法：`isLikelyChapterTitleNameLine(line)`
判定一行是否"像"章节序号下一行独立出现的标题名（如"投标邀请"，无编号前缀）：
1. `null` 返回 `false`。
2. `strip()` 并去掉行首 `#` 前缀、`trim()`。
3. 长度 `< 2` 或 `> 40` 返回 `false`。
4. 若是 `isChapterTocLine` 或 `isChapterPrefixOnlyLine`，返回 `false`。
5. 若是 `isStructuralChapterHeading` 或 `isStructuralCnSectionHeading`，返回 `false`。
6. 若 `HEADING_PREFIX_FRAGMENT` 能在其中 `find()` 到（含有层级标题前缀片段），返回 `false`。
7. 若整行匹配 `^第\s*.*`（仍以"第"开头，说明是没识别成结构标题但仍带"第"字残留），返回 `false`。
8. 若整行包含任意句读终止符号（正则 `.*[。！？；：,.!?;:].*`），返回 `false`。
9. 若整行包含任意数字（正则 `.*\d.*`），返回 `false`。
10. 最终要求整行匹配 `^[\p{IsIdeographic}\p{L}（）()、\s·]{2,40}$`（仅由汉字/任意语言字母/括号/顿号/空白/间隔号组成，2-40 字符），返回该匹配结果。

### 算法：`shouldPreserveInHeaderFooterBand(text)`（包内可见）
用于页眉/页脚重复区剔除逻辑里保护真正的标题碎片：
1. `null`/全空白返回 `false`。
2. `strip()`；若是 `isChapterTocLine`，返回 `false`（目录行不需要保护，本身就该被剔除）。
3. 若是 `isStructuralChapterHeading` 或 `isStructuralCnSectionHeading`，返回 `true`。
4. 若是 `isLikelyChapterTitleNameLine`，返回 `true`。
5. 否则返回 `HEADING_PREFIX_FRAGMENT.find()` 的结果。

### 算法：`splitEmbeddedCnSectionHeadings(line)`（单行版本，重载 1）
把"7.……。 二、申请人……"这类粘连行拆成独立行：
1. `null` 返回 `["" ]`（单元素列表含空串）。
2. `raw = line`；全空白直接返回 `[raw]`。
3. 用 `EMBEDDED_CN_SECTION_IN_LINE` 反复 `find()`，找第一个 `start() > 0`（即不在行首出现）的匹配位置 `splitAt`；找不到则直接返回 `[raw]`。
4. `left = raw.substring(0, splitAt)`，`trim()` 后再用正则 `\s*#{1,6}\s*$` 去掉末尾可能残留的孤立 `#`，再 `trim()`。
5. `right = raw.substring(splitAt)`，`trim()` 后用正则 `^#{1,6}\s*` 去掉行首可能残留的 `#`，再 `trim()`。
6. 构造结果列表：`left` 非空则加入；`right` 非空则**递归**调用本方法处理 `right` 并把结果全部加入（因为 `right` 内可能还含有后续的"三、"等）。
7. 返回结果列表。

### 算法：`splitEmbeddedCnSectionHeadings(lines)`（列表版本，重载 2）
1. `lines` 为空返回空列表。
2. 对每一行调用单行版本，把所有结果依次加入输出列表（`flatMap` 语义）。
3. 对输出列表的每一项调用 `stripOrphanTrailingHash` 做二次清理，就地替换。
4. 返回处理后的列表。

### 算法：`stripOrphanTrailingHash(line)`（包内可见）
清理拆分后残留的"……。 #"（正文与标题之间的孤立 `#`）：
1. `null`/全空白返回 `""`（`line==null` 时）或原样返回（全空白但非 `null` 时返回 `line` 本身，注意 Java 原实现是 `return line == null ? "" : line;`）。
2. `t = strip()`；若整行匹配 `^#{1,6}\s+\S.*`（`#` 后紧跟非空白内容，是正常标题行），原样返回 `line`。
3. 若整行匹配 `.*[。！？；]\s*#\s*$`（以句读符号收尾+孤立 `#`），用正则 `\s*#\s*$` 替换掉这个孤立 `#` 并 `trim()` 返回。
4. 否则原样返回 `line`。

### 算法：`toStructuralChapterHeadingFromTocLine(line)`
从目录行解析出规范章节名（**仅供** `mpp.ChapterTocCatalog` 校验用，不写入正文）：
1. `line` 为 `null` 或不满足 `isChapterTocLine`，返回 `null`。
2. `strip()`，用 `^#{1,6}\s*` 去掉行首 `#`，`trim()`。
3. 用 `CHAPTER_TOC_PAGE_SUFFIX` 替换掉页码后缀（只替换第一次出现），`trim()`。
4. 再用正则 `\.{2,}-\s*$` 兜底去掉可能残留的"引导点+悬空短横"，`trim()`。
5. 用正则 `^(第\s*[一二三四五六七八九十百千万零\d]+\s*章)(\p{IsIdeographic})` 在"章"与紧跟的汉字题名之间插入一个空格（`$1 $2`）。
6. 结果为空返回 `null`，否则返回。

### 算法：`recoverStructuralChapterHeading(line)`（已废弃，包内可见）
直接委托给 `toStructuralChapterHeadingFromTocLine`。移植时可保留为同名兼容函数或直接删除并统一调用点。

### 算法：`stripChapterHeadingTrailingNoiseChar(line)`
剔除章标题粘连的单个 OCR 噪声字符，两种场景互斥处理：
1. `line` 为 `null`/全空白，原样返回。
2. `out = line`。
3. 先尝试 `CHAPTER_HEADING_EMBEDDED_NOISE_CHAR_BEFORE_PAGE_NUM.find()`：命中则用 `replaceAll("$1")` 把整个匹配替换为捕获组 1（即删掉噪声字符与其后被断言吃掉但未捕获的部分——**注意**：Go 移植时，先行断言部分不参与替换内容，只用于校验位置，替换逻辑本身只需把"组1 + 噪声字符"整体替换为"组1"）。
4. 再尝试 `CHAPTER_HEADING_TRAILING_NOISE_CHAR.find()`（在上一步结果 `out` 上）：命中则同样 `replaceAll("$1")`。
5. 返回 `out`。

### 算法：`normalizeGluedStructuralChapterHeading(line)`
把"第 X 章题名"粘连成一行、且**没有**目录页页码后缀的正文标题，规范为"第 X 章 题名"（章与题名间插入空格）：
1. `line` 为 `null`/全空白/或本身是 `isChapterTocLine`，原样返回 `line`。
2. `raw = strip()`。
3. 若行首匹配 `^(#{1,6}\s*)`，记录 `hashPrefix`，并从 `raw` 中去掉这段前缀、`trim()`。
4. 用 `GLUED_CHAPTER_HEADING` 对 `raw` 做全串匹配，不匹配则原样返回 `line`。
5. `body = 捕获组2.strip()`；若 `body` 为空，或 `body` 含数字（`.*\d.*`），原样返回 `line`（说明题名里混了数字，不是纯题名，交由其他逻辑处理）。
6. 若 `CHAPTER_TOC_PAGE_SUFFIX` 能在 `raw` 中 `find()`（说明其实还带页码后缀，不该走这条正文规整分支），原样返回 `line`。
7. 返回 `hashPrefix + 捕获组1.strip() + " " + body`。

### 算法：`splitGluedChapterHeadingFromFollowingMarker(line)`
拆分"第 X 章 短题名"与紧随其后无分隔粘连的下一级标题/列表标记为两行，通过**逐字符扩张探测**定位题名边界：
1. `line` 为 `null`/全空白，返回 `[line 或 ""]`。
2. `t = strip()`；用 `GLUED_CHAPTER_PREFIX` 在 `t` 上 `find()`，要求 `start() == 0`（必须从行首开始），否则返回 `[line]`。
3. `chapter = 捕获组1.strip()`；`afterChapter = m.end()`（章前缀后的位置）。
4. `maxLen = min(MAX_GLUED_TITLE_LEN=20, t.length() - afterChapter)`。
5. 对 `len` 从 1 到 `maxLen` 循环：
   a. 取字符 `c = t.charAt(afterChapter + len - 1)`；若 `c` 不满足 `GLUED_TITLE_CHAR`（即不是汉字/括号/间隔号），`break` 跳出循环（题名字符集边界已到，后续不可能再是题名）。
   b. `title = t.substring(afterChapter, afterChapter+len).strip()`；`remainder = t.substring(afterChapter+len).strip()`。
   c. 若 `title` 或 `remainder` 为空，`continue`（跳过本次，继续扩张）。
   d. 调用 `mpp.MarkdownTitlePattern.matchFirst(remainder)`，若非 `null`（即 `remainder` 命中某种标题/列表标记形态，如"第…条/节"、"一、"、"（一）"、"1."、"1.1"、"I."、"A."），说明找到了正确的题名边界，返回 `[chapter + " " + title, remainder]`。
6. 循环结束仍未命中任何标记形态，返回 `[line]`（原样，不拆分）。

### 算法：`stripLines(lines)`
1. `lines` 为空返回空列表。
2. 先调用 `stripBareTocChapterRuns(lines)` 处理"裸目录"（无引导点/页码的 OCR 目录），再对结果调用私有的 `stripLinesInternal` 处理常规目录行剔除。

### 算法：`stripBareTocChapterRuns(lines)`（包内可见）
OCR 等场景丢失引导点/页码，目录条目退化为纯"第 X 章 短题名"行，靠"目录"锚点 + 后续连续裸章标题行数判定：
1. `lines` 为 `null`/空，原样返回。
2. 用双指针 `i` 遍历：
   a. 取当前行 `raw`，`trimmed = strip()`。
   b. 判断是否为锚点：
      - 若 `TOC_MARKER_LINE` 全串匹配 `trimmed`，`isAnchor=true`。
      - 否则若 `TRAILING_TOC_TABLE_CELL` 全串匹配，`isAnchor=true`，并记录 `anchorReplacement = 捕获组1 + "  |"`（把表格末列的"目录"替换为空单元格，保留表格列数与格式，其余列不变）。
   c. 非锚点：原样加入输出，`i++`，继续下一轮。
   d. 是锚点：从 `j = i+1` 开始向后扫描：
      - 空行跳过（`j++`，不计数、不中断）。
      - 若 `TOC_CONTENT_LINE` 全串匹配当前行 `s`：
        - 用 `TOC_CHAPTER_NUMERAL` 提取该行内出现的所有章号文本到 `numerals` 列表。
        - 若 `numerals` 中任一编号已存在于 `seenChapterNumerals` 集合（说明本行已经是重复出现的正文标题，已越过目录区），`break` 停止扫描（本行**不计入**目录区，consumeUntil 不更新到本行）。
        - 否则把 `numerals` 全部加入 `seenChapterNumerals`；`chapterHits += numerals.size()`；`j++`；`consumeUntil = j`（即本行计入待剔除区间）。
      - 否则（非目录内容行）`break`。
   e. 若 `chapterHits >= MIN_BARE_TOC_CHAPTER_RUN`（即 `>= 2`）：
      - 若 `anchorReplacement != null`，把它加入输出（保留改写后的锚点行/表格行）；否则该锚点行本身**不加入**输出（被剔除）。
      - `i = consumeUntil`（跳过整个目录区）。
      - `continue` 到下一轮外层循环。
   f. 否则（未达阈值）：原样加入锚点行 `raw` 到输出，`i++`。
3. 返回处理后的行列表。

### 算法：`stripLinesInternal(lines)`（私有）
在代码围栏（` ``` `）外剔除常规目录行及其后续附属的孤立页码行，并对非围栏、非目录的普通行做 `normalizeGluedStructuralChapterHeading` 规整：
1. `out = []`；`inFence = false`；`i = 0`。
2. 遍历 `lines`：
   a. `trimmed = line.strip()`（`line` 为 `null` 时视为空串）；若以 `` ``` `` 开头，翻转 `inFence`，原样加入 `out`，`i++`，继续。
   b. 若不在围栏内，且 `isChapterTocLine(trimmed)`：
      - 从 `j=i+1` 向后扫描：遇到 `` ``` `` 开头行则 `break`（不吞入代码块）；空行跳过（`j++`）；若仍是 `isChapterTocLine` 或 `isChapterTocOrphanPageSuffixLine`，`j++` 继续吞并；否则 `break`。
      - `i = j`（整段目录行+孤立页码行被跳过，不写入 `out`）；`continue`。
   c. 若不在围栏内，且 `isChapterTocOrphanPageSuffixLine(trimmed)`（孤立于目录区之外单独出现的页码残留行），跳过该行（`i++`，不写入 `out`），`continue`。
   d. 若不在围栏内（且未命中上述两种），对 `line` 调用 `normalizeGluedStructuralChapterHeading` 并写入 `out`；若在围栏内，原样写入 `out`。
   e. `i++`。
3. 返回 `out`。

### 调用方
`PdfToMarkdown.java` 中广泛使用：
- 段落/块级处理（约 1078-1109 行）：`isChapterTocOnlyBlock` 判断整段是否纯目录内容，命中则整段调用 `stripFromMarkdown` 二次清理；`isChapterTocOrphanPageSuffixLine` 用于判断残留页码行是否也应随目录段一起处理。
- 标题识别主链路（约 1142、1196-1197、2524-2551、2756-2757 行）：`isChapterTocLine`/`isChapterPrefixOnlyLine`/`isStructuralChapterHeading`/`isLikelyChapterTitleNameLine` 组合用于判断两个相邻文本块是否应合并为"章号+题名"一个标题单元，以及是否允许把"第 X 章"识别为层级标题。
- 页眉页脚剔除（约 2962、3930、3974 行）：`shouldPreserveInHeaderFooterBand` 用于避免页眉页脚重复行剔除逻辑误伤真正的章节标题/题名碎片。
- 章节前缀识别（约 3412-3432 行）：结合 `TITLE_CHAPTER` 判断粘连标题的题名部分（`isLikelyChapterTitleNameLine`）。
- 行拆分（约 4304 行）：`splitEmbeddedCnSectionHeadings` 用于把 PDF 抽取时粘连在一起的正文段与中文小节标题拆开。

---

## HeadingLevelPrefixHeuristics

### 职责
在 `mpp.HeadingReadingOrderValidator` 按阅读顺序完成嵌套层级校验之后，进一步保证"同一 Markdown 层级上所有标题必须使用同一种前缀模式"（如二级全部是"第 X 条"，不能一部分是"第 X 条"一部分是"（一）"）。不符合的行要么改到其自然层级，要么降级为正文。适用于一级到六级标题（`MAX_HEADING_LEVEL = 6`）。

### 常量与正则

| 名称 | 值 | 说明 |
|---|---|---|
| `MAX_HEADING_LEVEL` | `6` | 支持的最大标题层级 |
| `PATTERN_CANONICAL_PRIORITY`（Map） | `TITLE_CHAPTER_ONE=1, TITLE_CHAPTER_TOW=2, TITLE_CHAPTER_THREE=3, TITLE_CHAPTER_FOUR=4, TITLE_CHAPTER_FIVE=5, TITLE_CN_PAREN=10, TITLE_CN_NUM=11, TITLE_NUM_TOW=20, TITLE_NUM_THREE=21, TITLE_NUM_FOUR=22, TITLE_NUM_FIVE=23, TITLE_NUM_DOT=30, TITLE_NUM_DUNHAO=31, TITLE_NUM_SUFFIX=32, TITLE_NUM_PAREN=33, TITLE_ROMAN=40, TITLE_ALPHA=41` | 选定同级 canonical 前缀时，计数并列的优先级参考表（数值越小优先级越高） |

`PREFIX_DEFS`（有序列表，**匹配顺序即列表顺序，先命中者生效**）：

| Key | 正则（原样） | 说明（含 Go 断言处理提示） |
|---|---|---|
| `TITLE_CHAPTER_ONE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*章.*` | "第X章"起始 |
| `TITLE_CHAPTER_TOW` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*节.*` | "第X节"起始 |
| `TITLE_CHAPTER_THREE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*纲.*` | "第X纲"起始 |
| `TITLE_CHAPTER_FOUR` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*目.*` | "第X目"起始 |
| `TITLE_CHAPTER_FIVE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*条.*` | "第X条"起始 |
| `TITLE_CN_PAREN` | `^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）].*` | "（一）"起始 |
| `TITLE_CN_NUM` | `^([一二三四五六七八九十百千万]+)[、．.\s].*` | "一、"起始 |
| `TITLE_NUM_FIVE` | `^(\d+(?:\.\d+){4})(?:[.．])?(?![.．\d-])\s*.*` | 五级数字编号如"1.2.3.4.5"，含负向先行（见兼容性表） |
| `TITLE_NUM_FOUR` | `^(\d+(?:\.\d+){3})(?:[.．])?(?![.．\d-])\s*.*` | 四级数字编号，含负向先行 |
| `TITLE_NUM_THREE` | `^(\d+(?:\.\d+){2})(?:[.．])?(?![.．\d-])\s*.*` | 三级数字编号，含负向先行 |
| `TITLE_NUM_TOW` | `^(\d+(?:\.\d+){1})(?:[.．])?(?![.．\d-])\s*.*` | 二级数字编号如"1.2"，含负向先行 |
| `TITLE_NUM_DOT` | `^(\d+)[.．](?!\d|-)\s*.*` | "1."单级编号，含负向先行 |
| `TITLE_NUM_DUNHAO` | `^(\d+)、\s*.*` | "1、"顿号编号 |
| `TITLE_NUM_SUFFIX` | `^(\d+)[)）]\s*.*` | "1)"后括号编号 |
| `TITLE_NUM_PAREN` | `^[（(]\s*(\d+)\s*[)）]\s*.*` | "(1)"全括号编号 |
| `TITLE_ROMAN` | `^([IVXLCDM]+)\.\s*.*`（大小写不敏感） | 罗马数字编号 |
| `TITLE_ALPHA` | `^([A-Za-z])[.．]\s*.*` | 单字母编号 "A." |

注意：**该匹配顺序里数字多级模式的顺序是 FIVE→FOUR→THREE→TOW→DOT**（长模式在前），保证贪婪最长匹配优先命中。

### 算法：`naturalLevelForTitle(title)`
1. `classifyPrefixKey(title)` 得到 `key`。
2. `key == null` 返回 `0`；否则返回 `naturalLevelForPatternKey(key)`。

### 算法：`naturalLevelForPatternKey(patternKey)`
按 `switch` 映射：
- `null` → `0`
- `TITLE_CHAPTER_ONE` → `1`
- `TITLE_CHAPTER_TOW`/`TITLE_CHAPTER_THREE`/`TITLE_CHAPTER_FOUR`/`TITLE_CHAPTER_FIVE` → `2`
- `TITLE_CN_NUM`/`TITLE_CN_PAREN` → `3`
- `TITLE_NUM_TOW` → `3`
- `TITLE_NUM_DOT`/`TITLE_NUM_DUNHAO`/`TITLE_NUM_SUFFIX`/`TITLE_NUM_PAREN`/`TITLE_ROMAN`/`TITLE_ALPHA`/`TITLE_NUM_THREE`/`TITLE_NUM_FOUR`/`TITLE_NUM_FIVE` → `4`
- 其他（含未知 key） → `0`

### 算法：`classifyPrefixKey(title)`
1. `title` 为空/全空白返回 `null`。
2. 调用 `classifyPrefixKeyOnNormalized(normalizeForHeadingPrefixMatch(title))`。

### 算法：`normalizeForHeadingPrefixMatch(title)`
1. `normalized = mpp.MarkdownPipelineLineUtils.normalizeLine(title)`（外部依赖，语义为标准的行文本归一化，不在本文档范围）。
2. `ifStripped = stripLeadingPriorityMarkers(normalized)`。
3. **仅当** `ifStripped != normalized`（确实剥离了什么）**且** `isHierarchyTitlePrefix(ifStripped)`（剥离后能识别为层级标题前缀）时，返回 `ifStripped`；否则返回 `normalized`（不剥离，保留原文，避免误伤普通带 ★ 的正文/列表行）。

### 算法：`stripLeadingPriorityMarkers(normalized)`（私有）
1. 循环：`prev = t`；用 `mpp.MarkdownPipelineLineUtils.LEADING_HEADING_PRIORITY_MARKERS`（外部正则，语义为行首优先级标记如 ★）替换首次匹配为空；`t = stripEdgeWhitespace(t)`（外部工具函数）；直到 `t == prev`（不动点收敛，即反复剥离直到没有更多标记可去）。
2. 返回 `t`。

### 算法：`classifyPrefixKeyOnNormalized(norm)`（私有）
1. `norm` 空/全空白返回 `null`。
2. 按 `PREFIX_DEFS` **列表顺序**依次全串匹配，返回第一个命中的 `key`。
3. 全部不命中返回 `null`。

### 算法：`isHierarchyTitlePrefix(norm)`（私有）
1. `key = classifyPrefixKeyOnNormalized(norm)`。
2. `key != null` 且 `!isListLikePatternKey(key)`。

### 算法：`isListLikePatternKey(key)`（私有）
`switch`：`TITLE_NUM_DOT`/`TITLE_NUM_DUNHAO`/`TITLE_NUM_SUFFIX`/`TITLE_NUM_PAREN`/`TITLE_ROMAN`/`TITLE_ALPHA`/`TITLE_NUM_TOW`/`TITLE_NUM_THREE`/`TITLE_NUM_FOUR`/`TITLE_NUM_FIVE` → `true`；其他 → `false`。（即：只有"第X章/节/纲/目/条""一、""（一）"这类才算"层级标题前缀"，纯数字/罗马/字母编号被归为"列举式"，不算层级标题前缀。）

### 算法：`isLeadingPriorityMarkerHierarchyHeading(line)`
1. `normalized = mpp.MarkdownPipelineLineUtils.normalizeLine(line)`。
2. `ifStripped = stripLeadingPriorityMarkers(normalized)`。
3. 返回 `ifStripped != normalized && isHierarchyTitlePrefix(ifStripped)`（即：判断这一行是否是"行首 ★ 等标记 + 层级标题前缀"的组合，如"★二、商务要求"）。

### 算法：`applyLevelPrefixConsistency(lines, hits)`
主入口，输入某一版本的标题命中列表 `hits`（每个含 `level`/`titleRaw`/`lineIndex`），对 `lines` 就地做降级修改，返回调整后的 `hits`：
1. `lines`/`hits` 任一为 `null`，或 `hits` 为空，原样返回。
2. `working = hits` 的可变拷贝；`changed = true`。
3. **外层 while(changed)** 循环，直到一轮内没有任何调整：
   a. `changed = false`。
   b. 对 `level` 从 `1` 到 `6`：
      - `atLevel` = `working` 中 `level == lv` 的子集；数量 `< 2` 跳过（同级不足 2 条无需比较一致性）。
      - `canonical = pickCanonicalPatternForLevel(atLevel, lv)`；为空跳过。
      - `required = canonical.get()`。
      - 判断"层级是否不一致"：统计 `atLevel` 中 `classifyPrefixKey(titleRaw)` 的**去重非空**结果数，`> 1` 才算不一致；否则跳过本层级。
      - 若不一致，构造 `next` 新列表：
        - 遍历 `working`：非本层级的直接保留。
        - 本层级的：`pk = classifyPrefixKey(titleRaw)`；`pk.equals(required)` 直接保留（已是 canonical）。
        - 否则：`natural = naturalLevelForPatternKey(pk)`。
          - 若 `pk == "TITLE_CHAPTER_ONE"`：**始终保留，不降级、不改级**（结构章标题优先级最高，不参与同级前缀连坐降级；正文列举误判由 `shouldSuppressHeading`/scope 单独处理）。
          - 否则若 `natural > 0 && natural != lv`：把该 `hit.level` **就地改**为 `natural`（改到其自然层级），保留该 hit，`changed = true`。
          - 否则若 `natural > 0 && natural == lv && keepCanonicalMismatchAtLevel(pk, required, lv)`：保留（特殊豁免，见下）。
          - 否则：调用 `demoteHeadingLine(lines, h)` 把该行在 `lines` 中降级为正文（去掉 `#` 前缀），**不加入** `next`（即从 hits 中剔除），`changed = true`。
      - `working = next`。
4. 循环结束后按 `lineIndex` 排序 `working` 并返回。

### 算法：`pickCanonicalPatternForLevel(atLevel, markdownLevel)`（私有）
1. 统计每种 `pk` 的出现次数 `counts` 与首次出现行号 `firstLine`（取最小值）。
2. `counts` 为空返回 `Optional.empty()`。
3. **特殊规则**：若 `markdownLevel == 1` 且同时存在 `TITLE_CHAPTER_ONE` 与（`TITLE_CN_NUM` 或 `TITLE_CN_PAREN`）之一，**强制**选 `TITLE_CHAPTER_ONE` 为 canonical（章优先级高于计数，"一、"等会被挪到自然层级）。
4. 否则：在 `counts.keySet()` 中按以下比较器取最大值：
   - 主排序键：`counts.get(pk)` 降序（次数多者优先）。
   - 次排序键：`-patternCanonicalPriority(pk)` 即优先级数值**越小越优先**（因为取的是负值的最大，负值越大原值越小）。
   - 第三排序键：`-firstLine.get(pk)`，即**行号越大越优先**（等价于并列时取"最后出现"的模式而非最先出现的，需注意这是反直觉的一点，移植时不要写反）。

### 算法：`patternCanonicalPriority(patternKey)`（私有）
`PATTERN_CANONICAL_PRIORITY.getOrDefault(patternKey, 100)`。

### 算法：`keepCanonicalMismatchAtLevel(pk, required, level)`（私有）
豁免规则：仅当 `pk == "TITLE_CHAPTER_ONE"` 且 `level == 1` 且 `required` 非空，并且 `required` 是以下之一时返回 `true`：`required.startsWith("TITLE_NUM_")`、`"TITLE_NUM_DOT".equals(required)`（与前一条件重叠，冗余判断，按原样保留）、`"TITLE_CN_NUM".equals(required)`、`"TITLE_CN_PAREN".equals(required)`。其余情况返回 `false`。

### 算法：`demoteHeadingLine(lines, hit)`（私有）
1. `hit`/`lines` 为 `null` 直接返回。
2. `i = hit.lineIndex`；越界直接返回。
3. `trimmed = lines.get(i).strip()`；用正则 `^(#{1,6})\s*(.+)$` 全串匹配；命中则 `lines.set(i, 捕获组2.strip())`（去掉 `#` 前缀，保留标题正文，就地修改 `lines`）。

### 调用方
`PdfToMarkdown.java`：
- `detectDisqualifiedPatternDemoteBlockIds`（约 321 行）：`classifyPrefixKey(lines.get(i))` 用来判定每个候选行的前缀模式 key，配合 `HeadingPatternQualityHeuristics` 的坏 key 集合批量剔除。
- 加粗/标记行处理（约 3593 行）：`isLeadingPriorityMarkerHierarchyHeading(t)` 用于避免把"★二、商务要求"这类行错误当作普通加粗正文处理。

---

## HeadingPatternQualityHeuristics

### 职责
"同前缀模式的质量联动"：若某个 `pattern_key`（如 `TITLE_NUM_DOT`）下**任一**行明显不符合标题质量标准（如长段落、密集标点、以冒号收束等），则该模式在**全文**范围内一律不得作为层级标题输出（"连坐"机制）。另外还处理"混识别"场景：同一模式下部分行已被识别为标题（带 `#`）、部分未被识别，视为矛盾，整模式连坐降级。本类在 `mpp.MarkdownHeadingStage` 标题定稿链结束后执行最终否决；推断阶段可提前用于跳过候选。

### 常量与正则

| 名称 | 值 | 说明 |
|---|---|---|
| `PREFIX_BODY_LIKE_MIN_LEN` | `35` | 判定"像正文句子"所需的最小非空白字符数 |
| `PREFIX_BODY_LIKE_MIN_PUNCT` | `2` | 判定"像正文句子"所需的最小句读标点数 |
| `PREFIX_BODY_LIKE_MIN_PUNCT_DENSITY` | `0.015` | 判定"像正文句子"所需的最小标点密度（标点数/非空白字符数） |
| `HARD_MAX_NON_SPACE_LEN` | `80` | 硬性最大非空白字符数，超过一律判定为非标题 |
| `INCOMPLETE_TAIL_MIN_LEN` | `40` | 以"及"字结尾且视为"未完成的长列举"所需的最小非空白长度 |
| `CN_PAREN_COLON_GUIDE` | `^[（(]\s*[一二三四五六七八九十百千万]+\s*[)）][^：:]{0,40}[：:].*` | "（一）……：" 形式，括号中文序号后跟至多 40 个非冒号字符再以冒号收束——引导性文字，非标题 |
| `SENTENCE_BOUNDARY_PUNCT` | `[。；！？!?]` | 句子终止标点字符集 |
| `CLAUSE_DENSE_PUNCT` | `[，、,;；]` | 分句/列举标点字符集 |

（`loadMaxHeadingLength()` 默认值 `80`，可被 classpath `config.properties` 或系统属性 `config.file` 指定的外部配置文件的 `pdf2md.maxHeadingLength` 覆盖，最小钳制为 `8`；解析失败或未配置时回退默认值 `80`。此为运行时可配置项，Go 移植应保留同等配置能力，如项目已有统一配置读取机制则复用之。）

### 算法：`detectDisqualifiedPatternKeys(lines)`
扫描全文，收集应在全文禁用的 pattern key 集合：
1. `lines` 为空返回空集合。
2. `maxLen = loadMaxHeadingLength()`；`hasFailure` 为 `Map<String,Boolean>`。
3. 对每一行 `i`：
   a. `norm = normalizeForScan(lines.get(i))`；空则跳过。
   b. `pk = HeadingLevelPrefixHeuristics.classifyPrefixKey(norm)`；`null` 则跳过。
   c. 若 `!PdfToMarkdown.isHeadingByRegex(norm)`（不满足标题正则的基本形态），跳过（不参与质量判定，因为它本来就不会被当候选）。
   d. 若 `isColonTerminatedSectionFieldLabel(norm)`（是"1.1 适用范围："这种冒号收束字段标签），跳过（这行本身不进标题，但**不**连坐禁用同前缀模式）。
   e. 若 `overlongPrefixTitleOnlyFailure(norm, maxLen)`（仅因超长/非独立成行才否决，见下），跳过（同样不连坐）。
   f. 否则若 `clearlyFailsHeadingQuality(norm, maxLen)` 为真，`hasFailure.put(pk, true)`。
4. 返回 `hasFailure` 的 key 集合（为空则返回空集合）。

### 算法：`detectMixedRecognitionPatternKeys(lines, headingLineIndexes)`（重载 1）
委托给重载 2，`excludedFromUnrecognizedCount = Set.of()`。

### 算法：`detectMixedRecognitionPatternKeys(lines, headingLineIndexes, excludedFromUnrecognizedCount)`（重载 2）
1. `lines` 为空返回空集合。
2. `recognized`/`excluded` 分别取参数或空集合默认值。
3. `hasRecognized`/`hasUnrecognized` 为 `Map<String,Boolean>`；`inFence=false`。
4. 逐行遍历（跳过代码围栏内的行，围栏检测同"``` "开头翻转 `inFence`）：
   a. `norm = normalizeForScan(raw)`；若 `!countsForMixedRecognitionCandidate(norm)`（见下），跳过。
   b. 若该行已在 `recognized` 中，**且** `clearlyFailsHeadingQuality(norm)` 为真，跳过（已识别但明显质量不合格的行不计入"已识别"这一侧，避免其自身的质量问题误判为"模式混识别"）。
   c. `pk = classifyPrefixKey(norm)`；`null` 跳过。
   d. 若该行在 `recognized` 中：若也在 `excluded` 中跳过；否则 `hasRecognized.put(pk, true)`。
   e. 否则（未识别）：若不在 `excluded` 中 **且** `!clearlyFailsHeadingQuality(norm)`，`hasUnrecognized.put(pk, true)`。
5. 取 `hasRecognized` 与 `hasUnrecognized` 的 key 交集，作为 `mixed` 结果集合返回（为空返回空集合）。

### 算法：`countsForMixedRecognitionCandidate(norm)`（私有）
判断一行是否"够格"参与混识别统计：
1. 空/全空白返回 `false`。
2. `!PdfToMarkdown.isHeadingByRegex(norm)` 返回 `false`。
3. `ChapterTocLineRemover.isChapterTocLine(norm)` 为真返回 `false`。
4. `MarkdownStructureRules.isChapterTableOfContentsEntry(norm)` 为真返回 `false`。
5. `ChapterReferenceHeuristics.isBodyChapterReference(norm)` 为真返回 `false`。
6. `isColonTerminatedSectionFieldLabel(norm)` 为真返回 `false`。
7. `MarkdownStructureRules.isOrderedListItemLine(norm)` 为真返回 `false`。
8. `pk = classifyPrefixKey(norm)`；`null` 返回 `false`。
9. 若 `ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(pk, norm)` 为真，返回 `true`（短语式节标题编号行直接算数）。
10. 否则按 `switch(pk)`：
    - `TITLE_CN_NUM`/`TITLE_CN_PAREN`/`TITLE_ROMAN`/`TITLE_ALPHA` → 返回 `HeadingSuppressHeuristics.isStandaloneHeadingLine(norm)`（必须独立成行）。
    - `TITLE_CHAPTER_ONE`/`TITLE_CHAPTER_TOW`/`TITLE_CHAPTER_THREE`/`TITLE_CHAPTER_FOUR`/`TITLE_CHAPTER_FIVE` → 返回 `true`。
    - 其他 → 返回 `false`。

### 算法：`buildInferDisqualifiedPatternKeys(lines, existingHeadings)`（重载 1）
委托重载 2，`excludedFromMixedCount = Set.of()`。

### 算法：`buildInferDisqualifiedPatternKeys(lines, existingHeadings, excludedFromMixedCount)`（重载 2）
1. `keys = detectDisqualifiedPatternKeys(lines)` 的可变拷贝。
2. `existingLines` = `existingHeadings` 中每个 `lineIndex >= 0` 的集合。
3. `keys.addAll(detectMixedRecognitionPatternKeys(lines, existingLines, excludedFromMixedCount))`。
4. 返回合并后的 `keys`。

### 算法：`demoteHitsForMixedRecognition(lines, hits)`
1. `hits` 为空返回空列表。
2. 收集 `hits` 的所有 `lineIndex` 到 `hitLines`。
3. `mixed = detectMixedRecognitionPatternKeys(lines, hitLines)`。
4. 返回 `filterHitsAndDemoteLines(lines, hits, mixed)`。

### 算法：`filterHitsAndDemoteLines(lines, hits, disqualifiedPatternKeys)`（重载 1）
委托重载 2，`catalog = null`。

### 算法：`filterHitsAndDemoteLines(lines, hits, disqualifiedPatternKeys, catalog)`（重载 2）
1. `disqualifiedPatternKeys` 为空，原样返回 `hits`。
2. 若 `lines` 非空，调用 `demoteDisqualifiedPatternLines(lines, disqualifiedPatternKeys, catalog)` 就地降级行文本。
3. `hits` 为空返回空列表。
4. 遍历 `hits`：`pk = classifyPrefixKey(stripHashes(h.titleRaw))`；若 `pk` 非空且在坏集合中：
   - 若 `catalog != null` 且 `mpp.ChapterTocHeadingValidator.matchesAnyCatalogChapter(h.titleRaw, catalog)`（即这条标题虽被判定为坏模式，但能在目录目录已校验过的章节清单中找到匹配），**仍保留**该 hit。
   - 否则该 hit 被剔除（不加入结果）。
   - 其余（`pk` 为空或不在坏集合）的 hit 直接保留。
5. 返回保留下来的 `hits` 子集。

### 算法：`demoteDisqualifiedPatternLines(lines, disqualifiedPatternKeys)`（重载 1）
委托重载 2，`catalog = null`。

### 算法：`demoteDisqualifiedPatternLines(lines, disqualifiedPatternKeys, catalog)`（重载 2）
就地把 `lines` 中命中坏模式的 `#` 标题行去掉 `#` 前缀：
1. 任一参数为空/空集合，直接返回（不做任何事）。
2. `inFence=false`，遍历每一行（跳过代码围栏内的行）：
   a. `norm = normalizeForScan(raw)`；空跳过。
   b. `pk = classifyPrefixKey(norm)`；为空或不在坏集合中，跳过。
   c. 若 `catalog != null` 且 `matchesAnyCatalogChapter(norm, catalog)`，跳过（目录已验证过的章节标题不受连坐影响）。
   d. 若 `trimmed`（原行 `strip()` 结果）以 `#` 开头：用正则 `^(#{1,6})\s*(.+)$` 匹配，命中则 `lines.set(i, 捕获组2.strip())`（去掉 `#` 前缀）。

### 算法：`clearlyFailsHeadingQuality(text)`（重载 1）
委托重载 2，`maxHeadingLength = loadMaxHeadingLength()`。

### 算法：`clearlyFailsHeadingQuality(text, maxHeadingLength)`（重载 2）
判定一行是否"明显不符合层级标题标准"，按顺序检查（**任一命中即返回 `true`**）：
1. `text` 空/全空白返回 `false`（空行本身不算"不合格"，是无关行）。
2. `t = stripHashes(text).strip()`；空返回 `false`。
3. `ChapterTocLineRemover.isChapterTocLine(t)` 为真，返回 `false`（目录行不算标题质量问题，走单独逻辑）。
4. `MarkdownStructureRules.isChapterTableOfContentsEntry(t)` 为真，返回 `false`（同上）。
5. `HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead(t)` 为真 → `true`。
6. `HeadingSuppressHeuristics.looksLikeCnArticleBodySentence(t)` 为真 → `true`。
7. `t` 以"："或 `:` 结尾 → `true`（冒号收束的引导句/字段标签本身不是完整标题）。
8. `CN_PAREN_COLON_GUIDE.matches(t)` → `true`。
9. `hasMiddleChinesePeriod(t)`（句号出现在**非末尾**位置，说明一行里塞了不止一句话）→ `true`。
10. `countNonSpaceChars(t) > HARD_MAX_NON_SPACE_LEN(80)` → `true`（硬性长度上限，与 `maxHeadingLength` 参数无关）。
11. `t.length() > maxHeadingLength && countSentenceEnds(t) >= 1` → `true`（超过可配置长度且含至少一个句子终止符）。
12. `t.length() > maxHeadingLength * 2` → `true`（远超阈值，即使无句读也判不合格）。
13. `t.length() > maxHeadingLength && countClausePunct(t) >= 4` → `true`（超阈值且分句标点密集，≥4 个）。
14. `isBodyLikeHeadingSentence(t)`（见下）→ `true`。
15. `t` 以"及"结尾 且 `countNonSpaceChars(t) >= INCOMPLETE_TAIL_MIN_LEN(40)` → `true`（未完成列举的长句）。
16. `!HeadingSuppressHeuristics.isStandaloneHeadingLine(t)` 且 `HeadingLevelPrefixHeuristics.classifyPrefixKey(t) != null` 且 `countNonSpaceChars(t) > maxHeadingLength` → `true`（非独立成行、有编号前缀、且超长）。
17. 以上全部不命中，返回 `false`。

### 算法：`isPatternDisqualified(titleRaw, disqualifiedPatternKeys)`
1. `disqualifiedPatternKeys` 为空返回 `false`。
2. `pk = classifyPrefixKey(stripHashes(titleRaw))`；返回 `pk != null && disqualifiedPatternKeys.contains(pk)`。

### 算法：`isLineDisqualifiedForHeadingMerge(line, disqualifiedPatternKeys)`
同上，但对 `normalizeForScan(line)` 而非 `stripHashes(titleRaw)` 分类。用于"定稿后正文合并阶段"判断某行是否已经因模式否决而不再能充当标题挡板（阻止合并的边界）。

### 算法：`overlongPrefixTitleOnlyFailure(norm, maxLen)`（私有）
判断一行是否**仅仅因为**"超长/非独立成行"这一条规则而被判不合格（用来把这种情况从"连坐"判定里摘出去，不影响同前缀的短标题行）：
1. `!clearlyFailsHeadingQuality(norm, maxLen)` → `false`（本身就合格，不适用本函数语境）。
2. `t = stripHashes(norm).strip()`。
3. 若命中以下任一"实质性质量问题"（不是单纯超长问题）：以冒号/"："结尾、`CN_PAREN_COLON_GUIDE` 匹配、`hasMiddleChinesePeriod`、`isBodyLikeHeadingSentence`、`looksLikeCnArticleBodyParagraphLead`、`looksLikeCnArticleBodySentence`，均返回 `false`（说明不合格原因不止是"超长"，不适用摘出规则）。
4. 否则返回 `HeadingLevelPrefixHeuristics.classifyPrefixKey(t) != null`（即：唯一的不合格原因是长度/独立性问题，且这行确实有可分类的前缀模式）。

### 算法：`isColonTerminatedSectionFieldLabel(norm)`
1. `norm` 空/全空白返回 `false`。
2. `t = stripHashes(norm).strip()`；不以"："或 `:` 结尾返回 `false`。
3. `pk = classifyPrefixKey(t)`；为空返回 `false`。
4. 按 `switch(pk)`：`TITLE_NUM_TOW`/`TITLE_NUM_THREE`/`TITLE_NUM_FOUR`/`TITLE_NUM_FIVE`/`TITLE_NUM_DOT`/`TITLE_NUM_DUNHAO`/`TITLE_NUM_SUFFIX`/`TITLE_NUM_PAREN` → `true`；其他 → `false`（即仅限数字类编号才算"字段标签"，中文序号/章节类不算）。

### 算法：`isBodyLikeHeadingSentence(text)`（私有）
1. `nonSpaceLen = countNonSpaceChars(text)`；`> HARD_MAX_NON_SPACE_LEN(80)` 直接 `true`。
2. `< PREFIX_BODY_LIKE_MIN_LEN(35)` 直接 `false`（太短不构成"像句子"）。
3. `punct = countSentencePunctuation(text)`；`< PREFIX_BODY_LIKE_MIN_PUNCT(2)` 返回 `false`。
4. `density = punct / max(1, nonSpaceLen)`；返回 `density >= PREFIX_BODY_LIKE_MIN_PUNCT_DENSITY(0.015)`。

### 算法：`hasMiddleChinesePeriod(text)`（私有）
`idx = text.indexOf('。')`；返回 `idx >= 0 && idx < text.length()-1`（句号存在且不在最后一个字符位置）。

### 算法：`countSentenceEnds` / `countClausePunct`（私有）
分别用 `SENTENCE_BOUNDARY_PUNCT` / `CLAUSE_DENSE_PUNCT` 正则 `find()` 循环计数出现次数。

### 算法：`countSentencePunctuation(text)`（私有）
逐字符遍历，命中字符集 `"，。；：、,.!?;:"`（用 `String.indexOf(ch)`）计数。

### 算法：`countNonSpaceChars(text)`（私有）
逐字符遍历，`!Character.isWhitespace(ch)` 计数。

### 算法：`normalizeForScan(raw)`（私有）
1. `raw` 为 `null` 返回 `""`。
2. `t = strip()`；若以 `#` 开头，用 `^#{1,6}\s*` 去掉前缀并 `strip()`。
3. 若同时以 `**` 开头和结尾且长度 `>= 4`，去掉首尾各 2 个字符的 `**`（去粗体标记）并 `strip()`。
4. 把全角空格 ` `（**注意**：原文写的是 ` ` 即 NBSP，不是 `　` 全角空格，两者不同，移植时不要混淆）替换为普通空格，再 `strip()` 返回。

### 算法：`stripHashes(line)`（私有）
委托 `HeadingSuppressHeuristics.stripHashes(line)`。

### 算法：`loadMaxHeadingLength()`（私有）
1. `def = 80`。
2. 先尝试从 classpath 资源 `config.properties` 加载 `Properties`（读取失败静默忽略，保留默认）。
3. 再检查系统属性 `config.file`（默认值字符串 `"config.properties"`）指向的外部文件路径是否存在，存在则加载并 `putAll` 覆盖（外部文件优先级高于 classpath 资源）。
4. 读取键 `pdf2md.maxHeadingLength`；为空返回 `def`。
5. 尝试 `Integer.parseInt`，解析成功则 `Math.max(8, 解析值)`（钳制最小值 8）；解析失败返回 `def`。

（Go 移植提示：这是一个"运行时可配置项"，具体加载机制应对齐 wiki-brain 现有配置体系，而非照抄 Java 的 classpath+外部文件双源合并逻辑——这属于配置基础设施问题，不属于本文档的启发式算法范围，建议实现者与调用方模块商定统一的配置传入方式。）

### 算法：`detectLineIndexesToDemoteAsNonHeading(lines, isCurrentlyHeading)`
供 Word 批处理路径使用（本文档范围外的调用方，仅记录）：
1. `bad = detectDisqualifiedPatternKeys(lines)`；为空返回空集合。
2. 遍历所有行，`isCurrentlyHeading.test(i)` 为 `true` 且该行 `pk` 在 `bad` 中，加入返回集合。

### 调用方
`PdfToMarkdown.java`（约 316 行）：`detectDisqualifiedPatternDemoteBlockIds` 调用 `detectDisqualifiedPatternKeys(lines)` 得到坏 key 集合，再与自身维护的 `PATTERN_DEMOTE_INCLUDED_KEYS`（PdfToMarkdown 内部常量，允许参与"按模式批量降级"的 key 白名单，本文档范围外）求交集，最终按 `HeadingLevelPrefixHeuristics.classifyPrefixKey` 逐块匹配、收集需要降级的 `TextBlock.id`。

---

## HeadingSequenceConsistencyHeuristics

### 职责
"连续编号标题序列一致性"：同一 `pattern_key` 下，若干行按**行序紧邻**（行距 `< MAX_LINE_GAP`）且**编号连续 +1**，构成一个"段"；若段内**部分**行已被识别为层级标题、部分未被识别，则把段内已识别的标题一并降级为正文列表（整段处理原则同"同进同退"）。支持中文序号、括号中文、阿拉伯数字（含多级 `1.1`）、罗马数字、字母，**不含**"第 X 章"类章节模式（章节模式有独立保护逻辑，不受本类连坐降级影响）。

### 常量与正则

| 名称 | 值 | 说明 |
|---|---|---|
| `MAX_LINE_GAP` | `20` | 段内相邻编号项之间允许的最大行距（与 `mpp.MarkdownPostProcessorPipeline` §6.2 结构连续保护行距上限一致，Go 移植需与该文档核对一致性） |
| `MIN_SEGMENT_SIZE` | `2` | 构成"段"所需的最少条目数 |

`PATTERN_DEFS`（有序列表，用于从行文本解析出 `(patternKey, index[])`）：

| Key | 正则（原样） | 说明 |
|---|---|---|
| `TITLE_NUM_FIVE` | `^(\d+(?:\.\d+){4})\.?\s*.*` | **不含**负向先行断言（与 `HeadingLevelPrefixHeuristics`/`ShortPhraseListRunHeuristics` 同名 key 的正则不同，此处故意宽松） |
| `TITLE_NUM_FOUR` | `^(\d+(?:\.\d+){3})\.?\s*.*` | 同上，不含断言 |
| `TITLE_NUM_THREE` | `^(\d+(?:\.\d+){2})\.?\s*.*` | 同上 |
| `TITLE_NUM_TOW` | `^(\d+(?:\.\d+){1})\.?\s*.*` | 同上 |
| `TITLE_CN_PAREN` | `^[（(]\s*([一二三四五六七八九十百千万]+)\s*[)）].*` | 捕获中文数字文本，靠 `parseChineseNumber` 转数值 |
| `TITLE_CN_NUM` | `^([一二三四五六七八九十百千万]+)[、．.\s].*` | 同上 |
| `TITLE_NUM_DOT` | `^(\d+)\.\s*.*` | 不含断言（与其他两处含断言版本不同） |
| `TITLE_NUM_DUNHAO` | `^(\d+)、\s*.*` | |
| `TITLE_NUM_SUFFIX` | `^(\d+)[)）]\s*.*` | |
| `TITLE_NUM_PAREN` | `^[（(]\s*(\d+)\s*[)）]\s*.*` | |
| `TITLE_ROMAN` | `^([IVXLCDM]+)\.\s*.*`（大小写不敏感） | 靠 `parseRoman` 转数值 |
| `TITLE_ALPHA` | `^([A-Za-z])[.．]\s*.*` | 转数值：`toUpperCase(c) - 'A' + 1` |

### 算法：`detectMarkdownLinesToDemote(lines, isRecognizedAsHeading)`
纯文本/Word/WPS/HTML 后处理入口，返回应降级的行号集合：
1. 任一参数为空返回空集合。
2. `entries = collectMarkdownSequenceEntries(lines, isRecognizedAsHeading)`。
3. 返回 `findMixedSequenceBodyLineIds(entries, lines)`。

### 算法：`detectPdfBlocksToDemote(orderedBlocks, isRecognizedAsHeading)`
PDF 转 Markdown 入口，返回应视为正文的 `TextBlock.id` 集合：
1. 任一参数为空返回空集合。
2. 遍历 `orderedBlocks`，跳过 `null`/`monoFont` 块；`norm = PdfToMarkdown.normalizeText(block.text, Config.defaults())`；`parsed = parseSequenceLine(norm)`；`null` 跳过；否则加入 `entries`（记录索引 `i`、`patternKey`、`index`、`isRecognizedAsHeading.test(i, block)`）。
3. `lineIds = findMixedSequenceBodyLineIds(entries, null)`（**注意**：此处传 `lines=null`，因为 PDF 路径没有现成的 markdown 行文本，`shouldDemoteMixedSegment` 内部对 `lines==null` 有专门分支，见下）。
4. 把 `lineIds`（即 `orderedBlocks` 的索引）映射回对应 `block.id` 集合返回。

### 算法：`collectMarkdownSequenceEntries(lines, isRecognizedAsHeading)`（私有）
1. `inFence=false`，逐行遍历（跳过 `` ``` `` 围栏内容，围栏行本身也跳过不计入 entries）。
2. `norm = trimmed 去掉行首 #{1,6} 前缀后 strip()`。
3. `parsed = parseSequenceLine(norm)`；`null` 跳过。
4. 加入 `entries`：`(lineId=i, patternKey, index, recognizedAsHeading=isRecognizedAsHeading.test(i))`。

### 算法：`findMixedSequenceBodyLineIds(entries, lines)`（私有）
核心分段与判定逻辑：
1. `entries.size() < MIN_SEGMENT_SIZE(2)` 返回空集合。
2. 按 `lineId` 排序。
3. 双指针分段：`i=0`；内层 `j=i+1` 时只要 `continuesSegment(entries[j-1], entries[j])` 为真就 `j++`（贪婪扩展当前段）。
4. 对每一段 `seg = entries[i:j]`：若 `shouldDemoteMixedSegment(seg, lines)` 为真，把段内所有 `lineId` 加入 `bodyLineIds`。
5. `i=j`，继续下一段。
6. 返回 `bodyLineIds`。

### 算法：`continuesSegment(prev, next)`（私有）
1. `prev.patternKey().equals(next.patternKey())` 为假返回 `false`。
2. `isSequentialIndex(prev.index(), next.index())` 为假返回 `false`。
3. 返回 `next.lineId() - prev.lineId() < MAX_LINE_GAP(20)`。

### 算法：`shouldDemoteMixedSegment(seg, lines)`（私有）
1. `seg.size() < MIN_SEGMENT_SIZE(2)` 返回 `false`。
2. 统计段内 `anyHeading`（存在已识别为标题的条目）与 `anyNonHeading`（存在未识别的条目）；若两者不同时为真（即段内全是标题或全不是标题），返回 `false`（一致，无需降级）。
3. 对段内**每个未识别**的条目 `e`：若 `isParallelEnumerationSibling(lines, e.lineId())` 为真，**立即**返回 `true`（找到一个"确凿的并列列举项"未识别行，足以判定整段降级）。
4. 若循环结束都没有触发，返回 `colonLabelSiblingsDominateSegment(seg, lines)`（退而求其次检查"冒号字段标签占多数"这种更弱的降级信号）。

### 算法：`colonLabelSiblingsDominateSegment(seg, lines)`（私有）
1. `lines == null` 返回 `false`（PDF 路径没有行文本，跳过此项检测）。
2. 统计 `headingCount`（已识别标题数）与 `colonLabelCount`（冒号字段标签数）：
   - 已识别的条目直接计入 `headingCount`。
   - 未识别的条目：取该行 `norm`（去 `#` 前缀后 `strip()`）；若**不**满足 `HeadingPatternQualityHeuristics.isColonTerminatedSectionFieldLabel(norm)`，**立即**返回 `false`（只要有一个未识别行不是冒号字段标签，这条弱信号规则就不适用）；否则 `colonLabelCount++`。
3. 返回 `headingCount > 0 && colonLabelCount > headingCount`（冒号标签数**严格多数**才触发；1:1 或标题占多数不触发，留给 `isParallelEnumerationSibling` 的排除规则单独保护）。

### 算法：`isParallelEnumerationSibling(lines, lineId)`（私有）
判断混排段中某个"非标题"行是否为"并列列举项"（触发整段降级的条件）：
1. `lines == null` 返回 `true`（无行文本可查，保守地当作是并列项，倾向于降级——因为 PDF 路径已经通过其他信号如 `HeadingStyleProfile` 前置过滤）。
2. `lineId` 越界返回 `true`。
3. `norm = 去 # 前缀后 strip()`；空返回 `true`。
4. `isColonTerminatedSectionFieldLabel(norm)` 为真，返回 `false`（冒号字段标签不算并列列举项，是"标题反证"信号，另由弱规则处理）。
5. `clearlyFailsHeadingQuality(norm)` 为真，返回 `false`（明显不合格的行不该被当作"证据"触发降级）。
6. `parsed = parseSequenceLine(norm)`；若非空 **且** `ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(parsed.patternKey(), norm, lines)` 为真（像节标题编号），返回 `false`。
7. 以上均不命中，返回 `true`（默认当作并列列举项）。

### 算法：`isSequentialIndex(ia, ib)`
1. 任一为 `null`、长度不等、或长度为 0，返回 `false`。
2. 除最后一维外所有维度必须完全相等：`ia[k] == ib[k]`（`k` 从 0 到 `length-2`）。
3. 最后一维要求 `ib[last] == ia[last] + 1`（严格 +1 递增）。

### 算法：`isSectionTitleNumberedLine(normalizedLine)` / `isSectionTitleNumberedLine(normalizedLine, lines)`
1. `parsed = parseSequenceLine(normalizedLine)`。
2. 返回 `parsed != null && ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(parsed.patternKey(), normalizedLine, lines)`。

### 算法：`parseSequenceLine(normalizedLine)`（包内可见）
1. 空/全空白返回 `null`。
2. 按 `PATTERN_DEFS` 列表顺序全串匹配，命中即用对应 `indexParser` 解析出 `index[]`；`index` 非空则返回 `(key, index)`。
3. 全部不命中返回 `null`。

### 算法：`splitDotInts(dotted)` / `parseChineseNumber(token)` / `parseRoman(token)`
- `splitDotInts`：按 `.` 切分并逐段 `Integer.parseInt`。
- `parseChineseNumber`：中文数字转阿拉伯数字的标准算法——过滤非中文数字字符后逐字符处理：数字字符（`零`~`九`映射到 `0`~`9`）暂存为 `number`；遇到单位字符（`十`=10, `百`=100, `千`=1000, `万`=10000）时：若单位是"万"，`section = (section + max(number,1)) * 10000; total += section; section=0; number=0`（万位单独成节，做进位处理）；否则 `section += max(number,1)*unit; number=0`。循环结束后 `result = total+section+number`；`<=0` 返回 `null`。
- `parseRoman`：标准罗马数字转数值算法，从右往左遍历，若当前值 `< prev` 则减，否则加并更新 `prev`；结果 `<=0` 返回 `null`。

### 调用方
`PdfToMarkdown.java`：
- `detectHeadingSequenceConsistencyDemoteBlockIds`（约 556 行附近）：调用 `HeadingSequenceConsistencyHeuristics.detectPdfBlocksToDemote(orderedTextBlocks, recognizesBlockAsHeadingForSequenceConsistency)`，其中判定函数 `recognizesBlockAsHeadingForSequenceConsistency`（约 576 行）内部先用 `HeadingSequenceConsistencyHeuristics.parseSequenceLine(s) == null` 短路排除非编号行，再结合 `HeadingStyleProfile` 的 H1/H2 角色判定"样式上像标题"。
- 该检测结果与 `ShortPhraseListRunHeuristics.detectPdfShortPhraseListRuns` 的结果一并合并进 `headingSequenceDemoteBlockIds`（PdfToMarkdown 主流程约 238-250 行），共同决定最终哪些块被压制为正文列表。

---

## HeadingSuppressHeuristics

### 职责
层级标题的"抑制"判定核心：正文字体且非独立成行、或上一行以冒号收束（列举引导语境）时，都不应把候选行输出为层级标题。同时提供"独立成行"标题行的判定（数字层级、"第X条"条款等），供其他类复用。

### 常量与正则

| 名称 | 值/模式 | 说明 |
|---|---|---|
| `LEADING_MD_HASH` | `^\s{0,3}#{1,6}[\s　]*` | 同 `ChapterReferenceHeuristics` 里的定义（独立编译，两处正则文本相同） |
| `CN_ARTICLE_HEADING` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*条` | "第X条"起始（不要求整行匹配，只需前缀存在） |
| `HEADING_PREFIX_ONLY` | `^(?:[（(][一二三四五六七八九十百千万]+[)）]\|[一二三四五六七八九十百千万]+[、．.]\|第\s*(?:[一二三四五六七八九十百千万零\d]+)\s*(?:章\|节\|纲\|目\|条)\|\d+(?:\.\d+)*[\.、)）】]?\|[（(]\s*\d+\s*[)）]\|[IVXLCDMivxlcdm]+\.\|[A-Za-z][.．])` | 各类标题/列表前缀的统一联合模式（不含题名部分），用于剥离"仅前缀"部分判断是否独立成行 |
| `NUMERIC_HIERARCHY_PREFIX` | `^(\d+(?:\.\d+)*\.?)` | 行首数字层级编号（`1.`、`1.1`、`1.1.1.` 等，末尾点可选） |

### 算法：`fontMatchesBody(fontSizeMean, fontWeight, bodyFontMode, fontSizeDeltaPt)`（重载 1，纯数值）
返回 `fontSizeMean <= bodyFontMode + fontSizeDeltaPt && fontWeight < 600`（字号不超过正文字号+容差、且字重不到"加粗"阈值 600，即判定为"字体上看起来像正文"）。

### 算法：`fontMatchesBody(block, bodyFontMode, config)`（重载 2，接 `TextBlock`）
1. `block`/`config` 为 `null` 返回 `true`（无信息时保守判定为"像正文"，即倾向于抑制标题——**注意这个默认值方向**）。
2. 委托重载 1，传入 `block.fontSizeMean`、`block.fontWeight`、`bodyFontMode`、`config.fontSizeDeltaPt`。

### 算法：`prevEndsWithColon(prevText)`
1. 空/全空白返回 `false`。
2. `strip()` 后判断是否以"："或 `:` 结尾。

### 算法：`isStandaloneHeadingLine(line)`
判定整行是否"仅含一个标题前缀 + 短标题名"（独立成行）：
1. 空/全空白返回 `false`。
2. `t = stripHashes(line)`；空返回 `false`。
3. 用 `HEADING_PREFIX_ONLY` 在 `t` 上 `find()`，要求 `start() == 0`（必须从行首匹配），否则返回 `false`。
4. `rest = t.substring(m.end()).trim()`（前缀之后剩余部分）。
5. `rest` 为空，返回 `true`（纯前缀，无题名，视为独立——注意这与常理"标题应有题名"不同，是刻意的宽松判定，供上游进一步组合判断）。
6. 若 `MarkdownStructureRules.isOrderedListItemLine(t)` 为真，返回 `false`（整行其实是有序列表项，不是标题）。
7. 若 `rest` 包含"。"或"；"（正则 `.*[。；].*`），返回 `false`（含句子终止符，说明是正文续接而非短标题名）。
8. 若 `rest.length() > 50`，返回 `false`（题名过长不算"短标题"）。
9. 否则返回 `true`。

### 算法：`isStandaloneNumericHierarchyLine(line)`
判定"独立成行的数字层级标题"（如 `1.`、`1.1`、`1.1.1`、`1.1.1.1`），**排除**"编号+长段说明正文"粘连行：
1. 空/全空白返回 `false`。
2. `t = stripHashes(line)`；空 或 首字符非数字，返回 `false`。
3. 若 `MarkdownStructureRules.isOrderedListItemLine(t)` 为真，返回 `false`。
4. `NUMERIC_HIERARCHY_PREFIX` 在 `t` 上 `find()`，要求 `start()==0`，否则返回 `false`。
5. `rest = t.substring(m.end()).trim()`；为空返回 `true`。
6. 若 `rest` 含"。"或"；"，返回 `false`。
7. **比 `isStandaloneHeadingLine` 更严格**：要求 `rest.length() <= 18` **且** `rest` 不含 `[，、；：,.!?;:]` 中任意标点（正则 `.*[，、；：,.!?;:].*` 取反），两条件同时满足才返回 `true`；否则 `false`（此严格度专门用于排除"5.7.1 + 正文续段"这类误判）。

### 算法：`isStandaloneCnArticleLine(text)`
1. 空/全空白返回 `false`。
2. `t = stripHashes(text)`；空 或 `CN_ARTICLE_HEADING` 未在 `t` 中 `find()`，返回 `false`。
3. `ChapterTocLineRemover.isChapterTocLine(t)` 为真，返回 `false`。
4. 返回 `isStandaloneHeadingLine(text)`（注意：这里传入的是原始 `text` 而非 `t`，与 `isStandaloneHeadingLine` 内部会自行再做一次 `stripHashes` 等价，属冗余但需照抄行为）。

### 算法：`startsWithCnArticleHeading(text)`
1. 空/全空白返回 `false`。
2. `t = stripHashes(text).strip()`；返回 `CN_ARTICLE_HEADING.find(t)` 结果 **且** `!ChapterTocLineRemover.isChapterTocLine(t)`。

### 算法：`looksLikeCnArticleBodyParagraphLead(text)`
判断"第 X 条"后接的是否为"说明性正文"（而非短题名）：
1. 空/全空白返回 `false`。
2. `t = stripHashes(text).strip()`；`!startsWithCnArticleHeading(t)` 返回 `false`。
3. `rest = restAfterCnArticlePrefix(t)`；为空返回 `false`。
4. 若 `ChapterTocLineRemover.isLikelyChapterTitleNameLine(rest)` 为真，返回 `false`（`rest` 本身像个短标题名，不是正文）。
5. 若 `rest` 含"，"或"、"（正则 `.*[，、].*`），返回 `true`（硬断行逗号分句，是正文特征）。
6. 否则返回 `rest.length() > 18`（过长短语也视为正文）。

### 算法：`looksLikeCnArticleBodySentence(text)`
判断"第 X 条"整句是否为"以句号收束的说明性正文"：
1. 空/全空白返回 `false`。
2. `t = stripHashes(text).strip()`；`!startsWithCnArticleHeading(t)` 返回 `false`。
3. 不以"。"或"."结尾，返回 `false`。
4. 返回 `restAfterCnArticlePrefix(t).length() > 8`。

### 算法：`restAfterCnArticlePrefix(text)`（私有）
1. 空/全空白返回 `""`。
2. `strip()` 后用正则 `^第\s*[一二三四五六七八九十百千万零\d]+\s*条\s*` 替换第一次出现为空，再 `trim()` 返回。

### 算法：`shouldSuppressHeading(text, prevText, fontLikeBody)`（重载 1，纯字符串）
核心抑制判定：
1. `ChapterReferenceHeuristics.isBodyChapterReference(text)` 为真 → `true`。
2. `MarkdownStructureRules.isChapterTableOfContentsEntry(text)` 为真 → `true`。
3. `MarkdownStructureRules.isOrderedListItemLine(text)` 为真 → `true`。
4. 若 `isStandaloneCnArticleLine(text)` 为真：
   - 若 `prevEndsWithColon(prevText)` **且** `looksLikeListGuideContext(prevText)`，返回 `true`（前一行是"7.1 ……包括以下部分："这类列举引导语，且以冒号收束，则抑制当前"第X条"标题——因为它其实是被列举的条目名，不是独立条款标题）。
   - 否则返回 `false`（独立成行的"第X条"标题一般不因字体或前文冒号被抑制）。
5. 若 `prevEndsWithColon(prevText)`：
   - 特殊豁免：若 `ChapterTocLineRemover.isStructuralChapterHeading(text)` **且** `isStandaloneHeadingLine(text)` **且** `!looksLikeListGuideContext(prevText)`，返回 `false`（落款"日期："等普通冒号结尾行之后，仍应保留真正的"第X章"结构标题，不被误抑制；但如果前一行明确是列举引导语，则不豁免，走下一步）。
   - 否则返回 `true`（前一行以冒号收束，默认抑制当前行为标题，视为列举内容的一部分）。
6. `fontLikeBody && !isStandaloneHeadingLine(text)` → `true`（正文字体 且 非独立成行 → 抑制）。
7. 以上均不命中，返回 `false`（允许作为标题）。

### 算法：`looksLikeListGuideContext(prevText)`（私有）
1. 空/全空白返回 `false`。
2. `t = stripHashes(prevText)`；返回 `ListGuideHeuristics.looksLikeListGuideAnchor(t) || ListGuideHeuristics.looksLikeLevel2GuideAnchor(t)`。

### 算法：`shouldSuppressHeading(block, prevBlock, bodyFontMode, config, inListGuideScope)`（重载 2，接 `TextBlock`）
1. `block == null` 返回 `false`。
2. `s = block.text`（`null` 视为空串）；`prev = prevBlock.text`（`prevBlock` 为 `null` 时视为空串）。
3. 若 `inListGuideScope` 为真 **且** `PdfToMarkdown.isHeadingByRegex(s)` 为真，直接返回 `true`（处于列举引导区域内的、看起来像标题正则的行，一律抑制——不管字体如何）。
4. `fontLikeBody = fontMatchesBody(block, bodyFontMode, config)`。
5. 委托重载 1：`shouldSuppressHeading(s, prev, fontLikeBody)`。

### 算法：`stripHashes(line)`
与 `ChapterReferenceHeuristics.stripHashes` 逻辑完全相同（独立实现，两个类各自维护一份同名同逻辑方法——Go 移植可以共享同一个工具函数，见文末包组织建议）。

### 算法：`shouldSuppressHeadingLine(lines, lineId)`
供纯 Markdown 行序场景使用的入口封装：
1. `lines` 为 `null` 或 `lineId` 越界，返回 `false`。
2. `raw = lines.get(lineId)`。
3. `ChapterReferenceHeuristics.isBodyChapterReference(raw)` 为真 → `true`。
4. 若 `HeadingSequenceConsistencyHeuristics.isSectionTitleNumberedLine(stripHashes(raw))` 为真，**立即**返回 `false`（像节标题编号的行不做抑制，交给专门的序列一致性逻辑处理）。
5. 若 `!looksLikeHeadingCandidate(raw)`（见下），返回 `false`（本身就不像标题候选，谈不上"抑制"）。
6. `prev = previousNonBlankLine(lines, lineId)`。
7. `shouldSuppressHeading(stripHashes(raw), prev, false)` 为真 → `true`（注意此处 `fontLikeBody` 固定传 `false`，纯文本场景无字体信息）。
8. 若 `!isStandaloneHeadingLine(raw)`，返回 `true`（非独立成行也算需要抑制）。
9. 否则返回 `false`。

### 算法：`looksLikeHeadingCandidate(raw)`（私有）
1. 空/全空白返回 `false`。
2. `trimmed = strip()`；以 `#` 开头直接返回 `true`。
3. `t = stripHashes(raw)`；返回 `!t.isEmpty() && PdfToMarkdown.isHeadingByRegex(t)`。

### 算法：`previousNonBlankLine(lines, fromLineId)`
从 `fromLineId - 1` 起向前查找第一条 `stripHashes` 后非空的行，返回其**原始文本**（未经处理）；找不到返回 `""`。

### 调用方
`PdfToMarkdown.java` 中大量分散调用（约 1197、2492-2496、3147-3151、3221、3281、3299、3315、3382、3417、3547、3551、3561 行等）：
- 标题合并（2492-2496 行）：`isStandaloneNumericHierarchyLine` 判断相邻块是否分别是独立数字标题（决定是否阻止合并）。
- 粘连拆分（3147-3151 行）：`isStandaloneNumericHierarchyLine(glued)`/`(a+b)` 判断拆分候选是否本身构成独立数字标题。
- 主标题判定入口（3281 行）：`shouldSuppressHeading(block, prevBlock, bodyFontMode, config, inListGuideScope)` 是 PDF 逐块判定"这个候选块是否应该被压制不作为标题"的核心调用点。
- 字体判定复用（3221、3382 行）：`fontMatchesBody(block, bodyFont, config)`。
- 中文序号/条款独立行判断（3417、3547、3551、3561 行）：`isStandaloneHeadingLine`、`isStandaloneCnArticleLine`、`looksLikeCnArticleBodyParagraphLead`、`startsWithCnArticleHeading`。

---

## ListGuideHeuristics

### 职责
识别"7.1 ……包括下列内容："这类列举引导行之后的"列举区"，以及连续多行"第 X 章 ……"目录式罗列（正文中枚举章节构成，而非真的结构标题），标记出这些行号区间，供上游判定"即使命中标题正则或 PDF 加粗，此区间内也不应输出为层级标题"。

### 常量与正则

| 名称 | 值/模式 | 说明 |
|---|---|---|
| `NUM_LEVEL2_PREFIX` | `^(\d+)\.(\d+)(?!\.)(?:\s*.*)$` | "N.M"二级编号起始，**含负向先行**（见兼容性表），防止把三级编号"1.2.3"的前两段误判为二级 |
| `LIST_SCOPE_ITEM_CANDIDATE` | `^(?:第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\|[（(]?\d+[\.、)）\]]\s*.*\|[-+*•●○■□►→★☆]\s*.*)$` | 列举区内候选条目形态：第X章… / 数字编号(各种后缀标点) / 常见项目符号开头 |
| `CHAPTER_OUTLINE_LINE` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*[^。！？；：,.!?;:]{0,36}$` | 独立成行、无句读标点、长度受限的"第X章 短题名"整行 |
| `LEADING_MD_HASH` | `^\s{0,3}#{1,6}[\s　]*` | 同前，独立编译副本 |
| `TRAILING_MD_HASH` | `[\s　]*#{1,}[\s　]*$` | 行尾的 `#` 残留（数量不限，`{1,}` 无上限，注意与其他类里 `{1,6}` 上限不同） |
| `SPACE_RUN` | `[\s　]+` | 连续空白（含全角空格）游程，用于折叠为单个空格 |
| `EDGE_WHITESPACE` | `^[\s　]+\|[\s　]+$` | 首尾空白（含全角），用于裁剪 |
| `ANCHOR_KEYWORDS`（List） | `"包括下列内容", "包括以下内容", "包括以下部分", "包括以下", "下列部分", "如下", "如下所示", "下列内容", "下列章节", "以下章节", "如下章节", "参考下面的章节", "参考以下章节", "参考下列章节"` | 列举引导语关键词表（子串包含匹配） |
| `MIN_GUIDE_ITEMS` | `2` | 判定为"列举区"所需最少候选条目数 |
| `GUIDE_ITEM_SCAN_WINDOW` | `8` | 锚点后向前扫描候选条目数量时的窗口行数 |
| `GUIDE_MAX_SCOPE_LINES` | `20` | 列举区最大跨行数上限 |
| `CHAPTER_OUTLINE_MIN_RUN` | `3` | "第X章…"连续罗列判定所需最少连续行数 |
| `CHAPTER_OUTLINE_MAX_LINE_LEN` | `40` | 单行"第X章…"罗列判定的最大行长度 |
| `CHAPTER_OUTLINE_MAX_GAP` | `2` | 罗列行之间允许的最大空行间隔 |

### 算法：`looksLikeLevel2GuideAnchor(normalizedLine)`
1. 空/全空白返回 `false`。
2. 不以"："或 `:` 结尾，返回 `false`。
3. 返回 `NUM_LEVEL2_PREFIX` 全串匹配结果。

### 算法：`looksLikeListGuideAnchor(normalizedLine)`
1. 空/全空白返回 `false`。
2. 遍历 `ANCHOR_KEYWORDS`，任一为 `normalizedLine` 子串即返回 `true`。
3. 否则 `false`。

### 算法：`isScopeItemCandidate(normalizedLine)`
1. 空/全空白返回 `false`。
2. 若 `HeadingLevelPrefixHeuristics.isLeadingPriorityMarkerHierarchyHeading(normalizedLine)` 为真，返回 `false`（★ 标记的层级标题不算普通列举候选项）。
3. 返回 `LIST_SCOPE_ITEM_CANDIDATE` 全串匹配结果。

### 算法：`countScopeItems(lines, start, windowSize)`
1. `lines` 为空返回 `0`。
2. `end = min(lines.size(), start + max(0, windowSize))`。
3. 遍历 `[max(0,start), end)`：`t = normalizeLine(lines.get(i))`；空跳过；`isScopeItemCandidate(t)` 为真计数 `+1`。
4. 返回计数。

### 算法：`detectListGuideScopes(lines)`
识别"N.M ……：" 引导行之后的列举区间（半开区间 `[start, end)`）：
1. `lines` 为空返回空列表。
2. 遍历每行 `i` 作为潜在锚点：
   a. `anchor = normalizeLine(lines.get(i))`；须同时满足 `looksLikeLevel2GuideAnchor(anchor)` **且** `looksLikeListGuideAnchor(anchor)`（即：既要是"N.M...:"形态，又要含引导关键词），否则跳过。
   b. `countScopeItems(lines, i+1, GUIDE_ITEM_SCAN_WINDOW=8) < MIN_GUIDE_ITEMS=2`，跳过（后续候选条目不够多，不构成真正的列举区）。
   c. `m = NUM_LEVEL2_PREFIX.matches(anchor)`；不匹配跳过；`chapterNo = 捕获组1`（锚点自身的"N"部分，即章号）。
   d. `end = min(lines.size(), i+1+GUIDE_MAX_SCOPE_LINES=20)`（默认上限）。
   e. `sawNonChapterLine = false`；从 `j=i+1` 向后扫描直到 `lines.size()`（**注意**：内层循环条件是 `j < lines.size()`，而非 `j < end`，即扫描不受 `end` 限制，但一旦决定终止会用 `min(end, j)` 钳制结果）：
      - `s = normalizeLine(lines.get(j))`。
      - 若 `isScopeTerminatorChapterHeading(s)`（独立成行的结构性"第X章"标题，非目录行）：若 `sawNonChapterLine` 已为真（之前已出现过非章节的普通列举内容），则 `end = min(end, j)` 并 `break`（列举区在此终止，不越过一个真正的结构章节边界）；否则（还没见过普通内容，说明这仍处于锚点紧跟的"第一章 xxx""第二章 xxx"式章节名罗列阶段）不终止，继续。
      - 否则若 `s` 非空，`sawNonChapterLine = true`。
      - 无论上面分支如何，都再检查 `mj = NUM_LEVEL2_PREFIX.matches(s)`：若匹配且 `chapterNo.equals(捕获组1)`（遇到了同一章号的下一个"N.M'"条目，即回到了同一"N."系列的下一个二级条目），`end = min(end, j)` 并 `break`（列举区到此为止，因为已经进入了下一个正式的编号条目）。
   f. 若 `end > i+1`，把 `[i+1, end)` 加入结果 `scopes`。
3. 返回 `scopes`。

### 算法：`isScopeTerminatorChapterHeading(normalizedLine)`（私有）
1. 空/全空白返回 `false`。
2. `ChapterTocLineRemover.isChapterTocLine(normalizedLine)` 为真返回 `false`（目录行不算终止符）。
3. 返回 `ChapterTocLineRemover.isStructuralChapterHeading(normalizedLine) && HeadingSuppressHeuristics.isStandaloneHeadingLine(normalizedLine)`。

### 算法：`looksLikeChapterListGuideAnchor(normalizedLine)`
1. 空/全空白返回 `false`。
2. 不以"："或 `:` 结尾返回 `false`。
3. 若 `looksLikeListGuideAnchor(normalizedLine)` 为真（含通用引导关键词），进一步检查是否包含以下**任一**更具体的"章节罗列"关键词："章节"、"章节目录"、"各章"、"下列章"、"以下章"、"如下章"、"参考下面"、"参考以下"、"参考下列"；命中即返回 `true`。
4. 否则返回 `false`（注意：外层 `looksLikeListGuideAnchor` 为假时，本方法整体直接返回 `false`，不会进入内层关键词检查——即"章节罗列锚点"是"通用引导锚点"的**子集**，必须先满足通用条件）。

### 算法：`detectChapterListGuideScopes(lines)`
识别"参考下面的章节："等冒号引导行之后，连续两行及以上"第 X 章 ……"短标题列举：
1. `lines` 为空返回空列表。
2. 遍历每行 `i` 作锚点：`anchor = normalizeLine(...)`；`!looksLikeChapterListGuideAnchor(anchor)` 跳过。
3. `chapterCount=0`；`end=i+1`；`limit = min(lines.size(), i+1+GUIDE_MAX_SCOPE_LINES)`。
4. 从 `j=i+1` 到 `limit`：`t = normalizeLine(...)`；空跳过（**不中断**，继续下一行）；若 `isChapterOutlineLine(t)`，`chapterCount++`，`end=j+1`，`continue`；否则 `break`（非空但不是章节罗列行，列举区结束）。
5. `chapterCount >= MIN_GUIDE_ITEMS(2)` 则把 `[i+1, end)` 加入 `scopes`。
6. 返回 `scopes`。

### 算法：`detectChapterOutlineScopes(lines)`
不依赖冒号引导锚点，直接识别连续多行"第 X 章 ……"短标题罗列（招标文件常见的"构成"列举）：
1. `lines` 为空返回空列表。
2. 外层 `i=0` 循环遍历全文：
   a. `runStart=-1`；`runCount=0`；`lastHit=-1`；内层 `j=i`：
      - `t = normalizeLine(lines.get(j))`。
      - 若 `t` 为空：若 `runCount>0` **且** `j - lastHit <= CHAPTER_OUTLINE_MAX_GAP(2)`（允许游程内出现不超过 2 行的空行间隔），`j++` 继续；否则 `break`（空行超出容忍间隔或尚未开始游程，终止内层扫描）。
      - 若 `!isChapterOutlineLine(t)`，`break`。
      - 否则：若 `runCount==0`，`runStart=j`；`runCount++`；`lastHit=j`；`j++`。
   b. 若 `runCount >= CHAPTER_OUTLINE_MIN_RUN(3)` 且 `runStart>=0`：把 `[runStart, lastHit+1)` 加入 `scopes`；`i = lastHit+1`。
   c. 否则：`i = max(i+1, j)`（至少前进一行，避免死循环；若内层已经推进更远则跳到 `j`）。
3. 返回 `scopes`。

### 算法：`isChapterOutlineLine(normalizedLine)`
1. 空/全空白返回 `false`。
2. `t = strip()`；长度 `> CHAPTER_OUTLINE_MAX_LINE_LEN(40)` 返回 `false`。
3. `ChapterTocLineRemover.isChapterTocLine(t)` 为真返回 `false`。
4. 返回 `CHAPTER_OUTLINE_LINE` 全串匹配结果。

### 算法：`isInAnyScope(lineId, scopes)`
遍历 `scopes` 中每个 `[a,b)` 区间，`lineId >= a && lineId < b` 即返回 `true`；全部不命中返回 `false`。

### 算法：`detectListGuideScopeBlockIds(blocks)`
PDF 场景的整合入口：
1. `blocks` 为空返回空集合。
2. 把每个 `block.text`（`null` 视为空串）抽成 `texts` 列表（伪造成"行文本序列"）。
3. 依次调用并合并三种 scope 检测结果：`detectChapterListGuideScopes(texts)`、`detectListGuideScopes(texts)`、`detectChapterOutlineScopes(texts)`。
4. 对每个 scope 区间，把对应下标范围内（不超过 `blocks.size()`）的 `block.id` 加入 `scoped` 集合。
5. 返回 `scoped`。

### 算法：`normalizeLine(line)`
1. `stripEdgeWhitespace(line)`。
2. 用 `LEADING_MD_HASH` 替换首次出现为空（去 `#` 前缀）。
3. 用 `TRAILING_MD_HASH` 替换首次出现为空（去行尾残留 `#`）。
4. 再次 `stripEdgeWhitespace`。
5. 用 `SPACE_RUN` 把所有连续空白折叠为单个普通空格。
6. 返回结果。

### 算法：`stripEdgeWhitespace(text)`（私有）
`null`/空返回 `""`；否则用 `EDGE_WHITESPACE` 替换首尾空白（含全角）为空。

### 调用方
`PdfToMarkdown.java`（约 240 行）：主流程里在构建 `orderedTextBlocks` 之后立即调用 `ListGuideHeuristics.detectListGuideScopeBlockIds(orderedTextBlocks)` 得到 `listGuideScopeBlockIds` 集合，后续判定每个块是否应作为标题输出时（结合 `HeadingSuppressHeuristics.shouldSuppressHeading` 的 `inListGuideScope` 参数）会查询该集合。

---

## ShortPhraseListRunHeuristics

### 职责
识别"短语式连续编号清单"（`doc/title_extract_new.md` §6.1.1）：形如"1.总则 2.范围 3.附则"这种编号+极短正文名、且编号连续递增的一组行，本质是正文里的简短分项清单而非层级标题序列。区分三种检测模式（`PLAIN`/`PLAIN_MARKDOWN`/`EXISTING_HEADING`），供 `mpp.MarkdownPostProcessorPipeline` 与 `PdfToMarkdown` 共用，避免两处各自实现产生冲突判定。所有数值阈值来自调用方传入的 `PdfToMarkdown.Config`（`pdf2md.shortPhraseNumberedRun*` 系列配置）。

### 常量与正则

| 名称 | 值/模式 | 说明 |
|---|---|---|
| `HEADING_LINE` | `^(#{1,6})\s*(.+)$` | Markdown 标题行整体匹配，捕获井号与正文 |
| `TABLE_SEPARATOR` | `^\s*\|(?:\s*:?-{3,}:?\s*\|)+\s*$` | Markdown 表格分隔行（如 `\|---\|:---:\|`） |
| `STRUCTURAL_SECTION_KEYWORDS`（List） | `"总则", "范围", "附则", "概述", "项目背景", "建设内容", "实施范围", "总体要求", "工作目标", "基本原则", "组织实施", "保障措施", "术语", "定义"` | 已有标题降级时，去编号正文命中这些关键词则**倾向保留**为标题（不判定为短语清单） |
| `ListRunDetectionMode`（枚举） | `PLAIN`, `PLAIN_MARKDOWN`, `EXISTING_HEADING` | `PLAIN`：PDF 块/无样式剖面检测（不含 ocr 章节词保护）；`PLAIN_MARKDOWN`：未带 `#` 行的检测，含章节词保护；`EXISTING_HEADING`：已有 `#` 标题的降级反证，条件更严 |

`PATTERN_DEFS`（本类内部**独立的一份**，用于从行文本解析编号，含负向先行断言的完整版本，与 `HeadingLevelPrefixHeuristics.PREFIX_DEFS` 中数字类模式**逐字符相同**）：

| Key | 正则（原样） |
|---|---|
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

（不含 `TITLE_CHAPTER_*`、`TITLE_CN_PAREN`、`TITLE_CN_NUM` —— 这些 key 由 `supportsPatternKey` 显式排除，见下。）

### 算法：`supportsPatternKey(patternKey)`
1. `null` 返回 `false`。
2. `switch`：`TITLE_CHAPTER_ONE`/`TITLE_CHAPTER_TOW`/`TITLE_CHAPTER_THREE`/`TITLE_CHAPTER_FOUR`/`TITLE_CHAPTER_FIVE`/`TITLE_CN_PAREN`/`TITLE_CN_NUM` → `false`；其他（含未知 key，`default` 分支）→ `true`。

### 算法：`detectMarkedLineIds(entries, lines)`（重载 1）
委托重载 2，`config = PdfToMarkdown.Config.defaults()`。

### 算法：`detectMarkedLineIds(entries, lines, config)`（重载 2）
直接委托 `detectPlainShortPhraseListRuns(entries, lines, config)`。

### 算法：`detectPlainShortPhraseListRuns(entries, lines, config)`（Entry 列表版）
委托 `detectMarkedLineIdsInternal(entries, lines, config, null, null, PLAIN_MARKDOWN)`。

### 算法：`detectPlainShortPhraseListRuns(lines, config, lineNormalizer)`（文本行版）
委托 `detectMarkedLineIdsFromTextLinesInternal(lines, config, lineNormalizer, PLAIN_MARKDOWN)`。

### 算法：`detectExistingHeadingShortPhraseListRuns(lines, config, lineNormalizer)`
委托 `detectMarkedLineIdsFromTextLinesInternal(lines, config, lineNormalizer, EXISTING_HEADING)`。

### 算法：`detectMarkedLineIdsFromTextLines`（已废弃）
委托 `detectPlainShortPhraseListRuns(lines, config, lineNormalizer)`。移植时可直接删除，统一调用点。

### 算法：`detectMarkedLineIdsFromTextLinesInternal(lines, config, lineNormalizer, mode)`（私有）
从纯文本行构建 `Entry` 列表再进入统一分组逻辑：
1. `lines` 为空返回空集合。
2. `cfg = config` 或默认值；`normalize = lineNormalizer` 或默认的 `s -> s.trim()`（`null` 转空串）。
3. 逐行遍历：
   a. `trimmed = raw.strip()`；`isMarkdownHeading = HEADING_LINE.matches(trimmed)`。
   b. 若 `mode ∈ {PLAIN, PLAIN_MARKDOWN}` 且 `isMarkdownHeading`，跳过（这两种模式只处理**未带** `#` 的行）。
   c. 若 `mode == EXISTING_HEADING` 且 `!isMarkdownHeading`，跳过（此模式只处理**已带** `#` 的行）。
   d. `norm = normalize.apply(raw)`；空跳过。
   e. `parsed = parseNumberedLine(norm)`；`null` 或 `!supportsPatternKey(parsed.patternKey)`，跳过。
   f. 若 `parsed.patternKey != "TITLE_NUM_DOT"` **且** `looksLikeSectionTitleNumberedLine(parsed.patternKey, norm, lines)` 为真，跳过（像"1.1 实施要求"这种多级节标题不参与短语清单检测——**注意**唯独 `TITLE_NUM_DOT`（单级"1."）不受此排除保护，即"1. 总则"这类单级编号哪怕像节标题也仍会进入候选，由后续 `qualifiesAsShortPhraseListRun` 的其他规则如 `STRUCTURAL_SECTION_KEYWORDS` 判定）。
   g. 加入 `entries`。
4. 委托 `detectMarkedLineIdsInternal(entries, lines, cfg, null, null, mode)`。

### 算法：`detectMarkedLineIdsInternal(entries, lines, config, orderedBlocks, profile, mode)`（私有，核心分组器）
1. `entries` 为空返回空集合。
2. 过滤出 `supportsPatternKey` 为真的条目到 `filtered`，为空返回空集合。
3. 按 `lineId` 排序。
4. 按**相同 `patternKey` 连续段**分组（`i..j`，`j` 扩展条件是 `patternKey` 相等），对每组调用 `markRunsInPatternGroup`。
5. 返回累积的 `marked` 集合。

### 算法：`detectMarkedBlockIds(orderedBlocks, config)`（重载 1）
委托重载 2，`profile = null`。

### 算法：`detectPdfShortPhraseListRuns(orderedBlocks, config, profile)`
1. `profile == null` 抛出 `IllegalArgumentException`（**要求**必须先调用 `PdfToMarkdown.buildHeadingStyleProfile` 得到剖面才能调用本方法）。
2. 委托 `detectMarkedBlockIds(orderedBlocks, config, profile)`。

### 算法：`detectMarkedBlockIds(orderedBlocks, config, profile)`（重载 2，PDF 核心入口）
1. `orderedBlocks` 为空返回空集合。
2. `cfg = config` 或默认值。
3. `pseudoLines = buildPseudoLines(orderedBlocks, cfg)`（把块序列转换为等长的"伪行文本"列表，索引与 `orderedBlocks` 一一对应）。
4. 遍历 `orderedBlocks`：跳过 `null`/`monoFont` 块；`norm = PdfToMarkdown.normalizeText(block.text, cfg)`；空跳过；`parsed = parseNumberedLine(norm)`；`null` 或不支持的 key 跳过；否则加入 `entries`（**注意**：此路径**不做** `looksLikeSectionTitleNumberedLine` 排除，与文本行路径不同——PDF 路径把这一判断挪到后续 `qualifiesAsShortPhraseListRun` 内部按 mode 分支处理）。
5. 若 `entries` 非空：排序、按 `patternKey` 连续分组，对每组调用 `markRunsInPatternGroup(..., pseudoLines, marked, cfg, orderedBlocks, profile, PLAIN)`（**固定用 `PLAIN` 模式**）。
6. 把 `marked`（下标集合）映射回 `block.id` 集合返回。

### 算法：`buildPseudoLines(blocks, config)`（私有）
1. 遍历 `blocks`：`null`/`monoFont` 块 → 加入空字符串 `""`；否则加入 `PdfToMarkdown.normalizeText(block.text, config)`。
2. 返回与 `blocks` 等长的字符串列表。

### 算法：`markRunsInPatternGroup(group, lines, marked, config, orderedBlocks, profile, mode)`（私有）
在同一 `patternKey` 的条目组内，按"编号连续 +1 且行距 `< shortPhraseNumberedRunMaxGap`"继续切分出连续子段，逐段判定：
1. `group` 为空直接返回。
2. `i=0`；内层 `j=i+1` 时只要 `isSequential(group[j-1], group[j])` **且** `group[j].lineId - group[j-1].lineId < config.shortPhraseNumberedRunMaxGap` 就 `j++`。
3. `seg = group[i:j]`；若 `qualifiesAsShortPhraseListRun(seg, lines, config, orderedBlocks, profile, mode)` 为真，把 `seg` 内所有 `lineId` 加入 `marked`。
4. `i = max(i+1, j)`，继续。

### 算法：`qualifiesAsShortPhraseListRun(seg, lines, config)`（重载 1，测试/简化用）
委托重载 2，`orderedBlocks=null, profile=null, mode=PLAIN`。

### 算法：`qualifiesAsShortPhraseListRun(seg, lines, config, orderedBlocks, profile, mode)`（重载 2，核心判定）
1. `minRun = mode==EXISTING_HEADING ? max(3, config.shortPhraseNumberedRunMin) : config.shortPhraseNumberedRunMin`（已有标题降级场景要求更长的段，至少 3 条，即使配置值更小也钳制到 3）。
2. `seg.size() < minRun` → `false`。
3. `seqQualityAdjacent(seg) + 1e-9 < config.shortPhraseNumberedRunSeqQualityMin` → `false`（相邻编号连续性比例需达标，`1e-9` 是浮点比较容差）。
4. `meanLineGap(seg) >= config.shortPhraseNumberedRunMaxGap` → `false`（平均行距不能达到/超过最大允许行距——注意这里判定符号是 `>=`，与 `markRunsInPatternGroup` 里切分时用的 `<` 略有差异但语义一致，均为"严格小于阈值才算紧邻"）。
5. 对 `seg` 中每一条：`isShortPhraseBody(stripPrefix(normText, patternKey), config)` 必须全部为真，否则 → `false`（去编号正文必须"短"）。
6. `segmentHasExpansionBetweenItems(seg, lines, config)` 为真 → `false`（相邻编号项之间出现超过 `shortPhraseNumberedRunMaxBodyLines` 行的实质展开内容，说明不是简单清单）。
7. 若 `mode ∈ {PLAIN, PLAIN_MARKDOWN}`：
   a. 若 `profile != null && profile.isReliable() && orderedBlocks != null`（可靠的 PDF 样式剖面场景）：
      - `segmentProtectedByStableTitleStyleCluster(seg, orderedBlocks, profile)` 为真 → `false`（段落被稳定 H1/H2 样式簇保护，不判定为短语清单）。
      - `!segmentIsBodyOrNoiseStyleRun(seg, orderedBlocks, profile, config)` → `false`（要求段内块样式必须是"正文/噪声"类，否则不判定）。
   b. 否则（无可靠样式剖面，纯文本/弱剖面场景）：
      - `eachItemHasTrailingExpansion(seg, lines, config)` 为真 → `false`（若每个编号项到下一项之间都至少有 1 行实质展开，说明这更像真正的标题序列，不是短语清单）。
      - `segmentLooksLikeStableHeadingRun(seg, lines)` 为真 → `false`（段内各行本身就是统一层级的 `#` 标题，视为稳定标题序列）。
      - `segmentProtectedByStableTitleStyleCluster(seg, orderedBlocks, profile)` 为真 → `false`（`profile`/`orderedBlocks` 可能为 `null`，函数内部会做 `null` 判定返回 `false` 从而不生效，等价于跳过此项保护）。
      - 若 `mode == PLAIN_MARKDOWN` 且 `segmentProtectedByStructuralSectionKeywords(seg)` 为真 → `false`。
   c. 以上均未命中，返回 `true`（判定为短语式清单，应压制）。
8. 若 `mode == EXISTING_HEADING`（已有标题降级，走到此处说明前面 1-6 步已通过，进入更严格的降级反证）：
   - `!segmentHasUniformMarkdownHeadingLevel(seg, lines)` → `false`（段内 `#` 层级必须一致，否则不降级——层级本就不一致，不构成"同级混排"证据）。
   - `segmentHasAnyExpansionBetweenItems(seg, lines, config)` 为真 → `false`（任何相邻项之间有展开内容都不降级，比 PLAIN 分支的"超过阈值"更严格，此处"任何 > 0"就拒绝）。
   - 对每条 `Entry`：`isChineseStructuralSectionTitle(normText)` 为真 → `false`（中文强结构标题如"第X章""一、""（一）"混入段内则不降级）；`bodyContainsStructuralSectionKeyword(stripPrefix(...))` 为真 → `false`。
   - 均未命中，返回 `true`（判定应降级）。

### 算法：`segmentProtectedByStableTitleStyleCluster(seg, orderedBlocks, profile)`
1. `profile==null || !profile.isReliable() || orderedBlocks==null || seg 为空`，返回 `false`。
2. 统计段内每条对应 `orderedBlocks` 位置的 `profile.roleOf(block)`：`H1`/`H2` 计入 `h1h2`；`BODY` 计入 `body`；`NOISE` 计入 `noise`；其他（`UNKNOWN`）不计。索引越界或块为 `null` 立即返回 `false`。
3. `body>0 || noise>0` → `false`（只要出现任何正文/噪声角色的块，不算受保护）。
4. `h1h2 == seg.size()` → `true`（全部是 H1/H2）。
5. 否则返回 `h1h2 >= (seg.size()+1)/2`（向上取整的"过半数"判定，即多数是 H1/H2 也算受保护）。

### 算法：`segmentIsBodyOrNoiseStyleRun(seg, orderedBlocks, profile, config)`（私有）
1. `profile==null || !profile.isReliable() || orderedBlocks==null || seg 为空`，返回 `true`（无剖面信息时默认放行，不阻止判定为短语清单）。
2. 对段内每条：索引越界或块为 `null` 返回 `false`；调用 `PdfToMarkdown.isBodyOrNoiseLikeForShortPhraseListRun(block, profile, cfg)`（外部方法，语义为"该块样式是否类正文/噪声，含 UNKNOWN 但字号与正文簇一致的情况"），任一为 `false` 则整体返回 `false`。
3. 全部通过返回 `true`。

### 算法：`segmentHasExpansionBetweenItems(seg, lines, config)`（私有）
1. `maxAllowedLines = config.shortPhraseNumberedRunMaxBodyLines`。
2. 对相邻两条：`countExpansionLinesBetween(lines, seg[i].lineId, seg[i+1].lineId, config) > maxAllowedLines` 即返回 `true`。
3. 全部不超返回 `false`。

### 算法：`eachItemHasTrailingExpansion(seg, lines, config)`（私有）
1. `STRUCTURAL_TITLE_MIN_EXPANSION_LINES = 1`（本方法专用常量）。
2. `lines==null || seg.size()<2` 返回 `false`。
3. 对**每一对**相邻条目（不含最后一项之后的内容）：若 `countExpansionLinesBetween(...) < 1`，**立即**返回 `false`（只要有一对之间没有实质展开，就不满足"每项都有展开"）。
4. 全部满足返回 `true`。

### 算法：`segmentLooksLikeStableHeadingRun(seg, lines)`（私有）
1. `lines==null || seg 为空` 返回 `false`。
2. `level=-1`；遍历每条：`lineId` 越界返回 `false`；取该行原文 `strip()` 后用 `HEADING_LINE` 全串匹配，不匹配返回 `false`；`lv = 捕获组1长度`（即 `#` 数量）；首次记录 `level=lv`，否则要求与已记录 `level` 相等，不等返回 `false`。
3. 全部一致返回 `true`。

### 算法：`segmentHasUniformMarkdownHeadingLevel(seg, lines)`（私有）
与 `segmentLooksLikeStableHeadingRun` 逻辑完全相同（两个独立方法，同一实现，分别用于 PLAIN 分支的"保留标题序列"判定与 EXISTING_HEADING 分支的"层级一致性"前置判定）。

### 算法：`segmentHasAnyExpansionBetweenItems(seg, lines, config)`（私有）
1. `lines==null || seg.size()<2` 返回 `false`。
2. 对每对相邻条目：`countExpansionLinesBetween(...) > 0` 即返回 `true`（任何展开都触发，无阈值容忍）。
3. 返回 `false`。

### 算法：`isChineseStructuralSectionTitle(norm)`
1. 空/全空白返回 `false`。
2. `t = strip()`。
3. 匹配 `^第\s*[一二三四五六七八九十百千万零\d]+\s*章.*` → `true`。
4. 匹配 `^[一二三四五六七八九十百千万零]+[、.．].*` → `true`。
5. 匹配 `^[（(][一二三四五六七八九十百千万零]+[)）].*` → `true`。
6. 否则 `false`。

### 算法：`bodyContainsStructuralSectionKeyword(body)`
1. 空/全空白返回 `false`。
2. 遍历 `STRUCTURAL_SECTION_KEYWORDS`，任一为 `body` 子串即返回 `true`。
3. 否则 `false`。

### 算法：`segmentProtectedByStructuralSectionKeywords(seg)`（私有）
1. `seg` 为空返回 `false`。
2. 遍历每条：`body = stripPrefix(normText, patternKey)`；若 `!bodyContainsStructuralSectionKeyword(body)`，**立即**返回 `false`（要求**全部**条目的去编号正文都含结构关键词才算受保护）。
3. 全部满足返回 `true`。

### 算法：`countExpansionLinesBetween(lines, fromLineId, toLineId)`（重载 1）
委托重载 2，`config = PdfToMarkdown.Config.defaults()`。

### 算法：`countExpansionLinesBetween(lines, fromLineId, toLineId, config)`（重载 2）
统计两个编号行之间的"实质展开行数"：
1. `lines==null || toLineId <= fromLineId+1`（区间内无行）返回 `0`。
2. `count=0`；遍历 `i ∈ (fromLineId, toLineId)`（开区间，不含端点）：
   a. 越界跳过。
   b. `norm = normalizeLine(lines.get(i))`；空跳过。
   c. 若 `norm` 以 `|` 开头 或 满足 `TABLE_SEPARATOR`，`count++`，`continue`（表格行算一次实质展开）。
   d. 若 `isSubstantiveBodyLine(norm, config)` 为真，`count++`。
3. 返回 `count`。

### 算法：`isSubstantiveBodyLine(norm, config)`（私有）
1. 以 `#` 开头返回 `false`（标题行不算"正文展开"，可能是紧跟的下一个标题）。
2. `norm.length() > config.shortPhraseNumberedBodyMaxLen` → `true`（超过短语最大长度即算实质正文）。
3. 含句读标点（正则 `.*[。！？；：,.!?;:].*`）→ `true`。
4. `parseNumberedLine(norm) != null` → `false`（本身是另一个编号行，不算展开正文，避免把下一条编号项误计为展开）。
5. 否则返回 `norm.length() >= 8`（长度达标也算实质内容）。

### 算法：`normalizeLine(line)`（私有，本类内部版本）
1. `null` 返回 `""`。
2. `strip()`；以 `#` 开头则去掉 `^#{1,6}\s*` 前缀并 `strip()`。
3. 返回结果（**注意**：与 `ListGuideHeuristics.normalizeLine` 不同，本方法不折叠内部空白、不处理全角空格、不去行尾 `#`，是更简化的版本）。

### 算法：`isShortPhraseBody(body, config)`（私有）
1. `body==null` 返回 `false`。
2. `s = strip()`；为空 或 `s.length() > config.shortPhraseNumberedBodyMaxLen` → `false`。
3. 含句读标点（同上正则）→ `false`。
4. 否则 `true`。

### 算法：`stripPrefix(text, patternKey)`
按 `patternKey` 用对应正则去掉编号前缀，返回纯正文：

| patternKey | 剥离正则 |
|---|---|
| `TITLE_NUM_DUNHAO` | `^\d+、\s*` |
| `TITLE_NUM_DOT` | `^\d+[.．]\s*` |
| `TITLE_NUM_SUFFIX` | `^\d+[)）】]\s*` |
| `TITLE_NUM_PAREN` | `^[（(]\s*\d+\s*[)）]\s*` |
| `TITLE_NUM_TOW` | `^\d+(?:\.\d+){1}\.?\s*` |
| `TITLE_NUM_THREE` | `^\d+(?:\.\d+){2}\.?\s*` |
| `TITLE_NUM_FOUR` | `^\d+(?:\.\d+){3}\.?\s*` |
| `TITLE_NUM_FIVE` | `^\d+(?:\.\d+){4}\.?\s*` |
| `TITLE_ROMAN` | `^[IVXLCDMivxlcdm]+\.\s*` |
| `TITLE_ALPHA` | `^[A-Za-z][.．]\s*` |
| 其他/`default` | 不剥离，原样返回 `text` |

`text == null` 直接返回 `""`。每种情形均用 `replaceFirst`（只替换第一次匹配，即行首前缀）。

### 算法：`seqQualityAdjacent(sorted)`
1. `n = sorted.size()`；`n < 2` 返回 `1.0`（单条或空默认满分）。
2. 统计相邻对中 `isSequential(sorted[i], sorted[i+1])` 为真的次数 `r`。
3. 返回 `r / (n-1)`（连续性比例）。

### 算法：`isSequential(a, b)`
与 `HeadingSequenceConsistencyHeuristics.isSequentialIndex` 逻辑完全相同：除末位外各维相等，末位 `b[last] == a[last]+1`。

### 算法：`meanLineGap(seg)`
1. `seg.size() < 2` 返回正无穷（`Double.POSITIVE_INFINITY`）。
2. 累加相邻 `lineId` 差值，除以 `(size-1)` 返回平均值。

### 算法：`looksLikeSectionTitleNumberedLine(patternKey, norm)`（重载 1）
委托重载 2，`lines = null`。

### 算法：`looksLikeSectionTitleNumberedLine(patternKey, norm, lines)`（重载 2）
判断一条编号行是否"像"节标题编号（而非清单项），用于把这类行**默认当作层级标题前缀**、不参与短语清单检测：
1. `patternKey`/`norm` 为空返回 `false`。
2. `body = sectionTitleBodyAfterNumericPrefix(patternKey, norm)`；`!PdfToMarkdown.looksLikeSectionTitleBody(body)`（外部方法，语义未在本文件定义，按调用签名记录：判断去编号正文本身是否"像节标题正文"，具体标准由 `PdfToMarkdown.java` 分片定义）→ `false`。
3. 若 `patternKey == "TITLE_NUM_DOT"`，直接返回 `true`（单级"1."编号，只要 `looksLikeSectionTitleBody` 通过就认；不再做后续的章节绑定检查）。
4. 若 `!isMultiLevelNumericSectionKey(patternKey)`（即不是 TOW/THREE/FOUR/FIVE 中任一），返回 `false`（其余 key 如中文序号、罗马、字母等一律不算节标题编号）。
5. `first = firstNumericSegment(norm)`（提取行首第一段数字，如"2.4.7"取"2"）；`<= 0` 返回 `false`。
6. 返回：`lines == null`（无上下文，直接放行）**或** `!documentUsesChapterHeading(lines)`（全文根本没用"第X章"结构，不需要章节绑定检查）**或** `first == 1`（章号为 1 时总是放行，即"1.xxx"系列默认当节标题）**或** `documentUsesChapterNumber(lines, first)`（全文确实存在与 `first` 相同编号的"第X章"，说明这个多级编号是在该章内的条款编号，放行）。

### 算法：`documentUsesChapterHeading(lines)`（包内可见）
1. `lines` 为空返回 `false`。
2. `inFence=false`，逐行遍历（跳过代码围栏内容）：`norm = trimmed 去 #{1,6} 前缀后 strip()`；若 `HeadingLevelPrefixHeuristics.classifyPrefixKey(norm) == "TITLE_CHAPTER_ONE"`，返回 `true`。
3. 遍历完未命中返回 `false`。

### 算法：`documentUsesChapterNumber(lines, chapterNumber)`（私有）
1. `lines` 为空或 `chapterNumber <= 0` 返回 `false`。
2. `inFence=false`，逐行遍历（跳过围栏）：`norm = 去前缀后 strip()`；`classifyPrefixKey(norm) != "TITLE_CHAPTER_ONE"` 跳过；用正则 `^第\s*([一二三四五六七八九十百千万零\d]+)\s*章.*` 全串匹配提取章号文本，调用 `mpp.MarkdownTitlePattern.parseNum(捕获组1)`（外部方法，中文数字/阿拉伯数字转整数），若解析成功且等于 `chapterNumber`，返回 `true`。
3. 遍历完未命中返回 `false`。

### 算法：`isMultiLevelNumericSectionKey(patternKey)`（私有）
`switch`：`TITLE_NUM_TOW`/`TITLE_NUM_THREE`/`TITLE_NUM_FOUR`/`TITLE_NUM_FIVE` → `true`；其他 → `false`。

### 算法：`firstNumericSegment(norm)`（私有）
用正则 `^(\d+)` 在 `norm.strip()` 上 `find()`，命中返回解析的整数，否则返回 `0`。

### 算法：`sectionTitleBodyAfterNumericPrefix(patternKey, norm)`（包内可见）
1. 任一为空返回 `""`。
2. `t = strip()`。
3. `patternKey == "TITLE_NUM_DOT"`：用 `^\d+\.\s*` 替换为空并 `trim()`。
4. `patternKey ∈ {TOW,THREE,FOUR,FIVE}`：用正则 `^(\d+(?:\.\d+)+)\.?\s*(.*)$` 全串匹配，匹配则取捕获组 2 并 `trim()`，否则返回 `""`。
5. 其他 key 返回 `""`。

### 算法：`parseNumberedLine(norm)`（包内可见）
1. 空/全空白返回 `null`。
2. 按本类 `PATTERN_DEFS` 列表顺序全串匹配，命中即用对应 `indexParser` 解析出 `index[]`；非空则返回 `(key, index)`。
3. 全部不命中返回 `null`。

### 算法：`splitDotInts` / `parseRoman`
与 `HeadingSequenceConsistencyHeuristics` 中的同名私有方法逻辑完全相同（各自独立实现，逐字符相同）。

### 调用方
`PdfToMarkdown.java`：
- 主流程（约 245 行）：`ShortPhraseListRunHeuristics.detectPdfShortPhraseListRuns(orderedTextBlocks, config, headingStyleProfile)` 是在构建完 `HeadingStyleProfile` 之后立即调用的第一道"短语清单"压制检测，结果 `shortPhraseListRunBlockIds` 会传给后续的 `detectHeadingSequenceConsistencyDemoteBlockIds`（作为其判定函数的排除信号之一）。
- 测试辅助方法（约 728 行，`renderTextBlockBodyForTest`）：同样调用 `detectPdfShortPhraseListRuns`，供单元测试渲染单个文本块。
- 行内判定（约 854 行）：`ShortPhraseListRunHeuristics.parseNumberedLine(normalizedText) != null` 用作某处快速判断"这行是否为编号行"的短路条件（具体上下文属于 `PdfToMarkdown.java` 拆分范围，此处仅记录调用签名）。

---

## MarkdownStructureRules

### 职责
汇总"文档结构判定规则"，作为 PDF 转换与标题后处理共用的一组门面方法（Facade），本身**基本不含新逻辑**，大部分方法直接委托给 `ChapterTocLineRemover`、`HeadingSequenceConsistencyHeuristics`、`PdfToMarkdown`、`InlinePipeTableSplit`。文档注释里引用了外部文档 `doc/pdf2md.md`、`doc/title_extract_new.md` 的章节编号（§3.1/§3.2/§4.2/§5.2），这些文档不在本次读取范围内，仅按代码行为记录。

### 常量与正则

| 名称 | 值 | 说明 |
|---|---|---|
| `TERMINAL_PUNCTUATION` | 字符串 `"。！？；：，、.,!?;:"` | §5.2：判定"终止标点"的字符集合（不是正则，是普通字符串，用 `indexOf` 逐字符比对） |

### 算法：`endsWithTerminalPunctuation(text)`
1. 空/`null` 返回 `false`。
2. 返回 `TERMINAL_PUNCTUATION.indexOf(text.charAt(text.length()-1)) >= 0`（末字符是否在终止标点集合中）。

### 算法：`isChapterTableOfContentsEntry(line)`
直接委托 `ChapterTocLineRemover.isChapterTocLine(line)`。

### 算法：`isTitleExtractCandidateLine(normalizedLine)`
判定一行是否"允许"作为标题抽取候选（§4.2+§5.2+pdf2md§3.2 综合判定，**不含**模式匹配本身）：
1. 空/全空白返回 `false`。
2. 若 `HeadingSequenceConsistencyHeuristics.isSectionTitleNumberedLine(normalizedLine)` 为真，**立即**返回 `true`（像节标题编号的行直接放行，跳过后续终止标点/列表项检查——即使以句号结尾等情形也放行；这是一条优先级最高的短路规则）。
3. `endsWithTerminalPunctuation(normalizedLine)` 为真 → `false`。
4. `isChapterTableOfContentsEntry(normalizedLine)` 为真 → `false`。
5. 返回 `!isOrderedListItemLine(normalizedLine)`。

### 算法：`isOrderedListItemLine(line)`
直接委托 `PdfToMarkdown.isListItem(line)`（外部方法，pdf2md §3.2 定义的有序列表项判定，含字段标签形态如"1.项目编号：…"，具体规则属 `PdfToMarkdown.java` 拆分范围）。

### 算法：`hasEmbeddedPipeTable(line)`
直接委托 `InlinePipeTableSplit.findInlinePipeTableStart(line) >= 0`（外部类，本次读取范围外，仅记录调用签名：返回值为管道表起始位置索引，`< 0` 表示未找到）。

### 算法：`splitEmbeddedPipeTableLines(line)`（单行版）
直接委托 `InlinePipeTableSplit.splitLineIfNeeded(line)`。

### 算法：`splitEmbeddedPipeTableLines(lines)`（列表版）
直接委托 `InlinePipeTableSplit.splitMarkdownLines(lines)`。

### 调用方
`PdfToMarkdown.java`：
- 表格拆分（约 612、979、4229 行）：`splitEmbeddedPipeTableLines` 用于把"标签+管道表粘连"的行拆开。
- 块类型判定（约 1185 行）：`t.startsWith("|") || MarkdownStructureRules.hasEmbeddedPipeTable(t)` 判定该行属于 `BlockKind.TABLE`。
- 标题候选排除链（约 3278-3289 行）：`endsWithTerminalPunctuation`、`isOrderedListItemLine`、`isChapterTableOfContentsEntry` 三连检查，任一命中即排除该行不作为标题候选。

### Go 移植提示
本类因为几乎全是委托，Go 移植时**不需要**对应生成一个单独的 "structure rules" 文件；直接在调用方按需调用底层函数即可（详见文末包组织建议），除非团队希望保留一个门面层便于对照 Java 源码逐行核查。若保留门面，`TERMINAL_PUNCTUATION` 应实现为一个 `strings.ContainsRune` 判断，而不是正则。

---

## CodeFenceWriter

### 职责
把一组已被判定为"代码/SQL/命令行/日志/配置块"的文本行包成一个 ` ``` ` 围栏代码块字符串，供 `WordToMarkdown`（不在本次拆分范围）与 `PdfToMarkdown` 的"单行单列表格降级为文本"路径共用。围栏字符数按内容中最长连续反引号串+1 取值（至少 3 个），避免内容本身包含反引号导致代码块提前闭合。

包级可见性：`final class CodeFenceWriter`（无 `public` 修饰，包内私有工具类）。

### 常量与正则

| 名称 | 值/模式 | 说明 |
|---|---|---|
| `BACKTICK_RUN` | `` `+ `` | 匹配一个或多个连续反引号 |

### 算法：`wrap(lines)`
1. `body = String.join("\n", lines)`（把所有行用换行符拼接）。
2. `fence = "`".repeat(max(3, longestBacktickRun(body) + 1))`（围栏长度取"内容中最长反引号游程+1"与"3"的较大值）。
3. 返回 `fence + "\n" + body + "\n" + fence + "\n"`（前后各一道围栏，末尾额外带一个换行）。

### 算法：`longestBacktickRun(text)`（私有）
1. `longest = 0`。
2. 用 `BACKTICK_RUN` 反复 `find()`，每次取 `group().length()` 与 `longest` 的较大值。
3. 返回 `longest`。

### 调用方
`PdfToMarkdown.java`（约 3803 行，`appendSingleCellTableAsTextForTest`/相邻的单元格表格降级逻辑）：当 `MarkdownLineClassifier.looksLikePreformattedBlock(lines)`（外部方法，本次读取范围外，判定是否"看起来像预格式化块"）为真时，调用 `CodeFenceWriter.wrap(lines)` 把这些行包装为代码围栏输出；否则退化为普通空格拼接的一行文本。

---

## Go 包组织建议

1. **建议整体落在一个 Go 包**（如 `internal/pdfconvert/markdown` 或与负责 `PdfToMarkdown.java`/`mpp` 分片约定的公共包名），因为这 10 个类互相之间调用密集（`ChapterTocLineRemover` 被另外 6 个类直接引用；`HeadingLevelPrefixHeuristics.classifyPrefixKey` 被至少 5 个类使用），拆成多个 Go 包会产生大量循环依赖风险；用文件切分代替包切分：
   - `chapter_reference.go` ← `ChapterReferenceHeuristics`
   - `chapter_toc.go` ← `ChapterTocLineRemover`
   - `heading_level_prefix.go` ← `HeadingLevelPrefixHeuristics`
   - `heading_pattern_quality.go` ← `HeadingPatternQualityHeuristics`
   - `heading_sequence_consistency.go` ← `HeadingSequenceConsistencyHeuristics`
   - `heading_suppress.go` ← `HeadingSuppressHeuristics`
   - `list_guide.go` ← `ListGuideHeuristics`
   - `short_phrase_list_run.go` ← `ShortPhraseListRunHeuristics`
   - `structure_rules.go` ← `MarkdownStructureRules`（如决定保留门面层）
   - `code_fence.go` ← `CodeFenceWriter`

2. **可合并的重复小工具函数**（Java 里因包可见性/历史原因各自维护了同逻辑的多份拷贝，Go 移植时应收敛为共享函数，放在一个 `internal_helpers.go` 或直接放在 `heading_suppress.go`/`chapter_reference.go` 顶部，供其余文件调用）：
   - **`stripHashes`**：`ChapterReferenceHeuristics.stripHashes` 与 `HeadingSuppressHeuristics.stripHashes` 逻辑完全相同（同一正则 `^\s{0,3}#{1,6}[\s　]*`）。`HeadingPatternQualityHeuristics.stripHashes` 直接委托 `HeadingSuppressHeuristics.stripHashes`。→ Go 只写一个 `stripHeadingHashes(line string) string`。
   - **多级数字编号解析**（`(patternKey, index[])` 结构）：`HeadingSequenceConsistencyHeuristics.parseSequenceLine`/`PATTERN_DEFS`（不含负向先行）与 `ShortPhraseListRunHeuristics.parseNumberedLine`/`PATTERN_DEFS`（含负向先行）**几乎相同**，唯一区别是后者的 FIVE/FOUR/THREE/TOW/DOT 五个模式带负向先行断言、前者不带。Go 实现建议：写一个共享的 `parseNumberedPrefix(norm string, strictBoundary bool) (patternKey string, index []int, ok bool)`，`strictBoundary=true` 时对 FIVE/FOUR/THREE/TOW/DOT 额外做"下一字符不能是 `.`/`．`/数字/`-`"的手工校验（即前文断言替代逻辑），`false` 时跳过该校验；两个调用点各自传参数即可，避免维护两份几乎相同的正则表与两份 `splitDotInts`/`parseRoman`/`parseChineseNumber`。
   - **`splitDotInts`、`parseRoman`**：在 `HeadingSequenceConsistencyHeuristics` 与 `ShortPhraseListRunHeuristics` 中各自私有实现一份，逻辑完全相同。合并到共享函数后自然去重。
   - **`isSequential`/`isSequentialIndex`**：`ShortPhraseListRunHeuristics.isSequential(Entry,Entry)` 与 `HeadingSequenceConsistencyHeuristics.isSequentialIndex(int[],int[])` 本质是同一个"数组末位+1"算法，只是参数形态不同（一个直接传 index 数组，一个从 Entry 取 index 再比较）。Go 里写一个 `isSequentialIndex(a, b []int) bool`，两处按需从各自的结构体里取 `Index` 字段调用。
   - **`HeadingLevelPrefixHeuristics.PREFIX_DEFS`（含负向先行的 5 个数字模式）与 `ShortPhraseListRunHeuristics.PATTERN_DEFS`（同样含负向先行的 5 个数字模式）**：字符级相同的正则文本，应共用同一组 Go 正则变量与同一个"负向先行替代校验"辅助函数（如 `numericPrefixBoundaryOK(s string, matchEnd int) bool`：检查 `s[matchEnd]`（若存在）是否为 `.`/`．`/`0-9`/`-`，是则返回 `false`）。
   - **`ListGuideHeuristics.normalizeLine` vs `HeadingSequenceConsistencyHeuristics`/`ShortPhraseListRunHeuristics` 内部各自的 `normalizeLine`**：三者**不完全相同**（`ListGuideHeuristics` 版本额外做全角空格折叠、去行尾 `#`；其余两处只去行首 `#`），**不要**贸然合并，需保留三个语义不同的函数（可用不同函数名如 `normalizeLineFull`（ListGuide 版）与 `normalizeLineHeadPrefixOnly`（其余两处共用）以示区分）。

3. **`Entry`/`SequenceEntry`/`ParsedSequence`/`ParsedNumbered`/`PatternDef` 等 Java `record`**：建议在 Go 中定义为不导出的 `struct`（如 `numberedEntry{lineID int; normText string; patternKey string; index []int}`），`HeadingSequenceConsistencyHeuristics` 与 `ShortPhraseListRunHeuristics` 可共用同一个结构体类型（字段完全一致），进一步减少重复代码。

4. **正则预编译**：所有 `Pattern.compile` 常量应在 Go 中作为包级 `var xxxRe = regexp.MustCompile(...)` 一次性编译，与 Java `static final Pattern` 语义一致；含负向先行断言的正则去掉断言部分后编译，配合手工校验函数使用（见「Go regexp 兼容性预警」）。

5. **`MarkdownHeadingHit` 的可变字段（`level`/`lineIndex`）**：Java 版本里 `HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency`/`demoteHeadingLine` 直接修改传入对象的字段（引用语义）。Go 中若用值类型 `slice` 存储 `struct`，需要通过索引或指针（`[]*MarkdownHeadingHit`）修改，避免值拷贝导致修改丢失——这是移植时最容易踩的坑之一，建议 `mpp` 分片的 Go 结构体定义直接采用指针切片。

6. **配置加载（`HeadingPatternQualityHeuristics.loadMaxHeadingLength`）**：Java 版本用 classpath 资源 + 系统属性双源合并，属于 fileview 遗留的配置基础设施模式；Go 移植不应照搬 Java Properties 文件加载机制，应改为从 wiki-brain 既有的配置结构（`config.yml`/`internal/foundation/config`）注入 `maxHeadingLength` 参数，默认值保持 `80`，最小钳制 `8`。
