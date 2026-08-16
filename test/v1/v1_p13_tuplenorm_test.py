#!/usr/bin/env python3
"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P13：问题四元组归一化
（2026-08-12 新增能力，config-gated 默认关闭，见 `internal/activation/tuplenorm.go`
`TupleNormalizer` 与 `docs/impl/v1/retrieval.md` 步骤 2）。

背景（务必先读）：`activation.Matcher`/`BundleMatcher`/`wiki.matchFourTupleEntry`
三处四元组消费入口本身已经定案为纯精确匹配（2026-08-12 改判，不含任何模糊/模型
辅助），LLM 抽取的措辞抖动交给 Retrieval `tryFastPath` 里、送入这三处之前的
`TupleNormalizer.Normalize` 单独处理：同一意思的问题第二次问出来，四层递进
（Tier1 精确匹配 `question_tuple_norms` 表 → Tier2 本地词集 Jaccard →
Tier2.5 向量早筛（只拒绝不确认）→ Tier3 LLM 批量判断）把新抽取的四元组替换成
第一次见到的 canonical 四元组，再送去 Matcher 等处。

本脚本验证的是这条归一化本身是否生效，观察面选 `question_kp_cooccurrence`
（按 `question_terms` 分组的共现表，`UNIQUE(question_terms, point_id)`，
见 `internal/foundation/db/migrations/005_traces.sql`）而不是直接断言
`path_type=fast`——因为 Tier1-3 命中只解决"归一化到同一 canonical 四元组"，
是否真的走上快路径还要看 ActivationLink 是否已经在该 canonical 四元组下
积累到 self_graded/trusted 档，这是另一条独立的收敛曲线（P2 覆盖），不是本
阶段要重复验证的东西。归一化生效的直接证据是：两种措辞变体最终落在
`question_kp_cooccurrence` 的**同一行**（同一 `question_terms`）上，
`hit_count` 累加，而不是分裂成两行各自计数（这正是归一化要解决的"抖动导致
学习信号碎片化"问题，同 MEMORY.md「V1 test root causes」记录的现象）。

用法：
  python3 test/v1/v1_p13_tuplenorm_test.py \\
      --variants-file test/v1/v1_p13_variants.json
    # variants-file: {"variants": ["问法1", "问法2", ...]}（≥2 条，同一潜在问题
    # 的不同措辞，理想情况下第一条已在 P1/P2 培养过、有稳定的 KU/KP 归属）

  # 只想验证 Tier1/2（本地）而不启用向量早筛（默认）：
  python3 test/v1/v1_p13_tuplenorm_test.py --variants-file ...

  # 同时打开向量早筛（Tier2.5，要求 config.yml 的 vector_model_dir 指向真实
  # 已下载的 goformer 模型权重，否则该 tier 优雅降级为跳过）：
  python3 test/v1/v1_p13_tuplenorm_test.py --variants-file ... --enable-vector

注意：脚本会临时改写 config/config.yml 的 question_tuple_norm_enabled（及
--enable-vector 时的 vector_match_enabled）为 true 并重启服务以生效，结束时
（含异常路径）恢复为改动前的值并重启——这两个开关默认 false，不应该在测试脚本
跑完后遗留在 config.yml 里。
"""
import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

import v1_common as c

REPO_ROOT = c.REPO_ROOT
CONFIG_PATH = REPO_ROOT / "config" / "config.yml"
RUN_SH = REPO_ROOT / "run.sh"

DEFAULT_VARIANTS = [
    "达梦数据库怎么优化索引",
    "达梦数据库如何做索引方面的优化",
]


def load_variants(path):
    if not path:
        return DEFAULT_VARIANTS
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    variants = data.get("variants", DEFAULT_VARIANTS)
    if len(variants) < 2:
        raise RuntimeError("variants-file 至少需要 2 条同一问题的不同问法")
    return variants


def read_config_bool(key):
    text = CONFIG_PATH.read_text(encoding="utf-8")
    m = re.search(rf"{key}:\s*(true|false)", text)
    if not m:
        raise RuntimeError(f"config.yml 中找不到 {key}")
    return m.group(1) == "true"


def set_config_bool(key, enabled):
    text = CONFIG_PATH.read_text(encoding="utf-8")
    new_val = "true" if enabled else "false"
    replaced, n = re.subn(
        rf"({key}:\s*)(true|false)",
        rf"\g<1>{new_val}",
        text,
        count=1,
    )
    if n != 1:
        raise RuntimeError(f"未能改写 config.yml 中的 {key}（匹配数={n}）")
    CONFIG_PATH.write_text(replaced, encoding="utf-8")
    print(f"... config {key}={new_val}")


def restart_server(base_url):
    """同 P3/P11 的 restart_server：改配置后必须重启才能让新 config.yml 生效
    （TupleNormalizer 的 cfg 在启动时装配，不是热加载）。"""
    print("... 重启服务（run.sh restart）以刷新配置 ...")
    subprocess.check_call([str(RUN_SH), "restart"], cwd=str(REPO_ROOT))
    deadline = time.time() + 60
    while time.time() < deadline:
        if c.wait_for_server(base_url):
            print("... 服务已就绪")
            return
        time.sleep(1)
    raise RuntimeError(f"重启后服务未就绪: {base_url}")


def wait_trace(conn, answer_id, timeout_s=20):
    return c.poll_until(lambda: c.db_trace_by_answer_id(conn, answer_id), timeout_s=timeout_s, interval_s=0.5)


def ask_and_observe(base_url, conn, question, timeout):
    """走真实 session 路径（同 P11 ask_via_session 用法），返回
    (turn, result, trace, direct_point_ids, domain_ids)。"""
    turn, result = c.ask_via_session(base_url, question, deep=False, timeout=timeout, session_id=None)
    if not result:
        return turn, result, None, [], []
    trace = wait_trace(conn, result.get("answer_id"))
    direct_point_ids = json.loads(trace["direct_point_ids"] or "[]") if trace else []
    domain_ids = (turn.get("expanded_query") or {}).get("domain_ids") or []
    return turn, result, trace, direct_point_ids, domain_ids


def snapshot_cooc(conn, point_ids):
    """按 point_id 拍快照：{point_id: {question_terms: hit_count}}，用于对比
    两次问法之间是"同一行 hit_count 增长"还是"新增了一行"。"""
    snap = {}
    for pid in point_ids:
        rows = c.db_cooccurrence_for_point(conn, pid)
        snap[pid] = {r["question_terms"]: r["hit_count"] for r in rows}
    return snap


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    parser.add_argument("--db-path", default=None)
    parser.add_argument("--variants-file", default=None)
    parser.add_argument("--enable-vector", action="store_true",
                         help="同时打开 vector_match_enabled（Tier2.5）；默认只测 Tier1/2/3")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--delay", type=float, default=1.0)
    parser.add_argument("--no-restore", action="store_true",
                         help="调试用：跑完不恢复 config.yml（正常验收请勿加）")
    args = parser.parse_args()

    variants = load_variants(args.variants_file)

    prev_tuple_norm = read_config_bool("question_tuple_norm_enabled")
    prev_vector = read_config_bool("vector_match_enabled")

    conn = c.open_db(args.db_path)
    results = {"variants": variants, "enable_vector": args.enable_vector}

    try:
        set_config_bool("question_tuple_norm_enabled", True)
        if args.enable_vector:
            set_config_bool("vector_match_enabled", True)
        restart_server(args.base_url)

        watermark_norms = None
        row = conn.execute("SELECT MAX(created_at) AS m FROM question_tuple_norms").fetchone()
        if row and row["m"]:
            watermark_norms = row["m"]

        print(f"\n--- 第一次问法（预期 Tier4：全部未命中，插入新 canonical 行）---\n  {variants[0]!r}")
        turn1, result1, trace1, points1, domains1 = ask_and_observe(args.base_url, conn, variants[0], args.timeout)
        if not result1:
            print(f"! 第一次提问未进入 retrieve（action={turn1.get('action')}），无法继续", file=sys.stderr)
            sys.exit(1)
        print(f"  answer_id={result1.get('answer_id')} direct_point_ids={points1} domain_ids={domains1}")

        norms_after_1 = c.db_question_tuple_norms(conn, since_created_at=watermark_norms)
        print(f"  新增 question_tuple_norms 行数: {len(norms_after_1)}（每个 domain_id 各插入一行，期望 == len(domain_ids)={len(domains1)}）")

        cooc_before_2 = snapshot_cooc(conn, points1)
        time.sleep(args.delay)

        print(f"\n--- 第二次问法（预期归一化命中，落到同一 canonical 四元组）---\n  {variants[1]!r}")
        turn2, result2, trace2, points2, domains2 = ask_and_observe(args.base_url, conn, variants[1], args.timeout)
        if not result2:
            print(f"! 第二次提问未进入 retrieve（action={turn2.get('action')}），无法继续", file=sys.stderr)
            sys.exit(1)
        print(f"  answer_id={result2.get('answer_id')} direct_point_ids={points2} domain_ids={domains2}")

        norms_after_2 = c.db_question_tuple_norms(conn, since_created_at=watermark_norms)
        no_new_canonical = len(norms_after_2) == len(norms_after_1)
        print(f"  累计新增 question_tuple_norms 行数: {len(norms_after_2)}（期望与第一次问法后相同 {len(norms_after_1)}，"
              f"即第二次问法没有再插入新 canonical 行，而是命中了 Tier1/2/2.5+3 之一）")

        shared_points = sorted(set(points1) & set(points2))
        print(f"\n  两次问法共同命中的 point_id: {shared_points}（期望非空——变体应指向同一 KP）")

        no_fragmentation = True
        per_point_detail = {}
        for pid in shared_points:
            rows_after = {r["question_terms"]: r["hit_count"] for r in c.db_cooccurrence_for_point(conn, pid)}
            before = cooc_before_2.get(pid, {})
            new_terms_groups = set(rows_after) - set(before)
            grown_existing = [t for t in before if rows_after.get(t, 0) > before[t]]
            per_point_detail[pid] = {
                "before": before, "after": rows_after,
                "new_question_terms_groups": sorted(new_terms_groups),
                "grown_existing_groups": grown_existing,
            }
            print(f"  point_id={pid}: before={before} after={rows_after}")
            if new_terms_groups and not grown_existing:
                # 第二次问法在这个 point 上开了一个全新的 question_terms 分组，
                # 且没有任何已有分组增长——即归一化没有生效，信号碎片化了。
                no_fragmentation = False

        results.update({
            "answer_id_1": result1.get("answer_id"),
            "answer_id_2": result2.get("answer_id"),
            "domains_1": domains1,
            "points_1": points1,
            "points_2": points2,
            "shared_points": shared_points,
            "new_canonical_rows_after_variant1": len(norms_after_1),
            "new_canonical_rows_after_variant2": len(norms_after_2),
            "no_new_canonical_on_variant2": no_new_canonical,
            "per_point_cooccurrence": per_point_detail,
            "no_fragmentation": no_fragmentation,
        })

        overall_pass = bool(shared_points) and no_new_canonical and no_fragmentation
        print("\n=== P13 结果汇总 ===")
        print(json.dumps(results, ensure_ascii=False, indent=2, default=str))
        print(f"\n通过标准: shared_points 非空={bool(shared_points)}, "
              f"第二次问法未新增 canonical 行={no_new_canonical}, "
              f"共现未碎片化={no_fragmentation}")
        print(f"P13 总体: {'PASS' if overall_pass else 'FAIL'}")

        out_dir = c.RESULTS_DIR
        c.write_jsonl([results], out_dir, "v1_p13_tuplenorm")

        sys.exit(0 if overall_pass else 2)
    finally:
        conn.close()
        if not args.no_restore:
            try:
                set_config_bool("question_tuple_norm_enabled", prev_tuple_norm)
                set_config_bool("vector_match_enabled", prev_vector)
                restart_server(args.base_url)
            except Exception as e:
                print(f"! 恢复 config.yml 失败，请手工检查: {e}", file=sys.stderr)


if __name__ == "__main__":
    main()
