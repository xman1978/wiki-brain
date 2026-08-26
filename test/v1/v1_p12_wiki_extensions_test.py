#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P12：Wiki 页面关系 /
写作草稿 / 回流防护（单层架构下 P8 编译产物之上的扩展能力）。

**2026-08-19 全文重写**：`docs/impl/v1/wiki.md` 已于 2026-08-18 整体改判为
单层架构（`docs/design/wiki-single-tier-revision.md`），"两层架构"（一阶
概念/事实页 + 二阶主题页）本身已不存在，本脚本原标题"两层架构扩展"随之
失效。本次重写：

  - **整体删除**：`page_type=topic` 拒绝校验（一阶端点不再有 page_type
    区分，`page_type` 恒为 `topic`，请求体里传这个字段也不会被特殊处理）；
    `topic_page_candidate` 观测（Study 主题聚类、`POST /wiki/topics` 均已
    随两层架构删除，没有对应产物可观测）。
  - **保留、按新契约调整**：`GET /wiki/pages/:id/relations`
    （`related`/`contradicts` 两种，`contains` 已删除，机制不变——单层化后
    对所有已发布页面一视同仁派生，不再有"跳过主题页"的 early return）；
    写作草稿全生命周期（`source_page_ids` 单层化后恒为 `[page_id]`，"组装
    模式"随两层架构一起删除，`mode` 请求字段仍被接受但只有 page 语义有
    意义）；回流来源标记（`sources.origin`/`origin_page_id`，机制不变）；
    Study 报告 `question_complexity` 板块（与 Wiki 架构无关，机制不变）。

前提：依赖 P8 已经跑过并各产出一个 published 页面（制度域「销售回款管理」、
技术域「Oracle RAC」）——本阶段从 test/v1/results/ 里读取最近一次
v1_p8_wiki_*.jsonl 拿 page_id，不重新培养信号。若找不到，提示先跑 P8，
非致命错误直接退出。

轴一（确定性，必过）：
  - `GET /wiki/pages/:id/relations` 结构正确（可为空——P8 的制度/技术两页
    分属不同领域，天然不会有 KPN 关系，为空是预期，不是失败）；
  - 写作草稿全生命周期：POST drafts → GET（evidence_index 非空）→
    PATCH 内容 → 页面正文同步更新（2026-08-19 定案：PATCH 即写回，见
    CLAUDE.md）→ DELETE；
  - 回流来源标记：POST /sources 带 origin=wiki_draft + origin_page_id 时，
    DB 里 sources.origin/origin_page_id 正确落库；
  - GET /study/reports/latest 响应包含 question_complexity 板块。

轴二（观测性，不要求达标）：
  - knowledge_point_relations / wiki_page_relations 行数，仅记录不判定
    （P7 阶段的跨 Source fixture 可能已产生可供派生的关系）。

用法：
  python3 test/v1/v1_p12_wiki_extensions_test.py
"""
import argparse
import glob
import json
import sys
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


def axis1_relations(base_url, page_id, label):
    resp = c.http_get_json(base_url, f"/wiki/pages/{page_id}/relations")
    ok = isinstance(resp, list)
    if ok:
        for row in resp:
            for key in ("relation_type", "other_page_id", "derived_from"):
                if key not in row:
                    ok = False
            if row.get("relation_type") not in ("related", "contradicts"):
                ok = False
    print(f"  {label} page_id={page_id} relations: {len(resp) if isinstance(resp, list) else 'N/A'} 行, 结构校验={'PASS' if ok else 'FAIL'}")
    return {"count": len(resp) if isinstance(resp, list) else None, "structure_ok": ok, "rows": resp}


def axis1_draft_lifecycle(base_url, page_id, label):
    """草稿全生命周期（docs/impl/v1/wiki.md「写作草稿」）。
    source_page_ids 单层化后恒为 [page_id]，本脚本不再核对"组装模式"（已
    随两层架构删除）。2026-08-19 定案：PATCH 草稿只要 title/content 有实际
    改动就会同步写回来源页面（internal/wiki/drafts.go UpdateDraft ->
    SyncDraftToPage），不是"草稿隔离、不写回"——本脚本据此断言页面正文/
    标题在草稿改写后应当被同步更新，而不是保持不变。"""
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
    source_page_ids = draft.get("source_page_ids")
    print(f"  {label} draft_id={draft_id} evidence_index 非空={has_evidence} stale={draft.get('stale')} source_page_ids={source_page_ids}")

    new_content = content_before + "\n\n（人工改写追加内容，仅存在于草稿）"
    new_title = title_before + "（草稿改写）"
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
    synced = page_after.get("content") == new_content and page_after.get("title") == new_title
    print(f"  {label} 页面正文/标题在草稿改写后是否同步写回: {'PASS' if synced else 'FAIL（未写回！）'}")

    del_resp, del_status = c.http_delete_json(base_url, f"/wiki/drafts/{draft_id}")
    print(f"  {label} DELETE 草稿: HTTP {del_status}")

    return {
        "draft_id": draft_id,
        "evidence_index_present": has_evidence,
        "source_page_ids": source_page_ids,
        "source_page_ids_ok": source_page_ids == [page_id],
        "patch_status": patch_status,
        "page_synced_from_draft_edit": synced,
        "delete_status": del_status,
    }


def axis1_reflow_origin_tagging(base_url, conn, origin_page_id):
    """回流来源标记（docs/impl/v1/wiki.md「写作草稿」防自指部分）：验证
    POST /sources 的 origin/origin_page_id 字段被正确透传落库——自体祖先
    排除本身的行为由 Go 单测覆盖（internal/unit/kpn_reflow_test.go），
    这里只测 API 入口。"""
    import time

    SCRATCH_DIR.mkdir(parents=True, exist_ok=True)
    draft_export = SCRATCH_DIR / f"reflow_draft_export_{int(time.time())}.md"
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


def axis2_relation_observation(conn):
    kpn_rows = c.db_kp_relations(conn)
    wpr_rows = conn.execute("SELECT COUNT(*) AS n FROM wiki_page_relations").fetchone()["n"]
    print(f"  [观测] knowledge_point_relations 总行数={len(kpn_rows)}, wiki_page_relations 总行数={wpr_rows}")
    return {
        "kpn_relation_count": len(kpn_rows),
        "wiki_page_relation_count": wpr_rows,
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

    print("\n--- 轴二（观测性，不要求达标）：页面关系自然形成情况 ---")
    axis2_report = axis2_relation_observation(conn)

    conn.close()

    print("\n========== P12 通过标准核对 ==========")
    axis1_pass = (
        all(r["structure_ok"] for r in relations_report.values())
        and all(not d.get("error") and d.get("page_synced_from_draft_edit") and d.get("source_page_ids_ok") for d in draft_report.values())
        and reflow_report.get("origin_tagged_correctly")
        and complexity_report.get("has_section")
    )
    print(f"轴一（确定性，必过）: {'PASS' if axis1_pass else 'FAIL'}")
    print(f"轴二（观测性，仅记录）: kpn_relations={axis2_report['kpn_relation_count']}, "
          f"wiki_page_relations={axis2_report['wiki_page_relation_count']}")

    record = {
        "relations": relations_report,
        "drafts": draft_report,
        "reflow": reflow_report,
        "question_complexity": complexity_report,
        "axis2_observation": axis2_report,
        "axis1_pass": axis1_pass,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p12_wiki_extensions")
    print(f"\n详细结果: {jsonl_path}")

    if not axis1_pass:
        sys.exit(1)


if __name__ == "__main__":
    main()
