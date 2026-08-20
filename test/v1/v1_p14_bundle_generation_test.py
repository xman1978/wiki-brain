#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P14：ActivationBundle
生成与匹配（2026-08-20 重设计，取代此前"跨 unit 歧义仲裁"口径，见
`docs/design/activation-bundle.md`「10. 改判」、`docs/impl/v1/
activation-bundle.md` 步骤 2/4、`internal/study/bundle_scan.go`、
`internal/retrieval/fastpath_helpers.go`）。

背景（务必先读，与旧版 `v1_p14_bundle_ambiguity_test.py` 的核心区别）：

- **生成不再依赖查询时的跨 unit 歧义**。此前 `formCandidateBundle` 会在
  ActivationLink 匹配出现跨 unit 歧义、且没有已有 Bundle 覆盖时，直接把
  "两条 Link 各自独立命中同一问题（未必真的一起被引用过）"当成合并证据
  实时新建候选 Bundle——这条路径已删除。现在唯一可信的生成证据是**直接
  联合引用**：同一条 `retrieval_quality='confident'` 的 trace，其
  `direct_point_ids` 同时包含多个知识点。Study 周期扫描
  （`scanActivationBundles`）把这类 trace 的四元组归一化后累加进新表
  `bundle_trigger_cooccurrence`（跟 `question_kp_cooccurrence` 同构，键
  从 point_id 换成归一化四元组），越过 `create_confidence_min`/
  `create_width_max`（复用 ActivationLink 同一套创建门槛，不新增配置）
  才会创建/刷新 Bundle。
- **Bundle 身份是归一化四元组，不是 point 集合**：成员名单是"历史上匹配到
  这个四元组的全部 confident trace 的 `direct_point_ids` 并集"，每次
  `scanActivationBundles` 全量重算（同 ActivationLink 的
  `buildObservedConditions` 风格），不是创建时写死的固定集合。
- **匹配从"Link 歧义时被动 consult"改为"跟 Link 并行主动匹配"**：查询时
  `activation.Match`（Link）与 `resolveBundleCandidate`（Bundle）并行发起，
  命中优先级按连续置信度（tier/mean）比较，不是写死的层级顺序；Bundle 不
  再需要等 Link 先跑出歧义才有机会登场。

**2026-08-20 当天晚些时候补记，取代上面「已知实现现状」段落**：验证环节
（触发轴/成员轴的实时写回）已实现，不再是缺口。`internal/activation/
bundle_store.go` 新增 `RecordBundleOutcome`（触发轴，镜像 ActivationLink
的 `RecordOutcome`），`RecordMemberOutcome`（成员轴，此前已存在但零调用）
补上了生产路径调用点——`retrieval.EvidenceSet` 新增 `BundleHits` 字段把
这轮实际命中的 Bundle 及匹配到的触发条件、实际用到的成员点位带给
Trace，`trace.generateBundleActivationEvents` 对每次真实命中判定
成功/失败并调用上述两个方法，复用已有的 `activation_success`/
`activation_failure` 事件类型（不新增事件类型，payload 换成
`bundle_id`/`member_point_ids`）。同时修复了一个连带问题：Study 每轮
`scanActivationBundles` 重算此前是整体覆盖 `member_point_ids`/
`observed_conditions`，会把刚写入的实时计数冲掉——`RefreshBundleMembers`
改为合并写，已有条目保留累积计数，只有新发现的候选才作为新条目插入。
详见 `docs/design/activation-bundle.md`「11. 验证环节的实际接线」、
`docs/impl/v1/activation-bundle.md` 步骤 5 编注。

轴一（确定性，直接验收，端到端可跑）：反复问一个真实需要联合引用两个不同
knowledge_unit 下知识点的问题，确认 `bundle_trigger_cooccurrence` 与
`activation_bundles` 按预期生成/刷新。

轴二（确定性，但依赖人工种子加速置信度）：把 Bundle 触发轴与成员轴的
`success_count` 摆到远超阈值的水平，验证查询时 Bundle 候选确实会被
`resolveBundleCandidate` 采纳、并在优先级比较中生效（`path_type=fast`
且 `direct_point_ids` 同时包含两点）。跨 Bundle 的 `contradicts` 冲突拒绝
分支（`bundlesConflict`，仅在同一次查询命中≥2个不同 Bundle 时触发）不在
本脚本覆盖范围——端到端构造两条独立生成、核心成员互相冲突、且命中同一个
归一化四元组的 Bundle 过于刻意，该分支已有 Go 单测覆盖
（`internal/retrieval/fastpath_test.go`
`TestRetrieve_FastPath_ConflictingVerifiedBundles_FallsBackToSlowPath`），
本脚本不重复验证。

轴三（2026-08-20 新增，需 `--seed-confidence`，衔接轴二）：验证"生成-验证
-进化"闭环里此前缺失、这次补上的那一环——轴二种子后的 Bundle 已经能走
`path_type=fast`，轴三在此基础上再真实问一次，核对这次命中是否真的把
触发轴/成员轴的 `success_count` 各 +1（`RecordBundleOutcome`/
`RecordMemberOutcome` 是否被正确调用），核对该 trace 下是否恰好写入一条
`activation_success` 事件（payload 含 `bundle_id`/`member_point_ids`，
不是新的事件类型）；再跑一次 `POST /study/run`，核对刚才 +1 之后的计数
**没有**被这次重算冲回去（`RefreshBundleMembers` 合并写是否生效——这是
本次改动修的核心 bug，旧版 `UpdateBundleMembers` 会整体覆盖）。

用法：
  # 轴一：反复联合引用 -> Study 扫描 -> Bundle 生成/刷新
  python3 test/v1/v1_p14_bundle_generation_test.py \\
      --probe-question "两篇 RAC 文档在归档配置上有什么区别" \\
      --point-a <point_id，属于某个 knowledge_unit> \\
      --point-b <point_id，属于另一个不同的 knowledge_unit> \\
      --repeat 3

  # 轴二+轴三：人工种子把触发轴+成员轴置信度摆到阈值以上，验证命中生效
  # （轴二）与真实命中后的验证写回 + 抗重算覆盖（轴三）
  python3 test/v1/v1_p14_bundle_generation_test.py --probe-question "..." \\
      --point-a ... --point-b ... --repeat 3 --seed-confidence

前置：--point-a/--point-b 必须真实分属不同 knowledge_unit（脚本用
knowledge_points 关联校验，不满足则提前报错退出，不猜测/不静默跳过）；
--probe-question 必须是一个真实需要综合这两个知识点才能完整回答的问题
——脚本不会替你判断这一点，只会在每次提问后核对 trace 的
`direct_point_ids` 是否确实同时包含两点，不满足会在汇总里如实标注失败，
不会伪造通过。
"""
import argparse
import json
import sys

import v1_common as c


def wait_trace(conn, answer_id, timeout_s=20):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s=timeout_s, interval_s=0.5)


def point_unit(conn, point_id):
    row = conn.execute(
        "SELECT point_id, unit_id FROM knowledge_points WHERE point_id = ? LIMIT 1",
        (point_id,),
    ).fetchone()
    return row["unit_id"] if row else None


def find_bundle_covering(conn, point_a, point_b):
    for b in c.db_activation_bundles(conn):
        members = set(m["point_id"] for m in b["member_point_ids_parsed"])
        if {point_a, point_b} <= members:
            return b
    return None


def run_study(base_url):
    result, _ = c.http_post_json(base_url, "/study/run", {}, timeout=180)
    return result


def axis1_generation(base_url, conn, probe_question, point_a, point_b, repeat, timeout):
    print("\n--- 轴一：反复直接联合引用 -> Study 扫描 -> Bundle 生成/刷新 ---")

    trigger_before = conn.execute("SELECT COUNT(*) AS n FROM bundle_trigger_cooccurrence").fetchone()["n"]
    bundle_before = find_bundle_covering(conn, point_a, point_b)

    joint_hits = 0
    for i in range(repeat):
        turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
        path_type = (result or {}).get("path_type")
        trace = wait_trace(conn, (result or {}).get("answer_id")) if result else None
        direct_points = json.loads(trace["direct_point_ids"] or "[]") if trace else []
        hit = point_a in direct_points and point_b in direct_points
        joint_hits += 1 if hit else 0
        print(f"  第 {i + 1}/{repeat} 次提问 → action={turn.get('action')} path_type={path_type} "
              f"direct_point_ids={direct_points} 同时含两点: {hit}")

    study_result = run_study(base_url)
    print(f"  study/run 结果: {study_result}")

    trigger_after = conn.execute("SELECT COUNT(*) AS n FROM bundle_trigger_cooccurrence").fetchone()["n"]
    bundle_after = find_bundle_covering(conn, point_a, point_b)

    trigger_accumulated = trigger_after >= trigger_before  # 至少没有减少；新增行数取决于四元组是否已归一化到既有行
    bundle_created_or_refreshed = bundle_after is not None

    print(f"  bundle_trigger_cooccurrence 行数: {trigger_before} -> {trigger_after}")
    print(f"  覆盖 {{point_a, point_b}} 的 Bundle: "
          + (f"bundle_id={bundle_after['bundle_id']} status={bundle_after['status']} "
             f"members={sorted(m['point_id'] for m in bundle_after['member_point_ids_parsed'])}"
             if bundle_after else "未找到"))
    if bundle_after:
        conds = bundle_after["observed_conditions_parsed"]
        print(f"  触发轴 observed_conditions: {[{'success_count': cnd.get('success_count'), 'failure_count': cnd.get('failure_count')} for cnd in conds]}")

    return {
        "joint_hits": joint_hits,
        "repeat": repeat,
        "trigger_rows_before": trigger_before,
        "trigger_rows_after": trigger_after,
        "trigger_accumulated": trigger_accumulated,
        "bundle_id": bundle_after["bundle_id"] if bundle_after else None,
        "bundle_status": bundle_after["status"] if bundle_after else None,
        "bundle_members": sorted(m["point_id"] for m in bundle_after["member_point_ids_parsed"]) if bundle_after else [],
        "bundle_created_or_refreshed": bundle_created_or_refreshed,
    }


def seed_confidence(db_path, bundle_id, min_success=20):
    """轴二：人工把 Bundle 的触发轴 observed_conditions 与成员轴
    member_point_ids 的 success_count 全部摆到远超 serving_confidence_min
    对应阈值的水平（Beta 均值 (s+1)/(s+f+2)，f=0 时 s=20 -> mean≈0.955，
    覆盖默认 0.7 门槛），使 Bundle 在下一次 MatchBundles 时被判为
    self_graded/trusted、核心成员覆盖两点。不代表真实收敛路径（见文件头部
    说明），仅用于验证"命中后是否正确生效"这条代码路径本身。"""
    import sqlite3
    conn = sqlite3.connect(str(db_path))
    conn.row_factory = sqlite3.Row
    row = conn.execute(
        "SELECT observed_conditions, member_point_ids FROM activation_bundles WHERE bundle_id = ?",
        (bundle_id,),
    ).fetchone()
    if not row:
        conn.close()
        raise RuntimeError(f"bundle_id={bundle_id} 不存在，无法 seed")

    conds = json.loads(row["observed_conditions"] or "[]")
    for cnd in conds:
        cnd["success_count"] = min_success
        cnd["failure_count"] = 0
    members = json.loads(row["member_point_ids"] or "[]")
    for m in members:
        m["success_count"] = min_success
        m["failure_count"] = 0

    conn.execute(
        "UPDATE activation_bundles SET observed_conditions = ?, member_point_ids = ?, "
        "status = 'verified', updated_at = CURRENT_TIMESTAMP WHERE bundle_id = ?",
        (json.dumps(conds, ensure_ascii=False), json.dumps(members, ensure_ascii=False), bundle_id),
    )
    conn.commit()
    conn.close()
    print(f"  ... 已把 bundle_id={bundle_id} 的触发轴 {len(conds)} 条条件与成员轴 {len(members)} 个成员的 "
          f"success_count 摆到 {min_success}（人工种子，非自然收敛）")


def axis2_serving(base_url, conn, db_path, probe_question, bundle_id, point_a, point_b, timeout):
    print("\n--- 轴二（人工种子，观测命中生效分支本身是否走对，非真实收敛路径）---")
    seed_confidence(db_path, bundle_id)

    turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
    path_type = (result or {}).get("path_type")
    trace = wait_trace(conn, (result or {}).get("answer_id")) if result else None
    direct_points = json.loads(trace["direct_point_ids"] or "[]") if trace else []
    merged = point_a in direct_points and point_b in direct_points
    print(f"  种子后重问 → path_type={path_type}（期望 fast）direct_point_ids={direct_points}"
          f"（期望仍含两点，Bundle 候选在优先级比较中胜出）")

    return {
        "path_type": path_type,
        "resolved_fast": path_type == "fast",
        "direct_point_ids": direct_points,
        "merged_both_points": merged,
    }


def axis3_verification_loop(base_url, conn, probe_question, bundle_id, point_a, point_b, timeout):
    """轴三（2026-08-20 新增）：验证"生成-验证-进化"闭环里这次补上的那一
    环——真实命中 Bundle 之后，RecordBundleOutcome/RecordMemberOutcome 是否
    正确把结果写回触发轴/成员轴，且这份实时累积能撑过下一次 Study 重算
    （RefreshBundleMembers 合并写，取代此前 UpdateBundleMembers 整体覆盖
    清零）。前提：Bundle 已经通过轴二的人工种子达到能被 resolveBundleCandidate
    采纳的置信度（否则这次查询根本不会走 Bundle 命中，无法验证写回）。"""
    print("\n--- 轴三：验证环节闭环（真实命中写回 + 抗 Study 重算覆盖）---")

    def bundle_snapshot():
        for b in c.db_activation_bundles(conn):
            if b["bundle_id"] == bundle_id:
                return b
        return None

    before = bundle_snapshot()
    if not before:
        raise RuntimeError(f"bundle_id={bundle_id} 不存在，轴三无法执行")
    conds_before = before["observed_conditions_parsed"]
    if len(conds_before) != 1:
        print(f"! 警告：触发轴条件数 = {len(conds_before)}，设计预期恒为 1（见 CLAUDE.md "
              f"ActivationBundle 补记「一个 Bundle 的 ObservedConditions 实际上永远只有一条」），"
              f"如实记录但可能是本身就异常的数据", file=sys.stderr)
    trigger_success_before = conds_before[0]["success_count"] if conds_before else None
    members_before = {m["point_id"]: m["success_count"] for m in before["member_point_ids_parsed"]}

    turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout, session_id=None)
    path_type = (result or {}).get("path_type")
    trace = wait_trace(conn, (result or {}).get("answer_id")) if result else None
    trace_id = trace["trace_id"] if trace else None

    events = c.db_learning_events_for_trace(conn, trace_id) if trace_id else []
    bundle_events = [
        e for e in events
        if e["event_type"] in ("activation_success", "activation_failure")
        and json.loads(e["payload"] or "{}").get("bundle_id") == bundle_id
    ]

    after = bundle_snapshot()
    conds_after = after["observed_conditions_parsed"]
    trigger_success_after = conds_after[0]["success_count"] if conds_after else None
    members_after = {m["point_id"]: m["success_count"] for m in after["member_point_ids_parsed"]}

    print(f"  path_type={path_type}（期望 fast，沿用轴二已种子的 Bundle 命中）")
    print(f"  该 trace 下 bundle_id={bundle_id} 相关事件: {[e['event_type'] for e in bundle_events]}"
          f"（期望恰好 1 条 activation_success）")
    print(f"  触发轴 success_count: {trigger_success_before} -> {trigger_success_after}（期望 +1）")
    print(f"  成员轴 success_count: point_a {members_before.get(point_a)} -> {members_after.get(point_a)}, "
          f"point_b {members_before.get(point_b)} -> {members_after.get(point_b)}（期望各 +1）")

    exactly_one_success_event = len(bundle_events) == 1 and bundle_events[0]["event_type"] == "activation_success"
    trigger_incremented = (
        trigger_success_before is not None and trigger_success_after == trigger_success_before + 1
    )
    members_incremented = (
        members_after.get(point_a) == members_before.get(point_a, 0) + 1
        and members_after.get(point_b) == members_before.get(point_b, 0) + 1
    )

    # 再跑一次 Study 重算，核对刚写入的实时计数没有被这次重算冲回去——
    # 这是本次改动修的核心 bug（旧版 UpdateBundleMembers 整体覆盖）。
    run_study(base_url)
    survived = bundle_snapshot()
    conds_survived = survived["observed_conditions_parsed"]
    trigger_success_survived = conds_survived[0]["success_count"] if conds_survived else None
    members_survived = {m["point_id"]: m["success_count"] for m in survived["member_point_ids_parsed"]}

    print(f"  Study 重算后触发轴 success_count: {trigger_success_after} -> {trigger_success_survived}"
          f"（期望不变，不被重算覆盖清零）")
    print(f"  Study 重算后成员轴 success_count: point_a {members_after.get(point_a)} -> "
          f"{members_survived.get(point_a)}, point_b {members_after.get(point_b)} -> "
          f"{members_survived.get(point_b)}（期望均不变）")

    trigger_survived_ok = trigger_success_survived == trigger_success_after
    members_survived_ok = (
        members_survived.get(point_a) == members_after.get(point_a)
        and members_survived.get(point_b) == members_after.get(point_b)
    )

    return {
        "path_type": path_type,
        "resolved_fast": path_type == "fast",
        "bundle_event_types": [e["event_type"] for e in bundle_events],
        "exactly_one_success_event": exactly_one_success_event,
        "trigger_success_before": trigger_success_before,
        "trigger_success_after": trigger_success_after,
        "trigger_incremented": trigger_incremented,
        "members_before": members_before,
        "members_after": members_after,
        "members_incremented": members_incremented,
        "trigger_success_survived": trigger_success_survived,
        "trigger_survived_ok": trigger_survived_ok,
        "members_survived": members_survived,
        "members_survived_ok": members_survived_ok,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    parser.add_argument("--db-path", default=None)
    parser.add_argument("--probe-question", required=True)
    parser.add_argument("--point-a", required=True)
    parser.add_argument("--point-b", required=True)
    parser.add_argument("--repeat", type=int, default=3,
                         help="反复提问次数（默认 3，越多越接近自然收敛，但不保证单独凭这个次数就跨过创建门槛）")
    parser.add_argument("--seed-confidence", action="store_true",
                         help="额外跑轴二+轴三（人工把 Bundle 触发轴+成员轴置信度摆到阈值以上验证命中生效"
                              "分支，随后再真实命中一次核对 RecordBundleOutcome/RecordMemberOutcome 写回"
                              "且不被下一次 Study 重算冲掉）")
    parser.add_argument("--timeout", type=float, default=180.0)
    args = parser.parse_args()

    conn = c.open_db(args.db_path)
    db_path = args.db_path or c.DEFAULT_DB_PATH

    unit_a = point_unit(conn, args.point_a)
    unit_b = point_unit(conn, args.point_b)
    if not unit_a or not unit_b:
        print(f"! point_a/point_b 之一在 DB 中不存在: a={args.point_a} b={args.point_b}", file=sys.stderr)
        sys.exit(1)
    if unit_a == unit_b:
        print("! point_a 与 point_b 属于同一个 knowledge_unit，不构成跨 unit 的联合引用场景", file=sys.stderr)
        sys.exit(1)

    axis1 = axis1_generation(args.base_url, conn, args.probe_question, args.point_a, args.point_b, args.repeat, args.timeout)

    axis1_pass = axis1["joint_hits"] > 0 and axis1["bundle_created_or_refreshed"]

    axis2 = None
    axis3 = None
    if args.seed_confidence:
        if not axis1["bundle_id"]:
            print("! 轴一未生成/找到覆盖 {point_a, point_b} 的 Bundle，轴二/轴三无法 seed，跳过", file=sys.stderr)
        else:
            axis2 = axis2_serving(args.base_url, conn, db_path, args.probe_question,
                                   axis1["bundle_id"], args.point_a, args.point_b, args.timeout)
            if axis2["resolved_fast"]:
                axis3 = axis3_verification_loop(args.base_url, conn, args.probe_question,
                                                  axis1["bundle_id"], args.point_a, args.point_b, args.timeout)
            else:
                print("! 轴二未能命中快路径（Bundle 候选未生效），轴三无法验证真实命中写回，跳过", file=sys.stderr)

    summary = {"probe_question": args.probe_question, "point_a": args.point_a, "point_b": args.point_b,
               "axis1": axis1, "axis2": axis2, "axis3": axis3}
    print("\n=== P14 结果汇总 ===")
    print(json.dumps(summary, ensure_ascii=False, indent=2, default=str))
    print(f"\n轴一通过标准（问题确实需要联合引用两点 + Bundle 已生成/刷新）: {'PASS' if axis1_pass else 'FAIL'}")

    axis2_pass = None
    if axis2:
        axis2_pass = axis2["resolved_fast"] and axis2["merged_both_points"]
        print(f"轴二通过标准（人工种子，仅验证命中生效分支本身）: {'PASS' if axis2_pass else 'FAIL'}")

    axis3_pass = None
    if axis3:
        axis3_pass = (
            axis3["exactly_one_success_event"]
            and axis3["trigger_incremented"]
            and axis3["members_incremented"]
            and axis3["trigger_survived_ok"]
            and axis3["members_survived_ok"]
        )
        print(f"轴三通过标准（真实命中写回触发轴/成员轴 + 撑过 Study 重算不被清零）: "
              f"{'PASS' if axis3_pass else 'FAIL'}")

    out_dir = c.RESULTS_DIR
    c.write_jsonl([summary], out_dir, "v1_p14_bundle_generation")

    conn.close()
    sys.exit(0 if axis1_pass and (axis2_pass is not False) and (axis3_pass is not False) else 2)


if __name__ == "__main__":
    main()
