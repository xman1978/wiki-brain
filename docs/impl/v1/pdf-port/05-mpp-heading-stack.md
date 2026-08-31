# PDF 移植 Part 5：MPP 标题识别流水线（文本后处理）

范围：`internal/.../mpp` 包中「标题识别」半条链路——扫描已产出的纯文本 Markdown 行（不再有 PDF 版式/bbox 信息），推断标题层级，与检测到的目录（TOC）交叉校验，产出最终 `#` 标题标记与文首目录块。对应 Java 源文件（14 个，全部已完整读取）：

- `MarkdownHeadingStage.java`（1685 行，核心）
- `MarkdownPatternKey.java`
- `MarkdownTitlePattern.java`
- `MarkdownHeadingHit.java`
- `MarkdownLineKind.java`
- `MarkdownLineRange.java`
- `MarkdownPipelineStage.java`
- `MarkdownPipelineContext.java`
- `MarkdownPipelineLineUtils.java`
- `UnmarkedParentHeadingHeuristics.java`
- `ChapterTocCatalog.java`
- `ChapterTocHeadingValidator.java`
- `HeadingReadingOrderValidator.java`
- `MarkdownTocEmitStage.java`

`MarkdownHeadingStage` 调用了以下 Java 顶层类，**均不在本文档范围内**（由其他 Part 覆盖），此处只标注调用点与用途，不重复其内部实现：

| 类 | 归属（推测） | 在本文件中的调用点/用途 |
|---|---|---|
| `ChapterReferenceHeuristics` | Part 1-3 | `isBodyChapterReference`：判断一行是不是正文里对「第 X 章」的引用（而非章节标题本身），用于既有标题反证降级、目录候选过滤 |
| `ChapterTocLineRemover` | Part 1-3 | `isChapterTocLine`/`toStructuralChapterHeadingFromTocLine`/`isChapterPrefixOnlyLine`/`isLikelyChapterTitleNameLine`/`isStructuralChapterHeading`/`normalizeGluedStructuralChapterHeading`/`stripLines`：PDF 目录页行识别与结构化章节标题识别，供 `ChapterTocCatalog` 扫描目录、供 `filterCandidates`/`extractCandidates` 判断候选是否为结构性章节标题 |
| `HeadingLevelPrefixHeuristics` | Part 4 | `normalizeForHeadingPrefixMatch`（供 `MarkdownTitlePattern` 正则匹配前的归一化）、`classifyPrefixKey`（把一行文本归类为某个 `MarkdownTitlePattern` 名字字符串）、`applyLevelPrefixConsistency`（阶段 2 末尾的前缀一致性层级修正，本文件直接调用但不展开其算法） |
| `HeadingPatternQualityHeuristics` | Part 4 | `buildInferDisqualifiedPatternKeys`/`detectDisqualifiedPatternKeys`/`detectMixedRecognitionPatternKeys`/`filterHitsAndDemoteLines`/`clearlyFailsHeadingQuality`：标题模式的全局质量判定（某个模式在全篇范围内是否可信），本文件在 `apply` 主流程中多处调用其结果做过滤 |
| `HeadingSequenceConsistencyHeuristics` | Part 4 | `detectMarkdownLinesToDemote`/`isSectionTitleNumberedLine`：连续编号序列一致性判定 |
| `HeadingSuppressHeuristics` | Part 4 | `shouldSuppressHeadingLine`/`previousNonBlankLine`/`looksLikeCnArticleBodyParagraphLead`/`stripHashes`/`isStandaloneHeadingLine`：标题抑制规则集 |
| `MarkdownStructureRules` | Part 1-3 | `endsWithTerminalPunctuation`/`isTitleExtractCandidateLine`：行是否以句末标点收尾、是否具备成为标题候选的基本资格 |
| `ShortPhraseListRunHeuristics` | Part 4 | `detectExistingHeadingShortPhraseListRuns`/`supportsPatternKey`/`looksLikeSectionTitleNumberedLine`/`detectPlainShortPhraseListRuns`/`Entry`：短语式清单连续段识别 |
| `PdfToMarkdown` | Part 1-3（几何渲染主类） | `loadConfigOrDefaults()`/`Config`（复用其配置常量）、`looksLikeSectionTitleBody`（判断编号前缀后的正文是否像节标题）、`legacyHeadingLevel`（仅注释提及，未直接调用） |
| `MarkdownNoiseCleanupStage` | Part 6（推测） | `isInAnyScope(lineIndex, scopes)`：静态工具方法，判断某行是否落在给定的 `MarkdownLineRange` 列表内。阶段 1（噪声清理阶段）产出 `nonHeadingScopes`/`attachmentScopesForMerge` 并调用本类做扫描；本文件（阶段 2）复用同一工具方法过滤候选/命中 |
| `MarkdownWeakMergeHeuristics` | Part 6（推测） | `isTableLikeLine`/`isQuoteOrRuleLine`：行形态判断（表格样式行、引用/分隔线），用于既有标题反证降级 |
| `MarkdownLineClassifier` | Part 6（推测） | `classify(lines, i)` 返回 `MarkdownLineKind`：用于判断某行是否为 `PREFORMATTED`（配置注释误判防护） |

---

## Go regexp 兼容性预警

Go 标准库 `regexp`（RE2 引擎）不支持环视断言。以下正则在源码中使用了 lookahead/lookbehind，移植时必须改写。

### 1. `MarkdownPipelineLineUtils.NUMERIC_OUTLINE_BOUNDARY`

```java
public static final String NUMERIC_OUTLINE_BOUNDARY =
    "(?:\\s+|$|(?=[\\p{L}\\p{IsIdeographic}（(《]))";
```

含义：编号前缀（如 `1.` `1、`）后面的边界——要么跟空白，要么到行尾，要么后面紧跟一个字母/表意文字/中文括号/书名号开头字符（**不消费**该字符，零宽断言）。这是一个被拼进其他正则（`LIST_ITEM_LINE`）的**片段**，本身不是完整可编译正则。

Go workaround：不能作为字符串片段直接拼接进最终正则。改为两步：
1. 用不含该零宽断言的正则做前缀匹配（只匹配到编号本身，不含边界判断），得到匹配结束位置 `end`。
2. 在 Go 代码里检查 `end` 处的边界条件：`end == len(s)`，或 `s[end]` 是空白字符，或 `s[end:]` 的第一个 rune 属于字母/表意文字/`（`/`(`/`《` 集合。用 `unicode.IsLetter`、`unicode.Is(unicode.Han, r)`（或自建的表意文字判断集，因为 Go 没有内建 `\p{IsIdeographic}`，需要用 `unicode.RangeTable` 近似，建议直接用 `unicode.Han` 加上带covers CJK的自定义表）以及字符匹配 `（`/`(`/`《` 完成判断。

### 2. `MarkdownPipelineLineUtils.LIST_ITEM_LINE`

```java
public static final Pattern LIST_ITEM_LINE = Pattern.compile(
    "^\\s*(?:[-+*•●○■□►→★☆]\\s+|[（(]?" + NUMERIC_DOTTED_OUTLINE_PREFIX
        + "[)）\\]]?" + NUMERIC_OUTLINE_BOUNDARY
        + "|[（(]?\\d+[、)）\\]](?:\\s+|$|(?=[\\p{L}\\p{IsIdeographic}（(《]))"
        + "|[A-Za-z][\\.．]\\s+|[ivxlcdmIVXLCDM]+\\.\\s+).*$");
```

嵌入了两处零宽 lookahead（一处来自 `NUMERIC_OUTLINE_BOUNDARY` 展开，一处内联书写、内容相同）。

Go workaround：把整个正则拆成分支，每个分支去掉末尾的 `(?:\s+|$|(?=...))`，改为只匹配到「编号+可选右括号」为止；然后在 Go 里对每个候选分支单独尝试匹配前缀，匹配成功后手动检查上述边界条件（同上）。由于该正则本身用于「一整行是否为列表项」的布尔判断，可以写成一个 Go 函数 `isListItemLine(s string) bool`，内部枚举 5 个分支（bullet 符号、中文/数字点分编号、数字顿号/括号编号、字母编号、罗马数字编号），每个分支先用不含环视的正则匹配前缀部分，再手动判断紧跟字符的边界条件，全部满足才返回 true。不需要保留“一个正则搞定一切”的写法。

### 3. `MarkdownPipelineLineUtils.NUM_LEVEL2_PREFIX`

```java
public static final Pattern NUM_LEVEL2_PREFIX = Pattern.compile("^(\\d+)\\.(\\d+)(?!\\.)(?:\\s*.*)$");
```

`(?!\.)` 确保第二段数字后面不是再跟一个点（避免把 `1.2.3` 误判为二级 `1.2`）。

Go workaround：正则本身除环视外可直接编译（Go 支持 `(\d+)\.(\d+)`）。用 `regexp.FindStringSubmatchIndex` 拿到 `group(2)` 的结束位置 `end2`，然后检查 `end2 < len(s) && s[end2] == '.'`（或全角 `．`，注意源串统一后可能仍含全角点，源码里用 `[.．]`，Go 应同时检查两种点号）——若为 true 则整体判定为不匹配（因为存在第三段），否则视为匹配。

### 4. `MarkdownPipelineLineUtils.EMBEDDED_LEVEL2_PREFIX`

```java
public static final Pattern EMBEDDED_LEVEL2_PREFIX = Pattern.compile("(?<!\\d)(\\d+\\.\\d+)(?!\\d)");
```

用于在整段文本中查找「不被更多数字包围」的 `n.m` 子串（避免匹配到 `12.34.5` 中间的 `2.34` 这类误切）。含负向 lookbehind 与负向 lookahead。

Go workaround：先用宽松正则 `\d+\.\d+` 做 `FindAllStringIndex`，对每个匹配区间 `[start,end)`：
- 若 `start > 0` 且 `s[start-1]` 是数字字符（用 rune 检查 `unicode.IsDigit`，注意要按 rune 而非 byte 取前一字符，因为可能有多字节字符在前，需要用 `utf8.DecodeLastRuneInString(s[:start])`），则跳过该匹配（相当于 lookbehind 失败）。
- 若 `end < len(s)` 且 `s[end]`（用 `utf8.DecodeRuneInString(s[end:])`）是数字，则跳过该匹配。
- 否则保留为一个合法命中。

### 5. `MarkdownTitlePattern` 中的 `TITLE_NUM_TOW/THREE/FOUR/FIVE`

```java
TITLE_NUM_FIVE(Pattern.compile("^(\\d+(?:\\.\\d+){4})(?:[.．])?(?![.．\\d-])\\s*.*"));
TITLE_NUM_FOUR(Pattern.compile("^(\\d+(?:\\.\\d+){3})(?:[.．])?(?![.．\\d-])\\s*.*"));
TITLE_NUM_THREE(Pattern.compile("^(\\d+(?:\\.\\d+){2})(?:[.．])?(?![.．\\d-])\\s*.*"));
TITLE_NUM_TOW(Pattern.compile("^(\\d+(?:\\.\\d+){1})(?:[.．])?(?![.．\\d-])\\s*.*"));
```

每条都用 `(?![.．\d-])` 确保编号数字段（含可选收尾点）后面**不是**再一个点/全角点/数字/连字符——避免 `1.2.3.4` 被 `TITLE_NUM_TOW`（`1.2`）截断匹配、或 `1.2-3` 之类被误判。

Go workaround：正则去掉 `(?![.．\d-])` 后可直接编译为 `^(\d+(?:\.\d+){k})([.．])?`（k=1..4），用 `FindStringSubmatchIndex` 拿到整体匹配结束位置 `end`（即可选收尾点之后），再手动检查：`end < len(s)` 时看 `s[end]`（按 rune）是否为 `.`、`．`、数字、或 `-`；若是则判定为不匹配该模式，落给更长的模式（`TITLE_NUM_FIVE` 等）或彻底不匹配。由于 `MarkdownTitlePattern.matchFirst` 是按 `PATTERN_PRIORITY` 顺序试到第一个 `matches()` 为 true 的模式（且 Java 用的是 `matches()`——整串匹配，不是 `find()`），Go 侧要用 `regexp.MustCompile("^...$")`（锚定整行）配合上面的手动环视检查，逻辑等价于：先看是否整行都被「编号+边界后缀」结构占满。

注意：由于 Java 用 `.matches()`（隐式整行锚定），而这四条模式还带有 `\s*.*` 吞掉剩余内容，所以其实相当于「只要前缀不被更长数字串打断，整行都算命中」。Go 移植时可以简化为：先用不含环视的锚定正则匹配前缀 `^(\d+(?:\.\d+){k})([.．])?`，成功后检查上面第 `end` 位置的字符，通过则视为整行命中（因为剩余部分 `\s*.*` 总能匹配任意内容，包括空串）。

### 6. `MarkdownTitlePattern.TITLE_NUM_DOT`

```java
TITLE_NUM_DOT(Pattern.compile("^(\\d+)[.．](?!\\d|-)\\s*.*"));
```

`(?!\d|-)` 确保点号后不是数字或连字符（避免匹配 `1.2` 里的 `1.` 或 `1-2` 误判）。

Go workaround：与上面同样的思路——用 `^(\d+)[.．]` 匹配后取结束位置，检查该位置字符是否为数字或 `-`，是则整体判定不匹配 `TITLE_NUM_DOT`（会被更靠前优先级的 `TITLE_NUM_TOW` 等模式命中，因为 `PATTERN_PRIORITY` 数组里 `TITLE_NUM_TOW` 排在 `TITLE_NUM_DOT` 之前）。

### 汇总表

| 正则/常量 | 文件 | 环视类型 | Go 处理方式 |
|---|---|---|---|
| `NUMERIC_OUTLINE_BOUNDARY` | MarkdownPipelineLineUtils | 正向 lookahead（片段） | 拆成「匹配前缀 + 手动检查后续字符」 |
| `LIST_ITEM_LINE` | MarkdownPipelineLineUtils | 正向 lookahead ×2（内联） | 按分支枚举 + 手动边界检查，写成布尔函数而非单一正则 |
| `NUM_LEVEL2_PREFIX` | MarkdownPipelineLineUtils | 负向 lookahead | 匹配后检查下一字符是否为点号 |
| `EMBEDDED_LEVEL2_PREFIX` | MarkdownPipelineLineUtils | 负向 lookbehind + 负向 lookahead | `FindAllStringIndex` 后逐个校验前后字符 |
| `TITLE_NUM_TOW/THREE/FOUR/FIVE` | MarkdownTitlePattern | 负向 lookahead | 匹配前缀后检查结束位置字符 |
| `TITLE_NUM_DOT` | MarkdownTitlePattern | 负向 lookahead | 同上 |

共 **6 处**正则/正则片段使用了环视断言，需要人工改写为「匹配 + 手动边界检查」模式；均已给出具体做法。

---

## MarkdownPipelineContext（共享状态）

四阶段流水线（未在本 Part 范围内的阶段 1「噪声清理」、阶段 3「正文合并」、阶段 4「目录生成与输出」——阶段 4 的 `MarkdownTocEmitStage` 属于本 Part）共用的可变数据总线。字段写入时序（按流水线顺序）：

| 阶段 | 写入字段 |
|---|---|
| 阶段 1（噪声清理，Part 6 推测） | `lines`（初始）、`chapterTocCatalog`、`nonHeadingScopes`、`attachmentScopesForMerge`、`pageRemovedOutputAnchors` |
| 阶段 2（本 Part：`MarkdownHeadingStage`） | `hits`、`hierarchyLineIndexes`、`finalDisqualifiedPatternKeys`、更新后的 `lines`（写入 `#` 标题） |
| 阶段 3（正文合并，Part 6 推测） | 合并后的 `lines` |
| 阶段 4（本 Part：`MarkdownTocEmitStage`） | `resultMarkdown` |

完整字段清单（含构造参数与全部 getter/setter）：

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `generateToc` | `boolean`（构造参数，不可变） | — | 是否在最终输出前插入 `## 目录` 链接块 |
| `scannedSource` | `boolean`（构造参数，不可变） | `false`（单参构造函数） | 输入是否来自扫描件/OCR 通道；仅当为 true 时阶段 3 才会对正文做「重复页眉页脚」全文去重；见字段上的详细注释（原生 PDF/Word 文本抽取已用结构化信息剔除真页眉页脚，为 false 时不应对正文做全文重复计数去重，避免误删逐字重复的正文，如运维文档中重复的操作命令） |
| `lines` | `List<String>` | `new ArrayList<>()` | 当前处理中的行数组，各阶段原地/整体替换。`initLinesFromMarkdown` 用 `\n` 切分（先把 `\r\n`/`\r` 归一化为 `\n`，若含 OTSL 表格代码块则调用 `OtslToMarkdown` 转换——**该表格转换逻辑不在本 Part 范围，Go 侧留一个可插拔的 hook 位置**） |
| `pageRemovedOutputAnchors` | `Set<Integer>` | `Set.of()` | 阶段 1 产出（本 Part 只读取该字段名，未见 `MarkdownHeadingStage` 直接使用；供其他阶段消费） |
| `chapterTocCatalog` | `ChapterTocCatalog` | `ChapterTocCatalog.empty()` | 阶段 1 扫描出的章节目录快照，阶段 2 核心输入 |
| `nonHeadingScopes` | `List<MarkdownLineRange>` | `List.of()` | 阶段 1 标记的「非标题作用域」（如附件清单、列举引导），阶段 2 用于排除误识别 |
| `attachmentScopesForMerge` | `List<MarkdownLineRange>` | `List.of()` | 阶段 1 产出，供阶段 3 消费（本 Part 未直接使用） |
| `sourceMarkdownHeadingLineIndexes` | `Set<Integer>` | `Set.of()` | 阶段 2 入口时快照：原文中已带 `#` 的行号集合（用于目录章节提升后判断是否要整体加深层级） |
| `sourceMarkdownHeadingLevels` | `Map<Integer,Integer>` | `Map.of()` | 阶段 2 入口时快照：原文已带 `#` 的行号 → `#` 个数（写回时不低于用户原有层级） |
| `hits` | `List<MarkdownHeadingHit>` | `List.of()` | 阶段 2 定稿的标题命中列表 |
| `hierarchyLineIndexes` | `Set<Integer>` | `new HashSet<>()` | 阶段 2 定稿：最终确实作为层级标题写入 `#` 的行号集合（用于清理误留的非层级 `#`） |
| `finalDisqualifiedPatternKeys` | `Set<String>` | `Set.of()` | 阶段 2 产出：全文范围内被判定为「不可信」的模式键集合（字符串形式，来自 `HeadingLevelPrefixHeuristics.classifyPrefixKey` 命名空间） |
| `resultMarkdown` | `String` | `null` | 阶段 4 产出：最终 Markdown 全文 |

构造与初始化方法：
- `create(boolean generateToc)` → 等价于 `create(generateToc, false)`
- `create(boolean generateToc, boolean scannedSource)`
- `initLinesFromMarkdown(String markdown)`（包内可见）：
  1. 若 `markdown` 为 `null` 或空白，`lines = 空列表`，直接返回。
  2. 换行符归一化：`\r\n` → `\n`，再把残余 `\r` → `\n`。
  3. 若归一化文本包含 OTSL 表格标记（`OtslToMarkdown.containsOtslTable`），先用 `OtslToMarkdown.replaceOtslTableParagraphs` 替换（**该转换逻辑属于图片转 Markdown 后处理，不在本 Part 范围，Go 侧作为外部依赖注入点即可**）。
  4. 按 `\n` 分割（`split("\n", -1)`，保留末尾空字符串），存入 `lines`。
- `inputBlank()`（包内可见）：`lines == null || lines.isEmpty()`。

其余 setter（`setLines`/`setPageRemovedOutputAnchors`/…）均为包内可见，传 `null` 时回退到对应的空集合/空表默认值（`Set.of()`/`List.of()`/`Map.of()`），不允许字段本身为 `null`（除 `resultMarkdown` 允许 `null`）。

### Go struct 草案

```go
package mpp

type PipelineContext struct {
    generateToc   bool // 构造后只读
    scannedSource bool // 构造后只读

    Lines []string

    PageRemovedOutputAnchors map[int]struct{}
    ChapterTocCatalog        ChapterTocCatalog
    NonHeadingScopes         []LineRange
    AttachmentScopesForMerge []LineRange

    SourceMarkdownHeadingLineIndexes map[int]struct{}
    SourceMarkdownHeadingLevels      map[int]int

    Hits                       []*HeadingHit
    HierarchyLineIndexes       map[int]struct{}
    FinalDisqualifiedPatternKeys map[string]struct{}

    ResultMarkdown string
}

func NewPipelineContext(generateToc, scannedSource bool) *PipelineContext { ... }
func (c *PipelineContext) GenerateToc() bool   { return c.generateToc }
func (c *PipelineContext) ScannedSource() bool { return c.scannedSource }
func (c *PipelineContext) InitLinesFromMarkdown(markdown string) { ... }
func (c *PipelineContext) InputBlank() bool { return len(c.Lines) == 0 }
```

注：Go 无包私有 setter 概念（同包内直接赋字段即可），字段导出与否按「是否跨包读取」决定；若 Part 5/6 分属不同 Go 包，需要导出（大写）字段并提供 setter 保持与 Java `void set...` 语义一致的 nil→空集合兜底逻辑。

---

## MarkdownPipelineStage（接口）

```java
@FunctionalInterface
public interface MarkdownPipelineStage {
    void apply(MarkdownPipelineContext context);
}
```

单一方法接口：接收共享上下文，原地修改，无返回值。

### Go interface 草案

```go
type PipelineStage interface {
    Apply(ctx *PipelineContext)
}
```

由于 Go 没有匿名函数实现接口的隐式转换，若需要函数式用法可另加：

```go
type PipelineStageFunc func(ctx *PipelineContext)
func (f PipelineStageFunc) Apply(ctx *PipelineContext) { f(ctx) }
```

---

## MarkdownPipelineLineUtils

### 职责

跨阶段共享的行级工具函数与正则常量：行首尾归一化、Markdown 语法识别（标题行、表格分隔行、列表项、水平线）、目录/页码/附件相关正则、字符集统计（标点密度、句末标点、逗号计数）、配置文件加载（`maxHeadingLength`）。是 `MarkdownHeadingStage`、`UnmarkedParentHeadingHeuristics`、`ChapterTocCatalog`、`ChapterTocHeadingValidator` 等本 Part 全部文件的地基。

### 常量与正则

| 常量 | 正则/值 | 说明 |
|---|---|---|
| `EDGE_WHITESPACE` | `^[\s　]+|[\s　]+$` | 行首尾空白（含全角空格 `　`） |
| `SPACE_RUN` | `[\s　]+` | 连续空白（含全角空格），用于折叠为单个半角空格 |
| `LEADING_MD_HASH` | `^\s{0,3}#{1,6}[\s　]*` | 行首 ATX `#` 前缀（最多 3 个前导空格，1-6 个 `#`） |
| `LEADING_HEADING_PRIORITY_MARKERS` | `^[★☆●○■□►▶◆◇※▪▸√✓✔]+` | 层级标题前的强调符号字符集（是否剥离由 `HeadingLevelPrefixHeuristics` 判定，本文件只提供正则） |
| `TRAILING_MD_HASH` | `[\s　]*#{1,}[\s　]*$` | 行尾残留 `#`（闭合式 ATX 标题写法） |
| `HEADING_LINE` | `^(#{1,6})[\s　]+(.+?)[\s　]*$` | 完整 ATX 标题行：`group(1)`=`#`串，`group(2)`=标题正文（已去首尾空白，非贪婪） |
| `TABLE_SEPARATOR` | `^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?$` | Markdown 表格的分隔行（`---`/`:--:` 等） |
| `NUMERIC_OUTLINE_BOUNDARY` | 见上文环视预警 | 编号前缀后的边界片段（字符串常量，被拼接使用，非独立可编译正则） |
| `NUMERIC_DOTTED_OUTLINE_PREFIX` | `\d+(?:[.．]\d+)*(?:[.．])` | 形如 `1.` `1.2.` `1.2.3.` 的点分编号前缀（片段） |
| `NUMERIC_MULTI_LEVEL_OUTLINE_PREFIX` | `\d+(?:[.．]\d+)+(?:[.．])?` | 至少两段的点分编号（片段，末尾点号可选） |
| `NUM_LEVEL2_PREFIX` | 见环视预警 | 判断是否为「恰好二级」数字编号（`1.2` 但非 `1.2.3`） |
| `EMBEDDED_LEVEL2_PREFIX` | 见环视预警 | 在任意文本中查找孤立的 `n.m` 子串 |
| `LIST_SCOPE_ITEM_CANDIDATE` | `^(?:第\s*[一二三四五六七八九十百千万零\d]+\s*章.*\|[（(]?\d+[\.、)）\]]\s*.*\|[-+*•●○■□►→★☆]\s*.*)$` | 列表作用域候选行（章标题 / 数字编号 / 项目符号），供其他类判断「是否可能是列表项起始」 |
| `INVALID_SLUG_CHARS` | `[^\p{L}\p{N}\p{IsIdeographic}\-_\s]` | slug 化时要剔除的非法字符（Go 无 `\p{IsIdeographic}`，需用 `unicode.Han`/自定义表近似，见下方 slug 小节） |
| `DASH_RUN` | `-{2,}` | 连续连字符折叠 |
| `COLON_SPLIT` | `[:：]` | 半角/全角冒号 |
| `VERB_SINGLE_END` | `[做写看听说读]$` | 单字动词收尾（配合长度阈值 30 判断「像句子」） |
| `TOC_HEADING` | `^(?:#{1,6}\s*)?(?:目\s*录\|目录\|CONTENTS\|TABLE\s+OF\s+CONTENTS\|图目录\|表目录)\s*$`（忽略大小写） | 「目录」类标题行（可选带 `#`） |
| `TOC_MD_LINK_LINE` | `^\s*[-*+]\s+\[[^\]]+\]\(#.+\)\s*$` | Markdown 目录链接行 `- [标题](#slug)` |
| `TOC_PAGED_LINE` | `^\s*.+(?:(?:\.{2,}\|…{2,}\|·{2,}\|⋯{2,})\|(?:\t\|\s{2,})).*\d+\s*$` | 带引导点/多空格+页码收尾的目录行 |
| `LIST_ITEM_LINE` | 见环视预警 | 一整行是否为列表项（bullet/中文数字/阿拉伯数字/字母/罗马数字五种前缀之一） |
| `HORIZONTAL_RULE` | `^\s*(?:-{3,}\|\*{3,}\|_{3,})\s*$` | Markdown 水平分隔线 |
| `PAGE_NUMBER_LINE` | 三段可选分支：`第N页(共M页)?`、`(共)?N页(-/M页)?`、`- N -`/`— N —` 等 | 整行页脚页码；只在整行匹配时才算，降低误伤 |
| `ATTACHMENT_LIST_MAX_SCOPE_LINES` | `20`（int） | 附件清单作用域最大扫描行数 |
| `ATTACHMENT_ANCHOR` | `^附\s*件\s*[：:].*$` | 「附件：」引导行 |
| `OCR_MARGIN_ATTACHMENT_LABEL` | `^附\s*件(?:\s*\d+)?\s*$` | 无冒号的「附件」/「附件 N」独立行（OCR 场景） |
| `OCR_DOC_REFERENCE_YEAR` | 匹配 `〔2024〕`/`[2024]`/`（2024）`/`(2024)` 四种年份括注 | 红头文号行判定辅助 |
| `OCR_DOC_REFERENCE_MAX_LEN` | `40`（int） | 文号行最大长度 |
| `ATTACHMENT_ITEM_LINE` | `^\s*(?:[（(]?\d+[\.、)）\]]\s*.+\|[-+*•●○■□►→★☆]\s*.+)$` | 附件清单条目行 |
| `END_PARTICLES` | 字符集合 `{了,着,过,去,中,其,于,及,并}` | 判断句子是否以「动词性虚词」收尾 |
| `VERB_SUFFIXES` | 字符串集合（`完成 实现 达成 结束 到位 进行 开展 实施 执行 处理 通过 增加 减少 提高 降低 提升 下降 达到 讨论 商议 建议 要求 请求 告知`，共 21 个） | 判断句子是否以「动词短语」收尾 |

### 数据结构

无独立 struct/enum（全为静态工具方法与常量）。

### 算法：各方法

**`normalizeLine(String line) -> String`**
1. `stripEdgeWhitespace`（见下）去首尾空白。
2. 去掉行首 `LEADING_MD_HASH`（一次替换，`replaceFirst`）。
3. 去掉行尾 `TRAILING_MD_HASH`（一次替换）。
4. 再次 `stripEdgeWhitespace`。
5. 用 `SPACE_RUN` 把内部连续空白折叠为单个半角空格（`replaceAll`）。

**`endsWithTerminalPunctuation(String) -> boolean`**：直接委托 `MarkdownStructureRules.endsWithTerminalPunctuation`（不在本 Part 范围）。

**`endsWithAsciiOrFullwidthColon(String text) -> boolean`**：`text` 非空且最后一个字符是 `:` 或 `：`。

**`stripEdgeWhitespace(String text) -> String`**：`text` 为 `null`/空返回 `""`；否则用 `EDGE_WHITESPACE` 整体替换为空串（即去首尾空白，含全角空格）。

**`isBlankLine(List<String> lines, int idx) -> boolean`**：`idx` 越界（`<0` 或 `>=size`）视为空行（`true`）；否则 `normalizeLine` 结果是否为空。

**`containsAny(String text, String... parts) -> boolean`**：只要 `text` 包含任一 `parts` 元素即返回 `true`（短路遍历）。

**`hasLongTailAfterColon(String text, int limit) -> boolean`**：
1. 用 `COLON_SPLIT` 查找第一个冒号（`find()`，非整行匹配）。
2. 找不到冒号返回 `false`。
3. 取冒号之后的子串，`normalizeLine` 后取长度，若严格大于 `limit` 返回 `true`。

**`countCommas(String text) -> int`**：逐字符扫描，统计 `,` 与 `，` 的个数。

**`loadMaxHeadingLength() -> int`**：
1. 默认值 `80`。
2. 先从 classpath 资源 `config.properties` 加载（若存在，`IOException` 静默忽略保留默认）。
3. 再从系统属性 `config.file`（默认同名 `config.properties`）指定的文件路径加载并覆盖（若文件存在）。
4. 读取键 `pdf2md.maxHeadingLength`；为空/缺失返回默认值；否则 `Integer.parseInt` 后与 `8` 取 `max`（下限保护）；解析失败（`NumberFormatException`）返回默认值。
   - Go 移植提示：这是一个「启动期一次性读配置」的逻辑，Go 侧应在包初始化或显式 `LoadConfig` 时执行一次并缓存，不必每次调用都读文件；配置文件格式为 Java `Properties`（`key=value`，`#`/`!` 开头为注释），Go 可用简单的按行解析或复用项目已有的 config 加载机制（wiki-brain 用 YAML，此处提示实现者：这个 `pdf2md.maxHeadingLength` 配置项在 wiki-brain 里应该并入现有 `config/config.yml` 体系，而不是照搬 Java Properties 文件读取方式，具体桥接方式由整体移植负责人决定，本文档只描述原始语义）。

**`countSentencePunctuation(String text) -> int`**：`text` 为空返回 `0`；否则逐字符统计是否属于字符集 `，。；：、,.!?;:`（共 10 个字符：中文逗号/句号/分号/冒号/顿号 + 英文逗号/句号/叹号/问号/分号/冒号——注意源码字符串里同时含英文冒号 `:` 出现两次是笔误但不影响 `indexOf` 判断，Go 移植时字符集去重成一个 rune 集合即可：`，。；：、,.!?;:`）。

**`countNonSpaceChars(text) -> int`**：`text` 为空返回 `0`；否则统计非 `Character.isWhitespace` 的字符个数（Go 用 `unicode.IsSpace` 逐 rune 判断，注意按 rune 遍历而非 byte）。

**`endsWithVerbLike(String text) -> boolean`**：
1. `normalizeLine(text)`，空则 `false`。
2. 取最后一个字符，若属于 `END_PARTICLES` 集合，返回 `true`。
3. 若以 `VERB_SUFFIXES` 中任一后缀结尾，返回 `true`。
4. 若匹配 `VERB_SINGLE_END`（单字动词收尾）**且**归一化文本长度 `>30`，返回 `true`。
5. 否则用两条正则整体匹配兜底：`.*予以(奖励|处罚|说明)$` 或 `.*进行(分析|探讨|部署)$`（注意这两条是 `String.matches`，即整串匹配，Go 需要 `^.*...$` 锚定或直接用 `strings.HasSuffix` 判断更简单——因为 `.*` 前缀在 Go 里对整串匹配无实质意义，可简化为判断字符串是否以 `予以奖励`/`予以处罚`/`予以说明`/`进行分析`/`进行探讨`/`进行部署` 结尾）。

**`joinLines(List<String> lines) -> String`**：`String.join("\n", lines)`。

Go 侧全部函数应在 `mpp` 包内保持相同签名语义（用 `int`/`bool`/`string`/`[]string` 对应）。

---

## MarkdownPatternKey

### 职责

不可变值对象：`(MarkdownTitlePattern 类型, 深度 depth)` 的组合键，标识某个标题命中属于哪种编号体系、第几级深度。用于阶段 2 写入 `MarkdownHeadingHit.patternKey`，阶段 4（`MarkdownTocEmitStage`）用于过滤可写入目录的标题（只有 `patternKey != null` 的命中才进目录，见 `MarkdownTocEmitStage.buildToc`）。

### 数据结构

```go
type PatternKey struct {
    Type  TitlePattern
    Depth int
}
```

`equals`/`hashCode` 按 `(type, depth)` 值相等语义——Go 用 struct 值相等（`==`）天然满足，若用作 map key 直接可行（`TitlePattern` 需为可比较类型，如整数枚举）。

无算法，纯数据类。

---

## MarkdownTitlePattern

### 职责

标题编号体系的枚举定义：17 种模式（中文「第X章/节/纲/目/条」5 种、中文括号/顿号编号 2 种、五级点分阿拉伯数字编号 5 种、单级点号/顿号/括号后缀/括号包裹编号 4 种、罗马数字 1 种、字母编号 1 种），每种带匹配正则、`depth()`（多级编号的深度）、`supportListLike()`（是否参与 §6.1 高密度清单统计）。提供 `matchFirst`（按优先级找到第一个匹配的模式）、`parseIndex`（把编号文本解析为整数数组，供序列一致性/嵌套判断使用）、`stripBodyEnumerationPrefix`/`isBodyEnumerationPattern`（供正文枚举误判过滤）。

### 常量与正则（枚举全表）

| 枚举值 | 正则 | 语义 |
|---|---|---|
| `TITLE_CHAPTER_ONE` | `^第\s*([一二三四五六七八九十百千万零\d]+)\s*章.*` | 第X章 |
| `TITLE_CHAPTER_TOW` | `^第\s*([一二三四五六七八九十百千万零\d]+)\s*节.*` | 第X节（注：`TOW` 疑为 `TWO` 拼写笔误，保留原名） |
| `TITLE_CHAPTER_THREE` | `^第\s*([一二三四五六七八九十百千万零\d]+)\s*纲.*` | 第X纲 |
| `TITLE_CHAPTER_FOUR` | `^第\s*([一二三四五六七八九十百千万零\d]+)\s*目.*` | 第X目 |
| `TITLE_CHAPTER_FIVE` | `^第\s*([一二三四五六七八九十百千万零\d]+)\s*条.*` | 第X条 |
| `TITLE_CN_PAREN` | `^[（(]\s*([一二三四五六七八九十百千万]+)\s*[)）].*` | （一）/(一) |
| `TITLE_CN_NUM` | `^([一二三四五六七八九十百千万]+)[、.．\s].*` | 一、/一.（无「零」，注意与 `TITLE_CN_PAREN` 数字集一致，都不含「零」） |
| `TITLE_NUM_FIVE` | 见环视预警 | 五级点分数字 `1.2.3.4.5` |
| `TITLE_NUM_FOUR` | 见环视预警 | 四级点分数字 `1.2.3.4` |
| `TITLE_NUM_THREE` | 见环视预警 | 三级点分数字 `1.2.3` |
| `TITLE_NUM_TOW` | 见环视预警 | 二级点分数字 `1.2` |
| `TITLE_NUM_DOT` | 见环视预警 | 单级数字加点 `1.` |
| `TITLE_NUM_DUNHAO` | `^(\d+)、\s*.*` | 单级数字加顿号 `1、` |
| `TITLE_NUM_SUFFIX` | `^(\d+)[)）]\s*.*` | 单级数字加右括号 `1)`/`1）` |
| `TITLE_NUM_PAREN` | `^[（(]\s*(\d+)\s*[)）]\s*.*` | 数字括号包裹 `(1)`/`（1）` |
| `TITLE_ROMAN` | `^([IVXLCDM]+)\.\s*.*`（忽略大小写） | 罗马数字加点 `I.`/`ii.` |
| `TITLE_ALPHA` | `^([A-Za-z])[.．]\s*.*` | 单字母加点 `A.`/`a．` |

`PATTERN_PRIORITY` 数组（`matchFirst` 的尝试顺序，共 17 项，严格按此顺序）：
```
TITLE_CHAPTER_ONE, TITLE_CHAPTER_TOW, TITLE_CHAPTER_THREE, TITLE_CHAPTER_FOUR, TITLE_CHAPTER_FIVE,
TITLE_CN_PAREN, TITLE_CN_NUM,
TITLE_NUM_FIVE, TITLE_NUM_FOUR, TITLE_NUM_THREE, TITLE_NUM_TOW,
TITLE_NUM_DOT, TITLE_NUM_DUNHAO, TITLE_NUM_SUFFIX, TITLE_NUM_PAREN,
TITLE_ROMAN, TITLE_ALPHA
```

`supportListLike()`：除 5 种「第X章/节/纲/目/条」和 `TITLE_CN_PAREN`、`TITLE_CN_NUM` 之外（即除中文类模式）**全部**返回 `true`（数字/罗马/字母类都参与 list_like 统计）。逐条列出会更准确：`switch` 语句中 `case TITLE_CN_PAREN, TITLE_CN_NUM, TITLE_CHAPTER_ONE..FIVE -> false; default -> true`。也就是说不支持 list_like 统计的共 7 种（5 种章节类 + `TITLE_CN_PAREN` + `TITLE_CN_NUM`），其余 10 种都支持。

`depth()`：`TITLE_NUM_TOW→2`，`TITLE_NUM_THREE→3`，`TITLE_NUM_FOUR→4`，`TITLE_NUM_FIVE→5`，其余（含 `TITLE_NUM_DOT` 单级）默认 `1`。

### 数据结构

Go 枚举建议用有类型的 int 常量 + 关联数据表（因为每个枚举值需要绑定正则、`supportListLike`、`depth` 三项数据，且 Go 没有 Java 枚举的实例方法机制）：

```go
type TitlePattern int

const (
    TitleChapterOne TitlePattern = iota
    TitleChapterTow
    TitleChapterThree
    TitleChapterFour
    TitleChapterFive
    TitleCnParen
    TitleCnNum
    TitleNumFive
    TitleNumFour
    TitleNumThree
    TitleNumTow
    TitleNumDot
    TitleNumDunhao
    TitleNumSuffix
    TitleNumParen
    TitleRoman
    TitleAlpha
)

type titlePatternDef struct {
    match          *regexp.Regexp // 不含环视断言版本
    extraCheck     func(s string, loc []int) bool // 上文「Go workaround」里描述的手动环视校验，nil 表示无需额外校验
    supportListLike bool
    depth          int
}

var titlePatternDefs = map[TitlePattern]titlePatternDef{ /* 17 项 */ }

var patternPriority = []TitlePattern{
    TitleChapterOne, TitleChapterTow, TitleChapterThree, TitleChapterFour, TitleChapterFive,
    TitleCnParen, TitleCnNum,
    TitleNumFive, TitleNumFour, TitleNumThree, TitleNumTow,
    TitleNumDot, TitleNumDunhao, TitleNumSuffix, TitleNumParen,
    TitleRoman, TitleAlpha,
}
```

### 算法：`matchFirst(String line) -> MarkdownTitlePattern`

1. 用 `HeadingLevelPrefixHeuristics.normalizeForHeadingPrefixMatch(line)` 归一化（外部依赖，不在本 Part 范围，Go 侧直接调用 Part 4 提供的等价函数）。
2. 委托 `matchFirstOnNormalized`。

### 算法：`matchFirstOnNormalized(String norm) -> MarkdownTitlePattern`

1. `norm` 为 `null`/空白返回 `null`（Go 返回一个「无匹配」哨兵值，如 `-1` 或用 `(TitlePattern, bool)` 双返回值）。
2. 按 `PATTERN_PRIORITY` 顺序逐个尝试 `p.match.matcher(norm).matches()`（**整串匹配**），第一个命中即返回；含环视断言的模式按上文 workaround 处理（先前缀匹配再手动校验）。
3. 全部不命中返回 `null`。

### 算法：`parseIndex(String text, MarkdownTitlePattern type) -> int[]`

1. 归一化 `text`（同 `matchFirst`）。
2. 用 `type` 对应正则匹配（`matches()`），不匹配返回 `null`。
3. 按 `type` 分支解析：
   - 5 种章节类（`TITLE_CHAPTER_ONE..FIVE`）：`group(1)` 传给 `parseNum`（可能是阿拉伯数字或中文数字），失败返回 `null`，否则返回单元素数组。
   - `TITLE_CN_PAREN`/`TITLE_CN_NUM`：`group(1)` 传给 `parseChineseNumber`，失败返回 `null`，否则单元素数组。
   - `TITLE_NUM_TOW/THREE/FOUR/FIVE`：`group(1)` 按 `.` 分割（注意这里 split 用的是 `"\\."`，**不含全角点 `．`**——即使前面正则允许 `[.．]` 收尾，这里分割编号主体时假定编号段内部只用半角点分隔，是一个潜在的不一致但按原样保留），逐段 `Integer.parseInt` 组成数组。
   - `TITLE_NUM_DOT`/`TITLE_NUM_DUNHAO`/`TITLE_NUM_SUFFIX`/`TITLE_NUM_PAREN`：`group(1)` 直接 `Integer.parseInt`，单元素数组。
   - `TITLE_ROMAN`：`group(1)` 传给 `parseRoman`，失败返回 `null`，否则单元素数组。
   - `TITLE_ALPHA`：`group(1)` 首字符转大写，`ch - 'A' + 1` 作为单元素数组值。

### 算法：`parseNum(String text) -> Integer`

1. `text` 空白返回 `null`。
2. 若整串为阿拉伯数字（`\d+`），直接 `Integer.parseInt`。
3. 若整串为中文数字字符（`[一二三四五六七八九十百千万零]+`），委托 `parseChineseNumber`。
4. 否则返回 `null`。

### 算法：`parseChineseNumber(String token) -> Integer`

中文数字转阿拉伯数字（支持到「万」位）：
1. `token` 空白返回 `null`；剔除所有非中文数字字符后若为空返回 `null`。
2. 数字映射表：`零0 一1 二2 三3 四4 五5 六6 七7 八8 九9`。
3. 逐字符扫描，维护 `total`（万位以上累计）、`section`（当前「万」段内累计）、`number`（当前待应用的个位数，默认视作占位）：
   - 若字符是数字，记录到 `number`，继续下一字符（不立即应用）。
   - 若字符是单位字（十/百/千/万）：
     - `十→10, 百→100, 千→1000, 万→10000`；非法单位（未命中任何 case）直接返回 `null`。
     - 若单位是「万」：`section = (section + max(number,1)) * 10000`，累加进 `total`，`section` 和 `number` 清零。
     - 否则：`section += max(number,1) * unit`，`number` 清零。
4. 循环结束后 `result = total + section + number`；若 `result <= 0` 返回 `null`，否则返回 `result`。

注意：`max(number,1)` 意味着「十」单独出现时（`number` 尚为默认 0）按 1 处理，即「十」=10 而非 0；这是中文数字「十」可省略前导「一」的常见写法（「十一」=11，「十」=10 而非「一十」拼错时的兜底）。Go 移植需保留这个 `max(number,1)` 语义，`number` 默认值为 `0`。

### 算法：`parseRoman(String token) -> Integer`

标准罗马数字转整数（从右往左扫描，若当前值小于已记录的最大值则减，否则加）：
1. `token` 空白返回 `null`；转大写。
2. 单字符值表：`I1 V5 X10 L50 C100 D500 M1000`；遇到不在表中的字符直接返回 `null`。
3. 从字符串末尾向前遍历，维护 `sum` 与 `prev`（迄今见过的最大单值）：当前值 `cur < prev` 则 `sum -= cur`，否则 `sum += cur` 且 `prev = cur`。
4. `sum > 0` 才返回，否则 `null`。

（此算法**不做罗马数字合法性校验**，如 `IIII` 也能算出 4，与标准罗马数字规范不完全一致，按原样移植，不额外加校验。）

### 算法：`stripBodyEnumerationPrefix(String text, MarkdownTitlePattern pattern) -> String`

按 `pattern` 剥离编号前缀，取正文部分（用于「正文枚举误判」判断时看剥离前缀后的内容像不像标题）：
- `TITLE_NUM_TOW/THREE/FOUR/FIVE`：正则 `^(\d+(?:\.\d+)+)\.?\s*(.*)$` 匹配后取 `group(2).trim()`；不匹配则返回原文 `trim()`。
- `TITLE_NUM_DUNHAO`：`replaceFirst("^\\d+、\\s*", "")`。
- `TITLE_NUM_DOT`：`replaceFirst("^\\d+[.．]\\s*", "")`。
- `TITLE_NUM_SUFFIX`：`replaceFirst("^\\d+[)）】]\\s*", "")`（注意这里额外接受右书名号 `】`，与该模式定义正则里的 `[)）]` 不完全一致，属于宽松兜底，按原样保留）。
- `TITLE_NUM_PAREN`：`replaceFirst("^[（(]\\s*\\d+\\s*[)）]\\s*", "")`。
- `TITLE_CN_PAREN`：`replaceFirst("^[（(]\\s*[一二三四五六七八九十百千万]+\\s*[)）]\\s*", "")`。
- 其余模式：原样返回 `text`（`text` 为 `null` 时统一返回 `""`）。

### 算法：`isBodyEnumerationPattern(MarkdownTitlePattern pattern) -> boolean`

`pattern` 是否属于集合 `{TITLE_NUM_DUNHAO, TITLE_NUM_DOT, TITLE_NUM_SUFFIX, TITLE_NUM_PAREN, TITLE_CN_PAREN}`（这 5 种在正文中最常见被用作枚举列表而非标题，供 `markBodyEnumerationLists`/`filterCandidates` 使用）。

---

## MarkdownHeadingHit

### 职责

单条标题命中记录（阶段 2 产出，阶段 3-4 消费）。字段：`lineIndex`（行号，`final`）、`level`（层级，可变，供后续多轮修正）、`titleRaw`（标题原文，`final`）、`slug`（阶段 4 赋值的锚点 slug，可变）、`patternKey`（可为 `null`，`final`）、`scope`（`Object` 类型，**在本文件的全部读取路径中始终为 `null`，未见任何赋值为非 `null` 的调用点**——Go 移植可以直接省略该字段或保留为 `interface{}` 占位以兼容未来扩展，但当前无实际用途）。

### 数据结构

```go
type HeadingHit struct {
    LineIndex  int
    Level      int
    TitleRaw   string
    Slug       string
    PatternKey *PatternKey // nil 允许
    Scope      interface{} // 当前恒为 nil，保留字段位供未来扩展
}

func NewHeadingHit(lineIndex, level int, titleRaw string, patternKey *PatternKey, scope interface{}) *HeadingHit {
    return &HeadingHit{LineIndex: lineIndex, Level: level, TitleRaw: titleRaw, PatternKey: patternKey, Scope: scope}
}
```

无算法（纯数据类）。

---

## MarkdownLineKind

### 职责

包内可见枚举，描述一行在「阶段 3 正文合并」时的角色分类：`BLANK`/`FENCE`/`HEADING`/`LIST_ITEM`/`TABLE`/`QUOTE_OR_RULE`/`DATE`/`PREFORMATTED`（均阻断正文合并，`blocksBodyMerge()=true`）/`NATURAL_TEXT`（不阻断，`false`）。本 Part 唯一使用点：`MarkdownHeadingStage.isConfigDirectiveBeyondCommentRun` 调用 `MarkdownLineClassifier.classify(...)`（Part 6 范围）返回值与 `MarkdownLineKind.PREFORMATTED` 比较，用于「配置注释误判防护」。

### 数据结构

```go
type LineKind int

const (
    LineKindBlank LineKind = iota
    LineKindFence
    LineKindHeading
    LineKindListItem
    LineKindTable
    LineKindQuoteOrRule
    LineKindDate
    LineKindPreformatted
    LineKindNaturalText
)

func (k LineKind) BlocksBodyMerge() bool {
    return k != LineKindNaturalText
}
```

无算法。

---

## MarkdownLineRange

### 职责

半开行区间 `[startLine, endLine)`（阶段 1 写入，阶段 2 读取）。用于标记附件清单、列举引导等「非标题作用域」，供 `MarkdownHeadingStage` 排除误识别（`nonHeadingScopes`）。

### 数据结构

```go
type LineRange struct {
    StartLine int
    EndLine   int
}

func (r LineRange) Contains(lineID int) bool {
    return lineID >= r.StartLine && lineID < r.EndLine
}
```

（Java 版本本身未提供 `contains` 方法，是 `MarkdownNoiseCleanupStage.isInAnyScope` 与本文件内多处手写 `i >= scope.startLine && i < scope.endLine` 循环实现的；Go 移植建议补一个 `Contains` 方法统一收敛这些重复的手写循环，属于合理的工程收敛，不改变行为。）

---

## UnmarkedParentHeadingHeuristics

### 职责

处理 HTML/Word 转换常见的「章内小节被误导出为 `#`/`##`」问题：在**全部标题定稿并写入 `#` 之后**（标题定稿四步的第 3 步）调用，按行面状态做补偿降级——若某个已带 `#` 的子级小节（如 `三、`、`★（一）`、`1.1.`）上方存在**未标记**的常规父级（plain 文本形态的 `三、`、`1.` 等，没有 `#`），则把子级的 `#` 去掉，降回正文；若父级行本身也是应保留的 `#` 标题，则子级保持标题不变。

常规父子前缀关系表（`isConventionalParentPrefix`）：

| 子级 key | 允许的父级 key |
|---|---|
| `TITLE_CN_PAREN`（`（一）`） | `TITLE_CN_NUM`（`一、`）、`TITLE_CHAPTER_ONE..FIVE`（第X章/节/纲/目/条） |
| `TITLE_NUM_TOW`（`1.1`） | `TITLE_NUM_DOT`（`1.`）、`TITLE_CN_NUM`、`TITLE_CN_PAREN` |
| `TITLE_NUM_THREE`（`1.1.1`） | `TITLE_NUM_TOW` |
| `TITLE_NUM_FOUR` | `TITLE_NUM_THREE` |
| `TITLE_NUM_FIVE` | `TITLE_NUM_FOUR` |
| `TITLE_NUM_DOT`、`TITLE_NUM_DUNHAO` | `TITLE_CN_NUM`、`TITLE_CN_PAREN` |
| 其余 | 无（返回 `false`） |

可降级子级 key 集合（`isDemotableChildSectionKey`）：`{TITLE_CN_PAREN, TITLE_NUM_TOW, TITLE_NUM_THREE, TITLE_NUM_FOUR, TITLE_NUM_FIVE, TITLE_NUM_DOT, TITLE_NUM_DUNHAO}`。

纯数字编号 key 集合（`isPlainNumericSectionKey`）：`{TITLE_NUM_DOT, TITLE_NUM_TOW, TITLE_NUM_THREE, TITLE_NUM_FOUR, TITLE_NUM_FIVE}`。

### 数据结构

无独立 struct（静态方法集合，操作 `List<String> lines` 与 `List<MarkdownHeadingHit> hits`）。

### 算法：`demoteMisplacedSectionHeadings(lines, hits, demotedLineIndexes) -> List<MarkdownHeadingHit>`

主入口，四段处理：

**第一段**——扫描全文找「中文数字小节被错误提到章级」的情形：
1. 逐行扫描（跳过代码围栏 ``` 内），只处理已是 `#` 标题的行（`isMarkdownHeadingLine`）。
2. 取标题正文，`classifyPrefixKey` 分类，若 `key == null` 跳过。
3. 若 `key == "TITLE_CN_NUM"`（即 `一、` `二、` 形式）且满足 `isCnNumSectionDemotedUnderChapter`（见下），再检查其后 12 行内是否出现 `（一）` 类小节（`hasCnParenSectionFollowing`），满足则加入待降级集合 `toDemote`。

**第二段**——扫描全文找「常规子级前缀被误标为 `#`」的情形：
1. 逐行扫描已标 `toDemote` 的跳过；只处理 `#` 标题行。
2. 取标题正文分类 `childKey`；若 `childKey == null` 或不在 `isDemotableChildSectionKey` 集合中，跳过。
3. 调用 `shouldDemoteMisplacedChild(lines, lineId, childKey)`（见下），为真则加入 `toDemote`。

**第三段**——执行降级：对 `toDemote` 中每一行，`lines.set(lineId, headingTitleFromLine(...))`（去掉 `#` 前缀，写回纯文本），并记入 `demoted` 输出集合。

**第四段**——同步过滤 `hits` 列表：
1. `hits` 为空直接返回空表。
2. 遍历 `hits`，已在 `demoted` 中的行直接跳过（不进结果）。
3. 对每个未降级的命中，再次单独检查：
   - 若其 `titleRaw` 分类为 `TITLE_CN_NUM` 且满足 `isCnNumSectionDemotedUnderChapter`，则临时降级（写回行、加入 `demoted`），从结果中剔除。
   - 否则若分类为 `TITLE_CN_PAREN` 或 `isPlainNumericSectionKey`，且 `shouldDemoteMisplacedChild` 为真，同样临时降级并剔除。
   - 均不满足则保留进 `kept` 结果列表。
4. 返回 `kept`。

**为什么要有第四段**：第一、二段是按「行面当前状态」（`lines` 数组里已经写了 `#`）扫描的一次性判定；但调用方传入的 `hits` 列表是「逻辑命中」，可能在第一/二段扫描时序上还没被处理到（例如 `hits` 里某条记录对应的行在遍历顺序中排在判定它的父级之前）。第四段是对 `hits` 集合做的独立兜底复核，保证「降级」在两个数据视图（行面文本 + 命中列表）上最终一致。

### 算法：`isConventionalParentPrefix(String parentKey, String childKey) -> boolean`

见上表，`switch(childKey)` 按子级类型分支检查 `parentKey` 是否在允许集合中；`parentKey`/`childKey` 任一为 `null` 返回 `false`。

### 算法：`isUnderChapterHeading(lines, lineId) -> boolean`

从 `lineId-1` 向上找最近一个非空、非围栏、非空白的 `#` 标题行；若找到，判断其标题分类是否为 `TITLE_CHAPTER_ONE`（第X章）；找不到任何标题行则返回 `false`。

### 算法：`shouldDemoteMisplacedChild(lines, childLineId, childKey) -> boolean`

1. 参数校验：`lines`/`childKey` 为空或 `childLineId <= 0` 直接返回 `false`（注意：`childLineId==0` 时也直接返回 false，因为不可能有更上方的父级）。
2. 从 `childLineId-1` 向上逐行扫描（跳过代码围栏标记行与空行）：
   - 若该行是 `#` 标题行：取其分类 `pk`；若 `isConventionalParentPrefix(pk, childKey)` 为真——但要先检查这个父级本身是不是「已被判定为需要降级的中文数字小节」（`isCnNumSectionDemotedUnderChapter`），若是则**跳过继续向上找**（因为这个父级自己都不合法，不能作为子级的合法父级）；否则返回 `false`（找到合法的 `#` 父级，说明子级不需要降级，因为它已经有一个正当的标题父级）。
   - 若该行是 `#` 标题但分类前缀是 `TITLE_CHAPTER_*`（章节类，不满足上面的常规父子关系），直接返回 `false`（遇到章节级标题，说明中间没有合适的 plain 父级，不降级）。
   - 若该行不是 `#` 标题：检查是否为「未标记的合法小节标签行」（`plainSectionLabelTitle`），若是——取其分类 `parentKey`，返回 `isConventionalParentPrefix(parentKey, childKey)` 的结果（找到即判定，不再继续往上找）。
   - 否则（普通正文行）继续向上一行。
3. 循环到头（`i` 减到 0 以下）仍未命中任何分支，返回 `false`。

### 算法：`isCnNumSectionDemotedUnderChapter(lines, lineId, title) -> boolean`

1. `title` 分类不是 `TITLE_CN_NUM` 直接返回 `false`。
2. 否则要求三个条件同时成立：`isUnderChapterHeading(lines, lineId)`（上方最近标题是第X章）、`isValidSectionLabel(title)`（见下）、`hasCnParenSectionFollowing(lines, lineId)`（后续 12 行内有 `（一）` 类小节）。

### 算法：`plainSectionLabelTitle(lines, lineId) -> String`

1. 取该行原文；若为 `null`/空白，或该行本身已是 `#` 标题行，返回 `null`。
2. `isValidSectionLabel(raw)` 为假返回 `null`。
3. 分类 `key`；若为 `TITLE_CN_NUM` 或纯数字编号（`isPlainNumericSectionKey`），返回 `headingTitleFromLine(raw)`（即使不是 `#` 行，这个函数对非标题行也会退化为直接 `normalizeLine` 整行，等效于取归一化正文）；否则返回 `null`。

### 算法：`isValidSectionLabel(rawOrTitle) -> boolean`

1. 空白返回 `false`。
2. 归一化：先 `HeadingSuppressHeuristics.stripHashes` 去掉可能的 `#` 前缀，再 `HeadingLevelPrefixHeuristics.normalizeForHeadingPrefixMatch` 归一化（两个均为外部依赖，Part 4 范围）。
3. 若归一化结果以 `：`/`:` 收尾，返回 `false`（冒号引导多为正文列举，不是小节标签）。
4. 若 `HeadingPatternQualityHeuristics.clearlyFailsHeadingQuality(norm)` 为真（外部依赖，Part 4），返回 `false`。
5. 最终委托 `HeadingSuppressHeuristics.isStandaloneHeadingLine(rawOrTitle)`（外部依赖，Part 4）。

### 算法：`hasCnParenSectionFollowing(lines, fromLineId) -> boolean`

扫描 `[fromLineId+1, min(size, fromLineId+12))` 范围内的行（含已被误标 `#` 的行，因为用 `headingTitleFromLine` 统一剥离可能存在的 `#`），只要有一行分类为 `TITLE_CN_PAREN` 即返回 `true`；空行跳过不计入 12 行窗口消耗（注意：循环是固定行号窗口而非「12 个非空行」，空行也占用窗口位置，只是被 `continue` 跳过判断，不提前退出）。

### 算法：`isMarkdownHeadingLine(trimmed) -> boolean` / `headingTitleFromLine(raw) -> String`

- 前者：`HEADING_LINE` 正则整行匹配。
- 后者：先 `stripEdgeWhitespace`，若匹配 `HEADING_LINE` 返回 `normalizeLine(group(2))`；否则返回 `normalizeLine(trimmed)`（即对非标题行也做归一化整行返回，是一个「宽容」实现，被 `plainSectionLabelTitle` 等调用点依赖这个降级行为）。

### 废弃方法

`promoteUnmarkedParentsAndDemoteChildren` 标注 `@Deprecated`，直接转发到 `demoteMisplacedSectionHeadings`——Go 移植**不需要**保留这个别名（除非有外部调用点依赖旧名，本 Part 范围内未见调用）。

---

## ChapterTocCatalog

### 职责

章节目录快照：在阶段 1（噪声清理阶段，Part 6 范围）的 `stripLines` 之前扫描全文，合并三种来源识别出的章节条目——① PDF 式「第X章 + 页码」连续目录页行；②「目 录」标题段下的章条列举（Word 常见，无页码）；③ 正文中独立的「第 X 章 题名」结构行（无 PDF 目录页时的补全来源）。三种来源按优先级合并去重（structural=1 < tocSection=2 < tocPage=3，同一章号取优先级最高的来源）。产出的 `entries`（章条名+来源行号）供 `ChapterTocHeadingValidator` 补全/校验一级标题；`tocLineRanges`（仅 PDF 目录页那种连续行区间）供后续删除目录页正文。

### 数据结构

```go
type ChapterTocEntry struct {
    NormalizedHeading string
    SourceLineID      int
}

type TocLineRange struct { // 与顶层 LineRange 语义相同但独立定义（Java 用嵌套 record）
    Start int
    End   int
}
func (r TocLineRange) Contains(lineID int) bool { return lineID >= r.Start && lineID < r.End }

type ChapterTocCatalog struct {
    entries       []ChapterTocEntry
    tocLineRanges []TocLineRange
}

func EmptyChapterTocCatalog() ChapterTocCatalog { return ChapterTocCatalog{} }
func NewChapterTocCatalogEntriesOnly(entries []ChapterTocEntry) ChapterTocCatalog { ... }
```

Java 的 `LineRange` 构造函数有校验：`start < 0` 或 `end < start` 抛 `IllegalArgumentException`——Go 移植用普通 struct 无内建校验，若要保留校验语义可提供一个 `NewTocLineRange(start, end int) (TocLineRange, error)` 构造函数，调用方按需处理错误（本 Part 内部调用点均能保证合法区间，可选择用 `panic` 或忽略校验，视 Go 项目整体错误处理风格而定）。

### 算法：`parse(List<String> lines) -> ChapterTocCatalog`

1. `lines` 空返回 `empty()`。
2. `scanTocPageLines(lines)` 扫描 PDF 式目录页（见下），得到 `(entries_A, ranges)`。
3. `collectTocSectionChapterEntries(lines)` 扫描「目录」标题段列举，得到 `entries_B`。
4. `collectStructuralChapterEntries(lines)` 扫描正文结构行，得到 `entries_C`。
5. `mergeChapterEntriesByNumber(entries_A, entries_B, entries_C)` 合并去重（见下）。
6. 返回 `new ChapterTocCatalog(merged, ranges)`（只有 `ranges` 来自 PDF 目录页扫描，`entries_B`/`entries_C` 不产生行区间）。

### 算法：`scanTocPageLines(lines) -> (entries, ranges)`

1. 逐行扫描（维护 `inFence` 围栏状态，围栏内跳过）。
2. 用外部方法 `ChapterTocLineRemover.isChapterTocLine(trimmed)` 判断一行是否为「PDF 式章节目录行」（不在本 Part 范围）；非命中行 `i++` 继续。
3. 命中后，向下扩展连续区间 `[start, j)`：只要后续行仍是 `isChapterTocLine`（且不遇到围栏起止），持续扩展；遇到围栏标记或非目录行则停止。
4. 记录该连续区间为一个 `LineRange(start, j)` 加入 `ranges`。
5. 对区间内每一行，调用 `ChapterTocLineRemover.toStructuralChapterHeadingFromTocLine(line)`（外部方法）尝试提取规范化的章节标题文本；非空则加入 `entries`（带来源行号）。
6. `i = j` 继续外层扫描。

### 算法：`collectTocSectionChapterEntries(lines) -> List<ChapterTocEntry>`

1. `lines` 空返回空表。
2. 逐行扫描找到匹配 `TOC_HEADING`（「目录」标题行）的行 `i`（围栏内跳过）。
3. 从 `i+1` 开始向下扫描（`j`），直到遇到围栏标记为止（一旦遇到围栏立即 `break`，不切换围栏状态——这是与其他扫描函数不同的地方，此处围栏被当作硬边界而非需要跳过内部内容）：
   - 空行：`j++` 跳过继续。
   - 若当前行是「纯章节前缀行」（`ChapterTocLineRemover.isChapterPrefixOnlyLine`，如单独一行「第一章」，题名在下一行）**且**存在下一行，并且下一行像「章节题名行」（`isLikelyChapterTitleNameLine`）：合并两行文本（`t + " " + next`），`ChapterTocHeadingValidator.normalizeHeadingKey` 归一化；若 `isCatalogChapterCandidate(merged)` 通过，加入 `entries`（来源行号取 `j`，即前缀行的行号）；`j += 2` 跳过两行。
   - 否则：`normalizeCatalogChapterLine(t)` 尝试规范化单行章节标题；为 `null` 说明不再是目录列举的一部分，**整体 `break`**（结束本次「目录」标题段的扫描，回到外层继续找下一个「目录」标题）；否则加入 `entries`（来源行号 `j`），`j++` 继续。
4. 外层继续从下一行找下一个 `TOC_HEADING`（同一文档可能有多个「目录」段，如「目录」和「图目录」分别处理）。

### 算法：`collectStructuralChapterEntries(lines) -> List<ChapterTocEntry>`

用 `LinkedHashMap<String numKey, ChapterTocEntry>` 按「章号」去重（保留插入顺序）：
1. 逐行扫描（围栏内跳过，不切换状态时跳过判断但仍需维护 `inFence` 切换）。
2. 若当前行是「纯章节前缀行」且下一行存在且像章节题名行：合并两行，`normalizeHeadingKey` 归一化，调用 `putBetterChapterEntry`（见下）写入/更新 `byNumber`；`continue`（**注意此分支不像上一个方法那样跳两行，而是继续下一次外层循环的 `i++`**——即这里只处理了合并逻辑本身，下一行仍会被外层循环单独访问到，只是它作为「合并的第二行」不会再单独触发这个分支，因为它自身不太可能又是「纯前缀行」）。
3. 否则：`normalizeCatalogChapterLine(trimmed)` 规范化单行；为 `null` 跳过（`continue`）；否则同样 `putBetterChapterEntry`。
4. 返回 `byNumber.values()` 转为列表（保留插入顺序，即按章号首次出现顺序，而非按行号顺序——但由于是顺序扫描且 `LinkedHashMap` 保留插入序，实际上近似等价于按章号首次出现的行号顺序）。

### 算法：`putBetterChapterEntry(byNumber, heading, lineIndex, rawLine)`

1. `chapterNumberKey(heading)` 求章号 key（如「第一章」），为空跳过（不是可识别的章节格式，不参与去重也不写入）。
2. 若 `byNumber` 中尚无该 key，或新候选的 `chapterEntryScore` 严格高于已存在条目的 score，则覆盖写入。
3. `chapterEntryScore(rawLine, heading)`：原始行以 `#` 开头得 8 分（说明是已有 Markdown 标题，更可信）；再加 `min(归一化标题长度, 40)` 分（长度略长的标题更可能是完整题名而非截断）。

### 算法：`mergeChapterEntriesByNumber(tocPage, tocSection, structural) -> List<ChapterTocEntry>`

用两个 `LinkedHashMap`（`merged` 存最终条目，`priorityByNumber` 存已写入条目的来源优先级）：
1. 先合并 `structural`（优先级 1），再 `tocSection`（优先级 2），最后 `tocPage`（优先级 3）——**后写入的更高优先级来源会覆盖同章号的先前条目**（`putMergedEntry` 内判断 `priority > existingPriority` 才覆盖，同优先级不覆盖，因为用的是严格大于）。
2. 返回 `merged.values()`。

「PDF 目录页」优先级最高的原因：目录页文字通常最完整、最少受版式干扰；「正文结构行」优先级最低，因为容易与正文中提及的「详见第三章」等引用行混淆（虽然 `isCatalogChapterCandidate` 已经用 `ChapterReferenceHeuristics.isBodyChapterReference` 排除了明显的引用句，但仍保守地给最低优先级）。

### 算法：`normalizeCatalogChapterLine(trimmed) -> String`

`isCatalogChapterCandidate(trimmed)` 为假返回 `null`；否则 `ChapterTocHeadingValidator.normalizeHeadingKey(ChapterTocLineRemover.normalizeGluedStructuralChapterHeading(trimmed))`（先做「粘连修复」——外部方法，Part 1-3，处理如「第五章标题7.2」这种粘连——再归一化）。

### 算法：`isCatalogChapterCandidate(line) -> boolean`

七道过滤条件全部通过才算候选（任一失败即 `false`）：
1. 非空白。
2. `ChapterTocLineRemover.isStructuralChapterHeading(trimmed)` 为真（外部方法，判断形如「第 X 章 标题」的结构）。
3. `ChapterTocLineRemover.isChapterTocLine(trimmed)` 为**假**（不能已经是 PDF 目录页行，那种由 `scanTocPageLines` 单独处理）。
4. `ChapterReferenceHeuristics.isBodyChapterReference(trimmed)` 为**假**（不能是正文里「详见第三章」式的引用）。
5. `MarkdownStructureRules.endsWithTerminalPunctuation(trimmed)` 为**假**（标题不应以句末标点收尾）。
6. `MarkdownWeakMergeHeuristics.isTableLikeLine(trimmed)` 为**假**（不是表格样式行——**此依赖属于 Part 6 范围**）。
7. 归一化后长度 `<= 80` 且非空白字符数 `<= 60`。
8. 最终委托 `ChapterTocHeadingValidator.isChapterHeadingKey(norm)`（本文件内 `ChapterTocHeadingValidator` 提供，见下节）。

### `record` 类型

- `ChapterTocEntry(String normalizedHeading, int sourceLineId)`：不可变值对象。
- `LineRange(int start, int end)`：带校验的不可变值对象（`start<0` 或 `end<start` 抛异常），`contains(lineId)` 判断半开区间包含。

其余 getter（`entries()`/`tocLineRanges()`/`isEmpty()`/`isTocLine(lineId)`/`withoutLineRanges()`）均为简单委托，`withoutLineRanges()` 等价于 `entriesOnly(entries)`（丢弃行区间信息，只保留章条名——供跨阶段传递时避免行号失效，例如阶段 1 之后行号可能因为噪声清理而整体偏移，此时只保留章条名文本，行区间信息不再准确故丢弃）。

---

## ChapterTocHeadingValidator

### 职责

目录章条与正文标题的交叉校验（阶段 2 专用），在 `MarkdownHeadingStage` 标题推断完成后调用四个能力：① `augmentFromCatalog`——用目录章条名补全正文中缺失的一级「第X章」标题命中；② `enforceCatalogChapterLevels`——目录匹配到的「第X章」命中强制其 `level=1`；③ `shiftHeadingLevelsBelowNewPlainCatalogChapters`——新提升为一级的、原本没有 `#` 的章标题下方（到下一个同级章标题之前）的已有标题统一加深一级，为新插入的 H1 腾出子级层次空间；④ `matchesAnyCatalogChapter`——已有标题只要与目录中任一章条匹配就不应因其他反证被降级。

### 常量与正则

- `MAX_HEADING_LEVEL = 6`。
- `CHAPTER_HEADING_KEY = ^第\s*([一二三四五六七八九十百千万零\d]+)\s*章\s*(.*)$`：解析「第X章 标题」结构，`group(1)`=章号原文，`group(2)`=章节题名（可能为空串）。

### 数据结构

无独立 struct（静态方法集合）。

### 算法：`augmentFromCatalog(lines, catalog, hits) -> List<MarkdownHeadingHit>`

1. `catalog` 空或 `lines` 空，原样返回 `hits`。
2. 复制 `hits` 到可变列表 `out`；收集已用行号集合 `usedLines`。
3. 对目录中每个 `entry`：
   - `expected` 为空跳过。
   - 若 `out` 中已有命中与 `expected` 匹配（`hasMatchingChapterHit`），跳过（不重复添加）。
   - 否则调用 `findBodyLineMatchingCatalogEntry(lines, expected, usedLines)` 在正文中找匹配该章条的行号；找不到（`-1`）跳过。
   - 找到则新增一个 `MarkdownHeadingHit(lineIdx, level=1, expected, patternKey=null, scope=null)`（**注意这里 `titleRaw` 用的是目录里的 `expected` 文本，而不是正文原行的实际文本**——即用目录版本的标题文字覆盖写入，这是有意为之：目录页文字通常比正文（可能被 OCR/PDF 提取污染）更干净）加入 `out`，并把该行号标记为已用。
4. 按 `lineIndex` 排序后返回。

### 算法：`matchesAnyCatalogChapter(titleRaw, catalog) -> boolean`

1. `titleRaw`/`catalog` 空返回 `false`。
2. `isChapterHeadingKey(titleRaw)` 为假直接返回 `false`（不是「第X章」形态的标题，不参与目录匹配——这意味着此方法**只对章级标题生效**，不处理节/条等其他层级）。
3. 归一化后与目录中每个条目做 `headingKeysMatch` 比较，任一匹配返回 `true`。

### 算法：`validate(lines, catalog, hits) -> List<MarkdownHeadingHit>`

1. `hits` 空返回 `hits`（`null` 转空表）。
2. `catalog` 空返回 `hits` 的拷贝（不做任何过滤）。
3. 否则：剔除落在 `catalog.tocLineRanges()` 内的命中（`catalog.isTocLine(h.lineIndex)`），保留其余；再调用 `enforceCatalogChapterLevels` 原地修正 level；返回过滤后的列表。

### 算法：`enforceCatalogChapterLevels(hits, catalog)`

对每个命中，若 `matchesAnyCatalogChapter(h.titleRaw, catalog)` 为真，强制 `h.level = 1`（原地修改，无返回值）。

### 算法：`shiftHeadingLevelsBelowNewPlainCatalogChapters(hits, catalog, sourceMarkdownHeadingLineIndexes)`

1. 找出所有 `level==1` 且匹配目录章条的命中作为「章锚点」，按行号排序。
2. 若无锚点，直接返回。
3. 逐个锚点 `anchor`（按行号顺序）：
   - 若该锚点行号在 `sourceMarkdownHeadingLineIndexes`（原文本来就带 `#`）中，跳过（说明这不是「新提升」的章标题，是原文自带的，不需要给下方腾空间——因为原文自带的标题本来就应该已经有正确的子级关系）。
   - 否则确定本锚点的作用范围 `endLine`：下一个锚点的行号（若无下一个则 `Integer.MAX_VALUE`）。
   - 对所有 `hits` 中行号落在 `(anchor.lineIndex, endLine)` 区间内的其他命中（严格排除 `anchor` 自身），`level = min(MAX_HEADING_LEVEL, level+1)`（整体加深一级，带上限保护）。

### 算法：`hasMatchingChapterHit(hits, expected) -> boolean`

遍历 `hits`，跳过非「第X章」形态标题（`isChapterHeadingKey(h.titleRaw)` 为假的），对其余用 `headingKeysMatch` 与 `expected` 比较，任一匹配返回 `true`。

### 算法：`findBodyLineMatchingCatalogEntry(lines, expected, usedLines) -> int`

1. 逐行扫描（围栏内 `inFence` 状态跳过判断，仍需切换 `inFence`）。
2. 已在 `usedLines` 中的行跳过。
3. 若 `ChapterTocLineRemover.isChapterTocLine(trimmed)` 为真，跳过（目录页行本身不能作为「正文匹配位置」）。
4. 若 `headingKeysMatch(normalizeHeadingKey(trimmed), expected)`，直接返回该行号。
5. 否则若该行是「纯章节前缀行」且下一行存在，尝试合并当前行+下一行文本再做 `headingKeysMatch`；命中则返回**前缀行**的行号（不是下一行）。
6. 全文找不到返回 `-1`。

### 算法：`isChapterHeadingKey(titleRaw) -> boolean` / `chapterNumberKey(titleRaw) -> String`

- 前者：归一化后是否整体匹配 `CHAPTER_HEADING_KEY`。
- 后者：不匹配返回 `""`；匹配则返回 `"第" + 章号数字原文(去空白) + "章"`（**注意**：这里直接拼接的是 `group(1)` 的原始文本去空白后的结果，可能是阿拉伯数字也可能是中文数字——即「第1章」和「第一章」会生成**不同**的 key `第1章` vs `第一章`，**不会**被当作同一章号去重/匹配！这是一个需要在 Go 移植时原样保留的「已知限制」，不要自作主张加数字归一化，除非用户明确要求修复）。

### 算法：`bodyLineIndexesForCatalog(lines, catalog, usedLines) -> Set<Integer>`

对目录中每个 `entry`，调用 `findBodyLineMatchingCatalogEntry` 找到对应正文行号（找到则加入结果集合）；用于 `MarkdownHeadingStage.apply` 中排除「目录章条已对应到正文某行」的行，不被误计入「未出 #」的混合识别统计。

### 算法：`normalizeHeadingKey(text) -> String`

`text` 为 `null` 返回 `""`；否则 `strip()` 后用正则 `^#{1,6}\s*` 去掉可能的前导 `#`（`replaceFirst`），再 `trim()`，最后把内部连续空白折叠为单个半角空格（`replaceAll("\\s+", " ")`）。

### 算法：`headingKeysMatch(inferred, expected) -> boolean`

1. 任一为空返回 `false`。
2. 完全相等直接返回 `true`。
3. 用 `CHAPTER_HEADING_KEY` 分别匹配两者；任一不匹配（说明不是「第X章 标题」形态）返回 `false`。
4. 比较章号（`group(1)` 去空白后必须**字符串完全相等**——同样存在「1」vs「一」不互认的限制，见上）。
5. 章号相等的前提下，若任一方的题名（`group(2)`）为空，视为匹配（宽松：目录里常常只有「第一章」没有具体题名，或反之）。
6. 两者题名都非空时：完全相等，或一方包含另一方（`contains`），均视为匹配（容忍标题被截断或补充的情况，如目录「第一章 总则」与正文「第一章 总则（试行）」）。

---

## HeadingReadingOrderValidator

### 职责

阅读顺序嵌套校验（阶段 2 内）：在标题候选合并后、写入 `#` 之前，按正文行序用**单调栈**调整每个命中的 `level`，确保子标题层级严格深于父标题、不跳级；只做顺序一致性修正，不推翻前缀语义推断出的参考层级（作为「参考值」输入，实际输出层级在参考值与父级层级之间取一个满足嵌套约束的值）。

### 常量

`MIN_LEVEL=1`，`MAX_LEVEL=6`。

### 数据结构

```go
type stackEntry struct {
    hit            *HeadingHit
    referenceLevel int
}
```

### 算法：`applyReadingOrderNesting(hits) -> List<MarkdownHeadingHit>`

1. `hits` 空返回原样。
2. 复制并按 `lineIndex` 排序。
3. 用 `ArrayDeque` 作为栈，逐个处理排序后的命中 `hit`：
   a. `reference = clampLevel(hit.level)`（把参考层级夹到 `[1,6]`）。
   b. 弹栈：只要栈非空且栈顶的 `referenceLevel >= reference`，持续弹出（找到「真正比当前浅」的祖先）。
   c. 计算实际层级 `actual`：
      - 栈为空 → `actual = MIN_LEVEL`（顶级，无父）。
      - 栈非空，取栈顶（父级）的**实际层级** `parentLevel = 栈顶.hit.level`：
        - 若 `reference <= parentLevel`（参考层级不比父级深，说明原始判断可能有误或就是同级/更浅）→ `actual = parentLevel`（**注意：这里是让子级与父级同层，而不是维持原参考值**——这一步会让「本应是兄弟」的两个标题因为其中一个的原始参考层级算浅了，结果被强行拉到和父级一样深，需要按原样移植，不要理解错为「保持原样」）。
        - 若 `reference > parentLevel + 1`（比父级深超过 1 层，跳级）→ `actual = parentLevel + 1`（压缩到只深一级）。
        - 否则（`reference == parentLevel + 1`，正常嵌套）→ `actual = reference`。
   d. `hit.level = clampLevel(actual)`；把 `(hit, reference)` 压入栈（注意压栈的 `referenceLevel` 是步骤 a 算出的**原始参考值**，不是修正后的 `actual`——后续弹栈比较用的也是这个原始参考值，这样才能正确识别「原始层级相同或更深」的后续节点应该弹出这个栈帧）。
4. 返回排序后的列表（原地修改了每个 `hit.level`，返回值就是排序后的同一批对象引用）。

这个算法本质是一个「用参考层级做分组识别祖先关系，但输出层级看父级实际层级」的两阶段单调栈：`referenceLevel`（栈里存的判定依据）与 `hit.level`（真正写出去的值）是两个不同的量，Go 移植时**不能把两者合并成一个字段**，否则会破坏后续弹栈比较的语义。

---

## MarkdownTocEmitStage（阶段 4）

### 职责

流水线第四步（本 Part 覆盖的部分——阶段 4 除本类外还有 pipe 表格修复等，但那部分（`MarkdownPipeTableRepair`）不在本 Part 范围，只做直接调用）：① 给所有 `hits` 分配 slug；② 若 `generateToc=true` 且 `hits` 非空，生成 `## 目录` 链接块（仅收录 1-2 级标题，且要求 `patternKey != null`）；③ 用 `MarkdownPipeTableRepair.repair`（外部依赖）修复 pipe 表格并拼出最终 `resultMarkdown`。

### 算法：`apply(context)`

1. 取 `hits = context.hits()`。
2. `assignSlugs(hits)`（原地写 `h.slug`）。
3. `toc = generateToc && !hits.isEmpty() ? buildToc(hits) : ""`。
4. `body = MarkdownPipeTableRepair.repair(joinLines(context.lines()).trim())`（外部依赖，Part 6/其他范围，本类只做直接调用）。
5. `toc` 为空 → `resultMarkdown = body`；否则 `resultMarkdown = toc + "\n\n" + body + "\n"`。

### 算法：`buildToc(hits) -> String`

1. 筛选：`level==1 || level==2` 且 `patternKey != null` 的命中（**注意：`patternKey==null` 的命中——例如 `ChapterTocHeadingValidator.augmentFromCatalog` 补全出来的、`UnmarkedParentHeadingHeuristics` 之外的目录补全命中——不会出现在目录里**，即便它是一级标题；这是刻意的过滤：只有「真正被模式识别出来」的标题才进目录，纯靠目录补全但正文里没有可识别编号形态的标题不进目录，因为它没有一个稳定的 `patternKey` 依据其可信度）。
2. 按行号排序；为空返回 `""`。
3. 拼接：`## 目录\n\n`，一级标题 `- [标题](#slug)\n`，二级标题两个空格缩进 `  - [标题](#slug)\n`。
4. 整体 `trim()` 后返回。

### 算法：`assignSlugs(hits)`

1. 用 `Map<String base, Integer count>` 记录已用过的 slug 基名次数。
2. 逐个命中（**按 `hits` 传入的原始顺序，通常已经是按行号排好序**，因为调用方 `context.hits()` 在阶段 2 末尾已排序）：`base = slugify(titleRaw)`；若已用过（`count>0`），slug 为 `base + "-" + count`；否则就是 `base` 本身；`count` 累加。

### 算法：`slugify(title) -> String`

1. `stripEdgeWhitespace(title)`，转小写（`Locale.ROOT`）。
2. 用 `INVALID_SLUG_CHARS` 删除非法字符（保留字母、数字、表意文字、连字符、下划线、空白）。
3. `SPACE_RUN` 把空白折叠替换为单个连字符 `-`。
4. `DASH_RUN` 把连续连字符折叠为单个 `-`。
5. 去掉首尾连字符（`replaceAll("^-+|-+$", "")`）。
6. 结果为空则返回 `"section"`（兜底 slug）。

Go 移植 `INVALID_SLUG_CHARS`（`[^\p{L}\p{N}\p{IsIdeographic}\-_\s]`）：Go 正则不支持 `\p{IsIdeographic}`，需要自定义一个近似「表意文字」的 Unicode 范围表（覆盖 CJK 统一表意文字及扩展区，可复用 `unicode.Han` 作为近似——两者不完全等价但在本场景（过滤合法 slug 字符）差异可忽略；若要精确复现 Java 的 `IsIdeographic` 二进制属性，需要另建 `unicode.RangeTable`，包含 CJK Unified Ideographs 及其扩展 A-G、CJK Compatibility Ideographs 等区块，建议直接查 Unicode `Ideographic` 属性的官方区间表）。`\p{L}` 对应 Go 的 `unicode.L`（字母大类），`\p{N}` 对应 `unicode.N`（数字大类）。

---

## MarkdownHeadingStage（核心，阶段 2）

### 职责

阶段 2「层级标题识别」的主控类，完整流程见下方「流水线执行顺序」一节的详细拆解。内部分为三大块能力：
1. **候选提取与打分**（`extractCandidates`/`markListLikeCandidates`/`markBodyEnumerationLists`/`markShortPhraseListRuns`/`scoreCandidates`/`filterCandidates`）——从纯文本行中找出「像标题编号前缀」的候选，排除各类「实为正文列举/清单」的干扰。
2. **标题树递归构建**（`resolveScope`/`pickCurrentPattern`/`patternConfidence`/`rebuildTree`/`scopeLevelFix`）——用置信度公式在每个作用域内选出「主导编号体系」作为该层级的标题模式，递归划分子作用域，构建父子树并修正层级空洞/跳级问题。
3. **既有标题反证降级与定稿**（`filterExistingHeadingsByCounterEvidence`/`apply` 主流程的四步定稿）——已有 `#` 标题默认保留原层级，只有命中明确反证（目录行、附件清单、表格样式、短语清单、正文句式等）才降级。

### 常量与正则

| 常量 | 值 | 语义 |
|---|---|---|
| `MAX_LEVEL` | `4` | 标题树递归构建阶段的最大层级与递归深度上限（注意：这是**树构建阶段**的上限，不是最终输出层级上限——`HeadingReadingOrderValidator`/`ChapterTocHeadingValidator.shiftHeadingLevelsBelowNewPlainCatalogChapters` 等后续步骤仍可能把 level 推到 5、6，最终输出上限是 6，见 `MAX_HEADING_LEVEL`／`clampLevel`） |
| `CONFIDENCE_THRESHOLD` | `0.45` | 锚点模式置信度阈值：若第一个候选自带的模式置信度达到此值，直接采用，不再遍历全部候选模式取 argmax |
| `SIBLING_PROTECT_GAP` | `20` | 结构连续保护的最大行距（不含），用于 `protectStructuralSiblings`/`markBodyEnumerationLists` 的段合并判断 |
| `LIST_LIKE_MIN_RUN` | `5` | list_like 高密度段最小长度（同 pattern 连续候选数） |
| `BODY_ENUM_LIST_MIN_RUN` | `3` | 连续编号行视为正文枚举（而非标题）的最小条数 |
| `BODY_ENUM_MIN_CHARS_AFTER_PREFIX` | `15` | 去掉编号前缀后正文长度须不少于此值，否则不算「正文枚举」（避免误判「1、总则」这种短条目） |
| `LIST_LIKE_MAX_GAP` | `3` | list_like 段内平均行距须严格小于此值 |
| `LIST_LIKE_SEQ_QUALITY_PROTECT` | `0.8` | 序列质量达到此值则保护整段不标记为 list_like（说明是很规律的递增编号，更像真标题） |
| `SCORE_KEEP_THRESHOLD` | `2` | 打分保留阈值（`filterCandidates` 里 `score>=2` 才保留，除非命中其他强制保留分支） |
| `SHORT_PHRASE_LIST_SCORE_PENALTY` | `4` | 短语式清单强压制扣分 |
| `TEXT_HEADING_HEURISTICS_CONFIG` | `PdfToMarkdown.loadConfigOrDefaults()` | 纯文本短语清单阈值配置（外部依赖，复用几何渲染阶段同源配置常量，但本文件不使用样式簇/profile 信息） |
| `MAX_HEADING_LENGTH` | `MarkdownPipelineLineUtils.loadMaxHeadingLength()` | 标题最大长度（来自 `config.properties` 的 `pdf2md.maxHeadingLength`，默认 80） |
| `PREFIX_BODY_LIKE_MIN_LEN` | `35` | 判断「编号前缀+正文」是否为正文句子的最小非空白字符长度门槛 |
| `PREFIX_BODY_LIKE_MIN_PUNCT` | `2` | 同上，最小句读标点数 |
| `PREFIX_BODY_LIKE_MIN_PUNCT_DENSITY` | `0.015` | 同上，标点密度阈值（标点数/非空白字符数） |
| `WEAK_MERGE_TITLE_STUB_BODY_MIN_LEN` | `18` | 弱合并相关阈值（**本文件定义但未见任何方法实际引用**——搜索全文，这几个 `WEAK_MERGE_*` 常量在 `MarkdownHeadingStage.java` 中只有声明没有使用点，推测是从 Part 6 的弱合并逻辑复制过来但未清理的残留声明，或是预留给未来功能；Go 移植**可以不迁移这些未使用的常量**，仅在注释中记录其存在，避免误以为遗漏了某个算法） |
| `WEAK_MERGE_TITLE_STUB_BODY_MIN_PUNCT` | `2` | 同上，未使用 |
| `WEAK_MERGE_TITLE_STUB_BODY_MIN_PUNCT_RATIO` | `0.04` | 同上，未使用 |
| `WEAK_MERGE_TITLE_STUB_BODY_ALT_MIN_LEN` | `28` | 同上，未使用 |
| `WEAK_MERGE_TITLE_STUB_BODY_ALT_MIN_PUNCT_RATIO` | `0.03` | 同上，未使用 |
| `WEAK_MERGE_BODY_SENTENCE_PUNCT` | `，、。；：！？…,.;:!?､` | 同上，未使用（字符集含全角句读及 `､`=halfwidth ideographic full stop） |
| `WEAK_MERGE_COLUMN_HINT_MIN_PARTICIPANT_LEN` | `12` | 同上，未使用 |
| `WEAK_MERGE_COLUMN_HINT_SLACK` | `2` | 同上，未使用 |
| `WEAK_MERGE_TITLE_STUB_BODY_DYNAMIC_FLOOR` | `12` | 同上，未使用 |
| `TRIPLE_SHORT_PHRASE_MAX_LEN` | `40` | 同上，未使用 |
| `HOLE_FIX_MIN_H3` | `2` | 层级空洞修复（规则 3）：H3 最少条数 |
| `HOLE_FIX_SEQ_QUALITY_MIN` | `0.75` | 层级空洞修复：H3 子集序列质量下限 |
| `EPS` | `1e-9` | 浮点比较容差 |
| `CN_CHAPTER_HEADING` | `^第\s*[一二三四五六七八九十百千万零\d]+\s*章` | 「第X章」前缀识别（`find()` 语义，不要求整行） |
| `CN_ARTICLE_HEADING` | `^第\s*[一二三八九十百千万零\d]+\s*条`（原文用词与 CHAPTER 相同数字集） | 「第X条」前缀识别 |
| `BARE_IPV4_LINE` | `^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$` | 独立成行的四段点分数字 |
| `CONFIG_COMMENT_NEIGHBOR_SCAN_HOPS` | `6` | 配置注释误判防护：向邻近方向最多跳查 6 次 |
| `SHELL_OR_JVM_FLAG_LINE` | `^-{1,2}[A-Za-z][A-Za-z0-9_.-]*(?:[=:].*)?$` | shell/JVM 启动参数行（如 `-Xmx4g`、`--foo=bar`） |
| `XML_TAG_LINE` | `^</?[A-Za-z][^>]*>.*` | XML/HTML 标签起始行 |

### 数据结构

```go
type candidate struct {
    id              int
    lineID          int
    rawLine         string // Go 中未使用可省略，仅为对齐原始字段
    normText        string
    pattern         TitlePattern
    index           []int
    listLike        bool
    shortPhraseListRun bool
    score           int
}
// 排序：先 lineID 后 id
func candidateLess(a, b *candidate) bool {
    if a.lineID != b.lineID { return a.lineID < b.lineID }
    return a.id < b.id
}

type titleNode struct {
    id       int
    lineID   int
    level    int
    parentID *int // nil 表示无父
    titleRaw string
    pattern  TitlePattern
    index    []int
}

type scope struct {
    startLine int
    endLine   int
}

type hitFilterResult struct {
    accepted          []*HeadingHit
    rejectedLineIndexes []int
}

type catalogStripResult struct {
    lines         []string
    removedLineIDs map[int]struct{}
}
```

### 算法：`apply(context)`（主流程，逐步对应「流水线执行顺序」一节）

见文末专节，此处不重复。

### 算法：`pickCurrentPatternForWindow(lines, startLine1Based, endLine1Based) -> MarkdownPatternKey`

公开静态方法（可能被其他 Part 或测试直接调用，需在 Go 中保留导出）：
1. 对全文重新走一遍候选提取+打分+过滤全流程（`extractCandidates(lines)` 不传 `skipLineIds`/`disqualifiedPatternKeys`，即用默认空集合）。
2. 构造 `root = Scope(startLine1Based-1, endLine1Based)`（把 1-based 传入参数转为内部 0-based 半开区间）。
3. 取落在 `root` 内的候选；为空返回 `null`。
4. 按 `(lineId, id)` 排序，调用 `pickCurrentPattern` 选出该窗口的主导模式 `pk`。
5. 返回 `new MarkdownPatternKey(pk, pk.depth())`。

### 算法：`demoteNonHierarchyHashHeadings(lines, hierarchyLineIndexes)`

标题定稿第 4 步：逐行扫描（围栏内跳过），凡是 `#` 标题行但**不在** `hierarchyLineIndexes` 集合中的，调用 `demoteHashHeadingToBold` 降级为加粗正文。

### 算法：`demoteHashHeadingToBold(line) -> String`

1. `line` 空/空白原样返回。
2. 不匹配 `HEADING_LINE` 原样返回。
3. 取标题正文 `normalizeLine(group(2))`；为空返回 `""`（**整行清空**，不是保留空标题符号）。
4. 若正文已经是 `**...**` 包裹（且长度 `>=4`，即至少有 `**x**` 四个字符）直接返回该正文（避免重复加粗）。
5. 否则返回 `"**" + text + "**"`。

### 算法：`mergeInferredAndExisting` / `mergeExistingAndInferredWithExistingPreferred`

1. 用 `Map<Integer lineIndex, MarkdownHeadingHit>` 先把 `existing`（已有 `#` 标题解析出的命中）全部放入（后来源的 `existing` 优先，因为它是第一个填充的）。
2. 再遍历 `inferred`（推断出的候选），**只在该行尚未被 `existing` 占用时**才放入。
3. 转为列表，按 `lineIndex` 排序返回。

即：**同一行若两边都有命中，`existing` 完全胜出**（连 `patternKey`/`level` 都用 `existing` 的，不做任何合并折中）。

### 算法：`normalizeCnStructuralHeadingLevels(hits)`

对每个命中：若标题文本**包含**（`find()`，非整行匹配）「第X章」前缀，强制 `level=1`；否则若包含「第X条」前缀，`level = max(level, 2)`（只保底不封顶，即已经 `>=2` 时不变，`<2` 时提到 2）。与几何渲染阶段 `PdfToMarkdown.legacyHeadingLevel` 保持相同的硬编码规则（该方法本身不在本 Part 范围，此处只是注释提及两者一致，Go 移植不需要额外对齐验证，只需照此规则实现）。

### 算法：`collectSourceMarkdownHeadingLevels(lines) -> Map<Integer,Integer>`

逐行扫描（围栏内跳过判断），凡匹配 `HEADING_LINE` 的行，记录 `行号 -> #个数`。

### 算法：`collectMarkdownHeadingLineIndexes(lines) -> Set<Integer>`

同上扫描逻辑，只记录行号集合（不记层级）。**注意**：`apply` 主流程里这个函数被调用了两次，赋给两个不同的局部变量 `sourceMarkdownHeadingLineIndexes` 和 `markdownHeadingLineIndexes`——它们在调用时刻内容完全相同（都是入口时刻的快照），只是命名不同、用途不同（前者供后续「补全后是否要加深层级」判断用，写入 context；后者只作为局部变量传给 `inferHeadings` 当 `skipLineIds` 用，不写入 context）。Go 移植可以合并为一次调用、两个引用同一结果的变量，不需要真的调用两次（这是一个可以安全做的性能微优化，不改变行为，因为两次调用发生在同一份不可变 `lines` 快照之上，中间没有任何修改 `lines` 的代码）。

### 算法：`lineIndexesInScopes(scopes, lineCount) -> Set<Integer>`

展开 `scopes` 列表中所有 `MarkdownLineRange` 覆盖的行号为一个集合（越界保护：`i < lineCount`）。

### 算法：`filterExistingHeadingsByCounterEvidence(lines, existing, nonHeadingScopes, catalog, headingSequenceDemoteLineIds)`

1. `existing` 空原样返回。
2. `detectExistingHeadingShortPhraseListRunLineIds(lines)` 预先算出「短语清单」行号集合。
3. 逐个 `existing` 命中，调用 `shouldDemoteExistingHeadingByCounterEvidence` 判定；为真则调用 `demoteMarkdownHeadingLineToPlain` 就地把该行从 `#` 标题降为纯文本（**副作用**：直接修改 `lines`），并且**不**把该命中加入返回结果；为假则保留进结果列表。
4. **注意调用点**：`apply` 主流程调用此方法时传入的 `headingSequenceDemoteLineIds` 是 `Set.of()`（空集合硬编码），也就是说**这个参数在当前主流程中永远是空的**，`shouldDemoteExistingHeadingByCounterEvidence` 里对它的检查恒为假分支不生效——这是一个「预留参数但当前未被主流程实际使用」的情况，Go 移植时保留参数与检查逻辑（因为方法本身是可复用的，可能有测试或未来调用点会传非空集合），但要在文档中注明当前唯一调用点传的是空集合。

### 算法：`applyHeadingSequenceConsistencyDemotion(lines, hits)`

1. `lines`/`hits` 空原样返回。
2. 收集 `hits` 已覆盖的全部行号 `recognized`。
3. 调用外部方法 `HeadingSequenceConsistencyHeuristics.detectMarkdownLinesToDemote(lines, recognized::contains)`（Part 4 范围，传入一个「该行是否已被识别为标题」的判定回调）得到需要降级的行号集合。
4. 为空直接返回原 `hits`。
5. 对每个待降级行号，`demoteMarkdownHeadingLineToPlain` 就地修改 `lines`；同时从 `hits` 中剔除对应命中，返回过滤后的列表。

### 算法：`shouldDemoteExistingHeadingByCounterEvidence(lines, hit, nonHeadingScopes, catalog, shortPhraseLines, headingSequenceDemoteLineIds)`

**默认倾向保留**（`false`）——只有明确匹配以下任一反证才判定降级（`true`），按顺序短路检查：

1. `hit`/`lines` 为 `null`，或行号越界 → `true`（异常情况保守降级）。
2. 行号在 `headingSequenceDemoteLineIds` 中 → `true`（当前主流程恒为空集合，见上）。
3. 若 `matchesAnyCatalogChapter(hit.titleRaw, catalog)` → **直接返回 `false`**（目录匹配的章节标题享有最高保护，后续所有反证检查都不再执行——这是一个短路的「白名单」提前返回，顺序很关键）。
4. 若行号落在 `catalog.tocLineRanges()` 任一区间内 → `true`。
5. 若原文或归一化文本命中 `ChapterTocLineRemover.isChapterTocLine` → `true`。
6. 若匹配 `TOC_HEADING` 或 `TOC_MD_LINK_LINE` → `true`。
7. 若原文或归一化文本命中 `ChapterReferenceHeuristics.isBodyChapterReference`（正文引用句） → `true`。
8. 若行号落在 `nonHeadingScopes` 中（`MarkdownNoiseCleanupStage.isInAnyScope`） → `true`。
9. 若命中 `MarkdownWeakMergeHeuristics.isTableLikeLine` 或 `isQuoteOrRuleLine` → `true`。
10. 若行号在 `shortPhraseLines` 集合中 → `true`。
11. 若 `isExistingHeadingBodyLikeSentence(hit.titleRaw)`（见下） → `true`。
12. 若原文或归一化文本命中 `isCnParenBodyEnumeration`（见下） → `true`。
13. 若归一化文本长度 `> MAX_HEADING_LENGTH * 2`（即 160，按默认配置） → `true`。
14. 全部不命中 → `false`（保留原标题）。

### 算法：`isExistingHeadingBodyLikeSentence(text) -> boolean`

1. 空白返回 `false`。
2. 非空白字符数 `>80` 直接返回 `true`（超长安全冗余）。
3. 非空白字符数 `< PREFIX_BODY_LIKE_MIN_LEN(35)` 返回 `false`（太短不可能是正文句子）。
4. 句读标点数 `< PREFIX_BODY_LIKE_MIN_PUNCT(2)` 返回 `false`。
5. 标点密度 `= punct / nonSpaceLen`；`>= PREFIX_BODY_LIKE_MIN_PUNCT_DENSITY(0.015)` 才返回 `true`。

### 算法：`looksLikeCnParenBodyEnumerationText(normalizedLine) -> boolean`（公开）

1. 空白返回 `false`。
2. 必须整行匹配 `TITLE_CN_PAREN`（`（一）...` 形态），否则返回 `false`。
3. 剥离 `（一）` 前缀后取 `after`；若 `after` 匹配 `^[^：:]{0,40}[：:].*`（即前 40 字符内出现冒号，冒号前不含冒号自身），返回 `true`（典型的「（一）部门名称：」列举引导）。
4. 否则委托 `HeadingPatternQualityHeuristics.clearlyFailsHeadingQuality(t)`（外部依赖，Part 4）。

### 算法：`isCnParenBodyEnumeration(line) -> boolean`

去掉可能的 `#` 前缀后委托上面的方法。

### 算法：`detectExistingHeadingShortPhraseListRunLineIds(lines)`

委托外部方法 `ShortPhraseListRunHeuristics.detectExistingHeadingShortPhraseListRuns(lines, TEXT_HEADING_HEURISTICS_CONFIG, normalizeLine引用)`（Part 4 范围）。

### 算法：`demoteMarkdownHeadingLineToPlain(lines, lineIndex)`

行号越界直接返回；否则若该行匹配 `HEADING_LINE`，取正文归一化文本写回（空文本写 `""`）。**注意**：与 `demoteHashHeadingToBold` 不同，这里降级为**纯文本**而非加粗——两种「降级」在不同上下文含义不同：`demoteMarkdownHeadingLineToPlain` 用于「这一行判断根本不该是标题」（如目录行、附件清单），`demoteHashHeadingToBold` 用于「这一行确实有强调意味但不该是层级标题」（如推断/校验流程末尾清理的非层级 `#`）。

### 算法：`inferHeadings(lines, listGuideScopes, skipLineIds[, disqualifiedPatternKeys])`

标题推断主流程（两个重载，四参版本才是完整逻辑，三参版本转发时传 `disqualifiedPatternKeys=Set.of()`）：

1. `extractCandidates(lines, skipLineIds, disqualifiedPatternKeys)` 提取原始候选；为空直接返回空表。
2. 若 `listGuideScopes` 非空，剔除落在任一作用域内的候选（`removeIf`）；剔除后为空则返回空表。
3. 依次调用 `markListLikeCandidates`、`markBodyEnumerationLists`、`markShortPhraseListRuns`、`scoreCandidates` 完成标记与打分。
4. `filterCandidates` 过滤出最终候选集 `cands`；为空返回空表。
5. 构造 `root = Scope(0, lines.size())`；取 `inRoot`（落在全文根作用域内的候选，理论上应等于 `cands` 全部，因为 root 覆盖全文——这一步在语义上是恒等的，但代码里仍显式调用 `inScope` 保持与递归调用的一致写法）。
6. `resolveScope(root, inRoot, cands, level=1)` 递归构建标题树节点列表 `nodes`。
7. 若 `nodes` 为空，调用 `fallbackTitles(cands, root)` 降级兜底。
8. 若仍为空，返回空表。
9. `injectMissingTowSectionHeadings(nodes, cands)`：补回可能被树推断遗漏的 `3.3` 类二级段落锚点（见下）。
10. `rebuildTree(nodes)` 重建父子指针；`scopeLevelFix(nodes)` 做层级空洞/跳级/单节点根等修正；**再次** `rebuildTree(nodes)`（因为 `scopeLevelFix` 会改变 `level`，需要重新计算 `parentId`，虽然 `rebuildTree` 本身只依据 `level` 和顺序而不依据旧 `parentId`，重新跑一遍是为了让最终 `parentId` 反映修正后的层级——但**主流程后续代码实际上不再读取 `parentId`**，这次重建更多是保证数据结构自洽，非严格必要但保留以防未来消费方读取它）。
11. `removeEmptyTitleNodes(nodes)` 剔除归一化后为空文本的节点；为空返回空表。
12. 按 `(lineId, id)` 排序，转换为 `MarkdownHeadingHit` 列表返回（`patternKey` 用 `new MarkdownPatternKey(n.pattern, n.pattern.depth())`——**注意这里的 `depth()` 是模式固有的深度，不是树中实际的 `level`**，两者语义不同，不要混淆）。

### 算法：`filterHitsByListGuideScopes(hits, scopes) -> HitFilterResult`

`hits`/`scopes` 任一空，直接返回 `(hits, [])`。否则遍历 `hits`，落在任一 `scopes` 内的加入 `rejected`（记行号）不进 `accepted`，其余进 `accepted`。返回两者组成的结果对象。

### 算法：`extractCandidates`（四参完整版）

见上文「Go regexp 兼容性预警」提及的正则集中调用点，完整算法：

1. 逐行扫描（维护 `inFence`，用 ` ``` ` 前缀切换）。
2. 行号在 `skipLineIds`（已有 `#` 标题行）中 → 跳过。
3. 围栏起止行本身 → 切换状态后 `continue`；围栏内部 → `continue`。
4. 该行本身已是 `HEADING_LINE`（`#` 标题）→ `continue`（这类行在 `skipLineIds` 通常已覆盖，此判断是双重保险）。
5. 表格行（以 `|` 开头，或匹配 `TABLE_SEPARATOR`）→ `continue`。
6. 归一化 `norm = normalizeLine(raw)`。
7. **抑制判断**：若该行**不**是「节标题编号行」（`isSectionTitleExtractLine`）**且不**是「结构性章节标题」（`ChapterTocLineRemover.isStructuralChapterHeading`）**且** `HeadingSuppressHeuristics.shouldSuppressHeadingLine(lines, lineId)` 为真（三个条件都满足才抑制）→ `continue`（跳过该行，不产生候选）。即：即使命中了通用抑制规则，只要该行同时是「节标题编号行」或「结构性章节标题」，也**豁免**抑制，继续往下判断。
8. `MarkdownStructureRules.isTitleExtractCandidateLine(norm)` 为假 → `continue`（外部方法，Part 1-3，基础资格判断）。
9. `MarkdownTitlePattern.matchFirst(norm)`：无匹配 → `continue`。
10. 若命中模式是 `TITLE_CN_PAREN` 且 `looksLikeCnParenBodyEnumerationText(norm)` → `continue`（「（一）部门：」类列举不算候选）。
11. 若命中模式是 `TITLE_CHAPTER_FIVE`（第X条）且 `HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead(norm)`（外部方法）→ `continue`（「第五条」但其实是正文段落引导句）。
12. 若该行是「裸 IPv4 地址」（`isBareIpv4AddressLine`）且相邻花括号配置行（`sitsNextToBraceDelimitedLine`）→ `continue`。
13. `classifyPrefixKey(norm)` 得到 `pk`；若 `pk` 非空且在 `disqualifiedPatternKeys`（外部传入的全局黑名单）中 → `continue`。
14. `MarkdownTitlePattern.parseIndex(norm, hit)`：解析失败（`null` 或空数组）→ `continue`。
15. 构造 `Candidate` 加入结果列表，`nextId` 自增。

### 算法：`isSectionTitleExtractLine` / `isBareIpv4AddressLine` / `sitsNextToBraceDelimitedLine` / `looksLikeBraceDelimiter` / `nextNonBlankLine`

- `isSectionTitleExtractLine`：直接委托 `HeadingSequenceConsistencyHeuristics.isSectionTitleNumberedLine(norm, lines)`（外部）。
- `isBareIpv4AddressLine`：`BARE_IPV4_LINE` 整行匹配后，四段数字必须都 `<=255`（否则不是合法 IPv4，返回 `false`——但**注意**：`<=255` 检查通过≠一定是 IP，只是排除明显不是 IP 的「4 段数字层级标题」，如 `1.256.3.4` 这种超出字节范围的会被认定不是 IP 因而**可能被当作标题候选**，即此函数命名「isBareIpv4」但语义其实是「像不像合法 IPv4」，用于排除误判）。
- `sitsNextToBraceDelimitedLine`：取上一非空行（`HeadingSuppressHeuristics.previousNonBlankLine`，外部）与下一非空行（本文件内 `nextNonBlankLine`），任一形如「以 `{` 收尾」或「整行是 `}`」或「以 `}` 开头」（`looksLikeBraceDelimiter`）即返回 `true`。
- `nextNonBlankLine`：从 `fromLineId+1` 起找第一个归一化非空的行，返回其**原始**文本（未归一化）；找不到返回 `""`。

### 算法：`markListLikeCandidates(cands)`

1. 按 `(lineId, id)` 排序。
2. 双指针扫描：找连续同 `pattern` 的候选段 `[i,j)`（仅当该 `pattern.supportListLike()` 为真时才参与，否则 `i++` 跳过整个判断，**不会**因为遇到不支持的模式而中断已经在累积的另一段——因为这是逐段独立处理，`i` 直接跳到下一位置重新开始判断）。
3. 段长 `nSeg = j-i >= LIST_LIKE_MIN_RUN(5)` 时才继续判断：
   - `avgGap = meanLineGap(段)` 严格 `< LIST_LIKE_MAX_GAP(3)`（行距够密）。
   - `seqQualityAdjacent(段) < LIST_LIKE_SEQ_QUALITY_PROTECT(0.8)`（序列质量不够高，不足以被保护）。
   - 两条件都满足 → 整段标记 `listLike=true`。
4. 扫描完毕后调用 `protectStructuralSiblings(sorted)` 做「紧邻同模式对」的例外解除（见下）。

### 算法：`markBodyEnumerationLists(cands)`

1. 按 `(lineId, id)` 排序。
2. 双指针扫描：仅处理 `MarkdownTitlePattern.isBodyEnumerationPattern(pattern)` 为真的模式（`TITLE_NUM_DUNHAO/DOT/SUFFIX/PAREN/TITLE_CN_PAREN` 五种）；段的延展条件比 `markListLikeCandidates` 更宽松：只要求同 `pattern` **且**相邻两条候选行距 `< SIBLING_PROTECT_GAP(20)`（而非要求整体密度）。
3. 段长 `n = j-i >= BODY_ENUM_LIST_MIN_RUN(3)` 且 `bodyEnumRunLooksLikeBodyList(段)` 为真 → 整段标记 `listLike=true`。

### 算法：`bodyEnumRunLooksLikeBodyList(seg) -> boolean`

对段内**每一条**候选（全部满足才返回 `true`，任一不满足立即 `false`）：
1. `stripBodyEnumerationPrefix` 剥离编号前缀后取正文 `body`；若 `body.length() < BODY_ENUM_MIN_CHARS_AFTER_PREFIX(15)` → 返回 `false`（太短，可能是节标题不是正文枚举）。
2. `looksLikeNumericSectionTitleBody(pattern, body)` 为真 → 返回 `false`（正文形态像是节标题名，不当作正文枚举）。

### 算法：`looksLikeNumericSectionTitleBody(pattern, body) -> boolean`

仅当 `pattern` 是 `TITLE_NUM_DOT/TOW/THREE/FOUR/FIVE` 五种数字类之一时，委托外部方法 `PdfToMarkdown.looksLikeSectionTitleBody(body)`（Part 1-3 范围）；其余模式恒返回 `false`。

### 算法：`markShortPhraseListRuns(cands, lines)`

1. 逐个候选，仅保留 `ShortPhraseListRunHeuristics.supportsPatternKey(pattern.name())` 为真的模式（外部方法按枚举**名字字符串**判断，Go 移植需要保证枚举命名字符串与原 Java 名字一致，或改用等价的枚举值映射，只要行为一致即可，不强制字符串完全相同——但若 `ShortPhraseListRunHeuristics` 内部逻辑依赖具体名字字符串比较，则**必须**保留一致的命名以配合 Part 4 的实现，需与 Part 4 负责人协调命名映射表）。
2. 额外排除：`pattern != TITLE_NUM_DOT` 且 `ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(pattern.name(), normText, lines)` 为真的候选（即：除了 `TITLE_NUM_DOT` 模式外，其余模式若被判定为「像节标题编号行」就不参与短语清单检测——`TITLE_NUM_DOT` 本身被排除在这条豁免规则之外，意味着它即使被判定为「像节标题编号行」仍然会被纳入短语清单检测的输入）。
3. 用筛选后的候选构造 `Entry` 列表，调用 `ShortPhraseListRunHeuristics.detectPlainShortPhraseListRuns(entries, lines, config)`（外部）得到需要标记的行号集合。
4. 对全部 `cands`（不仅是参与检测的那些子集），行号在结果集合中的标记 `shortPhraseListRun=true`。

### 算法：`protectStructuralSiblings(sorted)`

双重循环（`O(n^2)`，`n` 为候选总数，性能上可能是热点但按原样移植）：对每对 `(a,b)`（`i<j`）都是 `listLike=true` 且 `pattern` 相同：
1. 若两者都是「正文枚举模式」（`isBodyEnumerationPattern`），跳过这对（不解除保护——正文枚举模式之间即使紧邻序列也不解除 list_like，因为这类模式本来就更倾向于是正文列举）。
2. `gap = b.lineId - a.lineId`；`gap<0` 或 `gap>=SIBLING_PROTECT_GAP(20)` 跳过。
3. `isSequential(a,b)`（index 前缀相同、末位 +1）为真 → 两者都解除 `listLike=false`（相邻紧密且严格递增编号，判定为「结构性兄弟」，不该被当成清单打压）。

### 算法：`scoreCandidates` / `scoreOne`

```
score = 0
+2  文本长度 <= MAX_HEADING_LENGTH
+2  上一行是空行
+2  下一行是空行
+3  非短语清单 且 文本不含「的/了/是/将」任一字
+2  非 listLike 且 非短语清单 且 不以「动词性」收尾（endsWithVerbLike 为假）
-3  冒号后长尾 > 30 字符（hasLongTailAfterColon）
-2  逗号（含全角）计数 >= 2
-2  listLike 为真
-4  shortPhraseListRun 为真（SHORT_PHRASE_LIST_SCORE_PENALTY）
```
各条独立判断，互不排斥（多条同时满足则累加/累减）。

### 算法：`filterCandidates(lines, cands)`

逐条候选按顺序检查（**注意此处顺序与短路逻辑，必须原样保留**）：
1. `normText == null` → 剔除。
2. `shortPhraseListRun == true` → 直接剔除（无论其他条件）。
3. 若 `ChapterTocLineRemover.isStructuralChapterHeading(normText)` **或** `ShortPhraseListRunHeuristics.looksLikeSectionTitleNumberedLine(pattern名, normText, lines)` → **直接保留**（跳过后续全部检查，包括长度、打分门槛）。
4. 文本长度 `> MAX_HEADING_LENGTH` → 剔除。
5. `isPrefixHeadingButBodyLikeSentence(c)`（见下）→ 剔除。
6. `pattern==TITLE_CHAPTER_FIVE` 且 `HeadingSuppressHeuristics.looksLikeCnArticleBodyParagraphLead` → 剔除。
7. `isBodyEnumerationPattern(pattern)` 且 `listLike==true`：进一步剥离前缀取 `body`，若 `!looksLikeNumericSectionTitleBody(pattern, body)` → 剔除（即：正文枚举模式被标记为清单、且剥离前缀后正文也不像节标题名，才剔除；如果正文像节标题名，则豁免不剔除）。
8. `pattern==TITLE_CN_PAREN` 且 `looksLikeCnParenBodyEnumerationText` → 剔除。
9. 走到这里的候选，只有 `score >= SCORE_KEEP_THRESHOLD(2)` 才保留。

### 算法：`isPrefixHeadingButBodyLikeSentence(c) -> boolean`

仅对三种模式生效（`TITLE_CN_PAREN`、`TITLE_CN_NUM`、`TITLE_CHAPTER_FIVE`），其余模式恒 `false`。判定逻辑与 `isExistingHeadingBodyLikeSentence` **完全相同**的四条规则（长度>80 直接真；长度<35 直接假；标点<2 直接假；密度>=0.015 才真），只是入参改为 `Candidate` 对象取 `normText`。**这是一处逻辑重复**（两个方法算法完全一致，只是调用方不同），Go 移植时建议抽成一个共享的私有函数 `looksLikeBodySentence(text string) bool`，两处都调用它，减少重复（这是安全的重构，不改变行为，符合项目「不要只针对当前问题改，要考虑通用性」的要求方向，但仍需以 Java 原文为准核对参数完全一致——已核对：两处判定阈值常量、分支顺序完全相同）。

### 算法：`resolveScope(scope, candidatesInScope, allFiltered, level)`（递归核心）

1. `level > MAX_LEVEL(4)` → 返回空。
2. `candidatesInScope` 空 → 返回空。
3. 排序 `cSorted`。
4. `pickCurrentPattern(cSorted, candidatesInScope, scope)` 选出本层锚点模式 `pk`。
5. 从 `cSorted` 中筛出 `pattern==pk` 的候选组成 `layer`（排序）；为空返回空。
6. 为 `layer` 中每个候选生成 `TitleNode.fromCandidate(c, level)`（当前递归层级）。
7. 若 `level >= MAX_LEVEL` → 直接返回这批节点，**不再递归**（达到深度上限，即使还有更深层候选也不再展开成子节点——这些更深的候选会在 `filterCandidates` 阶段就已经被保留在 `allFiltered` 里但永远进不了树，等价于被丢弃；这是一个已知的深度截断行为，按原样保留）。
8. 否则对 `layer` 中每个节点 `t`（按顺序）：
   - `endLine`：下一个同层节点的行号，若是最后一个则用 `scope.endLine`。
   - `childStart = t.lineId + 1`；若 `childStart >= endLine` → 跳过（无子区间空间）。
   - 构造子 `Scope(childStart, endLine)`，从 `allFiltered`（**全局候选池，不是当前层候选**）里筛出落在子区间的候选，递归调用 `resolveScope(child, sub, allFiltered, level+1)`，结果并入 `nodes`。
9. 返回 `nodes`（本层节点 + 所有递归子节点，扁平列表，父子关系靠后续 `rebuildTree` 从 `(lineId, level)` 重新推导，本函数不直接维护父指针）。

### 算法：`pickCurrentPattern(cSorted, candidatesInScope, scope)`

按顺序尝试三个特殊规则，都不命中才落到通用置信度 argmax：

1. `preferDotSectionLayerWhenPresent(candidatesInScope)`：非空则直接返回（优先级最高）。
2. `preferTowSectionLayerOverThree(candidatesInScope)`：非空则返回。
3. 通用规则：
   - `anchor = cSorted.get(0).pattern`（作用域内**行号最靠前**的候选的模式，作为默认锚点）。
   - `confAnchor = patternConfidence(该模式在本作用域的全部候选, scope)`。
   - 若 `confAnchor + EPS >= CONFIDENCE_THRESHOLD(0.45)` → 直接返回 `anchor`（不再比较其他模式，性能优化：多数情况下第一个出现的模式就是主导模式）。
   - 否则收集作用域内出现过的**全部不同模式** `pAll`（按首次出现顺序），及每个模式的「首次出现 (lineId, id)」用于打破平局。
   - 遍历 `pAll`，对每个模式算置信度（`anchor` 已算过的复用，其余重新算），维护当前最优 `best`/`bestConf`/`bestKey`：
     - 新模式置信度**严格更高**（`conf > bestConf + EPS`），或**置信度相近但首次出现行号更靠前**（`abs(conf-bestConf)<=EPS && compareFirstLine(fl,bestKey)<0`）→ 更新为新最优。
   - 返回 `best`。

### 算法：`compareFirstLine(a,b) -> int`

比较两个 `[lineId, id]`（`long[]`）：`a==null` 视为「更大」（除非 `b` 也是 `null`，此时相等）；否则按 `lineId` 比较，相等再按 `id` 比较。用于平局打破，值更小（行号更靠前）者优先。

### 算法：`preferDotSectionLayerWhenPresent(candidatesInScope)`

存在某个 `TITLE_NUM_DOT` 候选，其 `index.length==1`（即真的是单段编号如 `1.`，而非解析异常）**且** `!hasEarlierDifferentPatternCandidate(candidatesInScope, c)`（该候选之前没有其他模式的候选）→ 返回 `TITLE_NUM_DOT`；否则返回 `null`。

**用途**：让 `1.` 优先作为本层锚点，这样 `1.1.`/`1.2.2.` 等能正确落入其子作用域递归，而不是与 `1.` 争夺同一层级。

### 算法：`hasEarlierDifferentPatternCandidate(candidatesInScope, dotCandidate)`

存在任意其他候选（模式不同于 `dotCandidate`）的行号**严格小于** `dotCandidate` 的行号 → 返回 `true`。用于避免整个作用域混合了多种编号体系时错误地把 `1.` 拔高为整层锚点（这样会导致更早出现、属于另一体系的候选因不属于选中的锚点模式而在 `resolveScope` 里被永久丢弃，见 `resolveScope` 步骤 5「筛出 pattern==pk 的候选」——不属于 `pk` 的候选在本层被舍弃且不会进入任何子递归，因为子递归的候选池虽是 `allFiltered` 但子区间起点是 `t.lineId+1`，早于第一个 `pk` 节点的候选永远访问不到）。

### 算法：`preferTowSectionLayerOverThree(candidatesInScope)`

1. 若作用域内同时存在 `TITLE_NUM_TOW`（如 `3.1`）与 `TITLE_NUM_THREE`（如 `3.1.1`）候选，才继续；否则返回 `null`。
2. 若作用域内存在任何 `TITLE_CHAPTER_ONE`（第X章）候选，返回 `null`（章级标题存在时不应用此规则，避免干扰更高层级判定）。
3. 对每个 `index.length==3` 的 `TITLE_NUM_THREE` 候选 `c`：必须能在候选集中找到与其**前两段索引相同**的 `TITLE_NUM_TOW` 候选（`index.length==2` 且 `tow.index[0]==c.index[0] && tow.index[1]==c.index[1]`）作为「同前缀兄弟」；只要有一个 `TITLE_NUM_THREE` 候选找不到这样的兄弟，整个方法返回 `null`（要求**全员**满足，不是部分满足即可）。
4. 全部满足 → 返回 `TITLE_NUM_TOW`。

**用途**：当 `3.1`、`3.2`、`3.3` 都存在，且 `3.3` 下还有 `3.3.1` 这样的三级候选时，本层应该用 `TITLE_NUM_TOW`（`3.1/3.2/3.3`）做同级锚点，让 `3.3.1` 落入 `3.3` 的子作用域，而不是让 `TITLE_NUM_THREE` 争夺本层。

### 算法：`injectMissingTowSectionHeadings(nodes, pool)`

树构建后补偿：对 `pool`（全部经过滤的候选）中每个 `TITLE_NUM_TOW`（`index.length==2`）候选，若其行号**不在** `nodes` 已有节点集合中，但存在某个已在 `nodes` 中的 `TITLE_NUM_THREE` 节点与它共享前两段索引（`n.index[0]==c.index[0] && n.index[1]==c.index[1]`）——说明这个 `3.3` 段锚点本该在 `filterCandidates` 阶段被保留、却在树递归中因为某种原因（多为父作用域选择了别的锚点模式）没能挂上——则补插一个新节点：
- `anchorLevel` 默认 `2`；若 `nodes` 中存在 `TITLE_CHAPTER_ONE` 节点，取其 `level+1` 与默认值的较大者（只看**第一个**遍历到的 `TITLE_CHAPTER_ONE` 节点，找到就 `break`，不管是否是本候选的真实父章节——**这是一个近似处理**，不做精确的祖先查找）。
- 加入 `out` 列表，标记该行号已存在（防止重复补插）。
最终按 `(lineId, id)` 排序返回。

### 算法：`patternConfidence(same, scope) -> double`

置信度公式（四个子指标加权求和，权重固定）：
```
seqQualityAdjacent   权重 0.35
persistenceMetric    权重 0.25
localConsistencyAdjacent 权重 0.20
(1 - densityNorm)    权重 0.20
```
其中 `densityNorm = min(1.0, n / lScope)`（`n`=候选数，`lScope=max(1, scope.endLine-scope.startLine)`，密度越高说明该模式候选在作用域内占比越大，`(1-densityNorm)` 权重意味着**密度过高反而扣分**——直觉：如果一个模式的候选几乎铺满整个作用域的每一行，更可能是正文而非稀疏分布的标题）。`same` 为空返回 `0.0`。

### 算法：`seqQualityAdjacent(sorted) -> double`

相邻对中「index 满足 `isSequential`」的比例；`n<2` 时定义为 `1.0`（单个候选或空视为完全一致）。

### 算法：`isSequential(a,b) -> boolean`

两个候选的 `index` 数组长度必须相等且非零；除最后一段外所有段必须相等；最后一段 `b` 必须恰好是 `a` 的最后一段 `+1`。

### 算法：`persistenceMetric(sorted) -> double`

1. `n==0` 返回 `0.0`；`n==1` 返回 `1.0`。
2. 按行号去重（保持顺序，只留每个不同 `lineId` 的第一次出现——理论上不应有重复行号，是防御性写法）得到 `u` 个唯一行号。
3. `u==1` 返回 `1.0`。
4. `R` 从 `1` 开始，每当相邻两个唯一行号之差 `>1` 时 `R++`（即统计「连续段」个数：行号连续则算一段，出现跳跃则分段）。
5. 返回 `R/u`（段数占比，段数越少即越集中连续，值越小，*表示越集中/持久*——注意变量名 `persistence` 但公式是「段数/总数」，值越**小**代表越持久聚集，Go 移植保留原样计算，不要按字面理解成「值越大越持久」）。

### 算法：`localConsistencyAdjacent(sorted) -> double`

相邻对中 `index` 字典序严格递增（`Arrays.compare(a,b)<0`）的比例；`n<2` 时为 `1.0`。

### 算法：`inScope(cands, scope) -> List<Candidate>`

线性筛选 `lineId` 落在 `[scope.startLine, scope.endLine)` 内的候选。

### 算法：`fallbackTitles(cands, root)`

1. `cands` 空返回空表。
2. 统计不同模式种类 `distinct`（按首次出现顺序去重）。
3. `distinct.size()==1` → `allLevelOne(cands)`（全部设为 H1）。
4. `distinct.size()==2` → 取两种模式各自在 `root` 作用域的置信度，`c0+EPS>=c1` 则 `top=p0` 否则 `top=p1`；`top` 模式的候选设为 `level=1`，另一种（`second`）以及**任何其他情况**（代码 `int lv = c.pattern==top?1:c.pattern==second?2:2`——注意这个三元表达式的第三分支 `2` 与第二分支相同，意味着实际上只要不是 `top` 就统一是 `2`，`second` 变量的判断分支是多余的但不影响结果）都设为 `level=2`。
5. `distinct.size()>=3` → 同样 `allLevelOne(cands)`（**注意**：3 种及以上模式时的兜底策略与 1 种时相同，都是全部拍平为 H1，不像 2 种模式那样有区分——这是文档中提到的「工程折中」，全文非空回退优先于精细层级划分）。

### 算法：`allLevelOne(cands)`

每个候选生成 `level=1` 的节点。

### 算法：`rebuildTree(nodes)`

单调栈重建父子关系（**只写 `parentId`，不改 `lineId`/`level`**）：
1. 按 `(lineId, id)` 排序。
2. 用栈：对每个节点，弹出栈顶「层级 `>=` 当前节点层级」的所有节点（找到真正的父级——层级严格更浅的最近节点）；栈空则 `parentId=null`，否则 `parentId=`栈顶节点的 `id`。
3. 当前节点压栈。

### 算法：`scopeLevelFix(nodes)`

按顺序应用 5 条规则中的 3 条（规则 2、4 的具体描述在注释里提到「规则 1→5 顺序应用，规则 4 迭代至稳定」，但代码实际只显式实现了标号为「规则1」「规则3」「规则5」的三个方法，中间夹了一个「无编号」的统一 clamp 步骤，以及一个循环至多 12 轮的「父子层级差不超过1」修正——**这应该就是文档注释里所说的「规则 4」**，只是代码中没有专门命名为 `applyRule4...` 的方法，直接内联实现）：

1. 建立 `byId`（id→节点）、`children`（parentId→子节点列表）映射；`roots`（`parentId==null` 的节点）。
2. **规则 1**：对每个根的子树（BFS 收集），`applyRule1MinLevelShift`——子树内所有节点整体减去 `(最小level - 1)`，使最小层级归一为 1（子树内部相对层级差不变，只做整体平移）。
3. 全局 clamp：每个节点 `level = min(MAX_LEVEL, max(1, level))`（**这里的 `MAX_LEVEL` 是本文件的 `4`，不是最终输出的 6**——所以这一步会把规则 1 平移后可能产生的 `>4` 层级压到 4，这是树构建阶段内部的临时上限，后续 `HeadingReadingOrderValidator` 等步骤会用 `MAX_LEVEL=6` 重新处理）。
4. **规则 3**：对每个根的子树，`applyRule3HoleFix`——若子树内存在 H1、H3 但**没有** H2，且 H3 节点数 `>=2` 且这些 H3 节点按行序的 `seqQualityAdjacent >= 0.75`（伪造 `Candidate` 对象复用序列质量公式），则把所有 H3 提升为 H2（填补层级空洞）。
5. **规则 4（内联，未命名）**：最多迭代 12 轮，每轮检查所有非根节点，若 `本节点level > 父节点level + 1`（跳级）则压缩为 `父节点level + 1`；若整轮无变化提前退出。**这一步是全局的**（不按子树隔离，直接遍历 `nodes` 全体），与规则 1、3、5 的「仅在子树内」的约束（注释强调「禁止跨 S_R 统计 min/max level」）形成对比——规则 4 只是逐节点看自己和直接父节点的关系，不涉及跨子树的聚合统计，所以不违反这个约束。
6. **规则 5**：对每个根的子树，`applyRule5SingleFlatRoot`——若整个子树所有节点层级相同且 `>1`，仅当子树只有这一个节点（`sub.size()==1`，即孤立无子节点的根）时，才把它降为 H1；子树有多个同层节点时不处理（避免把一组本该同级的兄弟标题误判为顶级）。

### 算法：`collectSubtree(rootId, byId, children)`

BFS 遍历，用 `children` 预计算映射避免重复线性扫描（相对于对每个节点都扫一遍全部节点找子节点的 `O(n²)` 写法是优化）。

### 算法：`applyRule1MinLevelShift(sub)`

子树最小 `level` 记为 `m`；若 `m>1`，所有节点 `level -= (m-1)`。

### 算法：`applyRule3HoleFix(sub)`

见上文规则 3 描述；`HOLE_FIX_MIN_H3=2`，`HOLE_FIX_SEQ_QUALITY_MIN=0.75`。构造伪 `Candidate`（用节点自身的 `id/lineId/titleRaw/pattern/index`）只是为了复用 `seqQualityAdjacent` 函数签名，不产生实际候选副作用。

### 算法：`applyRule5SingleFlatRoot(sub)`

见上文规则 5 描述；`root=sub.get(0)`（**假设 BFS 收集的第一个元素就是根**——因为 `collectSubtree` 从 `dq.add(root)` 开始且 BFS 出队顺序保证根最先被加入 `out`，这个假设成立）；`root.parentId != null` 时提前返回（保护性检查，正常不会发生，因为 `collectSubtree` 只应该从真正的根节点调用）。

### 算法：`removeEmptyTitleNodes(nodes)`

`normalizeLine(titleRaw)` 为空的节点剔除。

### 算法：`extractExistingHeadings(lines)`

1. 逐行扫描（围栏内跳过）。
2. 匹配 `HEADING_LINE` 的行：`level = min(MAX_LEVEL(4), #个数)`（**注意这里截断到 4，不是 6**——即使原文是 `###### 标题`（6 个 `#`），这一步也会先截到 4；后续 `apply` 主流程里 `sourceMarkdownHeadingLevels`（单独收集、不受这次截断影响，见 `collectSourceMarkdownHeadingLevels`——那个方法用的是 `m.group(1).length()` 原始长度不截断）会在写回 `#` 时取 `max(level, sourceLevel)` 保底还原用户原始层级，所以最终不会真的丢失到 6 层的信息，只是本方法内部产出的「初始 level」被截断）。
3. 文本为空跳过（不产生命中）。
4. `MarkdownTitlePattern.matchFirst(text)` 尝试识别模式（可能为 `null`，不强制要求已有标题也命中某个编号模式）。
5. **配置注释误判防护**：若模式为 `null` **且** `looksLikeConfigCommentLineAmongPreformattedNeighbors(lines, i)` 为真 → 整行跳过，不产生命中（认为这是 `.vmoptions`/`.properties` 文件里的 `#` 注释而非 Markdown 标题）。
6. 构造命中加入结果（`patternKey` 为 `null` 时命中的 `patternKey` 也是 `null`）。

### 算法：`looksLikeConfigCommentLineAmongPreformattedNeighbors` / `isConfigDirectiveBeyondCommentRun`

1. 分别向上（`direction=-1`）和向下（`direction=1`）扫描，任一方向判定为「配置指令」即返回 `true`。
2. 每个方向最多跳 `CONFIG_COMMENT_NEIGHBOR_SCAN_HOPS(6)` 次：跳过空行和以 `#` 开头的行（视为同一注释块的延续或空白，继续跳）；遇到第一条既非空又不以 `#` 开头的「实质行」，判断：
   - 匹配 `SHELL_OR_JVM_FLAG_LINE`（`-x`/`--xxx=yyy` 形态）或 `XML_TAG_LINE`（`<tag ...>` 形态）→ `true`。
   - 否则委托 `MarkdownLineClassifier.classify(lines, i) == MarkdownLineKind.PREFORMATTED`（外部依赖，Part 6 推测范围）。
3. 6 跳内都是空行/`#` 注释续行、未找到任何实质行 → 返回 `false`。

### 算法：`meanLineGap(seg)`

`seg.size()<2` 返回 `Double.POSITIVE_INFINITY`（Go 用 `math.Inf(1)`）；否则相邻行号差的平均值。

### 算法：`removeOriginalTocBlock(lines)` / `removeAllCatalogLines(lines)`

- `removeOriginalTocBlock`：逐行扫描（围栏原样保留、只切换状态），遇到匹配 `TOC_HEADING` 的行，向下扫描判断是否紧跟着「目录条目」（`TOC_MD_LINK_LINE`/`TOC_PAGED_LINE`/`ChapterTocLineRemover.isChapterTocLine` 三者之一，空行允许穿插跳过），若确实有至少一条目录条目（`hasTocEntry`），则连同「目录」标题行本身**一并删除**整段（`i=j` 跳过，不把 `TOC_HEADING` 行加入输出）；若没有目录条目跟随（说明这只是一个普通标题恰好叫"目录"但后面不是真的目录列表），保留该行。
- `removeAllCatalogLines`：先调用外部 `ChapterTocLineRemover.stripLines(lines)`（Part 1-3 范围，删除 PDF 式目录页行），再调用本文件的 `removeOriginalTocBlock` 删除「目录」标题段。**调用时机**：仅当 `hits` 为空时才整体调用此函数（见 `apply` 主流程），意味着「一个标题都没识别出来」的极端情况下才做这种粗暴的全量目录清理；正常情况下（`hits` 非空）走的是 `stripCatalogLinesPreservingHits`（见下），因为需要保持命中行号的可映射性。

### 算法：`stripCatalogLinesPreservingHits(lines, catalog) -> CatalogStripResult`

在写入 `#` 标题前删除目录相关行，同时记录被删行号供命中行号重新映射（因为删除行会导致后续行号整体前移）：
1. `catalog` 非空时先把其 `tocLineRanges()` 全部展开为 `tocLines` 集合。
2. 逐行扫描（围栏原样保留、只切换状态；围栏内行**始终保留**，即使行号落在 `tocLines` 里——`!inFence && ...` 的条件保证围栏内不做任何目录删除判断）：
   - 若行号在 `tocLines` 中，或（非围栏内）匹配 `ChapterTocLineRemover.isChapterTocLine` → 该行不输出，记入 `removed`。
   - 否则若匹配 `TOC_HEADING`：与 `removeOriginalTocBlock` 相同逻辑向下扫描判断 `hasTocEntry`，是则整段（含标题行）都记入 `removed` 并跳过；否则按普通行处理。
   - 其余行正常输出。
3. 返回 `(过滤后的行列表, 全部被删行号集合)`（**`removed` 初始就包含 `tocLines`，即使这些行在扫描循环中也会被重复判定要删除，只是不会重复添加到 `Set`**）。

### 算法：`remapHeadingHitsAfterCatalogStrip(hits, removedLineIds)`

对每个命中，若其 `lineIndex` 本身就在 `removedLineIds` 中 → 剔除该命中（理论上不应发生，因为命中都是实际标题行，不该被判定为目录行删除，属于防御性处理）；否则新行号 `= lineIndex - countRemovedBefore(lineIndex, removedLineIds)`（减去该行之前被删除的行数，做行号左移），若结果 `<0` 也剔除（防御性）。

### 算法：`countRemovedBefore(lineIndex, removedLineIds)`

线性遍历 `removedLineIds` 集合统计小于 `lineIndex` 的个数——**`O(removed数量)` 的每次调用开销，且在 `remapHeadingHitsAfterCatalogStrip` 里对每个命中都调用一次，整体是 `O(hits数 × removed数)`**。Go 移植若关注性能，可以把 `removedLineIds` 排序后用二分查找替代线性统计（前缀计数），这是一个安全的性能优化、不改变语义（因为只是计算「小于某值的元素个数」，排序+二分与线性扫描结果完全一致）。

---

## 流水线执行顺序

`MarkdownHeadingStage` 是四阶段 MPP 流水线（阶段 1 噪声清理 → **阶段 2 层级标题识别（本类）** → 阶段 3 正文合并 → 阶段 4 目录生成与输出）的第二步。`MarkdownTocEmitStage`（本 Part 也覆盖）是阶段 4。

### 读取的 context 字段（入口时）

- `context.lines()`：阶段 1 产出的已清理正文行。
- `context.nonHeadingScopes()`：阶段 1 标记的非标题作用域（附件清单等）。
- `context.chapterTocCatalog()`：阶段 1 扫描出的章节目录快照。

### `apply(context)` 完整步骤（严格顺序，改动需谨慎——原注释明确标注「标题定稿四步，顺序勿改」）

1. 取 `lines`、`nonHeadingScopes`、`chapterTocCatalog`。
2. 快照原文已有 `#` 标题的行号集合与层级映射，写入 `context.setSourceMarkdownHeadingLineIndexes/Levels`（供后续「写回时不低于用户原有层级」使用）。
3. `extractExistingHeadings(lines)` 提取已有 `#` 标题的命中列表 `existing`。
4. 再次收集 `markdownHeadingLineIndexes`（内容同步骤 2 的快照，见前文关于重复调用的说明）。
5. `lineIndexesInScopes(nonHeadingScopes, lines.size())` 得到 `scopeExcludedFromMixed`。
6. `HeadingPatternQualityHeuristics.buildInferDisqualifiedPatternKeys(lines, existing, scopeExcludedFromMixed)`（外部，Part 4）得到全局黑名单 `inferDisqualifiedPatternKeys`——这是「推断阶段」要排除的模式键，与后面「定稿阶段」的黑名单是两次独立计算。
7. `filterExistingHeadingsByCounterEvidence(lines, existing, nonHeadingScopes, catalog, Set.of())` 对已有标题做反证降级（**副作用**：修改 `lines`，把被降级的行从 `#` 变回纯文本），更新 `existing`。
8. `inferHeadings(lines, nonHeadingScopes, markdownHeadingLineIndexes, inferDisqualifiedPatternKeys)` 推断出新的候选标题命中 `inferred`（不跳过已有 `#` 行，也排除全局黑名单模式）。
9. `mergeInferredAndExisting(inferred, existing)` 合并（已有优先）得到 `hits`。
10. `normalizeCnStructuralHeadingLevels(hits)`：原地修正「第X章」强制 H1、「第X条」保底 H2。
11. `ChapterTocHeadingValidator.augmentFromCatalog`：用目录快照补全缺失的一级「第X章」命中。
12. `ChapterTocHeadingValidator.validate`：剔除落在目录页行区间的命中，并强制目录匹配的「第X章」为 `level=1`。
13. `HeadingReadingOrderValidator.applyReadingOrderNesting`：单调栈修正阅读顺序嵌套（第一次）。
14. `applyHeadingSequenceConsistencyDemotion`：连续编号序列一致性再次降级（外部依赖 `HeadingSequenceConsistencyHeuristics`，Part 4）。
15. `HeadingLevelPrefixHeuristics.applyLevelPrefixConsistency(lines, hits)`：前缀一致性层级修正（**完全外部依赖，Part 4 范围，本类不展开算法**，但这一步可能把某些标题的层级改变到与「阅读顺序嵌套」不一致的状态，例如把「一、」从原本推断的第 3 层改到其「自然」层级）。
16. **再次** `HeadingReadingOrderValidator.applyReadingOrderNesting`：因为步骤 15 可能打乱嵌套关系（注释明确说明：「前缀一致可能把『一、』等改到自然层级（如 3）；再嵌套一次，使 H1 下首个子标题为 H2」）。
17. `filterHitsByListGuideScopes(hits, nonHeadingScopes)`：剔除落在非标题作用域内的命中，得到 `accepted`/`rejectedLineIndexes`。
18. 对 `rejectedLineIndexes` 中的行，`demoteHashHeadingToBold` 降级为加粗正文（**副作用**：修改 `lines`）。
19. `HeadingPatternQualityHeuristics.detectDisqualifiedPatternKeys(lines)`（外部）得到「定稿阶段」的黑名单初始集合 `finalDisqualifiedPatternKeys`（与步骤 6 的黑名单是独立的第二次计算，输入也不同——这次只依赖当前 `lines` 状态，不依赖 `hits`）。
20. `ChapterTocHeadingValidator.bodyLineIndexesForCatalog`：找出目录章条在正文中已匹配的行号，并入 `excludedFromMixedUnrecognized`（与步骤 17 的 `rejectedLineIndexes` + 非标题作用域内全部行号合并）。
21. `HeadingPatternQualityHeuristics.detectMixedRecognitionPatternKeys(lines, finalHitLineIndexes, excludedFromMixedUnrecognized)`（外部）：检测「混合识别」模式键（某个模式在全文里有的地方被识别为标题、有的地方没有，不一致的模式判定为不可信），并入 `finalDisqualifiedPatternKeys`。
22. `HeadingPatternQualityHeuristics.filterHitsAndDemoteLines(lines, hits, finalDisqualifiedPatternKeys, catalog)`（外部）：用最终黑名单过滤 `hits` 并（大概率有副作用）同步降级对应行文本——**这是「标题定稿四步」的第 1 步「推断/校验/质量过滤」的收尾**。
23. 若 `hits` 非空：`stripCatalogLinesPreservingHits` 删除目录相关行并重映射行号（更新 `lines` 和 `hits`）；否则（`hits` 为空）：`removeAllCatalogLines` 粗暴删除全部目录相关内容（不需要重映射，因为没有命中要保留）。
24. **标题定稿四步**（顺序固定）：
    - **第 1 步**（收尾）：`ChapterTocHeadingValidator.enforceCatalogChapterLevels` + `shiftHeadingLevelsBelowNewPlainCatalogChapters`。
    - **第 2 步**：遍历全部 `hits`，写入 `#` 到 `lines`：写入层级取 `hit.level`，但若该命中匹配目录章节则强制 `1`，否则若原文该行本来就有 `#`（`sourceMarkdownHeadingLevels` 命中）则取 `max(hit.level, 原始层级)`（保底不降低用户原有层级）；记录写入的行号到 `hierarchyLineIndexes`。
    - **第 3 步**：`UnmarkedParentHeadingHeuristics.demoteMisplacedSectionHeadings` 补偿降级（修改 `lines`、同步 `hits`），并从 `hierarchyLineIndexes` 中移除被降级的行号。
    - **第 4 步**：`demoteNonHierarchyHashHeadings`：清理不在 `hierarchyLineIndexes` 内、但行面仍是 `#` 形式的残留（这些是之前步骤间接产生但从未被认定为「层级标题」的 `#` 行，统一降级为加粗）。
25. 写回 `context`：`setLines`、`setHits`、`setHierarchyLineIndexes`、`setFinalDisqualifiedPatternKeys`。

### 写入的 context 字段（出口时）

- `context.lines()`：已写入 `#` 标题标记、目录行已删除、非层级残留 `#` 已清理的行数组（供阶段 3 正文合并消费）。
- `context.hits()`：定稿的标题命中列表（供阶段 4 `MarkdownTocEmitStage` 消费，生成目录与 slug）。
- `context.hierarchyLineIndexes()`：最终真正是层级标题的行号集合（供阶段 3 判断“这一行是标题，合并时不能把它并入上一段正文”——`MarkdownLineKind.HEADING` 的分类很可能直接依赖这个集合，但具体判断逻辑在 Part 6 的 `MarkdownLineClassifier`/阶段 3 中）。
- `context.finalDisqualifiedPatternKeys()`：全文范围内被判定不可信的模式键集合（供后续阶段或调试/测试参考，本 Part 内未见阶段 3/4 直接消费这个字段，可能是供测试断言或未来功能使用）。

### 与 Part 6（推测的「正文合并/噪声清理」）的衔接点

- Part 6 阶段 1（若存在，噪声清理）需要在调用本阶段之前产出：`context.lines()` 初始值、`ChapterTocCatalog`（可以复用本 Part 提供的 `ChapterTocCatalog.parse`，或由阶段 1 自己调用后存入 context）、`nonHeadingScopes`、`attachmentScopesForMerge`。
- Part 6 阶段 3（正文合并）需要消费本 Part 产出的：`hits`（跳过标题行不参与合并）、`hierarchyLineIndexes`（更精确地判断哪些行是「已定稿的层级标题」，不同于单纯正则匹配 `#` 前缀——因为定稿后仍可能有残留的非层级 `#`，但第 4 步已经清理掉了，所以理论上阶段 3 只需要检测行面 `#` 前缀就等价于 `hierarchyLineIndexes`，除非阶段 3 在本阶段之外还有其他来源产生新的 `#` 行）。
- `MarkdownLineClassifier`（阶段 3 使用，本 Part 未展开其实现，仅在 `isConfigDirectiveBeyondCommentRun` 里调用其 `classify` 方法读取 `PREFORMATTED` 分类）与 `MarkdownWeakMergeHeuristics`（`isTableLikeLine`/`isQuoteOrRuleLine`，供既有标题反证降级判断复用）大概率是 Part 6 的产出物，需要在整体移植时确认其 Go 包路径以便本 Part 代码 import。
- `MarkdownNoiseCleanupStage.isInAnyScope` 是一个跨阶段共享的纯函数（判断行号是否落在给定区间列表内），本文档在「MarkdownLineRange」一节建议将其固化为 `LineRange.Contains` + 一个 `AnyRangeContains(ranges []LineRange, lineID int) bool` 辅助函数，供 Part 5、Part 6 共同调用，避免两边各自实现一份。

---

## 移植注意事项汇总（供实现者速查）

1. **6 处正则含环视断言**，均已给出「匹配+手动边界检查」的具体改写方案，见文首「Go regexp 兼容性预警」。
2. `MarkdownHeadingStage` 中 `WEAK_MERGE_*`（10 个）常量**声明但未被任何方法使用**，Go 移植可跳过，仅需注释存档说明（避免误以为遗漏）。
3. `ChapterTocHeadingValidator.chapterNumberKey`/`headingKeysMatch` 存在「阿拉伯数字与中文数字不互认」的已知限制（如「第1章」与「第一章」不算同一章号），按原样移植，不要自作主张修复，除非用户明确要求。
4. `HeadingReadingOrderValidator` 的单调栈同时维护「参考层级」与「实际层级」两个独立的量，Go 移植不能合并这两个字段。
5. `filterExistingHeadingsByCounterEvidence` 的 `headingSequenceDemoteLineIds` 参数在当前唯一调用点始终传空集合，逻辑保留但当前不生效，Go 移植时如实保留这个「看似冗余但有存在原因」的参数。
6. `isPrefixHeadingButBodyLikeSentence` 与 `isExistingHeadingBodyLikeSentence` 算法完全重复，Go 移植建议合并为一个共享函数（安全重构，已核对两处参数与阈值完全一致）。
7. `collectMarkdownHeadingLineIndexes` 在 `apply` 中被调用两次但输入相同、输出等价，Go 移植可合并为一次调用（安全的性能优化）。
8. `countRemovedBefore` 是 `O(removed数)` 的线性扫描，在 `remapHeadingHitsAfterCatalogStrip` 里被多次调用，整体 `O(hits×removed)`；Go 侧可选用排序+二分优化，不改变语义。
9. `resolveScope` 递归深度硬上限 `MAX_LEVEL=4`；`scopeLevelFix` 内部 clamp 也用这个值；最终输出层级的硬上限是 `HeadingReadingOrderValidator`/`ChapterTocHeadingValidator` 使用的 `6`（`MAX_HEADING_LEVEL`）——两个不同阶段用不同的层级上限常量，Go 移植务必对应到各自所在文件的常量，不要混用。
10. `extractExistingHeadings` 中的 `level = min(MAX_LEVEL(4), #个数)` 与随后写回 `#` 时的 `max(level, sourceLevel)` 保底逻辑配合，才能保证「原文 6 级标题最终不会丢失层级信息」，Go 移植必须完整保留这条链路（`collectSourceMarkdownHeadingLevels` 不做 4 级截断，`extractExistingHeadings` 做截断，两者数据在 `apply` 步骤 24 第 2 步汇合），不能只移植其中一部分。
