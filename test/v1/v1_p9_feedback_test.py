#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P9：用户反馈通道（标准 8）。

默认拿 A12（P2/P3 里已验证的干净快路径样例）当"纠正"目标，T15 当"有用"反馈目标——
如果这两条链接在你的环境里状态不同（比如换了别的题被晋升），用 --correction-id/
--positive-id 指定题号覆盖。

流程：
  1. 对 A12 的一次快路径回答连续提交 2 次 correction 反馈；
  2. 核对 learning_events 出现 user_correction 且 payload.link_ids 包含 A12 链接；
  3. POST /study/run：correction_weight=2，2 次纠正折算 4 次 failure，核对触发
     verified->weakened（weaken_failure_min=3、weaken_ratio_min=0.5，需要链接原有
     的 adopt_count 让比值算得过——见脚本内联注释）；
  4. 对 T15 提交 1 次 positive 反馈，核对链接状态不变；
  5. 通过标准：纠正的加速效果在 learning_result 的 reason 数字里可见。

方案要求的"对照组：仅 2 次自然 activation_failure 不触发降权"没法在黑盒环境里精确
构造（无法强制回答判定为"未命中"），本脚本改为直接核对 correction_weight 的算术
效果（2 次纠正 = 4 次等效 failure，达到阈值），实际对照实验请人工在 Page 上观察。

用法：
  python3 test/v1/v1_p9_feedback_test.py
  python3 test/v1/v1_p9_feedback_test.py --correction-id A12 --positive-id T15
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c


def load_row(full_text, qid):
    for g in ("A", "T"):
        for row in c.load_group(g, full_text):
            if c.row_id(row) == qid:
                return row
    return None


def ask_and_get_trace(base_url, conn, question, timeout):
    _turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    if not result:
        return None, None
    trace = c.poll_until(lambda: c.db_trace_by_answer_id(conn, result.get("answer_id")), 15, 0.5)
    return result, trace


def submit_feedback(base_url, trace_id, fb_type, content=""):
    payload = {"type": fb_type, "content": content}
    resp, status = c.http_post_json(base_url, f"/traces/{trace_id}/feedback", payload)
    return resp, status


def link_for_trace(conn, trace_id):
    trace = conn.execute("SELECT activation_link_ids FROM traces WHERE trace_id=?", (trace_id,)).fetchone()
    if not trace:
        return []
    return json.loads(trace["activation_link_ids"] or "[]")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--correction-id", default="A12")
    parser.add_argument("--positive-id", default="T15")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    full_text = c.load_plan_text()
    conn = c.open_db(args.db_path)

    correction_row = load_row(full_text, args.correction_id)
    positive_row = load_row(full_text, args.positive_id)
    correction_question = c.question_variants(correction_row)[0]
    positive_question = c.question_variants(positive_row)[0]

    print(f"--- 纠正目标: {args.correction_id} 「{correction_question}」 ---")
    result, trace = ask_and_get_trace(args.base_url, conn, correction_question, args.timeout)
    if not trace:
        print("  ! 没能拿到 trace，无法继续", file=sys.stderr)
        sys.exit(1)
    print(f"  path_type={result.get('path_type')} trace_id={trace['trace_id']}")
    link_ids = link_for_trace(conn, trace["trace_id"])
    if not link_ids:
        print(
            f"  ! {args.correction_id} 这次回答没有 activation_link_ids（不是快路径命中），"
            f"correction 反馈测的是「verified 链接的纠正加速」，建议换一个已验证过快路径生效的题号",
            file=sys.stderr,
        )

    link_before = {lid: c.db_activation_link(conn, lid) for lid in link_ids}
    for lid, link in link_before.items():
        print(f"  反馈前链接 {lid}: status={link['status']} adopt={link['adopt_count']} fail={link['fail_count']}")

    print("\n  提交 2 次 correction 反馈...")
    feedback_resps = []
    for i in range(2):
        resp, status = submit_feedback(args.base_url, trace["trace_id"], "correction", f"P9 测试纠正 #{i+1}")
        print(f"    第{i+1}次: HTTP {status} {resp}")
        feedback_resps.append(resp)
        time.sleep(args.delay)

    events = c.db_learning_events_for_trace(conn, trace["trace_id"], event_type="user_correction")
    print(f"\n  user_correction 事件数: {len(events)}（应为 2）")
    for e in events:
        payload = json.loads(e["payload"])
        print(f"    payload.link_ids={payload.get('link_ids')}")

    print("\n>>> POST /study/run")
    study_result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
    print(json.dumps(study_result, ensure_ascii=False, indent=2)[:1500])

    print("\n  纠正后链接状态与 learning_result：")
    weaken_report = {}
    for lid in link_ids:
        current = c.db_activation_link(conn, lid)
        results = c.db_learning_results_for_object(conn, lid)
        weaken_results = [r for r in results if r["action"] == "weaken"]
        print(f"    {lid}: status={current['status']} fail_count={current['fail_count']}")
        for r in weaken_results:
            print(f"      weaken learning_result: reason={r['reason']}")
        weaken_report[lid] = {
            "status_before": link_before[lid]["status"] if lid in link_before else None,
            "status_after": current["status"],
            "weaken_results": [{"reason": r["reason"], "status": r["status"]} for r in weaken_results],
        }

    print(f"\n--- 有用反馈目标: {args.positive_id} 「{positive_question}」 ---")
    pos_result, pos_trace = ask_and_get_trace(args.base_url, conn, positive_question, args.timeout)
    pos_link_ids = link_for_trace(conn, pos_trace["trace_id"]) if pos_trace else []
    pos_link_before = {lid: dict(c.db_activation_link(conn, lid)) for lid in pos_link_ids}
    if pos_trace:
        resp, status = submit_feedback(args.base_url, pos_trace["trace_id"], "positive", "P9 测试有用反馈")
        print(f"  positive 反馈: HTTP {status} {resp}")
    pos_link_after = {lid: c.db_activation_link(conn, lid) for lid in pos_link_ids}
    pos_unchanged = all(
        pos_link_before[lid]["status"] == pos_link_after[lid]["status"] for lid in pos_link_ids
    )
    print(f"  positive 反馈后链接状态是否不变: {pos_unchanged}")

    conn.close()

    print("\n========== P9 通过标准核对 ==========")
    print(f"user_correction 事件数=2: {'PASS' if len(events) == 2 else 'FAIL'}")
    payload_has_link_ids = all(json.loads(e["payload"]).get("link_ids") for e in events)
    print(f"payload 含 link_ids: {'PASS' if payload_has_link_ids else 'FAIL'}")
    any_weakened = any(w["status_after"] == "weakened" for w in weaken_report.values())
    print(f"纠正触发降权（verified->weakened）: {'PASS' if any_weakened else 'FAIL/未达阈值，看上面 fail_count 与 reason'}")
    print(f"positive 反馈不改变链接状态: {'PASS' if pos_unchanged else 'FAIL'}")

    record = {
        "correction_id": args.correction_id,
        "correction_trace_id": trace["trace_id"],
        "feedback_resps": feedback_resps,
        "user_correction_events": [dict(e) for e in events],
        "study_result": study_result,
        "weaken_report": weaken_report,
        "positive_id": args.positive_id,
        "positive_unchanged": pos_unchanged,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p9_feedback")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
