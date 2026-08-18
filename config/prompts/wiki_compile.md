---
version: v3
---

## System

根据以下已确认的结论、张力，以及 Core / Context / Conflict 分组材料，把它们写成一篇完整的 Wiki 页面正文。

要求：

1. 正文必须包含五个二级标题，按顺序：## 摘要 / ## 稳定结论 / ## 展开说明 / ## 待验证点 / ## 依赖来源；
2. 「展开说明」以 Core 材料为主线组织，可以引用 Context 材料补充背景、引用 Conflict 材料交代例外/矛盾，但不要把 Context/Conflict 材料整段照搬——它们是辅助材料，不是本页正文主体；
3. 「摘要」是这个{{entry_kind_label}}的一句话定义加 2-3 句概览，不含 [point_id] 标注，必须能脱离全文独立成立；
4. 「稳定结论」逐条对应 claims，每条结论末尾以 [point_id] 标注依据；
5. 「待验证点」对应 tensions，以及材料间未调和的矛盾（尤其是 Conflict 分组里的内容）；
6. 「依赖来源」按来源归并列出涉及的 KU/Source；
7. 只使用提供的材料与结论，不得引用未提供的 point_id（citation 白名单 = Core ∪ Context ∪ Conflict 全部 point_id）；
8. {{entry_kind_hint}}

直接输出 Markdown 正文，不要输出 JSON、不要输出额外说明。

## User

{{entry_kind_label}}：{{entry_name}}（{{entry_description}}）

结论：
{{claims}}
张力：
{{tensions}}

Core（本页核心）：
{{core_material}}

Context（相关背景，一跳 related）：
{{context_material}}

Conflict（矛盾/例外，一跳 contradicts）：
{{conflict_material}}

相关知识缺口：
{{gaps}}
