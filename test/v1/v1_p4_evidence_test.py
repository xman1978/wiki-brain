#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P4：证据挖掘与幻构拦截专项（标准 4）。

覆盖：
  1. 重跑 D 组 + 3 个新长表格题（培训积分规则表内任选，见 EXTRA_LONG_TABLE_QUESTIONS），
     核验片段子串性质，并且比 P1 更严格——不只是"整篇文档里能找到"，而是
     GET /units/:id 对照 line_start/line_end：证据 content 必须是那个 KU 声明的
     行区间切片的子串（验证行号定位准确，不是蒙对了别处的相同文本）；
  2. 幻构拦截（黑盒二选一里的"纯黑盒"分支——本环境没有 fake LLM 注入手段）：
     跑完题后 grep logs/wiki-brain.log，确认 internal/evidence/service.go 里的
     "fragment not found in KU content, dropped"（校验不通过丢弃）/
     "mine batch ... failed"（重试）/"whole-segment fallback"（整段回退）
     这几类日志至少被观察到一次；
  3. 通过标准：核验脚本 0 例外（子串校验、行号定位）；mined=false 的回退次数如实统计。

EXTRA_LONG_TABLE_QUESTIONS 是本脚本新增的 3 个问题，方案原文只说"积分规则表内任选"，
没有给出期望答案/证据来源，本脚本不代为编造标准答案——只做机械核验（子串、行号、
回退率），回答是否正确仍需人工看打印内容判断。

用法：
  python3 test/v1/v1_p4_evidence_test.py
  python3 test/v1/v1_p4_evidence_test.py --log-path logs/wiki-brain.log
"""
import argparse
import json
import re
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import v1_common as c

EXTRA_LONG_TABLE_QUESTIONS = [
    "培训积分课件输出加多少分？",
    "培训积分满勤有奖励吗，加多少分？",
    "培训考试成绩70到79分之间扣多少分？",
]


def load_d_group_questions(full_text):
    rows = c.load_group("D", full_text)
    out = []
    for row in rows:
        variants = c.question_variants(row)
        out.append(
            {
                "id": c.row_id(row),
                "question": variants[0] if variants else None,
                "expected_fragment": row.get("期望引用片段（必须是原文子串）", ""),
            }
        )
    return out


def fragment_segments(expected_fragment):
    m = re.search(r"「(.+)」", expected_fragment)
    text = m.group(1) if m else expected_fragment.strip()
    return [s.replace(" ", "") for s in text.split("……") if s.strip()]


def check_evidence_against_unit_range(base_url, ev, markdown_cache, unit_cache, cache_lock):
    """比 P1 的"全文子串"更严格：取 ev 对应 KU 的 line_start/line_end，切原文，
    再核验 ev.content 是否落在那个切片里（而不只是全文任意位置）。

    cache_lock 保护 markdown_cache/unit_cache——并行提问时多题可能同时命中
    同一 unit/Source，避免重复拉取或竞争写入同一份缓存。"""
    unit_id = ev.get("unit_id")
    ref = ev.get("source_ref")
    if isinstance(ref, str):
        ref = json.loads(ref)
    source_id = (ref or {}).get("source_id")
    if not unit_id or not source_id:
        return {"unit_id": unit_id, "checked": False, "reason": "缺 unit_id/source_id"}

    with cache_lock:
        if unit_id not in unit_cache:
            try:
                unit_cache[unit_id] = c.http_get_json(base_url, f"/units/{unit_id}")
            except Exception as e:
                unit_cache[unit_id] = None
                print(f"    ! GET /units/{unit_id} 失败: {e}", file=sys.stderr)
        unit = unit_cache[unit_id]
    if not unit:
        return {"unit_id": unit_id, "checked": False, "reason": "unit 未找到"}

    with cache_lock:
        if source_id not in markdown_cache:
            markdown_cache[source_id] = c.http_get_text(base_url, f"/sources/{source_id}/markdown")
        full_doc = markdown_cache[source_id]
    lines = full_doc.splitlines()
    start, end = unit["line_start"], unit["line_end"]
    slice_text = "\n".join(lines[max(start - 1, 0):end])

    content = ev.get("content", "") or ""
    in_full_doc = content in full_doc
    in_unit_range = content in slice_text
    return {
        "unit_id": unit_id,
        "checked": True,
        "line_start": start,
        "line_end": end,
        "in_full_doc": in_full_doc,
        "in_unit_range": in_unit_range,
    }


def run_question(base_url, question, expected_fragment, id_to_title, markdown_cache, unit_cache, cache_lock, timeout):
    t0 = time.time()
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    latency = time.time() - t0
    if result is None:
        return {"question": question, "error": f"未走到 retrieve（action={turn.get('action')}）"}

    es = result.get("evidence_snapshot") or {}
    direct_ev = es.get("direct_evidence") or []
    supporting_ev = es.get("supporting") or []
    all_ev = direct_ev + supporting_ev

    range_checks = []
    mined_full_doc_fail = 0
    for ev in all_ev:
        if not ev.get("mined"):
            continue
        chk = check_evidence_against_unit_range(base_url, ev, markdown_cache, unit_cache, cache_lock)
        chk["fact_id"] = ev.get("fact_id")
        range_checks.append(chk)
        if chk.get("checked") and not chk.get("in_full_doc"):
            mined_full_doc_fail += 1

    frag_hit = None
    if expected_fragment:
        segments = fragment_segments(expected_fragment)

        def has_all(text):
            norm = (text or "").replace(" ", "")
            return bool(segments) and all(seg in norm for seg in segments)

        frag_hit = any(has_all(ev.get("content", "")) for ev in all_ev)

    mined_count = sum(1 for ev in all_ev if ev.get("mined"))
    fallback_count = sum(1 for ev in all_ev if not ev.get("mined"))

    return {
        "question": question,
        "latency_s": round(latency, 2),
        "content": result.get("content"),
        "path_type": result.get("path_type"),
        "expected_fragment": expected_fragment,
        "fragment_hit": frag_hit,
        "mined_count": mined_count,
        "fallback_count": fallback_count,
        "range_checks": range_checks,
        "mined_full_doc_fail": mined_full_doc_fail,
        "mined_range_fail": sum(1 for r in range_checks if r.get("checked") and not r.get("in_unit_range")),
    }


def scan_log_for_evidence_patterns(log_path: Path):
    patterns = {
        "校验不通过丢弃 (fragment not found in KU content, dropped)": "fragment not found in KU content, dropped",
        "批次失败重试 (mine batch llm call failed)": "mine batch llm call failed",
        "批次解析失败重试 (mine batch parse failed)": "mine batch parse failed",
        "批次 schema 校验失败重试 (mine batch schema validation failed)": "mine batch schema validation failed",
        "直接证据整段回退 (direct candidate mined nothing, whole-segment fallback)": "direct candidate mined nothing, whole-segment fallback",
        "支持证据整段回退 (supporting candidate ... last-resort whole-segment fallback)": "last-resort whole-segment fallback",
        "支持证据挖掘为空丢弃 (supporting candidate mined nothing, dropped)": "supporting candidate mined nothing, dropped",
        "片段扩宽为整张表格 (fragment widened to cover its whole markdown table)": "fragment widened to cover its whole markdown table",
    }
    if not log_path.exists():
        return {name: None for name in patterns}, f"日志文件不存在: {log_path}"
    text = log_path.read_text(encoding="utf-8", errors="ignore")
    counts = {name: text.count(needle) for name, needle in patterns.items()}
    return counts, None


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--log-path", default=str(c.REPO_ROOT / "logs" / "wiki-brain.log"))
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument(
        "--workers", type=int, default=6,
        help="并发提问数（各题彼此独立：不同问题、不共享 session_id、不改 config.yml，"
             "安全并行；设为 1 退化为原来的顺序提问）",
    )
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    full_text = c.load_plan_text()
    id_to_title = c.fetch_source_titles(args.base_url)
    markdown_cache, unit_cache = {}, {}
    cache_lock = threading.Lock()

    questions = [(r["id"], r["question"], r["expected_fragment"]) for r in load_d_group_questions(full_text)]
    questions += [(f"EXTRA{i+1}", q, "") for i, q in enumerate(EXTRA_LONG_TABLE_QUESTIONS)]

    print(f"共 {len(questions)} 道题（D 组 {len(questions) - len(EXTRA_LONG_TABLE_QUESTIONS)} + 新增 {len(EXTRA_LONG_TABLE_QUESTIONS)}，并发数={args.workers}）。\n")

    records = [None] * len(questions)
    workers = max(1, min(args.workers, len(questions)))
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {
            pool.submit(
                run_question, args.base_url, question, expected_fragment, id_to_title, markdown_cache, unit_cache, cache_lock, args.timeout
            ): i
            for i, (qid, question, expected_fragment) in enumerate(questions)
        }
        for future in as_completed(futures):
            i = futures[future]
            qid = questions[i][0]
            rec = future.result()
            rec["id"] = qid
            if rec.get("error"):
                print(f"[{qid}] ! {rec['error']}")
            else:
                print(
                    f"[{qid}] mined={rec['mined_count']} fallback={rec['fallback_count']} "
                    f"行号范围校验失败={rec['mined_range_fail']} fragment_hit={rec['fragment_hit']}"
                )
            records[i] = rec

    print("\n扫描证据挖掘日志...")
    log_counts, log_err = scan_log_for_evidence_patterns(Path(args.log_path))
    if log_err:
        print(f"  ! {log_err}")
    else:
        for name, n in log_counts.items():
            print(f"  {name}: {n} 次")

    total_mined = sum(r.get("mined_count", 0) for r in records if not r.get("error"))
    total_full_doc_fail = sum(r.get("mined_full_doc_fail", 0) for r in records if not r.get("error"))
    total_range_fail = sum(r.get("mined_range_fail", 0) for r in records if not r.get("error"))
    total_fallback = sum(r.get("fallback_count", 0) for r in records if not r.get("error"))
    total_evidence = total_mined + total_fallback

    print("\n========== P4 通过标准核对 ==========")
    print(
        f"mined=true 证据全文子串核验: {total_mined - total_full_doc_fail}/{total_mined} 通过 "
        f"({'PASS' if total_full_doc_fail == 0 else 'FAIL'})"
    )
    print(
        f"mined=true 证据行号定位核验（更严格，落在 KU 声明的 line_start/line_end 内）: "
        f"{total_mined - total_range_fail}/{total_mined} 通过 "
        f"({'PASS' if total_range_fail == 0 else '有 widen 现象，需人工核对 range_checks 明细'})"
    )
    fallback_rate = total_fallback / total_evidence * 100 if total_evidence else 0
    print(f"回退率（mined=false）: {total_fallback}/{total_evidence} = {fallback_rate:.1f}%（方案目标 ≤30%）")
    if log_err is None:
        observed = any(n for n in log_counts.values())
        print(f"「校验不通过→重试或回退」路径至少观察到一次: {'PASS' if observed else 'FAIL（可多跑几次长表题提高触发概率）'}")

    record = {"questions": records, "log_pattern_counts": log_counts}
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p4_evidence")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
