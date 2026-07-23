# Lifecycle 实现路径（V1）

## 职责

为 KnowledgeUnit / KnowledgePoint 增加生命周期状态，使过期或已被替代的知识不再进入召回和回答；定义 Source 更新与删除时的状态传导规则。V1 实现 `lifecycle.md`（设计文档）的完整 3 状态模型——不是子集，current / superseded / deprecated 已经覆盖知识生命周期的全部场景（见设计文档第 2 节的场景推导：candidate / needs_verification / conflicted / historical / retracted 均无独立于这 3 种状态的必要场景，不引入）。

本模块是 V1 所有能力的前置：ActivationLink、检索激活层、Wiki 编译都必须感知知识状态。

## 状态定义

```text
current      默认状态，参与召回、激活与回答
superseded   已被新版本替代，不参与召回，保留追溯
deprecated   来源已删除，不参与召回，保留追溯
```

状态只有 `current` 参与召回。其余状态的 KU/KP 保留在 SQLite 中（不物理删除），供 Trace 反查和历史回答的 evidence_snapshot 追溯。

## 数据结构

新增 migration（版本号顺延）：

```sql
ALTER TABLE knowledge_units  ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'current';
ALTER TABLE knowledge_points ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'current';
ALTER TABLE knowledge_units  ADD COLUMN lifecycle_changed_at DATETIME;
ALTER TABLE knowledge_points ADD COLUMN lifecycle_changed_at DATETIME;

CREATE INDEX idx_ku_lifecycle ON knowledge_units(lifecycle);
CREATE INDEX idx_kp_lifecycle ON knowledge_points(lifecycle);
```

存量数据经 DEFAULT 自动获得 `current`，无需回填脚本。

### Shadow Source（reupload 专用，仅 1 列扩展）

```sql
ALTER TABLE sources ADD COLUMN shadow_of TEXT REFERENCES sources(source_id);
CREATE INDEX idx_sources_shadow_of ON sources(shadow_of);
```

`shadow_of` 非空表示这是一条为 reupload 而创建的影子 Source，用于在新内容完全处理成功之前不影响、不暴露被替换的原 Source。字段含义与用途见步骤 2。

## 实现步骤

### 步骤 1：状态传导规则（KU → KP）

KP 的 lifecycle 始终跟随所属 KU，不独立变更：

```text
UPDATE knowledge_points SET lifecycle = ?, lifecycle_changed_at = now()
WHERE unit_id IN (受影响的 unit_id 列表)
```

所有状态变更通过统一的内部接口执行（`SetUnitLifecycle(unitIDs []string, state string)`），该接口负责：更新 KU 行、级联更新 KP 行、同步 Bleve（步骤 3）、记录 info 日志（含变更原因）。禁止业务代码直接 UPDATE lifecycle 字段，保证传导不遗漏。

### 步骤 2：触发来源

**Source 重新上传**（V1 新增 API，基于 Shadow Source）：

新内容完全在一个隐藏的"影子 Source"里走完整套普通导入流程（不改动 source_process / unit_extract 一行代码），只有影子处理全部成功后才做一次性"换血"。处理期间被替换的原 Source 完全不受影响，不存在旧内容失效、新内容还没就绪的空窗期，也不需要任何回滚逻辑——失败时直接丢弃影子即可。

```text
POST /sources/:id/reupload
  multipart 上传新文件；
  处理：
    1. 创建影子 Source：source_id 全新生成，shadow_of = :id；
       复用 Import() 的完整流程，但跳过 ExistsByFileName 对 :id 自身的
       文件名比对（reupload 允许新文件与原文件同名，这是预期场景，
       不当作重复文件拒绝；与其他 Source 的同名检查仍然生效）；
    2. 影子 Source 走完全正常的 source_process → unit_extract 链路，
       与普通导入没有任何区别：
         - source_process 失败 → 影子的 sources.status=failed，
           不影响 :id 本身，:id 下的 KU/KP 仍为 current 正常参与检索；
         - unit_extract 的抽取、语义或发布失败 → 影子的 units_status=failed，
           不执行换血；原 :id 下的 KU/KP 保持 current；
    3. 影子的 unit_extract 整体完成后（KPN 生成、Concept 匹配也走完），
       执行一次性"换血"事务：
         a. 将影子的 knowledge_units / knowledge_points / source_outlines
            批量 UPDATE source_id 从影子 ID 改为 :id（重新挂靠到原 Source）；
         b. 对 :id 下换血前**仍为 current** 的 KU（即真正被替代的旧版本）
            调用 SetUnitLifecycle 标记为 superseded；已经是
            superseded/deprecated 的历史版本不再重复调用——避免刷新其
            lifecycle_changed_at，破坏「归档 Markdown 回链」按
            lifecycle_changed_at 对齐 source_versions.archived_at 的匹配
            （见 docs/superpowers/specs/2026-07-22-historical-evidence-backlink-design.md）；
         c. 原始文件与规范化 Markdown 按 :id 覆盖写入，旧文件移动到
            data/sources/archived/<:id>/<timestamp>/ 保留追溯；
            sources.file_name 更新为新文件名（reupload 允许改文件名，
            因为换血后旧文件名对应的行已经不存在，不会破坏全局唯一性）；
         d. 删除影子 Source 行（其子行已在 a 步重新挂靠，不再需要影子本身）。
       该事务失败时整体回滚，:id 状态不受影响（等同于换血未发生过）。
  响应：{ source_id: ":id", shadow_source_id: "...", status: "processing" }
  （处理过程中查询 GET /sources/:id 应仍看到旧 KU/KP 为 current、可正常召回；
   换血完成后 GET /sources/:id 返回新内容，旧 KU/KP 转为 superseded）

POST /sources/:id/reupload/retry
  当影子的 source_process 失败（status=failed）时，复用既有
  POST /sources/:shadow_id/retry 续跑 source_process；
  当 source_process 已完成但 unit_extract 失败（status=completed、
  units_status=failed）时，只重新入队 unit_extract，不重复格式转换、
  大纲和摘要处理；客户端不需要知道影子 Source 的内部 ID；
  重试成功、影子 unit_extract 完成后同样触发第 3 步换血。
  若改用新文件重新发起 POST /sources/:id/reupload（而不是调用本接口重试
  同一次尝试），视为放弃当前失败的影子，丢弃后按上文步骤 1 重新创建。
```

影子 Source 对外不可见：`GET /sources` 列表、Domain 预过滤、Source 语义过滤（`mvp/retrieval.md` 步骤 2-3，由 `v1/retrieval.md` 步骤 5 追加过滤条件）均排除 `shadow_of IS NOT NULL` 的行，避免它在换血完成前被当成一个独立的知识来源参与检索。

**Source 删除**（V1 新增 API，软删除）：

```text
DELETE /sources/:id
  处理：
    1. 该 source 全部 KU/KP（含历史上已经是 superseded 的记录）
       标记 deprecated——删除是终态，覆盖掉更早的"被替代"原因；
       两者都不参与召回，覆盖不影响检索行为，只是原因标签统一为
       "来源已删除"；
    2. sources.status 置为 'deleted'（status 枚举扩展，migration 同版本加入）；
    3. 不删除 SQLite 行、不删除 Markdown 文件（answers.evidence_snapshot 反查需要）；
    4. outlines index 中该 source 的节点删除（不再参与目录召回）。
  响应：{ source_id, deprecated_units: N }
```

### 步骤 3：Bleve 索引同步过滤

units / points 索引写入字段增加 `lifecycle`（keyword，不分词）：

```text
写入：Unit 提取入库时写 lifecycle=current；
变更：SetUnitLifecycle 对受影响的 unit_id / point_id 重新 Index（覆盖旧文档）；
查询：Retrieval 的所有 Bleve 查询（units / points / outlines 路径）追加
      TermQuery(lifecycle=current) 与业务查询做 conjunction；
      outlines index 不加 lifecycle 字段，通过删除节点实现过滤（见步骤 2）。
```

SQLite 侧的直接查询（KPN 扩展、代表 KP 反查等）同样追加 `lifecycle = 'current'` 条件。检索链路的具体改动点清单见 `retrieval.md`（V1）步骤 5。

### 步骤 4：向 ActivationLink 与 Wiki 的传导

lifecycle 变更不直接修改 activation_links 和 wiki_pages，通过两条既有机制间接生效：

```text
检索时过滤：激活层召回 JOIN knowledge_points 检查 lifecycle=current，
            指向非 current KP 的链接自然失效（见 retrieval.md 步骤 2）；
Study 感知： Study 扫描时跳过指向非 current KP 的链接的强化，
            并对 verified 链接生成降权信号（见 study.md 步骤 4）；
Wiki 标记：  SetUnitLifecycle 执行后，查询 wiki_pages.source_point_ids
            包含受影响 point_id 的已发布页面，标记 needs_recompile
            （见 wiki.md 步骤 5）。
```

前两条无需在 lifecycle 模块写代码；第三条由 SetUnitLifecycle 内部调用 Wiki 模块的标记接口完成。

注：activation_links 自身也有一套独立状态机（candidate / verified / weakened / deprecated，见 `activation.md`），与本文档的 KU/KP lifecycle 是两套不同的状态，互不映射——只在"匹配时联合过滤 KP lifecycle=current"这一点上产生交集。

### 步骤 5：暴露 HTTP API

```text
POST   /sources/:id/reupload         见步骤 2
POST   /sources/:id/reupload/retry   见步骤 2
DELETE /sources/:id                  见步骤 2

GET    /units?lifecycle=...    既有列表接口增加 lifecycle 过滤参数
GET    /units/:id              响应增加 lifecycle / lifecycle_changed_at 字段
GET    /points/:id             同上
```

## 依赖

```text
基础设施：SQLite（migration，含 sources.shadow_of）、Bleve 索引管理接口
Source：  reupload 的影子 Source 完整复用 Import() / source_process →
          unit_extract 处理链与既有 POST /sources/:id/retry（MVP 已实现，
          不改动其逻辑，只是套用在影子 source_id 上）；
          ExistsByFileName 检查需排除 shadow_of 指向的目标 source_id；
          deleted 状态扩展 sources.status 枚举
Retrieval：Domain 预过滤 / Source 语义过滤（mvp/retrieval.md 步骤 2-3）需排除
          shadow_of IS NOT NULL 的行（见 v1/retrieval.md 步骤 5）
Unit：    新提取 KU/KP 写入时携带 lifecycle=current（影子与正常导入一致）
Wiki：    needs_recompile 标记接口（wiki.md，可后置——Wiki 模块未实现时该调用为 no-op）
```

## 完成标准

```text
migration 执行后存量 KU/KP 均为 current，检索行为与 V1 之前一致；
reupload 处理全程（含影子的 source_process、unit_extract、KPN、Concept 匹配）中，
             原 :id 下的 KU/KP 始终保持 current 且正常参与召回，
             不存在任何"旧内容已失效、新内容还没好"的空窗期；
影子 Source 在 GET /sources、Domain 预过滤、Source 语义过滤中均不可见；
影子的 source_process 阶段失败：原 :id 完全不受影响，
             影子 sources.status=failed，POST /sources/:id/reupload/retry
             可续跑影子（复用既有 retry 幂等逻辑）；
影子的 unit_extract 阶段失败：原 :id 完全不受影响，影子
             units_status=failed；reupload/retry 只重新入队 unit_extract；
影子放弃重试、改用新文件重新 reupload 时，旧影子被丢弃，原 :id 仍不受影响；
影子的 unit_extract 阶段完成（含部分分段失败，不算失败）后，
             换血事务原子执行：影子 KU/KP/outlines 重新挂靠到 :id、
             原 :id 下仍为 current 的旧 KU 一次性转 superseded（已是
             superseded/deprecated 的历史版本不重复触碰、
             lifecycle_changed_at 不被覆盖）、旧文件归档、影子行删除；
             事务失败时原 :id 状态不受影响，等同换血未发生；
多次 reupload：仅本轮真正被替代（原本 current）的 KU 转 superseded，
             更早轮次已 superseded 的 KU 的 lifecycle_changed_at 保持首次
             变更时间不变，供归档 Markdown 回链按该时间对齐
             source_versions.archived_at；
删除 Source 后：该 source 全部 KU/KP（含此前已 superseded 的）为 deprecated，
             outlines 节点从索引移除，historical answers 的
             evidence_snapshot 仍可反查原文；
Bleve 查询携带 lifecycle 过滤，非 current 文档不出现在 units/points 召回；
KP 状态始终与所属 KU 一致，不存在 KU=current 而 KP=superseded 的组合；
SetUnitLifecycle 是唯一状态变更入口，单元测试覆盖级联与索引同步；
fake 环境下 reupload（换血成功/影子 source_process 失败并重试/
             影子部分分段失败仍换血成功/放弃重试改新文件）、delete、
             状态过滤路径测试稳定运行。
```
