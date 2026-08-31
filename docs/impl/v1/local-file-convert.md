# 本地文件转换降级 工程实现

## 背景与定位

FileView（`/Users/jxu/Code/fileview`）是一个独立部署的 Java 服务，承担 DOCX/XLS/PDF → Markdown 的转换，底层用 Aspose（商业库）+ 自研 `PdfToMarkdown`。该服务不可达时（部署环境缺失、网络隔离等），当前 `internal/source/fileview.go` 的 `fileViewClient` 会直接报错，Source 导入流程整体失败。

本模块新增一套**纯 Go、内置于 wiki-brain 进程**的本地转换实现，覆盖 **DOCX、XLS/XLSX、PDF、PPTX** 四种格式，作为 FileView 不可用时的降级方案。

**明确不做的事**：
- 本次不纳入 HTML 格式（当前 `fileViewWhitelist` 本身也没有 `.html/.htm`，超出现有范围）。
- 不做转换质量标记（`sources` 表不新增字段区分本次是 FileView 转换还是本地降级），降级对上层完全透明。
- 不做自动探活切换——切换只能靠配置手动指定，不做"FileView 不可达时自动切换"的运行时探测逻辑。

**范围调整记录（2026-08-30）**：PDF 部分最初计划为"用 docmill 默认 pipeline 跑通基础转换、不追求 FileView 级别效果"的 MVP 方案，讨论后改为**完整移植** FileView `PdfToMarkdown.java` 及其依赖的全部启发式（约 15000 行、20 个文件），方法与 FileView 一致——用 docmill 只做 PDF 元素解析，组装/渲染层照搬 FileView 的判据完全自己实现。详见第 6 节与 `docs/impl/v1/pdf-port/` 下的六份逐函数移植规格文档。

**范围新增记录（2026-08-31）**：新增 PPTX → Markdown。核实 FileView 源码后发现 `PptConverter.java` 只做 PPT/PPTX → HTML（预览用），**没有**对应的 PPT-to-Markdown 实现，因此本次不是"逐函数移植 Java 逻辑"，而是直接采用开源项目 [greenstevester/pptx2md-go](https://github.com/greenstevester/pptx2md-go)（MIT License，纯标准库实现，约 500 行）的现成 Go 代码。详见第 9 节与 `docs/impl/v1/pptx-port/01-pptx-to-markdown.md`。

## 1. 触发机制：配置开关

`config.yml` 的 `fileview` 节新增 `mode` 字段：

```yaml
fileview:
  mode: "remote"   # remote | local，默认 remote，与现有行为完全一致
  base_url: "http://127.0.0.1:8000"
  poll_interval_ms: 1500
  max_poll_seconds: 600
```

```go
// internal/foundation/config/config.go
type FileViewConfig struct {
    Mode           string `yaml:"mode"` // "remote"（默认）| "local"
    BaseURL        string `yaml:"base_url"`
    PollIntervalMs int    `yaml:"poll_interval_ms"`
    MaxPollSeconds int    `yaml:"max_poll_seconds"`
}
```

`cmd/server/main.go` 按 `cfg.FileView.Mode` 二选一构造 `source.FileViewClient`，其余装配代码（`source.NewService(..., fvClient, ...)`）不变：

```go
var fvClient source.FileViewClient
switch cfg.FileView.Mode {
case "local":
    fvClient = source.NewLocalConvertClient()
default:
    fvClient = source.NewFileViewClient(cfg.FileView.BaseURL, cfg.FileView.PollIntervalMs, cfg.FileView.MaxPollSeconds)
}
```

运维/开发手动切换 `mode` 并重启进程即可，没有中间态、没有运行时自动降级。`mode` 为空或非法值一律按 `remote` 处理，保持向后兼容（现有部署不改 config.yml 时行为不变）。

## 2. 包结构

```text
internal/source/localconvert/
    client.go     // LocalConvertClient：实现 source.FileViewClient 接口，按扩展名分发
    excel.go       // XLS/XLSX → JSON（移植 FileView ExcelToMarkdown.java）
    docx.go        // DOC/DOCX → Markdown（基于 ieshan/go-ooxml）
    pdf.go         // PDF/OFD → Markdown（基于 ivanvanderbyl/docmill）
    pptx.go         // PPTX → Markdown（内联自 pptx2md-go，见第 7 节）
    pptx/           // 内联自 pptx2md-go 的 internal/pptx 包（extract/render/postprocess）
    html.go        // 简易 Markdown → HTML 预览渲染（ConvertToHTML 用，见第 6 节）
```

新增依赖（`go.mod`）：

```text
github.com/xuri/excelize/v2              // 已有依赖则复用，否则新增
github.com/ieshan/go-ooxml               // DOCX 解析 + Markdown 导出
github.com/ivanvanderbyl/docmill/v2      // PDF → Markdown（BSL 1.1，自用场景符合 Additional Use Grant）
// PPTX 不新增 go.mod 依赖：pptx2md-go 的 internal/pptx 包源码直接复制进本仓库（MIT License 允许内联复制），
// 纯标准库 archive/zip + encoding/xml 实现，见第 7 节
```

`internal/source/fileview.go` 的 `FileViewClient` 接口不改：

```go
type FileViewClient interface {
    ConvertToMarkdown(ctx context.Context, srcPath string) (markdown []byte, err error)
    ConvertToHTML(ctx context.Context, srcPath string) (html []byte, err error)
}
```

`LocalConvertClient` 是该接口的第二个实现，`internal/source/service.go` 消费方（`s.fileView.ConvertToMarkdown/ConvertToHTML`）不需要改一行代码。

## 3. client.go：按扩展名分发

```go
type LocalConvertClient struct{}

func NewLocalConvertClient() *LocalConvertClient { return &LocalConvertClient{} }

func (c *LocalConvertClient) ConvertToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
    switch strings.ToLower(filepath.Ext(srcPath)) {
    case ".xls", ".xlsx":
        return convertExcelToMarkdown(srcPath)
    case ".doc", ".docx":
        return convertDocxToMarkdown(srcPath)
    case ".pdf":
        return convertPDFToMarkdown(ctx, srcPath)
    case ".pptx":
        return convertPptxToMarkdown(srcPath)
    default:
        return nil, fmt.Errorf("localconvert: unsupported format %s (only doc/docx, xls/xlsx, pdf, pptx)", filepath.Ext(srcPath))
    }
}
```

**格式覆盖的已知缺口，需要在文档和错误信息里显式标注，不要静默降级成空转换**：

| 扩展名 | 覆盖情况 |
| --- | --- |
| `.docx` | go-ooxml 支持（OOXML 格式） |
| `.doc`（旧二进制格式） | **不支持**——go-ooxml 只解析 OOXML（zip+XML），老版 Word 二进制格式无法解析，直接返回明确错误，不静默失败 |
| `.xlsx` | excelize 支持 |
| `.xls`（旧二进制格式） | **不支持**——excelize 同样只处理 OOXML 格式的 Excel，返回明确错误 |
| `.pdf` | docmill 支持（born-digital PDF；不支持扫描件 OCR） |
| `.ofd` | **不支持**——国产 OFD 是完全不同的文件格式（不是 PDF 的变体），docmill/docx2md 均不覆盖，直接返回错误 |
| `.pptx` | 内联自 pptx2md-go 支持（OOXML 格式）；不提取图片内容/不处理合并单元格，见第 7 节「已知限制」 |
| `.ppt`（旧二进制格式） | **不支持**——与 `.doc`/`.xls` 同理，OLE2 复合文档格式，直接返回明确错误 |

`fileViewWhitelist`（`internal/source/format.go`）本身覆盖 `.doc/.xls/.ppt/.pptx/.wps/.et/.dps/.ofd/.rtf/.txt` 等格式，本地方案只覆盖其中 `.doc/.docx/.xls/.xlsx/.pdf/.pptx` 六种——`mode: local` 时如果上传了 PPT（旧格式）/OFD/RTF 等本地方案不覆盖的格式，`ConvertToMarkdown` 直接返回 `unsupported format` 错误，Source 处理按现有失败路径处理（`internal/source/service.go` 已有的错误处理逻辑不变），不新增特殊分支。

## 4. Excel → JSON（移植 `ExcelToMarkdown.java`）

设计依据：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/ExcelToMarkdown.java`。这是一次**逻辑到逻辑的移植**，不是重新设计——所有判据、阈值原样照搬，只把 Aspose Cells 的 API 换成 excelize 的等价调用。

### 4.1 API 对照

| FileView（Aspose Cells） | 本地实现（excelize） |
| --- | --- |
| `Worksheet.getCells().getMaxDataRow()/getMaxDataColumn()` | `excelize.GetRows(sheet)` 取行数；每行 `len()` 取该行最大列数，遍历取全表最大列数 |
| `cells.get(row, col).getStringValue()` | `f.GetCellValue(sheet, cellRef)`（`excelize.CoordinatesToCellName`拼 cellRef） |
| `cells.getMergedCells()` → `CellArea{StartRow,StartColumn,EndRow,EndColumn}` | `f.GetMergeCells(sheet)` → `[]MergeCell`，`GetStartAxis()/GetEndAxis()` 换算行列号 |
| `cell.getStyle().getFont()`（Name/Size/Bold） | `f.GetCellStyle(sheet, cellRef)` → styleID，`f.GetStyle(styleID)` → `Font.{Family,Size,Bold}` |
| `wb.getWorksheets().getCount()` / 逐个 `Worksheet` | `f.GetSheetList()` 遍历 |

### 4.2 移植范围

`ExcelToMarkdown.java` 的以下函数按原逻辑逐个移植为 Go 函数（同名或语义对应，放在 `excel.go`）：

```text
buildPivotJsonForSheet          → buildPivotJSONForSheet
detectHeaderRowsWithStyle       → detectHeaderRowsWithStyle
detectHeaderRowsByFontStyle     → detectHeaderRowsByFontStyle（字体签名采样+顶部扫描逻辑不变）
detectRowDimensionColumns       → detectRowDimensionColumns
detectConstantHeaderLabels      → detectConstantHeaderLabels
detectMostFrequentHeaderCell    → detectMostFrequentHeaderCell
buildTableJsonForSheet          → buildTableJSONForSheet（普通表格模式）
profileColumns / ColumnProfile  → profileColumns / columnProfile（isMostlyNumeric/isMostlyText 阈值不变：≥max(2, n*0.6)）
detectCarryForwardColumns       → detectCarryForwardColumns
looksLikePivotSingleHeader      → looksLikePivotSingleHeader（含 isCategoryLikeHeader 正则：季度/月/年/数字序号）
hasEnoughNumericValues          → hasEnoughNumericValues（numeric≥3 且占比≥0.2）
excelRowId                      → excelRowID（row_0001 格式，1-based，四位补零，超 9999 不补零）
buildRowLevelStatements         → buildRowLevelStatements（`id=xxx | 表=xxx | data={...}`）
```

判据全部原样保留，包括容易遗漏的边界：
- pivot 模式下若维度列已覆盖全部列（`valueStartCol > maxCol`）→ 退化为 table 模式；
- 单行表头且无常量标签行、不满足 `looksLikePivotSingleHeader` → 退化为 table 模式；
- value 区域同时存在"主要数字列"和"主要文本列" → 强制 table 模式（避免文本备注被 `parseNumber` 清零）；
- 多级行维度的"向下继承"（carry-forward）在上级维度变化时要清空所有下级继承值，避免跨组串值。

### 4.3 输出格式

与 FileView 完全一致的 Markdown 结构，因为 `internal/source/normalize.go` 的 `stripPivotTextDuplicate` 已经依赖这个形状（json 围栏块 + text 围栏块）：

```text
（多 sheet 时，每个 sheet 前）## Sheet: <sheet_name>

```json
{ "table_name": "...", "schema": [...], "data": [...], "meta": {...} }
```
```text
id=row_0001 | 表=... | data={...}
id=row_0002 | 表=... | data={...}
```
```

空 sheet（`GetRows` 返回空）跳过；全表为空时输出 `{table_name, schema:[], data:[]}` 兜底，与 Java 版一致。

### 4.4 测试

用 `internal/source/localconvert/testdata/` 下的样例 Excel（可从 `/Users/jxu/Code/fileview` 的测试资源里挑几个代表性文件复制过来：单行表头宽表、多级表头 pivot、含合并单元格、全文本表）逐个跑，**对照 FileView 实际转换结果做 JSON 结构级 diff**（不要求 key 顺序一致，要求 `table_name`/`schema`/`data`/`meta.mode` 语义一致）。

### 4.5 细化实现文档：`docs/impl/v1/xlsx-port/`

[xlsx-port/01-excel-to-markdown.md](xlsx-port/01-excel-to-markdown.md) 按本节"逻辑到逻辑移植"的方法，给出 `ExcelToMarkdown.java`（838 行）的完整函数级移植规格：`buildPivotJsonForSheet` 主决策链的四个退化判定短路顺序、header 行识别（内容启发式 + 字体样式启发式）、行/列维度探测、常量标签行识别、普通表格模式的数值列/文本列继承规则差异、`isNumeric`/`parseNumber` 等基础工具函数在不同调用点的不一致处理（如是否先去千分位逗号），以及 Aspose Cells → excelize 的 API 能力核实清单（`GetCellValue` 对格式化单元格的返回值、`Style.Font` 字段形状等未核实项）。是后续写 Go 代码时比本节优先的第一手依据。

## 5. DOCX → Markdown（`ieshan/go-ooxml`）

```go
// docx.go
func convertDocxToMarkdown(srcPath string) ([]byte, error) {
    doc, err := docx.Open(srcPath)
    if err != nil {
        return nil, fmt.Errorf("localconvert: open docx: %w", err)
    }
    defer doc.Close()
    md := doc.Markdown(&docx.MarkdownOptions{IncludeComments: false})
    return []byte(md), nil
}
```

go-ooxml 的 `Document.Markdown()` 已经实现：按样式识别标题层级、run 级粗体/斜体/删除线、表格、列表、批注转脚注。`IncludeComments` 设为 `false`——批注是文档协作痕迹，不是正文知识，转成脚注混进 Markdown 正文会被 Unit 抽取误当成知识点。

**验证方式**：go-ooxml 在 GitHub 活跃度极低（0 star、无 description），接入前必须先用几份代表性真实 DOCX（含合并单元格表格、多级列表、批注、修订）跑通，人工核对输出是否结构合理，而不是假设它和 Aspose Words 同等可靠。测试样例同样放 `internal/source/localconvert/testdata/`。

**已知限制**：仅支持 `.docx`（OOXML），`.doc`（旧二进制格式）直接报错，见第 3 节。

### 5.1 细化实现文档：`docs/impl/v1/docx-port/`

核实 FileView 实际实现（`WordToMarkdown.java`，553 行）后发现，FileView 并非简单调用 Aspose Words 自带的"另存为 Markdown"能力，而是自己实现了一套标题识别/层级判定/跨段合并/表格转换算法，且转换结果之后还要再经过与 PDF **共用同一套**的后处理管线（`MarkdownPostProcessorPipeline`）才是最终产出——与本节"调用 go-ooxml 内置转换"的方案在保真度上有实质差距。[docx-port/01-word-to-markdown.md](docx-port/01-word-to-markdown.md) 按逻辑到逻辑移植的方法给出了完整规格（标题判定、片段合并、表格合并单元格展开、go-ooxml API 能力核实清单等），供参考。

**该文档第 0 节提出的架构分歧尚未定案**：是维持本节"调用 go-ooxml 内置转换"的 MVP 方案，还是改为"完整移植 `WordToMarkdown.java` + 复用 PDF 已移植的 MPP 管线"（与 PDF 部分 2026-08-30 的范围调整同一性质），需要用户确认后再实现，不要在开工时自行选择路线。

## 6. PDF → Markdown（完整移植 FileView `PdfToMarkdown.java` 全套启发式）

**范围确认（2026-08-30 定案）**：不是"先跑通 docmill 默认 pipeline 接受较弱效果"的 MVP 方案，而是**完整移植** FileView 的 PDF 转换引擎——`PdfToMarkdown.java`（5215 行）及其依赖的全部同目录/`mpp` 子包启发式类（合计约 15000 行、20 个 Java 文件）。方法上与 FileView 一致："先解析 PDF 元素，再组装"：
- **元素解析**复用 docmill 的 pure-Go PDF 解析引擎（`pkg/parser`），不重新实现 PDF 二进制格式解析；
- **组装/渲染层**完全自己写，照搬 FileView 的判据、阈值、正则，不用 docmill 自带的 `pdf.ExtractMarkdown()` 默认组装逻辑（那是 docmill 自己的一套不同启发式，效果不等价于 FileView）。

### 6.1 详细算法规格：`docs/impl/v1/pdf-port/`

Java 源码本身耦合极深（`PdfToMarkdown.java` 与 `mpp` 包的类互相调用，甚至反向依赖），一次性通读+移植风险很高。已按依赖聚类拆成 6 份独立的移植规格文档，每份都是逐函数级别的算法描述（步骤、常量、正则全部照 Java 源码原样摘录，不是转述），**是后续写 Go 代码时的第一手依据，比这份总纲文档优先**：

| 文档 | 覆盖内容 | 对应 Java 源 |
| --- | --- | --- |
| [pdf-port/01-extraction-geometry.md](pdf-port/01-extraction-geometry.md) | 元素提取与几何计算：文本/表格块提取、页眉页脚识别、行合并、跨页合并、装饰性单格表还原 | `PdfToMarkdown.java` 前半 |
| [pdf-port/02-heading-style-render.md](pdf-port/02-heading-style-render.md) | 样式聚类、标题候选/层级判定、跨行标题合并、Markdown 渲染 | `PdfToMarkdown.java` 后半 |
| [pdf-port/03-toc-cleanup-sequence.md](pdf-port/03-toc-cleanup-sequence.md) | 目录剔除、同前缀连坐降级、块分类合并、最终清理 `cleanOutput` | `PdfToMarkdown.java` 首尾 |
| [pdf-port/04-toplevel-heuristics.md](pdf-port/04-toplevel-heuristics.md) | `ChapterReferenceHeuristics`/`ChapterTocLineRemover`/`HeadingLevelPrefixHeuristics`/`HeadingPatternQualityHeuristics`/`HeadingSequenceConsistencyHeuristics`/`HeadingSuppressHeuristics`/`ListGuideHeuristics`/`ShortPhraseListRunHeuristics`/`MarkdownStructureRules`/`CodeFenceWriter`（10 个顶层辅助类） | 同目录 10 个独立 `.java` 文件 |
| [pdf-port/05-mpp-heading-stack.md](pdf-port/05-mpp-heading-stack.md) | MPP 流水线的标题识别阶段：`MarkdownHeadingStage`（1685 行）、目录校验、`MarkdownPipelineContext`（跨阶段共享状态） | `mpp/` 14 个文件 |
| [pdf-port/06-mpp-merge-cleanup.md](pdf-port/06-mpp-merge-cleanup.md) | MPP 流水线的噪声清理与正文弱合并阶段：`MarkdownBodyMergeStage`（917 行）、`MarkdownWeakMergeHeuristics`（704 行） | `mpp/` 6 个文件 |

六份文档由独立 agent 并行编写后交叉核对，已发现并在此收敛以下**跨文档架构决策**（写 Go 代码时按此执行，不要按各文档里"留给人工决策"的措辞各自发挥）：

### 6.2 已收敛的架构决策

**(1) 单一 Go 包，不做严格分层**

Part 1 的 `shouldMerge`（判断两行是否应合并为一段）在合并阶段就要调用 Part 2 的 `isHeading`（判断一行是否已经是标题、不应被吞并）——这是双向依赖，Java 原始代码也是同一个类里互相调用。Go 移植**不要**把 6 份文档拆成 6 个互相导入的包，而是全部放进同一个内部包：

```text
internal/source/localconvert/pdfconv/
    geometry.go       // Part 1：提取、几何、行合并、跨页合并
    heading.go         // Part 2：样式聚类、标题判定、跨行标题合并
    render.go          // Part 2：Markdown 渲染（appendTextAsMarkdown 系列）
    toc.go              // Part 3：目录剔除、同前缀连坐、最终清理
    toplevel_*.go       // Part 4：10 个顶层类，每个（或每组相关的）一个文件
    mpp_context.go      // Part 5：MarkdownPipelineContext 及配套值类型
    mpp_heading.go      // Part 5：MarkdownHeadingStage 及标题校验链
    mpp_merge.go        // Part 6：MarkdownBodyMergeStage / MarkdownWeakMergeHeuristics
    mpp_cleanup.go      // Part 6：MarkdownNoiseCleanupStage / RepeatedBoilerplateLineRemover
    textnorm.go         // 见 (4)：跨 Part 共用的文本规范化原语，只实现一份
    pdf.go              // 入口：docmill 解析 + 调用上面各阶段，供 client.go 消费
```

**(2) 坐标系统一为 BOTTOMLEFT，在提取层入口一次性归一化**

Aspose 的 `Rectangle` 固定是 PDF 原生坐标系（左下角原点，Y 向上）。docmill 的 `geom.Box` 带显式 `Origin`（`TOPLEFT`/`BOTTOMLEFT`）。所有移植文档里的几何计算（`topDistance`、页眉页脚 Y 比例判断、行分组的 Y 排序）都是按 Aspose 的 BOTTOMLEFT 语义描述的。**必须**在读取 `page.TextCell` 后、送入任何几何函数前，先做一次统一转换：

```go
func toBottomLeft(box geom.Box, pageHeight float64) geom.Box
```

不要在每个函数里各自判断 Origin 再分别处理——否则极易在某个分支遗漏，导致该分支的 Y 方向整体颠倒且难以察觉（这类 bug 表现为"页眉误判成页脚"或"标题合并方向反了"，非常隐蔽）。

**(3) 表格边框检测：复用 docmill 的 `RulingSegments()`，不用 `pkg/table`**

Part 1 文档标注了一个 gap：Aspose 的 `TableAbsorber`/`AbsorbedTable` 边框识别没有 docmill 直接等价物，docmill 自己的 `pkg/table` 是另一套算法（OTSL + 无边框表格重建），套用它会产出与 FileView 不同的表格切分方式，不满足"完整移植"的目标。

已核实 docmill 默认 backend（`pkg/parser`）实际实现了 `RulingSegments(ctx) ([]page.RulingSegment, error)`——这是 PDF 页面里描边直线段的几何数据，正是 Aspose `TableAbsorber` 内部用来识别表格边框的同类原始数据。**移植方案**：用 `RulingSegments()` 拿到的线段重建表格网格（判断哪些线段构成横线/竖线、聚类出行列边界），替代 Aspose `AbsorbedTable` 的角色，再套用 Part 1 文档里 `clusterAbsorbedTables`/`buildMergedTableBlock`/`absorbOrphanFragments` 等函数描述的聚类与吸附算法（这些算法本身只依赖"矩形区域+文本"，与边框数据的来源无关，可以原样使用）。

**(4) 跨 Part 共用的文本规范化原语，只实现一份**

Part 1/2/3 都用到 `normalizeText`、`isHeadingByRegex`、`isListItem`、`mergeText` 等基础函数（Java 里是 `PdfToMarkdown` 类的静态方法，被到处调用）。三份文档各自描述了一遍，但这是同一份实现，不要在 Go 里按 Part 拆成三份各自维护。统一放进 `textnorm.go`，其余文件直接调用。

同理，`HeadingSuppressHeuristics` 的部分判定函数被 Part 2（`shouldSuppressHeading`）和 Part 3（`clearlyFailsHeadingQuality`/`shouldMergeMarkdownPlainBlocks`）同时调用——这是 Part 4 文档里的同一份实现，两处调用点不要各自复制。

**(5) Go 正则不支持 lookahead/lookbehind：手动边界检查，不引入新依赖**

六份文档合计发现十几处用到 Java 正则的 `(?=...)`/`(?!...)`/`(?<=...)`/`(?<!...)`（Go 标准库 `regexp`/RE2 不支持）。已统一策略，**不引入** `dlclark/regexp2` 等第三方正则库：

```text
1. 把断言部分从正则里去掉，用普通 RE2 正则匹配到断言应该生效的边界位置；
2. 用 FindStringIndex/FindAllStringIndex 拿到匹配的起止 rune/byte 位置；
3. 手动检查该位置前后一个字符是否满足原本断言的条件（例如 (?!\d) 就是检查匹配结束位置的下一个字符是否为数字）。
```

这些断言绝大多数只是"匹配结束位置后一个字符不是数字/小数点"这类单字符边界检查（例如 `TITLE_NUM_SIMPLE`/`EMBEDDED_ORDERED_LIST_MARKER`/`NUMERIC_OUTLINE_BOUNDARY` 等），手动检查足够且不增加依赖。每个具体正则的替换写法已在对应 Part 文档的"Go regexp 兼容性预警"章节列出。

`\p{IsIdeographic}`（Java 专有 Unicode 属性，用于 `ADDRESSEE_SALUTATION_LINE` 等）在 Go 里没有直接对应，改用 `\p{Han}`（Go `regexp/unicode` 支持的 Unicode script 分类），语义足够接近（都是匹配中文汉字，`ADDRESSEE_SALUTATION_LINE` 这类场景不涉及生僻的非汉字表意文字）。

**(6) 已发现但尚未指派归属的遗留问题**

以下是各文档交叉核对时发现、但本次收敛未下结论的点，写代码时遇到再决定，不阻塞开工：
- `TOC_CHAPTER_PAGED_ENTRY`（Part 3）疑似死代码（未找到调用点），移植时先跳过，若后续测试发现目录剔除缺了某类格式再回头看是否该激活；
- `isHeading` 内部一处调用 `normalizeText(block.Text)` 未传 `config`（该函数其余地方都传），像是 Java 原代码的既有 bug——移植时**按原样保留**（不要"顺手修掉"，除非之后用真实文档验证发现这确实是缺陷且经用户确认，参照 CLAUDE.md「修 bug 先确认根因」的要求）；
- `MarkdownHeadingStage` 里 10 个未使用的 `WEAK_MERGE_*` 常量：移植时可以不搬，除非发现它们其实是被跳过读取的分支引用（先用 grep 在完整文件里确认零调用再决定丢弃）；
- UTF-16 vs rune 长度语义：Java `String` 按 UTF-16 code unit 计长度/切片，Go `string`/`[]rune` 语义不同，所有涉及"字符位置"/"字符串长度阈值比较"（如 `MaxHeadingLength`）的地方，移植时统一按 Go `[]rune` 长度对齐 Java 的 `codePointCount`（不是 UTF-16 code unit 数，两者在处理 CJK 之外的 emoji/生僻字时会有差异，但 CJK 商务文档场景基本不受影响，这里不做特殊处理）。

### 6.3 入口与配置

```go
// pdf.go
func convertPDFToMarkdown(ctx context.Context, srcPath string) ([]byte, error) {
    data, err := os.ReadFile(srcPath)
    if err != nil {
        return nil, fmt.Errorf("localconvert: read pdf: %w", err)
    }

    backend := parser.NewBackend()
    defer backend.Close()

    doc, err := backend.OpenBytes(ctx, data)
    if err != nil {
        return nil, fmt.Errorf("localconvert: open pdf: %w", err)
    }
    defer doc.Close()

    cfg := pdfconv.DefaultConfig() // 镜像 FileView config.properties 的 pdf2md.* 默认值，见 pdf-port/01 附录
    return pdfconv.ConvertDocument(ctx, doc, cfg)
}
```

`pdfconv.DefaultConfig()` 的字段与默认值见 [pdf-port/01-extraction-geometry.md](pdf-port/01-extraction-geometry.md) 的「Config 字段清单」——原样镜像 `/Users/jxu/Code/fileview` 的 `config.properties` 里 `pdf2md.*` 一节的当前生产取值（不是重新调参，直接抄现有值，因为这些阈值已经在 FileView 生产环境里跑了较长时间）。

### 6.4 调试与验证：借用 FileView 的测试用例

`/Users/jxu/Code/fileview` 仓库自带的 PDF 测试资源直接复用来验证：

```text
/Users/jxu/Code/fileview/pdf/*.pdf                     — 基础用例（00-03.pdf）
/Users/jxu/Code/fileview/test/toc/original/*.pdf       — 含目录的复杂文档（11 份）
/Users/jxu/Code/fileview/test/toc/result/*.md          — FileView 转换的参考输出
```

由于这次是完整移植同一套算法（不是另起炉灶的简化实现），`test/toc/result/*.md` 不再只是"人工比对结构差异"的参考，而是**可以做接近逐字的自动化回归对比**——移植正确的话，Go 版输出应该与这些参考文件高度一致（允许的差异来源：docmill 与 Aspose 的底层文本提取顺序/字距计算存在细微差异，可能导致个别断行边界不同；不允许的差异：标题层级错、表格结构错、目录未剔除、正文内容丢失）。建议测试策略：
1. 先跑通、人工核对前 2-3 份，确认差异都在"允许"范围内；
2. 再对全部 11+4 份做自动化 diff，记录差异行数占比，作为移植质量的量化指标，而不是零容忍逐字断言。

### 6.5 已知限制

- 不支持扫描件/图片型 PDF（无 OCR，FileView 的 OCR 路径 `ocr2md`/`img2md` 不在本次移植范围内）；
- 不支持加密 PDF；
- `.ofd` 不是 PDF 的变体，docmill 不覆盖，见第 3 节；
- 许可证是 BSL 1.1（非标准开源协议）：允许自用/自托管，附加条款禁止将其包装成对外托管转换服务出售给第三方——wiki-brain 是内部组件，不违反该条款；2030-07-02 后转为 Apache 2.0。

## 7. PPTX → Markdown（内联 `pptx2md-go`）

**与第 4/5/6 节方法论不同**：核实 FileView 源码（`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/adapters/PptConverter.java`）后确认，FileView 对 PPT/PPTX **没有**对应的 Markdown 转换实现——只有 `convert()`（Aspose.Slides 另存为单文件 HTML，供预览用）和 `convertToContentHtml()`（逐 shape 抽文字拼简单 HTML，同样输出 HTML）。既然没有"FileView PPT-to-Markdown"这条产线可移植，本节直接采用开源项目 [greenstevester/pptx2md-go](https://github.com/greenstevester/pptx2md-go)（MIT License）的 `internal/pptx` 包：纯标准库（`archive/zip` + `encoding/xml`）实现，约 500 行、6 个文件，无第三方依赖，抽取 zip 内 OOXML part（`ppt/presentation.xml` 定序、`ppt/slides/slideN.xml` 取正文/图片/表格、`ppt/notesSlides/*.xml` 取演讲者备注）→ 组装为 `Deck{Title, Slides[]}` 语义结构 → 渲染为 Markdown（`# 标题` + 每页 `## Slide N: 标题` + 项目符号/图片占位/GFM 表格 + `> **Notes:**` 备注块）→ 后处理（统一换行符、去行尾空白、折叠连续空行）。

```go
// pptx.go
func convertPptxToMarkdown(srcPath string) ([]byte, error) {
    md, err := pptx.Convert(srcPath) // pptx 为内联包，见下方
    if err != nil {
        return nil, fmt.Errorf("localconvert: convert pptx: %w", err)
    }
    return []byte(md), nil
}
```

**依赖引入方式**：`pptx2md-go` 的 `internal/pptx` 因 Go `internal/` 路径规则无法作为外部依赖直接 import，采用**源码复制**——把该包的非测试文件复制进 `internal/source/localconvert/pptx/`（作为本项目自有内部包），MIT 协议允许内联复制，复制时需保留原始 LICENSE 声明（包内加 `LICENSE` 文件或在 `doc.go` 顶部注明来源），对应的 `_test.go` 一并复制作为回归基线。不新增 `go.mod` 依赖项。

**已知限制**（详见 [pptx-port/01-pptx-to-markdown.md](pptx-port/01-pptx-to-markdown.md) 第 5 节）：
- 不提取图片二进制内容，仅保留 `[IMAGE: <alt文本>]` 占位，不做图内文字 OCR；
- 表格不处理合并单元格（`gridSpan`/`rowSpan`/`vMerge`），会被拆成独立单元格；
- 不解析样式/配色/动画/切换效果，仅抽取正文语义内容；
- `.ppt`（旧二进制格式）不支持，直接返回明确 unsupported 错误，与 `.doc`/`.xls` 同理；
- 不支持嵌入 OLE 对象（嵌入 Excel 表格、SmartArt 等），`graphicFrame` 只处理其中的 `tbl` 子元素。

### 7.1 细化实现文档：`docs/impl/v1/pptx-port/`

[pptx-port/01-pptx-to-markdown.md](pptx-port/01-pptx-to-markdown.md) 给出完整的抽取/渲染/后处理规格：OOXML part 与关系解析、幻灯片顺序与标题三级回退（`docProps/core.xml` → 第一张幻灯片标题 → 文件名兜底）、文本/图片/表格/备注四类内容块的抽取细节与已知限制、渲染格式、后处理规则，以及"为什么本节不是移植而是直接采用"的完整依据（第 0 节）。是后续写 Go 代码时的第一手依据。

### 7.2 测试

复制 `pptx2md-go` 自带的 `extract_test.go`/`render_test.go`/`convert_test.go`/`postprocess_test.go`/`relationships_test.go`（`bench_test.go`/`eval_test.go`/`eval_local_test.go` 是上游 CLI/评测脚本，不带）作为回归基线。另用 `internal/source/localconvert/testdata/` 下几份代表性真实 PPTX（含标题页、多级项目符号、图片、表格、演讲者备注）跑通并人工核对——**没有 FileView 参考输出可比对**（FileView 本身不产出 PPT Markdown），验收标准是"结构清晰、无内容丢失、无 panic"，不是与另一套实现做 diff。

## 8. ConvertToHTML：本地降级下的 HTML 预览

`internal/source/service.go` 中 `ConvertToHTML` 失败已经是非致命的（仅 `slog.Warn`，不影响 Source 主流程，见 `convertToMarkdown` 步骤 8 的现有实现）。本地方案不追求 FileView 的排版还原 HTML，只做**从已转换出的 Markdown 渲染一个基础预览页**：

```go
// html.go
func convertToHTMLPreview(markdown []byte) ([]byte, error) {
    var buf bytes.Buffer
    if err := goldmark.Convert(markdown, &buf); err != nil {
        return nil, fmt.Errorf("localconvert: render html preview: %w", err)
    }
    return buf.Bytes(), nil
}
```

`LocalConvertClient.ConvertToHTML` 内部直接复用 `ConvertToMarkdown` 的结果再转 HTML（而不是分别转换两次），避免重复解析源文件：

```go
func (c *LocalConvertClient) ConvertToHTML(ctx context.Context, srcPath string) ([]byte, error) {
    md, err := c.ConvertToMarkdown(ctx, srcPath)
    if err != nil {
        return nil, err
    }
    return convertToHTMLPreview(md)
}
```

`goldmark`（`github.com/yuin/goldmark`）是否已在 `go.mod` 中需要检查；没有则新增依赖，纯 Markdown 渲染，无额外风险。

## 9. 完成标准

```text
配置切换（回归项）：
  fileview.mode 缺省/为空/为 "remote" -> 行为与改动前完全一致（远程 FileView）；
  fileview.mode: "local" -> Service 使用 LocalConvertClient，不再发起任何 HTTP 请求到 FileView。

Excel（xls/xlsx）：
  单行表头宽表 -> table 模式，schema/data 结构与 FileView 对同一文件的转换结果语义一致；
  多级表头 pivot 表（如"季度/销售额"类） -> pivot 模式，dimension/measure 字段名与 FileView 结果一致；
  含合并单元格 -> 合并区域正确展开，不出现空白断档；
  全文本表（无数值列） -> table 模式，不误判为 pivot；
  混合数字/文本列 -> 强制 table 模式，文本备注不被清零；
  多 sheet -> 正确插入 "## Sheet: <name>" 标题，空 sheet 跳过。

DOCX（doc/docx）：
  .docx 正常文档 -> 标题层级、表格、列表结构合理还原；
  .doc（旧格式） -> 返回明确的 unsupported 错误，不静默产出空/错误内容；
  含批注 -> 批注不出现在正文 Markdown 中。

PDF（完整移植，回归项按 pdf-port/ 六份规格逐项对照，此处只列端到端验收）：
  用 fileview 仓库 test/toc/original/*.pdf（11 份）+ pdf/*.pdf（4 份）全量跑一遍 -> 全部成功转换、无 panic/超时；
  与 test/toc/result/*.md 做自动化 diff -> 标题层级、表格结构、目录剔除结果、正文内容与参考文件一致；
    允许存在的差异仅限断行边界（docmill 与 Aspose 文本提取顺序细微差异导致），不允许标题层级错、
    表格结构错、目录残留、正文丢字；
  页眉页脚 -> 正确按 Y 位置比例剔除，不出现在输出正文中；
  跨页表格/跨页段落 -> 正确合并为单个逻辑块，不因分页产生断裂；
  装饰性单格表格（无实际表格语义的方框） -> 正确还原为普通文本，不被误判为表格；
  同前缀标题连坐降级（如"第 X 条"下出现不合格格式） -> 全文一致降级为正文，不出现部分降部分保留；
  扫描件/加密 PDF -> 返回明确错误，不产出空白或乱码内容；
  .ofd -> 返回明确的 unsupported 错误。

PPTX（内联 pptx2md-go，无 FileView 参考输出，验收标准是结构正确而非逐字 diff）：
  含标题页、多级项目符号、图片、表格、演讲者备注的真实 PPTX -> 全部成功转换、无 panic；
  标题解析三级回退（docProps 标题 -> 首页幻灯片标题 -> 文件名兜底） -> 按优先级正确取值；
  幻灯片渲染顺序与演示文稿实际播放顺序（sldIdLst）一致，不是 zip 内文件名顺序；
  项目符号层级（lvl 属性）-> 正确映射为 Markdown 缩进；
  全空白表格 -> 整体丢弃，不产出空表格噪音；
  含合并单元格的表格 -> 按已知限制拆成独立单元格，不 panic、不丢数据；
  .ppt（旧格式） -> 返回明确的 unsupported 错误，与 .doc/.xls 同理；
  嵌入 SmartArt/OLE 对象 -> 静默跳过，不报错、不产出乱码内容。

跨格式：
  ConvertToMarkdown 遇到本地方案不覆盖的扩展名（ppt/wps/et/dps/ofd/rtf/txt 等） -> 返回明确 unsupported 错误，
    Source 处理走现有失败路径，不新增特殊分支；
  ConvertToHTML 失败 -> 现有非致命处理路径不变（仅 warn log，不影响 Markdown 主流程）。
```
