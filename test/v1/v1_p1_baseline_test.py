#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P1：慢路径基线。

跑 A+T+B+C+D+G 组主问法（单轮 POST /answer, deep=false），核对：
  - 此时应全部 path_type=full（尚无 verified 链接，P1 阶段本就在 P2 学习转化之前跑）；
  - A/D/G 组（B 组暂不强制，见下）按制度域/技术域分别统计 direct_hit 与关键词覆盖率——
    这两个是自动化代理指标，不能替代方案要求的人工核对"回答要点是否正确"，报告里留了
    manual_verdict 列，人工复核后按该列重新统计才是最终验收结果（沿用 qa_accuracy_test.py
    的口径）；
  - D 组核验 mined=true 的证据 content 是否为对应 Source 当前 markdown 的子串，并统计
    回退率（mined=false）；
  - C 组核验没有产生引用（不得幻构）且 learning_events 出现 knowledge_gap；
  - A 组核验 learning_events 出现 activation_gap 且 payload 含 question_terms/direct_point_ids
    （标准 1 的燃料链路，一条都没有说明链路断裂）；
  - 记录每题 latency，写入 jsonl 供 test/v1/v1_p3_fastpath_test.py 用 --baseline 参数对比
    "耗时下降 ≥40%"。

不覆盖：LLM 调用次数——方案第 3 节明确当前代码没有按问题计数的 slog（"若当前无此日志
需先补一行 slog"），本脚本不伪造该指标，需要该数据请先在
internal/foundation/llm/client.go 加计数日志。

用法：
  python3 test/v1/v1_p1_baseline_test.py                   # 全部 A+T+B+C+D+G
  python3 test/v1/v1_p1_baseline_test.py --group A         # 只跑 A 组
  python3 test/v1/v1_p1_baseline_test.py --ids A1,T3,D2    # 只跑指定题号
"""
import argparse
import json
import re
import sys
import time
from pathlib import Path

import v1_common as c

GROUPS = ["A", "T", "B", "C", "D", "G"]


def expected_source_cell(row):
    for key in ("期望证据来源", "期望证据", "来源"):
        if key in row:
            return row[key]
    return None


def load_bank(groups, full_text):
    bank = []
    for g in groups:
        for row in c.load_group(g, full_text):
            variants = c.question_variants(row)
            bank.append(
                {
                    "id": c.row_id(row),
                    "group": g,
                    "question": variants[0] if variants else None,
                    "variants": variants,
                    "expected_points": row.get("期望答案要点", ""),
                    "expected_source": expected_source_cell(row) or "",
                    "expected_fragment": row.get("期望引用片段（必须是原文子串）", ""),
                    "expected_behavior": row.get("期望行为", ""),
                    "counter_check": row.get("反例检查", ""),
                }
            )
    return bank


def wait_trace(conn, answer_id, timeout_s=15):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s, 0.5)


def check_d_group(base_url, row, direct_ev, supporting_ev, markdown_cache):
    all_ev = direct_ev + supporting_ev
    mined_check = []
    for ev in all_ev:
        if not ev.get("mined"):
            continue
        ref = ev.get("source_ref")
        if isinstance(ref, str):
            ref = json.loads(ref)
        sid = (ref or {}).get("source_id")
        if not sid:
            continue
        if sid not in markdown_cache:
            markdown_cache[sid] = c.http_get_text(base_url, f"/sources/{sid}/markdown")
        mined_check.append(
            {"fact_id": ev.get("fact_id"), "is_substring": ev.get("content", "") in markdown_cache[sid]}
        )

    # 「...」内才是真正的期望片段文本；外面的"表行/片段/所在..."是描述性后缀，不能
    # 参与匹配。片段里的"……"是方案原文的省略号（表示中间省略），不是待匹配的字面
    # 字符，要拆成子段分别核对是否都出现，而不是连省略号一起当整串子串比对。
    m = re.search(r"「(.+)」", row["expected_fragment"])
    fragment_text = m.group(1) if m else row["expected_fragment"].strip()
    segments = [s.replace(" ", "") for s in fragment_text.split("……") if s.strip()]

    def content_has_all_segments(content):
        norm = (content or "").replace(" ", "")
        return bool(segments) and all(seg in norm for seg in segments)

    frag_hit = any(content_has_all_segments(ev.get("content", "")) for ev in all_ev)
    return mined_check, frag_hit


def run_question(base_url, conn, row, id_to_title, markdown_cache, timeout):
    if not row["question"]:
        return {**row, "domain": c.domain_of(row["id"]), "error": "题库缺少问题文本"}

    payload = {"question": row["question"], "deep": False}
    t0 = time.time()
    try:
        result, _status = c.http_post_json(base_url, "/answer", payload, timeout=timeout)
    except Exception as e:
        return {**row, "domain": c.domain_of(row["id"]), "error": str(e)}
    latency = time.time() - t0

    content = result.get("content", "")
    citations = result.get("citations", [])
    es = result.get("evidence_snapshot") or {}
    direct_ev = es.get("direct_evidence") or []
    supporting_ev = es.get("supporting") or []
    direct_ids = c.evidence_source_ids(direct_ev)
    direct_titles = sorted({id_to_title.get(sid, sid) for sid in direct_ids})

    expected_titles = c.expected_titles_for(row["expected_source"]) if row["expected_source"] else []
    direct_hit = bool(expected_titles) and any(t in expected_titles for t in direct_titles)

    key_terms = c.extract_key_terms(row["expected_points"]) if row["expected_points"] else []
    found_terms = [t for t in key_terms if t.lower() in content.lower()]

    trace = wait_trace(conn, result.get("answer_id"))
    events = c.db_learning_events_for_trace(conn, trace["trace_id"]) if trace else []

    mined_check, fragment_hit = ([], None)
    if row["group"] == "D" and row["expected_fragment"]:
        mined_check, fragment_hit = check_d_group(base_url, row, direct_ev, supporting_ev, markdown_cache)

    return {
        **row,
        "domain": c.domain_of(row["id"]),
        "path": result.get("path"),
        "path_type": result.get("path_type"),
        "has_answer": result.get("has_answer"),
        "answer_id": result.get("answer_id"),
        "latency_s": round(latency, 2),
        "content": content,
        "citations_count": len(citations),
        "expected_titles": expected_titles,
        "direct_evidence_titles": direct_titles,
        "direct_hit": direct_hit,
        "key_terms": key_terms,
        "found_terms": found_terms,
        "key_term_coverage": [len(found_terms), len(key_terms)],
        "trace_id": trace["trace_id"] if trace else None,
        "event_types": [e["event_type"] for e in events] if trace else None,
        "mined_substring_check": mined_check,
        "fragment_hit": fragment_hit,
        "max_evidence_len": max([len(ev.get("content", "") or "") for ev in direct_ev], default=0),
    }


def summarize(records):
    lines = []
    non_full = [r for r in records if not r.get("error") and r.get("path_type") != "full"]
    lines.append(
        f"path_type 全为 full（此时无 verified 链接）: {'PASS' if not non_full else 'FAIL'} "
        f"({[r['id'] for r in non_full]})"
    )

    scoring = [r for r in records if r["group"] in ("A", "T", "B", "D", "G") and not r.get("error")]
    by_domain = {}
    for r in scoring:
        st = by_domain.setdefault(
            r["domain"], {"n": 0, "hit_n": 0, "hit": 0, "cov_found": 0, "cov_total": 0}
        )
        st["n"] += 1
        # D 组表格结构上没有"期望证据来源"列（只核对片段子串，见 fragment_hit），
        # expected_titles 恒为空——不能算进 direct_hit 分母，否则会被结构性拉低。
        if r["expected_titles"]:
            st["hit_n"] += 1
            st["hit"] += 1 if r["direct_hit"] else 0
        found, total = r["key_term_coverage"]
        st["cov_found"] += found
        st["cov_total"] += total
    lines.append("\n按域统计（自动代理指标，最终以人工 manual_verdict 复核为准；direct_hit 分母只含有期望来源的题，D 组片段级核验见下）：")
    for domain, st in by_domain.items():
        hit_rate = st["hit"] / st["hit_n"] * 100 if st["hit_n"] else 0
        cov_rate = st["cov_found"] / st["cov_total"] * 100 if st["cov_total"] else 0
        lines.append(
            f"  {domain}: n={st['n']}（direct_hit 计分题数={st['hit_n']}）"
            f"direct_hit={hit_rate:.1f}% 关键词覆盖={cov_rate:.1f}%（目标各自 ≥90%）"
        )

    # C 组判定口径（2026-07-19 定案，见方案 4.4 节）：主判定是"回答不幻构"——
    # 要么零引用，要么正文明确说明知识库无相关内容；knowledge_gap 事件仅在检索
    # 证据全空时才会产生（有 supporting 即 partial 不触发），只作信息展示不计失败。
    honest_markers = ("无相关", "没有相关", "无法确定", "无法回答", "未包含", "不足以", "暂无相关", "未提及")
    c_rows = [r for r in records if r["group"] == "C" and not r.get("error")]
    for r in c_rows:
        content = r.get("content") or ""
        honest = r["citations_count"] == 0 or any(m in content for m in honest_markers)
        gap_event = (r.get("event_types") or []).count("knowledge_gap") > 0
        lines.append(
            f"  {r['id']} 缺口题: 不幻构(诚实作答)={'PASS' if honest else 'FAIL'}"
            f"（citations={r['citations_count']}）；knowledge_gap 事件={'有' if gap_event else '无'}"
            f"（仅证据全空时产生，不计失败）"
        )

    d_rows = [r for r in records if r["group"] == "D" and not r.get("error")]
    total_mined = sum(len(r["mined_substring_check"]) for r in d_rows)
    bad_mined = sum(
        1 for r in d_rows for m in r["mined_substring_check"] if not m["is_substring"]
    )
    lines.append(
        f"\nD 组片段子串核验：mined=true 证据 {total_mined} 条，子串校验失败 {bad_mined} 条"
        f"（目标 0 例外）"
    )
    frag_miss = [r["id"] for r in d_rows if r.get("fragment_hit") is False]
    lines.append(f"D 组期望片段命中: {'PASS' if not frag_miss else 'FAIL'}（未命中: {frag_miss}）")

    a_rows = [r for r in records if r["group"] == "A" and not r.get("error")]
    a_gap_events = [r for r in a_rows if (r.get("event_types") or []).count("activation_gap") > 0]
    lines.append(
        f"\nA 组 activation_gap 事件: {len(a_gap_events)}/{len(a_rows)} 题产生"
        f"（标准 1 的燃料链路，一条都没有说明链路断裂，需先修再继续）"
    )

    return "\n".join(lines)


def write_md_report(records, out_dir: Path):
    lines = ["# P1 慢路径基线报告\n"]
    lines.append(
        "direct_hit 与关键词覆盖率为自动代理指标，**不能替代人工核对「回答要点是否正确」**；"
        "manual_verdict 列留空，需人工按方案标准填写 正确/错误 后重新统计。\n"
    )
    lines.append("| ID | 组 | 域 | path_type | direct_hit | 关键词覆盖 | 耗时(s) | manual_verdict | 回答摘要 |")
    lines.append("|---|---|---|---|---|---|---|---|---|")
    for r in records:
        if r.get("error"):
            lines.append(f"| {r['id']} | {r['group']} | {r['domain']} | ERROR |  |  |  |  | {r['error'][:80]} |")
            continue
        found, total = r["key_term_coverage"]
        cov = f"{found}/{total}" if total else "n/a"
        summary = (r["content"] or "").replace("\n", " ")[:60]
        lines.append(
            f"| {r['id']} | {r['group']} | {r['domain']} | {r.get('path_type')} | "
            f"{'✅' if r['direct_hit'] else '❌'} | {cov} | {r['latency_s']} |  | {summary} |"
        )
    return c.write_text("\n".join(lines) + "\n", out_dir, "v1_p1_baseline")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--group", choices=GROUPS + ["all"], default="all")
    parser.add_argument("--ids", help="逗号分隔题号，如 A1,T3,D2（优先于 --group）")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5, help="题间等待秒数")
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    full_text = c.load_plan_text()
    groups = GROUPS if args.group == "all" else [args.group]
    bank = load_bank(groups, full_text)
    if args.ids:
        wanted = {s.strip() for s in args.ids.split(",") if s.strip()}
        bank = [r for r in bank if r["id"] in wanted]
    if not bank:
        print("没有匹配到任何题目", file=sys.stderr)
        sys.exit(1)

    id_to_title = c.fetch_source_titles(args.base_url)
    conn = c.open_db(args.db_path)
    markdown_cache = {}

    print(f"共 {len(bank)} 道题待测。\n")
    records = []
    for i, row in enumerate(bank, 1):
        print(f"[{i}/{len(bank)}] {row['id']}: {row['question']}")
        rec = run_question(args.base_url, conn, row, id_to_title, markdown_cache, args.timeout)
        if rec.get("error"):
            print(f"  ! 出错: {rec['error']}")
        else:
            found, total = rec["key_term_coverage"]
            print(
                f"  path_type={rec.get('path_type')} direct_hit={rec['direct_hit']} "
                f"关键词 {found}/{total} 耗时 {rec['latency_s']}s"
            )
        records.append(rec)
        if i < len(bank):
            time.sleep(args.delay)

    conn.close()

    print("\n========== P1 通过标准核对 ==========")
    print(summarize(records))

    jsonl_path = c.write_jsonl(records, Path(args.out), "v1_p1_baseline")
    md_path = write_md_report(records, Path(args.out))
    print(f"\n详细结果: {jsonl_path}")
    print(f"汇总报告: {md_path}")
    print(
        "\n提示：test/v1/v1_p3_fastpath_test.py --baseline 可传入上面的 jsonl 路径，"
        "用于对比快路径的耗时下降幅度。"
    )


if __name__ == "__main__":
    main()
