#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P0：导入与提取验收。

用途：
  1. 前置检查 preset/domains.json 是否覆盖技术域 Domain（database_operations /
     docker_container_operations），否则 Domain 预过滤会把技术问题全部错杀；
  2. 依次上传 test/markdown/ 下 21 份文档，轮询 source_process + unit_extract 完成；
  3. 核对：21 个 source 处理完成（无 failed）；每份文档 KU/KP 数量 > 0；所有 KU/KP
     lifecycle=current；GET /sources 无影子行（shadow_of 非空的行）；
  4. 对 3 篇长文档（K8S部署、达梦数据库优化、SQL Server AlwaysOn 安装配置）各抽 2 个
     KU，做"是否落在未闭合围栏代码块内"的机械判定并打印原文切片——这只是自动化能做的
     部分，方案要求的"命令步骤是否被从中间切开"仍需人工看 snippet_preview 判定；
  5. 核对制度域/技术域关键事实字符串（45天/-5分/256元/6%/25%、2377/containerd/
     MAX_SESSION_STATEMENT/20000/MAX_CONNECTIONS/128/srvctl/chmod u+s）能在某个
     KP.content 里找到。

用法：
  python3 test/v1/v1_p0_ingest_test.py                       # 上传 21 篇 + 全部核对
  python3 test/v1/v1_p0_ingest_test.py --skip-upload          # 假设已上传过，只跑核对
  python3 test/v1/v1_p0_ingest_test.py --base-url http://localhost:8800 --db-path data/wiki-brain.db
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

KEY_FACTS = {
    "制度域": ["45天", "-5分", "256元", "6%", "25%"],
    "技术域": [
        "2377",
        "containerd",
        "MAX_SESSION_STATEMENT",
        "20000",
        "MAX_CONNECTIONS",
        "128",
        "srvctl",
        "chmod u+s",
    ],
}

LONG_DOC_SAMPLES = ["K8S部署", "达梦数据库优化", "SQL Server AlwaysOn 安装配置"]

REQUIRED_TECH_DOMAINS = {"database_operations", "docker_container_operations"}


def check_domain_preset():
    path = c.REPO_ROOT / "preset" / "domains.json"
    data = json.loads(path.read_text(encoding="utf-8"))
    ids = {d["id"] for d in data.get("domains", [])}
    return REQUIRED_TECH_DOMAINS - ids


def upload_all(base_url, files):
    results = []
    for f in files:
        try:
            resp, status = c.http_post_multipart_file(base_url, "/sources", f)
            results.append(
                {
                    "file": f.name,
                    "source_id": resp.get("source_id"),
                    "http_status": status,
                    "title": resp.get("title"),
                }
            )
            print(f"  上传 {f.name} -> source_id={resp.get('source_id')}")
        except Exception as e:
            results.append({"file": f.name, "error": str(e)})
            print(f"  ! 上传失败 {f.name}: {e}")
    return results


def wait_ready(base_url, source_id, timeout_s=600, interval_s=3):
    deadline = time.time() + timeout_s
    while True:
        src = c.http_get_json(base_url, f"/sources/{source_id}")
        if src["status"] == "failed" or src.get("units_status") == "failed":
            return src
        if src["status"] == "completed" and src.get("units_status") == "completed":
            return src
        if time.time() >= deadline:
            return None
        time.sleep(interval_s)


def fence_state_at_line(lines, line_no_1based):
    """粗略判定第 line_no 行之前围栏代码块（```）是否处于未闭合状态。"""
    count = 0
    for line in lines[: line_no_1based - 1]:
        if line.strip().startswith("```"):
            count += 1
    return count % 2 == 1


def spot_check_long_docs(conn, title_to_source_id):
    report = []
    for title in LONG_DOC_SAMPLES:
        source_id = title_to_source_id.get(title)
        if not source_id:
            report.append({"title": title, "error": "source not found（标题未匹配到已导入文档）"})
            continue
        units = c.db_units_for_source(conn, source_id)[:2]
        md_path = c.MARKDOWN_DIR / f"{title}.md"
        if not md_path.exists():
            report.append({"title": title, "error": f"原文件不存在: {md_path}"})
            continue
        lines = md_path.read_text(encoding="utf-8").splitlines()
        for ku in units:
            start, end = ku["line_start"], ku["line_end"]
            snippet = "\n".join(lines[start - 1 : end])
            start_in_fence = fence_state_at_line(lines, start)
            end_in_fence = fence_state_at_line(lines, end + 1)
            report.append(
                {
                    "title": title,
                    "unit_id": ku["unit_id"],
                    "line_start": start,
                    "line_end": end,
                    "start_in_fence": start_in_fence,
                    "end_in_fence": end_in_fence,
                    "suspect_cut": start_in_fence or end_in_fence,
                    "snippet_preview": snippet[:300],
                }
            )
    return report


def check_key_facts(conn):
    rows = conn.execute("SELECT content FROM knowledge_points").fetchall()
    normalized = "\n".join(r["content"] for r in rows).replace(" ", "")
    found = {}
    for domain, facts in KEY_FACTS.items():
        found[domain] = {fact: fact.replace(" ", "") in normalized for fact in facts}
    return found


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument(
        "--skip-upload", action="store_true", help="跳过上传，假设 21 篇文档已导入"
    )
    parser.add_argument("--timeout", type=float, default=600.0, help="单文档处理超时秒数")
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    missing = check_domain_preset()
    if missing:
        print(
            f"! preset/domains.json 缺少技术域 Domain: {missing}，请先补 preset 再继续",
            file=sys.stderr,
        )
        sys.exit(1)
    print("preset/domains.json 技术域覆盖检查通过。\n")

    files = c.list_markdown_files()
    print(f"test/markdown/ 下共 {len(files)} 份文档。")
    if len(files) != 21:
        print(
            f"! 警告：预期 21 份文档，实际 {len(files)} 份，请核对 test/markdown/ 目录",
            file=sys.stderr,
        )

    upload_results = []
    if not args.skip_upload:
        print("\n开始上传...")
        upload_results = upload_all(args.base_url, files)
    else:
        print("\n--skip-upload：跳过上传，直接核对已有 source。")

    title_to_source_id = {v: k for k, v in c.fetch_source_titles(args.base_url).items()}

    print("\n轮询处理进度（这一步可能需要几分钟到几十分钟，取决于真实 LLM 延迟）...")
    ready_report = []
    for f in files:
        title = f.stem
        source_id = title_to_source_id.get(title)
        if not source_id:
            ready_report.append({"title": title, "error": "未找到对应 source（上传是否失败？）"})
            print(f"  {title}: ! 未找到对应 source")
            continue
        src = wait_ready(args.base_url, source_id, timeout_s=args.timeout)
        if src is None:
            entry = {"title": title, "source_id": source_id, "error": "超时未完成"}
        else:
            entry = {
                "title": title,
                "source_id": source_id,
                "status": src["status"],
                "units_status": src.get("units_status"),
                "error_msg": src.get("error_msg"),
            }
        ready_report.append(entry)
        print(
            f"  {title}: status={entry.get('status')} units_status={entry.get('units_status')} "
            f"error={entry.get('error_msg') or entry.get('error') or '-'}"
        )

    conn = c.open_db(args.db_path)

    print("\n核对 KU/KP 数量与 lifecycle...")
    ku_kp_report = []
    for f in files:
        title = f.stem
        source_id = title_to_source_id.get(title)
        if not source_id:
            continue
        units = c.db_units_for_source(conn, source_id)
        points = c.db_points_for_source(conn, source_id)
        ku_kp_report.append(
            {
                "title": title,
                "source_id": source_id,
                "ku_count": len(units),
                "kp_count": len(points),
                "non_current_ku": sum(1 for u in units if u["lifecycle"] != "current"),
                "non_current_kp": sum(1 for p in points if p["lifecycle"] != "current"),
            }
        )

    shadows = c.db_shadow_sources(conn)

    print("核对长文档切片（K8S部署/达梦数据库优化/AlwaysOn）...")
    slice_report = spot_check_long_docs(conn, title_to_source_id)
    for r in slice_report:
        if r.get("suspect_cut"):
            print(f"  ! 疑似截断: {r['title']} unit={r['unit_id']} L{r['line_start']}-{r['line_end']}")
            print(f"    片段预览: {r['snippet_preview'][:120]!r}")

    print("核对关键事实字符串...")
    facts_report = check_key_facts(conn)

    zero_ku = [r for r in ku_kp_report if r["ku_count"] == 0]
    zero_kp = [r for r in ku_kp_report if r["kp_count"] == 0]
    bad_lifecycle = [r for r in ku_kp_report if r["non_current_ku"] or r["non_current_kp"]]
    failed_sources = [
        r
        for r in ready_report
        if r.get("status") == "failed" or r.get("units_status") == "failed" or r.get("error")
    ]

    print("\n========== P0 通过标准核对 ==========")
    print(
        f"21 份文档全部处理完成: "
        f"{'PASS' if not failed_sources and len(ready_report) == len(files) else 'FAIL'}"
    )
    for r in failed_sources:
        print(f"  ! {r}")
    print(f"KU 数量 > 0（全部文档）: {'PASS' if not zero_ku else 'FAIL'} {[r['title'] for r in zero_ku]}")
    print(f"KP 数量 > 0（全部文档）: {'PASS' if not zero_kp else 'FAIL'} {[r['title'] for r in zero_kp]}")
    print(
        f"KU/KP lifecycle=current: {'PASS' if not bad_lifecycle else 'FAIL'} "
        f"{[r['title'] for r in bad_lifecycle]}"
    )
    print(f"GET /sources 无影子行: {'PASS' if not shadows else 'FAIL'} {[s['source_id'] for s in shadows]}")
    for dom, facts in facts_report.items():
        missing_facts = [k for k, v in facts.items() if not v]
        print(f"{dom} 关键事实全部命中: {'PASS' if not missing_facts else 'FAIL'} (缺失: {missing_facts})")
    print(
        "长文档切片：脚本只能机械判定'是否落在未闭合代码块内'，"
        "'切割是否合理'请人工核对上面打印的 snippet_preview。"
    )

    record = {
        "upload_results": upload_results,
        "ready_report": ready_report,
        "ku_kp_report": ku_kp_report,
        "shadow_sources": shadows,
        "slice_report": slice_report,
        "facts_report": facts_report,
    }
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p0_ingest")
    print(f"\n详细结果: {jsonl_path}")
    conn.close()


if __name__ == "__main__":
    main()
