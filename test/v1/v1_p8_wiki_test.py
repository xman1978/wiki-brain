#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P8：Wiki 单层编译闭环
（标准 7，两域各一个主题）。

**2026-08-19 全文重写，取代此前的两层架构版本**：`docs/impl/v1/wiki.md` 已于
2026-08-18 整体改判为单层架构（`docs/design/wiki-single-tier-revision.md`），
本脚本按新契约重写：

  - Wiki 只有一种页面（page_type 恒为 topic），编译请求体是
    `{"entry_ids": ["...", ...]}`（数组，不是单个 entry_id），**不再有**
    `page_type` 请求参数——服务端不再区分 concept/fact/topic。
  - **不存在任何自动候选识别入口**：没有 Study 主题聚类、没有 qualifying KP
    自动标记、没有 `wiki_candidate` learning_result 生产方。因此本脚本不再
    需要"培养到 verified"这一步——Core 展开只要求 `lifecycle=current`，
    人工指定 entry_id 本身就是准入信号。培养问答仍然需要，但目的只是让
    entry 下出现足够多 current KP（供编译产出有内容的稳定结论），不是为了
    凑 confident_count/verified/days_active 三个已废弃的门槛。
  - `POST /wiki/compile/analyze` → `POST /wiki/compile` 两步分析-生成链路
    不变，但材料来源从"切面聚类分组"换成 Core/Context/Conflict 子图
    （`internal/wiki/subgraph.go`），citation 白名单 = 该子图覆盖的全部
    point_id。
  - 正文仍是五节结构（摘要/稳定结论/展开说明/待验证点/依赖来源），但"展开
    说明"下不再强制要求切面三级标题——本脚本不再核验 aspect 小标题数量，
    只核验五节标题齐全。
  - `wiki_claim_checks`（阶段 E 支持度核验）与 selfcheck→publish 质量门
    （阶段 G）机制不受单层化影响，核验方式不变。
  - needs_recompile 的自动标记来源只剩两条（`docs/impl/v1/wiki.md`「重编译
    标记」）：a. lifecycle 传导；新增的 entry_id 归属变化。Study 周期扫描
    新增 qualifying KP（b）与 ActivationLink 越过服务门槛（d）明确拍板不
    恢复。本脚本的 reupload 微改仍然通过 (a) 触发。

流程（对应方案 P8 步骤 1-6）：
  1. 探测 A9/A10/A11（制度域「销售回款管理」）与 T10/T11/T18/B4（技术域
     「Oracle RAC」）各自命中的 KP 及其 entry_id，围绕这些词条密集问答，
     只为产出足够的 current KP 内容，不追求任何置信度门槛；
  2. 直接 `POST /wiki/compile/analyze`（`entry_ids` = 该域命中的全部
     entry_id，无 `result_id`、无 `page_type`）→ 核对 claims 均引用
     Core/Context/Conflict 白名单内 point_id → 原样带回 `POST /wiki/compile`
     → 核对 draft 页面要素齐全（五节结构、citation 不越界）；
  3. `POST /wiki/pages/:id/selfcheck` → `POST /wiki/pages/:id/publish`
     （未过质量门则 force=true 覆盖，核对 wiki_quality_checks.forced=1）；
  4. 重问主题问题，核对 `path_type=wiki` 且不产生激活类事件；
  5. 对底层 source 各做一次微改 reupload（制度域改应收账款、技术域改
     19c RAC）→ 核对页面 `status=needs_recompile`（lifecycle 传导触发，
     不得自动重编译）；
  6. `POST /wiki/pages/:id/recompile` → 核对新版本、revisions 可查旧版。

用法：
  python3 test/v1/v1_p8_wiki_test.py --rounds 3
  python3 test/v1/v1_p8_wiki_test.py --extra-phrasing-file test/v1/v1_p8_extra_phrasings.json
  python3 test/v1/v1_p8_wiki_test.py --skip-cultivate   # 假设已有足够 current KP，只跑 compile 往后
"""
import argparse
import json
import re
import sys
import time
from pathlib import Path

import v1_common as c

SCRATCH_DIR = Path("/tmp/v1_p8_scratch")

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
        "new_title_suffix": "（微改-P8触发重编译）",
    },
    "tech": {
        "base_title": "Oracle 19c RAC 集群安装部署维护环境",
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
            bank[rid] = {"id": rid, "question_variants": variants, "point_ids": set(), "entry_ids": set()}
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


def resolve_entry_ids(conn, bank):
    entry_ids = set()
    for rid, item in bank.items():
        for pid in item["point_ids"]:
            row = conn.execute(
                "SELECT ku.entry_id FROM knowledge_points kp JOIN knowledge_units ku ON kp.unit_id = ku.unit_id WHERE kp.point_id = ?",
                (pid,),
            ).fetchone()
            if row and row["entry_id"]:
                item["entry_ids"].add(row["entry_id"])
                entry_ids.add(row["entry_id"])
    return entry_ids


def analyze_page(base_url, entry_ids):
    """POST /wiki/compile/analyze（docs/impl/v1/wiki.md「触发与 API」）：
    不落库，产出拟采用的 claims/tensions 供人工确认，本脚本原样带回
    compile_page。请求体只有 entry_ids（数组）+ 可选 result_id，没有
    page_type——单层化后不再区分 concept/fact/topic。"""
    payload = {"entry_ids": sorted(entry_ids)}
    resp, status = c.http_post_json(base_url, "/wiki/compile/analyze", payload, timeout=180)
    return resp, status


def compile_page(base_url, entry_ids, claims=None, tensions=None):
    payload = {"entry_ids": sorted(entry_ids)}
    if claims is not None:
        payload["claims"] = claims
    if tensions is not None:
        payload["tensions"] = tensions
    resp, status = c.http_post_json(base_url, "/wiki/compile", payload, timeout=180)
    return resp, status


def verify_page_draft(base_url, page_id):
    page = c.http_get_json(base_url, f"/wiki/pages/{page_id}")
    source_point_ids = set(json.loads(page.get("source_point_ids") or "[]"))
    content = page.get("content", "") or ""
    cited_ids_in_content = set(re.findall(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", content))
    off_whitelist = cited_ids_in_content - source_point_ids
    return page, off_whitelist


REQUIRED_SECTIONS = ["## 摘要", "## 稳定结论", "## 展开说明", "## 待验证点", "## 依赖来源"]


def verify_generation_quality(conn, page_id, entry_name, claims):
    """核对 docs/impl/v1/wiki-generation.md 简化版的生成质量链路——正文五节
    结构（不再强制"展开说明"下有切面三级标题，Core/Context/Conflict 是材料
    组织方式，不是正文强制排版）、支持度核验（阶段 E）落库、aliases/
    trigger_questions 程序化取代 LLM 生成。GET /wiki/pages/:id 不暴露
    aliases/trigger_questions/claim_checks，这几项直接读库核对。"""
    row = c.db_wiki_page_row(conn, page_id)
    content = row.get("content") or ""

    missing_sections = [s for s in REQUIRED_SECTIONS if s not in content]

    aliases = json.loads(row.get("aliases") or "[]")
    trigger_questions = json.loads(row.get("trigger_questions") or "[]")
    synonyms = c.db_subject_synonyms(conn, canonical=entry_name)
    synonym_terms = {s["term"] for s in synonyms}
    aliases_off_table = [a for a in aliases if a not in synonym_terms]

    real_questions = c.db_trace_questions(conn)
    fabricated_triggers = [q for q in trigger_questions if q not in real_questions]

    claim_checks = c.db_wiki_claim_checks(conn, page_id)
    bad_verdicts = [r["verdict"] for r in claim_checks if r["verdict"] not in ("supported", "partial", "unsupported")]

    result = {
        "missing_sections": missing_sections,
        "aliases": aliases,
        "aliases_off_subject_synonyms_table": aliases_off_table,
        "trigger_questions": trigger_questions,
        "fabricated_trigger_questions": fabricated_triggers,
        "claim_count": len(claims),
        "claim_check_count": len(claim_checks),
        "claim_check_bad_verdicts": bad_verdicts,
        "summary_nonempty": bool((row.get("summary") or "").strip()),
        "aspects_field": row.get("aspects"),
    }
    print("  生成质量核验:")
    print(f"    五节标题缺失: {missing_sections or '无（PASS）'}")
    print(f"    aliases={aliases}，不在 subject_synonyms 表内的: {aliases_off_table or '无（PASS，说明确实查表而非 LLM 现编）'}")
    print(f"    trigger_questions={trigger_questions}")
    print(f"    不是真实 traces.question 原文的 trigger_questions（疑似编造）: {fabricated_triggers or '无（PASS）'}")
    print(f"    wiki_claim_checks: {len(claim_checks)} 行（claims 数 {len(claims)}），非法 verdict: {bad_verdicts or '无（PASS）'}")
    print(f"    summary 非空: {result['summary_nonempty']}；aspects 字段（单层化后恒 '[]'，遗留列）: {result['aspects_field']}")
    return result


def selfcheck_then_publish(base_url, conn, page_id):
    """阶段 G 发布前质量门（docs/impl/v1/wiki-generation.md 阶段 G，单层化
    不影响）：先显式调一次 selfcheck 看 metrics/passed（不改页面状态），
    再走 publish。quality gate 未过时 publish 返回 409，未过则带 force=true
    重试，核对 wiki_quality_checks 最新一行 forced=1。真实 LLM 生成的页面
    大概率直接过闸——未过属观测性结果，不因此判 FAIL。"""
    sc_resp, sc_status = c.http_post_json(base_url, f"/wiki/pages/{page_id}/selfcheck", {}, timeout=180)
    print(f"  selfcheck: HTTP {sc_status} passed={sc_resp.get('passed')} metrics={json.dumps(sc_resp.get('metrics'), ensure_ascii=False)}")

    pub_resp, pub_status = c.http_post_json_tolerant(base_url, f"/wiki/pages/{page_id}/publish", {})
    forced = False
    if pub_status == 409:
        blocking = (sc_resp.get("metrics") or {}).get("blocking_reasons")
        print(f"  publish 首次被质量门拦截（409，观测性结果非失败）: {blocking}；改用 force=true 重试")
        pub_resp, pub_status = c.http_post_json_tolerant(base_url, f"/wiki/pages/{page_id}/publish", {"force": True})
        forced = True
    print(f"  publish: HTTP {pub_status} {pub_resp}")

    qc_rows = c.db_wiki_quality_checks(conn, page_id)
    latest_qc = qc_rows[0] if qc_rows else None
    print(f"  wiki_quality_checks 最新一行: passed={latest_qc.get('passed') if latest_qc else None}, "
          f"forced={latest_qc.get('forced') if latest_qc else None}（期望: 若上面走了 force 重试，这里应为 1）")
    return {
        "selfcheck": sc_resp,
        "publish_status": pub_status,
        "forced_override_used": forced,
        "latest_quality_check": latest_qc,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--rounds", type=int, default=2, help="每个话题的培养轮数（只为产出足够 current KP 内容，无置信度门槛，通常无需很多轮）")
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
        print("--skip-cultivate：跳过培养，直接尝试从现有信号里找 entry。")

    policy_entries = resolve_entry_ids(conn, policy_bank)
    tech_entries = resolve_entry_ids(conn, tech_bank)
    print(f"\n制度域涉及 entry_id: {policy_entries}")
    print(f"技术域涉及 entry_id: {tech_entries}")

    print("\n--- 人工指定 entry_id 直接编译（无自动候选识别，无置信度门槛） ---")
    domain_pages = {}
    for label, entries in (("policy", policy_entries), ("tech", tech_entries)):
        if not entries:
            print(f"  {label}: 未解析到 entry_id，跳过（培养问答未命中任何 KP，检查 base_ids/extra_defaults）")
            domain_pages[label] = {"error": "no entry_ids resolved"}
            continue
        print(f"  {label}: entry_ids={sorted(entries)}")

        analyze_resp, analyze_status = analyze_page(args.base_url, entries)
        print(f"  analyze: HTTP {analyze_status} {json.dumps(analyze_resp, ensure_ascii=False)[:1000]}")
        if analyze_status != 200:
            domain_pages[label] = {"error": f"analyze failed: {analyze_resp}"}
            continue
        claims = analyze_resp.get("claims") or []
        analyze_whitelist = set()
        for claim in claims:
            analyze_whitelist |= set(claim.get("cited_point_ids") or [])
        print(f"  analyze claims 数: {len(claims)}, 引用 point_id 并集: {analyze_whitelist}")

        resp, status = compile_page(args.base_url, entries, claims=claims, tensions=analyze_resp.get("tensions"))
        print(f"  compile: HTTP {status} {resp}")
        if status != 200:
            domain_pages[label] = {"error": f"compile failed: {resp}"}
            continue
        page_id = resp["page_id"]

        page, off_whitelist = verify_page_draft(args.base_url, page_id)
        print(f"  draft 页面引用越界白名单的 KP: {off_whitelist}（应为空）")
        got_page_type = page.get("page_type")
        if got_page_type and got_page_type != "topic":
            print(f"  ! page_type 不是预期的 topic（单层化后恒为 topic）: {got_page_type}")

        entry_name = c.db_entry_name(conn, next(iter(entries))) or ""
        quality = verify_generation_quality(conn, page_id, entry_name, claims)

        publish_info = selfcheck_then_publish(args.base_url, conn, page_id)

        domain_pages[label] = {
            "entry_ids": sorted(entries),
            "page_id": page_id,
            "off_whitelist_citations": list(off_whitelist),
            "generation_quality": quality,
            "publish_status": publish_info["publish_status"],
            "forced_override_used": publish_info["forced_override_used"],
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

    print("\n--- 底层变化：reupload 微改触发 needs_recompile（lifecycle 传导） ---")
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

        def _shadow_gone():
            try:
                c.http_get_json(args.base_url, f"/sources/{shadow_id}")
                return None
            except Exception:
                return True

        if not c.poll_with_backoff(_shadow_gone, 300):
            print(f"  ! {label} reupload 超时未完成")

        page_after = c.http_get_json(args.base_url, f"/wiki/pages/{page_info['page_id']}")
        print(f"  {label} 页面状态: {page_after['status']}（应为 needs_recompile，经 SetUnitLifecycle → WikiEntryNotifier 传导，不经 Study）")
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
        publish_ok = info.get("publish_status") == 200
        quality = info.get("generation_quality") or {}
        structure_ok = not quality.get("missing_sections")
        claim_check_ok = quality.get("claim_check_count", 0) >= quality.get("claim_count", 0) and not quality.get("claim_check_bad_verdicts")
        print(
            f"{label}: compile+publish={'PASS' if publish_ok else 'FAIL'}"
            f"{'（force 覆盖）' if info.get('forced_override_used') else ''}, "
            f"五节结构={'PASS' if structure_ok else 'FAIL/观察'}, "
            f"支持度核验落库={'PASS' if claim_check_ok else 'FAIL/观察'}, "
            f"aliases/trigger 疑似编造={'PASS（无）' if not (quality.get('aliases_off_subject_synonyms_table') or quality.get('fabricated_trigger_questions')) else 'FAIL/观察'}, "
            f"重问 path_type=wiki={'PASS' if wiki_ok else 'FAIL'}, "
            f"needs_recompile 触发={'PASS' if recompile_report.get(label, {}).get('status_after_reupload') == 'needs_recompile' else 'FAIL/未测'}"
        )

    record = {
        "policy_entries": list(policy_entries),
        "tech_entries": list(tech_entries),
        "domain_pages": domain_pages,
        "reask_report": reask_report,
        "recompile_report": recompile_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p8_wiki")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
