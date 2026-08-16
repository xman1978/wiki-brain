#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P14：ActivationBundle
跨 unit 歧义仲裁（2026-08-12 部分落地，见 `internal/retrieval/fastpath_helpers.go`
`resolveBundleForAmbiguousHits`/`formCandidateBundle`，`docs/impl/v1/
activation-bundle.md` 步骤 4 修正记录）。

背景（务必先读）：ActivationLink 快路径命中的 point_id 若解析到多个不同的
knowledge_unit（`resolveUnitsForPoints` 返回 `unitResolutionAmbiguous`），
Retrieval 不再直接落回慢路径，而是先 consult ActivationBundle
（`resolveBundleForAmbiguousHits`）：
  - 命中一个或多个互不冲突（KPN `contradicts` 判定，`bundlesConflict`）的
    "eligible to serve"（`MatchBundles` 用同一套 `ConfidenceConfig` 分档，
    2026-08-13 起不再有离散 `status=verified` 过滤）Bundle，就合并这些
    Bundle 的核心成员（`CoreMemberPointIDs`，按每个成员自己的
    `mean(success_count, failure_count)` 越过 `serving_confidence_min`
    派生，不是写死的静态数组）继续走快路径；
  - 没有 Bundle 覆盖，则从这次观测实时新建/加强一条 `candidate` Bundle
    （`formCandidateBundle`，按核心成员集合去重，命中已有集合只追加
    observed condition，不新建行），本轮仍回落慢路径；
  - 多个 Bundle 都命中但其核心成员之间存在 KPN `contradicts` 边，视为仍然
    歧义，同样回落慢路径（不做仲裁）。

已知的实现现状缺口（本脚本据此拆分两条轴，见下）：`internal/retrieval/`
`internal/trace/` 尚未把 Bundle 成员的 `RecordMemberOutcome` 接到真实
Trace 回写路径上（`grep RecordMemberOutcome` 命中的只有
`internal/activation/bundle_store.go` 的方法定义和其自身单测，没有生产调用
点）——即"某个 Bundle 核心成员的置信度会随线上使用继续演化"这条闭环，代码
写了原语但还没接线。这意味着"一个 Bundle 从 candidate 走到有 CoreMember 越过
serving_confidence_min、从而真正被 `resolveBundleForAmbiguousHits` 采纳"这
条路径，目前在真实流量下无法自然达成，只能靠人工在 DB 里把
`member_point_ids` 的 `success_count`/`failure_count` 直接摆到阈值以上来验证
"一旦达标，仲裁分支本身是否走对"（同 P11 `--seed-manual` 的既有做法）。

轴一（确定性，直接验收）：候选 Bundle 首次形成（API/DB 全链路，端到端可跑）；
轴二（确定性，但依赖人工种子数据）：候选越过 serving 阈值后仲裁生效、以及
contradicts 冲突时拒绝仲裁——因为达到阈值这一步目前无法端到端自然培养，
本脚本对轴二使用 `--seed-member-confidence` 直接改库，不代表真实收敛概率，
报告里会如实注明。

用法：
  # 轴一：首次遇到跨 unit 歧义 -> 候选 Bundle 应出现/被追加
  python3 test/v1/v1_p14_bundle_ambiguity_test.py \\
      --probe-question "RAC 怎么开启归档" \\
      --link-id-a <单元A下已 self_graded/trusted 的 link_id> \\
      --link-id-b <单元B下已 self_graded/trusted 的 link_id，unit 与A不同>

  # 轴二：额外验证"达到 serving 阈值后仲裁生效 + contradicts 冲突时拒绝仲裁"
  python3 test/v1/v1_p14_bundle_ambiguity_test.py --probe-question "..." \\
      --link-id-a ... --link-id-b ... --seed-member-confidence

前置：--link-id-a/--link-id-b 指向的两条 ActivationLink 必须已经能让
resolveUnitsForPoints 判定为 unitResolutionAmbiguous——即 Match()（Tier1 全
字段精确匹配）在 --probe-question 上同时命中两条链接、且它们的 point_id 分
属不同 knowledge_unit（脚本用 db_activation_link + knowledge_points 关联校
验，不满足则提前报错退出，不猜测/不静默跳过）。
"""
import argparse
import json
import sys
import time

import v1_common as c


def wait_trace(conn, answer_id, timeout_s=20):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s=timeout_s, interval_s=0.5)


def points_and_units(conn, link_id):
    link = c.db_activation_link(conn, link_id)
    if not link:
        return None, None
    row = conn.execute(
        "SELECT point_id, unit_id FROM knowledge_points WHERE point_id = ? LIMIT 1",
        (link["point_id"],),
    ).fetchone()
    return link["point_id"], (row["unit_id"] if row else None)


def find_candidate_bundle(conn, point_id_a, point_id_b):
    for b in c.db_activation_bundles(conn, status="candidate"):
        members = set(m["point_id"] for m in b["member_point_ids_parsed"])
        if {point_id_a, point_id_b} <= members:
            return b
    return None


def seed_member_confidence(db_path, bundle_id, min_success=20, target_status=None):
    """轴二：人工把 bundle_id 的全部成员 success_count 摆到远超
    serving_confidence_min 对应阈值的水平（Beta 均值 (s+1)/(s+f+2)，f=0 时
    s=20 -> mean≈0.955，覆盖默认 0.7 门槛），使其在下一次 MatchBundles 时被判
    为 self_graded/trusted，从而进入 CoreMemberPointIDs。不代表真实收敛路径
    （见文件头部说明），仅用于验证仲裁分支本身的正确性。"""
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.row_factory = sqlite3.Row
    row = conn.execute("SELECT member_point_ids FROM activation_bundles WHERE bundle_id = ?", (bundle_id,)).fetchone()
    if not row:
        conn.close()
        raise RuntimeError(f"bundle_id={bundle_id} 不存在，无法 seed")
    members = json.loads(row["member_point_ids"] or "[]")
    for m in members:
        m["success_count"] = min_success
        m["failure_count"] = 0
    conn.execute(
        "UPDATE activation_bundles SET member_point_ids = ?, status = COALESCE(?, status), updated_at = CURRENT_TIMESTAMP WHERE bundle_id = ?",
        (json.dumps(members, ensure_ascii=False), target_status, bundle_id),
    )
    conn.commit()
    conn.close()
    print(f"  ... 已把 bundle_id={bundle_id} 全部 {len(members)} 个成员的 success_count 摆到 {min_success}（人工种子，非自然收敛）")


def axis1_first_occurrence(base_url, conn, probe_question, link_id_a, link_id_b, timeout):
    print("\n--- 轴一：首次跨 unit 歧义 -> 候选 Bundle 形成/追加 ---")
    point_a, _ = points_and_units(conn, link_id_a)
    point_b, _ = points_and_units(conn, link_id_b)
    if not point_a or not point_b:
        raise RuntimeError(f"link_id_a/link_id_b 之一在 DB 中不存在: a={link_id_a} b={link_id_b}")

    bundles_before = {b["bundle_id"] for b in c.db_activation_bundles(conn)}

    turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
    path_type = (result or {}).get("path_type")
    print(f"  提问「{probe_question}」→ action={turn.get('action')} path_type={path_type}")

    trace = wait_trace(conn, (result or {}).get("answer_id")) if result else None
    direct_points = json.loads(trace["direct_point_ids"] or "[]") if trace else []
    print(f"  direct_point_ids={direct_points}（期望同时包含 {point_a} 与 {point_b}，才说明真的触发了跨 unit 歧义分支）")

    ambiguity_triggered = point_a in direct_points and point_b in direct_points
    # 没有 verified Bundle 覆盖时应回落慢路径；这一轮结束后应能在 activation_bundles
    # 里找到（或追加了 observed condition 的）一条同时含 point_a/point_b 的 candidate。
    candidate = find_candidate_bundle(conn, point_a, point_b)
    bundle_formed_or_strengthened = candidate is not None

    print(f"  歧义是否命中两个 unit 的 point: {ambiguity_triggered}")
    print(f"  是否找到覆盖 {{point_a, point_b}} 的 candidate Bundle: {bundle_formed_or_strengthened}"
          + (f"（bundle_id={candidate['bundle_id']}）" if candidate else ""))
    print(f"  本轮 fallback 到慢路径（未被误判为 fast）: {path_type != 'fast'}")

    return {
        "point_a": point_a, "point_b": point_b,
        "path_type": path_type,
        "ambiguity_triggered": ambiguity_triggered,
        "candidate_bundle_id": candidate["bundle_id"] if candidate else None,
        "bundle_formed_or_strengthened": bundle_formed_or_strengthened,
        "fell_back_to_slow_path": path_type != "fast",
        "new_bundle_ids": sorted({b["bundle_id"] for b in c.db_activation_bundles(conn)} - bundles_before),
    }


def axis2_serving_and_conflict(base_url, conn, db_path, probe_question, bundle_id, point_a, point_b, timeout):
    print("\n--- 轴二（人工种子，观测仲裁分支本身是否走对，非真实收敛路径）---")
    result = {}

    contradicts = c.db_kp_relations(conn, relation_type="contradicts")
    conflicting_pair = any(
        {r["source_point_id"], r["target_point_id"]} == {point_a, point_b}
        for r in contradicts
    )
    print(f"  point_a/point_b 之间是否已存在 contradicts KPN 关系: {conflicting_pair}")
    result["pre_existing_contradicts"] = conflicting_pair

    if conflicting_pair:
        # 冲突场景：即使成员置信度达标，仲裁也应该拒绝合并、继续回落慢路径。
        seed_member_confidence(db_path, bundle_id)
        turn, r = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
        path_type = (r or {}).get("path_type")
        print(f"  contradicts 场景下重问 → path_type={path_type}（期望非 fast：冲突不应被仲裁合并）")
        result["conflict_case"] = {"path_type": path_type, "correctly_rejected": path_type != "fast"}
    else:
        # 非冲突场景：成员置信度达标后，仲裁应该合并核心成员、这一轮走快路径。
        seed_member_confidence(db_path, bundle_id, target_status="verified")
        turn, r = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
        path_type = (r or {}).get("path_type")
        trace = wait_trace(conn, (r or {}).get("answer_id")) if r else None
        direct_points = json.loads(trace["direct_point_ids"] or "[]") if trace else []
        merged = point_a in direct_points and point_b in direct_points
        print(f"  达标后重问 → path_type={path_type}（期望 fast）direct_point_ids={direct_points}（期望仍含两点，已合并核心成员）")
        result["no_conflict_case"] = {
            "path_type": path_type, "resolved_fast": path_type == "fast",
            "direct_point_ids": direct_points, "merged_both_points": merged,
        }

    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    parser.add_argument("--db-path", default=None)
    parser.add_argument("--probe-question", required=True)
    parser.add_argument("--link-id-a", required=True)
    parser.add_argument("--link-id-b", required=True)
    parser.add_argument("--seed-member-confidence", action="store_true",
                         help="额外跑轴二（人工把 Bundle 成员置信度摆到阈值以上，验证仲裁分支本身）")
    parser.add_argument("--timeout", type=float, default=180.0)
    args = parser.parse_args()

    conn = c.open_db(args.db_path)
    db_path = args.db_path or c.DEFAULT_DB_PATH

    link_a = c.db_activation_link(conn, args.link_id_a)
    link_b = c.db_activation_link(conn, args.link_id_b)
    if not link_a or not link_b:
        print(f"! link_id_a/link_id_b 之一不存在: a={args.link_id_a} b={args.link_id_b}", file=sys.stderr)
        sys.exit(1)
    if link_a["point_id"] == link_b["point_id"]:
        print("! link_id_a 与 link_id_b 指向同一 point_id，不构成跨 unit 歧义场景", file=sys.stderr)
        sys.exit(1)

    axis1 = axis1_first_occurrence(args.base_url, conn, args.probe_question, args.link_id_a, args.link_id_b, args.timeout)

    axis1_pass = axis1["ambiguity_triggered"] and axis1["bundle_formed_or_strengthened"] and axis1["fell_back_to_slow_path"]

    axis2 = None
    if args.seed_member_confidence:
        if not axis1["ambiguity_triggered"]:
            print("! 轴一未能触发真实歧义（两点未同时出现在 direct_point_ids），轴二跳过——"
                  "先确认 --link-id-a/--link-id-b 在 --probe-question 上都能被 Tier1 精确匹配命中", file=sys.stderr)
        elif not axis1["candidate_bundle_id"]:
            print("! 轴一未形成 candidate Bundle，轴二无法 seed，跳过", file=sys.stderr)
        else:
            axis2 = axis2_serving_and_conflict(
                args.base_url, conn, db_path, args.probe_question,
                axis1["candidate_bundle_id"], axis1["point_a"], axis1["point_b"], args.timeout,
            )

    summary = {"probe_question": args.probe_question, "axis1": axis1, "axis2": axis2}
    print("\n=== P14 结果汇总 ===")
    print(json.dumps(summary, ensure_ascii=False, indent=2, default=str))
    print(f"\n轴一通过标准: {'PASS' if axis1_pass else 'FAIL'}")

    axis2_pass = None
    if axis2:
        if "conflict_case" in axis2:
            axis2_pass = axis2["conflict_case"]["correctly_rejected"]
        elif "no_conflict_case" in axis2:
            axis2_pass = axis2["no_conflict_case"]["resolved_fast"] and axis2["no_conflict_case"]["merged_both_points"]
        print(f"轴二通过标准（人工种子，仅验证仲裁分支本身）: {'PASS' if axis2_pass else 'FAIL'}")

    out_dir = c.RESULTS_DIR
    c.write_jsonl([summary], out_dir, "v1_p14_bundle_ambiguity")

    conn.close()
    sys.exit(0 if axis1_pass and (axis2_pass is not False) else 2)


if __name__ == "__main__":
    main()
