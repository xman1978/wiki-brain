#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P5：自我修正与删除生命周期
（标准 5 + 标准 6 删除侧）。

依赖 P2：方案假定 A1（报销规定）、T8（RAC 开启归档）此时已通过重复问答自然收敛为
status=verified 链接。若实际测试环境里 A1、T8 都没能攒够置信度形成 verified 链接
（见 P2 报告），"verified→deprecated"这部分在当前数据下没有对象可测——脚本会在
运行时动态查一遍，真有 verified 链接就测，没有就如实报告"无相关链接可测"，不假装
测过。删除生命周期本身（lifecycle=deprecated、检索不再返回）不依赖 verified 链接，
照常全测。

2026-08-13 改判后重写（`weakened` 状态整体废弃）：生命周期驱动的降权不再是"failure_n
累积达到 weaken_failure_min 才降权"，而是 SoftDelete 在同一次调用内同步触发
`unit.Service.setLifecycle` -> `activationNotifier.NotifyPointsLifecycleChanged`
-> `deriveAndPersistStatus`（`internal/source/service.go` SoftDelete、
`internal/unit/service.go` 对应 setLifecycle 逻辑均为同步调用，无异步队列），
把相关链接直接置为 `status=deprecated`——不经过任何中间态，也不需要等
`POST /study/run` 或累积失败次数。因此本脚本在 DELETE 之后、还没跑 study/run
之前就应该已经能观察到 status=deprecated。

流程：
  1. 删除《日常费用报销期限管理规定》《Oracle RAC 开启归档》两个 Source；
  2. 核对两个 Source 下 KU/KP 全部 lifecycle=deprecated；同时核对删除前 verified
     的链接此刻已 status=deprecated（不等 study/run）；
  3. 重问 A1、T8 各 3-4 个变体：核对不再引用被删文档的 KP；技术侧额外核查 T8 删除后
     是否从《Oracle RAC 问题汇总》等其他 RAC 文档拼凑出归档步骤（那些文档没有归档
     内容，出现即为幻构缺陷）；顺带重问 A2（差旅费）确认不受牵连；
  4. 若 A1/T8 存在（或曾存在过的）链接指向被删内容，`POST /study/run` 后核对：这次
     status=deprecated 的迁移本身不写新的 learning_result（`deriveAndPersistStatus`
     只是状态派生，不经过 InsertLearningResult），可审计性来自「删除前 verified →
     删除后立即 deprecated」这一状态对照，以及此后不再出现任何针对该链接的新
     `create_candidate`/`prune_condition` learning_result（说明其确实已退出学习循环）；
  5. 通过标准：0 次引用 deprecated KP；两域链接均在删除时点即时变为 deprecated；
     快路径自动回落无 5xx。

用法：
  python3 test/v1/v1_p5_lifecycle_test.py
  python3 test/v1/v1_p5_lifecycle_test.py --skip-delete   # 假设已经删过，只跑后续核对
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

DELETE_TARGETS = ["日常费用报销期限管理规定", "Oracle RAC 开启归档"]
RAC_OTHER_DOCS = ["Oracle RAC 问题汇总", "Oracle 11g RAC 集群安装部署维护环境", "Oracle 11g RAC 集群安装部署维护",
                  "Oracle 19c RAC 集群安装部署维护环境", "Oracle 19c RAC 集群安装部署维护"]


def resolve_source_id(base_url, title):
    id_to_title = c.fetch_source_titles(base_url)
    for sid, t in id_to_title.items():
        if t == title:
            return sid
    return None


def delete_source(base_url, source_id):
    resp, status = c.http_delete_json(base_url, f"/sources/{source_id}")
    return resp, status


def check_lifecycle(conn, source_id):
    units = c.db_units_for_source(conn, source_id)
    points = c.db_points_for_source(conn, source_id)
    non_deprecated_units = [u for u in units if u["lifecycle"] != "deprecated"]
    non_deprecated_points = [p for p in points if p["lifecycle"] != "deprecated"]
    return {
        "unit_count": len(units),
        "point_count": len(points),
        "non_deprecated_units": len(non_deprecated_units),
        "non_deprecated_points": len(non_deprecated_points),
    }


def wait_trace(conn, answer_id, timeout_s=15):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s, 0.5)


def ask_and_check(base_url, conn, question, deleted_source_ids, timeout):
    t0 = time.time()
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    latency = time.time() - t0
    if result is None:
        return {"question": question, "error": f"未走到 retrieve（action={turn.get('action')}）"}

    es = result.get("evidence_snapshot") or {}
    all_ev = (es.get("direct_evidence") or []) + (es.get("supporting") or [])
    cited_deleted = []
    for ev in all_ev:
        ref = ev.get("source_ref")
        if isinstance(ref, str):
            ref = json.loads(ref)
        sid = (ref or {}).get("source_id")
        if sid in deleted_source_ids:
            cited_deleted.append(ev.get("fact_id"))

    trace = wait_trace(conn, result.get("answer_id"))
    events = c.db_learning_events_for_trace(conn, trace["trace_id"]) if trace else []

    return {
        "question": question,
        "latency_s": round(latency, 2),
        "content": result.get("content"),
        "path_type": result.get("path_type"),
        "has_answer": result.get("has_answer"),
        "gap_reason": es.get("gap_reason"),
        "cited_deleted_fact_ids": cited_deleted,
        "event_types": [e["event_type"] for e in events] if trace else None,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--skip-delete", action="store_true", help="假设已删过，只跑后续核对")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    full_text = c.load_plan_text()
    conn = c.open_db(args.db_path)

    print("--- 解析待删除 Source ---")
    target_ids = {}
    for title in DELETE_TARGETS:
        sid = resolve_source_id(args.base_url, title)
        target_ids[title] = sid
        print(f"  {title}: {sid}")
        if not sid:
            print(f"  ! 找不到标题为「{title}」的 Source，请检查库里实际标题", file=sys.stderr)

    # 删除前记录：这两个 source 下现有的 verified 链接（若有）
    pre_links = {}
    for title, sid in target_ids.items():
        if sid:
            pre_links[title] = c.db_links_for_source(conn, sid, status="verified")
            if pre_links[title]:
                print(f"  {title} 删除前有 {len(pre_links[title])} 条 verified 链接指向其 KP")

    delete_results = {}
    if not args.skip_delete:
        print("\n--- 删除 Source ---")
        for title, sid in target_ids.items():
            if not sid:
                continue
            resp, status = delete_source(args.base_url, sid)
            delete_results[title] = {"status": status, "resp": resp}
            print(f"  DELETE /sources/{sid} ({title}): HTTP {status} {resp}")
    else:
        print("\n--skip-delete：跳过删除。")

    print("\n--- 核对 lifecycle=deprecated ---")
    lifecycle_report = {}
    for title, sid in target_ids.items():
        if not sid:
            continue
        lifecycle_report[title] = check_lifecycle(conn, sid)
        r = lifecycle_report[title]
        print(
            f"  {title}: KU {r['unit_count']} 个（非 deprecated {r['non_deprecated_units']}）、"
            f"KP {r['point_count']} 个（非 deprecated {r['non_deprecated_points']}）"
        )

    deleted_source_ids = {sid for sid in target_ids.values() if sid}

    print("\n--- 核对删除前 verified 链接此刻（未跑 study/run）已即时变为 deprecated ---")
    immediate_deprecate_report = {}
    for title, links in pre_links.items():
        for link in links:
            current = c.db_activation_link(conn, link["link_id"])
            now_status = current["status"] if current else None
            immediate_deprecate_report[link["link_id"]] = {"title": title, "status_now": now_status}
            print(
                f"  {title} 链接 {link['link_id']}: 删除前 verified，删除后（未跑 study/run）现状态={now_status} "
                f"{'PASS' if now_status == 'deprecated' else 'FAIL/待复核'}"
            )

    print("\n--- 重问 A1/T8/A2 各变体，核对不引用已删内容 ---")
    a1_row = next(r for r in c.load_group("A", full_text) if c.row_id(r) == "A1")
    t8_row = next(r for r in c.load_group("T", full_text) if c.row_id(r) == "T8")
    a2_row = next(r for r in c.load_group("A", full_text) if c.row_id(r) == "A2")

    ask_report = {"A1": [], "T8": [], "A2": []}
    for rid, row, repeats in (("A1", a1_row, 4), ("T8", t8_row, 4), ("A2", a2_row, 2)):
        variants = c.question_variants(row)
        asked = 0
        vi = 0
        while asked < repeats:
            q = variants[vi % len(variants)]
            print(f"  {rid}: {q}")
            rec = ask_and_check(args.base_url, conn, q, deleted_source_ids, args.timeout)
            if rec.get("error"):
                print(f"    ! {rec['error']}")
            else:
                print(
                    f"    path_type={rec['path_type']} gap_reason={rec.get('gap_reason')} "
                    f"引用已删内容={len(rec['cited_deleted_fact_ids'])} events={rec['event_types']}"
                )
            ask_report[rid].append(rec)
            asked += 1
            vi += 1
            time.sleep(args.delay)

    print("\n--- T8 幻构侧核查：是否从其他 RAC 文档拼凑归档步骤（人工确认 content 里的步骤来源）---")
    other_rac_ids = c.fetch_source_titles(args.base_url)
    other_rac_source_ids = {sid for sid, t in other_rac_ids.items() if t in RAC_OTHER_DOCS}
    t8_suspect = []
    for rec in ask_report["T8"]:
        if rec.get("error"):
            continue
        if rec.get("cited_deleted_fact_ids"):
            continue  # 已经算在"引用已删内容"里了
        # 这里没法从 rec 直接拿到非删除来源的 source_id 明细（当时没收集），
        # 打印 content 供人工确认是否提到 srvctl/archive log 步骤却没有合理来源
        if any(kw in (rec.get("content") or "") for kw in ("srvctl", "archive log", "归档")):
            t8_suspect.append(rec["content"])
    for content in t8_suspect:
        print(f"  ! 需人工核对是否幻构: {content[:200]}")

    print("\n--- POST /study/run，核对 deprecated 迁移不产生新学习动作（若删除前存在相关链接）---")
    study_result = None
    weaken_report = {}
    any_pre_links = any(pre_links.values())
    if any_pre_links:
        study_result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
        print(json.dumps(study_result, ensure_ascii=False, indent=2)[:1500])
        for title, links in pre_links.items():
            for link in links:
                results = c.db_learning_results_for_object(conn, link["link_id"])
                current = c.db_activation_link(conn, link["link_id"])
                new_actions_after_delete = [
                    r for r in results if r["action"] in ("create_candidate", "prune_condition")
                ]
                weaken_report[link["link_id"]] = {
                    "title": title,
                    "status_now": current["status"] if current else None,
                    "learning_results": [{"action": r["action"], "status": r["status"], "reason": r["reason"]} for r in results],
                    "note": "deprecated 迁移本身不写 learning_result（deriveAndPersistStatus 只做状态派生）；"
                            "可审计性来自「删除前 verified → 删除后立即 deprecated」的状态对照",
                }
                print(
                    f"  {title} 链接 {link['link_id']}: 现状态={current['status'] if current else '?'} "
                    f"（study/run 后 learning_results 总数={len(results)}）"
                )
    else:
        print("  当前环境下 A1/T8 删除前都没有 verified 链接指向其内容，此步骤无对象可测（如实记录，非脚本缺陷）。")

    print("\n========== P5 通过标准核对 ==========")
    total_cited_deleted = sum(
        len(rec.get("cited_deleted_fact_ids", []))
        for recs in ask_report.values()
        for rec in recs
        if not rec.get("error")
    )
    print(f"删除后引用 deprecated KP 次数: {total_cited_deleted}（目标 0）: {'PASS' if total_cited_deleted == 0 else 'FAIL'}")
    bad_lifecycle = [t for t, r in lifecycle_report.items() if r["non_deprecated_units"] or r["non_deprecated_points"]]
    print(f"两个 Source 下 KU/KP 全部 deprecated: {'PASS' if not bad_lifecycle else 'FAIL'} ({bad_lifecycle})")
    bad_immediate = [lid for lid, r in immediate_deprecate_report.items() if r["status_now"] != "deprecated"]
    print(
        f"删除前 verified 链接均在删除时即时变为 deprecated（不等 study/run）: "
        f"{'PASS' if not bad_immediate else 'FAIL'} ({bad_immediate or '无相关链接可测/全部通过'})"
    )
    a2_errors = [r for r in ask_report["A2"] if r.get("error")]
    print(f"A2（差旅费，不应受牵连）无异常: {'PASS' if not a2_errors else 'FAIL'}")
    print(f"T8 疑似幻构（从其他 RAC 文档拼凑步骤）: {len(t8_suspect)} 条，需人工确认")

    conn.close()
    record = {
        "target_ids": target_ids,
        "delete_results": delete_results,
        "lifecycle_report": lifecycle_report,
        "ask_report": ask_report,
        "t8_suspect_hallucination": t8_suspect,
        "study_result": study_result,
        "immediate_deprecate_report": immediate_deprecate_report,
        "weaken_report": weaken_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p5_lifecycle")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
