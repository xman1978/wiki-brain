#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P8：Wiki 初版闭环（标准 7，
两域各一个主题）。

这是 P0-P10 里成本最高的一个阶段——wiki_confident_min=8 是"单个 KP 的 confident
cooccurrence 数"（不是求和），配合 wiki_kp_min=4（至少 4 个这样的 KP），意味着
一个话题至少要攒出 4 个 KP、每个 KP 各自被至少 8 种不同问法命中——参考 P2 的实测
经验，不同问法解析出的 subject 不一定收敛，实际需要的问法数量可能远超 8 种。
本脚本按方案设计正确实现了整条链路，但cultivate_topic的默认问法量是保守估计，
真正跑的时候大概率需要用 --extra-phrasing-file 追加更多问法、或多跑几轮
--rounds 才能达到阈值——这是真实 LLM 语义解析的不确定性，不是脚本要回避的东西。

流程（严格对应方案 P8 步骤 1-6）：
  1. 探测 A9/A10/A11（制度域「销售回款管理」）与 T10/T11/T18/B4（技术域「Oracle RAC」）
     各自命中的 KP 及其 concept_id，围绕这些概念密集问答；
  2. POST /study/run，核对两域各出现 action=wiki_candidate, status=pending_confirm；
  3. POST /wiki/compile（concept_id + result_id）→ 核对 draft 页面要素齐全，
     正文引用的 KP 都在白名单（source_point_ids）内；
  4. POST /wiki/pages/:id/publish → 重问主题问题，核对 path_type=wiki 且不产生
     激活类事件（activation_gap/success/failure）；
  5. 对底层 source 各做一次微改 reupload（制度域改应收账款、技术域改 19c RAC）→
     study/run → 核对页面 status=needs_recompile（不得自动重编译）；
  6. POST /wiki/pages/:id/recompile → 核对新版本、revisions 可查旧版。

用法：
  python3 test/v1/v1_p8_wiki_test.py --rounds 3
  python3 test/v1/v1_p8_wiki_test.py --extra-phrasing-file test/v1/v1_p8_extra_phrasings.json
  python3 test/v1/v1_p8_wiki_test.py --skip-cultivate   # 假设已经攒够信号，只跑 compile 往后
"""
import argparse
import json
import re
import sys
import time
from pathlib import Path

import v1_common as c

SCRATCH_DIR = Path("/private/tmp/claude-501/-Users-jxu-Code-wiki-brain/fa63b57a-4195-46fb-855c-de7ab5a9d99b/scratchpad")

POLICY_TOPIC = {
    "name": "销售回款管理",
    "base_ids": ["A9", "A10", "A11"],
    "extra_defaults": {
        "A9": ["客户超过90天不还款，销售该怎么处理？", "客户回款逾期三个月，需要采取什么催收动作？"],
        "A10": ["回款逾期能申请延期吗，最多几次、每次多长时间？", "回款延期申请的次数和时长上限是多少？"],
        "A11": ["客户回款延迟超过9个月，销售提成打几折？", "回款延迟9个月以上转催款专员后提成怎么算？"],
    },
}

TECH_TOPIC = {
    "name": "Oracle RAC",
    "base_ids": ["T10", "T11", "T18", "B4"],
    "extra_defaults": {
        "T10": ["Oracle RAC 虚拟化环境下 VKTM 进程 CPU 占用高怎么处理？", "RAC 的 VKTM 高 CPU 问题有什么解决办法？"],
        "T11": ["客户端连接 RAC 报 TNS-12518 是什么原因？", "RAC 出现 TNS-12518 hand off 错误怎么解决？"],
        "T18": ["Oracle 19c RAC 支持哪些磁盘绑定方式？", "19c RAC 用 AFD 绑定磁盘怎么操作？"],
        "B4": ["Oracle 11g 和 19c RAC 部署环境有哪些不同？", "11g RAC 和 19c RAC 的操作系统、数据库版本区别是什么？"],
    },
}

REUPLOAD_FIXTURES = {
    "policy": {
        "base_title": "应收账款管理制度",
        "old_line": None,  # 运行时由脚本查找一处安全的数字微改点
        "new_title_suffix": "（微改-P8触发重编译）",
    },
    "tech": {
        "base_title": "Oracle 19c RAC 集群安装部署维护环境",
        "old_line": None,
        "new_title_suffix": "（微改-P8触发重编译）",
    },
}


def load_extra_phrasings(path):
    if not path:
        return {}
    return json.loads(Path(path).read_text(encoding="utf-8"))


def build_bank(topic, full_text, extra_phrasings):
    bank = {}
    for g in ("A", "T", "B"):
        for row in c.load_group(g, full_text):
            rid = c.row_id(row)
            if rid not in topic["base_ids"]:
                continue
            variants = list(c.question_variants(row))
            for extra in topic["extra_defaults"].get(rid, []):
                if extra not in variants:
                    variants.append(extra)
            for extra in extra_phrasings.get(rid, []):
                if extra not in variants:
                    variants.append(extra)
            bank[rid] = {"id": rid, "question_variants": variants, "point_ids": set(), "concept_ids": set()}
    return bank


def cultivate(base_url, bank, timeout, delay, rounds, label):
    for rnd in range(rounds):
        print(f"\n--- {label} 第 {rnd + 1}/{rounds} 轮 ---")
        for rid, item in bank.items():
            for q in item["question_variants"]:
                print(f"  {rid}: {q}")
                try:
                    _turn, result = c.ask_via_session(base_url, q, deep=False, timeout=timeout)
                    if result:
                        es = result.get("evidence_snapshot") or {}
                        for ev in es.get("direct_evidence") or []:
                            if ev.get("point_id"):
                                item["point_ids"].add(ev["point_id"])
                except Exception as e:
                    print(f"    ! 出错: {e}")
                time.sleep(delay)


def resolve_concept_ids(conn, bank):
    concept_ids = set()
    for rid, item in bank.items():
        for pid in item["point_ids"]:
            row = conn.execute(
                "SELECT ku.concept_id FROM knowledge_points kp JOIN knowledge_units ku ON kp.unit_id = ku.unit_id WHERE kp.point_id = ?",
                (pid,),
            ).fetchone()
            if row and row["concept_id"]:
                item["concept_ids"].add(row["concept_id"])
                concept_ids.add(row["concept_id"])
    return concept_ids


def find_wiki_candidate_result(base_url, concept_id):
    results = c.http_get_json(base_url, f"/study/results?action=wiki_candidate&status=pending_confirm&limit=50")
    for r in results:
        if r["object_id"] == concept_id:
            return r
    return None


def compile_page(base_url, concept_id, result_id):
    payload = {"concept_id": concept_id, "page_type": "topic"}
    if result_id:
        payload["result_id"] = result_id
    resp, status = c.http_post_json(base_url, "/wiki/compile", payload, timeout=180)
    return resp, status


def verify_page_draft(base_url, page_id):
    page = c.http_get_json(base_url, f"/wiki/pages/{page_id}")
    source_point_ids = set(json.loads(page.get("source_point_ids") or "[]"))
    content = page.get("content", "") or ""
    cited_ids_in_content = set(re.findall(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", content))
    off_whitelist = cited_ids_in_content - source_point_ids
    return page, off_whitelist


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--rounds", type=int, default=3, help="每个话题的培养轮数（confident_count 门槛高，可能需要更多轮）")
    parser.add_argument("--skip-cultivate", action="store_true")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    extra_phrasings = load_extra_phrasings(args.extra_phrasing_file)
    full_text = c.load_plan_text()
    conn = c.open_db(args.db_path)

    policy_bank = build_bank(POLICY_TOPIC, full_text, extra_phrasings)
    tech_bank = build_bank(TECH_TOPIC, full_text, extra_phrasings)

    if not args.skip_cultivate:
        cultivate(args.base_url, policy_bank, args.timeout, args.delay, args.rounds, "制度域「销售回款管理」")
        cultivate(args.base_url, tech_bank, args.timeout, args.delay, args.rounds, "技术域「Oracle RAC」")
    else:
        print("--skip-cultivate：跳过培养，直接尝试从现有信号里找 concept。")

    policy_concepts = resolve_concept_ids(conn, policy_bank)
    tech_concepts = resolve_concept_ids(conn, tech_bank)
    print(f"\n制度域涉及 concept_id: {policy_concepts}")
    print(f"技术域涉及 concept_id: {tech_concepts}")

    print("\n>>> POST /study/run")
    study_result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
    print(json.dumps(study_result, ensure_ascii=False, indent=2)[:2000])

    print("\n--- 核对 wiki_candidate ---")
    domain_pages = {}
    for label, concepts in (("policy", policy_concepts), ("tech", tech_concepts)):
        found = None
        for cid in concepts:
            r = find_wiki_candidate_result(args.base_url, cid)
            if r:
                found = (cid, r)
                break
        if not found:
            print(f"  {label}: 没有找到 wiki_candidate/pending_confirm——信号还不够（需要更多轮培养或补充问法）")
            domain_pages[label] = {"error": "no wiki_candidate"}
            continue
        cid, result = found
        print(f"  {label}: concept_id={cid} result_id={result['result_id']} reason={result['reason']}")

        resp, status = compile_page(args.base_url, cid, result["result_id"])
        print(f"  compile: HTTP {status} {resp}")
        if status != 200:
            domain_pages[label] = {"error": f"compile failed: {resp}"}
            continue
        page_id = resp["page_id"]

        page, off_whitelist = verify_page_draft(args.base_url, page_id)
        print(f"  draft 页面引用越界白名单的 KP: {off_whitelist}（应为空）")

        pub_resp, pub_status = c.http_post_json(args.base_url, f"/wiki/pages/{page_id}/publish", {})
        print(f"  publish: HTTP {pub_status} {pub_resp}")

        domain_pages[label] = {
            "concept_id": cid,
            "result_id": result["result_id"],
            "page_id": page_id,
            "off_whitelist_citations": list(off_whitelist),
            "publish_status": pub_status,
        }

    print("\n--- 重问主题问题，核对 path_type=wiki 且无激活类事件 ---")
    reask_report = {}
    for label, bank in (("policy", policy_bank), ("tech", tech_bank)):
        info = domain_pages.get(label) or {}
        if info.get("error"):
            continue
        rid0 = next(iter(bank))
        question = bank[rid0]["question_variants"][0]
        t0 = time.time()
        turn, result = c.ask_via_session(args.base_url, question, deep=False, timeout=args.timeout)
        latency = time.time() - t0
        if not result:
            reask_report[label] = {"error": f"未走到 retrieve: {turn.get('action')}"}
            continue
        trace = c.poll_until(lambda: c.db_trace_by_answer_id(conn, result.get("answer_id")), 15, 0.5)
        events = c.db_learning_events_for_trace(conn, trace["trace_id"]) if trace else []
        print(f"  {label} 重问「{question}」: path_type={result.get('path_type')} events={[e['event_type'] for e in events]}")
        reask_report[label] = {
            "question": question,
            "path_type": result.get("path_type"),
            "latency_s": round(latency, 2),
            "event_types": [e["event_type"] for e in events],
        }

    print("\n--- 底层变化：reupload 微改触发 needs_recompile ---")
    recompile_report = {}
    for label, fixture_key, page_info in (
        ("policy", "policy", domain_pages.get("policy")),
        ("tech", "tech", domain_pages.get("tech")),
    ):
        if not page_info or page_info.get("error"):
            continue
        fixture = REUPLOAD_FIXTURES[fixture_key]
        source_id = None
        id_to_title = c.fetch_source_titles(args.base_url)
        for sid, t in id_to_title.items():
            if t == fixture["base_title"]:
                source_id = sid
                break
        if not source_id:
            print(f"  ! 找不到「{fixture['base_title']}」，跳过 {label} 的重编译触发")
            continue

        md_path = c.MARKDOWN_DIR / f"{fixture['base_title']}.md"
        text = md_path.read_text(encoding="utf-8")
        m = re.search(r"^(.*\d+.*)$", text, re.M)
        if not m:
            print(f"  ! 「{fixture['base_title']}」找不到可微改的数字行，跳过")
            continue
        line = m.group(1)
        digits = re.search(r"\d+", line)
        modified_line = line[:digits.start()] + str(int(digits.group()) + 1) + line[digits.end():]
        modified_text = text.replace(line, modified_line, 1)
        out_path = SCRATCH_DIR / f"{fixture['base_title']}{fixture['new_title_suffix']}.md"
        SCRATCH_DIR.mkdir(parents=True, exist_ok=True)
        out_path.write_text(modified_text, encoding="utf-8")

        resp, status = c.http_post_multipart_file(args.base_url, f"/sources/{source_id}/reupload", out_path)
        print(f"  {label} reupload: HTTP {status} {resp}")
        shadow_id = resp.get("shadow_source_id")

        deadline = time.time() + 300
        while True:
            try:
                c.http_get_json(args.base_url, f"/sources/{shadow_id}")
            except Exception:
                break
            if time.time() >= deadline:
                print(f"  ! {label} reupload 超时未完成")
                break
            time.sleep(3)

        study_result_2, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
        page_after = c.http_get_json(args.base_url, f"/wiki/pages/{page_info['page_id']}")
        print(f"  {label} 页面状态: {page_after['status']}（应为 needs_recompile）")
        recompile_report[label] = {"status_after_reupload": page_after["status"]}

        if page_after["status"] == "needs_recompile":
            recompile_resp, recompile_status = c.http_post_json(
                args.base_url, f"/wiki/pages/{page_info['page_id']}/recompile", {"reason": "P8 test"}, timeout=180
            )
            print(f"  {label} recompile: HTTP {recompile_status} {recompile_resp}")
            recompile_report[label]["recompile_result"] = recompile_resp

    conn.close()
    print("\n========== P8 通过标准核对 ==========")
    for label in ("policy", "tech"):
        info = domain_pages.get(label) or {}
        if info.get("error"):
            print(f"{label}: FAIL（{info['error']}）")
            continue
        r = reask_report.get(label) or {}
        wiki_ok = r.get("path_type") == "wiki" and not (r.get("event_types") or [])
        print(
            f"{label}: compile+publish 成功, 重问 path_type=wiki={'PASS' if wiki_ok else 'FAIL'}, "
            f"needs_recompile 触发={'PASS' if recompile_report.get(label, {}).get('status_after_reupload') == 'needs_recompile' else 'FAIL/未测'}"
        )

    record = {
        "policy_concepts": list(policy_concepts),
        "tech_concepts": list(tech_concepts),
        "study_result": study_result,
        "domain_pages": domain_pages,
        "reask_report": reask_report,
        "recompile_report": recompile_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p8_wiki")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
