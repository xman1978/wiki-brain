# V1 检索与学习闭环测试说明

本目录用于验证 v1 的两个目标：

1. 实现是否符合设计：Trace -> Study -> ActivationLink / Wiki / Concept 的闭环是否能跑通。
2. 真实应用场景是否提升检索能力和速度：Wiki / fast path 是否减少调用和延迟，同时保持证据质量。

脚本只依赖 Python 标准库，默认服务地址为 `http://127.0.0.1:8800`。

## 0. 前置检查

在仓库根目录启动服务：

```bash
rtk go run ./cmd/server -config config/config.yml
```

确认真实 markdown 已在这里：

```bash
ls data/sources/markdown
```

如果服务已经运行，直接进入下一步。

## 1. 生成 80-100 个真实场景问题

```bash
python3 test/v1/generate_questions.py --count 90
```

输出：

```text
test/v1/questions.jsonl
test/v1/questions.csv
```

`questions.csv` 方便人工快速审阅。建议先删掉明显重复或表述不自然的问题，再继续测试；脚本生成的是稳定问题集，不追求一次性完美。

## 2. 建立 full path 基线

先跑小样本，确认服务和问题集正常：

```bash
python3 test/v1/run_eval.py --mode retrieval --limit 5
```

再跑完整检索对照：

```bash
python3 test/v1/run_eval.py --mode retrieval
```

这个脚本会对每个问题调用两次：

```text
POST /retrieval {"question": "...", "force_full": true}
POST /retrieval {"question": "..."}
```

输出：

```text
test/v1/out/retrieval.jsonl
test/v1/out/retrieval_summary.json
```

第一轮预期通常是：

```text
normal_path_type 多数为 full；
fast/wiki 很少或没有；
latency 与 full 基线差别不大；
full_direct_count 不应长期为 0。
```

如果大量问题 `full_direct_count=0`，说明导入、索引、Rerank 或问题集需要先修正。

## 3. 让 Trace 写入完成

`/retrieval` 只测试检索，不一定产生完整 Answer Trace。为了产生端到端 Trace，可抽样跑 `/answer`：

```bash
python3 test/v1/run_eval.py --mode answer --limit 30
```

输出：

```text
test/v1/out/answer.jsonl
test/v1/out/answer_summary.json
```

需要更强的学习信号时，可以把 `--limit` 去掉，跑完整 90 个问题。LLM 调用会更多，耗时也更长。

## 4. 运行 Study，观察候选

```bash
python3 test/v1/v1_ops.py run-study
```

查看候选 ActivationLink：

```bash
python3 test/v1/v1_ops.py candidate-links
```

查看 pending promotion：

```bash
python3 test/v1/v1_ops.py pending-promotions
```

查看 Wiki 候选：

```bash
python3 test/v1/v1_ops.py wiki-candidates
```

查看概念候选：

```bash
python3 test/v1/v1_ops.py concept-candidates
```

第一轮如果没有 candidate，不一定是失败，常见原因是：

```text
同一 question_terms / point_id 的 confident_count 不够；
answer 抽样太少；
问题过于分散；
Rerank/Answer 没有引用 direct KP；
Study 阈值较高，例如 candidate_confident_min=5。
```

## 5. 人工确认 ActivationLink

从候选列表挑选你认为合理的链接，确认：

```bash
python3 test/v1/v1_ops.py confirm-link <link_id>
```

拒绝明显不合理的链接：

```bash
python3 test/v1/v1_ops.py reject-link <link_id>
```

确认后再次运行 retrieval eval：

```bash
python3 test/v1/run_eval.py --mode retrieval
```

达标迹象：

```text
normal_path_type 中 fast 数量增加；
normal_ms 低于 full_ms；
normal_activation_hits > 0；
normal_direct_count 不低于 full_direct_count 太多；
point_jaccard 不应系统性接近 0。
```

## 6. 编译并发布 Wiki 页面

先看 Wiki 候选：

```bash
python3 test/v1/v1_ops.py wiki-candidates
```

对某个候选执行编译：

```bash
python3 test/v1/v1_ops.py compile-wiki <concept_id> --result-id <result_id> --page-type concept
```

查看页面：

```bash
python3 test/v1/v1_ops.py wiki-pages
```

发布页面：

```bash
python3 test/v1/v1_ops.py publish-wiki <page_id>
```

再跑 retrieval eval：

```bash
python3 test/v1/run_eval.py --mode retrieval
```

达标迹象：

```text
相关问题 normal_path_type=wiki；
wiki 问题延迟进一步下降；
wiki sufficient=false 时能回落 fast/full；
发布页面的 citations 能回到 point_id。
```

## 7. 评估指标怎么看

`retrieval_summary.json` 里重点看：

```text
path_type_counts
avg_speedup_full_over_normal
avg_point_jaccard_full_vs_normal
normal_fast_or_wiki
normal_direct_empty
latency_p50_ms / latency_p90_ms
```

建议验收标准：

```text
学习前：normal 多数为 full；
确认链接后：同类问题开始出现 fast；
发布 Wiki 后：稳定主题问题出现 wiki；
fast/wiki 的 normal_direct_empty 不应升高；
fast/wiki 的延迟应明显低于 force_full；
错误链接在后续 answer 测试中会产生 activation_failure，并可被 Study 降权。
```

## 8. 推荐测试节奏

```text
第 1 轮：生成问题 -> retrieval 基线 -> answer 抽样 -> Study
第 2 轮：确认少量高置信 ActivationLink -> retrieval 对照
第 3 轮：编译/发布 1-3 个 Wiki 页面 -> retrieval 对照
第 4 轮：加入变体问题，检查 fast/wiki 是否泛化
第 5 轮：加入反例问题，检查 audience/constraint 与 fallback 是否挡住误召回
```

每轮保留 `test/v1/out/*.jsonl`。需要长期比较时，把目录改名为带日期的目录，例如：

```bash
mv test/v1/out test/v1/out-round-1
```

## 9. 注意事项

`/retrieval` 适合测速度和证据路径；`/answer` 适合产生 Trace 和检查最终回答质量。

如果你只跑 `/retrieval`，学习闭环可能不完整，因为 Trace/Study 的主要输入来自 AnswerResult。

如果你想快速形成 candidate，可先用同一类问题多问几种变体，然后跑 `/answer` 和 `/study/run`。
