#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P7：跨 Source KPN（两域）。

流程：
  1. POST /sources/:id/kpn-cross：制度域对《项目考核与激励制度》触发，技术域对
     《Oracle 19c RAC 集群安装部署维护环境》和《达梦数据库优化》各触发一次；
  2. 核对 knowledge_point_relations 出现 scope=cross 的行；关系类型只应是
     related/contradicts，direction 恒为 bidirectional；重复触发验证幂等（关系数不增）；
  3. contradicts fixture（两域各一）：
     - 制度域：把《培训积分管理办法》"旷课-5"改"旷课-10"的修改版，改名后作为**新
       Source** 导入（与原版并存，不是 reupload）；
     - 技术域：《达梦数据库优化》MAX_SESSION_STATEMENT 建议值 "20000"->"5000"，
       同样改名作为新 Source 导入；
     两者各自触发 kpn-cross 后，核对原版与衍生版之间出现 contradicts 关系；
  4. 重问 B1、B2、B4、B5：核对 KPN 扩展是否把跨 Source 邻居带进 supporting 证据
     （Evidence.origin == "kpn_expansion"）；
  5. 抽查最多 10 条 cross 关系打印双方 KP 内容，供人工判定合理率（方案要求 ≥80%，
     这是人工判断，脚本只负责把内容摆出来，不代为下结论）。

用法：
  python3 test/v1/v1_p7_kpn_test.py
  python3 test/v1/v1_p7_kpn_test.py --skip-contradicts-fixture   # 只测 related 部分
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

SCRATCH_DIR = Path("/private/tmp/claude-501/-Users-jxu-Code-wiki-brain/fa63b57a-4195-46fb-855c-de7ab5a9d99b/scratchpad")

RELATED_TRIGGER_TITLES = ["项目考核与激励制度", "Oracle 19c RAC 集群安装部署维护", "达梦数据库优化"]

CONTRADICTS_FIXTURES = [
    {
        "base_title": "培训积分管理办法",
        "old_line": "| 旷课（不参加未请假者） | 每旷课1次 | -5 |  |",
        "new_line": "| 旷课（不参加未请假者） | 每旷课1次 | -10 |  |",
        "new_title": "培训积分管理办法（新版-矛盾测试）",
    },
    {
        "base_title": "达梦数据库优化",
        "old_line": "| MAX_SESSION_STATEMENT | 20000 | 每个会话打开的最大句柄数 |",
        "new_line": "| MAX_SESSION_STATEMENT | 5000 | 每个会话打开的最大句柄数 |",
        "new_title": "达梦数据库优化（新版-矛盾测试）",
    },
]

B_GROUP_IDS = ["B1", "B2", "B4", "B5"]


def resolve_source_id(base_url, title):
    id_to_title = c.fetch_source_titles(base_url)
    for sid, t in id_to_title.items():
        if t == title:
            return sid
    return None


def trigger_kpn_cross(base_url, source_id):
    resp, status = c.http_post_json(base_url, f"/sources/{source_id}/kpn-cross", {}, timeout=180)
    return resp, status


def build_fixture_file(fixture):
    src_path = c.MARKDOWN_DIR / f"{fixture['base_title']}.md"
    text = src_path.read_text(encoding="utf-8")
    if fixture["old_line"] not in text:
        raise RuntimeError(f"「{fixture['base_title']}」原文里找不到预期行: {fixture['old_line']!r}")
    modified = text.replace(fixture["old_line"], fixture["new_line"], 1)
    out_path = SCRATCH_DIR / f"{fixture['new_title']}.md"
    out_path.write_text(modified, encoding="utf-8")
    return out_path


def wait_source_ready(base_url, source_id, timeout_s=600):
    """用 poll_with_backoff 而非固定 3s 间隔——source 处理通常几秒内结束，指数
    退避能更快发现完成，长时间未完成时也不会一直高频轮询。"""

    def check():
        src = c.http_get_json(base_url, f"/sources/{source_id}")
        if src["status"] == "failed" or src.get("units_status") == "failed":
            return src
        if src["status"] == "completed" and src.get("units_status") == "completed":
            return src
        return None

    return c.poll_with_backoff(check, timeout_s)


def summarize_relations(conn):
    rows = c.db_kp_relations(conn, scope="cross")
    by_type = {}
    for r in rows:
        by_type.setdefault(r["relation_type"], 0)
        by_type[r["relation_type"]] += 1
    bad_direction = [r for r in rows if r["direction"] != "bidirectional"]
    bad_type = [r for r in rows if r["relation_type"] not in ("related", "contradicts")]
    return rows, by_type, bad_direction, bad_type


def print_relation_samples(conn, rows, n=10):
    print(f"\n抽查 {min(n, len(rows))} 条 cross 关系（人工判定合理率，方案目标 ≥80%）：")
    for r in rows[:n]:
        src_kp = conn.execute("SELECT content, source_id FROM knowledge_points WHERE point_id=?", (r["source_point_id"],)).fetchone()
        tgt_kp = conn.execute("SELECT content, source_id FROM knowledge_points WHERE point_id=?", (r["target_point_id"],)).fetchone()
        print(f"  [{r['relation_type']}] {r['relation_id'][:8]}")
        print(f"    A: {(src_kp['content'] if src_kp else '?')[:80]}")
        print(f"    B: {(tgt_kp['content'] if tgt_kp else '?')[:80]}")


def ask_b_group(base_url, conn, full_text, timeout, delay):
    rows = {c.row_id(r): r for r in c.load_group("B", full_text)}
    report = {}
    for bid in B_GROUP_IDS:
        row = rows.get(bid)
        if not row:
            continue
        question = c.question_variants(row)[0]
        print(f"\n{bid}: {question}")
        _turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
        if not result:
            report[bid] = {"error": "no result"}
            time.sleep(delay)
            continue
        es = result.get("evidence_snapshot") or {}
        supporting = es.get("supporting") or []
        kpn_expansion = [ev for ev in supporting if ev.get("origin") == "kpn_expansion"]
        print(f"  supporting 总数={len(supporting)}，来自 kpn_expansion={len(kpn_expansion)}")
        report[bid] = {
            "question": question,
            "content": result.get("content"),
            "supporting_count": len(supporting),
            "kpn_expansion_count": len(kpn_expansion),
        }
        time.sleep(delay)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--skip-contradicts-fixture", action="store_true")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=0.5)
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    conn = c.open_db(args.db_path)
    full_text = c.load_plan_text()

    print("--- 1. 触发 related 类 kpn-cross ---")
    trigger_results = {}
    for title in RELATED_TRIGGER_TITLES:
        sid = resolve_source_id(args.base_url, title)
        if not sid:
            print(f"  ! 找不到「{title}」", file=sys.stderr)
            continue
        resp, status = trigger_kpn_cross(args.base_url, sid)
        print(f"  {title} ({sid}): HTTP {status} {resp}")
        trigger_results[title] = resp

    rows_after_1, by_type_1, bad_dir_1, bad_type_1 = summarize_relations(conn)
    print(f"\n触发后 cross 关系总数: {len(rows_after_1)}，按类型: {by_type_1}")

    print("\n--- 2. 幂等性核对：重复触发 ---")
    for title in RELATED_TRIGGER_TITLES:
        sid = resolve_source_id(args.base_url, title)
        if sid:
            trigger_kpn_cross(args.base_url, sid)
    rows_after_2, by_type_2, _, _ = summarize_relations(conn)
    print(f"重复触发后 cross 关系总数: {len(rows_after_2)}（应与上面相同）: "
          f"{'PASS' if len(rows_after_2) == len(rows_after_1) else 'FAIL'}")

    contradicts_report = {}
    if not args.skip_contradicts_fixture:
        print("\n--- 3. contradicts fixture ---")
        SCRATCH_DIR.mkdir(parents=True, exist_ok=True)
        for fixture in CONTRADICTS_FIXTURES:
            try:
                path = build_fixture_file(fixture)
                resp, status = c.http_post_multipart_file(args.base_url, "/sources", path)
                print(f"  上传 {fixture['new_title']}: HTTP {status} {resp}")
                new_sid = resp.get("source_id")
                src = wait_source_ready(args.base_url, new_sid, timeout_s=300)
                if not src or src.get("units_status") != "completed":
                    contradicts_report[fixture["new_title"]] = {"error": f"处理未完成: {src}"}
                    continue
                cross_resp, cross_status = trigger_kpn_cross(args.base_url, new_sid)
                print(f"  {fixture['new_title']} kpn-cross: HTTP {cross_status} {cross_resp}")
                contradicts_report[fixture["new_title"]] = {"source_id": new_sid, "kpn_cross_result": cross_resp}
            except Exception as e:
                print(f"  ! {fixture['new_title']} 处理失败: {e}", file=sys.stderr)
                contradicts_report[fixture["new_title"]] = {"error": str(e)}

        rows_final, by_type_final, bad_dir_final, bad_type_final = summarize_relations(conn)
        contradicts_rows = [r for r in rows_final if r["relation_type"] == "contradicts"]
        print(f"\ncontradicts 关系总数: {len(contradicts_rows)}（目标 ≥2，两域各至少一组）")
    else:
        rows_final, by_type_final, bad_dir_final, bad_type_final = rows_after_2, by_type_2, [], []
        print("\n--skip-contradicts-fixture：跳过矛盾 fixture。")

    print_relation_samples(conn, rows_final, n=10)

    print("\n--- 4. B 组重问，核对 kpn_expansion supporting 证据 ---")
    b_report = ask_b_group(args.base_url, conn, full_text, args.timeout, args.delay)

    print("\n========== P7 通过标准核对 ==========")
    print(f"关系类型只有 related/contradicts: {'PASS' if not bad_type_final else 'FAIL'} (异常: {[r['relation_type'] for r in bad_type_final]})")
    print(f"direction 恒为 bidirectional: {'PASS' if not bad_dir_final else 'FAIL'} (异常数: {len(bad_dir_final)})")
    total_kpn_expansion = sum(r.get("kpn_expansion_count", 0) for r in b_report.values())
    print(f"B 组 supporting 里出现 kpn_expansion 来源证据: {total_kpn_expansion} 条（>0 说明跨 Source 扩展生效）")
    print("跨关系合理性抽查：见上方打印，需人工按方案标准判定合理率 ≥80%")

    conn.close()
    record = {
        "trigger_results": trigger_results,
        "relations_after_first_trigger": len(rows_after_1),
        "relations_after_repeat_trigger": len(rows_after_2),
        "by_type": by_type_final,
        "contradicts_report": contradicts_report,
        "b_group_report": b_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p7_kpn")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
