#!/usr/bin/env python3
"""
Run retrieval/answer evaluation against a local wiki-brain server.

Primary mode compares /retrieval force_full=true vs normal retrieval for every
question. This directly measures full-path baseline versus wiki/fast-enabled
retrieval without changing server config.
"""

from __future__ import annotations

import argparse
import json
import statistics
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_QUESTIONS = ROOT / "test" / "v1" / "questions.jsonl"
DEFAULT_OUT_DIR = ROOT / "test" / "v1" / "out"


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows = []
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def post_json(base_url: str, path: str, payload: dict[str, Any], timeout: int) -> tuple[int, dict[str, Any], float]:
    url = base_url.rstrip("/") + path
    data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"}, method="POST")
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            elapsed = (time.perf_counter() - start) * 1000
            body = json.loads(raw.decode("utf-8")) if raw else {}
            return resp.status, body, elapsed
    except urllib.error.HTTPError as e:
        elapsed = (time.perf_counter() - start) * 1000
        raw = e.read().decode("utf-8", errors="replace")
        try:
            body = json.loads(raw)
        except json.JSONDecodeError:
            body = {"error": raw}
        return e.code, body, elapsed


def evidence_ids(es: dict[str, Any]) -> set[str]:
    ids: set[str] = set(es.get("cited_point_ids") or [])
    for key in ("direct_evidence", "supporting"):
        for item in es.get(key) or []:
            point_id = item.get("point_id")
            if point_id:
                ids.add(point_id)
    return ids


def direct_count(es: dict[str, Any]) -> int:
    if es.get("path_type") == "wiki":
        return len(es.get("cited_point_ids") or [])
    return len(es.get("direct_evidence") or [])


def run_retrieval_eval(args: argparse.Namespace) -> list[dict[str, Any]]:
    questions = read_jsonl(args.questions)
    if args.limit:
        questions = questions[: args.limit]

    results: list[dict[str, Any]] = []
    for i, q in enumerate(questions, 1):
        question = q["question"]
        print(f"[{i}/{len(questions)}] {q['id']} {question[:60]}")

        full_status, full_body, full_ms = post_json(
            args.base_url,
            "/retrieval",
            {"question": question, "force_full": True},
            args.timeout,
        )
        normal_status, normal_body, normal_ms = post_json(
            args.base_url,
            "/retrieval",
            {"question": question},
            args.timeout,
        )

        full_ids = evidence_ids(full_body) if full_status == 200 else set()
        normal_ids = evidence_ids(normal_body) if normal_status == 200 else set()
        overlap = len(full_ids & normal_ids)
        union = len(full_ids | normal_ids)

        row = {
            "id": q["id"],
            "question": question,
            "category": q.get("category", ""),
            "source_title": q.get("source_title", ""),
            "heading": q.get("heading", ""),
            "full_status": full_status,
            "normal_status": normal_status,
            "full_ms": round(full_ms, 1),
            "normal_ms": round(normal_ms, 1),
            "speedup": round(full_ms / normal_ms, 2) if normal_ms > 0 else None,
            "full_path_type": full_body.get("path_type"),
            "normal_path_type": normal_body.get("path_type"),
            "full_path": full_body.get("path"),
            "normal_path": normal_body.get("path"),
            "full_direct_count": direct_count(full_body) if full_status == 200 else 0,
            "normal_direct_count": direct_count(normal_body) if normal_status == 200 else 0,
            "normal_activation_hits": len(normal_body.get("activation_hits") or []) if normal_status == 200 else 0,
            "point_overlap": overlap,
            "point_jaccard": round(overlap / union, 3) if union else None,
            "full_error": "" if full_status == 200 else full_body,
            "normal_error": "" if normal_status == 200 else normal_body,
        }
        results.append(row)
    return results


def run_answer_sample(args: argparse.Namespace) -> list[dict[str, Any]]:
    questions = read_jsonl(args.questions)
    if args.limit:
        questions = questions[: args.limit]

    results: list[dict[str, Any]] = []
    for i, q in enumerate(questions, 1):
        question = q["question"]
        print(f"[{i}/{len(questions)}] answer {q['id']} {question[:60]}")
        status, body, elapsed = post_json(
            args.base_url,
            "/answer",
            {"question": question, "deep": False},
            args.timeout,
        )
        results.append(
            {
                "id": q["id"],
                "question": question,
                "status": status,
                "latency_ms": round(elapsed, 1),
                "path": body.get("path"),
                "path_type": body.get("path_type"),
                "has_answer": body.get("has_answer"),
                "citations_count": len(body.get("citations") or []),
                "answer_id": body.get("answer_id"),
                "content_preview": (body.get("content") or "")[:180],
                "error": "" if status == 200 else body,
            }
        )
    return results


def write_outputs(rows: list[dict[str, Any]], out_dir: Path, name: str) -> None:
    out_dir.mkdir(parents=True, exist_ok=True)
    jsonl = out_dir / f"{name}.jsonl"
    with jsonl.open("w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    summary = summarize(rows)
    (out_dir / f"{name}_summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    print(f"wrote {jsonl}")


def summarize(rows: list[dict[str, Any]]) -> dict[str, Any]:
    ok = [r for r in rows if r.get("normal_status", r.get("status")) == 200]
    lat_key = "normal_ms" if rows and "normal_ms" in rows[0] else "latency_ms"
    latencies = [float(r[lat_key]) for r in ok if isinstance(r.get(lat_key), (int, float))]
    path_counts: dict[str, int] = {}
    for r in ok:
        path_type = r.get("normal_path_type") or r.get("path_type") or "unknown"
        path_counts[path_type] = path_counts.get(path_type, 0) + 1

    summary: dict[str, Any] = {
        "total": len(rows),
        "ok": len(ok),
        "errors": len(rows) - len(ok),
        "path_type_counts": path_counts,
    }
    if latencies:
        summary.update(
            {
                "latency_avg_ms": round(statistics.mean(latencies), 1),
                "latency_p50_ms": round(statistics.median(latencies), 1),
                "latency_p90_ms": round(percentile(latencies, 90), 1),
            }
        )
    if rows and "full_ms" in rows[0]:
        speedups = [float(r["speedup"]) for r in ok if isinstance(r.get("speedup"), (int, float))]
        jaccards = [float(r["point_jaccard"]) for r in ok if isinstance(r.get("point_jaccard"), (int, float))]
        summary.update(
            {
                "avg_speedup_full_over_normal": round(statistics.mean(speedups), 2) if speedups else None,
                "avg_point_jaccard_full_vs_normal": round(statistics.mean(jaccards), 3) if jaccards else None,
                "normal_fast_or_wiki": sum(1 for r in ok if r.get("normal_path_type") in ("fast", "wiki")),
                "normal_direct_empty": sum(1 for r in ok if r.get("normal_direct_count", 0) == 0),
            }
        )
    return summary


def percentile(values: list[float], p: int) -> float:
    values = sorted(values)
    if not values:
        return 0
    k = (len(values) - 1) * p / 100
    lo = int(k)
    hi = min(lo + 1, len(values) - 1)
    frac = k - lo
    return values[lo] * (1 - frac) + values[hi] * frac


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    parser.add_argument("--questions", type=Path, default=DEFAULT_QUESTIONS)
    parser.add_argument("--out-dir", type=Path, default=DEFAULT_OUT_DIR)
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--mode", choices=["retrieval", "answer"], default="retrieval")
    args = parser.parse_args()

    if args.mode == "retrieval":
        rows = run_retrieval_eval(args)
    else:
        rows = run_answer_sample(args)
    write_outputs(rows, args.out_dir, args.mode)


if __name__ == "__main__":
    main()
