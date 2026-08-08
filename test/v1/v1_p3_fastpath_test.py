#!/usr/bin/env python3
"""
V1 验收测试方案 P3：快路径生效（标准 1 后半 + 标准 2）+ 对象守门 + ActivationLink 可用性。

对齐 test/v1/v1-acceptance-test-plan.md「P3 快路径生效」2026-08-07 口径：
  准确（守门/不串台）硬门槛；已培养问法 M1+M2 召回率 ≥70% 软门槛；
  不要求 M1/M2 逐次全过；不引入向量匹配。

依赖 P2 已把 A1/A9/A12/T8/T12/T15/F1_PRE 确认为 verified，A2/T13 reject→deprecated。

用法：
  python3 test/v1/v1_p3_fastpath_test.py \\
    --baseline test/v1/results/v1_p1_baseline_20260807-105306.jsonl \\
    --extra-phrasing-file test/v1/v1_p2_extra_phrasings.json

  # 跳过会改库/重启的步骤（M6/M7、V4）
  python3 test/v1/v1_p3_fastpath_test.py --skip-db-mutate --skip-v4
"""
from __future__ import annotations

import argparse
import json
import re
import sqlite3
import subprocess
import sys
import time
from pathlib import Path

import v1_common as c

REPO_ROOT = c.REPO_ROOT
CONFIG_PATH = REPO_ROOT / "config" / "config.yml"
RUN_SH = REPO_ROOT / "run.sh"

# 已培养问法（M1+M2）合计召回率软门槛；准确项另见 summarize 硬门槛
RECALL_MIN_RATIO = 0.70

FASTPATH_IDS = ["A1", "A9", "A12", "T8", "T12", "T15"]
FULL_ONLY_IDS = ["A2", "T13"]  # M8: deprecated, 应走 full
F1_PRE_ID = "F1_PRE"
F1_PRE_BASELINE = "达梦怎么查询会话执行情况"
F1_PRE_M4_PROBE = "达梦在Windows环境下怎么查询会话执行情况？"
F1_PRE_DEFAULT_VARIANTS = [
    "达梦怎么查询会话执行情况",
    "达梦数据库怎么查看当前的会话执行情况？",
    "怎么在达梦里查询会话的执行状态？",
]
F_PRECONDITION = {"F1": F1_PRE_ID, "F2": "T12", "F3": "T8"}

# M3 观测用：表内/P2 未出现过的自然改写（不计入通过标准）
DEFAULT_M3_PHRASINGS = {
    "A1": "业务招待费报销有没有期限限制？",
    "A9": "客户超过九十天没回款，应该怎么催收？",
    "A12": "项目考核评优秀需要达到什么标准，绩效系数是多少？",
    "T8": "在 Oracle RAC 环境里怎样打开数据库归档模式？",
    "T12": "达梦数据库提示语句句柄数超限或内存不足，该怎么处理？",
    "T15": "神通数据库的最大连接数默认值和上限分别是多少？",
}

# M2：只调词序/空白/标点，不改用词
M2_NORMALIZE = {
    "A1": "招待费用报销期限是多久 ？",
    "A9": "客户逾期90天没付款怎么办？",
    "A12": "项目考核优秀的标准和 绩效系数？",
    "T8": "Oracle RAC怎么开启归档？",
    "T12": "达梦报「语句句柄个数超过上限或系统内存不足」怎么解决？",
    "T15": "神通数据库最大连接数默认是多少、上限多少 ?",
}


def load_extra_phrasings(path):
    if not path:
        return {}
    return json.loads(Path(path).read_text(encoding="utf-8"))


def expected_source_cell(row):
    for key in ("期望证据来源", "期望证据", "来源"):
        if key in row:
            return row[key]
    return None


def load_row_by_id(full_text, target_id):
    for g in ("A", "T"):
        for row in c.load_group(g, full_text):
            if c.row_id(row) == target_id:
                return row
    return None


def wait_trace(conn, answer_id, timeout_s=20):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s, 0.5)


def ask_once(base_url, conn, question, id_to_title=None, expected_titles=None, key_terms=None, timeout=180):
    """真实客户端路径（session turn + answer/stream），带四元组。"""
    id_to_title = id_to_title or {}
    expected_titles = expected_titles or set()
    key_terms = key_terms or []
    t0 = time.time()
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    latency = time.time() - t0
    if result is None:
        return {
            "question": question,
            "answer_id": None,
            "path_type": None,
            "latency_s": round(latency, 2),
            "content": None,
            "direct_hit": False,
            "found_terms": [],
            "trace_id": None,
            "activation_link_ids": [],
            "direct_point_ids": [],
            "event_types": None,
            "error": f"未走到 retrieve（action={turn.get('action')}）",
            "subject": (turn.get("expanded_query") or {}).get("subject"),
            "intent": (turn.get("expanded_query") or {}).get("intent"),
            "constraint": (turn.get("expanded_query") or {}).get("constraint"),
        }

    content = result.get("content", "")
    es = result.get("evidence_snapshot") or {}
    direct_ev = es.get("direct_evidence") or []
    direct_ids = c.evidence_source_ids(direct_ev)
    direct_titles = sorted({id_to_title.get(sid, sid) for sid in direct_ids})
    direct_hit = bool(expected_titles) and any(t in expected_titles for t in direct_titles)
    found_terms = [t for t in key_terms if t.lower() in content.lower()]

    trace = wait_trace(conn, result.get("answer_id"))
    events = c.db_learning_events_for_trace(conn, trace["trace_id"]) if trace else []
    direct_point_ids = json.loads(trace["direct_point_ids"] or "[]") if trace else []
    activation_link_ids = json.loads(trace["activation_link_ids"] or "[]") if trace else []
    eq = turn.get("expanded_query") or {}

    return {
        "question": question,
        "answer_id": result.get("answer_id"),
        "path_type": result.get("path_type"),
        "latency_s": round(latency, 2),
        "content": content,
        "direct_hit": direct_hit,
        "found_terms": found_terms,
        "trace_id": trace["trace_id"] if trace else None,
        "activation_link_ids": activation_link_ids,
        "direct_point_ids": direct_point_ids,
        "event_types": [e["event_type"] for e in events] if trace else None,
        "subject": eq.get("subject") or "",
        "intent": eq.get("intent") or "",
        "audience": eq.get("audience") or "",
        "constraint": eq.get("constraint") or "",
    }


def find_verified_link_by_probe(base_url, probe_question, timeout):
    _turn, result = c.ask_via_session(base_url, probe_question, deep=False, timeout=timeout)
    if not result:
        return None, None
    es = result.get("evidence_snapshot") or {}
    point_ids = [ev.get("point_id") for ev in (es.get("direct_evidence") or []) if ev.get("point_id")]
    if not point_ids:
        return None, None
    all_links = c.http_get_json(base_url, "/activation-links?status=verified&limit=500")
    for link in all_links:
        if link["point_id"] in point_ids:
            return link["link_id"], link["point_id"]
    return None, point_ids[0]


def load_baseline(path):
    if not path:
        return {}
    baseline = {}
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        rec = json.loads(line)
        if rec.get("id") and rec.get("latency_s") is not None:
            # 同 id 多行时取最后一次（完整跑通常一题一行）
            baseline[rec["id"]] = rec["latency_s"]
    return baseline


def open_db_rw(db_path):
    conn = sqlite3.connect(str(db_path), timeout=30)
    conn.row_factory = sqlite3.Row
    return conn


def apply_empty_observed_fallback(db_path, link_id, question_terms):
    """模拟存量空观测组：清空 denorm 四列与 observed_conditions，只留 question_terms 回退。"""
    rw = open_db_rw(db_path)
    rw.execute(
        "UPDATE activation_links SET subject_terms='', intent_terms='', audience='', "
        "constraint_terms='', observed_conditions='[]', question_terms=? WHERE link_id=?",
        (question_terms, link_id),
    )
    rw.commit()
    rw.close()


def strip_observed_groups_by_constraint_substr(db_path, link_id, needles):
    """从 observed_conditions 剔除 constraint 含指定子串的组（隔离 M4 历史超集污染）。"""
    rw = open_db_rw(db_path)
    row = rw.execute(
        "SELECT observed_conditions FROM activation_links WHERE link_id=?", (link_id,)
    ).fetchone()
    if not row:
        rw.close()
        return 0, 0
    conds = json.loads(row["observed_conditions"] or "[]")
    lowered = [n.casefold() for n in needles]
    kept = []
    removed = 0
    for g in conds:
        cons = (g.get("constraint") or "").casefold()
        if any(n in cons for n in lowered):
            removed += 1
            continue
        kept.append(g)
    rw.execute(
        "UPDATE activation_links SET observed_conditions=? WHERE link_id=?",
        (json.dumps(kept, ensure_ascii=False), link_id),
    )
    rw.commit()
    rw.close()
    return removed, len(kept)


def restart_server(base_url="http://127.0.0.1:8800"):
    """改库/改配置后重启以刷新 Matcher 缓存。"""
    print("... 重启服务（run.sh restart）以刷新 Matcher 缓存/配置 ...")
    subprocess.check_call([str(RUN_SH), "restart"], cwd=str(REPO_ROOT))
    deadline = time.time() + 60
    while time.time() < deadline:
        if c.wait_for_server(base_url):
            print("... 服务已就绪")
            return
        time.sleep(1)
    raise RuntimeError(f"重启后服务未就绪: {base_url}")


def set_fast_path_verify(enabled: bool):
    text = CONFIG_PATH.read_text(encoding="utf-8")
    new_val = "true" if enabled else "false"
    replaced, n = re.subn(
        r"(fast_path_verify:\s*)(true|false)",
        rf"\g<1>{new_val}",
        text,
        count=1,
    )
    if n != 1:
        raise RuntimeError(f"未能改写 config.yml 中的 fast_path_verify（匹配数={n}）")
    CONFIG_PATH.write_text(replaced, encoding="utf-8")
    print(f"... config fast_path_verify={new_val}")


def m3_phrasing(rid, table_variants, extra_phrasings):
    known = list(table_variants)
    for p in extra_phrasings.get(rid, []):
        if p not in known:
            known.append(p)
    # 优先用表外/额外文件里尚未作为主问法的表述
    candidates = list(extra_phrasings.get(rid, [])) + ([DEFAULT_M3_PHRASINGS[rid]] if rid in DEFAULT_M3_PHRASINGS else [])
    for p in candidates:
        if p not in table_variants[:1]:  # 避开主问法
            return p, known
    if len(known) >= 2:
        return known[-1], known
    return known[0], known


def run_m1(base_url, conn, full_text, id_to_title, timeout, delay, repeats):
    report = {}
    print("\n========== M1 精确复现（主问法原句 ×3） ==========")
    for rid in FASTPATH_IDS:
        row = load_row_by_id(full_text, rid)
        question = c.question_variants(row)[0]
        expected_titles = c.expected_titles_for(expected_source_cell(row) or "")
        key_terms = c.extract_key_terms(row.get("期望答案要点", ""))
        print(f"\n--- {rid} 主问法: {question} ---")
        runs = []
        for i in range(repeats):
            r = ask_once(base_url, conn, question, id_to_title, expected_titles, key_terms, timeout)
            print(
                f"  第{i+1}次: path_type={r['path_type']} 耗时={r['latency_s']}s "
                f"direct_hit={r['direct_hit']} links={r['activation_link_ids']} events={r['event_types']}"
            )
            runs.append(r)
            time.sleep(delay)
        report[rid] = {"question": question, "runs": runs}
    return report


def run_m2(base_url, conn, id_to_title, timeout, delay):
    report = {}
    print("\n========== M2 归一化容差（词序/空白/标点） ==========")
    for rid in FASTPATH_IDS:
        question = M2_NORMALIZE[rid]
        print(f"\n--- {rid}: {question} ---")
        r = ask_once(base_url, conn, question, id_to_title, timeout=timeout)
        print(f"  path_type={r['path_type']} links={r['activation_link_ids']}")
        report[rid] = r
        time.sleep(delay)
    return report


def run_m3(base_url, conn, full_text, extra_phrasings, id_to_title, timeout, delay, repeats):
    report = {}
    print("\n========== M3 改写观察（不计入通过标准） ==========")
    for rid in FASTPATH_IDS:
        row = load_row_by_id(full_text, rid)
        table_variants = c.question_variants(row)
        question, _ = m3_phrasing(rid, table_variants, extra_phrasings)
        expected_titles = c.expected_titles_for(expected_source_cell(row) or "")
        key_terms = c.extract_key_terms(row.get("期望答案要点", ""))
        # 记录改写前该 point 上已有的 candidate 数（用首次跑后的 link point 反查）
        print(f"\n--- {rid} 改写: {question} ---")
        runs = []
        for i in range(repeats):
            r = ask_once(base_url, conn, question, id_to_title, expected_titles, key_terms, timeout)
            print(
                f"  第{i+1}次: path_type={r['path_type']} 耗时={r['latency_s']}s "
                f"links={r['activation_link_ids']} subject={r.get('subject')!r}"
            )
            runs.append(r)
            time.sleep(delay)
        # 观测：是否新增 candidate（按跑完后的 activation-links 列表长度粗记）
        new_candidates = []
        point_ids = set()
        for r in runs:
            point_ids.update(r.get("direct_point_ids") or [])
        for pid in point_ids:
            cands = c.http_get_json(base_url, f"/activation-links?point_id={pid}&status=candidate&limit=100")
            new_candidates.append({"point_id": pid, "candidate_count": len(cands), "candidates": cands})
        hit_fast = sum(1 for r in runs if r["path_type"] == "fast")
        report[rid] = {
            "question": question,
            "runs": runs,
            "fast_hit_rate": f"{hit_fast}/{len(runs)}",
            "candidate_snapshot": new_candidates,
        }
    return report


def run_m4(base_url, db_path, timeout):
    """超集排除前剔除历史 Windows 观测组，避免前次 P3 污染导致误命中。"""
    print("\n========== M4 约束超集排除（应 path_type=full） ==========")
    print(f"探针: {F1_PRE_M4_PROBE}")
    link_id, _ = find_verified_link_by_probe(base_url, F1_PRE_BASELINE, timeout)
    if link_id:
        removed, kept = strip_observed_groups_by_constraint_substr(
            db_path, link_id, ["windows", "window"]
        )
        print(f"  隔离 link={link_id}: 剔除含 Windows 的观测组 {removed} 条，剩余 {kept}")
        if removed:
            restart_server(base_url)
    else:
        print("  ! 未找到 F1_PRE verified 链接，超集排除可能空洞")

    conn = c.open_db(db_path)
    r = ask_once(base_url, conn, F1_PRE_M4_PROBE, timeout=timeout)
    conn.close()
    print(f"  path_type={r['path_type']} constraint={r.get('constraint')!r} links={r['activation_link_ids']}")
    return r


def run_m5_gating(base_url, conn, full_text, extra_phrasings, timeout, delay, repeats):
    print("\n========== M5 对象/约束错配排除（F 组） ==========")
    report = {}
    precondition_link_ids = {}
    precondition_point_ids = {}
    for f_id, pre_id in F_PRECONDITION.items():
        if pre_id == F1_PRE_ID:
            probe = (extra_phrasings.get(F1_PRE_ID) or F1_PRE_DEFAULT_VARIANTS)[0]
        else:
            row = load_row_by_id(full_text, pre_id)
            variants = c.question_variants(row) if row else []
            probe = variants[0] if variants else None
        link_id, point_id = (find_verified_link_by_probe(base_url, probe, timeout) if probe else (None, None))
        precondition_link_ids[f_id] = link_id
        precondition_point_ids[f_id] = point_id
        print(f"{f_id} 前置（{pre_id}，探测: {probe}）: link_id={link_id} point_id={point_id}")
        if not link_id:
            print(f"  ! {pre_id} 无 verified 链接，{f_id} 守门测试会空洞通过")

    for row in c.load_group("F", full_text):
        f_id = c.row_id(row)
        question = row["守门问题"]
        pre_link_id = precondition_link_ids.get(f_id)
        pre_point_id = precondition_point_ids.get(f_id)
        print(f"\n--- {f_id}: {question} ---")
        runs = []
        failures = 0
        for i in range(repeats):
            r = ask_once(base_url, conn, question, timeout=timeout)
            gated_wrongly = bool(
                pre_link_id
                and pre_link_id in (r["activation_link_ids"] or [])
                and pre_point_id
                and pre_point_id in (r["direct_point_ids"] or [])
            )
            if gated_wrongly:
                failures += 1
            print(
                f"  第{i+1}次: path_type={r['path_type']} links={r['activation_link_ids']} "
                f"points={r['direct_point_ids']} {'! 守门失效' if gated_wrongly else '正常'}"
            )
            runs.append({**r, "gated_wrongly": gated_wrongly})
            time.sleep(delay)
        report[f_id] = {
            "precondition_link_id": pre_link_id,
            "precondition_point_id": pre_point_id,
            "precondition_exists": bool(pre_link_id),
            "runs": runs,
            "failures": failures,
        }
    return report


def _is_fast_hit(r):
    """已培养问法召回：快路径且激活到至少一条链接。"""
    return r.get("path_type") == "fast" and bool(r.get("activation_link_ids"))


def _looks_like_honest_gap(text):
    t = text or ""
    return any(x in t for x in ("暂无", "无法回答", "没有相关", "未能找到"))


def run_e3(base_url, timeout):
    print("\n========== E3 会话追问（达梦 BUFFER → 神通呢？）不串台 ==========")
    sess, _ = c.http_post_json(base_url, "/sessions", {})
    session_id = sess["session_id"]

    def turn_and_answer(user_input):
        turn, _ = c.http_post_json(
            base_url, "/session/turn", {"session_id": session_id, "user_input": user_input}, timeout=timeout
        )
        eq = turn.get("expanded_query") or {}
        expanded = eq.get("expanded_question") or user_input
        payload = {
            "question": expanded,
            "deep": False,
            "session_id": session_id,
            "subject": eq.get("subject") or "",
            "intent": eq.get("intent") or "",
            "audience": eq.get("audience") or "",
            "constraint": eq.get("constraint") or "",
            "domain_ids": eq.get("domain_ids") or [],
            "domain_resolved": True,
            "follow_up": bool(eq.get("follow_up")),
        }
        ans = c.http_post_sse(base_url, "/answer/stream", payload, timeout=timeout) or {}
        return expanded, ans, eq

    q1, ans1, _ = turn_and_answer("达梦 BUFFER 配多大？")
    q2, ans2, eq2 = turn_and_answer("神通呢？")
    content1 = ans1.get("content") or ""
    content2 = ans2.get("content") or ""
    expanded_ok = "神通" in q2
    answer_mentions = "神通" in content2 or "BUF_DATA_BUFFER_PAGES" in content2
    gap_ok = _looks_like_honest_gap(content2)
    # 串台：展开已是神通，却把达梦首轮答案大段原样当作神通回答（且非诚实缺口）
    recycled = bool(content1) and len(content1) > 40 and content1[:60] in content2
    cross = expanded_ok and recycled and not gap_ok and not answer_mentions
    no_cross = not cross
    report = {
        "session_id": session_id,
        "turn1_expanded": q1,
        "answer1": content1,
        "turn2_expanded": q2,
        "answer2": content2,
        "path_type2": ans2.get("path_type"),
        "constraint2": eq2.get("constraint") or "",
        "expanded_mentions_shentong": expanded_ok,
        "answer_mentions_shentong": answer_mentions,
        "honest_gap": gap_ok,
        "cross_product": cross,
        "no_cross": no_cross,
    }
    print(json.dumps({k: v for k, v in report.items() if k not in ("answer1", "answer2")}, ensure_ascii=False, indent=2))
    print(
        f"  expanded→神通: {expanded_ok}; 不串台: {no_cross}；"
        f"answer→神通(观测): {answer_mentions}; 诚实缺口(观测): {gap_ok}"
    )
    return report


def run_m6_m7(base_url, db_path, timeout, delay, repeats):
    """清空观测组+denorm 四列，模拟存量迁移链接的 question_terms 回退；测完恢复。

    注意：慢路径 confident 会 EnrichFromConfidentFullPath 写回观测组。
    因此每次 M6 若走了 full，下一轮前必须再次清空；M7 开始前也必须再次清空。
    """
    print("\n========== M6/M7 四元组缺失回退（改库模拟存量链接） ==========")
    ro = c.open_db(db_path)
    link_id, point_id = find_verified_link_by_probe(base_url, F1_PRE_BASELINE, timeout)
    if not link_id:
        print("! 找不到 F1_PRE verified 链接，跳过 M6/M7")
        ro.close()
        return {"skipped": True, "reason": "no F1_PRE verified link"}

    row = dict(ro.execute("SELECT * FROM activation_links WHERE link_id=?", (link_id,)).fetchone())
    qt_row = ro.execute(
        "SELECT question_terms FROM traces WHERE question = ? AND question_terms != '' "
        "ORDER BY created_at DESC LIMIT 1",
        (F1_PRE_BASELINE,),
    ).fetchone()
    baseline_qt = (qt_row["question_terms"] if qt_row else None) or row["question_terms"]
    ro.close()
    original = {
        "subject_terms": row["subject_terms"],
        "intent_terms": row["intent_terms"],
        "audience": row["audience"],
        "constraint_terms": row["constraint_terms"],
        "observed_conditions": row["observed_conditions"] or "[]",
        "question_terms": row["question_terms"],
    }
    print(f"选用 link_id={link_id} point_id={point_id}")
    print(f"  原 observed_conditions 长度={len(original['observed_conditions'])}")
    print(f"  回退 question_terms 设为: {baseline_qt!r}")

    def reset_empty_and_reload():
        apply_empty_observed_fallback(db_path, link_id, baseline_qt)
        restart_server(base_url)

    reset_empty_and_reload()
    conn = c.open_db(db_path)

    m6_runs = []
    print(f"\n--- M6 精确复现 ×{repeats}: {F1_PRE_BASELINE} ---")
    for i in range(repeats):
        if i > 0 and m6_runs and m6_runs[-1]["path_type"] != "fast":
            # 上一轮 full 会 Enrich 写回观测组，必须清掉再测回退
            print("  ... 上一轮为 full，重新清空观测组以免污染回退语义")
            conn.close()
            reset_empty_and_reload()
            conn = c.open_db(db_path)
        r = ask_once(base_url, conn, F1_PRE_BASELINE, timeout=timeout)
        ok = r["path_type"] == "fast" and link_id in (r["activation_link_ids"] or [])
        print(f"  第{i+1}次: path_type={r['path_type']} links={r['activation_link_ids']} {'PASS' if ok else 'FAIL'}")
        m6_runs.append({**r, "ok": ok})
        time.sleep(delay)

    print("... M7 前再次清空观测组（防止 M6 的 full 写回或缓存残留）")
    conn.close()
    reset_empty_and_reload()
    conn = c.open_db(db_path)

    m7_q = "达梦数据库如何监控当前会话的执行状态？"
    m7_runs = []
    print(f"\n--- M7 改写不得命中该空观测组链接 ×{repeats}: {m7_q} ---")
    for i in range(repeats):
        if i > 0 and m7_runs and link_id in (m7_runs[-1].get("activation_link_ids") or []):
            # 极端情况：改写若误命中又 Enrich，下一轮前清掉
            print("  ... 上一轮误命中本 link，重新清空观测组")
            conn.close()
            reset_empty_and_reload()
            conn = c.open_db(db_path)
        elif i > 0 and m7_runs and m7_runs[-1]["path_type"] == "full":
            # full 可能 Enrich 了其它组到本 link（同 point），仍清一次更稳
            conn.close()
            reset_empty_and_reload()
            conn = c.open_db(db_path)
        r = ask_once(base_url, conn, m7_q, timeout=timeout)
        ok = link_id not in (r["activation_link_ids"] or [])
        print(
            f"  第{i+1}次: path_type={r['path_type']} links={r['activation_link_ids']} "
            f"{'PASS' if ok else 'FAIL'}（判定：本 link 未出现）"
        )
        m7_runs.append({**r, "ok": ok})
        time.sleep(delay)

    conn.close()

    rw = open_db_rw(db_path)
    rw.execute(
        "UPDATE activation_links SET subject_terms=?, intent_terms=?, audience=?, constraint_terms=?, "
        "observed_conditions=?, question_terms=? WHERE link_id=?",
        (
            original["subject_terms"],
            original["intent_terms"],
            original["audience"],
            original["constraint_terms"],
            original["observed_conditions"],
            original["question_terms"],
            link_id,
        ),
    )
    rw.commit()
    rw.close()
    restart_server(base_url)

    return {
        "link_id": link_id,
        "point_id": point_id,
        "original": {
            k: original[k]
            for k in ("subject_terms", "intent_terms", "audience", "constraint_terms", "question_terms")
        },
        "baseline_question_terms": baseline_qt,
        "m6_runs": m6_runs,
        "m7_runs": m7_runs,
        "m7_question": m7_q,
    }


def run_m8(base_url, conn, full_text, id_to_title, timeout, delay):
    print("\n========== M8 状态过滤（deprecated / candidate） ==========")
    report = {"full_only": {}, "a11_candidate": None}

    for rid in FULL_ONLY_IDS:
        row = load_row_by_id(full_text, rid)
        question = c.question_variants(row)[0]
        r = ask_once(base_url, conn, question, id_to_title, timeout=timeout)
        print(f"{rid}（应 full）: path_type={r['path_type']} links={r['activation_link_ids']}")
        report["full_only"][rid] = r
        time.sleep(delay)

    # A11：方案期望仍为 candidate；P2 若因邻近簇误 confirm 则记观测
    a11_links = []
    all_links = c.http_get_json(base_url, "/activation-links?limit=500")
    # 用 A11 主问法探测 point，再看该 point 上链接状态
    row = load_row_by_id(full_text, "A11")
    q = c.question_variants(row)[0]
    r = ask_once(base_url, conn, q, id_to_title, timeout=timeout)
    print(f"A11 重问: path_type={r['path_type']}（candidate 可记信号但不走快路径 → 期望 full） links={r['activation_link_ids']}")
    for lid in r.get("activation_link_ids") or []:
        for link in all_links:
            if link["link_id"] == lid:
                a11_links.append({"link_id": lid, "status": link.get("status")})
    # 另查仍为 candidate 的链接数（全局观测）
    cands = [x for x in all_links if x.get("status") == "candidate"]
    report["a11_candidate"] = {
        "ask": r,
        "activated_link_statuses": a11_links,
        "global_candidate_count": len(cands),
        "note": "若 A11 归属链接在 P2 被邻近簇误升为 verified，本项记观测而非硬失败",
    }
    return report


def run_v4(base_url, db_path, full_text, id_to_title, timeout):
    print("\n========== V4 灰度关闭 fast_path_verify ==========")
    row = load_row_by_id(full_text, "A1")
    question = c.question_variants(row)[0]
    try:
        set_fast_path_verify(False)
        restart_server()
        conn = c.open_db(db_path)
        r = ask_once(base_url, conn, question, id_to_title, timeout=timeout)
        conn.close()
        print(f"  A1 path_type={r['path_type']} links={r['activation_link_ids']}（期望 fast，且不因关闭校验报错）")
        return r
    finally:
        set_fast_path_verify(True)
        restart_server()


def summarize(m1, m2, m3, m4, m5, e3, m6m7, m8, v4, baseline, repeats):
    print("\n========== P3 通过标准核对（准确硬门槛 + 召回≥70% 软门槛） ==========")
    verdicts = {}

    # M1 明细（不逐题全过）
    m1_hits = 0
    m1_total = 0
    for rid, block in m1.items():
        runs = block["runs"]
        hits = sum(1 for r in runs if _is_fast_hit(r))
        m1_hits += hits
        m1_total += len(runs)
        not_fast = [r for r in runs if r["path_type"] != "fast"]
        no_success = [r for r in runs if "activation_success" not in (r["event_types"] or [])]
        avg = sum(r["latency_s"] for r in runs) / len(runs)
        base = baseline.get(rid)
        drop = ((base - avg) / base * 100) if base else None
        print(
            f"M1 {rid}: 命中 {hits}/{len(runs)}"
            f"{'（含非 fast ' + str(len(not_fast)) + '）' if not_fast else ''}；"
            f"activation_success 缺失 {len(no_success)}/{repeats}（观测）；"
            f"avg={avg:.2f}s"
            + (f"（P1={base:.2f}s，下降{drop:.0f}%，参考≥40%、不挡通过）" if drop is not None else "（无 baseline）")
        )
    verdicts["M1_detail"] = {"hits": m1_hits, "total": m1_total}

    # M2 明细
    m2_hits = sum(1 for r in m2.values() if _is_fast_hit(r))
    m2_total = len(m2)
    for rid, r in m2.items():
        print(
            f"M2 {rid}: {'HIT' if _is_fast_hit(r) else 'MISS'} "
            f"path_type={r['path_type']}"
        )
    verdicts["M2_detail"] = {"hits": m2_hits, "total": m2_total}

    recall_hits = m1_hits + m2_hits
    recall_total = m1_total + m2_total
    recall_ratio = (recall_hits / recall_total) if recall_total else 0.0
    recall_ok = recall_ratio >= RECALL_MIN_RATIO
    print(
        f"M1+M2 召回: {recall_hits}/{recall_total} = {recall_ratio:.1%} "
        f"（门槛 ≥{RECALL_MIN_RATIO:.0%}）{'PASS' if recall_ok else 'FAIL'}"
    )
    verdicts["M1_M2_recall"] = recall_ok
    verdicts["M1_M2_recall_ratio"] = round(recall_ratio, 4)

    # M3 仅观测
    print("--- M3 改写观察（不计通过） ---")
    for rid, block in m3.items():
        print(f"M3 {rid}: fast_hit_rate={block['fast_hit_rate']} q={block['question']}")
    verdicts["M3"] = "observe"

    # M4
    m4_ok = m4["path_type"] == "full"
    print(f"M4 超集排除: path_type=full {'PASS' if m4_ok else 'FAIL'}（实际 {m4['path_type']}）")
    verdicts["M4"] = m4_ok

    # M5
    total_fail = sum(g["failures"] for g in m5.values())
    for f_id, g in m5.items():
        print(f"M5 {f_id}: 守门失效 {g['failures']}/{repeats}")
    m5_ok = total_fail == 0
    print(f"M5 合计: {'PASS' if m5_ok else 'FAIL'}（失效 {total_fail} 次，目标 0）")
    verdicts["M5"] = m5_ok

    # E3：展开转向神通 + 不串台（诚实缺口算通过）
    e3_ok = bool(e3.get("expanded_mentions_shentong")) and bool(e3.get("no_cross"))
    print(
        f"E3 追问不串台: {'PASS' if e3_ok else 'FAIL'} "
        f"（expanded→神通={e3.get('expanded_mentions_shentong')}, no_cross={e3.get('no_cross')}）"
    )
    verdicts["E3"] = e3_ok

    # M6/M7
    if m6m7.get("skipped"):
        print(f"M6/M7: SKIP（{m6m7.get('reason')}）")
        verdicts["M6"] = None
        verdicts["M7"] = None
    else:
        m6_ok = all(r["ok"] for r in m6m7["m6_runs"])
        m7_ok = all(r["ok"] for r in m6m7["m7_runs"])
        print(f"M6 空观测组精确复现: {'PASS' if m6_ok else 'FAIL'}")
        print(f"M7 空观测组改写不命中该链接: {'PASS' if m7_ok else 'FAIL'}")
        verdicts["M6"] = m6_ok
        verdicts["M7"] = m7_ok

    # M8
    m8_ok = True
    for rid, r in m8["full_only"].items():
        ok = r["path_type"] == "full"
        m8_ok = m8_ok and ok
        print(f"M8 {rid} deprecated→full: {'PASS' if ok else 'FAIL'}（实际 {r['path_type']}）")
    a11 = m8["a11_candidate"]
    print(
        f"M8 A11: path_type={a11['ask']['path_type']} activated={a11['activated_link_statuses']} "
        f"global_candidates={a11['global_candidate_count']}（观测）"
    )
    verdicts["M8"] = m8_ok

    # V4
    if v4 is None:
        print("V4: SKIP")
        verdicts["V4"] = None
    else:
        v4_ok = v4["path_type"] == "fast" and not v4.get("error")
        print(f"V4 fast_path_verify=false 仍 fast: {'PASS' if v4_ok else 'FAIL'}（实际 {v4['path_type']}）")
        verdicts["V4"] = v4_ok

    # 总判定：计入硬/软门槛的布尔项（跳过 observe / detail / ratio / None）
    skip_keys = {"M3", "M1_detail", "M2_detail", "M1_M2_recall_ratio"}
    scored = {k: v for k, v in verdicts.items() if k not in skip_keys and v is not None}
    overall = all(bool(v) for v in scored.values()) if scored else False
    print(f"\n总体（计入项）: {'PASS' if overall else 'FAIL'}")
    print(f"分项: {verdicts}")
    return overall, verdicts


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--extra-phrasing-file", default=None)
    parser.add_argument("--baseline", default=None, help="P1 报告 jsonl，用于耗时下降对比")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--skip-db-mutate", action="store_true", help="跳过 M6/M7（改库+重启）")
    parser.add_argument("--skip-v4", action="store_true", help="跳过 V4（改配置+重启）")
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    extra = load_extra_phrasings(args.extra_phrasing_file)
    baseline = load_baseline(args.baseline)
    full_text = c.load_plan_text()
    id_to_title = c.fetch_source_titles(args.base_url)
    conn = c.open_db(args.db_path)

    m1 = run_m1(args.base_url, conn, full_text, id_to_title, args.timeout, args.delay, args.repeats)
    m2 = run_m2(args.base_url, conn, id_to_title, args.timeout, args.delay)
    m3 = run_m3(args.base_url, conn, full_text, extra, id_to_title, args.timeout, args.delay, args.repeats)
    m4 = run_m4(args.base_url, args.db_path, args.timeout)
    m5 = run_m5_gating(args.base_url, conn, full_text, extra, args.timeout, args.delay, args.repeats)
    e3 = run_e3(args.base_url, args.timeout)

    conn.close()

    if args.skip_db_mutate:
        m6m7 = {"skipped": True, "reason": "--skip-db-mutate"}
    else:
        m6m7 = run_m6_m7(args.base_url, args.db_path, args.timeout, args.delay, args.repeats)

    conn = c.open_db(args.db_path)
    m8 = run_m8(args.base_url, conn, full_text, id_to_title, args.timeout, args.delay)
    conn.close()

    if args.skip_v4:
        v4 = None
    else:
        v4 = run_v4(args.base_url, args.db_path, full_text, id_to_title, args.timeout)

    overall, verdicts = summarize(m1, m2, m3, m4, m5, e3, m6m7, m8, v4, baseline, args.repeats)

    record = {
        "m1": m1,
        "m2": m2,
        "m3": m3,
        "m4": m4,
        "m5": m5,
        "e3": e3,
        "m6m7": m6m7,
        "m8": m8,
        "v4": v4,
        "baseline_used": args.baseline,
        "recall_min_ratio": RECALL_MIN_RATIO,
        "verdicts": verdicts,
        "overall": overall,
    }
    # strip bulky answer texts in nested runs for jsonl readability? keep full for audit
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p3_fastpath")
    print(f"\n详细结果: {jsonl_path}")
    sys.exit(0 if overall else 1)


if __name__ == "__main__":
    main()
