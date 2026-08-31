# PDF 移植 Part 2：样式聚类、标题识别与 Markdown 渲染

来源文件：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/PdfToMarkdown.java`（共 5215 行）。
本文档覆盖该文件中"全文样式聚类 → 标题候选/层级判定 → 跨行标题合并 → 正文/标题 Markdown 渲染"这条链路。行号引用均为该 Java 源文件的行号，供交叉核对，不代表 Go 实现的行号。

## 覆盖范围

**样式聚类**
- `buildHeadingStyleProfile`（L2775-2844）
- `styleClusterKeyOf`（L2846-2856）
- `isEligibleForStyleClustering`（L2858-2862）
- `inferPageHeightsFromBlocks`（L2864-2875）
- `inferFallbackPageHeight`（L2877-2880）
- `assignClusterRoles`（L2882-2942）
- `medianFontSize`（L2944-2954）
- `isLikelyNoiseBlock`（L2956-2971）
- 类：`HeadingStyleProfile`（L5043-5158）、`StyleCluster` + `StyleCluster.Builder`（L4946-5037）、`StyleClusterKey`（L4887-4943）、`StyleClusterRole`（L4874-4881）

**标题候选/层级判定**
- `isHeadingCandidate`（L2682-2690）
- `isLikelyHeadingText`（L2692-2698）
- `normalizePdfFontFamilyForCompare`（L2705-2730，样式比较基础设施，标题合并与聚类都依赖它）
- `sameFontFamily`（L2732-2737）
- `isCenteredOnPage`（L2743-2748）、`isCenteredStructuralChapterHeading`（L2753-2758）
- `isHeading`（六个重载，L3215-3350 主体逻辑在最后一个全参数重载）
- `passesHeadingStyleProfileGate`（L3355-3377）
- `fontMatchesBodyCluster`（L3379-3383）
- `isBodyOrNoiseLikeForShortPhraseListRun`（L3390-3403）
- `hasStrongHeadingSemantics`（L3408-3421）
- `hasChapterTitleNameAfterPrefix`（L3427-3433）
- `looksLikeRegexMatchedBodyParagraph`（L3435-3448）+ `countPatternMatches`（L3450-3455）+ `textAfterNumericHierarchyPrefix`（L3458-3465）
- `detectHeadingPrefixStyleMismatch`（L3467-3486）+ `dominantStyle`（L3488-3510）
- `hasMiddleChinesePeriod`（L3512-3518）
- `looksLikeCnParenBodyEnumeration`（L3521-3527）
- `isStandaloneCnArticleHeading`（两个重载，L3530-3548）+ `looksLikeCnArticleBodyParagraphLead`（L3550-3552，委托 `HeadingSuppressHeuristics`）
- `restAfterCnArticlePrefix`（L3554-3557，未见调用点，可能是已废弃的死代码——移植时确认后再决定是否保留）
- `startsWithCnArticleHeading`（L3560-3562，委托）
- `isHeadingByRegex`（L3564-3576）
- `resolveHeadingLevel`（两个重载，L742-756）
- `normalizeCnStructuralHeadingLevel`（L759-766）
- `legacyHeadingLevel`（L908-917）
- `isReliableStyleClusterHeading`（L863-869）
- `shouldSuppressByShortPhraseListRun`（L875-891）
- `isStyleClusterProtectedFromShortPhraseListRun`（L893-896）
- `resolveHeadingLevelSourceTraceLabel`（L898-905）
- `resolveStyleClusterTraceLabel`（L826-833）
- `resolveSequenceRunTraceLabel`（L835-858）
- `recognizesBlockAsHeadingForSequenceConsistency`（L569-584，用于标题序列一致性降级扫描的"像标题"判定）

**跨行标题/正文合并**
- `mergeLines`（L2268-2288）、`mergeWrappedHeadingLines`（L2314-2331）
- `merge`（普通行合并，L2333-2359）、`mergeHeadingBlocks`（标题合并，L2361-2387）
- `mergeText`（L2389-2398）
- `shouldMerge`（两个重载，rejection-first 规则集，L2407-2511）
- `shouldBlockMergeAtChapterHeadingBoundary`（L2518-2528）
- `hasTypographicBoundary`（L2533-2535，委托 `styleDifferent`）
- `isChapterPrefixWithTitleNamePair`（L2538-2556）
- `canMergeHeadingPair`（L2558-2628，标题跨行合并的独立判定）
- `traceHeadingMergeDecision`（L2630-2673）+ `previewForTrace`（L2675-2680）
- `isHeadingCandidate`（同上，供 `canMergeHeadingPair` 复用）
- `headingLastTopRef`（L2973-2976）
- `headingCenterForMergePair`（L2982-2993）、`headingCenterFromMetrics`（L2995-3010）、`headingCenter`（L3012-3018）
- `isForcedContinuation`（L3020-3022）、`isInlineTailContinuation`（L3024-3030）
- `startsWithContinuationPunctuation`（L3032-3035）、`shouldDropDuplicatedBoundary`（L3037-3043）
- `styleDifferent`（L3045-3054）
- `isFullWidthHardWrapLeadLine`（L3067-3074）
- `layoutBreak`（L3085-3109）
- `isFullWidthBodyLine`（L3115-3125）
- `isAcrossTable`（L3127-3130）
- `isShortLabel`（L3132-3134）
- `isNumericHierarchyHardWrapContinuation`（L3139-3154，跨行标题续行辅助规则）
- `shouldBlockLeftAlignedDateLineMerge`（L3171-3178，落款日期行不与机关名合并）
- `endsWithSentenceTerminator`（L3180-3184）、`endsWithSemanticBreak`（L3186-3189）
- `shouldForcePlainDueToParagraphContinuation`（L3195-3201）

**Markdown 渲染**
- `appendTextAsMarkdown`（三个重载，L586-653）
- `appendTextBodyAsMarkdown`（L655-705）
- `ensureBlankLineBeforeBlock`（L708-719）
- `shouldEmitShortPhraseSkipTrace`（L768-778）
- `buildHeadingTraceComment`（L791-824）
- `textBlockWithText`（L919-944）
- `collapseIntraBlockLineBreaks`（L946-958）
- `appendListContinuationInline`（L960-969）
- `shouldMergeListWithPlainContinuation`（L1233-1253，渲染期"列表项+续行内联"判定，正文渲染路径与 `fallbackMergeMarkdown` 共用）

**表格→文本降级渲染**（辅助渲染路径，与 Part 1 的表格提取相邻但输出属于本文档职责）
- `appendSingleCellTableAsText`（L3793-3807）
- `renderTableMarkdown`（L3814-3861）
- `escapePipe`（L3863-3865）

**数据结构（本 Part 定义/使用）**
- `Config`（字段与 `defaults()`，L4397-4656；仅摘录本 Part 用到的字段）
- `TextBlock`（L4686-4843，Part 1 产出，本 Part 大量读取）
- `GeometricElement`（L4658-4684，`TextBlock`/`TableBlock` 基类）
- `StyleToken`（L4845-4859）、`StyleSignature`（L4861-4870）

**不在本 Part 范围内，仅记录调用点**
- `fallbackMergeMarkdown` 及其子函数（`splitMarkdownBlocks`/`classifyBlock`/`BlockParts`/`BlockKind` 等，L975+）：文档级兜底断句合并，属于 Part 3"最终清理"范围；本 Part 的渲染输出（`convertDocument` 主循环产出的 `StringBuilder out`）是它的输入。
- `removeTocFromMarkdown`、`cleanOutput`：Part 3 范围。
- `detectHeadingSequenceConsistencyDemoteBlockIds`（L547-560）、`detectDisqualifiedPatternDemoteBlockIds`（L306-327）：这两个函数本身委托给 `HeadingSequenceConsistencyHeuristics`/`HeadingPatternQualityHeuristics`（未在本文件定义，属于其他 top-level 类的移植范围），但本 Part 的 `isHeading`/`appendTextBodyAsMarkdown` **消费**它们产出的 `headingSequenceDemoteBlockIds` 集合（作为参数传入，命中时强制非标题）。移植时只需把这个 `Set<string>` 当作外部输入即可，不需要在本 Part 重新实现判定逻辑。
- `ListGuideHeuristics.detectListGuideScopeBlockIds`、`ShortPhraseListRunHeuristics.detectPdfShortPhraseListRuns`：同上，外部产出集合作为参数消费。
- 表格提取（`extractTableBlocks`/`extractTextBlocks`/`groupFragmentsIntoLines`/`buildLineBlock`/跨页表合并/页眉页脚过滤等）：Part 1 范围。
- `HeadingSuppressHeuristics`、`ChapterTocLineRemover`、`MarkdownStructureRules`、`HeadingLevelPrefixHeuristics`、`ChapterReferenceHeuristics`、`MarkdownHeadingStage`（mpp 包）：均为独立类/文件，属于其他 agent 的移植范围；本文档只标注调用点与依赖的方法签名/语义。

## 常量与阈值

| 常量 | 值 | 用途 |
|---|---|---|
| `STYLE_CLUSTER_FONT_BUCKET_PT` | 0.5 | 样式聚类字号分桶步长（pt），`Math.round(fontSizeMean/0.5)*0.5` |
| `STYLE_CLUSTER_INDENT_BUCKET_PT` | 8.0 | 样式聚类左缩进分桶步长（pt） |
| `STYLE_CLUSTER_H1_FONT_DELTA_PT` | 2.5 | 判定 H1 簇相对正文簇的最小字号差（pt） |
| `STYLE_CLUSTER_H1_MAX_SHARE` | 0.08 | H1 候选簇占全部符合聚类条件块数的最大占比（`h1MaxCount = max(3, ceil(eligible*0.08))`） |
| `STYLE_CLUSTER_NOISE_BAND_RATIO` | 0.5 | 簇内落在页眉/页脚带等噪声块占比 ≥ 此值 → 整簇标记 NOISE |
| `CENTERED_HEADING_PAGE_RATIO` | 0.15 | 判定"页面居中"的容差：`|blockCenter - pageWidth/2| <= pageWidth*0.15` |
| `PARAGRAPH_CONTINUATION_X_TOLERANCE_EM` | 3.6 | 段落延续判定放宽的行首偏移容差（em），覆盖中文首行缩进(~2字符)/列表悬挂缩进(~3.5字符) |
| `FULL_WIDTH_RIGHT_GAP_TOLERANCE_EM` | 2.5 | 铺满行宽判定：行右边缘距正文右边界的容差（em） |
| `CONTINUATION_PUNCTUATION` | `"，。；：、！？,.!?;:）)]】》"` | 续行强制合并标点集合（起首标点/重复边界标点） |
| `CROSS_PAGE_TABLE_X_TOLERANCE_PT`（Part1用，本文档只读） | 6.0 | 跨页表格左右边界对齐容差 |
| `DECORATIVE_SINGLE_CELL_MAX_LINES`（Part1用） | 2.5 | 装饰性 1x1 表格的高度上限（行高倍数） |
| `Config.fontSizeDeltaPt` 默认值 | 0.5 | "字号显著大于正文"的阈值增量（pt），多处判定共用 |
| `Config.maxHeadingLength` 默认值 | 80 | 标题候选最大长度（字符数），派生用途见各函数 `*2`/`*3` |
| `Config.headingMergeFontDeltaPt` 默认值 | 1.2 | 标题跨行合并允许的字号差上限（pt） |
| `Config.headingMergeCenterTolerancePt` 默认值 | 24.0 | 标题跨行合并允许的居中中心点偏差上限（pt） |
| `Config.headingMergeMaxGapMultiplier` 默认值 | 2.2 | 标题跨行合并允许的行距上限（相对 `max(lineHeight)` 的倍数） |
| `Config.indentThresholdPt` 默认值 | 8.0 | 普通行合并缩进变化阈值（pt） |
| `Config.xOffsetThresholdPt` 默认值 | 10.0 | 普通行合并行首 X 偏移阈值（pt） |
| `Config.lineSpacingMultiplier` 默认值 | 2.4 | 普通行合并行距阈值倍数（相对 `max(lineHeight,1.0)`） |
| `Config.styleClusterHeadingEnabled` 默认值 | `true` | 是否启用全文样式聚类门控（`isHeading`/`resolveHeadingLevel` 均受控） |
| 段落延续放宽的行距倍数下限 | `max(config.lineSpacingMultiplier, 2.8)` | `shouldMerge` 内联计算，非独立常量 |
| `HeadingStyleProfile` 可靠性判据 | `bodyCluster != null && bodyCluster.blockCount>=3 && eligibleBlocks>=5 && bodyCluster.blockCount >= eligibleBlocks*0.20` | 决定 `HeadingStyleProfile.isReliable()`，不可靠则调用方回退旧 pt/字重逻辑 |
| H1 打分公式 | `meanFontSize*10.0 + (centered?5.0:0.0) + (fontWeight>=600?2.0:0.0) - blockCount` | 多个 H1 候选簇中取最高分者 |
| `detectHeadingPrefixStyleMismatch` 差异阈值 | 字体族名不同 / 字重跨 600 分界 / 字号差 `>=0.8pt`，命中 `diffCount>=1` 即判定不一致 | 前缀（编号）与正文样式不一致检测 |
| `isFullWidthBodyLine` 容差 | `FULL_WIDTH_RIGHT_GAP_TOLERANCE_EM * max(fontSizeMean,1.0)` | 右边缘容差（绝对单位 pt，由 em 换算） |
| `estimatePageRightEdge` 兜底条件 | `max < pageWidth*0.6` 时返回 NaN | 全页仅短行时不可信任右边界 |
| `shouldBlockLeftAlignedDateLineMerge` 容差 | `leftDelta ∈ [-tol, tol*3.0]`，`tol=config.xOffsetThresholdPt` | 落款机关名+日期行左对齐判定 |
| `INLINE_TAIL_TOKEN` 判定 spacing 上限 | `maxLine*2.5` | 内联 ASCII/数字尾巴续行（如 "AI+"） |
| `shortPhrase*` 系列（`Config`） | `shortPhraseNumberedRunMin=3`、`shortPhraseNumberedRunMaxGap=3`、`shortPhraseNumberedRunMaxBodyLines=1`、`shortPhraseNumberedRunSeqQualityMin=0.8`、`shortPhraseNumberedBodyMaxLen=18` | 由 `ShortPhraseListRunHeuristics`（外部类）消费，本文档只做参数传递说明 |

## 数据结构

以下为 Go 化的字段草案（命名沿用 camelCase 转 Go 惯例，具体导出/非导出视 Go 包边界而定；`Rectangle`/`geom.Box` 用 docmill 提供的几何类型替代）。

```go
// TextBlock 对应 Java TextBlock（Part 1 产出，Part 2 大量读取/派生新副本）。
type TextBlock struct {
    GeometricElement // 内嵌：ID, PageNo, BBox, TopDistance, Left

    Text        string
    FontSizeMean float64
    FontFamily   string
    FontWeight   int     // 400=常规, 700=粗体（>=600 视为粗体）
    Italic       bool
    MonoFont     bool
    LineHeight   float64
    IndentLeft   float64
    TableID      int     // -1 表示不属于任何表格
    BodyFontMode float64

    // 跨行合并累积字段：未合并过时与首行自身一致
    HeadingLastLineTop   float64 // NaN 表示未设置，退化用 TopDistance
    HeadingTrailingLeft  float64 // 最后一次合并进来的行的 Left；NaN 表示未设置
    HeadingTrailingText  string  // 最后一次合并进来的行的规范化文本；空表示未设置
    PageWidth            float64
    HeadingPrefixStyleMismatch bool // 前缀（编号）与正文样式不一致
}

// GeometricElement 是 TextBlock / TableBlock 的公共基础（Part 1 已定义，此处仅注明字段形状）。
type GeometricElement struct {
    ID          string
    PageNo      int
    BBox        *geom.Box // nil 允许（测试构造的块可能没有 bbox）
    TopDistance float64
    Left        float64
}

// StyleClusterRole 样式簇语义角色。
type StyleClusterRole int
const (
    RoleBody StyleClusterRole = iota
    RoleH1
    RoleH2
    RoleNoise
    RoleUnknown // 零值也可设为 Unknown，视 Go 习惯而定；未归类，可靠剖面下回退旧 pt 逻辑
)

// StyleClusterKey 样式聚类签名：字号分桶/字重/字体族/缩进分桶/是否居中。
type StyleClusterKey struct {
    FontSizeBucket float64
    FontWeight     int    // 700 或 400
    FontFamilyNorm string
    IndentBucket   float64
    Centered       bool
}
// 需要可比较（Go struct 天然支持 == ；用作 map key 需保证字段均为可比较类型，均满足）。

// StyleCluster 单一样式簇统计。
type StyleCluster struct {
    Key           StyleClusterKey
    BlockCount    int
    MeanFontSize  float64
    MeanIndentLeft float64
    NoiseScore    float64 // noiseHits / blockCount
    BlockIDs      []string
    Role          StyleClusterRole // 可变字段，聚类后续赋值
}

// StyleClusterBuilder 聚合中间态（对应 Java StyleCluster.Builder）。
type StyleClusterBuilder struct {
    Key          StyleClusterKey
    BlockIDs     []string
    FontSizeSum  float64
    IndentSum    float64
    NoiseHits    int
}
func (b *StyleClusterBuilder) AddBlock(block *TextBlock, pageHeight float64, config Config) { ... }
func (b *StyleClusterBuilder) Build() StyleCluster { ... }

// HeadingStyleProfile 全文标题样式剖面。
type HeadingStyleProfile struct {
    Clusters        []StyleCluster
    BodyClusterKey  *StyleClusterKey // nil 表示无
    H1ClusterKey    *StyleClusterKey
    H2ClusterKey    *StyleClusterKey
    BlockRoles      map[string]StyleClusterRole      // blockID -> role
    BlockClusterKeys map[string]StyleClusterKey       // blockID -> key
    Reliable        bool
    EligibleBlockCount int
}
func EmptyHeadingStyleProfile() HeadingStyleProfile { ... } // 对应 Java EMPTY 单例
func (p HeadingStyleProfile) IsReliable() bool { return p.Reliable }
func (p HeadingStyleProfile) ShouldFallbackToLegacyHeuristics() bool { return !p.Reliable }
func (p HeadingStyleProfile) RoleOf(block *TextBlock) StyleClusterRole { ... } // 缺省 Unknown
func (p HeadingStyleProfile) ClusterKeyOf(block *TextBlock) *StyleClusterKey { ... }
func (p HeadingStyleProfile) ClusterFor(block *TextBlock) *StyleCluster { ... }
func (p HeadingStyleProfile) BodyCluster() *StyleCluster { ... }
func (p HeadingStyleProfile) H1Cluster() *StyleCluster { ... }
func (p HeadingStyleProfile) H2Cluster() *StyleCluster { ... }
// SuggestedHeadingLevel: !Reliable 或 block==nil 时返回 (0, false)；
// RoleOf==H1 -> (1, true)；RoleOf==H2 -> (2, true)；否则 (0, false)。
func (p HeadingStyleProfile) SuggestedHeadingLevel(block *TextBlock) (level int, ok bool) { ... }

// styleToken：行内单个 fragment 的样式片段（正文中相对位置区间 [start,end) + 样式）。
type styleToken struct {
    Start, End int
    FontFamily string
    FontWeight int
    FontSize   float64
}

// styleSignature：dominantStyle 的返回值形状。
type styleSignature struct {
    FontFamily string
    FontWeight int
    FontSize   float64
}
```

`Config` 字段（本 Part 用到的子集，完整字段清单见 Part 1/汇总文档，此处仅列出本 Part 直接读取的项）：

```go
type Config struct {
    FontSizeDeltaPt            float64 // 0.5
    IndentThresholdPt          float64 // 8.0
    XOffsetThresholdPt         float64 // 10.0
    LineSpacingMultiplier      float64 // 2.4
    EmitTraceComments          bool    // false
    EmitHeadingTrace           bool    // false
    MergeWrappedHeadings       bool    // true
    MaxHeadingLength           int     // 80
    HeadingMergeFontDeltaPt        float64 // 1.2
    HeadingMergeCenterTolerancePt  float64 // 24.0
    HeadingMergeMaxGapMultiplier   float64 // 2.2
    StyleClusterHeadingEnabled bool // true
    ShortStopwords             map[string]struct{} // 小写化的英文短停用词集合
    // ... 其余字段（表格/页眉页脚/TOC等）属于 Part 1/Part 3
}
```

## 算法：`buildHeadingStyleProfile`

输入：`blocks []*TextBlock`（Part 1 产出，`convertDocument` 中已过滤掉 `monoFont` 块——注意调用方 `orderedTextBlocks` 已排除等宽字体块，见「与其他 Part 的接口」）、`config Config`。

1. `blocks` 为空 → 返回 `EmptyHeadingStyleProfile()`。
2. `pageHeights := inferPageHeightsFromBlocks(blocks)`。
3. 用 `map[StyleClusterKey]*StyleClusterBuilder`（Go 中用有序 key 列表模拟 Java `LinkedHashMap` 的插入序，若不需要严格插入序仅需保证聚合正确即可，但排序阶段会重新排序，所以插入序在 Go 中可忽略，直接用普通 map）遍历所有 block：
   - `!isEligibleForStyleClustering(block)` → 跳过。
   - `eligibleBlocks++`。
   - `key := styleClusterKeyOf(block)`。
   - `pageHeight := pageHeights[block.PageNo]`，缺失则 `inferFallbackPageHeight(block)`。
   - 若 `key` 尚无 builder，创建；调用 `builder.AddBlock(block, pageHeight, config)`。
4. `eligibleBlocks == 0` → 返回 Empty。
5. 对每个 builder 调用 `Build()` 得到 `[]StyleCluster`。
6. 排序：主键 `BlockCount` 降序，次键 `Key.FontSizeBucket` 降序（Java: `Comparator.comparingInt(blockCount).reversed().thenComparingDouble(c -> -fontSizeBucket)`）。
7. `assignClusterRoles(clusters, eligibleBlocks, config)`（原地修改各 cluster 的 Role）。
8. 构建 `blockRoles`/`blockClusterKeys`：遍历每个 cluster 的 `BlockIDs`，写入两个 map。
9. 在 `clusters` 中分别查找第一个 `Role==Body`/`Role==H1`/`Role==H2` 的簇（找不到为 nil）。
10. 可靠性判据（见常量表）：`reliable := bodyCluster!=nil && bodyCluster.BlockCount>=3 && eligibleBlocks>=5 && bodyCluster.BlockCount>=float64(eligibleBlocks)*0.20`。
11. 返回组装好的 `HeadingStyleProfile`。

## 算法：`styleClusterKeyOf`

1. `fontSizeBucket = round(block.FontSizeMean/0.5)*0.5`。
2. `fontWeight = 700 if block.FontWeight>=600 else 400`。
3. `fontFamilyNorm = normalizePdfFontFamilyForCompare(block.FontFamily)`。
4. `indentBucket = round(block.IndentLeft/8.0)*8.0`。
5. `centered = isCenteredOnPage(block)`。
6. 组装 `StyleClusterKey`。

## 算法：`isEligibleForStyleClustering`

`block==nil || block.MonoFont` → false；`block.Text` 为 nil 或全空白 → false；否则 true。

## 算法：`inferPageHeightsFromBlocks`

对每个非 nil block：`extent = block.TopDistance + max(block.LineHeight, block.FontSizeMean*1.2)`；按 `PageNo` 取多个 block 中的最大值（`merge` 语义：`Math.max`）。第二遍：对每个 page 的聚合值做 `value*1.05 + 24.0`，得到最终估计页高。返回 `map[int]float64`。

## 算法：`inferFallbackPageHeight`

`block==nil` → 842.0（A4 默认高度，pt）。否则 `max(600.0, block.TopDistance + max(block.LineHeight, block.FontSizeMean*1.2) + 48.0)`。

## 算法：`assignClusterRoles`

1. 对每个 cluster：`NoiseScore >= 0.5` → `Role = Noise`。
2. 选 Body 簇：在非 Noise 簇中，按 `(BlockCount 降序, |MeanFontSize - medianFontSize(clusters)| 升序)` 取最大者（Java `max` + 复合 comparator：先比较 blockCount，相等再比较 `-|delta|` 即取差值最小者）。若存在则设为 `Role=Body`。
3. `bodyFont := bodyCluster!=nil ? bodyCluster.MeanFontSize : 12.0`。
4. `h1MaxCount := max(3, ceil(eligibleBlocks * 0.08))`。
5. 遍历候选 H1：跳过 Noise 簇和刚选中的 Body 簇；跳过 `BlockCount > h1MaxCount` 的簇；
   - `largeEnough := MeanFontSize >= bodyFont + 2.5`
   - `centeredBold := Key.Centered && Key.FontWeight>=600`
   - 二者都不满足则跳过；
   - `score := MeanFontSize*10.0 + (Centered?5.0:0.0) + (FontWeight>=600?2.0:0.0) - BlockCount`
   - 取 `score` 最大者为 H1 候选（严格 `>`，Java 用 `bestH1Score` 初始 `NEGATIVE_INFINITY`）。
   - 找到则设 `Role=H1`。
6. `h1Font := h1Cluster!=nil ? h1Cluster.MeanFontSize : bodyFont+2.5`。
7. 遍历候选 H2：**只考虑当前 `Role==Unknown` 的簇**（已定 Body/H1/Noise 的排除）；
   - `boldSameSize := Key.FontWeight>=600 && MeanFontSize <= bodyFont+config.FontSizeDeltaPt`
   - 若 `MeanFontSize < bodyFont+config.FontSizeDeltaPt && !boldSameSize` → 跳过
   - 若 `h1Cluster!=nil && MeanFontSize >= h1Font-config.FontSizeDeltaPt` → 跳过（避免与 H1 字号重叠）
   - `boldOrRaised := Key.FontWeight>=600 || MeanFontSize>=bodyFont+1.0`；不满足则跳过
   - 按 `BlockCount` 最大者选中（严格 `>`，`bestH2Count` 初始 `-1`）
   - 找到则设 `Role=H2`。

注意：H1/H2 挑选都是"全局单一簇"，不是每种角色可能有多个簇。

## 算法：`medianFontSize`

把每个 cluster 的 `MeanFontSize` 按 `BlockCount` 重复次数展开成一个多重集合，排序后取中位数（`size/2` 下标，整数除法，即 Java 的"取中间偏右"策略，不做奇偶平均）。空集合返回 12.0。

## 算法：`isLikelyNoiseBlock`

1. `block==nil` → true。
2. `normalizeText(block.Text, config)` 为空 → true。
3. `isPageNumberBlock(block, pageHeight, config)` → true（Part 1 函数，直接调用）。
4. `isInHeaderFooterBand(block.TopDistance, pageHeight, config)`：
   - 若 `ChapterTocLineRemover.ShouldPreserveInHeaderFooterBand(text)` → false（受保护，不算噪声）
   - 否则 → true。
5. `block.TableID >= 0 && len(text) <= 40` → true（表格内短文本视为噪声，如表内小标签）。
6. 否则 false。

## 算法：`isHeadingCandidate`

供 `canMergeHeadingPair` 判定"两侧是否都像标题"使用（注意：与后面渲染路径的 `isHeading` 是不同的、更宽松的判定）。

1. `block==nil` → false。
2. `text := normalizeText(block.Text, config)`；空 → false。
3. `len(text) > config.MaxHeadingLength*2` → false。
4. `isHeadingByRegex(text)` → true。
5. `isLikelyHeadingText(text) && block.FontWeight>=600` → true。
6. 否则 `block.FontSizeMean >= bodyFontMode + config.FontSizeDeltaPt`。

## 算法：`isLikelyHeadingText`

`text` trim 后为空 → false；长度 `>42` → false；不含句末/断句标点集合 `[。！？；：,.!?;:]` 中任一字符 → true（即：短且无强标点的文本"像标题"）。

## 算法：`normalizePdfFontFamilyForCompare`

1. `raw` 为空 → `""`。
2. trim；空 → `""`。
3. PDF 子集嵌入前缀：若 `len(s)>7 && s[6]=='+'`，且前 6 个字符匹配 `[A-Za-z0-9]{6}`，去掉前 7 个字符（前缀+`+`）。
4. 转小写。
5. 若含逗号，且逗号之后的尾部恰为 `,bold`/`,italic`/`,bolditalic`（大小写不敏感，已经小写化），截断到逗号之前。
6. 特例：若整串匹配正则 `^fzxbs[a-z0-9]{1,3}$`（方正小标宋家族的内部变体后缀，如 FZXBSK/FZXBSJ/FZXBSJW），统一归一为 `"fzxbs"`。
7. 返回处理后的字符串。

## 算法：`sameFontFamily`

两侧分别 `normalizePdfFontFamilyForCompare`；任一侧为空字符串 → true（宽松：无法判断时不算不同）；否则字符串相等判断。

## 算法：`isCenteredOnPage` / `isCenteredStructuralChapterHeading`

- `isCenteredOnPage`：`block==nil || block.PageWidth<=0` → false；`pageCenter=PageWidth/2`；`blockCenter=headingCenter(block)`；`|blockCenter-pageCenter| <= PageWidth*0.15` → true。
- `isCenteredStructuralChapterHeading`：`block==nil || !isCenteredOnPage(block)` → false；`t=strip(block.Text)`；`ChapterTocLineRemover.IsChapterPrefixOnlyLine(t) || ChapterTocLineRemover.IsStructuralChapterHeading(t)`。

## 算法：`isHeading`（核心标题判定，多层否决式）

签名（Go 化，单一函数，其余为默认参数的便捷重载，建议 Go 用可选参数结构体或多个显式重载函数模拟）：

```
isHeading(block *TextBlock, bodyFontMode float64, config Config,
    inListGuideScope bool, prevBlock *TextBlock,
    headingStyleProfile HeadingStyleProfile,
    shortPhraseListRunBlockIds map[string]struct{},
    headingSequenceDemoteBlockIds map[string]struct{}) bool
```

步骤（严格保持顺序，任一否决命中立即返回 false；候选判定通过后再走"允许提升"路径）：

1. 若 `block.ID` 在 `headingSequenceDemoteBlockIds` 中 → **false**（标题序列一致性扫描已判定该块要整体降级为正文；这是最高优先级的外部否决，先于本函数所有内部规则）。
2. `s := normalizeText(block.Text)`（注意：这里调用的是**无 config 参数**的重载，等价于 `normalizeText(text, Config.defaults())`——移植时需要确认这不是笔误：与函数其余部分使用 `config` 不一致，若原意如此需原样保留，若怀疑是 bug 应向用户确认，不要擅自"修正"）。`len := len(s)`（Java `String.length()` 按 UTF-16 code unit 计，Go 需用 `utf16.RuneLen` 或等价方式统计，以保持长度阈值判断行为一致；见下方「与其他 Part 的接口」的编码注意事项）。
3. `MarkdownStructureRules.EndsWithTerminalPunctuation(s)` → false（如"第三条交通工具的选择："以冒号收尾，不当层级标题）。
4. `HeadingSuppressHeuristics.ShouldSuppressHeading(block, prevBlock, bodyFontMode, config, inListGuideScope)` → false。
5. `MarkdownStructureRules.IsOrderedListItemLine(s)` → false（列表项优先于标题）。
6. `MarkdownStructureRules.IsChapterTableOfContentsEntry(s)` → false（目录项：「第 X 章 + 题名 + 页码」）。
7. `TITLE_CN_PAREN` 正则匹配 `s` 且 `looksLikeCnParenBodyEnumeration(s, config)` → false（如"（一）人力部门："类列举，非标题）。
8. `block.HeadingPrefixStyleMismatch && !hasChapterTitleNameAfterPrefix(s) && !HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(s)` → false（前缀编号与正文样式不一致，且不是"第X章+短题名"或独立数字层级标题的例外情形）。
9. `looksLikeCnArticleBodyParagraphLead(s)` → false（"第 X 条"后接说明性正文）。
10. `isStandaloneCnArticleHeading(block, config)` → **true（提前返回，跳过后续所有判定）**（独立成行的"第 X 条 + 短题名"，即使字号与正文相同也标为标题）。
11. `regexMatch := isHeadingByRegex(s)`。
12. 若 `regexMatch && !HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(s) && looksLikeRegexMatchedBodyParagraph(s, config)` → false（正则命中但整段像说明性正文）。
13. 若 `regexMatch && hasMiddleChinesePeriod(s)` → false（标题中间出现中文句号，通常是正文被硬断行）。
14. 候选判定 `candidate`：
    - 若 `regexMatch` → `candidate=true`。
    - 否则若 `len<=config.MaxHeadingLength*3 && isLikelyHeadingText(s) && block.FontSizeMean > bodyFontMode+max(config.FontSizeDeltaPt,3.0)` → `candidate=true`。
    - 否则若 `len>config.MaxHeadingLength` → `candidate=false`。
    - 否则 `candidate = block.FontSizeMean > bodyFontMode+config.FontSizeDeltaPt`。
15. `!candidate` → false。
16. `isReliableStyleClusterHeading(block, headingStyleProfile, config)` → **true**（可靠 H1/H2 样式簇直接放行，跳过短语清单压制与旧兜底门控）。
17. `shouldSuppressByShortPhraseListRun(block, s, headingStyleProfile, config, shortPhraseListRunBlockIds)` → false（BODY/NOISE/UNKNOWN 簇上的短语式连续编号清单压制）。
18. 返回 `passesHeadingStyleProfileGate(block, s, headingStyleProfile, config)`（旧 pt/字重逻辑或样式簇门控的最终裁决）。

## 算法：`passesHeadingStyleProfileGate`

1. `!config.StyleClusterHeadingEnabled` → true（功能关闭，全部放行）。
2. `profile==nil || profile.ShouldFallbackToLegacyHeuristics()` → true（剖面不可靠，不做二次门控）。
3. `role := profile.RoleOf(block)`。
4. `role==H1 || role==H2` → true。
5. `role==Body || role==Noise` → 返回 `hasStrongHeadingSemantics(block, normalizedText)`。
6. `role==Unknown && fontMatchesBodyCluster(block, profile, config)` → 返回 `hasStrongHeadingSemantics(...)`。
7. 其余情况（`Unknown` 且字号不像正文簇）→ true。

## 算法：`fontMatchesBodyCluster`

`body := profile.BodyCluster()`；`bodyFont := body!=nil ? body.MeanFontSize : block.BodyFontMode`；返回 `HeadingSuppressHeuristics.FontMatchesBody(block, bodyFont, config)`（外部函数，语义："字号/字重与正文相同"）。

## 算法：`isBodyOrNoiseLikeForShortPhraseListRun`

（供外部 `ShortPhraseListRunHeuristics` 使用，判定"该块的样式是否算正文/噪声类"）

1. `block==nil || profile==nil || !profile.IsReliable()` → true（不可靠剖面下一律视为正文类，不排除）。
2. `role := profile.RoleOf(block)`。
3. `role==H1 || role==H2` → false。
4. `role==Body || role==Noise` → true。
5. 否则（`Unknown`）→ `fontMatchesBodyCluster(block, profile, config)`。

## 算法：`hasStrongHeadingSemantics`

判定"强章节/层级编号语义"，允许正文样式簇上仍输出为标题：

1. `text` 为空 → false；`t := trim(text)`。
2. `TITLE_CHAPTER` 匹配 `t` → 返回 `!ChapterTocLineRemover.IsChapterTocLine(t)`（排除目录项）。
3. `isCenteredStructuralChapterHeading(block)` → true。
4. `ChapterTocLineRemover.IsStructuralCnSectionHeading(t)` → true。
5. `TITLE_NUM_MULTI` 匹配（如 `1.2.3`）→ true。
6. `TITLE_CN_NUM` 匹配（如 `一、`）且 `HeadingSuppressHeuristics.IsStandaloneHeadingLine(t)` → true。
7. 否则 false。

## 算法：`hasChapterTitleNameAfterPrefix`

判定"第一章"紧跟题名（如"投标邀请"）同块的情况，即使前后样式不一致仍算标题：

1. `s` 为空 → false；`t := trim(s)`。
2. `!TITLE_CHAPTER.Matches(t) || ChapterTocLineRemover.IsChapterTocLine(t)` → false。
3. `rest := 去掉开头 "第<中文数字或阿拉伯数字>章" 前缀后 trim`（正则 `^第\s*[一二三四五六七八九十百千万零\d]+\s*章\s*`）。
4. 返回 `ChapterTocLineRemover.IsLikelyChapterTitleNameLine(rest)`。

## 算法：`looksLikeRegexMatchedBodyParagraph`

判定正则命中的行是否实为长段说明性正文：

1. `s` 为空 → false；`t := trim(s)`。
2. `maxLen := max(8, config.MaxHeadingLength)`。
3. `punctScope := textAfterNumericHierarchyPrefix(t)`（跳过 `1.1.1` 类数字层级前缀，避免其中的 ASCII `.` 被计入句读）。
4. `sentenceEnds := count(SENTENCE_BOUNDARY_PUNCT, punctScope)`（`[。.!！?？]`）。
5. `clauses := count(CLAUSE_DENSE_PUNCT, punctScope)`（`[，、；：,:;]`）。
6. `sentenceEnds>=2` → true。
7. `len(t)>maxLen && sentenceEnds>=1` → true。
8. `len(t)>maxLen && clauses>=4` → true（应对句末标点被 PDF 文本层丢失/误读的情况，退化用逗顿密度判断）。
9. `len(t)>maxLen*2` → true。
10. 返回 `len(t)>maxLen && clauses>=6`。

## 算法：`textAfterNumericHierarchyPrefix`

正则 `^(\d+(?:\.\d+)*\.?)\s*` 匹配 `t` 开头，命中则返回匹配结束位置之后的子串；否则原样返回 `t`。

## 算法：`detectHeadingPrefixStyleMismatch`

在 `buildLineBlock`（Part 1）构建 `TextBlock` 时调用，检测"编号前缀"与"正文"字体/字重/字号是否不一致：

1. `rawText` 空或 `tokens` 为空 → false。
2. `HEADING_PREFIX_ONLY` 正则（覆盖"（一）"/"一、"/"第X章"/"1.1."/"（1）"/罗马数字"I."/字母"A."等多种前缀形式，`find()` 非 `matches()`，即找子串前缀匹配）在 `rawText` 开头找到匹配；`prefixEnd := m.End()`；`prefixEnd<=0 || prefixEnd>=len(rawText)` → false。
3. `rest := trim(rawText[prefixEnd:])`；空 → false。
4. `prefixSig := dominantStyle(tokens, 0, prefixEnd)`；`restSig := dominantStyle(tokens, prefixEnd, len(rawText))`；任一为 nil → false。
5. `familyDiff := normalizePdfFontFamilyForCompare(prefixSig.FontFamily) != normalizePdfFontFamilyForCompare(restSig.FontFamily)`。
6. `weightDiff := (prefixSig.FontWeight>=600) != (restSig.FontWeight>=600)`。
7. `sizeDiff := |prefixSig.FontSize - restSig.FontSize| >= 0.8`。
8. `diffCount := 命中数(familyDiff)+命中数(weightDiff)+命中数(sizeDiff)`；返回 `diffCount>=1`。

## 算法：`dominantStyle`

在字符区间 `[start,end)` 内，按各 `styleToken` 与该区间的重叠字符数累加得分，找出得分最高的"样式签名"（`fontFamilyNorm|b/n|fontSize取1位小数`作为聚合 key），返回对应的原始 `styleSignature`（family/weight/size 未归一化的原值）。`tokens` 为空或 `start>=end` → nil。无重叠 → nil。

## 算法：`hasMiddleChinesePeriod`

`s` 为空 → false；找第一个中文句号 `。` 的位置 `idx`；`idx<0` → false；返回 `idx < len(t)-1`（句号不是最后一个字符，即句号后还有内容）。

## 算法：`looksLikeCnParenBodyEnumeration`

`text` 为空 → false；`t := trim(text)`；若 `MarkdownHeadingStage.LooksLikeCnParenBodyEnumerationText(t)`（外部方法）→ true；若 `t` 不匹配 `TITLE_CN_PAREN` → false；否则返回 `looksLikeRegexMatchedBodyParagraph(t, config)`。

## 算法：`isStandaloneCnArticleHeading`（两个入口）

- `(block, config)` 版本：`block==nil || block.Text 空` → false；`block.HeadingPrefixStyleMismatch` → false（前缀样式不一致时不算独立标题）；委托文本版本。
- `(text, config)` 文本版本 `isStandaloneCnArticleHeadingText`：
  1. `text` 空 → false；`t := trim(text)`。
  2. `!startsWithCnArticleHeading(t)` → false。
  3. `looksLikeCnArticleBodyParagraphLead(t)` → false。
  4. `looksLikeRegexMatchedBodyParagraph(t, config)` → false。
  5. `hasMiddleChinesePeriod(t)` → false。
  6. 返回 `HeadingSuppressHeuristics.IsStandaloneCnArticleLine(t)`（外部判定）。

## 算法：`isHeadingByRegex`

`t` 空 → false；否则任一以下正则完整匹配（`Matches`，非 `Find`）即为 true：`TITLE_CN_NUM`、`TITLE_CN_PAREN`、`TITLE_CHAPTER`、`TITLE_NUM_SIMPLE`、`TITLE_NUM_MULTI`、`TITLE_NUM_PAREN`、`TITLE_NUM_SUFFIX`、`TITLE_ROMAN`、`TITLE_ALPHA`（正则定义见文件头 L62-121，逐字复制到 Go 时注意 Go `regexp`（RE2）不支持零宽断言 `(?!...)`/`(?<=...)`——`TITLE_NUM_SIMPLE`/`TITLE_NUM_MULTI` 用了否定前瞻 `(?!\d)`，`HEADING_PREFIX_ONLY` 等多处用了环视断言，**这是 Part 1/Part 2 移植到 Go 的一个通用障碍**，需要用非 RE2 的正则库（如 `github.com/dlclark/regexp2`）或手写等价逻辑替代，本文档只标注哪些正则含此类断言，具体替代方案留给实现阶段统一决策）。

## 算法：`isOfficialDocumentTitleTail` / `isAddresseeSalutationLine`

纯正则匹配包装：
- `OFFICIAL_DOCUMENT_TITLE_TAIL = ".*的(?:通知|决定|决议|公告|通告|议案|报告|请示|批复|意见|函|纪要|命令|条例|规定|办法)$"`
- `ADDRESSEE_SALUTATION_LINE = "^[\\p{IsIdeographic}、，,\\s]{1,40}[：:]$"`（`\p{IsIdeographic}` 在 Go 中需用 `unicode.Ideographic` 配合自定义匹配，或用 CJK 统一表意文字 Unicode 范围正则近似）

## 算法：`resolveHeadingLevel`

```
resolveHeadingLevel(block, profile, config) -> int:
  if block == nil: return 2
  if config.StyleClusterHeadingEnabled && profile.IsReliable():
      if level, ok := profile.SuggestedHeadingLevel(block); ok:
          return normalizeCnStructuralHeadingLevel(block.Text, level)
  return normalizeCnStructuralHeadingLevel(block.Text, legacyHeadingLevel(block))
```

（无 `config` 参数的重载等价于传入 `Config.defaults()`。）

## 算法：`normalizeCnStructuralHeadingLevel`

`text` 空 → 原样返回 `level`；`t := trim(text)`；`!TITLE_CHAPTER.Matches(t)` → 原样返回 `level`；`t` 含"章" → 强制返回 1；`t` 含"条" → 返回 `max(2, level)`；否则原样返回 `level`。

## 算法：`legacyHeadingLevel`

`text := trim(block.Text)`（空则 `""`）；若 `hasStrongHeadingSemantics(block, text)`：`TITLE_CHAPTER.Matches(text) && text含"章"` → 返回 1；否则返回 2。否则返回 `block.FontSizeMean >= block.BodyFontMode+3.0 ? 1 : 2`。

## 算法：`isReliableStyleClusterHeading`

`block==nil || config==nil || !config.StyleClusterHeadingEnabled` → false；`profile==nil || !profile.IsReliable()` → false；`role := profile.RoleOf(block)`；返回 `role==H1 || role==H2`。

## 算法：`shouldSuppressByShortPhraseListRun`

`shortPhraseListRunBlockIds` 为空或不含 `block.ID` → false（未被识别为短语清单，无需压制）。`hasStrongHeadingSemantics(block, normalizedText)` → false（强语义豁免）。`isReliableStyleClusterHeading(block, profile, config)` → false（可靠 H1/H2 豁免）。否则 → true（压制为非标题）。

## 算法：`isStyleClusterProtectedFromShortPhraseListRun`

直接等价于 `isReliableStyleClusterHeading(block, profile, config)`。

## 算法：`resolveHeadingLevelSourceTraceLabel` / `resolveStyleClusterTraceLabel` / `resolveSequenceRunTraceLabel`

这三个是 TRACE 注释文本生成的纯字符串标签函数，只在 `config.EmitTraceComments || config.EmitHeadingTrace` 为 true 时被调用，不影响实际渲染结果，可按原逻辑逐字翻译：

- `resolveHeadingLevelSourceTraceLabel`：`!enabled || profile不可靠` → `"FALLBACK_FONT_DELTA"`；否则若 `profile.SuggestedHeadingLevel(block)` 命中 → `"STYLE_CLUSTER"`，否则 `"FALLBACK_FONT_DELTA"`。
- `resolveStyleClusterTraceLabel`：`!enabled` → `"DISABLED"`；`profile==nil` → `"UNKNOWN"`；否则 `profile.RoleOf(block)` 的字符串名。
- `resolveSequenceRunTraceLabel`：`skipped` → `"SHORT_PHRASE_LIST"`；若命中 `shortPhraseListRunBlockIds`：`isStyleClusterProtectedFromShortPhraseListRun` → `"STYLE_CLUSTER_PROTECTED"`；`hasStrongHeadingSemantics` → `"STRUCTURAL_HEADING"`；否则 `"SHORT_PHRASE_LIST"`。未命中但 `ShortPhraseListRunHeuristics.ParseNumberedLine(normalizedText)!=nil` → `"STRUCTURAL_HEADING"`；否则 `"NONE"`。

## 算法：`recognizesBlockAsHeadingForSequenceConsistency`

供标题序列一致性降级扫描（外部类 `HeadingSequenceConsistencyHeuristics`）判断某行"样式上像标题"：

1. `block==nil` → false。
2. `s := normalizeText(block.Text, cfg)`；`HeadingSequenceConsistencyHeuristics.ParseSequenceLine(s)==nil` → false（不是可识别的编号序列行）。
3. 若 `prof.IsReliable()`：`role := prof.RoleOf(block)`；`role==H1||role==H2` → true。
4. 否则回退：`block.FontWeight>=600 && block.FontSizeMean > block.BodyFontMode+0.5`。

（此函数产出的布尔谓词作为回调传给外部扫描函数，本身不遍历/不维护状态。）

---

## 算法：`mergeLines`

```
mergeLines(lines []*TextBlock, bodyFontMode float64, config Config) []*TextBlock:
  if empty(lines): return lines
  pageRightEdge := estimatePageRightEdge(lines)   // Part 1 函数，本 Part 消费
  out := []
  i := 0
  while i < len(lines):
      current := lines[i]
      lastLine := current
      while i+1 < len(lines):
          next := lines[i+1]
          if !shouldMerge(current, lastLine, next, bodyFontMode, config, pageRightEdge): break
          current = merge(current, next, config)
          lastLine = next
          i++
      out.append(current)
      i++
  return out
```

关键点：`current` 是**链式累积后的块**（文本已拼接），而 `lastLine` 始终是**参与合并的最后一条原始行**（未拼接前的单行）——`shouldMerge` 的几何判定要用 `lastLine` 而非 `current`，否则第 3 行起的续行会因为 `current` 的 `topDistance`/`left` 仍是首行数据而被误判为布局断裂。

## 算法：`mergeWrappedHeadingLines`

```
mergeWrappedHeadingLines(lines, bodyFontMode, config) []*TextBlock:
  if empty(lines): return []
  if !config.MergeWrappedHeadings: return lines
  out := []
  i := 0
  while i < len(lines):
      current := lines[i]
      while i+1 < len(lines):
          next := lines[i+1]
          if !canMergeHeadingPair(current, next, bodyFontMode, config): break
          current = mergeHeadingBlocks(current, next, config)
          i++
      out.append(current)
      i++
```

注意这里**没有** `lastLine` 参数——`canMergeHeadingPair` 内部自己用 `headingLastTopRef(a)`/`headingCenterForMergePair(a,config)` 从累积块 `a` 中提取"最后一行"的等效信息（通过 `headingTrailingLeft`/`headingTrailingText`/`headingLastLineTop` 字段），机制与 `mergeLines` 的显式 `lastLine` 参数不同但目的相同。

消费 Part 1 输出字段：`TextBlock.PageNo/MonoFont/TableID/Text/FontSizeMean/FontWeight/Left/IndentLeft/LineHeight/TopDistance/BBox/PageWidth/HeadingPrefixStyleMismatch/HeadingLastLineTop/HeadingTrailingLeft/HeadingTrailingText`。

## 算法：`merge`（普通行合并）与 `mergeHeadingBlocks`（标题合并）

两者结构几乎一致，构造新 `TextBlock`：

共同点：
- `mergedText := mergeText(a.Text, b.Text)`
- `lastTop := max(headingLastTopRef(a), b.TopDistance)`
- `trailNorm := normalizeText(b.Text, config)`
- `id/pageNo/bbox` 取 `a` 的（不变）
- `topDistance := min(a.TopDistance, b.TopDistance)`
- `left := min(a.Left, b.Left)`
- `fontFamily := a.FontFamily`（始终取 a 的，不比较/合并 b 的）
- `fontWeight := max(a.FontWeight, b.FontWeight)`
- `italic := a.Italic || b.Italic`
- `monoFont := a.MonoFont || b.MonoFont`
- `lineHeight := max(a.LineHeight, b.LineHeight)`
- `indentLeft := min(a.IndentLeft, b.IndentLeft)`
- `tableId := a.TableID`
- `bodyFontMode := a.BodyFontMode`
- `headingLastLineTop := lastTop`
- `headingTrailingLeft := b.Left`
- `headingTrailingText := trailNorm`
- `pageWidth := a.PageWidth`
- `headingPrefixStyleMismatch := a.HeadingPrefixStyleMismatch || b.HeadingPrefixStyleMismatch`

唯一差异：**字号（`fontSizeMean`）的合并策略**——
- `merge`（普通行）：`(a.FontSizeMean + b.FontSizeMean) / 2.0`（取均值，因为普通段内换行两行字号本应接近，均值更稳健）。
- `mergeHeadingBlocks`：`max(a.FontSizeMean, b.FontSizeMean)`（取最大值，标题跨行时哪怕次行字号因识别误差偏小，也不应拖累整体标题字号判定）。
另外 `mergeHeadingBlocks` 内 `mergedText` 额外套了一层 `normalizeText(..., config)`（`merge` 没有对 `mergedText` 本身再 normalize，只在赋值给结果 TextBlock 时调用 `normalizeText(mergedText, config)`——两者最终效果一致，均为"合并文本后再规范化一次"，实现时统一即可）。

## 算法：`mergeText`

```
mergeText(a, b string) string:
  left := a ?? ""
  right := b ?? ""
  if shouldDropDuplicatedBoundary(left, right):
      right = right[1:]   // 按 rune 而非 byte 截断（Java String.substring(1) 是按 UTF-16 code unit，中文场景等价于去掉第一个字符）
  if needSpace(left, right):
      return left + " " + right
  return left + right
```

## 算法：`shouldMerge`（rejection-first 规则集，普通行合并核心）

两个重载：`shouldMerge(a,b,bodyFontMode,config)` 等价于 `shouldMerge(a,a,b,bodyFontMode,config,NaN)`（`lastLine=a` 自身，无已知 `pageRightEdge`）。完整版签名：

```
shouldMerge(a, lastLine, b *TextBlock, bodyFontMode float64, config Config, pageRightEdge float64) bool
```

步骤（严格顺序，命中任一"不合并"分支立即 `return false`；命中"合并"分支立即 `return true`；两者都未命中才继续下一步）：

1. `na := a.WithText(normalizeText(a.Text, config))`；`nb := b.WithText(normalizeText(b.Text, config))`。
2. `na.Text 空 || nb.Text 空` → false。
3. `a.PageNo != b.PageNo` → false。
4. `chapterTitleContinuation := isChapterPrefixWithTitleNamePair(na.Text, nb.Text)`。
5. `isCenteredStructuralChapterHeading(na) && !chapterTitleContinuation` → false。
6. `!chapterTitleContinuation && shouldBlockMergeAtChapterHeadingBoundary(na, nb, config)` → false。
7. `isOfficialDocumentTitleTail(na.Text) && isAddresseeSalutationLine(nb.Text)` → false（公文标题末行后接主送机关称谓行，不合并；见函数注释中"仅在上一行本身是已完整收束的公文标题时才拦截"的边界说明）。
8. `aList := isListItem(na.Text)`；`bList := isListItem(nb.Text)`；`listToContinuation := aList && !bList`。
9. `ChapterReferenceHeuristics.IsNumberedClauseContinuation(na.Text, nb.Text)` → **true**。
10. `!endsWithSentenceTerminator(na.Text) && ChapterReferenceHeuristics.IsBodyChapterReference(nb.Text)` → **true**。
11. `isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)` → **true**。
12. `!listToContinuation && isHeading(na, bodyFontMode, config, false, a) && !isFullWidthHardWrapLeadLine(na, nb, config, pageRightEdge)` → false（na 本身是标题，除非被识别为铺满行宽的硬断行首行例外）。
13. `isHeading(nb, bodyFontMode, config, false, na) && !ChapterReferenceHeuristics.IsBodyChapterReference(nb.Text)` → false（nb 是标题，除非 nb 实为正文中的章节引用而非真标题）。
14. `listToContinuation && startsWithCnArticleHeading(nb.Text)` → false（列表项后接独立"第 X 条"标题行）。
15. `(aList && bList) || (!aList && bList)` → false（列表边界策略：list→list 不合并；normal→list 不合并；只有 list→normal 允许继续判断）。
16. `!listToContinuation && endsWithSentenceTerminator(na.Text)` → false。
17. `!chapterTitleContinuation && !na.HeadingPrefixStyleMismatch && hasTypographicBoundary(na, nb, config)` → false（排版边界：字号/字重差异过大；`na.HeadingPrefixStyleMismatch` 为真时**跳过**此项否决，因为 `na` 整行字重被编号前缀污染，不可信）。
18. `isInlineTailContinuation(na, nb)` → **true**。
19. 计算段落延续标志：`paragraphContinuation := !endsWithSentenceTerminator(na.Text) && !isHeading(nb, bodyFontMode, config, false, na) && !isListItem(nb.Text)`。
20. `spacingMultiplier := paragraphContinuation ? max(config.LineSpacingMultiplier, 2.8) : config.LineSpacingMultiplier`。
21. `layoutBreak(lastLine ?? na, nb, config, spacingMultiplier, paragraphContinuation, pageRightEdge)` → false。
22. `isAcrossTable(na, nb)` → false。
23. `shouldBlockLeftAlignedDateLineMerge(na, nb, config)` → false。
24. `HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(nb.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)` → false。
25. `HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(na.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)` → false。
26. `isShortLabel(na.Text) && !endsWithSentenceTerminator(na.Text) && !isNumericHierarchyHardWrapContinuation(na.Text, nb.Text)` → false。
27. `endsWithSemanticBreak(na.Text)` → false（如以"如下"/"包括"结尾）。
28. `isForcedContinuation(na.Text, nb.Text)` → **true**。
29. 兜底 → **true**（未命中任何否决规则，默认合并）。

## 算法：`shouldBlockMergeAtChapterHeadingBoundary`

`a==nil||b==nil||config==nil` → false；`left := normalizeText(a.Text, config)`；`right := normalizeText(b.Text, config)`；`left` 空 → false；`ChapterTocLineRemover.IsChapterTocLine(left)` → false（目录项本身不设边界）；`isChapterPrefixWithTitleNamePair(left, right)` → false（章节前缀+题名配对例外）；返回 `ChapterTocLineRemover.IsChapterPrefixOnlyLine(left) || ChapterTocLineRemover.IsStructuralChapterHeading(left)`。

## 算法：`hasTypographicBoundary`

直接委托 `styleDifferent(a, b, config)`（同一逻辑，两个函数名并存，Go 移植可以只留一个）。

## 算法：`isChapterPrefixWithTitleNamePair`

判定"第一章"+下一行"招标公告"应合并为一条章标题：

1. `left/right` 任一为空/全空白 → false。
2. `ChapterTocLineRemover.IsChapterTocLine(left) || IsChapterTocLine(right)` → false。
3. `!ChapterTocLineRemover.IsLikelyChapterTitleNameLine(right)` → false。
4. `ChapterTocLineRemover.IsChapterPrefixOnlyLine(left)` → **true**（纯前缀"第一章"无题名）。
5. `!ChapterTocLineRemover.IsStructuralChapterHeading(left)` → false。
6. `afterChapter := 去掉 "第<数字>章" 前缀后 trim`（同 `hasChapterTitleNameAfterPrefix` 用的正则）；返回 `afterChapter.isEmpty()`（即 `left` 本身就是纯章节前缀，没有夹带题名，与题名行分离）。

## 算法：`canMergeHeadingPair`（标题跨行合并独立判定，rejection-first + trace）

```
canMergeHeadingPair(a, b *TextBlock, bodyFontMode float64, config Config) bool
```

步骤：
1. `a==nil||b==nil` → false；`!config.MergeWrappedHeadings` → false。
2. `allow := true; reason := "ok"`（用于 trace，不影响返回值本身，Go 实现若不做 trace 可省略）。
3. `a.PageNo != b.PageNo` → `allow=false, reason="crossPage"`。
4. 否则 `a.MonoFont || b.MonoFont` → `allow=false, reason="monoFont"`。
5. 否则 `a.TableID != b.TableID` → `allow=false, reason="crossTable"`。
6. 否则 `isListItem(a.Text) || isListItem(b.Text)` → `allow=false, reason="listItem"`。
7. `left := normalizeText(a.Text, config)`; `right := normalizeText(b.Text, config)`。
8. `allow && (left空 || right空)` → `allow=false, reason="emptyText"`。
9. 否则 `allow && isHeadingByRegex(right)` → `allow=false, reason="nextIsStandaloneHeading"`（下一行本身独立成标题，不并入）。
10. 否则 `allow && endsWithSentenceTerminator(left) && !isLikelyHeadingText(right)` → `allow=false, reason="leftEndsSentence"`。
11. **特殊短路**：`allow && isChapterPrefixWithTitleNamePair(left, right)` → 记录 trace 后**直接返回 true**（跳过后续所有几何/样式判定）。
12. `allow && isCenteredStructuralChapterHeading(a) && !isChapterPrefixWithTitleNamePair(left, right)` → `allow=false, reason="centeredChapterHeading"`。
13. 计算几何/样式差异量：
    - `fontDelta := |a.FontSizeMean - b.FontSizeMean|`
    - `spacing := |b.TopDistance - headingLastTopRef(a)|`
    - `maxLine := max(a.LineHeight, b.LineHeight, 1.0)`
    - `leftDelta := |a.Left - b.Left|`（仅用于 trace，不参与判定）
    - `centerDelta := |headingCenterForMergePair(a, config) - headingCenter(b)|`
    - `aCandidate := isHeadingCandidate(a, bodyFontMode, config)`
    - `bCandidate := isHeadingCandidate(b, bodyFontMode, config)`
14. `allow && fontDelta > config.HeadingMergeFontDeltaPt(1.2)` → `allow=false, reason="fontDelta"`。
15. 否则 `allow && spacing > maxLine*config.HeadingMergeMaxGapMultiplier(2.2)` → `allow=false, reason="spacing"`。
16. 否则 `allow && centerDelta > config.HeadingMergeCenterTolerancePt(24.0)` → `allow=false, reason="centerDelta"`。
17. 否则 `allow && !sameFontFamily(a.FontFamily, b.FontFamily)` → `allow=false, reason="fontFamily"`。
18. 否则 `allow && (!aCandidate || !bCandidate)`：若 `!isChapterPrefixWithTitleNamePair(left,right)` → `allow=false, reason="notHeadingCandidate"`（否则保持 allow=true，即章节前缀配对例外覆盖此项否决）。
19. （trace 记录）返回 `allow`。

## 算法：`traceHeadingMergeDecision` / `previewForTrace`

纯日志输出，仅在 `config.EmitHeadingTrace` 为 true 时执行；`previewForTrace` 把换行替换为空格、截断到 40 字符并加 `...`。移植时可用 `log`/`slog` 或按需省略——不影响任何判定结果，只影响可观测性。

## 算法：`headingLastTopRef`

`t==nil` → 0.0；`isNaN(t.HeadingLastLineTop)` → 返回 `t.TopDistance`；否则返回 `t.HeadingLastLineTop`。

## 算法：`headingCenterForMergePair`

1. `block==nil` → 0.0。
2. `block.BBox != nil` → 返回 `headingCenter(block)`（有真实几何信息时优先用它）。
3. `block.HeadingTrailingText` 非空：`t := trim(normalizeText(block.HeadingTrailingText, config))`；非空则：
   - `trailLeft := isNaN(block.HeadingTrailingLeft) ? block.Left : block.HeadingTrailingLeft`
   - 返回 `headingCenterFromMetrics(t, trailLeft, block.FontSizeMean)`
4. 否则返回 `headingCenter(block)`。

（目的：链式合并后的长标题不应该用累积后的完整文本宽度算中心，而应该用**最后一行**的文本/位置来估算，避免长标题"膨胀"宽度破坏 `centerDelta` 判定。）

## 算法：`headingCenterFromMetrics`

用于没有真实 bbox 时，凭字符宽度经验值估算文本视觉宽度的中心点：

1. `text` 空 → 返回 `left`。
2. 遍历字符：跳过空白；`isChinese(ch)` 计入 `cjkCount`，否则计入 `otherCount`。
3. `cjkWidth := fontSizeMean`（假设 CJK 字符视觉宽度 ≈ 字号，即等宽方块字）；`otherWidth := fontSizeMean*0.55`（拉丁字符视觉宽度经验系数）。
4. `widthGuess := cjkCount*cjkWidth + otherCount*otherWidth`；若 `widthGuess<=0` → `max(fontSizeMean*2.0, 1.0)`。
5. 返回 `left + widthGuess/2.0`。

## 算法：`headingCenter`

`block==nil` → 0.0；`block.BBox != nil` → 返回 `(BBox.LLX + BBox.URX)/2.0`（真实几何中心）；否则 `text := trim(block.Text)`；空 → 返回 `block.Left`；否则委托 `headingCenterFromMetrics(text, block.Left, block.FontSizeMean)`。

## 算法：`isForcedContinuation`

`startsWithContinuationPunctuation(b) || shouldDropDuplicatedBoundary(a, b)`。

## 算法：`isInlineTailContinuation`

判定形如 "AI+"/"2025" 这类被硬断行拆到下一行的 ASCII/数字尾巴：

1. `compact := 去除 b.Text 中所有空白字符`。
2. `!INLINE_TAIL_TOKEN.Matches(compact)` → false（正则：`^[A-Za-z0-9+\-_/().]{2,16}$`，2~16 个字符，仅含字母/数字/`+-_/().`）。
3. `spacing := |b.TopDistance - a.TopDistance|`；`maxLine := max(a.LineHeight, b.LineHeight)`。
4. 返回 `spacing <= maxLine*2.5`。

## 算法：`startsWithContinuationPunctuation`

`first := 第一个非空白字符`；`first!=0 && CONTINUATION_PUNCTUATION 包含 first`。

## 算法：`shouldDropDuplicatedBoundary`

判定 PDF 硬换行时重复了边界字符（如"之一"+"一，在"应去重为"之一，在"）：

1. `left := a 的最后一个非空白字符`；`first := b 的第一个非空白字符`；`second := b 的第二个非空白字符`。
2. 任一为 0（不存在）→ false。
3. 返回 `left==first && CONTINUATION_PUNCTUATION 包含 second`（即：b 的开头重复了 a 的结尾字符，且 b 的第二个字符是延续标点）。

## 算法：`styleDifferent`

```
styleDifferent(a, b, config) bool:
  if |a.FontSizeMean - b.FontSizeMean| > config.FontSizeDeltaPt: return true
  if a.FontWeight != b.FontWeight: return true
  return false   // 字体族名差异不算排版边界（OFD/PDF 常按 Unicode 范围拆分内嵌字体资源导致同一视觉字体报告不同 family name）
```

## 算法：`isFullWidthHardWrapLeadLine`

```
isFullWidthHardWrapLeadLine(a, b, config, pageRightEdge) bool:
  if a==nil || b==nil: return false
  if endsWithSentenceTerminator(a.Text) || endsWithSemanticBreak(a.Text): return false
  if isCenteredStructuralChapterHeading(a): return false
  if styleDifferent(a, b, config): return false
  return isFullWidthBodyLine(a, b, pageRightEdge)
```

## 算法：`layoutBreak`

```
layoutBreak(lastLine, b, config, spacingMultiplier, paragraphContinuation, pageRightEdge) bool:
  indentChange := |lastLine.IndentLeft - b.IndentLeft|
  spacing := |b.TopDistance - lastLine.TopDistance|
  maxLine := max(lastLine.LineHeight, 1.0)
  xOffset := |lastLine.Left - b.Left|
  indentTolerance := config.IndentThresholdPt
  xTolerance := config.XOffsetThresholdPt
  if paragraphContinuation && lastLine.BBox != nil && lastLine.PageWidth > 0:
      if isFullWidthBodyLine(lastLine, b, pageRightEdge):
          fontTolerance := PARAGRAPH_CONTINUATION_X_TOLERANCE_EM(3.6) * max(lastLine.FontSizeMean, b.FontSizeMean, 1.0)
          indentTolerance = max(indentTolerance, fontTolerance)
          xTolerance = max(xTolerance, fontTolerance)
      else:
          return true   // 末行未铺满却句子未完：判定为段落边界（如字段行"采购人：X"）
  return indentChange > indentTolerance || spacing > maxLine*spacingMultiplier || xOffset > xTolerance
```

## 算法：`isFullWidthBodyLine`

```
isFullWidthBodyLine(line, next, pageRightEdge) bool:
  if line==nil || line.BBox==nil || line.PageWidth<=0: return false
  tolerance := FULL_WIDTH_RIGHT_GAP_TOLERANCE_EM(2.5) * max(line.FontSizeMean, 1.0)
  if !isNaN(pageRightEdge) && pageRightEdge > 0:
      return line.BBox.URX >= pageRightEdge - tolerance
  rightGap := line.PageWidth - line.BBox.URX
  nextLeft := next==nil ? line.Left : next.Left
  leftMarginEstimate := max(min(line.Left, nextLeft), 0.0)
  return rightGap <= leftMarginEstimate + tolerance
```

消费 Part 1 输出的 `estimatePageRightEdge` 结果（本 Part 只读取，不重新实现）。

## 算法：`isAcrossTable`

`a.TableID<0 && b.TableID<0` → false（都不在表格内）；否则返回 `a.TableID != b.TableID`。

## 算法：`isShortLabel`

`text != nil && len(trim(text)) < 8`（按 rune 计数，非 byte）。

## 算法：`isNumericHierarchyHardWrapContinuation`

判定形如 `2.5.` + `1.开标` 或 `2.5.1.` + `开标` 这种被硬断行拆开的数字层级前缀续行：

1. `left/right` 任一为 nil → false；`a := trim(left)`；`b := trim(right)`；任一为空 → false。
2. `!a.matches(\d+(?:\.\d+)*\.$)` → false（`a` 必须是"纯数字层级前缀+末尾一个点"，如 `2.5.`）。
3. 若 `b[0]` 是数字：`glued := a+b`；返回 `HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(glued) || isHeadingByRegex(normalizeText(glued))`。
4. 否则若 `len(b)<=18 && !b.matches(".*[，、；：,.!?;:].*")`（不含逗顿分句标点）：返回 `HeadingSuppressHeuristics.IsStandaloneNumericHierarchyLine(a+b)`。
5. 否则 false。

## 算法：`isStandaloneChineseDateLine` / `shouldBlockLeftAlignedDateLineMerge`

- `isStandaloneChineseDateLine`：正则 `^\s*\d{4}年\d{1,2}月\d{1,2}日\s*$` 完整匹配 trim 后的文本。
- `shouldBlockLeftAlignedDateLineMerge`：
  1. `a==nil||b==nil||config==nil` → false。
  2. `!isStandaloneChineseDateLine(b.Text)` → false（b 不是纯日期行则不拦截）。
  3. `leftDelta := b.Left - a.Left`；`tol := config.XOffsetThresholdPt`。
  4. `leftDelta < -tol` → false（日期行明显偏左于机关名，不算落款场景）。
  5. 返回 `leftDelta <= tol*3.0`（允许日期行略右缩进，最多 3 倍容差）。

## 算法：`endsWithSentenceTerminator` / `endsWithSemanticBreak`

- `endsWithSentenceTerminator`：`text` 空/空白 → false；`c := 最后一个非空白字符`；`c` 在字符集 `"。.!！?？:：;；）)"` 中 → true。
- `endsWithSemanticBreak`：`t := trim(text)`（nil 视为空）；`t` 以 `"如下"`/`"如下："`/`"包括"`/`"如下所示"` 结尾 → true。

## 算法：`shouldForcePlainDueToParagraphContinuation`

供渲染层 `appendTextBodyAsMarkdown` 使用，抑制"分页导致字号被抽取偏大"的误判标题：

```
shouldForcePlainDueToParagraphContinuation(prevBlock, block) bool:
  if prevBlock==nil || block==nil: return false
  if endsWithSentenceTerminator(prevBlock.Text): return false  // 前一块已完整收束，不算续行
  if isHeadingByRegex(block.Text) || isListItem(block.Text): return false
  if !endsWithSentenceTerminator(block.Text): return false
  return true
```

（即：前块未完、当前块以句末标点结束、当前块不是列表/正则标题 → 强制按纯文本输出，跳过标题判定。）

---

## 算法：`appendTextAsMarkdown`（渲染入口，三个重载 + 主体逻辑）

三个重载均归约到四参数以上的主体（缺省参数用 `Set.of()`/`HeadingStyleProfile.empty()` 填充）。主体逻辑：

```
appendTextAsMarkdown(out *strings.Builder, block, prevBlock *TextBlock, config Config,
    listGuideScopeBlockIds, headingStyleProfile, shortPhraseListRunBlockIds, headingSequenceDemoteBlockIds):
  text := normalizeText(block.Text, config)
  if text == "": return
  prevText := prevBlock==nil ? "" : normalizeText(prevBlock.Text, config)

  if config.EmitTraceComments:
      out.WriteString("<!-- TRACE: page=" + block.PageNo + " source=" + block.ID + " -->\n")

  if block.MonoFont:
      out.WriteString("```\n" + text + "\n```\n\n")
      return

  text = collapseIntraBlockLineBreaks(text)
  prevText = collapseIntraBlockLineBreaks(prevText)

  textSegments := MarkdownStructureRules.SplitEmbeddedPipeTableLines(text)  // 外部函数：切出行内嵌入的 "| a | b |" 表格片段
  if len(textSegments) > 1:
      for segment in textSegments:
          if segment == "": continue
          if strings.HasPrefix(segment, "|"):
              out.WriteString(segment + "\n")
          else:
              appendTextBodyAsMarkdown(out, segment, block, prevBlock, prevText, config, ...)
      out.WriteString("\n")
      return

  appendTextBodyAsMarkdown(out, text, block, prevBlock, prevText, config, ...)
```

消费 Part 1 输出字段：`block.Text/MonoFont/PageNo/ID`。依赖外部 `MarkdownStructureRules.SplitEmbeddedPipeTableLines`（未在本 Part 定义，视为其他 agent 移植范围，行为契约：把一段文本按"行内表格片段 vs 非表格片段"切分成有序列表，非表格片段仍需走标题/正文渲染判定，表格片段原样透传）。

## 算法：`appendTextBodyAsMarkdown`（单段文本渲染主体）

```
appendTextBodyAsMarkdown(out, text, block, prevBlock, prevText, config,
    listGuideScopeBlockIds, headingStyleProfile, shortPhraseListRunBlockIds, headingSequenceDemoteBlockIds):

  1. if shouldForcePlainDueToParagraphContinuation(prevBlock, block):
         out.WriteString(text + "\n\n")
         return

  2. if prevText != "" && isListItem(prevText) && !isListItem(text)
        && shouldMergeListWithPlainContinuation(prevText, text):
         appendListContinuationInline(out, prevText, text)
         return

  3. inListGuideScope := listGuideScopeBlockIds.Contains(block.ID)
     view := textBlockWithText(block, text)   // 用规范化后的 text 替换 block.Text 构造判定专用视图

  4. if isHeading(view, block.BodyFontMode, config, inListGuideScope, prevBlock,
                  headingStyleProfile, shortPhraseListRunBlockIds, headingSequenceDemoteBlockIds):
         if config.EmitTraceComments || config.EmitHeadingTrace:
             out.WriteString(buildHeadingTraceComment(block, text, config, headingStyleProfile,
                              shortPhraseListRunBlockIds, false) + "\n")
         ensureBlankLineBeforeBlock(out)
         level := resolveHeadingLevel(block, headingStyleProfile, config)
         out.WriteString(strings.Repeat("#", level) + " " + text + "\n\n")
         return

  5. if (config.EmitTraceComments || config.EmitHeadingTrace)
        && shouldEmitShortPhraseSkipTrace(block, text, config, headingStyleProfile, shortPhraseListRunBlockIds):
         out.WriteString(buildHeadingTraceComment(block, text, config, headingStyleProfile,
                          shortPhraseListRunBlockIds, true) + "\n")

  6. if isListItem(text):
         level := max(0, round(block.IndentLeft / 24.0))
         out.WriteString(strings.Repeat("  ", level) + text + "\n")
         return

  7. out.WriteString(text + "\n\n")
```

要点：
- 步骤 1/2 是"跳过标题判定的早退路径"，优先级高于标题判定本身。
- 步骤 3 的 `view` 只是把规范化文本临时塞回一个 `TextBlock` 副本，供 `isHeading` 读取 `.Text` 字段用，不修改原 `block`。
- 步骤 6 的列表缩进层级公式：`24.0` pt 每级，四舍五入（Go `math.Round`），每级两个空格。
- 步骤 4/5 的 TRACE 注释生成互斥（要么走标题分支的 trace，要么走"本应是标题但被短语清单压制"的 skip-trace），且都只在开关打开时执行，不影响非 trace 场景的输出内容。

消费 Part 1 输出字段：`block.ID/BodyFontMode/IndentLeft`。

## 算法：`ensureBlankLineBeforeBlock`

保证标题前有空行，避免列表项单换行输出后紧跟标题被后续 `fallbackMergeMarkdown` 误并入同一段落：

```
ensureBlankLineBeforeBlock(out):
  if out.Len() == 0: return
  s := out.String()
  if len(s)>=2 && s[len-1]=='\n' && s[len-2]=='\n': return          // 已有空行
  if len(s)>=1 && s[len-1]=='\n': out.WriteByte('\n'); return       // 只有单换行，补一个
  out.WriteString("\n\n")                                            // 完全没有换行，补两个
```

## 算法：`shouldEmitShortPhraseSkipTrace`

`block==nil||text==nil||shortPhraseListRunBlockIds==nil` → false；`s := trim(text)`；`!isHeadingByRegex(s)` → false（只有正则本该命中标题的行才需要报告"被压制"）；返回 `shouldSuppressByShortPhraseListRun(block, s, profile, config, shortPhraseListRunBlockIds)`。

## 算法：`buildHeadingTraceComment`

纯字符串拼装，格式（`skipped=false` 时）：

```
<!-- TRACE-HEADING: rule=<regex|fontSize> len=<N> maxHeadingLength=<N> fontSizeMean=<f>
 bodyFontMode=<f> fontWeight=<n> styleCluster=<LABEL> sequenceRun=<LABEL>
 headingLevelSource=<LABEL> level=<n> -->
```

`skipped=true` 时 tag 为 `TRACE-HEADING-SKIP`，且不追加 `level=` 字段，`headingLevelSource` 固定为 `"NONE"`。各字段来源：
- `rule`：`isHeadingByRegex(s) ? "regex" : "fontSize"`
- `styleCluster`：`resolveStyleClusterTraceLabel(...)`
- `sequenceRun`：`resolveSequenceRunTraceLabel(...)`
- `headingLevelSource`：`skipped ? "NONE" : resolveHeadingLevelSourceTraceLabel(...)`
- `level`：`skipped ? 0 : resolveHeadingLevel(...)`（仅在非 skipped 时输出）

（纯诊断信息，不影响渲染主体输出，除非 `config.EmitTraceComments/EmitHeadingTrace` 开启。）

## 算法：`textBlockWithText`

`block==nil || text==nil || text==block.Text` → 原样返回 `block`；否则构造一个除 `Text` 字段外其余全部字段照抄的新 `TextBlock`（等价于 Go 的 `block.WithText(text)`，与已有的 `TextBlock.withText` 方法完全同义——Java 代码里这是两个重复实现，移植到 Go 时应该**合并为一个方法**，只保留 `TextBlock.WithText`）。

## 算法：`collapseIntraBlockLineBreaks`

把一个 `TextBlock.Text` 内部可能残留的换行符（`\r`/` `/` ` 统一替换为 `\n`）折叠为一行：

```
collapseIntraBlockLineBreaks(text) string:
  if text == "": return ""
  normalized := 把 \r,  ,   替换为 \n
  if !strings.Contains(normalized, "\n"): return normalized
  parts := 按连续的 \n 切分（正则 \n+，等价于 Go strings.FieldsFunc 或 regexp.Split）
  merged := ""
  for part in parts:
      t := trim(part)
      if t == "": continue
      merged = merged=="" ? t : mergeText(merged, t)   // 复用同一套硬换行拼接规则（去重复边界字符+补空格）
  return merged
```

## 算法：`appendListContinuationInline`

把"列表项 + 续行"内联拼接到同一行末尾（用于 `appendTextBodyAsMarkdown` 步骤 2 的早退路径）：

```
appendListContinuationInline(out, prevListText, continuationText):
  if out 以 '\n' 结尾: 删除该字符（撤回上一次 WriteString 留下的换行，改为原地续写）
  right := continuationText
  if shouldDropDuplicatedBoundary(prevListText, right) && right != "":
      right = right[1:]  // 按 rune 截断
  if needSpace(prevListText, right):
      out.WriteString(" " + right)
  else:
      out.WriteString(right)
  out.WriteString("\n\n")
```

## 算法：`shouldMergeListWithPlainContinuation`

判定"上一行是列表项、当前行是否应该内联续接到列表项末尾而不是另起一行"：

```
shouldMergeListWithPlainContinuation(listBody, plainBody) bool:
  a := trim(listBody) ?? ""
  b := trim(plainBody) ?? ""
  if a=="" || b=="": return false
  if !isListItem(a) || plainLineRejectedForListContinuation(plainBody): return false
  if hasEmbeddedOrderedListMarker(a): return false   // a 本身已包含多个编号标记（结构已损坏），不再续接
  if endsWithSemanticBreak(a): return false
  if startsWithContinuationPunctuation(b) || shouldDropDuplicatedBoundary(a, b): return true
  end := a 的最后一个非空白字符
  if end in "。.!！?？": return false          // 强终止标点，列表项已完整
  if end in "，,、；;：:": return true          // 软标点，允许续接
  return !endsWithSentenceTerminator(a)          // 兜底：句子未完则续接
```

**注意**：`hasEmbeddedOrderedListMarker` 与 `plainLineRejectedForListContinuation` 两个辅助函数在提供的行号范围之外（本文档未读取到其定义），据函数名与调用上下文推断：
- `hasEmbeddedOrderedListMarker(text)`：检测 `text` 内部（不一定在开头）是否含有形如 `EMBEDDED_ORDERED_LIST_MARKER` 正则（`(?<!\d)(?:\d+[\.、)）\]](?!\d)|[（(]\s*\d+\s*[)）])`）描述的编号标记出现多次。
- `plainLineRejectedForListContinuation(plainBody)`：大概率是"当前行本身是标题/表格片段/代码块等结构化行，不应被并入列表续行"的否决判定，具体逻辑需要移植时再核对源码（该函数不在本文档读取范围内，建议实现前用 `grep -n "plainLineRejectedForListContinuation\|hasEmbeddedOrderedListMarker" PdfToMarkdown.java` 定位并确认，若落在 Part 1/Part 3 范围则由对应文档补充，若落在本 Part 范围内则需要另行读取补全——本文档不臆测其实现）。

---

## 算法：`appendSingleCellTableAsText`

把被识别为"1 行 1 列"的表格（`TableBlock.RowCount==1 && ColCount==1`）还原为正文文本块渲染：

```
appendSingleCellTableAsText(out, table):
  lines := table.SingleCellLines
  if lines == nil:
      fallback := len(table.Cells)==0 ? "" : table.Cells[0].Text
      lines = (fallback=="" || 全空白) ? [] : [trim(fallback)]
  if len(lines) == 0: return
  if MarkdownLineClassifier.LooksLikePreformattedBlock(lines):
      out.WriteString(CodeFenceWriter.Wrap(lines) + "\n")     // ``` 围栏包裹，保留原始分行
  else:
      out.WriteString(strings.Join(lines, " ") + "\n\n")       // 按空格拼接为一行
```

依赖外部类 `MarkdownLineClassifier.LooksLikePreformattedBlock`（判定"是否像代码/SQL/命令行/日志/配置块"）与 `CodeFenceWriter.Wrap`（生成 ``` 围栏），两者均不在本文件定义，视为独立组件移植（不确定具体划归 Part 几，建议向用户确认或按"通用 Markdown 工具类"单列）。

## 算法：`renderTableMarkdown`

把行列数据渲染为 GFM 表格：

```
renderTableMarkdown(rowCount, colCount int, cells []TableCellData) string:
  rows := max(1, rowCount); cols := max(1, colCount)
  grid := rows x cols 的空字符串二维数组

  for cell in cells:
      r0, c0 := cell.Row, cell.Col
      if r0<0 || c0<0 || r0>=rows || c0>=cols: continue
      text := escapePipe(normalizeTableCellText(cell.Text, Config.Defaults()))
      grid[r0][c0] = text
      rowSpan := max(1, cell.RowSpan); colSpan := max(1, cell.ColSpan)
      for r in 0..rowSpan-1:
          for c in 0..colSpan-1:
              rr, cc := r0+r, c0+c
              if rr>=rows || cc>=cols: continue
              if rr==r0 && cc==c0: continue
              grid[rr][cc] = text   // 合并单元格拆分渲染：每个被合并格子都填相同内容，不留空
                                     // （GFM 表格不支持真正 rowspan/colspan）

  逐行拼 "| c1 | c2 | ... |\n"；第 0 行之后额外插入分隔行 "| --- | --- | ... |\n"
  返回结果（去掉末尾多余的换行）
```

**移植注意**：`normalizeTableCellText` 内部调用 `Config.defaults()` 而非接收调用方传入的 `config`——这意味着表格单元格文本规范化**永远使用默认配置**，与正文 `normalizeText(text, config)` 使用调用方 `config` 不一致。这看起来像是遗留的硬编码（`renderTableMarkdown` 签名里根本没有 `config` 参数），移植时应原样保留这个行为（不要"顺手"改成接收外部 config），如果需要修正应先向用户确认，因为 CLAUDE.md 明确要求"修 bug 前不确定根因不要直接改"。

## 算法：`escapePipe`

`text==nil` → `""`；否则把 `|` 替换为 `\|`，把 `\n` 替换为空格（表格单元格内不允许出现真实换行或未转义的竖线）。

---

## 与其他 Part 的接口

### 依赖 Part 1 输出的具体数据形状

本 Part 的入口点是 `convertDocument` 主循环中，Part 1 已完成以下工作后交给本 Part：

1. **`elements []GeometricElement`**：已按 `(PageNo, TopDistance, Left)` 排序、已完成跨页表合并（`mergeCrossPageTables`）、装饰性单格表降级（`demoteDecorativeSingleCellTables`）、跨页段落合并（`mergeCrossPageParagraphBlocks`，注意：这个函数虽然出现在文件前部（L461-498），但它复用了本 Part 的 `merge`/`styleDifferent`/`isHeadingByRegex`/`isHeading`/`isListItem`/`isStandaloneChineseDateLine`/`endsWithSentenceTerminator`/`endsWithSemanticBreak`，属于"Part 1 的编排逻辑调用 Part 2 的判定函数"，实现顺序上 Part 2 的这些函数需要先行/同时提供）。
2. **`orderedTextBlocks []*TextBlock`**：从 `elements` 中过滤出 `TextBlock` 且 `!MonoFont` 的子集，是 `buildHeadingStyleProfile`/`ShortPhraseListRunHeuristics.DetectPdfShortPhraseListRuns`/`ListGuideHeuristics.DetectListGuideScopeBlockIds`/`detectHeadingSequenceConsistencyDemoteBlockIds`/`detectDisqualifiedPatternDemoteBlockIds` 五个函数的共同输入。**关键**：单纯的等宽字体块（代码/表格残留）被排除在样式聚类与标题判定的统计范围之外，但在最终渲染主循环里仍然会被渲染（`appendTextAsMarkdown` 对 `MonoFont` 块直接走 ``` 围栏分支，不参与标题判定）。
3. **`TextBlock` 每个字段的填充责任**：`ID/PageNo/BBox/TopDistance/Left/Text/FontSizeMean/FontFamily/FontWeight/Italic/MonoFont/LineHeight/IndentLeft/TableID/BodyFontMode/PageWidth` 由 Part 1 的 `buildLineBlock`/`mergeLines` 填充；`HeadingLastLineTop/HeadingTrailingLeft/HeadingTrailingText`（NaN/NaN/nil 表示"未合并过、就是原始单行"）由本 Part 的 `merge`/`mergeHeadingBlocks` 在合并时写入，Part 1 产出的初始单行块这三个字段均为零值（NaN/NaN/空字符串）。`HeadingPrefixStyleMismatch` 由 Part 1 的 `detectHeadingPrefixStyleMismatch`（本文档已涵盖，因为它被 `buildLineBlock` 调用，逻辑上属于"标题判定"范畴但物理上在 `buildLineBlock` 内联执行）计算写入。
4. **`estimatePageRightEdge(lines []*TextBlock) float64`**：Part 1 提供的函数，本 Part 的 `mergeLines`/`shouldMerge`/`layoutBreak`/`isFullWidthBodyLine` 消费其返回值（可能是 `NaN`，表示"当页正文右边界不可信，回退对称页边距估计"）。
5. **`Config` 的完整字段集合**：Part 1/2/3 共享同一个 `Config` 结构体，本 Part 只读取其中标题/合并/渲染相关字段（见「数据结构」一节列出的子集），不拥有 `Config` 的定义权——`Config` 的完整字段清单与默认值应在总体移植文档（或 Part 1 文档）中统一定义一次，避免多处重复定义产生分歧。

### 编码/长度语义注意（跨 Part 通用陷阱）

Java 的 `String.length()`/`charAt()`/`substring()` 按 UTF-16 code unit 计算，绝大多数中文字符落在基本多语言平面（BMP）内，一个汉字 = 一个 code unit，与 Go 的 `[]rune` 长度（按 Unicode code point 计）在纯中文场景下数值一致；但涉及非 BMP 字符（如某些生僻字、emoji）时两者会不同。本 Part 大量使用 `text.length()`、`charAt(0)`、`substring(1)` 做字符级操作（如 `shouldDropDuplicatedBoundary`、`mergeText`、`appendListContinuationInline`），移植到 Go 时应统一按 `[]rune` 操作，不要直接用 Go 的 `len(string)`（按字节）或字节索引，否则中文字符会被从中间切断。这不是本 Part 独有的问题，Part 1/3 的字符串处理函数也需要遵守同一约定，建议在移植的公共工具层提供 `runeLen`/`runeAt`/`runeSubstring` 等封装，全项目统一使用。

### 正则环视断言（RE2 不兼容）问题清单

Go 标准库 `regexp`（RE2 引擎）不支持零宽断言（`(?!...)`、`(?<=...)`、`(?<!...)`）。本 Part 依赖以下含此类断言的正则，移植时需要用 `github.com/dlclark/regexp2`（支持 .NET 风格环视）或改写为等价的手写扫描逻辑，两种方案各有性能/维护权衡，建议在开工前统一决策（这是 Part 1/2/3 共同面对的问题，不应该三个 Part 各自选择不同方案）：

- `TITLE_NUM_SIMPLE`、`TITLE_NUM_MULTI`：均含 `(?!\d)` 或 `(?!\d|\.|\%|％)` 否定前瞻。
- `HEADING_PREFIX_ONLY`：内部子模式本身不含断言，但被 `find()` 用于"前缀匹配"，Go 需要用 `FindStringIndex` 之类的锚定开头匹配替代（这个相对好办，不涉及断言）。
- `LIST_NUM_PREFIX`、`ORDERED_LIST_MARKER_PREFIX`、`EMBEDDED_ORDERED_LIST_MARKER`：均含 `(?!\d)` 及 `(?<!\d)`（`EMBEDDED_ORDERED_LIST_MARKER` 还用了否定后顾 `(?<!\d)`）。
- `ADDRESSEE_SALUTATION_LINE` 用到 `\p{IsIdeographic}`：Go `regexp` 不认识这个 Unicode 属性名（Java 专有写法），需要换成 Go 支持的 `\p{Han}` 或显式 CJK 统一表意文字 Unicode 范围（如 `\x{4E00}-\x{9FFF}`），语义上可能有细微差别（`IsIdeographic` 覆盖范围比 `Han` 脚本略广，包含了一些扩展区），建议移植前用几个边界字符测试两者差异。

### 输出交给 Part 3 的形状

`convertDocument` 主循环遍历 `elements`，对每个 `TextBlock` 调用本 Part 的 `appendTextAsMarkdown` 写入同一个 `StringBuilder out`（Go 对应 `strings.Builder`），对每个 `TableBlock` 调用本 Part 的 `appendSingleCellTableAsText`/`renderTableMarkdown`。最终产出一个**扁平的 Markdown 文本流**（`string`），标题已经是 `#`/`##` 前缀、列表已经是缩进+原始编号前缀（不是 Markdown 原生 `-`/`1.` 列表语法，直接透传原文的中文编号如"一、"/"（一）"/"1."）、表格已经是 GFM 语法、代码块已经是 ``` 围栏。

这个字符串交给 Part 3 依次执行：
1. `fallbackMergeMarkdown(markdown, config)`（若 `config.FallbackMergeMarkdown` 为真）：按 Markdown 块（用空行切分）做兜底断句合并，只处理"纯正文段落块"，会调用本 Part 的 `shouldMergeListWithPlainContinuation`/`isListItem`/`isHeadingByRegex` 等函数做块分类（`classifyBlock`），但合并逻辑本身（`splitMarkdownBlocks`/`splitTracePrefix`/`BlockParts`/`BlockKind`）是 Part 3 的职责。
2. `removeTocFromMarkdown(markdown)`（若 `config.RemoveToc` 为真）：目录块移除，纯文本级操作，不依赖本 Part 的任何函数。
3. `cleanOutput(markdown)`：最终收尾清理（多余空行合并等）。

本 Part 不需要知道 Part 3 内部如何实现这三步，只需要保证：**渲染阶段产出的 Markdown 文本本身语法正确、标题层级正确、且已经过 `normalizeText`/`collapseIntraBlockLineBreaks` 规范化**——Part 3 的兜底合并是"防御性"的最后一道保险，不应该依赖它来修正本 Part 本该做对的事情。

### 需要向用户确认的疑点（不要擅自处理）

1. `isHeading` 步骤 2 中 `normalizeText(block.Text)`（无 `config` 参数，等价于用 `Config.defaults()`）与函数其余部分统一使用调用方传入的 `config` 不一致，是否是原始实现的笔误。
2. `renderTableMarkdown` 内部硬编码调用 `normalizeTableCellText(cell.Text, Config.defaults())` 而不接收外部 `config`，导致表格单元格文本规范化行为与正文不联动任何 `Config` 覆盖项，是否需要移植时保留这个"分裂"行为。
3. `restAfterCnArticlePrefix`（L3554-3557）在提供的搜索范围内未发现调用点，可能是死代码，移植时是否需要保留（若确认无用可以不移植）。
4. `hasEmbeddedOrderedListMarker`/`plainLineRejectedForListContinuation` 两个被 `shouldMergeListWithPlainContinuation` 依赖的函数不在本文档读取的行号范围内，需要另行定位读取（很可能落在 Part 1 或 Part 3 的范围，或需要补一次单独读取）。
