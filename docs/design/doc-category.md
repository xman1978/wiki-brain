# 文档分类（doc_category）设计

设计方向，**不排入 V1 强制实现顺序**，不影响正在进行的 V1 模块序列。

## 1. 问题

同一个知识领域内的文档，除了"讲的是哪个概念/事实"这个语义维度外，还有一个正交的**体裁/用途维度**：同一领域内的文档可能是安装手册、故障案例、架构说明、制度原文……这个维度与文档具体讨论的主题无关，是文档本身固有的、导入时就确定的属性，检索时可以作为一个独立的、结构化的过滤条件使用（"只看故障排查案例"、"只看制度原文不看案例解读"）。

## 2. 为什么不能直接复用现有"主题标签"

现有的"主题标签"机制（`source_affinity`/`subject_norms`，见 `docs/design/retrieval.md`「14. Source 匹配：主题标签而不是问答绑定」）解决的是完全不同的问题：

| | 主题标签（source_affinity） | 文档分类（doc_category） |
|---|---|---|
| 产生方式 | 问题驱动、后台异步生成——只有被问到过的主题才会给文档打标签 | 文档固有属性，导入时即可确定，不依赖是否被问到过 |
| 值域 | 开放、随问法自然增长，做归一化去重 | 封闭、有限，按领域人工预定义 |
| 语义 | "这个主题该查哪些文档" | "这份文档是什么体裁" |
| 稳定性 | 会随问法归一化调整（合并/拆分） | 文档不变则分类不变 |

把体裁分类塞进主题标签机制会导致"没人问过的类型永远没标签"（比如从未有人问过"有哪些故障案例"，故障案例文档就永远不会被打上对应标签），且两套语义混在一起后，主题归一化的合并/拆分逻辑会污染分类语义。因此文档分类需要独立的 schema 与独立的生成方式，与主题标签并行、互不覆盖。

## 3. 决策

### 3.1 值域按领域各自预定义，不设全局枚举

不同领域的文档体裁完全不同（产品领域是"白皮书/方案/案例"，运维领域是"安装指南/故障案例/调优指南"，制度领域是"制度文件/审批流程"），没有一套全局分类能同时适用。因此 doc_category 的值域**按 `domain_id` 各自维护**，复用 `domains`/`entries` 现有的"人工预定义 + `preset/domains.json` 启动时 UPSERT"治理模式（见 `internal/foundation/preset.go`）：每个 domain 对象下新增一个 `doc_categories` 数组，与 `entries` 平级，各领域独立维护自己的类目集合，互不影响、互不共享。

### 3.2 数据结构

新增 `doc_categories` 表（迁移 075）：

```sql
CREATE TABLE doc_categories (
    category_id TEXT PRIMARY KEY,
    domain_id   TEXT NOT NULL REFERENCES domains(domain_id),
    name        TEXT NOT NULL,
    description TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`sources.doc_category_id` 新增可空外键列，指向 `doc_categories.category_id`。可空是必须的——没有匹配到任何预定义类目的文档允许保持未分类，不强行归类到"其他"污染统计（是否设"其他"兜底类目，由人工在维护该领域的值域时自行决定，不写死在代码里）。

`description` 字段与 `entries.description`/`boundary` 同等地位——是分类时的判断依据（喂给 LLM），不是展示用的说明文字，因此每个类目的 description 要写清楚"什么样的文档属于这一类"。

### 3.3 分类时机与方式：镜像现有 `matchDomain`

`internal/source/service.go` 的 `matchDomain` 已经是"用 LLM 从一份预定义的枚举列表中选一个最匹配项"的现成实现（`source_domain_match.md`，domain 匹配失败或列表为空时静默跳过，不阻塞主流程）。doc_category 分类采用完全相同的形状：

1. **时机**：`source_process` 流程中，`matchDomain` 完成、`domain_id` 已确定之后紧接着执行（因为 doc_category 的候选列表本身是按 `domain_id` 过滤的，必须先知道属于哪个领域）。归入现有「Step 8: 领域匹配」进度节点，不新增独立的进度步骤——这一步和领域匹配本质上是同一个"分类"动作的两个层次，没有必要在 UI 进度条上拆成两段。
2. **新增 prompt**：`config/prompts/source_doc_category_match.md`，输入文档标题+摘要+该领域的类目列表（id+name+description），输出 `{"category_id": "xxx 或 null"}`，未命中任何类目时返回 null，不报错、不阻塞。
3. **候选列表为空时跳过**：某个领域尚未维护 doc_categories（值域为空数组）时，直接跳过分类，不产生任何调用，`sources.doc_category_id` 保持 NULL——与 `matchDomain` 在 `len(domains)==0` 时的处理方式一致。
4. **人工覆盖**：新增 `PATCH /sources/:id/doc-category`，镜像 `PATCH /sources/:id/domain`（`SetDomain`）的手动纠正入口——LLM 分类不准时人工可以直接改，不需要重新导入。
5. **`domain_id` 变更时联动清空**：`SetDomain` 改变一个文档所属领域时，其原有 `doc_category_id`（属于旧领域的值域）不再有效，需要清空并（若新领域有值域）异步重新分类——同 `SetDomain` 触发 `conceptMatcher.MatchEntries` 重新匹配是同一种"上游分类变了，下游依赖的分类要跟着失效重算"的处理方式。
6. **Shadow Source 换血**：`SwapShadowIntoTarget`（reupload 流程）把 `doc_category_id` 与 `domain_id`/`outline_type`/`summary` 一起从影子行拷贝到目标行——影子的分类结果就是新内容的分类结果，同现有字段处理方式一致。

### 3.4 检索集成：本次不做，留作独立后续决策

`doc_category` 目前只落地为一个可查询、可人工维护、可通过 API 按 `domain_id`+`category_id` 过滤 `GET /sources` 列表的结构化字段，**不改动 `internal/retrieval` 的匹配/过滤/排序逻辑**。原因：

- Retrieval 模块正处于 V1 强制顺序内，本功能明确"不排入 V1 顺序"，不应该在 V1 序列进行中去改动 Retrieval 的行为。
- doc_category 要不要、以及怎样接入检索（例如作为 domain 预过滤之外新增的一道显式过滤维度，或是仅供人工浏览/管理使用），本身是一个需要单独确认的设计问题，不在本次方案范围内一并决定。

`docs/impl/v1/retrieval.md` 留一处"分类指针"标注这里未来可能是一个可选的过滤维度，但当前检索行为不因此改变。

## 4. 不做的事

- 不做 doc_category 的自动挖掘/候选/人工确认流程（不是 Study 驱动的学习对象，纯人工预定义 + LLM 归类，同 entries 的 preset 部分而非 candidate 部分）。
- 不允许一份文档同时属于多个类目（`sources.doc_category_id` 是单值外键，不是多对多）——体裁分类假设互斥；如果实践中出现明显跨类目的文档，交给人工判断归到最贴切的一个，而不是引入多值。
- 不做类目的自动发现（不从语料反推该建哪些类目）——类目集合完全由人工按领域维护。
