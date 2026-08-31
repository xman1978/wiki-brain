# Excel → Markdown 移植规格：`ExcelToMarkdown.java`

对应 `docs/impl/v1/local-file-convert.md` 第 4 节（Excel → JSON）的细化文档。方法与第 6 节 PDF 移植、`docs/impl/v1/docx-port/01-word-to-markdown.md` 一致：**逻辑到逻辑移植**，所有判据、阈值、常量原样照搬，只把 Aspose Cells 的 API 换成 excelize 的等价调用。本文档是后续写 Go 代码时的第一手依据，比 `local-file-convert.md` 第 4 节优先。

Java 源：`/Users/jxu/Code/fileview/src/main/java/com/fileview/convert/markdown/ExcelToMarkdown.java`（838 行，单文件、无跨文件依赖，不像 PDF/DOCX 移植那样需要拆分多个文档或复用共享包）。

## 1. 总体流程：`ExcelToMarkdown.convert(input, output)`

```text
1. 打开 Workbook。
2. 逐个 sheet（按索引遍历 wb.getWorksheets()）：
   若 cells.getMaxDataRow() < 0 且 cells.getMaxDataColumn() < 0（sheet 完全无数据），跳过该 sheet；
   否则调用 buildPivotJsonForSheet(sheet)（第 2 节），把结果加入 sheetRoots。
3. 若 sheetRoots 为空（全部 sheet 都是空的）：
   构造一个兜底根节点：table_name = 输入文件名，schema=[]，data=[]（没有 meta 字段）。
4. multiSheet = sheetRoots.size() > 1。
5. 逐个 root 拼接 Markdown：
   若不是第一个 root，先追加 "\n\n" 分隔。
   若 multiSheet，追加 "## Sheet: <table_name>\n\n" 标题。
   追加该 root 的 JSON（ObjectMapper 带缩进美化输出）包在 ```json ... ``` 围栏里。
   调用 buildRowLevelStatements(root)（第 9 节）；若返回非空，追加换行后再包一个 ```text ... ``` 围栏。
6. 写入 output（UTF-8）。
```

**空文件兜底根节点没有 `meta` 字段**——这与 `buildPivotJsonForSheet`/`buildTableJsonForSheet` 正常路径产出的根节点（都带 `meta`）不同，Go 移植要保留这个不对称，不要为了"统一"而给空文件兜底也加上 `meta`。

## 2. 单 sheet 主算法：`buildPivotJsonForSheet(sheet)`

这是整个模块的核心决策链，先给出总体判定顺序，再逐步骤展开细节：

```text
maxRow = cells.getMaxDataRow()；maxCol = cells.getMaxDataColumn()

若 maxRow < 0 或 maxCol < 0（sheet 无数据）：
  返回 { table_name, data: [], meta: { sheet, is_pivot: true } }   // 注意：无 schema 字段

① 构建二维矩阵 matrix[maxRow+1][maxCol+1]，每格取 safeCellString（第 10 节）
② 展开合并单元格：把每个合并区域左上角单元格的值，写入该区域覆盖到的所有格子（覆盖 matrix，不是"跳过空格取上级"，是直接物理覆盖）
③ headerRows = detectHeaderRowsWithStyle(cells, matrix, maxRow, maxCol)（第 3 节）
③.1 rowDimCols = detectRowDimensionColumns(matrix, headerRows, maxRow, maxCol)（第 4 节）；为空则退化为 [0]
    rowDimFieldNames = 对每个 rowDimCols 元素调用 detectHeaderFieldNameForColumn（第 4.1 节）
    valueStartCol = rowDimCols 最后一个元素 + 1

【退化判定 1】若 valueStartCol > maxCol（维度列已覆盖全部列）：
  → 返回 buildTableJsonForSheet(...)（第 6 节，普通表格模式）

③.2 constantHeaderLabels = detectConstantHeaderLabels(matrix, headerRows, valueStartCol, maxCol)（第 5 节）
    colDimFieldName = constantHeaderLabels 第 1 个元素的 value，否则 detectFirstNonEmptyHeaderCell(...)（第 5.1 节）
    measureFieldName = constantHeaderLabels 第 2 个元素的 value，否则 detectMostFrequentHeaderCell(..., exclude=colDimFieldName)（第 5.2 节）

【退化判定 2】若 headerRows <= 1 且 constantHeaderLabels 为空 且 !looksLikePivotSingleHeader(...)（第 7 节）：
  → 返回 buildTableJsonForSheet(...)

    profilesAll = profileColumns(matrix, headerRows, maxRow, maxCol)（第 8 节），统计 valueStartCol..maxCol 范围内
    mostlyNumeric = 该范围内 isMostlyNumeric() 为真的列数
    mostlyText = 该范围内 isMostlyText() 为真的列数

【退化判定 3】若 mostlyNumeric >= 1 且 mostlyText >= 1（value 区域同时有主要数字列和主要文本列）：
  → 返回 buildTableJsonForSheet(...)

【退化判定 4】若 !hasEnoughNumericValues(matrix, headerRows, maxRow, valueStartCol, maxCol)（第 9.1 节，value 区域数字不够多）：
  → 返回 buildTableJsonForSheet(...)

（以上四个退化判定全部不触发，才继续走 pivot 模式）

④ schema = [ {id,string}, 每个 rowDimFieldNames 各一个 {name,string}, {colDimFieldName,string}, {measureFieldName,number} ]

⑤ 构建列维度值 colDimensions：
   对 j 从 valueStartCol 到 maxCol：
     拼接 matrix[i][j]（i 从 0 到 headerRows-1，跳过 constantHeaderLabels 命中的行号），用 "_" 连接非空片段
     若拼接结果为空（排除常量标签行后啥也没剩），回退为 buildFallbackColDimension(matrix, headerRows, j)（第 5.3 节，不排除常量标签行地重新拼一次）

⑥ 构建 data（pivot → fact 展开）：
   lastDimVals[rowDimCols.size()]：多列行维度的"向下继承"状态
   对 i 从 headerRows 到 maxRow：
     对每个行维度列 idx：
       v = matrix[i][rowDimCols[idx]]
       若 v 非空：
         若 v != lastDimVals[idx]（上级维度发生变化）：清空 idx 之后所有下级的 lastDimVals（置 null）
         lastDimVals[idx] = v；dimVals[idx] = v
       否则：dimVals[idx] = lastDimVals[idx]（继承上一次的值）
     若 dimVals[0] 为空（第一维度既没有原值也没有继承值），跳过整行
     对 j 从 valueStartCol 到 maxCol：
       value = matrix[i][j]；为空则跳过（不产出该 fact）
       否则产出一条 data 记录：
         id = excelRowId(i)（第 10.3 节）
         每个行维度字段 = dimVals[idx]（null 时写空字符串）
         colDimFieldName 字段 = colDimensions[j - valueStartCol]
         measureFieldName 字段 = parseNumber(value)（第 10.2 节，数值类型）

⑦ meta = { source: "excel", sheet: sheet.name, is_pivot: true }
```

**关键边界，Go 移植不能遗漏**：
- 合并单元格展开是**物理覆盖 matrix**，发生在 header 行识别（第 3 节）之前——意味着 header 行识别看到的已经是展开后的矩阵，不是原始稀疏矩阵；
- 四个退化判定按**固定顺序**短路求值，一旦命中立即返回 table 模式，不再继续后面的判定；
- "至少第一个维度必须可得"（`dimVals[0]` 非空）是 data 生成阶段唯一的行级别过滤条件，其余维度列即使继承不到值也不影响该行是否产出（只会在该维度字段里写空字符串）；
- pivot 模式的 `data` 里，**每个非空的 value 单元格产出一条独立记录**（宽表转长表/融化），不是每行一条记录——这与 table 模式（第 6 节）"每行一条记录"是完全不同的展开方式，移植时不要混淆两种模式各自的 data 语义。

## 3. Header 行数识别

### 3.1 `detectHeaderRowsWithStyle(cells, matrix, maxRow, maxCol)`

```text
contentGuess = detectHeaderRows(matrix)              // 第 3.2 节，纯内容启发式
styleGuess = detectHeaderRowsByFontStyle(cells, maxRow, maxCol)   // 第 3.3 节，字体样式启发式
guess = max(contentGuess, styleGuess)
返回 clamp(guess, 1, 10)                              // 至少 1 行，至多 10 行
```

「尽量信任样式（多行表头更常见），但防止异常值」——取两者较大值而不是样式优先/内容优先的单一来源，再统一钳制到 `[1,10]`。

### 3.2 `detectHeaderRows(matrix)`（纯内容启发式，注释标注"可扩展为 LLM"，暂未替换）

```text
rows = min(3, matrix行数)                            // 只看前 3 行
对每行 i（0..rows-1）：
  统计该行非空格子数 total，其中非数字格子数 textCount（isNumeric 判断见第 10.1 节）
  若 total > 0 且 textCount/total > 0.6：返回 i+1     // 一旦某行"以文本为主"比例超过 60%，就认为 header 到此行（含）为止
返回 1                                                // 前 3 行都不满足，兜底 1 行
```

### 3.3 `detectHeaderRowsByFontStyle(cells, maxRow, maxCol)`

```text
若 maxRow<0 或 maxCol<0：返回 1

scanTop = min(10, maxRow+1)                           // 顶部扫描窗口最多 10 行
bodyStart = min(max(2, scanTop), max(2, maxRow/10))   // 正文采样起始行
bodyEnd = min(maxRow, bodyStart + 20)                 // 正文采样窗口最多 21 行

采样 bodyStart..bodyEnd 范围内所有非空格子的 FontSig（第 3.4 节），统计频次 bodyFreq，
bodyNonEmpty = 采样到的非空格子总数。
bodySig = bodyFreq 中出现频次最高的 FontSig（"正文最常见字体签名"）。

若 bodySig 为空 或 bodyNonEmpty < 3（采样不足）：返回 1

从第 0 行扫到 scanTop-1 行：
  对每行：nonEmpty / styleDiff / numeric 三个计数器
  对该行每个非空格子：
    nonEmpty++
    若数字（isNumeric，逗号先去掉）：numeric++
    取该格子 FontSig sig；若 sig != bodySig 或 sig.bold 或 sig.size > bodySig.size：styleDiff++
  若该行 nonEmpty==0：跳过（空行不计入 header，也不终止扫描，继续看下一行）
  diffRatio = styleDiff/nonEmpty；numericRatio = numeric/nonEmpty
  looksHeader = diffRatio >= 0.55 且 numericRatio <= 0.5
  若 looksHeader：headerRows = 当前行号+1；continue（继续扫描下一行，允许多行表头）
  否则：若 headerRows > 0（已经识别到过表头），立即 break（遇到明显正文行则停止）
        （若 headerRows 仍是 0，即还没识别到任何表头行，则不 break，继续扫描——注意这是隐含在"否则"分支里没有显式 else 的行为：循环体只在 `headerRows>0` 时才 break，`headerRows==0` 时该行判负后循环自然进入下一次迭代）

返回 headerRows<=0 ? 1 : headerRows
```

### 3.4 `FontSig` 与 `fontSig(cell)`

```text
FontSig = (name string, size int, bold bool)          // Java record，Go 用可比较的 struct（字段都是值类型，可直接用作 map key）
fontSig(cell):
  cell 为空：返回 ("", 0, false)
  取 cell.getStyle().getFont()：name（空则 ""）、size 四舍五入取整、bold
  任何异常：返回 ("", 0, false)
```

`size` 用 `Math.round` 取整到整数——不是浮点比较，两个字号相差不到 0.5pt 会被视为同一字号，Go 移植同样要四舍五入再比较/入 map key，不能直接用 `float64` 做 map key（否则微小的浮点表示误差会让本该视为相同字号的单元格被判定为不同签名）。

## 4. 行维度列探测

### 4.1 `detectRowDimensionColumns(matrix, headerRows, maxRow, maxCol)`

```text
startRow = max(0, headerRows)
dims = []
对 c 从 0 到 maxCol：
  统计该列 startRow..maxRow 范围内非空格子数 nonEmpty、非数字格子数 nonNumeric
  若 nonEmpty >= 3 且 nonNumeric >= max(2, nonEmpty*0.6)：dims.append(c)   // 该列判定为维度列
  否则若 dims 非空：break                                                 // 已经找到过维度列，遇到非维度列即停止（维度列必须是从第 0 列开始的连续前缀）
返回 dims
```

**维度列必须是连续的、从第 0 列开始的前缀**——一旦中间出现不满足条件的列就停止扫描（哪怕后面的列又重新满足条件也不会被纳入）。调用方（第 2 节 ③.1）对空结果的兜底是 `[0]`（强制把第 0 列当作行维度）。

### 4.2 `detectHeaderFieldNameForColumn(matrix, headerRows, col)`

```text
limit = min(headerRows, matrix行数)
先倒序扫（i 从 limit-1 到 0）：找第一个非空且非数字的格子，命中即返回其值   // 优先取"最靠近数据区"的表头行
若倒序没找到，再正序扫（i 从 0 到 limit-1）：同样条件，命中返回
都没找到：返回 "dim_" + col
```

## 5. 列维度字段名 / 度量字段名探测

### 5.1 `detectConstantHeaderLabels(matrix, headerRows, fromCol, toCol)`

```text
rows = min(headerRows, matrix行数)；rows<=0 返回空列表
对每行 i（0..rows-1）：
  统计该行 fromCol..toCol 范围内非空、非数字格子的值频次 freq，candidates = 候选格子总数
  若 candidates<=0：跳过该行
  best = freq 中频次最高的 (value, count)
  ratio = best.count / candidates
  若 ratio >= 0.8 且 best.count >= 2：把 (i, best.value) 加入结果列表   // "高重复"才算常量标签行
返回结果列表（按行号顺序，可能有 0~多行命中）
```

「典型用途：排除把"季度/销售额"标签拼进维度取值里」——即某个 header 行里绝大多数格子都写着同一个字符串（如整行都是"销售额"），这更像是"这一整行都是同一个度量的标签"，不是"每列各自的维度取值"。

### 5.2 `detectFirstNonEmptyHeaderCell` / `detectMostFrequentHeaderCell`

```text
detectFirstNonEmptyHeaderCell(matrix, headerRows, fromCol, toCol):
  limit = min(headerRows, matrix行数)
  按行优先（i 外层、j 内层）扫描 0..limit-1 行、fromCol..toCol 列，
  找到第一个非空且非数字的格子即返回其值；找不到返回 "col_dim"

detectMostFrequentHeaderCell(matrix, headerRows, fromCol, toCol, exclude1):
  rows = min(headerRows, matrix行数)
  统计 0..rows-1 行、fromCol..toCol 列范围内非空、非数字、且值不等于 exclude1 的格子值频次 freq，candidates = 候选总数
  若 candidates<=0 或 freq 为空：返回 "value"
  best = freq 中频次最高的 (value, count)
  ratio = best.count / candidates
  返回 ratio >= 0.3 ? best.value : "value"      // 阈值比常量标签行的 0.8 低得多——这里只要"相对多数"即可，不要求绝对主导
```

`colDimFieldName` 优先取 `constantHeaderLabels` 第 1 条（如果检测到常量标签行），否则退回 `detectFirstNonEmptyHeaderCell`；`measureFieldName` 优先取 `constantHeaderLabels` 第 2 条，否则调用 `detectMostFrequentHeaderCell` 且传入 `colDimFieldName` 作为排除项（避免度量名和维度名撞成同一个值）。**两者的"否则"分支用的是完全不同的两套探测策略**（"第一个非空格"vs"出现频率最高的格"），不要在 Go 移植时图省事合并成一套。

### 5.3 `buildFallbackColDimension(matrix, headerRows, col)`

```text
rows = min(headerRows, matrix行数)
拼接该列 0..rows-1 行所有非空格子值，用 "_" 连接（不跳过任何行，包括常量标签行）
拼接结果非空则返回，否则返回 "col_" + col
```

与第 2 节步骤⑤的主路径拼接逻辑几乎一样，唯一区别是**不排除常量标签行**——只有当主路径排除常量标签行后拼接结果为空时才会走到这个兜底，此时"宁可带上标签也不要空值"。

## 6. 普通表格模式：`buildTableJsonForSheet(...)`

四个退化判定命中时的输出路径，产出「每行一条 record」的语义（与 pivot 模式的「每个非空单元格一条 fact」语义完全不同）：

```text
headers = detectTableHeaders(matrix, headerRows, maxCol)（第 6.1 节）
profiles = profileColumns(matrix, headerRows, maxRow, maxCol)（第 8 节）
carryForwardCols = detectCarryForwardColumns(profiles)（第 8.1 节）：每列是否需要"向下继承"

schema = [ {id,string}, 每列一个 {headers[c], profiles[c].isMostlyNumeric() ? "number" : "string"} ]

lastVals[maxCol+1]：多级行表头/分组场景的继承状态（对所有列统一维护，但只在 carryForwardCols[c] 为真时才生效）

对 r 从 headerRows 到 maxRow：
  row = { id: excelRowId(r) }
  anyNonEmpty = false
  对 c 从 0 到 maxCol：
    v = matrix[r][c]
    若 carryForwardCols[c]：
      若 v 非空：
        若 v != lastVals[c]（父维度变化）：把 c 之后**同样是 carryForwardCols 的列**的 lastVals 清空（数值列不受影响，因为它们本来就不参与继承）
        lastVals[c] = v
      否则：v = lastVals[c]（继承）
    outVal = v 或 ""
    若 outVal 非空：anyNonEmpty = true
    若 profiles[c].isMostlyNumeric()：
      outVal 为空则该字段写 JSON null（putNull），否则写 parseNumber(outVal) 数值
    否则：该字段写 outVal 字符串（含空字符串，不写 null）
  若 anyNonEmpty（该行至少有一个字段非空）：加入 data；否则整行丢弃（不产出全空记录）

meta = { source: "excel", sheet: <table_name>, mode: "table", header_rows: headerRows }
```

**数值列的空值语义与文本列不同**：数值列缺失值写 JSON `null`（区别于 `0`，避免下游把"没填"误读成"填了 0"）；文本列缺失值写空字符串 `""`。**只有维度列（`isMostlyText`）参与向下继承**，数值列即使连续为空也不继承——这与 pivot 模式（第 2 节步骤⑥）"只对行维度列继承"的原则是一致的，但 table 模式这里的继承范围判据用的是 `profiles[c].isMostlyText()`（`detectCarryForwardColumns`，第 8.1 节），而 pivot 模式继承的是显式的 `rowDimCols` 列表——两处继承虽然思路相同，**判据来源不同**，Go 移植不要试图合并成一份共用函数。

### 6.1 `detectTableHeaders(matrix, headerRows, maxCol)`

```text
rows = min(headerRows, matrix行数)
对每列 c（0..maxCol）：
  拼接 0..rows-1 行该列所有非空格子值，用 "_" 连接
  拼接结果非空则作为列名，否则用 "col_" + c
去重：同名列（在拼接结果或兜底名相同的情况下）第 2 次起出现的追加 "_2"、"_3"...后缀（用一个 name→出现次数 的 map 顺序处理，第一次出现不加后缀）
```

## 7. `looksLikePivotSingleHeader(matrix, headerRows, maxRow, fromCol, toCol)`

只在 `headerRows == 1` 时会被调用（第 2 节退化判定 2 的条件之一）：

```text
若 headerRows != 1：返回 false
valueCols = max(0, toCol - fromCol + 1)
若 valueCols < 3（value 区域列数太少）：返回 false

统计 fromCol..toCol 范围内第 0 行（唯一的 header 行）的非空表头数 nonEmptyHeaders，
其中"看起来像类别"的表头数 categoryLike（isCategoryLikeHeader，见下）
若 nonEmptyHeaders == 0：返回 false

hasNumbers = hasEnoughNumericValues(matrix, headerRows, maxRow, fromCol, toCol)（第 9.1 节）

返回 hasNumbers 且 categoryLike >= max(2, nonEmptyHeaders/2)   // 类别型表头至少占一半
```

`isCategoryLikeHeader(h)`：`h.trim()` 后满足以下任一条件即算类别型表头：
```text
^(?i)q\d+$          （Q1/q1/Q12 等，大小写不敏感）
^\d{1,2}月$          （1月..12月，含个位不补零和两位）
^\d{4}年$            （2024年）
^\d+$               （纯数字，如season序号 1/2/3/4）
包含子串 "季度"
包含子串 "月"
包含子串 "年"
```

**注意最后三条是子串包含判断**，不是整串匹配——"第一季度"、"1月销售额"这类表头也会被判定为类别型（只要含"季度"/"月"/"年"这几个字），Go 移植用 `strings.Contains` 而非正则整串匹配。

## 8. 列画像与向下继承判定

### 8.1 `ColumnProfile` / `profileColumns` / `isMostlyNumeric` / `isMostlyText`

```text
ColumnProfile = (nonEmpty int, numeric int, nonNumeric int)

profileColumns(matrix, headerRows, maxRow, maxCol):
  对每列 c（0..maxCol）：
    扫描 headerRows..maxRow 行（含边界检查，行数组越界时提前 break 该列的扫描，不是 continue——即一旦某一行的列数不够，后续更靠下的行也不再检查该列，这是原始 Java `if (c >= matrix[r].length) break;` 的行为，不是 `continue`）：
      非空格子：nonEmpty++；数字（isNumeric，先去掉逗号）则 numeric++，否则 nonNumeric++
  返回每列的 ColumnProfile

isMostlyNumeric(): nonEmpty==0 则 false；否则 numeric >= max(2, nonEmpty*0.6)
isMostlyText():    nonEmpty==0 则 false；否则 nonNumeric >= max(2, nonEmpty*0.6)
```

**`isMostlyNumeric` 和 `isMostlyText` 可以同时为 false**（例如某列恰好一半数字一半文本，两个阈值都够不到 60%），这种"暧昧列"在 pivot 判定链的退化判定 3 里不会被计入 `mostlyNumeric` 也不会计入 `mostlyText`，因此不会触发那条判定——移植时要保证这是两个独立的布尔判断，不是互斥的三态分类。

### 8.2 `detectCarryForwardColumns(profiles)`

```text
对每列：carry[c] = profiles[c].isMostlyText()
```

单行代码级的简单委托，直接照搬。

## 9. 数值判定与行级语句

### 9.1 `hasEnoughNumericValues(matrix, headerRows, maxRow, fromCol, toCol)`

```text
统计 headerRows..maxRow 行、max(0,fromCol)..toCol 列范围内的 numeric / nonEmpty 计数（isNumeric，先去逗号）
若 nonEmpty == 0：返回 false（没有任何数据不算 pivot）
返回 numeric >= 3 且 (numeric/nonEmpty) >= 0.2      // 绝对数量和占比双重门槛，缺一不可
```

## 10. 基础工具函数

### 10.1 `isNumeric(str)`

```text
正则整串匹配：^-?\d+(\.\d+)?$
```

**不识别科学计数法、千分位逗号、百分号**——调用方在传入前会先 `.replace(",", "")` 去掉千分位逗号（第 3.3/8.1/9.1 节多处调用点都是 `isNumeric(v.replace(",", ""))`），但 `detectHeaderFieldNameForColumn`/`detectRowDimensionColumns`/`detectFirstNonEmptyHeaderCell`/`detectMostFrequentHeaderCell`/`detectConstantHeaderLabels` 等判断"表头/维度列是否为数字"的调用点**直接传原始值、不去逗号**（见对应函数里的 `isNumeric(v)` 而非 `isNumeric(v.replace(",", ""))`）——这个不一致是 Java 原始代码的既有行为，移植时按原样保留每个调用点各自的处理方式，不要统一成"总是先去逗号"或"总是不去逗号"。

### 10.2 `parseNumber(val)`

```text
val.replace(",", "") 后 Double.parseDouble；解析失败（含 val 本身就不是数字格式）返回 0
```

**解析失败静默返回 0，不是返回 null/报错**——这只在 pivot 模式的 measure 字段赋值时用到（第 2 节步骤⑥），且调用前该单元格已经通过了"非空"检查，正常情况下不会失败；但如果 value 区域被误判为数值型（实际是文本），这里会把文本值静默转成 0，Go 移植保留这个行为（不主动加校验拦截），这也是第 2 节退化判定 3（"混合数字/文本列强制走 table 模式"）要提前拦截的原因——两处配合才能避免文本被静默清零。

### 10.3 `excelRowId(zeroBasedRowIndex)`

```text
excelRow = zeroBasedRowIndex + 1；若 < 1 则钳制为 1
excelRow <= 9999：返回 "row_" + 4 位补零（如 row_0001）
否则：返回 "row_" + excelRow（不补零，如 row_10000）
```

### 10.4 `safeCellString(cells, row, col)`

```text
try { return cells.get(row,col).getStringValue().trim() } catch (任何异常) { return "" }
```

**任何读取异常（越界、类型转换失败等）一律吞掉返回空字符串**，不向上抛出——这是 Go 移植时 excelize 版本对应函数需要保持的容错行为（`excelize.GetCellValue` 返回 `(string, error)`，Go 版本应在 error 非 nil 时同样返回空字符串，不 panic、不向上传播）。

### 10.5 `createField(name, type)`

```text
{ "name": name, "type": type }   // type 取值只有 "string" / "number" 两种，没有第三种
```

## 11. 行级语句：`buildRowLevelStatements(root)`

```text
tableName = root.table_name（缺失则 ""）
data = root.data；若不是数组或长度为 0：返回空字符串
对 data 每个元素（保序）：
  rowId = 该元素的 id 字段（缺失则 ""）
  dataJson = 该元素完整序列化为紧凑（非美化）JSON 字符串
  一行：`id=<rowId> | 表=<tableName> | data=<dataJson>`
行与行之间用 "\n" 连接（首行前不加，行间加），返回整体字符串
```

调用方（第 1 节步骤 5）只在返回值非空时才追加 `` ```text ``` `` 围栏块；`data` 为空数组（如 sheet 有表头无数据行，或全空 sheet 的兜底根节点）时不追加该围栏块。

## 12. 合并单元格展开细节

第 2 节步骤②依赖 Aspose 的 `cells.getMergedCells()`，返回值元素类型是 `CellArea`（含 `StartRow`/`StartColumn`/`EndRow`/`EndColumn` 四个字段，均为闭区间）：

```text
对每个合并区域 area：
  value = safeCellString(cells, area.StartRow, area.StartColumn)   // 取左上角格子的值
  对 area 覆盖的每个 (i,j)（i: StartRow..EndRow，j: StartColumn..EndColumn）：
    若 (i,j) 在 matrix 边界内：matrix[i][j] = value                 // 物理覆盖，包括左上角格子自己（重新赋一次自己的值，无副作用）
```

Java 源码里有一处运行时类型校验（`if (!(obj instanceof CellArea area)) continue;`），注释说明"Aspose 的 `getMergedCells()` 在不同版本/泛型标注上可能是原生类型，这里做一次运行时校验，避免编译器推断为 Object"——这是 Java 泛型擦除相关的防御性写法，**Go 移植不需要对应这一步**（excelize 的 `GetMergeCells` 直接返回强类型的 `[]MergeCell`，不存在这个问题），但要保留"合并区域按左上角格子的值整体展开覆盖"这条核心语义。

## 13. Aspose Cells → excelize API 映射与核实清单

延续 `local-file-convert.md` 第 4.1 节已给出的映射表，本节补充需要在开工前逐项验证的细节（同 `docx-port/01-word-to-markdown.md` 第 10 节的处理方式——不能假设第三方库的 API 形状与预期完全一致就直接开始移植）：

| 需要读到的信息 | Aspose Cells API | excelize 对应 | 核实要点 |
| --- | --- | --- | --- |
| 表格数据范围 | `Cells.getMaxDataRow()/getMaxDataColumn()` | `GetRows(sheet)` 取行数，逐行 `len()` 取列数 | excelize 没有直接等价的"最大数据行/列"概念，需要**在读取全部行后自行计算**最大行号（`len(rows)-1`）和最大列号（各行长度的最大值 -1）——注意 `GetRows` 对每行返回的切片长度可能因该行末尾的空单元格被截断而各不相同，逐行取 `len()` 后取最大值即为 `maxCol`，这与 Aspose "有数据的最大列"语义是否完全等价需要用真实文件核实（尤其"某列只有中间某几行有值、首尾行都空"这种情况） |
| 单元格字符串值 | `cells.get(row,col).getStringValue()` | `f.GetCellValue(sheet, cellRef)`（`excelize.CoordinatesToCellName` 拼 cellRef，注意 excelize 坐标从 1 开始，Aspose 从 0 开始，转换时要 `+1`） | `GetCellValue` 对数值/日期格式的单元格返回的是**格式化后的显示字符串**还是**原始值**，需要核实是否与 `getStringValue()` 语义一致（尤其日期、百分比、货币格式的单元格，直接影响 `isNumeric` 判断是否命中） |
| 合并单元格 | `cells.getMergedCells()` → `CellArea` | `f.GetMergeCells(sheet)` → `[]MergeCell`，`GetStartAxis()/GetEndAxis()` 返回单元格引用字符串（如 "A1"），需要再用 `excelize.CellNameToCoordinates` 转回行列号 | 已在 `local-file-convert.md` 第 4.1 节列出，本文档确认其 API 存在（`GetMergeCells`/`GetStartAxis`/`GetEndAxis` 均已核实存在于 excelize v2），**未核实**的是 `GetMergeCells` 返回的区域是否包含"值"本身（`MergeCell.GetCellValue()` 存在，但第 12 节的展开算法需要的是左上角格子在 `matrix` 里已经读到的值，不一定需要用这个方法，需在实现时确认用哪个更可靠） |
| 单元格字体（判断 header 行样式差异） | `cell.getStyle().getFont()`（Name/Size/Bold） | `f.GetCellStyle(sheet, cellRef)` → styleID，`f.GetStyle(styleID)` → `Style.Font.{Family,Size,Bold}` | `GetCellStyle`/`GetStyle` 的方法签名已核实存在（`GetCellStyle(sheet, cell string) (int, error)`、`GetStyle(idx int) (*Style, error)`），但 **`Style` 结构体是否确有 `Font` 字段、`Font` 是否确有 `Bold bool`/`Size float64` 子字段未能从文档确认，必须用真实代码验证**——若字段名不同或需要额外一层解引用，第 3.4 节的 `FontSig` 提取逻辑要相应调整；此外空格子/无样式格子调用 `GetCellStyle` 是否报错也需确认（对应 Java 版本 `fontSig` 里 `try/catch` 兜底为空 `FontSig` 的行为，Go 版本对 `GetCellStyle`/`GetStyle` 返回 error 时同样要兜底返回零值 `FontSig`，不 panic） |
| 遍历 sheet | `wb.getWorksheets().getCount()` / 逐个 `Worksheet` | `f.GetSheetList()` 遍历 sheet 名 | 已核实存在，注意 excelize 是按 sheet **名字**遍历，不是按索引——若原 Java 逻辑依赖 sheet 的物理顺序（`wb.getWorksheets().get(i)` 是按索引），需确认 `GetSheetList()` 返回顺序是否等价于工作簿内 sheet 的物理顺序（一般是，但建议用一份多 sheet 真实文件验证一次，不要假设） |

## 14. Go 包结构建议

延续 `local-file-convert.md` 第 2 节的包结构，`excel.go` 单文件即可覆盖（Java 源本身也是单文件、无跨文件依赖），函数命名对照见 `local-file-convert.md` 第 4.2 节已给出的表格，不在本文档重复列出。**若单文件过长不便维护**，可选按本文档的分节方式拆成：

```text
internal/source/localconvert/
    excel.go              // 入口 convertExcelToMarkdown、buildPivotJsonForSheet 主决策链（第 1、2 节）
    excel_header.go         // header 行识别、行/列维度探测（第 3、4、5、7 节）
    excel_table.go           // 普通表格模式、列画像（第 6、8 节）
    excel_util.go             // 数值判定、行级语句、id/字段构造等基础工具（第 9、10、11、12 节）
```

是否拆分、拆分方式，不影响函数级别的移植正确性，留给实现阶段按代码量决定。

## 15. 已知限制（与 `local-file-convert.md` 第 3 节一致，不重复展开）

- 仅支持 `.xlsx`（excelize 覆盖 OOXML 格式），`.xls`（旧二进制格式）不支持，直接报错；
- header 行识别（第 3 节）纯启发式，Java 源注释本身标注"可扩展为 LLM"但当前未接入模型判断，Go 移植同样不接入；
- `isNumeric` 不识别科学计数法/百分号/负数带括号（会计格式）等 Excel 常见数字表示，只识别 `-?\d+(\.\d+)?`——这是 FileView 现有实现的固有局限，移植时不做超出原始判据范围的"改进"（除非另有用户确认，参照项目 CLAUDE.md「修 bug 先确认根因、不擅自扩展判据」的要求）。

## 16. 测试与验证策略

参照 `local-file-convert.md` 第 4.4 节：

1. 用 `internal/source/localconvert/testdata/` 下的样例 Excel（可从 `/Users/jxu/Code/fileview` 的测试资源里挑几个代表性文件复制过来）逐个跑，对照 FileView 实际转换结果做 **JSON 结构级 diff**（不要求 key 顺序一致，要求 `table_name`/`schema`/`data`/`meta.mode`/`meta.is_pivot` 语义一致）。
2. 覆盖场景至少包含：单行表头宽表（走 table 模式）、多级表头 pivot（如"季度/销售额"类）、含合并单元格（横向/纵向/多行多列区域）、全文本表（无数值列，走 table 模式）、混合数字/文本列（强制走 table 模式）、多 sheet（含空 sheet 应跳过）、单 sheet 但全空（走兜底根节点）、header 行需要靠字体样式而非内容比例判断的文档（验证第 3.3 节字体签名逻辑）。
3. 第 13 节标注的"未核实"项（`GetCellValue` 对格式化单元格的返回值、`Style.Font` 字段形状、`GetMergeCells` 顺序/取值方式、sheet 遍历顺序）需要在写正式测试前先用最小样例文件验证，作为决定"照搬移植"还是"需要绕过实现"的依据。

## 17. 完成标准

```text
配置切换（回归项，与 local-file-convert.md 第 8 节一致）：
  fileview.mode 缺省/为空/为 "remote" -> 行为与改动前完全一致；
  fileview.mode: "local" -> Service 使用 LocalConvertClient，xls/xlsx 走本文档描述的转换路径。

Header 行识别：
  内容比例判据（前 3 行文本占比 > 60%）与字体样式判据（顶部行样式差异 diffRatio>=0.55 且 numericRatio<=0.5）
    取两者较大值，且结果被钳制在 [1,10]。

Pivot / Table 模式判定（四个退化判定按固定顺序短路）：
  维度列覆盖全部列 -> table 模式；
  单行表头且无常量标签行且不满足"类别型表头"启发式 -> table 模式；
  value 区域同时有主要数字列和主要文本列 -> table 模式；
  value 区域数字不够多（<3 个 或 占比<0.2） -> table 模式；
  以上均不命中 -> pivot 模式，每个非空单元格产出一条 fact 记录（宽表转长表语义）。

行/列维度：
  行维度列必须是从第 0 列开始的连续前缀，非维度列出现即停止扫描；
  多级行维度向下继承：上级维度变化时清空所有下级继承值，不跨组串值；
  常量标签行（高重复文本，ratio>=0.8 且出现次数>=2）从列维度取值拼接中排除，排除后为空则回退不排除的拼接结果。

普通表格模式：
  数值列缺失写 JSON null，文本列缺失写空字符串，不混淆；
  只有"主要为文本"的列参与向下继承，数值列不继承；
  全空行不产出记录；
  同名列自动去重加后缀。

输出格式：
  多 sheet 时正确插入 "## Sheet: <name>" 标题，单 sheet 不插入；
  每个 sheet 的 JSON 围栏块之后，data 非空则追加行级语句 text 围栏块，data 为空则不追加；
  空文件兜底根节点只有 table_name/schema/data 三个字段，没有 meta。

跨格式（与 local-file-convert.md 第 8 节一致）：
  ConvertToMarkdown 遇到 .xls（旧格式）-> 返回明确 unsupported 错误，不静默产出空/错误内容。
```
