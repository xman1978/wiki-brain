# PPTX → Markdown 移植规格

对应 `docs/impl/v1/local-file-convert.md` 第 9 节。参考实现：[greenstevester/pptx2md-go](https://github.com/greenstevester/pptx2md-go)（MIT License，commit 抓取于 2026-08-31，`internal/pptx/` 共 6 个文件、约 500 行，纯标准库 `archive/zip` + `encoding/xml`，无第三方依赖）。

## 0. 与 DOCX/XLSX/PDF 三份移植文档的方法论差异

DOCX/XLSX/PDF 三份文档都是"FileView 已有 `XxxToMarkdown.java` → 逐函数移植成 Go"。PPTX **不是**这种情况：

`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/adapters/PptConverter.java` 核实后，FileView 对 PPT/PPTX **没有对应的 `ToMarkdown` 实现**——只有 `convert()`（Aspose.Slides 另存为单文件 HTML，用于预览）和 `convertToContentHtml()`（逐 shape 抽文字拼成简单 HTML，同样输出 HTML 不是 Markdown）。也就是说 FileView 里根本没有"PPT 转 Markdown"这条产线，`fileViewWhitelist` 虽然列了 `.ppt`/`.pptx`，但走的是转 HTML 预览的路径，不经过任何 Markdown 转换。

因此本节**不是移植 Java 逻辑**，而是直接采用 `pptx2md-go` 现成的 Go 实现（结构和命名基本照搬，按本项目既有代码风格做少量调整），不存在"跟 FileView 输出对齐"这个校验基准——验收方式相应也不同，见第 5 节。

## 1. 数据模型（`model.go`）

```go
// Deck 是一份 PPTX 演示文稿的语义化表示。
type Deck struct {
    Title  string
    Slides []Slide
}

// Slide 保存单张幻灯片的有序内容块。
type Slide struct {
    Number int
    Title  string
    Blocks []Block
    Notes  string
}

// Block 是一个内容单元。Type 取值："paragraph" | "bullet" | "image" | "table"。
type Block struct {
    Type  string
    Text  string     // paragraph / bullet 的文本
    Level int        // 项目符号缩进层级，来自 <a:pPr lvl> 属性
    Alt   string      // 图片替代文本（优先 descr，否则 name）
    Rows  [][]string // 表格单元格
}
```

原样采用，无需调整——这是纯数据结构，不含平台相关逻辑。

## 2. XML 解析与抽取（`extract.go` → `internal/source/localconvert/pptx.go` 的 extract 部分）

### 2.1 OOXML part 结构与关系解析

PPTX 是 zip 包，涉及的 part：

```text
ppt/presentation.xml              — 幻灯片顺序（sldIdLst，每项一个 r:id 关系引用）
ppt/_rels/presentation.xml.rels   — presentation.xml 的关系表，r:id → 实际 slideN.xml 路径
ppt/slides/slideN.xml             — 单张幻灯片内容（形状树 cSld/spTree）
ppt/slides/_rels/slideN.xml.rels  — 该幻灯片的关系表，用来找 notesSlide 关联
ppt/notesSlides/notesSlideN.xml   — 演讲者备注（关系类型 .../relationships/notesSlide）
docProps/core.xml                 — 文档属性，<dc:title> 作为演示文稿标题的第一来源
```

`parseRelationships`（`relationships.go`）解析标准 `.rels` XML（`<Relationships><Relationship Id=".." Type=".." Target=".."/></Relationships>`），target 归一化规则：
- 相对路径：用 `path.Join(baseDir, target)` 拼接后 `path.Clean`（`baseDir` 是该 `.rels` 文件所属 part 的目录，如 `ppt`、`ppt/slides`）；
- 绝对路径（以 `/` 开头）：`path.Clean` 后去掉前导 `/`，与 zip 条目名（zip 内路径从不带前导 `/`）对齐。

`relsPathFor(part)` 给出 part 对应的 `.rels` 路径：`dir/_rels/file.rels`（OOXML 约定的固定拼接方式，不是配置项）。

### 2.2 幻灯片顺序与标题解析

`Extract` 的主流程：

1. 解析 `ppt/presentation.xml` 的 `sldIdLst>sldId`，每项取 `r:id`（命名空间 `http://schemas.openxmlformats.org/officeDocument/2006/relationships`）；`sldIdLst` 为空直接报错（"presentation has no slides"）——不是静默产出空文档。
2. 解析 `ppt/_rels/presentation.xml.rels` 拿到 `r:id → relationship` 映射；按 `sldIdLst` 顺序（即演示文稿实际播放顺序，**不是** zip 内文件名顺序，两者可能不一致）依次取 `relationship.Target`，过滤 `Type` 不含 `/slide` 的关系（防御性，理论上 `sldIdLst` 里都应该是 slide 关系）。
3. 演示文稿标题 `deck.Title` 的确定顺序：先读 `docProps/core.xml` 的 `<dc:title>`；仍为空则回退到**第一张幻灯片的标题**（循环处理 slide 时，第一次遇到非空 `slide.Title` 就回填）；两者都空，`ExtractFile`（读文件的入口，区别于 `Extract` 读字节流）兜底为**文件名去掉扩展名**。三级回退顺序不能改变——`docProps` 优先是因为它是作者显式填写的元数据，比"猜测第一张幻灯片是标题页"更可靠。

### 2.3 单张幻灯片内容抽取（`extractSlide`）

按 `cSld/spTree` 下三类子元素分别处理，**处理顺序**（先 `sp` 形状，再 `pic` 图片，再 `graphicFrame` 表格）决定了输出 Markdown 里内容块的相对顺序——这与形状在 spTree 里的实际前后顺序**不完全一致**（原实现是按类型分组顺序处理，不是按 XML 文档顺序遍历所有子元素）。移植时保持这个"分组顺序"，不要改成按 XML 元素出现顺序遍历，除非用户明确要求改进保真度。

**文本形状（`sp`）**：
- 每个 `sp` 的 `txBody>p`（段落）转换成 `Block`（`paragraphsToBlocks`）：段落文本为空（`TrimSpace` 后）的段落整个跳过，不产出空 Block；
- 段落有 `pPr`（存在该元素，不论 `lvl` 属性是否显式给出）→ `Type="bullet"`，`Level = max(0, pPr.lvl)`（`lvl` 缺省时 XML 解析为 Go `int` 零值 0，天然是 top-level，不需要额外判断"属性是否存在"）；没有 `pPr` → `Type="paragraph"`，`Level=0`；
- 段落文本 = 该段落下所有 `r`（run）与 `fld`（域代码，如页码占位符）的 `t` 子元素文本按出现顺序拼接（`fld` 混在 `r` 之后统一 append，不保留原始交替顺序——PPTX 里 `fld` 通常只出现在页码/日期占位符形状，混排场景本来就少，交换顺序不影响可读性）；
- **标题判定**：该 `sp` 是幻灯片里**遇到的第一个**满足 `isTitlePH` 的形状（`nvSpPr>nvPr>ph@type` 属于 `title`/`ctrTitle`/`subTitle` 三者之一）且抽出的 Block 非空，其文本（多个 Block 的 `Text` 用空格拼接，`blockText`）设为 `slide.Title`，**该形状本身不计入 `slide.Blocks`**（`continue`，不落入正文）。**只认第一个**——同一页理论上不该有多个标题占位符，若真的出现，后续标题占位符会被当作普通段落落入正文（因为 `titleSet` 已置 true）。

**图片（`pic`）**：每个 `pic` 产出一个 `Block{Type:"image", Alt: descr 或 name}`，`firstNonEmpty(descr, name)` 都为空时兜底字面量 `"image"`（不是空字符串，保证渲染阶段 `[IMAGE: ...]` 里总有内容）。**不提取图片二进制数据、不做 OCR**——这是与 DOCX/PDF 两份移植文档的重要差异点，PPTX 场景里图片经常是配图/图表截图，本次范围只记录"这里有一张图，alt 文本是什么"，图片承载的知识内容对 Unit 抽取不可见，属于已知限制（见第 5 节）。

**表格（`graphicFrame` → `graphic>graphicData>tbl`）**：每个 `tbl` 产出一个 `Block{Type:"table", Rows: [][]string}`，每个 `tc`（cell）内部同样调用 `paragraphsToBlocks` + `blockText` 抽文字（表格单元格本质也是一个 `txBody`，复用同一套段落抽取，不重新实现一遍）。**不处理跨行/跨列合并单元格**（`gridSpan`/`rowSpan`/`vMerge` 属性未读取，合并单元格会被当成多个独立空单元格渲染）——PPTX 表格结构远比 Word/Excel 简单，这是已知限制，不在本次范围内补齐，除非后续实测发现合并单元格是常见场景。

**演讲者备注**：在幻灯片自身的 `.rels` 里找 `Type` 等于 `.../relationships/notesSlide` 的关系（`findRelByType`，为了输出确定性对候选 key 排过序再遍历，避免 map 遍历顺序不稳定导致的测试 flaky），取其 `Target` 解析对应的 `notesSlideN.xml`，同样走 `sp` 形状遍历抽文字，但**跳过** `ph@type=sldNum` 的占位符（备注页固定带的页码占位符，不是真实备注内容），其余文本用空格拼接成 `slide.Notes` 单个字符串（不保留备注内部的段落/项目符号结构——备注通常是自由文本，不像正文需要保留层级）。

## 3. Markdown 渲染（`render.go`）

`ToMarkdown(deck Deck) string`：

```text
# <演示文稿标题>          （deck.Title 为空 → 兜底 "Presentation"）

## Slide <N>: <幻灯片标题>  （slide.Title 为空 → 兜底 "Slide <N>"）

<按 Blocks 顺序渲染每个内容块>

> **Notes:** <演讲者备注>   （仅当 Notes 非空时输出这一行）

---

（下一张幻灯片……）
```

内容块按 `Type` 渲染：
- `bullet`：`strings.Repeat("  ", Level) + "- " + Text`（两个空格一级缩进，Markdown 列表通用约定）；
- `image`：`[IMAGE: <alt>]`，后跟空行；
- `table`：调用 `renderTable`，产出标准 GFM 表格（首行表头 + `---` 分隔行 + 数据行），单元格内容里的 `|` 转义为 `\|`；**全部单元格都是空白的表格整体丢弃**（`tableHasContent` 判定，PPT 里常见"占位表格框架"没有真实数据，渲染成空表格是纯噪音，不进 Markdown 正文）；
- 其余（`paragraph`）：原文 + 空行。

每张幻灯片渲染完追加 `\n---\n\n` 作为分隔线（幻灯片边界的视觉标记，同时便于后续按 `---` 做简单分段）。

**表格渲染细节**：列数固定取第一行的长度（`cols := len(rows[0])`），后续行若列数不一致，多出的列直接截断丢弃、不足的列留空——不做"整表按最大列数对齐"的归一化，与原实现一致，保持简单。

## 4. 后处理（`postprocess.go`）

`PostprocessText`：
1. `\r\n` 统一替换为 `\n`；
2. 每行去除行尾空白（`[ \t]+$`）；
3. 连续 2 行及以上的空白行折叠为 1 个空行（用一个 `blank` 计数器，第一次遇到空行输出一个空行占位，后续连续空行直接跳过，直到再遇到非空行才重置计数器）。

`Convert(input string) (string, error)`（`convert.go`）是整条链路的入口：`ExtractFile` → `ToMarkdown` → `PostprocessText`，三步顺序固定，返回最终 Markdown 字符串（无后缀换行以外的额外包装）。

## 5. 已知限制（原样保留，本次不扩展范围）

- 不提取图片二进制内容，只记录 alt 文本占位——不做图内文字 OCR；
- 表格不处理合并单元格（`gridSpan`/`rowSpan`/`vMerge`），会被拆成多个独立单元格；
- 不解析形状的样式/配色/动画/切换效果——纯文本内容抽取，PPT 作为"演示"媒介本身携带的视觉设计信息不在知识抽取范围内，与 Unit 抽取的定位一致（只关心正文语义，不关心排版）；
- 不支持 `.ppt`（旧二进制格式，OLE2 复合文档），与 `.doc`/`.xls` 同理，直接返回明确的 unsupported 错误，不静默失败；
- 不支持嵌入的 OLE 对象（如嵌入的 Excel 表格、SmartArt）——`graphicFrame` 只处理其中的 `tbl` 子元素，SmartArt（`dgm`）、嵌入对象（`oleObject`）等其他 `graphicData` 类型直接忽略，不产出任何 Block、也不报错。

## 6. 集成到 `internal/source/localconvert/`

不新建子包（不像 PDF 那样体量到需要 `pdfconv/` 子包），按 `docx.go`/`excel.go` 同级放一个 `pptx.go`，内部函数命名对齐现有风格（`convertPptxToMarkdown`），`client.go` 的 `ConvertToMarkdown` 分发新增 `.pptx` 分支：

```go
case ".pptx":
    return convertPptxToMarkdown(srcPath)
```

`.ppt`（旧格式）落入 `default` 分支的 unsupported 错误，与 `.doc`/`.xls` 同等处理，不单独写分支。

对外只暴露一个函数：

```go
func convertPptxToMarkdown(srcPath string) ([]byte, error) {
    md, err := pptx.Convert(srcPath) // 见下方"依赖引入方式"
    if err != nil {
        return nil, fmt.Errorf("localconvert: convert pptx: %w", err)
    }
    return []byte(md), nil
}
```

**依赖引入方式**：`pptx2md-go` 的 `internal/pptx` 包按 Go 惯例不可被外部模块直接 import（`internal/` 路径限制）。两种可行方式：
1. 把 `internal/pptx/*.go`（不含其 `_test.go`）复制进本仓库 `internal/source/localconvert/pptx/`（作为本项目自己的内部包，不是外部依赖），复制时按 MIT 协议要求保留原始 LICENSE 声明（在包内加一个 `LICENSE` 文件或在 `doc.go` 顶部注明来源与许可证）；
2. 联系上游把这几个文件 fork 到独立可导入仓库。

**建议采用方式 1**——代码量小（约 500 行，6 个文件）、无第三方依赖、MIT 协议对复制内联无限制，不需要为此维护一个 fork 仓库或引入 `replace` 指令。文件级复制之后按本仓库风格做的调整（如果有）应在对应文件顶部用一行注释注明"改动自 pptx2md-go（MIT）"，避免后续误以为是原创实现。

## 7. 测试

`pptx2md-go` 自带 `extract_test.go`/`render_test.go`/`convert_test.go`/`postprocess_test.go`/`relationships_test.go`（含 `bench_test.go`/`eval_test.go`/`eval_local_test.go` 是上游仓库自己的基准/评测脚本，非单元测试，复制时可以不带）——复制核心文件时一并带上对应的 `_test.go`（去掉与上游 CLI/评测相关的部分，只保留纯 `internal/pptx` 包内测试），作为回归的第一道防线。

在此基础上，比照 `docx.go`/`excel.go` 的验证方式，用 `internal/source/localconvert/testdata/` 下几份代表性真实 PPTX（含标题页、多级项目符号、图片、表格、演讲者备注）跑通并人工核对输出结构是否合理——**没有 FileView 参考输出可比对**（见第 0 节），验收标准是"结构清晰、无内容丢失、无 panic"，不是"与另一套实现的输出做 diff"。
