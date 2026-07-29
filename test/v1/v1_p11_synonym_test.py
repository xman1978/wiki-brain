#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P11：Subject 同义词收敛
（2026-07-24 新增能力，见 activation.md 附属表 `subject_synonyms` + 步骤 3a）。

前置依赖：P2 已跑过，F1_PRE 链接（问题「达梦怎么查询会话执行情况」）已 verified。
本脚本不重新培养 F1_PRE——直接按 link_id 复用，运行前请勿手工改动其四元组字段
（P2 脚本头部注释已提示这点）。

设计取舍（务必先读）：subject_synonym_gap 事件要求同一条 ActivationLink 的
intent/audience/constraint 全部匹配、仅 subject 未通过 coreContained——这个条件
能否被真实 Session Parser 自然产出，和 P3 M3、P8 头部注释描述的"subject 抖动
不可控"是同一个不确定性来源，不是本脚本能控制的变量。因此分两条轴：
  轴一（确定性，计入通过标准）：候选一旦存在（无论自然产生还是 --seed-manual
    人工兜底），confirm/reject 状态机与 Match 收敛效果是否正确。
  轴二（观测性，不计入通过标准）：花几轮、几种变体才能自然攒够
    synonym_gap_min/synonym_gap_distinct_min，只记录不判定。

用法：
  python3 test/v1/v1_p11_synonym_test.py --link-id <F1_PRE 的 link_id>
  python3 test/v1/v1_p11_synonym_test.py --link-id <...> --extra-phrasing-file test/v1/v1_p11_extra_phrasings.json
  python3 test/v1/v1_p11_synonym_test.py --link-id <...> --seed-manual
    # 若 --rounds 轮后仍未自然产生候选，改走人工插事件路径验证轴一状态机
      （仅验证状态机本身，不代表真实收敛概率，见方案 P11 步骤 9）
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

DEFAULT_VARIANTS = [
    "达梦怎么查询会话执行情况",
    "达梦如何查看当前会话的执行状态",
    "达梦怎么检查会话的运行状况",
    "达梦要怎样查询会话执行进展",
]


def load_variants(path):
    if not path:
        return DEFAULT_VARIANTS
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return data.get("variants", DEFAULT_VARIANTS)


def trace_activation_link_ids(conn, answer_id, wait_s=10):
    """API 响应不带 activation_link_ids，需按 answer_id 反查 trace（trace_write 异步，短轮询等待落库）。"""
    if not answer_id:
        return []
    trace = c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s=wait_s, interval_s=0.5)
    if not trace:
        return []
    return json.loads(trace["activation_link_ids"] or "[]")


def axis1_confirm_flow(base_url, conn, synonym_id, probe_question, link_id, timeout):
    print(f"\n--- 轴一：confirm 生效验证（synonym_id={synonym_id}） ---")
    result, status = c.http_post_json(base_url, f"/subject-synonyms/{synonym_id}/confirm", {}, timeout=30)
    ok_confirm = status == 200 and result and result.get("synonym_id") == synonym_id
    print(f"  confirm 响应 status={status} body={result}")

    syn = c.http_get_json(base_url, f"/subject-synonyms/{synonym_id}")
    ok_active = syn and syn.get("status") == "active"
    print(f"  confirm 后 status={syn.get('status') if syn else None}（期望 active）")

    turn, retrieve = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout)
    path_type = (retrieve or {}).get("path_type")
    hit_ids = trace_activation_link_ids(conn, (retrieve or {}).get("answer_id"))
    ok_match = path_type == "fast" and link_id in hit_ids
    print(f"  重问「{probe_question}」→ path_type={path_type} activation_link_ids={hit_ids}（期望 fast 且含 {link_id}）")

    return {
        "confirm_ok": ok_confirm,
        "status_active": ok_active,
        "match_converged": ok_match,
        "path_type": path_type,
        "activation_link_ids": hit_ids,
    }


def axis1_reject_flow(base_url, synonym_id, probe_question, timeout):
    print(f"\n--- 轴一：reject 不误收敛验证（synonym_id={synonym_id}） ---")
    result, status = c.http_post_json(base_url, f"/subject-synonyms/{synonym_id}/reject", {}, timeout=30)
    print(f"  reject 响应 status={status} body={result}")
    syn = c.http_get_json(base_url, f"/subject-synonyms/{synonym_id}")
    ok_rejected = syn and syn.get("status") == "rejected"

    turn, retrieve = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout)
    path_type = (retrieve or {}).get("path_type")
    ok_no_converge = path_type != "fast"
    print(f"  reject 后重问「{probe_question}」→ path_type={path_type}（期望非 fast，即未被误收敛）")

    # 重复触发同一 pair 不应再新增候选
    result2, status2 = c.http_post_json(base_url, "/study/run", {}, timeout=180)
    candidates_after = c.http_get_json(base_url, "/subject-synonyms?status=candidate") or []
    revived = any(row.get("synonym_id") == synonym_id for row in candidates_after)
    print(f"  study/run 后该 synonym_id 是否复活为 candidate: {revived}（期望 False）")

    return {
        "reject_ok": status == 200,
        "status_rejected": ok_rejected,
        "no_converge": ok_no_converge,
        "not_revived": not revived,
    }


def cultivate_and_watch_gap(base_url, conn, variants, delay, timeout):
    """轴二观测：连续问 subject 变体，观察 subject_synonym_gap 是否自然累积。"""
    print("\n--- 轴二：subject 变体培养（观测，不设通过标准） ---")
    watermark = None
    row = conn.execute("SELECT MAX(created_at) AS m FROM learning_events").fetchone()
    if row and row["m"]:
        watermark = row["m"]

    for i, q in enumerate(variants, 1):
        print(f"  轮次{i}: {q}")
        try:
            c.ask_via_session(base_url, q, deep=False, timeout=timeout)
        except Exception as e:
            print(f"    ! 出错: {e}")
        time.sleep(delay)

    gap_events = c.db_learning_events_by_type(conn, "subject_synonym_gap", since_created_at=watermark)
    print(f"  本轮共产生 {len(gap_events)} 条 subject_synonym_gap 事件")
    for ev in gap_events:
        payload = json.loads(ev["payload"] or "{}")
        print(f"    trace={ev['trace_id']} query_subject={payload.get('query_subject')!r} "
              f"observed_subject={payload.get('observed_subject')!r}")
    return gap_events


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--db-path", default=None)
    parser.add_argument("--link-id", required=True, help="P2 培养出的 F1_PRE verified link_id")
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--rounds", type=int, default=1, help="轴二培养轮次（每轮问一遍全部变体）")
    parser.add_argument("--delay", type=float, default=1.0)
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--seed-manual", action="store_true",
                         help="轴二若干轮后仍未自然产生候选时，直接扫描已有 candidate 走轴一（不再等待自然收敛）")
    args = parser.parse_args()

    conn = c.open_db(args.db_path)
    variants = load_variants(args.extra_phrasing_file)

    link = c.db_activation_link(conn, args.link_id)
    if not link:
        print(f"! link_id={args.link_id} 不存在，请先跑 P2 培养 F1_PRE", file=sys.stderr)
        sys.exit(1)
    print(f"复用链接: link_id={link['link_id']} status={link['status']} point_id={link['point_id']}")
    if link["status"] != "verified":
        print(f"! 警告: 链接状态为 {link['status']}，不是 verified，轴一 M 系列结果仅供参考")

    all_gap_events = []
    for r in range(args.rounds):
        all_gap_events += cultivate_and_watch_gap(args.base_url, conn, variants, args.delay, args.timeout)
        result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
        print(f"  study/run 第 {r + 1} 轮结果: {result}")

    candidates = c.http_get_json(args.base_url, "/subject-synonyms?status=candidate") or []
    print(f"\n当前 candidate 数量: {len(candidates)}")
    for row in candidates:
        print(f"  synonym_id={row['synonym_id']} term={row['term']!r} canonical={row['canonical']!r} source={row['source']}")

    if not candidates:
        msg = "轴二未自然产生候选（这是记录项，不是失败）"
        if not args.seed_manual:
            print(f"! {msg}；如需继续验证轴一状态机，重跑并加 --seed-manual，"
                  f"或人工在 DB 里插入满足 synonym_gap_min/synonym_gap_distinct_min 的"
                  f"subject_synonym_gap 事件后重跑本脚本（见方案 P11 步骤 9）。")
        else:
            print(f"! {msg}，且当前 DB 里也没有可用 candidate，--seed-manual 无法自动造数据"
                  f"（本脚本不代为写库，需人工按 trace.md 步骤 3 的 payload 格式插入 learning_events "
                  f"后重跑 /study/run）。")
        conn.close()
        return

    # 轴一：取一条 candidate 走 confirm 全流程；若有第二条则走 reject 对照
    axis1_results = {}
    target = candidates[0]
    probe = variants[0]
    axis1_results["confirm"] = axis1_confirm_flow(
        args.base_url, conn, target["synonym_id"], probe, args.link_id, args.timeout
    )

    if len(candidates) > 1:
        reject_target = candidates[1]
        axis1_results["reject"] = axis1_reject_flow(args.base_url, reject_target["synonym_id"], probe, args.timeout)
    else:
        print("\n! 只攒到 1 条 candidate，reject 对照分支跳过（不影响 confirm 分支通过标准）")

    print("\n=== P11 结果汇总 ===")
    print(json.dumps({
        "gap_events_total": len(all_gap_events),
        "candidates_found": len(candidates),
        "axis1": axis1_results,
    }, ensure_ascii=False, indent=2))

    confirm_pass = all(axis1_results.get("confirm", {}).get(k) for k in ("confirm_ok", "status_active", "match_converged"))
    print(f"\n轴一通过标准（confirm 分支）: {'PASS' if confirm_pass else 'FAIL'}")
    if "reject" in axis1_results:
        reject_pass = all(axis1_results["reject"].get(k) for k in ("reject_ok", "status_rejected", "no_converge", "not_revived"))
        print(f"轴一通过标准（reject 分支）: {'PASS' if reject_pass else 'FAIL'}")

    conn.close()


if __name__ == "__main__":
    main()
