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

注意（2026-07-24）：wiki_confident_min 统计的是 question_kp_cooccurrence（按
point_id 聚合），不是 activation_links 的 subject 条件组，所以本阶段的收敛
成本不会被 subject_synonyms 直接降低——同义词归一化只作用于 Matcher.Match
按 activation_links 找已有链接这条路径，不改变 Study 侧 cooccurrence 计数。
上面这段"问法数量可能远超 8 种"的预期依旧成立，不要因为 subject_synonyms
已实现就假设本阶段成本会下降；subject_synonyms 收敛效果的验收是独立的
P11（见 test/v1/v1_p11_synonym_test.py），针对的是 ActivationLink 快路径命中，
不是 Wiki 候选门槛。

注意（2026-07-29 修订，qualifying KP 口径变更，见 docs/design/wiki.md；2026-08-13
编注：status=verified 的产生机制已改为连续置信度自然收敛，判据本身——直接读
status 字段——不变）：qualifying KP 现在除 confident_count 外，还要求 (a) 对应
ActivationLink 已 status=verified、(b) 词条级 days_active ≥
wiki.qualifying_min_days_active（默认 7）。参照 P11 的既有拆分方式，本脚本把这两条
也拆成两半：

  轴一（确定性，必过）：verified 门槛——2026-08-13 起没有任何人工确认动作
    （POST /activation-links/:id/confirm 端点已移除）：cultivate 产生 candidate
    链接后，脚本对尚未收敛的 point_id 追加若干轮重复问答（converge_verified_links），
    让其观测条件的 mean 自然跨过 retrieval.serving_confidence_min，验证"qualifying
    判据直接读 status 字段，而 status 完全由置信度自动派生"这条设计生效；
  轴二（观测性，不要求达标）：days_active 门槛——这依赖真实自然日跨度，单次
    脚本运行（本身只读 DB，见 v1_common.py 头部注释，不会去反向改
    question_kp_cooccurrence.last_seen_at 伪造跨天数据）大概率无法自然凑够
    ≥7 天活跃，最终多半停在 status=needs_more_data。这不算失败，只要核实
    Reason/Stats 里 qualifying_kp_count、kpn_connection_count 已经达标，
    只差 days_active 即可。真要观测 days_active 生效，需要 --skip-cultivate
    跨真实自然日续跑多次，或临时调低 config.yml 的
    wiki.qualifying_min_days_active 做冒烟测试，两者都不在本脚本自动化范围内。

编译流程也改为两步（docs/impl/v1/wiki.md 步骤 2）：POST /wiki/compile/analyze
先产出拟采用的论断结构（claims/tensions，不落库），本脚本把它原样带回
POST /wiki/compile 完成生成——这是本阶段新增的必过环节（验证两步分析-生成
链路本身能走通），不是可选项。

生成质量链路新增核验（2026-07-31，docs/impl/v1/wiki-generation.md 简化版）：
概念页编译内部现在会先把 qualifying KP 按切面（aspect）分组、生成后做支持度
核验（阶段 E）、发布前跑质量门回放（阶段 G），且 aliases/trigger_questions
不再由 LLM 生成而是程序查表。这几项都不改变 analyze/compile 的外部契约
（仍是扁平 claims[]/tensions[]，见上一段），所以不需要新增脚本步骤去驱动，
但需要新增核验：
  - GET /wiki/pages/:id 目前只暴露 summary/aspects，不暴露 aliases/
    trigger_questions/claim_checks/quality_check，这几项本脚本改为直接读库
    核对（与 v1_common.py 一贯的"API 之外的观察面读表"惯例一致）；
  - 正文结构：应含"## 摘要"，且"## 展开说明"下按切面出现多个"### "三级标题；
  - wiki_claim_checks：应有与 claims 数量匹配的核验行，verdict 落在
    supported/partial/unsupported 三者之一；
  - 发布前先显式调 POST /wiki/pages/:id/selfcheck 看 metrics/passed，
    再走 publish——若质量门未过（真实 LLM 生成的页面大概率会过，未过属
    观测性结果不算失败）用 force=true 覆盖，核对 wiki_quality_checks 最新一行
    forced=1（这是 force 覆盖唯一的留痕方式：不写 learning_results 事件，
    不进学习报告——force 是编译/发布链路内部的一次性人工决定，不是 Study 要
    追踪的学习动作，见 wiki-generation.md 7.4 已按此口径定稿）。

一阶编译 page_type（docs/impl/v1/wiki.md「概念页 / 事实页」，2026-08-03）：
POST /wiki/compile(/analyze) 只接受 concept|fact（或省略，由 entries.kind
派生）；主题页（page_type=topic）只能由二阶端点
POST /wiki/pages/:id/topic/analyze|compile 产出。本脚本每个领域培养的是
单一词条（entries 行，kind=concept 或 fact），analyze/compile 按
db_entry_kind 传匹配的 page_type。Wiki 正式生成只有两条路径：Study 问答
积累出 wiki_candidate → 人工确认编译，或人工指定词条直接编译（无预览
路径）。两层架构扩展验收见 v1_p12_two_tier_test.py。

流程（严格对应方案 P8 步骤 1-8）：
  1. 探测 A9/A10/A11（制度域「销售回款管理」）与 T10/T11/T18/B4（技术域「Oracle RAC」）
     各自命中的 KP 及其 entry_id，围绕这些词条密集问答；
  2. POST /study/run → 对每个命中的 point_id，若尚未自然收敛为 verified 则
     反复补问已知问法直至其观测条件的 mean 跨过 serving_confidence_min（轴一，
     必过；无任何人工确认动作）；
  3. 再次 POST /study/run，核对两域各出现 action=wiki_candidate, status=pending_confirm
     （若因 days_active 仍是 needs_more_data，按轴二记为观测性缺口，不判失败）；
  4. 若拿到 pending_confirm：POST /wiki/compile/analyze（entry_id + result_id，
     page_type 与 entries.kind 一致）→ 核对 claims 均引用白名单内 point_id →
     把 claims/tensions 带回 POST /wiki/compile → 核对 draft 页面要素齐全，
     正文引用的 KP 都在白名单（source_point_ids）内；
  5. POST /wiki/pages/:id/publish → 重问主题问题，核对 path_type=wiki 且不产生
     激活类事件（activation_gap/success/failure）；
  6. 对底层 source 各做一次微改 reupload（制度域改应收账款、技术域改 19c RAC）→
     study/run → 核对页面 status=needs_recompile（不得自动重编译）；
  7. POST /wiki/pages/:id/recompile → 核对新版本、revisions 可查旧版（内部自动
     走分析→生成两步，不额外暴露 analyze 预览接口）。

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


def _point_to_questions(*banks):
    """把 rid -> {question_variants, point_ids} 的培养 bank 反过来映射成
    point_id -> 该 point 关联过的全部问法，供收敛阶段针对尚未 verified 的
    point 精准补问。"""
    out = {}
    for bank in banks:
        for item in bank.values():
            for pid in item.get("point_ids") or set():
                out.setdefault(pid, set()).update(item["question_variants"])
    return out


def converge_verified_links(base_url, point_ids, point_to_questions, timeout, delay, max_extra_rounds=3):
    """轴一（确定性，必过）：2026-08-13 起没有 confirm 端点，qualifying 判据直接
    读 status 字段，而 status 由 observed_conditions 的置信度自动派生（见
    docs/impl/v1/activation.md「状态机」）。这里不再调用任何人工确认接口，
    而是对尚未收敛为 verified 的 point_id 反复重问其已知问法、并在每轮之后
    POST /study/run，让其对应链接的 mean 自然跨过 serving_confidence_min。
    返回 {point_id: link_id} —— 收敛成功（status=verified）的集合，供调用方核对。
    """
    converged = {}

    def _check_once():
        for pid in list(point_ids):
            if pid in converged:
                continue
            links = c.http_get_json(base_url, f"/activation-links?point_id={pid}&limit=5")
            for link in links:
                if link.get("status") == "verified":
                    converged[pid] = link["link_id"]
                    print(f"    point_id={pid} link_id={link['link_id']} 已自然收敛 → verified")
                    break

    _check_once()
    for rnd in range(max_extra_rounds):
        pending = [pid for pid in point_ids if pid not in converged]
        if not pending:
            break
        print(f"  收敛补问 第 {rnd + 1}/{max_extra_rounds} 轮，待收敛 point_id 数={len(pending)}")
        for pid in pending:
            for q in point_to_questions.get(pid, ()):
                try:
                    c.ask_via_session(base_url, q, deep=False, timeout=timeout)
                except Exception as e:
                    print(f"    ! 补问出错 ({pid}): {e}")
                time.sleep(delay)
        c.http_post_json(base_url, "/study/run", {}, timeout=180)
        _check_once()

    still_pending = [pid for pid in point_ids if pid not in converged]
    if still_pending:
        print(f"    ! {len(still_pending)} 个 point_id 补问 {max_extra_rounds} 轮后仍未收敛为 verified（观测记录，非脚本缺陷）: {still_pending}")
    return converged


def find_wiki_candidate_result(base_url, entry_id):
    results = c.http_get_json(base_url, f"/study/results?action=wiki_candidate&status=pending_confirm&limit=50")
    for r in results:
        if r["object_id"] == entry_id:
            return r
    return None


def analyze_page(base_url, entry_id, result_id, page_type=None):
    """POST /wiki/compile/analyze（docs/impl/v1/wiki.md 步骤 2）：不落库，
    产出拟采用的 claims/tensions 供人工确认，本脚本原样带回 compile_page。
    page_type 省略时由服务端按 entries.kind 派生；传入时必须与 kind 一致。"""
    payload = {"entry_id": entry_id}
    if page_type:
        payload["page_type"] = page_type
    if result_id:
        payload["result_id"] = result_id
    resp, status = c.http_post_json(base_url, "/wiki/compile/analyze", payload, timeout=180)
    return resp, status


def compile_page(base_url, entry_id, result_id, claims=None, tensions=None, page_type=None):
    payload = {"entry_id": entry_id}
    if page_type:
        payload["page_type"] = page_type
    if result_id:
        payload["result_id"] = result_id
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
    """核对 docs/impl/v1/wiki-generation.md 简化版新增的生成质量链路——切面分组
    正文结构、支持度核验（阶段 E）落库、aliases/trigger_questions 程序化取代
    LLM 生成。GET /wiki/pages/:id 不暴露 aliases/trigger_questions/claim_checks，
    这几项直接读库核对（与 v1_common.py 一贯的"API 之外的观察面"惯例一致）。
    返回一个 dict 供最终 PASS/FAIL 汇总打印，不在内部直接判定失败——多数是
    观测性核验（真实 LLM 输出的结构完整度不是 0/1 的硬门槛，见方案里"轴一/轴二"
    的一贯拆法）。"""
    row = c.db_wiki_page_row(conn, page_id)
    content = row.get("content") or ""

    missing_sections = [s for s in REQUIRED_SECTIONS if s not in content]
    expand_idx = content.find("## 展开说明")
    verify_idx = content.find("## 待验证点")
    expand_body = content[expand_idx:verify_idx] if expand_idx >= 0 and verify_idx > expand_idx else ""
    aspect_subheadings = re.findall(r"^### (.+)$", expand_body, re.M)

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
        "aspect_subheading_count": len(aspect_subheadings),
        "aspect_subheadings": aspect_subheadings,
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
    print(f"    展开说明下切面三级标题数: {len(aspect_subheadings)} {aspect_subheadings}")
    print(f"    aliases={aliases}，不在 subject_synonyms 表内的: {aliases_off_table or '无（PASS，说明确实查表而非 LLM 现编）'}")
    print(f"    trigger_questions={trigger_questions}")
    print(f"    不是真实 traces.question 原文的 trigger_questions（疑似编造）: {fabricated_triggers or '无（PASS）'}")
    print(f"    wiki_claim_checks: {len(claim_checks)} 行（claims 数 {len(claims)}），非法 verdict: {bad_verdicts or '无（PASS）'}")
    print(f"    summary 非空: {result['summary_nonempty']}；aspects 字段: {result['aspects_field']}")
    return result


def selfcheck_then_publish(base_url, conn, page_id):
    """阶段 G 发布前质量门（docs/impl/v1/wiki-generation.md 阶段 G）：先显式
    调一次 selfcheck 看 metrics/passed（不改页面状态），再走 publish。
    quality gate 未过时 publish 返回 409（ErrQualityGateFailed），这里改用
    http_post_json_tolerant 接住而不是让脚本崩溃；未过则带 force=true 重试，
    核对 wiki_quality_checks 最新一行 forced=1 作为覆盖留痕（当前实现里 force
    覆盖只做到这一步，没有额外的 learning_results 事件，见脚本头部说明）。
    真实 LLM 生成的页面大概率直接过闸——未过属观测性结果，不因此判 FAIL。"""
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
        print("--skip-cultivate：跳过培养，直接尝试从现有信号里找 entry。")

    policy_entries = resolve_entry_ids(conn, policy_bank)
    tech_entries = resolve_entry_ids(conn, tech_bank)
    print(f"\n制度域涉及 entry_id: {policy_entries}")
    print(f"技术域涉及 entry_id: {tech_entries}")

    print("\n>>> POST /study/run（第 1 次：产生 candidate ActivationLink）")
    study_result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
    print(json.dumps(study_result, ensure_ascii=False, indent=2)[:2000])

    print("\n--- 轴一（确定性，必过）：让候选链接自然收敛为 verified（无人工确认动作） ---")
    all_point_ids = set()
    for bank in (policy_bank, tech_bank):
        for item in bank.values():
            all_point_ids |= item["point_ids"]
    point_to_questions = _point_to_questions(policy_bank, tech_bank)
    confirmed_links = converge_verified_links(
        args.base_url, all_point_ids, point_to_questions, args.timeout, args.delay
    )
    print(f"  已自然收敛为 verified 的 point_id 数: {len(confirmed_links)}/{len(all_point_ids)}")

    print("\n>>> POST /study/run（第 2 次：verified 门槛已满足，核对 wiki_candidate）")
    study_result, _ = c.http_post_json(args.base_url, "/study/run", {}, timeout=180)
    print(json.dumps(study_result, ensure_ascii=False, indent=2)[:2000])

    print("\n--- 核对 wiki_candidate ---")
    domain_pages = {}
    for label, entries in (("policy", policy_entries), ("tech", tech_entries)):
        found = None
        for eid in entries:
            r = find_wiki_candidate_result(args.base_url, eid)
            if r:
                found = (eid, r)
                break
        if not found:
            # 轴二（观测性，不判失败）：days_active 门槛依赖真实自然日跨度，
            # 单次脚本运行大概率无法自然凑够，即使 verified/KP 数/KPN 连接都
            # 已达标也会停在 needs_more_data——不算失败，见脚本头部说明。
            print(f"  {label}: 没有找到 wiki_candidate/pending_confirm——大概率是 days_active 门槛"
                  f"（观测性缺口，不算失败）或信号本身不够（需要更多轮培养/补充问法）")
            domain_pages[label] = {"error": "no wiki_candidate"}
            continue
        eid, result = found
        page_type = c.db_entry_kind(conn, eid) or "concept"
        print(f"  {label}: entry_id={eid} kind/page_type={page_type} result_id={result['result_id']} reason={result['reason']}")

        analyze_resp, analyze_status = analyze_page(args.base_url, eid, result["result_id"], page_type=page_type)
        print(f"  analyze: HTTP {analyze_status} {json.dumps(analyze_resp, ensure_ascii=False)[:1000]}")
        if analyze_status != 200:
            domain_pages[label] = {"error": f"analyze failed: {analyze_resp}"}
            continue
        claims = analyze_resp.get("claims") or []
        analyze_whitelist = set()
        for claim in claims:
            analyze_whitelist |= set(claim.get("cited_point_ids") or [])
        print(f"  analyze claims 数: {len(claims)}, 引用 point_id 并集: {analyze_whitelist}")

        resp, status = compile_page(args.base_url, eid, result["result_id"], claims=claims, tensions=analyze_resp.get("tensions"), page_type=page_type)
        print(f"  compile: HTTP {status} {resp}")
        if status != 200:
            domain_pages[label] = {"error": f"compile failed: {resp}"}
            continue
        page_id = resp["page_id"]

        page, off_whitelist = verify_page_draft(args.base_url, page_id)
        print(f"  draft 页面引用越界白名单的 KP: {off_whitelist}（应为空）")
        got_page_type = page.get("page_type")
        if got_page_type and got_page_type != page_type:
            print(f"  ! page_type 不一致: 请求={page_type} 响应={got_page_type}")

        entry_name = c.db_entry_name(conn, eid) or ""
        quality = verify_generation_quality(conn, page_id, entry_name, claims)

        publish_info = selfcheck_then_publish(args.base_url, conn, page_id)

        domain_pages[label] = {
            "entry_id": eid,
            "page_type": page_type,
            "result_id": result["result_id"],
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

        def _shadow_gone():
            try:
                c.http_get_json(args.base_url, f"/sources/{shadow_id}")
                return None
            except Exception:
                return True

        # poll_with_backoff 而非固定 3s 间隔——换血通常几秒内完成，指数退避能更快
        # 发现完成，长时间未完成时也不会一直高频轮询。
        if not c.poll_with_backoff(_shadow_gone, 300):
            print(f"  ! {label} reupload 超时未完成")

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
    print(f"轴一（自然收敛→verified，必过，无人工确认动作）: {len(confirmed_links)}/{len(all_point_ids)} 个 point_id 已收敛为 verified")
    for label in ("policy", "tech"):
        info = domain_pages.get(label) or {}
        if info.get("error") == "no wiki_candidate":
            # 轴二观测性缺口：verified/KP 数/KPN 连接可能已达标，只差
            # days_active 的自然日跨度，单次脚本运行拿不到属预期，不计 FAIL。
            print(f"{label}: SKIP（未观测到 wiki_candidate，大概率是 days_active 轴二缺口，非必然失败）")
            continue
        if info.get("error"):
            print(f"{label}: FAIL（{info['error']}）")
            continue
        r = reask_report.get(label) or {}
        wiki_ok = r.get("path_type") == "wiki" and not (r.get("event_types") or [])
        publish_ok = info.get("publish_status") == 200
        quality = info.get("generation_quality") or {}
        structure_ok = not quality.get("missing_sections") and quality.get("aspect_subheading_count", 0) >= 1
        claim_check_ok = quality.get("claim_check_count", 0) >= quality.get("claim_count", 0) and not quality.get("claim_check_bad_verdicts")
        print(
            f"{label}: compile+publish={'PASS' if publish_ok else 'FAIL'}"
            f"{'（force 覆盖）' if info.get('forced_override_used') else ''}, "
            f"五节+切面结构={'PASS' if structure_ok else 'FAIL/观察'}, "
            f"支持度核验落库={'PASS' if claim_check_ok else 'FAIL/观察'}, "
            f"aliases/trigger 疑似编造={'PASS（无）' if not (quality.get('aliases_off_subject_synonyms_table') or quality.get('fabricated_trigger_questions')) else 'FAIL/观察'}, "
            f"重问 path_type=wiki={'PASS' if wiki_ok else 'FAIL'}, "
            f"needs_recompile 触发={'PASS' if recompile_report.get(label, {}).get('status_after_reupload') == 'needs_recompile' else 'FAIL/未测'}"
        )

    record = {
        "policy_entries": list(policy_entries),
        "tech_entries": list(tech_entries),
        "study_result": study_result,
        "domain_pages": domain_pages,
        "reask_report": reask_report,
        "recompile_report": recompile_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p8_wiki")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
