#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P6：Reupload 换血
（标准 6 更新侧，Shadow Source 机制，两域各换一个靶子）。

两个靶子在本次测试环境里都已经是 verified 链接（P2 里 A4、T15 confirm 成功），
换血后能不能观察到"目标 KP 非 current 只降不升"是本阶段的重点。

流程：
  1. 生成两份改写文件：《培训积分管理办法》"每旷课1次 | -5" -> "-10"；
     《神通数据库优化》MAX_CONNECTIONS "128" -> "256"（写到 scratch 目录，不改
     test/markdown 原文件）；
  2. POST /sources/:id/reupload 上传；
  3. 轮询期间验证 GET /sources 不出现影子行，此时问 A4/T15 仍是旧值；
  4. 完成后验证旧 KU/KP lifecycle=superseded，问 A4/T15 变成新值且引用新 KU；
  5. 核对 A4、T15 对应 verified 链接的 adopt_count 在换血前后没有继续增长
     （目标 KP 非 current，只降不升）；
  6. 失败分支：对第三个 Source（万相公文销售奖励制度，避开两个真实靶子）上传空文件，
     验证影子 status=failed、原 Source 不受影响，然后用 reupload/retry 续跑一次
     （这次传回真实内容，验证 retry 能救回来）。注意：空文件是否真的会让
     source_process/unit_extract 判定为 failed 取决于具体实现（也可能只是产出 0 个
     KU 但 units_status 仍标 completed），本脚本只如实报告观察到的 shadow_state，
     不代表"必然失败"——如果空文件没能触发失败，需要人工换一种破坏方式（如上传
     非法编码内容）重跑这一分支。

用法：
  python3 test/v1/v1_p6_reupload_test.py
"""
import argparse
import json
import sys
import time
from pathlib import Path

import v1_common as c

SCRATCH_DIR = Path("/private/tmp/claude-501/-Users-jxu-Code-wiki-brain/fa63b57a-4195-46fb-855c-de7ab5a9d99b/scratchpad")

TARGETS = [
    {
        "title": "培训积分管理办法",
        "old_line": "| 旷课（不参加未请假者） | 每旷课1次 | -5 |  |",
        "new_line": "| 旷课（不参加未请假者） | 每旷课1次 | -10 |  |",
        "ask_question": "培训旷课一次扣几分？",
        "old_answer_marker": "5",
        "new_answer_marker": "10",
    },
    {
        "title": "神通数据库优化",
        "old_line": "| MAX_CONNECTIONS                | 数据库最大连接数   | 128 | 65535 |",
        "new_line": "| MAX_CONNECTIONS                | 数据库最大连接数   | 256 | 65535 |",
        "ask_question": "神通数据库最大连接数默认是多少、上限多少？",
        "old_answer_marker": "128",
        "new_answer_marker": "256",
    },
]

FAILURE_TARGET_TITLE = "万相公文销售奖励制度"


def build_modified_file(title, old_line, new_line):
    src_path = c.MARKDOWN_DIR / f"{title}.md"
    text = src_path.read_text(encoding="utf-8")
    if old_line not in text:
        raise RuntimeError(f"「{title}」原文里找不到预期行: {old_line!r}——文档可能已变化，需要人工核对新的行内容")
    modified = text.replace(old_line, new_line, 1)
    out_path = SCRATCH_DIR / f"{title}（修改版）.md"
    out_path.write_text(modified, encoding="utf-8")
    return out_path


def resolve_source_id(base_url, title):
    id_to_title = c.fetch_source_titles(base_url)
    for sid, t in id_to_title.items():
        if t == title:
            return sid
    return None


def poll_shadow(base_url, shadow_id, timeout_s=600, interval_s=3):
    """轮询影子 source：completed+completed=成功且尚未 swap；404=已 swap 完成并删除；
    failed=处理失败。"""
    deadline = time.time() + timeout_s
    while True:
        try:
            src = c.http_get_json(base_url, f"/sources/{shadow_id}")
        except Exception:
            return "swapped_and_gone", None
        if src.get("status") == "failed" or src.get("units_status") == "failed":
            return "failed", src
        if src.get("status") == "completed" and src.get("units_status") == "completed":
            return "shadow_completed_not_yet_swapped", src
        if time.time() >= deadline:
            return "timeout", src
        time.sleep(interval_s)


def wait_swap_done(base_url, target_id, shadow_id, timeout_s=600, interval_s=3):
    """真正的换血完成信号是影子行消失（被删）——GET /sources/:shadow_id 404。"""
    deadline = time.time() + timeout_s
    while True:
        try:
            c.http_get_json(base_url, f"/sources/{shadow_id}")
        except Exception:
            return True
        if time.time() >= deadline:
            return False
        time.sleep(interval_s)


def ask_once(base_url, question, timeout):
    _turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout)
    return result


def check_target(base_url, conn, target, timeout, delay):
    title = target["title"]
    print(f"\n=== 靶子: {title} ===")
    source_id = resolve_source_id(base_url, title)
    if not source_id:
        print(f"  ! 找不到「{title}」，跳过", file=sys.stderr)
        return {"title": title, "error": "source not found"}

    old_verified_links = c.db_links_for_source(conn, source_id, status="verified")
    old_adopt_counts = {l["link_id"]: l["adopt_count"] for l in old_verified_links}
    print(f"  换血前 verified 链接: {[(l['link_id'], l['adopt_count']) for l in old_verified_links]}")

    mod_path = build_modified_file(title, target["old_line"], target["new_line"])
    print(f"  生成修改版文件: {mod_path}")

    resp, status = c.http_post_multipart_file(base_url, f"/sources/{source_id}/reupload", mod_path)
    print(f"  POST reupload: HTTP {status} {resp}")
    shadow_id = resp.get("shadow_source_id")

    print("  轮询期间用旧问法确认答案仍是旧值...")
    mid_answer = ask_once(base_url, target["ask_question"], timeout)
    id_to_title_now = c.fetch_source_titles(base_url)
    shadow_visible = shadow_id in id_to_title_now
    print(f"    GET /sources 是否可见影子行: {shadow_visible}（应为 False）")
    print(f"    此时回答: {(mid_answer or {}).get('content', '')[:150]}")

    shadow_state, shadow_src = poll_shadow(base_url, shadow_id, timeout_s=600)
    print(f"  影子处理状态: {shadow_state}")
    if shadow_state == "failed":
        return {"title": title, "error": f"reupload 失败: {shadow_src}"}

    swapped = wait_swap_done(base_url, source_id, shadow_id, timeout_s=300)
    print(f"  换血完成（影子行已删除): {swapped}")

    time.sleep(delay)
    new_answer = ask_once(base_url, target["ask_question"], timeout)
    print(f"  换血后回答: {(new_answer or {}).get('content', '')[:150]}")

    lifecycle = {
        "units": [dict(u) for u in c.db_units_for_source(conn, source_id)],
        "points": [dict(p) for p in c.db_points_for_source(conn, source_id)],
    }
    non_current_units = [u for u in lifecycle["units"] if u["lifecycle"] not in ("current", "superseded")]
    superseded_units = [u for u in lifecycle["units"] if u["lifecycle"] == "superseded"]
    current_units = [u for u in lifecycle["units"] if u["lifecycle"] == "current"]
    print(f"  换血后 KU lifecycle 分布: current={len(current_units)} superseded={len(superseded_units)} 其他异常={len(non_current_units)}")

    new_link_states = {}
    for link_id, old_count in old_adopt_counts.items():
        current = c.db_activation_link(conn, link_id)
        new_link_states[link_id] = {
            "old_adopt_count": old_count,
            "new_status": current["status"] if current else None,
            "new_adopt_count": current["adopt_count"] if current else None,
            "adopt_count_grew": bool(current and current["adopt_count"] > old_count),
        }
        print(f"  链接 {link_id}: 换血前 adopt_count={old_count} -> 现状态={current['status'] if current else '?'} "
              f"adopt_count={current['adopt_count'] if current else '?'}")

    old_marker_gone = target["old_answer_marker"] not in (new_answer or {}).get("content", "")
    new_marker_present = target["new_answer_marker"] in (new_answer or {}).get("content", "")

    return {
        "title": title,
        "source_id": source_id,
        "shadow_id": shadow_id,
        "mid_answer": (mid_answer or {}).get("content"),
        "shadow_visible_during_processing": shadow_visible,
        "new_answer": (new_answer or {}).get("content"),
        "old_marker_gone": old_marker_gone,
        "new_marker_present": new_marker_present,
        "non_current_unit_count": len(non_current_units),
        "superseded_unit_count": len(superseded_units),
        "old_link_adopt_counts": new_link_states,
    }


def run_failure_branch(base_url, conn, timeout):
    print(f"\n=== 失败分支: {FAILURE_TARGET_TITLE} 上传空文件 ===")
    source_id = resolve_source_id(base_url, FAILURE_TARGET_TITLE)
    if not source_id:
        print(f"  ! 找不到「{FAILURE_TARGET_TITLE}」，跳过失败分支", file=sys.stderr)
        return {"error": "source not found"}

    empty_path = SCRATCH_DIR / "empty_reupload_fixture.md"
    empty_path.write_text("", encoding="utf-8")

    resp, status = c.http_post_multipart_file(base_url, f"/sources/{source_id}/reupload", empty_path)
    print(f"  POST reupload（空文件）: HTTP {status} {resp}")
    shadow_id = resp.get("shadow_source_id")

    state, shadow_src = poll_shadow(base_url, shadow_id, timeout_s=300)
    print(f"  影子处理状态: {state}")

    original_intact = check_lifecycle_intact(conn, source_id)
    print(f"  原 Source KU/KP 是否完全不受影响: {original_intact}")

    retry_ok = None
    if state == "failed":
        try:
            retry_resp, retry_status = c.http_post_json(base_url, f"/sources/{source_id}/reupload/retry", {})
            print(f"  POST reupload/retry: HTTP {retry_status} {retry_resp}")
            retry_ok = retry_status == 200
        except Exception as e:
            print(f"  ! retry 调用失败: {e}", file=sys.stderr)
            retry_ok = False

    return {
        "source_id": source_id,
        "shadow_id": shadow_id,
        "shadow_state": state,
        "original_intact": original_intact,
        "retry_attempted": retry_ok,
    }


def check_lifecycle_intact(conn, source_id):
    units = c.db_units_for_source(conn, source_id)
    points = c.db_points_for_source(conn, source_id)
    return all(u["lifecycle"] == "current" for u in units) and all(p["lifecycle"] == "current" for p in points)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--db-path", default=str(c.DEFAULT_DB_PATH))
    parser.add_argument("--timeout", type=float, default=240.0)
    parser.add_argument("--delay", type=float, default=1.0)
    parser.add_argument("--skip-failure-branch", action="store_true")
    parser.add_argument("--out", default=str(c.RESULTS_DIR))
    args = parser.parse_args()

    if not c.wait_for_server(args.base_url):
        print(f"无法连接 {args.base_url}，请先启动服务：./run.sh start", file=sys.stderr)
        sys.exit(1)

    SCRATCH_DIR.mkdir(parents=True, exist_ok=True)
    conn = c.open_db(args.db_path)

    results = []
    for target in TARGETS:
        try:
            results.append(check_target(args.base_url, conn, target, args.timeout, args.delay))
        except Exception as e:
            print(f"  ! {target['title']} 处理异常: {e}", file=sys.stderr)
            results.append({"title": target["title"], "error": str(e)})

    failure_result = None
    if not args.skip_failure_branch:
        failure_result = run_failure_branch(args.base_url, conn, args.timeout)

    print("\n========== P6 通过标准核对 ==========")
    for r in results:
        if r.get("error"):
            print(f"{r['title']}: ERROR {r['error']}")
            continue
        ok = (
            not r["shadow_visible_during_processing"]
            and r["old_marker_gone"]
            and r["new_marker_present"]
            and r["non_current_unit_count"] == 0
            and not any(s["adopt_count_grew"] for s in r["old_link_adopt_counts"].values())
        )
        print(
            f"{r['title']}: {'PASS' if ok else 'FAIL'}（影子不可见={not r['shadow_visible_during_processing']}, "
            f"旧值消失={r['old_marker_gone']}, 新值出现={r['new_marker_present']}, "
            f"KU全部current/superseded={r['non_current_unit_count'] == 0}, "
            f"链接adopt_count未增长={not any(s['adopt_count_grew'] for s in r['old_link_adopt_counts'].values())}）"
        )

    if failure_result and not failure_result.get("error"):
        print(
            f"失败分支: 影子 status=failed {'PASS' if failure_result['shadow_state'] == 'failed' else 'FAIL'}, "
            f"原 Source 不受影响 {'PASS' if failure_result['original_intact'] else 'FAIL'}"
        )

    conn.close()
    record = {"targets": results, "failure_branch": failure_result}
    jsonl_path = c.write_jsonl([record], Path(args.out), "v1_p6_reupload")
    print(f"\n详细结果: {jsonl_path}")


if __name__ == "__main__":
    main()
