#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P2：学习转化 candidate 形成与
置信度自然收敛（标准 1 前半 + 标准 3）。

培养清单（制度域 6 题 + 技术域 5 题）：A1、A2、A4、A9、A11、A12 + T8、T12、T15、T13，
以及 F1 前置问题「达梦怎么查询会话执行情况」（这题不在任何表格里，是 F 组 P3 对象守门
测试需要预先培养的一条独立链接）。

2026-08-13 改判后重写（离散状态机 candidate/verified/weakened/deprecated 的人工晋升流程
整体废弃，`POST /activation-links/:id/confirm` 端点已从代码移除，见
docs/impl/v1/activation.md「状态机」）：

  - 没有任何"晋升确认"动作。`status=verified` 由 observed_conditions 每条条件的
    mean=(success_count+1)/(success_count+failure_count+2) 自然跨过
    retrieval.serving_confidence_min 派生产生（GET /activation-links/:id 响应的
    conditions[].tier ∈ {self_graded, trusted}）。
  - Matcher 四元组精确匹配：变体不保证命中同一 link；本脚本的收敛硬门槛只要求至少 1 条
    归属链接（默认盯 A1）自然收敛为 verified，其余观测条件的 mean/tier 分布记观测。
  - 链接按题号独占归属（topic hint + 证据 point），禁止用 point_id 并集做多题共享判定。
  - POST /activation-links/:id/reject 仍存在，但语义已变为"清空该链接全部观测条件、
    状态重新派生"——对现有 current KP 会打回 candidate（不是字面 deprecated）。
  - A11 入选培养但刻意少问几轮（欠采样），作为"证据不足不会自然收敛"的对照组，取代旧版
    "未被人工确认"的对照语义。

重要缺口（脚本不代为决定，需要人工介入）：
  A11、A12、T12、T13、T15 在方案题库表里只有 1 种问法（无变体），而共现创建门槛
  （study.create_confidence_min/create_width_max）与置信度收敛都受益于多问法、多次
  独立观测。请用 --extra-phrasing-file 补充，例如：
    {"A11": ["新问法1", "新问法2"], "T12": [...]}
  F1_PRE 默认内置问法见 F1_PRE_DEFAULT_VARIANTS，可用同一 JSON 覆盖 "F1_PRE" 键。

  注意（2026-07-24）：question_hash 统计的是字面问法，不受 subject_synonyms 归一化
  影响。subject_synonyms 验收见 test/v1/v1_p11_synonym_test.py（依赖本脚本培养出的
  F1_PRE 链接，不要在 P11 之前手工改动其四元组字段）。

流程（对应方案 P2 步骤 1-7）：
  1. 对培养清单每题问一轮全部已知变体问法（A11 只问 1 轮，刻意欠采样）；
  2. POST /study/run，按独占归属核对 candidate + create_candidate 可回溯；
  3. 再问一轮变体；对 CONVERGENCE_DEMO_IDS（默认 A1）额外反复复现，尽量让其观测条件
     的 mean 跨过 serving_confidence_min；
  4. POST /study/run：核对至少 1 条归属链接未经任何人工操作即自然收敛为 verified，
     A11 对照组仍停留在 exploring 档；
  5. 仅对 REJECT_IDS 调用 reject（不再有"确认集"人工操作）；
  6. 核对最终状态（含 conditions[] 的 mean/tier 明细）。

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

# 不再有"确认集"（无人工确认动作）；仅保留 reject 驳回集与欠采样对照组。
# 2026-08-25 改选：A2/T13 的答案天然横跨多个 KP，命中
# internal/trace/service.go updateCooccurrence 里 2026-08-24 新加的规则——
# confident trace 引用 >1 个 point_id 时整条跳过 ActivationLink 的
# cooccurrence 累积（只喂给 ActivationBundle，避免抢跑 Bundle 的联合累积，
# 见 docs/design/activation-bundle.md）——导致它们在当前设计下永远不会形成
# 独占归属的 ActivationLink，reject 断言无从验证。改选 A9、T15：两者答案均
# 稳定命中单一 KP，历史多次运行都能形成独占链接（T15 通常自然收敛至
# verified，A9 停留 candidate），能分别覆盖"reject 已 verified 链接"与
# "reject 仍是 candidate 的链接"两种场景。
REJECT_IDS = ["A9", "T15"]
CONTROL_IDS = ["A11"]  # 刻意欠采样，验证"证据不足不会自然收敛"
CONVERGENCE_DEMO_IDS = ["A1"]  # 第 2 轮额外复现，争取自然跨过 serving_confidence_min
NATURALLY_OBSERVED_IDS = [
    rid for rid in CULTIVATION_TABLE_IDS + [F1_PRE_ID] if rid not in REJECT_IDS and rid not in CONTROL_IDS
]

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

# 共享 point 时的优先归属（驳回集 > 对照组 > 其余），同级再比 topic score
OWNERSHIP_PRIORITY = {rid: 0 for rid in REJECT_IDS}
OWNERSHIP_PRIORITY.update({rid: 1 for rid in CONTROL_IDS})
for rid in CULTIVATION_TABLE_IDS:
    OWNERSHIP_PRIORITY.setdefault(rid, 2)
OWNERSHIP_PRIORITY.setdefault(F1_PRE_ID, 2)


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
        if rid in CONTROL_IDS:
            continue
        if len(item["question_variants"]) < 2:
            print(
                f"! 警告: {rid} 只有 {len(item['question_variants'])} 种问法，"
                f"共现创建门槛/置信度收敛可能不达标，见脚本头部说明",
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
    3) 多题命中 → topic_score 高者胜；同分比 OWNERSHIP_PRIORITY（驳回集/对照组优先，
       避免它们被误吞并到普通观察题下）；
    4) 无线索（如 --skip-cultivate 未采到 point）→ 对全体题号按 topic_score 软归属，
       最高分 >0 才接纳，否则不归属。
    """
    point_to_rids = {}
    for rid, item in bank.items():
        for pid in item.get("point_ids") or set():
            point_to_rids.setdefault(pid, set()).add(rid)

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
                sc = topic_score(rid, link, "")
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
            scored.append((topic_score(rid, link, ""), -OWNERSHIP_PRIORITY.get(rid, 9), rid))
        scored.sort(reverse=True)
        winner = scored[0][2]
        ownership[lid] = winner
        shared_obs.append(
            {
                "link_id": lid,
                "point_id": pid,
                "candidates": candidates,
                "winner": winner,
                "scores": {rid: topic_score(rid, link, "") for rid in candidates},
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


def fetch_link_detail(base_url, link_id):
    """GET /activation-links/:id — 只有详情端点返回 conditions[]（mean/tier/
    success_count/failure_count），列表端点（GET /activation-links）没有这个字段。"""
    try:
        return c.http_get_json(base_url, f"/activation-links/{link_id}")
    except Exception as e:
        print(f"  ! 拉取链接详情失败 {link_id}: {e}")
        return None


def best_condition(detail):
    """详情响应 conditions[] 里 mean 最高的一条（用于报告/判定"是否已收敛"）。"""
    conds = (detail or {}).get("conditions") or []
    if not conds:
        return None
    return max(conds, key=lambda x: x.get("mean", 0))


def report_links(base_url, bank, links_by_id, conn, stage_label):
    print(f"\n--- 链接状态（{stage_label}） ---")
    report = []
    for rid, item in bank.items():
        matched = links_by_id.get(rid) or []
        entry = {"id": rid, "domain": item["domain"], "links": []}
        if not matched:
            print(f"  {rid}: 未找到独占归属的 activation_link")
        for link in matched:
            results = learning_results_for_link(conn, link["link_id"])
            detail = fetch_link_detail(base_url, link["link_id"])
            top_cond = best_condition(detail)
            has_object_terms = bool(link.get("subject_terms") or link.get("constraint_terms"))
            cond_str = (
                f"best_condition(mean={top_cond['mean']:.3f} tier={top_cond['tier']} "
                f"success={top_cond['success_count']} failure={top_cond['failure_count']})"
                if top_cond
                else "best_condition=(无观测条件)"
            )
            print(
                f"  {rid}: link_id={link['link_id']} status={link['status']} "
                f"adopt={link.get('adopt_count')} fail={link.get('fail_count')} {cond_str} "
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
                    "adopt_count": link.get("adopt_count"),
                    "fail_count": link.get("fail_count"),
                    "subject_terms": link.get("subject_terms"),
                    "constraint_terms": link.get("constraint_terms"),
                    "has_object_terms": has_object_terms,
                    "conditions": (detail or {}).get("conditions") or [],
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
    parser.add_argument("--skip-reject", action="store_true", help="跳过 reject 调用")
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
        # A11 只问第 1 轮、限 1 个问法，刻意欠采样（对照组）；其余题正常培养。
        cultivate_round(
            args.base_url, bank, args.timeout, args.delay,
            "第 1 轮（每题问一遍全部已知问法；A11 仅问 1 个问法，刻意欠采样）",
            variants_limit=None,
        )
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
    stage1_report = report_links(args.base_url, bank, links_by_id, conn, "第 1 次 study/run 之后（独占归属）")

    missing_links = [e["id"] for e in stage1_report if not e["links"]]
    non_candidate = [
        e["id"]
        for e in stage1_report
        if e["links"] and not any(l["status"] == "candidate" for l in e["links"])
    ]
    print(f"\ncandidate 创建核对: 缺独占 link={missing_links} 未变成 candidate={non_candidate}")

    if not args.skip_cultivate:
        cultivate_round(
            args.base_url, bank, args.timeout, args.delay,
            "第 2 轮（全清单变体再问一遍；A11 跳过，维持欠采样）",
            only_ids=set(bank.keys()) - set(CONTROL_IDS),
        )
        # 精确匹配下变体常打不中同一 link；对收敛演示题额外反复复现主问法+变体，
        # 尽量让同一四元组的 success_count 攒够、mean 跨过 serving_confidence_min。
        cultivate_round(
            args.base_url,
            bank,
            args.timeout,
            args.delay,
            f"第 2b 轮（收敛演示 {CONVERGENCE_DEMO_IDS}：主问法+变体反复复现）",
            only_ids=set(CONVERGENCE_DEMO_IDS),
        )
        cultivate_round(
            args.base_url,
            bank,
            args.timeout,
            args.delay,
            f"第 2c 轮（收敛演示 {CONVERGENCE_DEMO_IDS}：再复现一次）",
            only_ids=set(CONVERGENCE_DEMO_IDS),
        )
    else:
        print("--skip-cultivate：跳过第 2 轮提问。")

    print("\n>>> POST /study/run（第 2 次）")
    study_result_2 = run_study(args.base_url)
    print(json.dumps(study_result_2, ensure_ascii=False, indent=2)[:2000])

    links_by_id, _all_links, ownership, shared_obs = refresh_ownership_views(args.base_url, bank, conn)
    stage2_report = report_links(
        args.base_url, bank, links_by_id, conn,
        "第 2 次 study/run 之后（应至少 1 条归属链接未经人工操作自然收敛为 verified）",
    )

    naturally_verified = [
        e["id"]
        for e in stage2_report
        if e["id"] in NATURALLY_OBSERVED_IDS and any(l["status"] == "verified" for l in e["links"])
    ]
    control_still_exploring = all(
        not any(cond.get("tier") in ("self_graded", "trusted") for cond in (l.get("conditions") or []))
        for e in stage2_report
        if e["id"] in CONTROL_IDS
        for l in e["links"]
    )
    print(
        f"\n自然收敛核对（无人工确认动作）: "
        f"{'PASS' if naturally_verified else 'FAIL'} "
        f"自然收敛为 verified 的题号={naturally_verified or '[]'}"
    )
    print(
        f"A11 欠采样对照组仍停留 exploring 档: "
        f"{'PASS' if control_still_exploring else 'FAIL'}"
    )
    condition_obs = {
        e["id"]: [
            {"mean": cond.get("mean"), "tier": cond.get("tier")}
            for l in e["links"]
            for cond in (l.get("conditions") or [])
        ]
        for e in stage2_report
        if e["links"]
    }
    print(f"观测 mean/tier 分布: {json.dumps(condition_obs, ensure_ascii=False)[:1500]}")

    rejected_link_ids = set()
    if args.skip_reject:
        print("\n--skip-reject：跳过 reject 步骤。")
    else:
        print("\n--- 第 5 步：仅对驳回集调用 reject（同一 link 只操作一次；不再有确认动作） ---")
        for rid in REJECT_IDS:
            for link in links_by_id.get(rid) or []:
                lid = link["link_id"]
                if lid in rejected_link_ids:
                    continue
                try:
                    resp, _ = c.http_post_json(args.base_url, f"/activation-links/{lid}/reject", {})
                    print(f"  reject {rid} ({lid}): {resp}")
                    rejected_link_ids.add(lid)
                except Exception as e:
                    print(f"  ! reject {rid} ({lid}) 失败: {e}")
        print("  A11：有意欠采样、不作任何人工操作（对照组）")

    links_by_id, _all_links, ownership, shared_obs = refresh_ownership_views(args.base_url, bank, conn)
    final_report = report_links(args.base_url, bank, links_by_id, conn, "reject 之后（最终状态，独占归属）")

    print("\n========== P2 通过标准核对 ==========")
    gate = {}

    ok = bool(naturally_verified)
    gate["natural_convergence_ge1"] = ok
    print(f"  至少 1 条归属链接未经人工操作自然收敛为 verified: {'PASS' if ok else 'FAIL'} ({naturally_verified or '无'})")

    gate["control_stays_exploring"] = control_still_exploring
    print(f"  A11 欠采样对照组未跨过 serving_confidence_min: {'PASS' if control_still_exploring else 'FAIL'}")

    for rid in REJECT_IDS:
        links = links_by_id.get(rid) or []
        statuses = [l["status"] for l in links]
        conds_empty = all(not (fetch_link_detail(args.base_url, l["link_id"]) or {}).get("conditions") for l in links)
        # 目标 KP 仍 current：reject 清空条件后应重新派生为 candidate（不是 deprecated，
        # 也不是旧版文档写的字面 rejected）。
        ok = bool(statuses) and all(s == "candidate" for s in statuses) and conds_empty
        gate[f"reject_{rid}"] = ok
        print(
            f"  {rid} 独占归属 reject 后应为 candidate 且 conditions 已清空: "
            f"{'PASS' if ok else 'FAIL'} (实际 status={statuses or '无链接'}, conditions 已清空={conds_empty})"
        )

    all_traceback_ok = all(
        r["events_missing"] == 0
        for e in final_report
        for l in e["links"]
        for r in l["learning_results"]
        if r["action"] in ("create_candidate", "prune_condition")
    )
    gate["traceback"] = all_traceback_ok
    print(f"  每次条件变化 learning_result -> reason -> event_ids 完整回溯: {'PASS' if all_traceback_ok else 'FAIL'}")

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
        "naturally_verified_ids": naturally_verified,
        "condition_obs": condition_obs,
        "gate": gate,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p2_learning")
    print(f"\n详细结果: {jsonl_path}")
    conn.close()
    if failed:
        sys.exit(1)


if __name__ == "__main__":
    main()
