#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P3：快路径生效（标准 1 后半 + 标准 2）
+ 对象守门。依赖 test/v1/v1_p2_learning_test.py 已经把 A1/A4/A9/A12/T8/T12/T15/F1_PRE 确认
为 verified、A2/T13 reject 掉。

覆盖：
  1. 重问 A1、A9、A12、T8、T12、T15（"换第三种问法"——表里没有第 3 种的题，用
     --extra-phrasing-file 补充，否则退化为复用现有问法并打印警告，不代为编造技术
     问法）各 3 次，核对 path_type=fast、activation_link_ids 非空、耗时对比
     --baseline（P1 报告 jsonl）下降幅度、关键词覆盖/direct_hit（自动代理指标）、
     activation_success 事件是否产生；
  2. 核对未晋升的 A2、T13 仍走 path_type=full；
  3. F 组对象守门 F1/F2/F3 各 3 次：核对前置 verified 链接（F1→F1_PRE，F2→T12，
     F3→T8）不被同句式异对象问题错误激活——判定口径严格按方案 4.7 节："trace 的
     activation_link_ids 含前置链接且其 KP 进入 direct"才算一次守门失效；
  4. E3 会话追问一轮（先问 T13，追问"神通呢？"），核对补全问题与回答是否正确转向神通、
     没有沿用达梦的参数名。

不覆盖：LLM 调用次数（同 P1，代码里没有按问题计数的 slog，不伪造）；回答要点是否正确
（自动代理指标，人工用 manual_verdict 复核）。

用法：
  python3 test/v1/v1_p3_fastpath_test.py --baseline test/v1/results/v1_p1_baseline_20260718-100000.jsonl
  python3 test/v1/v1_p3_fastpath_test.py --extra-phrasing-file test/v1/v1_p2_extra_phrasings.json
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

FASTPATH_IDS = ["A1", "A9", "A12", "T8", "T12", "T15"]
FULL_ONLY_IDS = ["A2", "T13"]
F1_PRE_ID = "F1_PRE"
F1_PRE_DEFAULT_VARIANTS = [
    "达梦怎么查询会话执行情况",
    "达梦数据库怎么查看当前的会话执行情况？",
    "怎么在达梦里查询会话的执行状态？",
]
F_PRECONDITION = {"F1": F1_PRE_ID, "F2": "T12", "F3": "T8"}


def load_extra_phrasings(path):
    if not path:
        return {}
    return json.loads(Path(path).read_text(encoding="utf-8"))


def expected_source_cell(row):
    for key in ("期望证据来源", "期望证据", "来源"):
        if key in row:
            return row[key]
    return None


def load_row_by_id(full_text, target_id):
    for g in ("A", "T"):
        for row in c.load_group(g, full_text):
            if c.row_id(row) == target_id:
                return row
    return None


def third_phrasing(rid, table_variants, extra_phrasings):
    combined = list(table_variants)
    for p in extra_phrasings.get(rid, []):
        if p not in combined:
            combined.append(p)
    if len(combined) >= 3:
        return combined[2], combined
    if len(combined) >= 2:
        print(f"! {rid} 只有 {len(combined)} 种问法，没有真正的'第三种'，退化用最后一种", file=sys.stderr)
        return combined[-1], combined
    print(f"! {rid} 只有 1 种问法，无法验证快路径对改写的鲁棒性，直接复用", file=sys.stderr)
    return combined[0], combined


def wait_trace(conn, answer_id, timeout_s=15):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s, 0.5)


def ask_once(base_url, conn, question, id_to_title, expected_titles, key_terms, timeout):
    """走真实客户端路径（/sessions -> /session/turn -> /answer/stream）而非裸
    POST /answer——ActivationLink 的对象/约束守门匹配（activation.md 步骤 2）用的是
    检索请求里的 subject/intent/audience/constraint 四元组，裸 /answer 从不填这几
    个字段，测出来的根本不是四元组匹配路径，而是回退阈值路径（activation_match_min_fallback），
    F 组守门测试若用裸 /answer 会失真。"""
    t0 = time.time()
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    latency = time.time() - t0
    if result is None:
        return {
            "question": question, "answer_id": None, "path_type": None, "latency_s": round(latency, 2),
            "content": None, "direct_hit": False, "found_terms": [], "trace_id": None,
            "activation_link_ids": [], "direct_point_ids": [], "event_types": None,
            "error": f"未走到 retrieve（action={turn.get('action')}）",
        }

    content = result.get("content", "")
    es = result.get("evidence_snapshot") or {}
    direct_ev = es.get("direct_evidence") or []
    direct_ids = c.evidence_source_ids(direct_ev)
    direct_titles = sorted({id_to_title.get(sid, sid) for sid in direct_ids})
    direct_hit = bool(expected_titles) and any(t in expected_titles for t in direct_titles)
    found_terms = [t for t in key_terms if t.lower() in content.lower()]

    trace = wait_trace(conn, result.get("answer_id"))
    events = c.db_learning_events_for_trace(conn, trace["trace_id"]) if trace else []
    direct_point_ids = json.loads(trace["direct_point_ids"] or "[]") if trace else []
    activation_link_ids = json.loads(trace["activation_link_ids"] or "[]") if trace else []

    return {
        "question": question,
        "answer_id": result.get("answer_id"),
        "path_type": result.get("path_type"),
        "latency_s": round(latency, 2),
        "content": content,
        "direct_hit": direct_hit,
        "found_terms": found_terms,
        "trace_id": trace["trace_id"] if trace else None,
        "activation_link_ids": activation_link_ids,
        "direct_point_ids": direct_point_ids,
        "event_types": [e["event_type"] for e in events] if trace else None,
    }


def find_verified_link_by_probe(base_url, probe_question, timeout):
    """用一次探测提问拿到目标 KP 的 point_id，再按 point_id 反查 verified 链接。
    ActivationLink.question_terms 是 LLM 生成的语义标签（如"数据库会话监控"），不是
    字面问题文本，没法直接拿变体问法反查（P2 脚本最初就是栽在这个假设上）。"""
    _turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout)
    if not result:
        return None, None
    es = result.get("evidence_snapshot") or {}
    point_ids = [ev.get("point_id") for ev in (es.get("direct_evidence") or []) if ev.get("point_id")]
    if not point_ids:
        return None, None
    all_links = c.http_get_json(base_url, "/activation-links?status=verified&limit=500")
    for link in all_links:
        if link["point_id"] in point_ids:
            return link["link_id"], link["point_id"]
    return None, point_ids[0]


def load_baseline(path):
    if not path:
        return {}
    baseline = {}
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        rec = json.loads(line)
        if rec.get("id") and rec.get("latency_s") is not None:
            baseline[rec["id"]] = rec["latency_s"]
    return baseline


def run_fastpath_group(base_url, conn, full_text, extra_phrasings, id_to_title, timeout, delay, repeats=3):
    report = {}
    for rid in FASTPATH_IDS:
        row = load_row_by_id(full_text, rid)
        table_variants = c.question_variants(row)
        question, _all_variants = third_phrasing(rid, table_variants, extra_phrasings)
        expected_titles = c.expected_titles_for(expected_source_cell(row) or "")
        key_terms = c.extract_key_terms(row.get("期望答案要点", ""))

        print(f"\n--- {rid}（第三种问法）: {question} ---")
        runs = []
        for i in range(repeats):
            r = ask_once(base_url, conn, question, id_to_title, expected_titles, key_terms, timeout)
            print(
                f"  第{i+1}次: path_type={r['path_type']} 耗时={r['latency_s']}s "
                f"direct_hit={r['direct_hit']} activation_link_ids={r['activation_link_ids']} "
                f"events={r['event_types']}"
            )
            runs.append(r)
            time.sleep(delay)
        report[rid] = runs
    return report


def run_full_only_check(base_url, conn, full_text, id_to_title, timeout):
    report = {}
    for rid in FULL_ONLY_IDS:
        row = load_row_by_id(full_text, rid)
        question = c.question_variants(row)[0]
        expected_titles = c.expected_titles_for(expected_source_cell(row) or "")
        key_terms = c.extract_key_terms(row.get("期望答案要点", ""))
        r = ask_once(base_url, conn, question, id_to_title, expected_titles, key_terms, timeout)
        print(f"{rid}（未晋升，应仍为 full）: path_type={r['path_type']}")
        report[rid] = r
    return report


def run_gating_group(base_url, conn, full_text, extra_phrasings, timeout, delay, repeats=3):
    report = {}
    precondition_link_ids = {}
    precondition_point_ids = {}
    for f_id, pre_id in F_PRECONDITION.items():
        if pre_id == F1_PRE_ID:
            probe = (extra_phrasings.get(F1_PRE_ID) or F1_PRE_DEFAULT_VARIANTS)[0]
        else:
            row = load_row_by_id(full_text, pre_id)
            variants = c.question_variants(row) if row else []
            probe = variants[0] if variants else None
        link_id, point_id = (find_verified_link_by_probe(base_url, probe, timeout) if probe else (None, None))
        precondition_link_ids[f_id] = link_id
        precondition_point_ids[f_id] = point_id
        print(f"{f_id} 前置链接（{pre_id}，探测问法: {probe}）: link_id={link_id} point_id={point_id}")
        if not link_id:
            print(f"  ! {pre_id} 目前没有 verified 链接，{f_id} 守门测试会因为'没有可错误激活的对象'而空洞地通过，不代表守门机制被验证")

    for row in c.load_group("F", full_text):
        f_id = c.row_id(row)
        question = row["守门问题"]
        pre_link_id = precondition_link_ids.get(f_id)
        pre_point_id = precondition_point_ids.get(f_id)
        print(f"\n--- {f_id} 守门问题: {question} ---")
        runs = []
        failures = 0
        for i in range(repeats):
            t0 = time.time()
            _turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
            latency = time.time() - t0
            if result is None:
                print(f"  第{i+1}次: 未走到 retrieve，跳过")
                time.sleep(delay)
                continue
            trace = wait_trace(conn, result.get("answer_id"))
            activation_link_ids = json.loads(trace["activation_link_ids"] or "[]") if trace else []
            direct_point_ids = json.loads(trace["direct_point_ids"] or "[]") if trace else []
            gated_wrongly = bool(
                pre_link_id
                and pre_link_id in activation_link_ids
                and pre_point_id
                and pre_point_id in direct_point_ids
            )
            if gated_wrongly:
                failures += 1
            print(
                f"  第{i+1}次: path_type={result.get('path_type')} 耗时={latency:.1f}s "
                f"activation_link_ids={activation_link_ids} direct_point_ids={direct_point_ids} "
                f"{'! 守门失效' if gated_wrongly else '正常'}"
            )
            runs.append(
                {
                    "answer_id": result.get("answer_id"),
                    "path_type": result.get("path_type"),
                    "content": result.get("content"),
                    "activation_link_ids": activation_link_ids,
                    "direct_point_ids": direct_point_ids,
                    "gated_wrongly": gated_wrongly,
                }
            )
            time.sleep(delay)
        report[f_id] = {
            "precondition_link_id": pre_link_id,
            "precondition_exists": bool(pre_link_id),
            "runs": runs,
            "failures": failures,
        }
    return report


def run_e3_followup(base_url, timeout):
    """同一 session 内两轮追问，每轮都走 /session/turn 解析 + /answer/stream 带四元组
    （裸 POST /answer 不接受 subject/intent/audience/constraint，见 ask_once 的说明），
    否则"神通呢？"这类省略主语的追问即使补全了文字，检索也拿不到消歧所需的四元组。"""
    sess, _ = c.http_post_json(base_url, "/sessions", {})
    session_id = sess["session_id"]

    def turn_and_answer(user_input):
        turn, _ = c.http_post_json(
            base_url, "/session/turn", {"session_id": session_id, "user_input": user_input}, timeout=timeout
        )
        eq = turn.get("expanded_query") or {}
        expanded = eq.get("expanded_question") or user_input
        payload = {
            "question": expanded, "deep": False, "session_id": session_id,
            "subject": eq.get("subject") or "", "intent": eq.get("intent") or "",
            "audience": eq.get("audience") or "", "constraint": eq.get("constraint") or "",
        }
        ans = c.http_post_sse(base_url, "/answer/stream", payload, timeout=timeout) or {}
        return expanded, ans

    q1, ans1 = turn_and_answer("达梦 BUFFER 配多大？")
    q2, ans2 = turn_and_answer("神通呢？")

    expanded_mentions_shentong = "神通" in q2
    answer_mentions_shentong = "神通" in (ans2.get("content") or "") or "BUF_DATA_BUFFER_PAGES" in (ans2.get("content") or "")
    return {
        "session_id": session_id,
        "turn1_expanded": q1,
        "answer1": ans1.get("content"),
        "turn2_expanded": q2,
        "answer2": ans2.get("content"),
        "expanded_mentions_shentong": expanded_mentions_shentong,
        "answer_mentions_shentong": answer_mentions_shentong,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--baseline", default=None, help="P1 报告 jsonl 路径，用于耗时下降对比")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    extra_phrasings = load_extra_phrasings(args.extra_phrasing_file)
    baseline = load_baseline(args.baseline)
    full_text = c.load_plan_text()
    id_to_title = c.fetch_source_titles(args.base_url)
    conn = c.open_db(args.db_path)

    print("========== 1. 快路径重问（A1/A9/A12/T8/T12/T15） ==========")
    fastpath_report = run_fastpath_group(
        args.base_url, conn, full_text, extra_phrasings, id_to_title, args.timeout, args.delay, args.repeats
    )

    print("\n========== 2. 未晋升题仍应 full（A2/T13） ==========")
    full_only_report = run_full_only_check(args.base_url, conn, full_text, id_to_title, args.timeout)

    print("\n========== 3. 对象守门（F1/F2/F3） ==========")
    gating_report = run_gating_group(
        args.base_url, conn, full_text, extra_phrasings, args.timeout, args.delay, args.repeats
    )

    print("\n========== 4. E3 会话追问（达梦 BUFFER -> 神通呢？） ==========")
    e3_report = run_e3_followup(args.base_url, args.timeout)
    print(json.dumps(e3_report, ensure_ascii=False, indent=2))

    conn.close()

    print("\n========== P3 通过标准核对 ==========")
    all_fast = True
    for rid, runs in fastpath_report.items():
        not_fast = [r for r in runs if r["path_type"] != "fast"]
        no_activation = [r for r in runs if not r["activation_link_ids"]]
        no_success_event = [r for r in runs if "activation_success" not in (r["event_types"] or [])]
        avg_latency = sum(r["latency_s"] for r in runs) / len(runs)
        base_latency = baseline.get(rid)
        drop_pct = None
        if base_latency:
            drop_pct = (base_latency - avg_latency) / base_latency * 100
        ok = not not_fast and not no_activation
        all_fast = all_fast and ok
        print(
            f"{rid}: path_type=fast {'PASS' if not not_fast else 'FAIL'}；"
            f"activation_link_ids 非空 {'PASS' if not no_activation else 'FAIL'}；"
            f"activation_success 事件 {'PASS' if not no_success_event else 'FAIL(' + str(len(no_success_event)) + '/3 缺失)'}；"
            f"平均耗时={avg_latency:.2f}s"
            + (f"（P1基线={base_latency:.2f}s，下降{drop_pct:.0f}%，目标≥40%）" if drop_pct is not None else "（无 --baseline 对比数据）")
        )

    for rid, r in full_only_report.items():
        ok = r["path_type"] == "full"
        print(f"{rid}（未晋升）: path_type=full {'PASS' if ok else 'FAIL'}（实际 {r['path_type']}）")

    total_gating_failures = sum(g["failures"] for g in gating_report.values())
    for f_id, g in gating_report.items():
        print(f"{f_id}: 守门失效次数 = {g['failures']}/{args.repeats}")
    print(
        f"对象守门合计（F1+F2+F3, {args.repeats*3} 次）: "
        f"{'PASS' if total_gating_failures == 0 else 'FAIL'}（失效 {total_gating_failures} 次，目标 0）"
    )

    e3_ok = e3_report["expanded_mentions_shentong"] and e3_report["answer_mentions_shentong"]
    print(f"E3 追问正确转向神通: {'PASS' if e3_ok else 'FAIL（需人工核对 answer2 内容)'}")

    print(f"\n总体: {'PASS' if all_fast else 'FAIL'}（快路径部分，未含人工要点核对结果）")

    record = {
        "fastpath_report": fastpath_report,
        "full_only_report": full_only_report,
        "gating_report": gating_report,
        "e3_report": e3_report,
        "baseline_used": args.baseline,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p3_fastpath")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
