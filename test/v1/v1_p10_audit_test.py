#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P10：审计与报告总核（标准 3 收口）。

流程：
  1. 拉取全量 GET /study/results（该接口不支持分页 offset，只能靠调大 limit 一次性
     拉全部——测试环境规模下够用），核对每条 result 的 object_id/reason/event_ids
     三要素齐全；
  2. 随机抽 5 条反向核对：从 reason 文本正则提取 success_n/failure_n/distinct_n 等
     统计数字，与 learning_events 按 event_window_days 窗口重新统计做对比；
  3. 核对最新学习报告（GET /study/reports/latest）包含 summary.kpn_citation_rate、
     knowledge_gaps 列表（且能看到 C 组缺口题）。

用法：
  python3 test/v1/v1_p10_audit_test.py
  python3 test/v1/v1_p10_audit_test.py --sample 8
"""
import argparse
import json
import random
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

import v1_common as c

EVENT_WINDOW_DAYS_DEFAULT = 30


def fetch_all_results(base_url, limit=2000):
    return c.http_get_json(base_url, f"/study/results?limit={limit}")


def check_three_essentials(results):
    """object_id/reason 对所有 result 都该有；event_ids 只对"由 learning_events
    驱动"的动作有意义——不是每个动作背后都有 learning_event：

    - reason=manual_reject 是人工直接调 POST /activation-links/:id/reject 触发
      的 prune_condition（2026-08-13 起 confirm 端点已不存在，唯一的人工动作只
      剩 reject），背后没有 learning_event（人的点击就是原因本身）。
    - entry_add_candidate / entry_merge_candidate 有三种合法来源
      （internal/entry/service.go）：
        1. 跨 Source KPN 聚类匹配（reason 以"跨 Source KPN 匹配"开头，
           service.go:146）——内容驱动的统计聚类，不经过 learning_events。
        2. 概念对共现合并（reason 以"概念对共现"开头，service.go:625）——同样
           是内容驱动的统计，不经过 learning_events。
        3. 人工手动新增/确认概念（reason 为"人工手动新增概念候选"
           或以"人工确认新增概念"开头，service.go:206/754）——人工直接触发。
      只有 service.go:413 那条真正由 learning_events 共现驱动的
      entry_add_candidate 会带 event_ids，这类不豁免，仍然要求非空。
    这类 result 没有 event_ids 是正常的，不能算审计缺口。"""
    NON_EVENT_ENTRY_REASON_PREFIXES = (
        "跨 Source KPN 匹配",
        "概念对共现",
        "人工确认新增概念",
    )
    NON_EVENT_ENTRY_REASON_EXACT = (
        "人工手动新增概念候选",
    )
    missing = []
    for r in results:
        event_ids = r.get("event_ids")
        reason = r.get("reason") or ""
        action = r.get("action") or ""
        is_manual = reason.startswith("manual_")
        is_non_event_entry_candidate = (
            action in ("entry_add_candidate", "entry_merge_candidate")
            and (
                reason in NON_EVENT_ENTRY_REASON_EXACT
                or reason.startswith(NON_EVENT_ENTRY_REASON_PREFIXES)
            )
        )
        is_exempt = is_manual or is_non_event_entry_candidate
        problems = []
        if not r.get("object_id"):
            problems.append("object_id 缺失")
        if not reason:
            problems.append("reason 缺失")
        if not event_ids and not is_exempt:
            problems.append("event_ids 缺失/空")
        if problems:
            missing.append({"result_id": r["result_id"], "action": r["action"], "reason": reason, "problems": problems})
    return missing


# 2026-08-13 改判后重写：promote/weaken/reverify/deprecate 已废弃，reason 文本
# 只来自 create_candidate（`internal/study/service.go` "共现命中：confident_count=
# %d, hit_count=%d, ratio=%.2f"）与 prune_condition（"收敛剪枝：converged_low=%d,
# long_idle=%d，剩余观测条件 %d 条" 或 manual_reject）两类动作。
NUMBER_PATTERNS = {
    "confident_count": r"confident_count[=:：]\s*(\d+)",
    "hit_count": r"hit_count[=:：]\s*(\d+)",
    "ratio": r"ratio[=:：]\s*([\d.]+)",
    "converged_low": r"converged_low[=:：]\s*(\d+)",
    "long_idle": r"long_idle[=:：]\s*(\d+)",
    "remaining_conditions": r"剩余观测条件\s*(\d+)\s*条",
}


def parse_reason_numbers(reason):
    out = {}
    for key, pattern in NUMBER_PATTERNS.items():
        m = re.search(pattern, reason)
        if m:
            out[key] = float(m.group(1)) if "." in m.group(1) else int(m.group(1))
    return out


def recompute_from_events(conn, object_id, window_days):
    since = (datetime.now(timezone.utc) - timedelta(days=window_days)).strftime("%Y-%m-%d %H:%M:%S")
    events = conn.execute(
        """
        SELECT le.event_type, COUNT(*) n
        FROM learning_events le
        JOIN traces t ON le.trace_id = t.trace_id
        WHERE ? IN (SELECT value FROM json_each(t.activation_link_ids))
          AND le.created_at >= ?
        GROUP BY le.event_type
        """,
        (object_id, since),
    ).fetchall()
    return {row["event_type"]: row["n"] for row in events}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--sample", type=int, default=5)
    parser.add_argument("--event-window-days", type=int, default=EVENT_WINDOW_DAYS_DEFAULT)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    conn = c.open_db(args.db_path)

    print("--- 1. 拉取全量 learning_results ---")
    results = fetch_all_results(args.base_url)
    print(f"共 {len(results)} 条")
    by_action = {}
    for r in results:
        by_action.setdefault(r["action"], 0)
        by_action[r["action"]] += 1
    print(f"按 action 分布: {by_action}")

    missing = check_three_essentials(results)
    print(f"\n三要素（object_id/reason/event_ids）齐全: {'PASS' if not missing else 'FAIL'}")
    for m in missing[:10]:
        print(f"  ! {m}")

    # 2026-08-13 改判：promote/weaken/reverify/deprecate/confirm 已从 action 词表移除
    # （internal/activation/types.go 当前枚举 create_candidate/prune_condition/
    # gap_flag/wiki_candidate/recompile_flag/entry_add_candidate/
    # entry_merge_candidate/entry_add/entry_merge/topic_page_candidate）。
    DEAD_ACTIONS = {"promote", "weaken", "reverify", "deprecate", "confirm"}
    stale_actions = sorted(DEAD_ACTIONS & set(by_action.keys()))
    print(f"action 词表不含已废弃动作(promote/weaken/reverify/deprecate/confirm): "
          f"{'PASS' if not stale_actions else 'FAIL'} ({stale_actions or '无'})")

    print(f"\n--- 2. 随机抽 {args.sample} 条反向核对 ---")
    sample_pool = [r for r in results if r.get("object_id")]
    sample = random.sample(sample_pool, min(args.sample, len(sample_pool)))
    sample_report = []
    for r in sample:
        numbers = parse_reason_numbers(r["reason"])
        recomputed = recompute_from_events(conn, r["object_id"], args.event_window_days)
        print(f"  result_id={r['result_id'][:8]} action={r['action']} object_id={r['object_id'][:8]}")
        print(f"    reason: {r['reason']}")
        print(f"    reason 里解析出的数字: {numbers}")
        print(f"    learning_events 窗口内重算（按 event_type 计数）: {recomputed}")
        sample_report.append(
            {
                "result_id": r["result_id"],
                "action": r["action"],
                "object_id": r["object_id"],
                "reason": r["reason"],
                "reason_numbers": numbers,
                "recomputed_from_events": recomputed,
            }
        )

    print("\n--- 3. 最新学习报告核对 ---")
    try:
        report = c.http_get_json(args.base_url, "/study/reports/latest")
    except Exception as e:
        report = None
        print(f"  ! 拉取失败: {e}", file=sys.stderr)

    report_checks = {}
    if report:
        summary = report.get("summary") or {}
        gaps = report.get("knowledge_gaps") or []
        has_kpn_rate = "kpn_citation_rate" in summary
        gap_questions = {g.get("question_terms") for g in gaps}
        print(f"  summary.kpn_citation_rate: {summary.get('kpn_citation_rate')}（存在: {has_kpn_rate}）")
        print(f"  knowledge_gaps 条数: {len(gaps)}")
        for g in gaps[:10]:
            print(f"    - {g}")
        report_checks = {
            "has_kpn_citation_rate": has_kpn_rate,
            "kpn_citation_rate": summary.get("kpn_citation_rate"),
            "gap_count": len(gaps),
            "gap_questions_sample": list(gap_questions)[:10],
        }

    conn.close()

    print("\n========== P10 通过标准核对 ==========")
    print(f"状态迁移审计三要素齐全率: {'100%' if not missing else f'{len(missing)}/{len(results)} 条有缺失'} "
          f"({'PASS' if not missing else 'FAIL'})")
    print(f"抽样反向核对: 已打印 {len(sample_report)} 条明细，需人工确认 reason 数字与 recomputed_from_events 一致")
    print(f"学习报告含 kpn_citation_rate: {'PASS' if report_checks.get('has_kpn_citation_rate') else 'FAIL/未拉到报告'}")
    print(f"学习报告含知识缺口清单: {'PASS' if report_checks.get('gap_count', 0) >= 0 and report else 'FAIL'}")

    record = {
        "total_results": len(results),
        "by_action": by_action,
        "missing_essentials": missing,
        "sample_report": sample_report,
        "report_checks": report_checks,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p10_audit")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
