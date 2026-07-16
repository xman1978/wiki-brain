#!/usr/bin/env python3
"""
MVP 验收测试方案第 4 节「问答准确率测试集」自动化脚本。

用途：逐题向运行中的 wiki-brain 服务发送 A/T/G 组共 80 题（POST /answer，
short 路径），自动核对：
  - direct 命中：EvidenceSet.direct_evidence 的来源是否落在期望文档
  - 关键词覆盖：从「期望答案要点」抽取的数字/代码片段是否出现在回答正文中
    （这是可自动化的正确性代理指标，不能替代 test/mvp-acceptance-test-plan.md
    第 4 节要求的人工核对「回答要点是否正确」——报告里留了 manual_verdict 列，
    人工复核后按该列重新统计才是最终验收结果）

题库直接从 test/mvp-acceptance-test-plan.md 的表格解析，不在本脚本内重复抄写，
避免两处漂移（该文档第 5 行本身要求"两份方案的题库同源，修改任一处须同步"）。

用法：
  python3 test/qa_accuracy_test.py                  # 跑全部 80 题
  python3 test/qa_accuracy_test.py --group A        # 只跑 A 组
  python3 test/qa_accuracy_test.py --ids A1,T3,G10  # 只跑指定题
  python3 test/qa_accuracy_test.py --base-url http://localhost:8800
"""
import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
PLAN_PATH = REPO_ROOT / "test" / "mvp-acceptance-test-plan.md"
RESULTS_DIR = REPO_ROOT / "test" / "results"

# 期望证据来源缩写 -> 实际已导入 Source 的标题（见 data/wiki-brain.db sources 表）。
# 一个缩写可能对应多个候选标题（如"两篇 RAC"同时指向 11g/19c 两篇文档），
# 命中其一即算 direct_hit。
SOURCE_ABBREV_TO_TITLES = {
    "报销规定": ["日常费用报销期限管理规定"],
    "培训积分": ["培训积分管理办法"],
    "平台办法": ["大模型开发测试基础平台使用暂行管理办法"],
    "应收账款": ["应收账款管理制度"],
    "项目考核": ["项目考核与激励制度"],
    "差旅费": ["差旅费报销制度"],
    "绩效管理": ["绩效管理制度"],
    "无合同立项": ["无合同立项申请与审批规范"],
    "万相公文": ["万相公文销售奖励制度", "万相公文渠道伙伴合作政策"],
    "docker swarm": ["Dock Swam 集群部署"],
    "swarm": ["Dock Swam 集群部署"],
    "k8s": ["k8s部署"],
    "mysql": ["MYSQL 主从热备部署"],
    "rac 开启归档": ["Oracle RAC 开启归档"],
    "rac 问题汇总": ["Oracle RAC 问题汇总"],
    "达梦": ["达梦数据库优化"],
    "神通": ["神通数据库优化"],
    "金仓": ["金仓数据库优化"],
    "alwayson": ["SQL Server AlwaysOn 安装配置"],
    "19c rac": ["Oracle 19c RAC 集群安装部署维护"],
    "11g rac": ["Oracle 11g RAC 集群安装部署维护"],
    "两篇 rac": ["Oracle 11g RAC 集群安装部署维护", "Oracle 19c RAC 集群安装部署维护"],
}

ROW_RE = re.compile(
    r"^\|\s*([ATG]\d+)\s*\|\s*(.+?)\s*\|\s*(.+?)\s*\|\s*(.+?)\s*\|\s*$"
)


def parse_question_bank(plan_path: Path):
    """从第 4 节的 markdown 表格里解析出 80 题，返回按 ID 排序的 list[dict]。"""
    text = plan_path.read_text(encoding="utf-8")
    m = re.search(r"^## 4\.[^\n]*\n(.*?)^## 5\.", text, re.M | re.S)
    if not m:
        raise RuntimeError("在测试方案里找不到第 4 节（问答准确率测试集）")
    section = m.group(1)

    rows = []
    for line in section.splitlines():
        m = ROW_RE.match(line)
        if not m:
            continue
        qid, question, points, source = m.groups()
        if qid in ("ID",) or set(question) <= {"-"}:
            continue
        rows.append(
            {
                "id": qid,
                "question": question,
                "expected_points": points,
                "expected_source": source,
            }
        )

    rows.sort(key=lambda r: (r["id"][0], int(r["id"][1:])))
    return rows


def domain_of(qid: str) -> str:
    """A 组、G1-G24 为制度域；T 组、G25-G48 为技术域（见方案 4.3 节分组标题）。"""
    if qid.startswith("A"):
        return "制度域"
    if qid.startswith("T"):
        return "技术域"
    if qid.startswith("G"):
        n = int(qid[1:])
        return "制度域" if n <= 24 else "技术域"
    raise ValueError(f"未知题号分组: {qid}")


def expected_titles_for(source_text: str):
    abbrev = source_text.split("·", 1)[0].strip().lower()
    for key, titles in SOURCE_ABBREV_TO_TITLES.items():
        if key.lower() == abbrev:
            return titles
    # 退化匹配：缩写作为子串出现在映射表 key 里（容错标点/空格差异）
    for key, titles in SOURCE_ABBREV_TO_TITLES.items():
        if key.lower() in abbrev or abbrev in key.lower():
            return titles
    return []


UNIT_NUMBER_RE = re.compile(
    r"\d+(?:\.\d+)?\s*(?:%|天|元|分|次|个月|小时|台|条|倍|港币|年|周|万|亿|人|级|档)"
)
BARE_NUMBER_RE = re.compile(r"(?<!\d)\d{2,}(?:\.\d+)?(?!\d)|(?<!\d)\d+\.\d+(?!\d)")


def extract_key_terms(points_text: str):
    """从「期望答案要点」里抽取可机器核对的关键词：反引号代码片段 + 数字(+单位)。

    单独的个位数(如"3")噪音太大——几乎任何回答里都会出现，不计入；
    优先保留"数字+单位"整体(如"45天")，未命中单位的裸数字只在两位数以上
    或带小数点时才保留(如端口号 2377、价格 256、0.15)。
    """
    terms = set(re.findall(r"`([^`]+)`", points_text))
    consumed = set()
    for m in UNIT_NUMBER_RE.finditer(points_text):
        terms.add(re.sub(r"\s+", "", m.group(0)))
        consumed.add(m.span())
    for m in BARE_NUMBER_RE.finditer(points_text):
        if any(m.start() >= s and m.end() <= e for s, e in consumed):
            continue
        terms.add(m.group(0))
    return sorted(terms)


def http_get_json(base_url, path, timeout=30):
    with urllib.request.urlopen(f"{base_url}{path}", timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def http_post_json(base_url, path, payload, timeout=180):
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{base_url}{path}",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8")), resp.status


def fetch_source_titles(base_url):
    """GET /sources 分页拉全量，建 source_id -> title 映射，用于 direct_hit 判定。"""
    id_to_title = {}
    offset = 0
    limit = 50
    while True:
        data = http_get_json(base_url, f"/sources?limit={limit}&offset={offset}")
        items = data.get("items") or data.get("sources") or []
        if not items:
            break
        for it in items:
            id_to_title[it["source_id"]] = it["title"]
        if len(items) < limit:
            break
        offset += limit
    return id_to_title


def evidence_source_ids(evidence_list):
    ids = []
    for ev in evidence_list or []:
        ref = ev.get("source_ref")
        if not ref:
            continue
        if isinstance(ref, str):
            try:
                ref = json.loads(ref)
            except json.JSONDecodeError:
                continue
        sid = ref.get("source_id")
        if sid:
            ids.append(sid)
    return ids


def run_question(base_url, row, id_to_title, timeout):
    payload = {"question": row["question"], "deep": False}
    t0 = time.time()
    try:
        result, status = http_post_json(base_url, "/answer", payload, timeout=timeout)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", "ignore")
        return {
            **row,
            "domain": domain_of(row["id"]),
            "error": f"HTTP {e.code}: {body[:300]}",
        }
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return {**row, "domain": domain_of(row["id"]), "error": str(e)}
    latency = time.time() - t0

    content = result.get("content", "")
    citations = result.get("citations", [])
    es = result.get("evidence_snapshot") or {}
    direct_ev = es.get("direct_evidence", [])
    direct_ids = evidence_source_ids(direct_ev)
    direct_titles = sorted({id_to_title.get(sid, sid) for sid in direct_ids})

    expected_titles = expected_titles_for(row["expected_source"])
    direct_hit = bool(expected_titles) and any(t in expected_titles for t in direct_titles)

    key_terms = extract_key_terms(row["expected_points"])
    found_terms = [t for t in key_terms if t.lower() in content.lower()]
    coverage = (len(found_terms), len(key_terms))

    return {
        **row,
        "domain": domain_of(row["id"]),
        "path": result.get("path"),
        "path_type": result.get("path_type"),
        "has_answer": result.get("has_answer"),
        "answer_id": result.get("answer_id"),
        "latency_s": round(latency, 2),
        "content": content,
        "citations_count": len(citations),
        "expected_titles": expected_titles,
        "direct_evidence_titles": direct_titles,
        "direct_hit": direct_hit,
        "key_terms": key_terms,
        "found_terms": found_terms,
        "key_term_coverage": coverage,
    }


def write_reports(records, out_dir: Path):
    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    jsonl_path = out_dir / f"qa_accuracy_{stamp}.jsonl"
    md_path = out_dir / f"qa_accuracy_{stamp}.md"

    with jsonl_path.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    lines = []
    lines.append("# QA 准确率测试报告（MVP 方案第 4 节，自动核对部分）\n")
    lines.append(
        "direct_hit 与关键词覆盖率为自动核对，**不能替代人工核对「回答要点是否正确」**；"
        "manual_verdict 列留空，需人工按方案第 4 节标准填写 正确/错误 后重新统计。\n"
    )
    lines.append(
        "| ID | 域 | path | direct_hit | 关键词覆盖 | citations | manual_verdict | 回答摘要 |"
    )
    lines.append("|---|---|---|---|---|---|---|---|")

    domain_stats = {}
    for r in records:
        if r.get("error"):
            lines.append(f"| {r['id']} | {r['domain']} | ERROR | - | - | - |  | {r['error'][:80]} |")
            continue
        found, total = r["key_term_coverage"]
        cov = f"{found}/{total}" if total else "n/a"
        summary = r["content"].replace("\n", " ")[:60]
        lines.append(
            f"| {r['id']} | {r['domain']} | {r.get('path')} | "
            f"{'✅' if r['direct_hit'] else '❌'} | {cov} | {r['citations_count']} |  | {summary} |"
        )

        st = domain_stats.setdefault(r["domain"], {"n": 0, "direct_hit": 0, "cov_found": 0, "cov_total": 0})
        st["n"] += 1
        st["direct_hit"] += 1 if r["direct_hit"] else 0
        st["cov_found"] += found
        st["cov_total"] += total

    lines.append("\n## 汇总（自动核对指标，目标见方案第 6 节：direct ≥85%，回答要点 ≥90%）\n")
    lines.append("| 域 | 题数 | direct 命中率 | 关键词覆盖率 |")
    lines.append("|---|---|---|---|")
    for domain, st in domain_stats.items():
        dh_rate = st["direct_hit"] / st["n"] * 100 if st["n"] else 0
        cov_rate = st["cov_found"] / st["cov_total"] * 100 if st["cov_total"] else 0
        lines.append(f"| {domain} | {st['n']} | {dh_rate:.1f}% | {cov_rate:.1f}% |")

    md_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return jsonl_path, md_path


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", default="http://localhost:8800")
    parser.add_argument("--group", choices=["A", "T", "G", "all"], default="all")
    parser.add_argument("--ids", help="逗号分隔的题号列表，如 A1,T3,G10（优先于 --group）")
    parser.add_argument("--timeout", type=float, default=180.0, help="单题 HTTP 超时秒数")
    parser.add_argument("--delay", type=float, default=0.5, help="题间等待秒数，避免打爆 LLM 并发")
    parser.add_argument("--out", default=str(RESULTS_DIR))
    args = parser.parse_args()

    bank = parse_question_bank(PLAN_PATH)
    if args.ids:
        wanted = {s.strip() for s in args.ids.split(",") if s.strip()}
        bank = [r for r in bank if r["id"] in wanted]
    elif args.group != "all":
        bank = [r for r in bank if r["id"].startswith(args.group)]

    if not bank:
        print("没有匹配到任何题目，检查 --group/--ids 参数", file=sys.stderr)
        sys.exit(1)

    try:
        http_get_json(args.base_url, "/sources?limit=1")
    except Exception as e:
        print(f"无法连接 {args.base_url}（{e}）。请先启动服务：go run ./cmd/server", file=sys.stderr)
        sys.exit(1)

    id_to_title = fetch_source_titles(args.base_url)
    print(f"已加载 {len(id_to_title)} 个 Source，共 {len(bank)} 道题待测。\n")

    records = []
    for i, row in enumerate(bank, 1):
        print(f"[{i}/{len(bank)}] {row['id']}: {row['question']}")
        rec = run_question(args.base_url, row, id_to_title, args.timeout)
        if rec.get("error"):
            print(f"  ! 出错: {rec['error']}")
        else:
            found, total = rec["key_term_coverage"]
            print(
                f"  path={rec.get('path')} direct_hit={rec['direct_hit']} "
                f"关键词 {found}/{total} 耗时 {rec['latency_s']}s"
            )
        records.append(rec)
        if i < len(bank):
            time.sleep(args.delay)

    jsonl_path, md_path = write_reports(records, Path(args.out))
    print(f"\n详细结果: {jsonl_path}")
    print(f"汇总报告: {md_path}")


if __name__ == "__main__":
    main()
