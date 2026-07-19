#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P2：学习转化 candidate 形成与人工晋升
（标准 1 前半 + 标准 3）。

培养清单（制度域 6 题 + 技术域 5 题）：A1、A2、A4、A9、A11、A12 + T8、T12、T15、T13，
以及 F1 前置问题「达梦怎么查询会话执行情况」（这题不在任何表格里，是 F 组 P3 对象守门
测试需要预先培养的一条独立链接）。

重要缺口（脚本不代为决定，需要人工介入）：
  A11、A12、T12、T13、T15 在方案题库表里只有 1 种问法（无变体），而
  promote_distinct_min=2 要求晋升前至少 2 个不同 question_hash——同一字面问多少遍
  distinct_n 都不会增长（方案第 7 节"变体问法是硬要求"明确提到这点）。这几题若不补充
  变体问法，最终会停在 candidate 阶段、晋升不达标，这是可预期的结果而不是脚本 bug。
  如果需要它们也进入 verified，请用 --extra-phrasing-file 传一个 JSON：
    {"A11": ["新问法1", "新问法2"], "T12": [...]}
  本脚本不会替你编造技术问题的变体措辞。
  同理 F1 前置问题（不在任何表格）默认给了 3 个内置问法（1 主 + 2 变体，见
  F1_PRE_DEFAULT_VARIANTS），如需替换请用同一个 JSON 文件覆盖 "F1_PRE" 键。

流程（严格对应方案 P2 步骤 1-6）：
  1. 对培养清单每题问一轮全部已知变体问法；
  2. POST /study/run，核对 candidate 创建 + learning_result(action=create_candidate)
     可回溯到 learning_events；
  3. 再对每题追加问 1-2 次已知变体（累计 success_n>=3、distinct>=2）；
  4. POST /study/run，核对 promote/pending_confirm 出现且链接状态未变（auto_promote=false）；
  5. 确认 A1/A4/A9/A12/T8/T12/T15/F1_PRE 晋升，A2/T13 执行 reject（A11 有意不处理，
     留作"未确认的 candidate 不应进入 verified 列表"的对照）；
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
                f"promote_distinct_min=2 要求不会达标，见脚本头部说明",
                file=sys.stderr,
            )
    return bank


def ask(base_url, question, timeout):
    """走真实客户端路径（/sessions -> /session/turn -> /answer/stream），每次新建
    独立 session——共现分组按 subject 归并，subject 只有走这条路径才会被解析，直接
    裸调 POST /answer 会导致每种问法各自落成独立的字面 term 分组，candidate 永远
    攒不到 confident_count（本脚本早期版本踩过这个坑，见 v1_common.ask_via_session
    的说明）。"""
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    if result is None:
        print(f"    ! 未走到 retrieve（action={turn.get('action')}），跳过")
    return result


def cultivate_round(base_url, bank, timeout, delay, label):
    """走 c.ask_via_session（真实客户端路径）并把每次命中的 direct_evidence
    point_id 记进 item["point_ids"]——activation_links.question_terms 是 LLM 生成
    的语义标签（如"数据库会话监控"），不是字面问题文本，事后没法按问法字符串反查
    对应链接，只有 point_id 是稳定、可比对的锚点。"""
    print(f"\n--- 培养轮次: {label} ---")
    for rid, item in bank.items():
        item.setdefault("point_ids", set())
        for q in item["question_variants"]:
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


def find_links_for_bank(base_url, bank):
    """按 point_id 匹配 activation-links（见 cultivate_round 的说明）；point_id
    还没采集到的题（如 --skip-cultivate 场景）退化回 question_terms 包含匹配兜底。"""
    all_links = c.http_get_json(base_url, "/activation-links?limit=500")
    by_id = {}
    for rid, item in bank.items():
        point_ids = item.get("point_ids") or set()
        if point_ids:
            matched = [link for link in all_links if link["point_id"] in point_ids]
        else:
            matched = [
                link
                for link in all_links
                if link["question_terms"] in item["question_variants"]
                or any(v in link["question_terms"] or link["question_terms"] in v for v in item["question_variants"])
            ]
        by_id[rid] = matched
    return by_id, all_links


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
            print(f"  {rid}: 未找到任何 activation_link")
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


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--skip-cultivate", action="store_true", help="跳过两轮培养提问，假设已经问过")
    parser.add_argument("--skip-confirm", action="store_true", help="跳过第 5 步的 confirm/reject 调用")
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
    links_by_id, _all_links = find_links_for_bank(args.base_url, bank)
    stage1_report = report_links(bank, links_by_id, conn, "第 1 次 study/run 之后")

    non_candidate = [
        e["id"]
        for e in stage1_report
        if e["links"] and not any(l["status"] == "candidate" for l in e["links"])
    ]
    missing_links = [e["id"] for e in stage1_report if not e["links"]]
    print(f"\ncandidate 创建核对: 缺 link={missing_links} 未变成 candidate={non_candidate}")

    if not args.skip_cultivate:
        cultivate_round(args.base_url, bank, args.timeout, args.delay, "第 2 轮（追加问法，累计 success_n>=3）")
    else:
        print("--skip-cultivate：跳过第 2 轮提问。")

    print("\n>>> POST /study/run（第 2 次）")
    study_result_2 = run_study(args.base_url)
    print(json.dumps(study_result_2, ensure_ascii=False, indent=2)[:2000])

    links_by_id, _all_links = find_links_for_bank(args.base_url, bank)
    stage2_report = report_links(bank, links_by_id, conn, "第 2 次 study/run 之后（应出现 promote/pending_confirm）")

    still_candidate_status = [
        e["id"]
        for e in stage2_report
        if e["links"] and any(l["status"] == "candidate" for l in e["links"])
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
    print(
        f"\nauto_promote=false 核对: 链接状态仍为 candidate（未被自动晋升）="
        f"{'PASS' if set(still_candidate_status) >= set(e['id'] for e in stage2_report if e['links']) else '部分 FAIL'} "
        f"{still_candidate_status}"
    )
    print(f"promote/pending_confirm 已出现: {has_promote_pending}")

    if args.skip_confirm:
        print("\n--skip-confirm：跳过 confirm/reject 步骤。")
    else:
        print("\n--- 第 5 步：confirm / reject ---")
        for rid in CONFIRM_IDS:
            for link in links_by_id.get(rid) or []:
                try:
                    resp, _ = c.http_post_json(args.base_url, f"/activation-links/{link['link_id']}/confirm", {})
                    print(f"  confirm {rid} ({link['link_id']}): {resp}")
                except Exception as e:
                    print(f"  ! confirm {rid} ({link['link_id']}) 失败: {e}")
        for rid in REJECT_IDS:
            for link in links_by_id.get(rid) or []:
                try:
                    resp, _ = c.http_post_json(args.base_url, f"/activation-links/{link['link_id']}/reject", {})
                    print(f"  reject {rid} ({link['link_id']}): {resp}")
                except Exception as e:
                    print(f"  ! reject {rid} ({link['link_id']}) 失败: {e}")

    links_by_id, _all_links = find_links_for_bank(args.base_url, bank)
    final_report = report_links(bank, links_by_id, conn, "confirm/reject 之后（最终状态）")

    print("\n========== P2 通过标准核对 ==========")
    for rid in CONFIRM_IDS:
        statuses = [l["status"] for l in (links_by_id.get(rid) or [])]
        ok = "verified" in statuses
        print(f"  {rid} 应为 verified: {'PASS' if ok else 'FAIL'} (实际: {statuses or '无链接'})")
    for rid in REJECT_IDS:
        statuses = [l["status"] for l in (links_by_id.get(rid) or [])]
        ok = "rejected" in statuses and "verified" not in statuses
        print(f"  {rid} 应为 rejected（不参与后续召回）: {'PASS' if ok else 'FAIL'} (实际: {statuses or '无链接'})")
    a11_statuses = [l["status"] for l in (links_by_id.get("A11") or [])]
    print(
        f"  A11（有意不确认，对照组）不应为 verified: "
        f"{'PASS' if 'verified' not in a11_statuses else 'FAIL'} (实际: {a11_statuses or '无链接'})"
    )
    all_traceback_ok = all(
        r["events_missing"] == 0
        for e in final_report
        for l in e["links"]
        for r in l["learning_results"]
    )
    print(f"  每次迁移 learning_result -> reason -> event_ids 完整回溯: {'PASS' if all_traceback_ok else 'FAIL'}")

    record = {
        "bank": {k: v["question_variants"] for k, v in bank.items()},
        "study_result_1": study_result_1,
        "study_result_2": study_result_2,
        "stage1_report": stage1_report,
        "stage2_report": stage2_report,
        "final_report": final_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p2_learning")
    print(f"\n详细结果: {jsonl_path}")
    conn.close()


if __name__ == "__main__":
    main()
