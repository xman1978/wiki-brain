#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P2：学习转化 candidate 形成与人工晋升
（标准 1 前半 + 标准 3）。

培养清单（制度域 6 题 + 技术域 5 题）：A1、A2、A4、A9、A11、A12 + T8、T12、T15、T13，
以及 F1 前置问题「达梦怎么查询会话执行情况」（这题不在任何表格里，是 F 组 P3 对象守门
测试需要预先培养的一条独立链接）。

2026-08-07 口径（与方案 P2 修订同步）：
  - Matcher 四元组精确匹配：变体不保证命中同一 link；pending_confirm 只要求至少 1 条
    （默认盯 A1），其余 adopt 覆盖记观测。
  - 链接按题号独占归属（topic hint + 证据 point），禁止用 point_id 并集做多题共享判定。
  - reject 终态是 deprecated（不是字面 rejected）。
  - A11 入选培养但不 confirm，对照组只看 A11 独占归属链接。

重要缺口（脚本不代为决定，需要人工介入）：
  A11、A12、T12、T13、T15 在方案题库表里只有 1 种问法（无变体），而
  promote_distinct_min=2 / 共现侧仍要求 ≥2 个不同 question_hash——同一字面问多少遍
  distinct_n 都不会增长。请用 --extra-phrasing-file 补充，例如：
    {"A11": ["新问法1", "新问法2"], "T12": [...]}
  F1_PRE 默认内置问法见 F1_PRE_DEFAULT_VARIANTS，可用同一 JSON 覆盖 "F1_PRE" 键。

  注意（2026-07-24）：distinct_n 统计的是字面 question_hash，不受 subject_synonyms
  归一化影响。subject_synonyms 验收见 test/v1/v1_p11_synonym_test.py（依赖本脚本
  培养出的 F1_PRE 链接，不要在 P11 之前手工改动其四元组字段）。

流程（对应方案 P2 步骤 1-6）：
  1. 对培养清单每题问一轮全部已知变体问法；
  2. POST /study/run，按独占归属核对 candidate + create_candidate 可回溯；
  3. 再问一轮变体；对 PROMOTE_DEMO_IDS（默认 A1）额外用主问法+一变体复现；
  4. POST /study/run：硬门槛 auto_promote=false + ≥1 条 pending_confirm；
  5. 按独占归属 confirm 确认集 / reject 驳回集；跳过已 verified；不碰 A11；
  6. 核对最终状态。

用法：
  python3 test/v1/v1_p2_learning_test.py
  python3 test/v1/v1_p2_learning_test.py --extra-phrasing-file test/v1/v1_p2_extra_phrasings.json
  python3 test/v1/v1_p2_learning_test.py --skip-cultivate   # 假设已经问过，只跑 study/run 与后续核对
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

CULTIVATION_TABLE_IDS = ["A1", "A2", "A4", "A9", "A11", "A12", "T8", "T12", "T13", "T15"]
F1_PRE_ID = "F1_PRE"
F1_PRE_QUESTION = "达梦怎么查询会话执行情况"
F1_PRE_DEFAULT_VARIANTS = [
    F1_PRE_QUESTION,
    "达梦数据库怎么查看当前的会话执行情况？",
    "怎么在达梦里查询会话的执行状态？",
]

CONFIRM_IDS = ["A1", "A4", "A9", "A12", "T8", "T12", "T15", F1_PRE_ID]
REJECT_IDS = ["A2", "T13"]
CONTROL_IDS = ["A11"]  # 培养但不 confirm
PROMOTE_DEMO_IDS = ["A1"]  # 第 2 轮额外复现，争取跑通 pending_confirm

# 题号独占归属：用 KP 正文 / subject / question_terms 关键词消歧邻近簇
TOPIC_HINTS = {
    "A1": ["招待", "45天", "45 天"],
    "A2": ["差旅", "出差"],
    "A4": ["培训", "旷课", "缺席", "考勤"],
    "A9": ["催收", "催款", "90", "三个月", "三个 月"],
    "A11": ["九个月", "九个 月", "9个月", "25%", "75%", "提成比例"],
    "A12": ["优秀", "绩效系数", "考核"],
    "T8": ["归档", "archive", "oracle rac", "rac"],
    "T12": ["句柄", "max_session_statement", "20000", "系统内存不足"],
    "T13": ["buffer", "缓冲"],
    "T15": ["max_connections", "连接数", "神通"],
    F1_PRE_ID: ["v$session", "session_event", "会话执行", "等待事件", "sql文本", "客户端ip"],
}

# 共享 point 时的优先归属（确认集 > 对照组 > 驳回集），同级再比 topic score
OWNERSHIP_PRIORITY = {rid: 0 for rid in CONFIRM_IDS}
OWNERSHIP_PRIORITY.update({rid: 1 for rid in CONTROL_IDS})
OWNERSHIP_PRIORITY.update({rid: 2 for rid in REJECT_IDS})
for rid in CULTIVATION_TABLE_IDS:
    OWNERSHIP_PRIORITY.setdefault(rid, 3)
OWNERSHIP_PRIORITY.setdefault(F1_PRE_ID, 0)


def load_extra_phrasings(path):
    if not path:
        return {}
    return json.loads(Path(path).read_text(encoding="utf-8"))


def load_cultivation_bank(full_text, extra_phrasings):
    bank = {}
    for g in ("A", "T"):
        for row in c.load_group(g, full_text):
            rid = c.row_id(row)
            if rid not in CULTIVATION_TABLE_IDS:
                continue
            variants = list(c.question_variants(row))
            for extra in extra_phrasings.get(rid, []):
                if extra not in variants:
                    variants.append(extra)
            bank[rid] = {"id": rid, "question_variants": variants, "domain": c.domain_of(rid)}

    f1_variants = extra_phrasings.get(F1_PRE_ID, F1_PRE_DEFAULT_VARIANTS)
    bank[F1_PRE_ID] = {"id": F1_PRE_ID, "question_variants": f1_variants, "domain": "技术域"}

    for rid, item in bank.items():
        if len(item["question_variants"]) < 2:
            print(
                f"! 警告: {rid} 只有 {len(item['question_variants'])} 种问法，"
                f"共现/distinct 侧可能不达标，见脚本头部说明",
                file=sys.stderr,
            )
    return bank


def ask(base_url, question, timeout):
    """走真实客户端路径（/sessions -> /session/turn -> /answer/stream），每次新建
    独立 session——共现分组按 subject 归并，subject 只有走这条路径才会被解析。"""
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    if result is None:
        print(f"    ! 未走到 retrieve（action={turn.get('action')}），跳过")
    return result


def cultivate_round(base_url, bank, timeout, delay, label, only_ids=None, variants_limit=None):
    """记录每题 direct_evidence 的 point_id，供后续独占归属消歧。"""
    print(f"\n--- 培养轮次: {label} ---")
    for rid, item in bank.items():
        if only_ids is not None and rid not in only_ids:
            continue
        item.setdefault("point_ids", set())
        variants = item["question_variants"]
        if variants_limit is not None:
            variants = variants[:variants_limit]
        for q in variants:
            print(f"  {rid}: {q}")
            try:
                result = ask(base_url, q, timeout)
                if result:
                    es = result.get("evidence_snapshot") or {}
                    for ev in es.get("direct_evidence") or []:
                        if ev.get("point_id"):
                            item["point_ids"].add(ev["point_id"])
            except Exception as e:
                print(f"    ! 出错: {e}")
            time.sleep(delay)


def run_study(base_url):
    result, _ = c.http_post_json(base_url, "/study/run", {}, timeout=180)
    return result


def _norm_text(*parts):
    return " ".join(str(p or "") for p in parts).lower()


def topic_score(rid, link, kp_content=""):
    hints = TOPIC_HINTS.get(rid) or []
    if not hints:
        return 0
    blob = _norm_text(
        link.get("subject_terms"),
        link.get("intent_terms"),
        link.get("constraint_terms"),
        link.get("question_terms"),
        kp_content,
    )
    return sum(1 for h in hints if h.lower() in blob)


def kp_content_map(conn, point_ids):
    out = {}
    for pid in point_ids:
        row = conn.execute(
            "SELECT content FROM knowledge_points WHERE point_id = ?", (pid,)
        ).fetchone()
        out[pid] = (row["content"] if row else "") or ""
    return out


def assign_link_owners(bank, all_links, conn):
    """每个 link_id 至多归属一个题号。

    1) 先看哪些题的培养证据点到了该 link.point_id；
    2) 仅一题命中 → 直接归属；
    3) 多题命中 → topic_score 高者胜；同分比 OWNERSHIP_PRIORITY（确认集优先）；
    4) 无线索（如 --skip-cultivate 未采到 point）→ 对全体题号按 topic_score 软归属，
       最高分 >0 才接纳，否则不归属。
    """
    point_to_rids = {}
    for rid, item in bank.items():
        for pid in item.get("point_ids") or set():
            point_to_rids.setdefault(pid, set()).add(rid)

    needed = {link["point_id"] for link in all_links if link.get("point_id")}
    contents = kp_content_map(conn, needed)
    all_rids = list(bank.keys())

    ownership = {}  # link_id -> rid
    shared_obs = []
    for link in all_links:
        lid = link["link_id"]
        pid = link.get("point_id")
        candidates = sorted(point_to_rids.get(pid) or [])
        if not candidates:
            soft = []
            for rid in all_rids:
                sc = topic_score(rid, link, contents.get(pid, ""))
                if sc > 0:
                    soft.append((sc, -OWNERSHIP_PRIORITY.get(rid, 9), rid))
            if not soft:
                continue
            soft.sort(reverse=True)
            ownership[lid] = soft[0][2]
            continue
        if len(candidates) == 1:
            ownership[lid] = candidates[0]
            continue
        scored = []
        for rid in candidates:
            scored.append(
                (
                    topic_score(rid, link, contents.get(pid, "")),
                    -OWNERSHIP_PRIORITY.get(rid, 9),
                    rid,
                )
            )
        scored.sort(reverse=True)
        winner = scored[0][2]
        ownership[lid] = winner
        shared_obs.append(
            {
                "link_id": lid,
                "point_id": pid,
                "candidates": candidates,
                "winner": winner,
                "scores": {rid: topic_score(rid, link, contents.get(pid, "")) for rid in candidates},
            }
        )
    return ownership, shared_obs


def links_by_owner(all_links, ownership):
    by_id = {rid: [] for rid in CULTIVATION_TABLE_IDS + [F1_PRE_ID]}
    for link in all_links:
        rid = ownership.get(link["link_id"])
        if rid:
            by_id.setdefault(rid, []).append(link)
    return by_id


def learning_results_for_link(conn, link_id):
    rows = conn.execute(
        "SELECT * FROM learning_results WHERE object_id = ? ORDER BY created_at", (link_id,)
    ).fetchall()
    out = []
    for r in rows:
        event_ids = json.loads(r["event_ids"] or "[]") or []
        found, missing = 0, 0
        for eid in event_ids:
            row = conn.execute(
                "SELECT event_id FROM learning_events WHERE event_id = ?", (eid,)
            ).fetchone()
            if row:
                found += 1
            else:
                missing += 1
        out.append(
            {
                "result_id": r["result_id"],
                "action": r["action"],
                "status": r["status"],
                "reason": r["reason"],
                "event_ids": event_ids,
                "events_found": found,
                "events_missing": missing,
            }
        )
    return out


def report_links(bank, links_by_id, conn, stage_label):
    print(f"\n--- 链接状态（{stage_label}） ---")
    report = []
    for rid, item in bank.items():
        matched = links_by_id.get(rid) or []
        entry = {"id": rid, "domain": item["domain"], "links": []}
        if not matched:
            print(f"  {rid}: 未找到独占归属的 activation_link")
        for link in matched:
            results = learning_results_for_link(conn, link["link_id"])
            has_object_terms = bool(link.get("subject_terms") or link.get("constraint_terms"))
            print(
                f"  {rid}: link_id={link['link_id']} status={link['status']} "
                f"adopt={link['adopt_count']} fail={link['fail_count']} "
                f"subject_terms={link.get('subject_terms') or '(空)'} "
                f"constraint_terms={link.get('constraint_terms') or '(空)'}"
            )
            for r in results:
                bad = r["events_missing"] > 0
                print(
                    f"      learning_result: action={r['action']} status={r['status']} "
                    f"event_ids={r['event_ids']}（缺失 {r['events_missing']} 条）"
                    f"{'  ! 回溯断裂' if bad else ''}"
                )
            entry["links"].append(
                {
                    "link_id": link["link_id"],
                    "status": link["status"],
                    "adopt_count": link["adopt_count"],
                    "fail_count": link["fail_count"],
                    "subject_terms": link.get("subject_terms"),
                    "constraint_terms": link.get("constraint_terms"),
                    "has_object_terms": has_object_terms,
                    "learning_results": results,
                }
            )
        report.append(entry)
    return report


def refresh_ownership_views(base_url, bank, conn):
    all_links = c.http_get_json(base_url, "/activation-links?limit=500")
    ownership, shared_obs = assign_link_owners(bank, all_links, conn)
    by_id = links_by_owner(all_links, ownership)
    return by_id, all_links, ownership, shared_obs


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--skip-cultivate", action="store_true", help="跳过培养提问，假设已经问过")
    parser.add_argument("--skip-confirm", action="store_true", help="跳过 confirm/reject 调用")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    extra_phrasings = load_extra_phrasings(args.extra_phrasing_file)
    full_text = c.load_plan_text()
    bank = load_cultivation_bank(full_text, extra_phrasings)

    if not args.skip_cultivate:
        cultivate_round(args.base_url, bank, args.timeout, args.delay, "第 1 轮（每题问一遍全部已知问法）")
    else:
        print("--skip-cultivate：跳过第 1 轮提问。")

    print("\n>>> POST /study/run（第 1 次）")
    study_result_1 = run_study(args.base_url)
    print(json.dumps(study_result_1, ensure_ascii=False, indent=2)[:2000])

    conn = c.open_db(args.db_path)
    links_by_id, _all_links, ownership, shared_obs = refresh_ownership_views(args.base_url, bank, conn)
    if shared_obs:
        print(f"\n观测：共享 point 消歧 {len(shared_obs)} 条（详见 jsonl shared_ownership）")
        for row in shared_obs[:12]:
            print(f"  link={row['link_id'][:8]}… candidates={row['candidates']} → {row['winner']} scores={row['scores']}")
    stage1_report = report_links(bank, links_by_id, conn, "第 1 次 study/run 之后（独占归属）")

    missing_links = [e["id"] for e in stage1_report if not e["links"]]
    non_candidate = [
        e["id"]
        for e in stage1_report
        if e["links"] and not any(l["status"] == "candidate" for l in e["links"])
    ]
    print(f"\ncandidate 创建核对: 缺独占 link={missing_links} 未变成 candidate={non_candidate}")

    if not args.skip_cultivate:
        cultivate_round(args.base_url, bank, args.timeout, args.delay, "第 2 轮（全清单变体再问一遍）")
        # 精确匹配下变体常打不中同一 link；对演示题额外用主问法+第一变体复现，争取 pending_confirm
        cultivate_round(
            args.base_url,
            bank,
            args.timeout,
            args.delay,
            f"第 2b 轮（晋升演示 {PROMOTE_DEMO_IDS}：主问法+一变体复现）",
            only_ids=set(PROMOTE_DEMO_IDS),
            variants_limit=2,
        )
    else:
        print("--skip-cultivate：跳过第 2 轮提问。")

    print("\n>>> POST /study/run（第 2 次）")
    study_result_2 = run_study(args.base_url)
    print(json.dumps(study_result_2, ensure_ascii=False, indent=2)[:2000])

    links_by_id, _all_links, ownership, shared_obs = refresh_ownership_views(args.base_url, bank, conn)
    stage2_report = report_links(
        bank, links_by_id, conn, "第 2 次 study/run 之后（应至少 1 条 promote/pending_confirm）"
    )

    still_candidate_status = [
        e["id"]
        for e in stage2_report
        if e["links"] and any(l["status"] == "candidate" for l in e["links"])
    ]
    pre_verified = [
        e["id"]
        for e in stage2_report
        if any(l["status"] == "verified" for l in e["links"])
    ]
    has_promote_pending = [
        e["id"]
        for e in stage2_report
        if any(
            r["action"] == "promote" and r["status"] == "pending_confirm"
            for l in e["links"]
            for r in l["learning_results"]
        )
    ]
    # 以 Study 本轮动作计数为准（脏库里可能已有上次 P2 留下的 verified，不能据此误判）
    promoted_1 = (study_result_1.get("learning_actions") or {}).get("promoted", 0)
    promoted_2 = (study_result_2.get("learning_actions") or {}).get("promoted", 0)
    auto_promote_ok = promoted_1 == 0 and promoted_2 == 0
    print(
        f"\nauto_promote=false 核对: "
        f"{'PASS' if auto_promote_ok else 'FAIL'} "
        f"(study promoted 计数: run1={promoted_1} run2={promoted_2}; "
        f"仍为 candidate={still_candidate_status}; "
        f"库内已是 verified（可能来自既往确认）={pre_verified or '[]'})"
    )
    print(
        f"promote/pending_confirm（硬门槛 ≥1）: "
        f"{'PASS' if has_promote_pending else 'FAIL'} 出现于 {has_promote_pending or '[]'}"
    )
    adopt_obs = {
        e["id"]: [l["adopt_count"] for l in e["links"]] for e in stage2_report if e["links"]
    }
    print(f"观测 adopt_count 分布: {adopt_obs}")

    confirmed_link_ids = set()
    rejected_link_ids = set()
    if args.skip_confirm:
        print("\n--skip-confirm：跳过 confirm/reject 步骤。")
    else:
        print("\n--- 第 5 步：按独占归属 confirm / reject（同一 link 只操作一次） ---")
        for rid in CONFIRM_IDS:
            for link in links_by_id.get(rid) or []:
                lid = link["link_id"]
                if lid in confirmed_link_ids:
                    continue
                if link.get("status") == "verified":
                    print(f"  skip confirm {rid} ({lid}): 已是 verified")
                    confirmed_link_ids.add(lid)
                    continue
                try:
                    resp, _ = c.http_post_json(args.base_url, f"/activation-links/{lid}/confirm", {})
                    print(f"  confirm {rid} ({lid}): {resp}")
                    confirmed_link_ids.add(lid)
                except Exception as e:
                    print(f"  ! confirm {rid} ({lid}) 失败: {e}")
        for rid in REJECT_IDS:
            for link in links_by_id.get(rid) or []:
                lid = link["link_id"]
                if lid in rejected_link_ids or lid in confirmed_link_ids:
                    print(f"  skip reject {rid} ({lid}): 已处理或不该驳回确认集链接")
                    continue
                if link.get("status") == "deprecated":
                    print(f"  skip reject {rid} ({lid}): 已是 deprecated")
                    rejected_link_ids.add(lid)
                    continue
                try:
                    resp, _ = c.http_post_json(args.base_url, f"/activation-links/{lid}/reject", {})
                    print(f"  reject {rid} ({lid}): {resp}")
                    rejected_link_ids.add(lid)
                except Exception as e:
                    print(f"  ! reject {rid} ({lid}) 失败: {e}")
        print("  A11：有意不 confirm（对照组）")

    links_by_id, _all_links, ownership, shared_obs = refresh_ownership_views(args.base_url, bank, conn)
    final_report = report_links(bank, links_by_id, conn, "confirm/reject 之后（最终状态，独占归属）")

    print("\n========== P2 通过标准核对 ==========")
    gate = {}
    for rid in CONFIRM_IDS:
        statuses = [l["status"] for l in (links_by_id.get(rid) or [])]
        ok = "verified" in statuses
        gate[f"confirm_{rid}"] = ok
        print(f"  {rid} 独占归属应为 verified: {'PASS' if ok else 'FAIL'} (实际: {statuses or '无链接'})")
    for rid in REJECT_IDS:
        statuses = [l["status"] for l in (links_by_id.get(rid) or [])]
        # 设计终态：reject → deprecated（activation.md）；不得残留 verified
        ok = bool(statuses) and all(s == "deprecated" for s in statuses) and "verified" not in statuses
        gate[f"reject_{rid}"] = ok
        print(
            f"  {rid} 独占归属应为 deprecated（reject 终态，不参与召回）: "
            f"{'PASS' if ok else 'FAIL'} (实际: {statuses or '无链接'})"
        )
    for rid in CONTROL_IDS:
        statuses = [l["status"] for l in (links_by_id.get(rid) or [])]
        ok = "verified" not in statuses
        gate[f"control_{rid}"] = ok
        print(
            f"  {rid}（有意不确认，对照组）独占归属不应为 verified: "
            f"{'PASS' if ok else 'FAIL'} (实际: {statuses or '无链接'})"
        )
    gate["auto_promote_false"] = auto_promote_ok
    print(f"  auto_promote=false（未自动 verified）: {'PASS' if auto_promote_ok else 'FAIL'}")
    gate["pending_confirm_ge1"] = bool(has_promote_pending)
    print(
        f"  至少 1 条 promote/pending_confirm: "
        f"{'PASS' if has_promote_pending else 'FAIL'} ({has_promote_pending or '无'})"
    )
    all_traceback_ok = all(
        r["events_missing"] == 0
        for e in final_report
        for l in e["links"]
        for r in l["learning_results"]
    )
    gate["traceback"] = all_traceback_ok
    print(f"  每次迁移 learning_result -> reason -> event_ids 完整回溯: {'PASS' if all_traceback_ok else 'FAIL'}")

    failed = [k for k, v in gate.items() if not v]
    print(f"\nP2 总评: {'PASS' if not failed else 'FAIL'}  (失败项: {failed or '无'})")

    record = {
        "bank": {k: v["question_variants"] for k, v in bank.items()},
        "ownership": ownership,
        "shared_ownership": shared_obs,
        "study_result_1": study_result_1,
        "study_result_2": study_result_2,
        "stage1_report": stage1_report,
        "stage2_report": stage2_report,
        "final_report": final_report,
        "promote_pending_ids": has_promote_pending,
        "adopt_count_obs": adopt_obs,
        "gate": gate,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p2_learning")
    print(f"\n详细结果: {jsonl_path}")
    conn.close()
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
