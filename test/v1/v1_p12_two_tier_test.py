#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P12：Wiki 两层架构扩展
（docs/impl/v1/two-tier-task-brief.md，页面关系 / 主题页候选 / 二阶编译 /
检索骨架注入 / 写作草稿 / 回流防护）。

前提：依赖 P8 已经跑过并各产出一个 published 的 concept 页（制度域「销售回款
管理」、技术域「Oracle RAC」）——本阶段从 test/v1/results/ 里读取最近一次
v1_p8_wiki_*.jsonl 拿 concept_id/page_id，不重新培养信号。若找不到，提示先跑
P8，非致命错误直接退出。

同 P8/P11 的既有拆分方式，本阶段也分两条轴：

  轴一（确定性，必过）：两层架构新增端点/字段的契约行为——
    - POST /wiki/compile、/wiki/compile/analyze 传 page_type=topic 必须被拒绝
      （docs/impl/v1/wiki.md 步骤 8：主题页只能走 topic/analyze|compile，
      一阶编译端点收紧为仅接受 concept）；
    - GET /wiki/pages/:id/relations 结构正确（可为空——P8 的制度/技术两页
      分属不同领域，天然不会有 KPN 关系，为空是预期，不是失败）；
    - 写作草稿全生命周期：POST drafts → GET（evidence_index 非空）→
      PATCH 内容 → 页面正文不受影响（无 draft → page 写回路径）→ DELETE；
    - 回流来源标记：POST /sources 带 origin=wiki_draft + origin_page_id 时，
      DB 里 sources.origin/origin_page_id 正确落库（自体祖先排除本身的行为
      已由 Go 单测 internal/unit/kpn_reflow_test.go 覆盖，本阶段只验证 API
      入口把字段正确透传落库）；
    - GET /study/reports/latest 响应包含 question_complexity 板块（结构
      本身，不要求 groups 非空）。

  轴二（观测性，不要求达标）：真实数据下页面关系/主题候选是否自然形成——
    - 检查 knowledge_point_relations 里是否已有可供页面关系派生的行
      （P7 阶段的跨 Source fixture 可能已产生）、对应 wiki_page_relations
      是否已派生；
    - 检查 learning_results 里是否已出现 topic_page_candidate（需要
      topic_member_min=3 个同域已发布概念页互相 related 且 contradicts 不
      反客为主——P8 目前每个领域只发布了 1 个概念页，单次运行大概率观测
      不到，属预期中的观测性缺口，不判失败）。

用法：
  python3 test/v1/v1_p12_two_tier_test.py
"""
import argparse
import glob
import json
import sys
import time
from pathlib import Path

import v1_common as c

SCRATCH_DIR = Path("/tmp/v1_p12_scratch")


def load_latest_p8_result():
    files = sorted(glob.glob(str(c.RESULTS_DIR / "v1_p8_wiki_*.jsonl")))
    if not files:
        return None
    with open(files[-1], encoding="utf-8") as f:
        lines = [json.loads(l) for l in f if l.strip()]
    return lines[-1] if lines else None


def axis1_reject_topic_page_type(base_url):
    """POST /wiki/compile(/analyze) 必须拒绝 page_type=topic（docs/impl/v1/wiki.md
    步骤 8：一阶编译端点收紧为仅接受 concept，主题页只能走 topic/analyze|compile）。"""
    results = {}
    for path in ("/wiki/compile/analyze", "/wiki/compile"):
        resp, status = c.http_post_json(base_url, path, {"concept_id": "nonexistent", "page_type": "topic"})
        results[path] = {"status": status, "rejected": status != 200}
        print(f"  {path} page_type=topic: HTTP {status} rejected={status != 200} resp={resp}")
    return results


def axis1_relations(base_url, page_id, label):
    resp = c.http_get_json(base_url, f"/wiki/pages/{page_id}/relations")
    ok = isinstance(resp, list)
    if ok:
        for row in resp:
            for key in ("relation_type", "other_page_id", "derived_from"):
                if key not in row:
                    ok = False
    print(f"  {label} page_id={page_id} relations: {len(resp) if isinstance(resp, list) else 'N/A'} 行, 结构校验={'PASS' if ok else 'FAIL'}")
    return {"count": len(resp) if isinstance(resp, list) else None, "structure_ok": ok, "rows": resp}


def axis1_draft_lifecycle(base_url, page_id, label):
    """草稿全生命周期 + 写回防护核对（docs/impl/v1/wiki.md 步骤 10）。"""
    page_before = c.http_get_json(base_url, f"/wiki/pages/{page_id}")
    content_before = page_before.get("content", "")
    title_before = page_before.get("title", "")

    resp, status = c.http_post_json(base_url, f"/wiki/pages/{page_id}/drafts", {"mode": "page"})
    if status != 200:
        print(f"  ! {label} 创建草稿失败: HTTP {status} {resp}")
        return {"error": f"create draft failed: {resp}"}
    draft_id = resp["draft_id"]

    draft = c.http_get_json(base_url, f"/wiki/drafts/{draft_id}")
    has_evidence = bool(draft.get("evidence_index"))
    print(f"  {label} draft_id={draft_id} evidence_index 非空={has_evidence} stale={draft.get('stale')}")

    new_content = content_before + "\n\n（人工改写追加内容，仅存在于草稿）"
    new_title = title_before + "（草稿改写）"
    # v1_common 的 http_post_json 固定 POST，这里手写一次 PATCH。
    import urllib.request

    req = urllib.request.Request(
        f"{base_url}/wiki/drafts/{draft_id}",
        data=json.dumps({"title": new_title, "content": new_content}).encode("utf-8"),
        method="PATCH",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=30) as r:
        patch_status = r.status
        patch_resp = json.loads(r.read().decode("utf-8"))
    print(f"  {label} PATCH 草稿: HTTP {patch_status}")

    page_after = c.http_get_json(base_url, f"/wiki/pages/{page_id}")
    unaffected = page_after.get("content") == content_before and page_after.get("title") == title_before
    print(f"  {label} 页面正文/标题在草稿改写后是否不变: {'PASS' if unaffected else 'FAIL（发现写回！）'}")

    del_resp, del_status = c.http_delete_json(base_url, f"/wiki/drafts/{draft_id}")
    print(f"  {label} DELETE 草稿: HTTP {del_status}")

    return {
        "draft_id": draft_id,
        "evidence_index_present": has_evidence,
        "patch_status": patch_status,
        "page_unaffected_by_draft_edit": unaffected,
        "delete_status": del_status,
    }


def axis1_reflow_origin_tagging(base_url, conn, origin_page_id):
    """回流来源标记（docs/impl/v1/wiki.md 步骤 10「回流的自体循环必须挡住」）：
    验证 POST /sources 的 origin/origin_page_id 字段被正确透传落库——自体祖先
    排除本身的行为由 Go 单测覆盖（internal/unit/kpn_reflow_test.go），这里只测
    API 入口。"""
    SCRATCH_DIR.mkdir(parents=True, exist_ok=True)
    draft_export = SCRATCH_DIR / "reflow_draft_export.md"
    draft_export.write_text("# 回流草稿导出\n\n这是 P12 测试用的最小回流内容，仅验证 origin 字段落库。\n", encoding="utf-8")

    resp, status = c.http_post_multipart_file(
        base_url, "/sources", draft_export, extra_fields={"origin": "wiki_draft", "origin_page_id": origin_page_id}
    )
    print(f"  reflow 导入: HTTP {status} {resp}")
    if status not in (200, 201):
        return {"error": f"import failed: {resp}"}
    source_id = resp.get("source_id")

    row = c.poll_until(lambda: c.db_source_by_id(conn, source_id), 15, 0.5)
    ok = bool(row) and row.get("origin") == "wiki_draft" and row.get("origin_page_id") == origin_page_id
    print(f"  DB 核对 sources.origin={row.get('origin') if row else None} origin_page_id={row.get('origin_page_id') if row else None}: {'PASS' if ok else 'FAIL'}")
    return {"source_id": source_id, "origin_tagged_correctly": ok}


def axis1_question_complexity_section(base_url):
    resp, status = c.http_post_json(base_url, "/study/run", {}, timeout=180)
    report = c.http_get_json(base_url, "/study/reports/latest")
    has_section = "question_complexity" in report and "groups" in (report.get("question_complexity") or {})
    print(f"  study report 含 question_complexity 板块: {'PASS' if has_section else 'FAIL'}（groups 数={len(report.get('question_complexity', {}).get('groups', []))}）")
    return {"has_section": has_section, "groups_count": len(report.get("question_complexity", {}).get("groups", []))}


def axis2_relation_and_topic_candidate_observation(conn, base_url):
    kpn_rows = c.db_kp_relations(conn)
    wpr_rows = conn.execute("SELECT COUNT(*) AS n FROM wiki_page_relations").fetchone()["n"]
    print(f"  [观测] knowledge_point_relations 总行数={len(kpn_rows)}, wiki_page_relations 总行数={wpr_rows}")

    topic_candidates = c.http_get_json(base_url, "/study/results?action=topic_page_candidate&status=pending_confirm&limit=50")
    print(f"  [观测] topic_page_candidate pending_confirm 数={len(topic_candidates)}"
          f"（P8 每域仅发布 1 个概念页，< topic_member_min=3，单次运行大概率为 0，属预期观测性缺口）")
    return {
        "kpn_relation_count": len(kpn_rows),
        "wiki_page_relation_count": wpr_rows,
        "topic_page_candidate_pending": len(topic_candidates),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    p8_result = load_latest_p8_result()
    if not p8_result:
        print("找不到 test/v1/results/v1_p8_wiki_*.jsonl，请先跑 test/v1/v1_p8_wiki_test.py", file=sys.stderr)
        sys.exit(1)

    domain_pages = p8_result.get("domain_pages") or {}
    published = {
        label: info for label, info in domain_pages.items()
        if info and not info.get("error") and info.get("publish_status") == 200
    }
    if not published:
        print("P8 结果里没有任何 publish 成功的页面，P12 无法继续（先确保 P8 至少一个域跑通）", file=sys.stderr)
        sys.exit(1)
    print(f"从 P8 结果加载已发布页面: { {k: v['page_id'] for k, v in published.items()} }")

    conn = c.open_db(args.db_path)

    print("\n--- 轴一（确定性，必过）：page_type=topic 必须被一阶编译端点拒绝 ---")
    reject_report = axis1_reject_topic_page_type(args.base_url)

    print("\n--- 轴一：GET /wiki/pages/:id/relations 结构校验 ---")
    relations_report = {}
    for label, info in published.items():
        relations_report[label] = axis1_relations(args.base_url, info["page_id"], label)

    print("\n--- 轴一：写作草稿全生命周期 + 写回防护 ---")
    draft_report = {}
    for label, info in published.items():
        draft_report[label] = axis1_draft_lifecycle(args.base_url, info["page_id"], label)

    print("\n--- 轴一：回流来源标记（origin/origin_page_id 落库） ---")
    first_label, first_info = next(iter(published.items()))
    reflow_report = axis1_reflow_origin_tagging(args.base_url, conn, first_info["page_id"])

    print("\n--- 轴一：study 报告 question_complexity 板块 ---")
    complexity_report = axis1_question_complexity_section(args.base_url)

    print("\n--- 轴二（观测性，不要求达标）：页面关系 / 主题页候选自然形成情况 ---")
    axis2_report = axis2_relation_and_topic_candidate_observation(conn, args.base_url)

    conn.close()

    print("\n========== P12 通过标准核对 ==========")
    axis1_pass = (
        all(r["rejected"] for r in reject_report.values())
        and all(r["structure_ok"] for r in relations_report.values())
        and all(not d.get("error") and d.get("page_unaffected_by_draft_edit") for d in draft_report.values())
        and reflow_report.get("origin_tagged_correctly")
        and complexity_report.get("has_section")
    )
    print(f"轴一（确定性，必过）: {'PASS' if axis1_pass else 'FAIL'}")
    print(f"轴二（观测性，仅记录）: kpn_relations={axis2_report['kpn_relation_count']}, "
          f"wiki_page_relations={axis2_report['wiki_page_relation_count']}, "
          f"topic_page_candidate_pending={axis2_report['topic_page_candidate_pending']}")

    record = {
        "reject_topic_page_type": reject_report,
        "relations": relations_report,
        "drafts": draft_report,
        "reflow": reflow_report,
        "question_complexity": complexity_report,
        "axis2_observation": axis2_report,
        "axis1_pass": axis1_pass,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p12_two_tier")
    print(f"\n详细结果: {jsonl_path}")

    if not axis1_pass:
        sys.exit(1)


if __name__ == "__main__":
    main()
