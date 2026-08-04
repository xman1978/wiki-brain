#!/usr/bin/env python3
"""
仅清理 P8（Wiki 初版闭环）测试留下的状态，不清库、不删 P0–P7 的 traces / 共现 / 链接。

清理范围：
  1. 归档 P8 两域相关 entry 下的 Wiki 概念/事实页（draft/published/needs_recompile）；
  2. 删除 wiki_candidate / recompile_flag 类 learning_results（待确认项）；
  3. 打印仍存在的影子 Source（P8 reupload 未完成时），不自动删库。

用法：
  python3 test/v1/v1_p8_reset.py --base-url http://127.0.0.1:8800
  python3 test/v1/v1_p8_reset.py --dry-run
"""
import argparse
import glob
import json
import sys
from pathlib import Path

import v1_common as c

# 与 v1_p8_wiki_test.py 一致
POLICY_TOPIC = {
    "base_ids": ["A9", "A10", "A11"],
}
TECH_TOPIC = {
    "base_ids": ["T10", "T11", "T18", "B4"],
}
P8_ACTIONS = ("wiki_candidate", "recompile_flag")


def entry_ids_from_latest_jsonl():
    files = sorted(glob.glob(str(c.RESULTS_DIR / "v1_p8_wiki_*.jsonl")))
    if not files:
        return set(), []
    data = json.loads(Path(files[-1]).read_text(encoding="utf-8").strip().splitlines()[-1])
    # 新字段 policy_entries/tech_entries；兼容旧落盘 policy_concepts/tech_concepts
    eids = (
        set(data.get("policy_entries") or data.get("policy_concepts") or [])
        | set(data.get("tech_entries") or data.get("tech_concepts") or [])
    )
    page_ids = []
    for info in (data.get("domain_pages") or {}).values():
        if info.get("page_id"):
            page_ids.append(info["page_id"])
        # 兼容旧落盘里的 concept_id
        for key in ("entry_id", "concept_id"):
            if info.get(key):
                eids.add(info[key])
    return eids, page_ids


def resolve_p8_entries(conn):
    """从最近一次 P8 落盘 + 题库 ID 命中的 KP 推导 entry_id。"""
    from v1_p8_wiki_test import POLICY_TOPIC as PT, TECH_TOPIC as TT, build_bank, load_extra_phrasings

    extra = load_extra_phrasings(None)
    full_text = c.load_plan_text()
    entries = set()
    for topic in (PT, TT):
        bank = build_bank(topic, full_text, extra)
        for item in bank.values():
            for pid in item.get("point_ids") or []:
                row = conn.execute(
                    "SELECT ku.entry_id FROM knowledge_points kp "
                    "JOIN knowledge_units ku ON kp.unit_id = ku.unit_id WHERE kp.point_id = ?",
                    (pid,),
                ).fetchone()
                if row and row["entry_id"]:
                    entries.add(row["entry_id"])
    from_jsonl, _ = entry_ids_from_latest_jsonl()
    entries |= from_jsonl
    return entries


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    if not args.dry_run and not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    conn = c.open_db(args.db_path)
    n_sources = conn.execute("SELECT COUNT(*) FROM sources WHERE shadow_of IS NULL").fetchone()[0]
    if n_sources == 0:
        print("! 当前库内没有已导入的 Source（sources=0）。P8 依赖 P0–P7 验收数据，请先恢复备份或跑 P0，再执行 P8。")

    entry_ids = resolve_p8_entries(conn)
    _, page_ids_jsonl = entry_ids_from_latest_jsonl()
    print(f"P8 相关 entry_id 数: {len(entry_ids)}")
    if entry_ids:
        print("  ", ", ".join(sorted(entry_ids)[:12]) + ("..." if len(entry_ids) > 12 else ""))

    pages = []
    if entry_ids:
        placeholders = ",".join("?" * len(entry_ids))
        pages = conn.execute(
            f"SELECT page_id, entry_id, page_type, status, title FROM wiki_pages "
            f"WHERE entry_id IN ({placeholders}) AND status != 'archived'",
            list(entry_ids),
        ).fetchall()
    for pid in page_ids_jsonl:
        if not any(p["page_id"] == pid for p in pages):
            row = conn.execute(
                "SELECT page_id, entry_id, page_type, status, title FROM wiki_pages WHERE page_id = ? AND status != 'archived'",
                (pid,),
            ).fetchone()
            if row:
                pages.append(row)

    print(f"待归档 Wiki 概念/事实页: {len(pages)}")
    for p in pages:
        print(f"  - {p['page_id'][:8]}… {p['page_type']} {p['status']} {p['title'][:40]}")

    lr_rows = []
    if entry_ids:
        placeholders = ",".join("?" * len(entry_ids))
        lr_rows = conn.execute(
            f"SELECT result_id, action, object_id, status FROM learning_results "
            f"WHERE action IN ('wiki_candidate', 'recompile_flag') AND object_id IN ({placeholders})",
            list(entry_ids),
        ).fetchall()
    page_ids = [p["page_id"] for p in pages]
    if page_ids:
        ph = ",".join("?" * len(page_ids))
        lr_rows += conn.execute(
            f"SELECT result_id, action, object_id, status FROM learning_results "
            f"WHERE action IN ('wiki_candidate', 'recompile_flag') AND object_id IN ({ph})",
            page_ids,
        ).fetchall()
    print(f"待删除 learning_results: {len(lr_rows)}")

    shadows = conn.execute(
        "SELECT source_id, title, status, shadow_of FROM sources WHERE shadow_of IS NOT NULL"
    ).fetchall()
    if shadows:
        print(f"影子 Source（请人工确认是否 retry/丢弃）: {len(shadows)}")
        for s in shadows:
            print(f"  - {s['source_id'][:8]}… shadow_of={s['shadow_of'][:8]}… status={s['status']} {s['title'][:50]}")

    if args.dry_run:
        print("\n[dry-run] 未修改数据库。")
        conn.close()
        return

    for p in pages:
        try:
            c.http_post_json(args.base_url, f"/wiki/pages/{p['page_id']}/archive", {})
            print(f"  已归档页面 {p['page_id']}")
        except Exception as e:
            print(f"  ! 归档失败 {p['page_id']}: {e}")

    for row in lr_rows:
        conn.execute("DELETE FROM learning_results WHERE result_id = ?", (row["result_id"],))
    if lr_rows:
        conn.commit()
        print(f"  已删除 {len(lr_rows)} 条 learning_results")

    conn.close()
    print("\nP8 状态已清理（未动 traces / cooccurrence / activation_links）。可重新运行 v1_p8_wiki_test.py。")


if __name__ == "__main__":
    main()
