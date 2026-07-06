#!/usr/bin/env python3
"""
Generate an 80-100 question evaluation set from imported markdown sources.

The generator is deterministic and uses only local markdown text. It does not
call an LLM, so the questions are intentionally template-based. The goal is to
produce a stable smoke/eval set for retrieval-path, latency, and citation
comparison.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
DEFAULT_SOURCE_DIR = ROOT / "data" / "sources" / "markdown"
DEFAULT_OUT = ROOT / "test" / "v1" / "questions.jsonl"


STOP_HEADINGS = {"目录", "前言", "附则", "附件"}
QUESTION_TARGET = 90


@dataclass
class Section:
    source_file: str
    source_title: str
    heading: str
    body: str
    keywords: list[str]


def clean_text(text: str) -> str:
    text = re.sub(r"```.*?```", " ", text, flags=re.S)
    text = re.sub(r"!\[[^\]]*\]\([^)]*\)", " ", text)
    text = re.sub(r"\[[^\]]+\]\([^)]*\)", " ", text)
    text = re.sub(r"<[^>]+>", " ", text)
    text = re.sub(r"[ \t]+", " ", text)
    return text.strip()


def source_title(path: Path, text: str) -> str:
    for line in text.splitlines():
        m = re.match(r"^\s*#\s+(.+?)\s*$", line)
        if m:
            return clean_text(m.group(1))
    return path.stem


def extract_sections(path: Path) -> list[Section]:
    text = path.read_text(encoding="utf-8", errors="ignore")
    title = source_title(path, text)
    sections: list[Section] = []
    current_heading = title
    current: list[str] = []

    def flush() -> None:
        nonlocal current
        body = clean_text("\n".join(current))
        heading = clean_text(current_heading)
        if len(body) < 60 or heading in STOP_HEADINGS:
            current = []
            return
        keywords = extract_keywords(heading + " " + body)
        sections.append(
            Section(
                source_file=path.name,
                source_title=title,
                heading=heading,
                body=body,
                keywords=keywords,
            )
        )
        current = []

    for raw in text.splitlines():
        m = re.match(r"^\s{0,3}(#{1,4})\s+(.+?)\s*$", raw)
        if m:
            flush()
            current_heading = m.group(2)
            continue
        current.append(raw)
    flush()
    return sections


def extract_keywords(text: str, limit: int = 8) -> list[str]:
    candidates: list[str] = []
    patterns = [
        r"[\u4e00-\u9fff]{2,12}(?:管理|制度|办法|规范|流程|审批|报销|考核|奖励|积分|合同|立项|应收|账款|平台|权限|考勤|绩效)",
        r"(?:报销|审批|考勤|绩效|奖励|积分|合同|立项|应收账款|回款|差旅|项目|平台|权限|培训|销售)[\u4e00-\u9fff]{0,8}",
        r"[A-Za-z0-9][A-Za-z0-9_.-]{2,}",
    ]
    for pattern in patterns:
        for m in re.finditer(pattern, text):
            token = m.group(0).strip("，。；：、（）()[]【】")
            if 2 <= len(token) <= 18 and token not in candidates:
                candidates.append(token)
            if len(candidates) >= limit:
                return candidates
    return candidates


def short_context(section: Section) -> str:
    sentence = re.split(r"[。！？\n]", section.body)[0].strip()
    sentence = re.sub(r"^\s*[-*0-9.、）)]+\s*", "", sentence)
    return sentence[:80]


def make_questions(sections: list[Section], target: int) -> list[dict]:
    questions: list[dict] = []
    templates = [
        "根据《{title}》，{heading}的核心规则是什么？",
        "《{title}》里关于{heading}有哪些办理要求？",
        "如果遇到{keyword}相关事项，应该按什么流程处理？",
        "{heading}适用于哪些场景或对象？",
        "{heading}中有哪些限制条件、例外或注意事项？",
        "请说明《{title}》中{keyword}的判断标准。",
        "{keyword}需要哪些材料、审批或记录？",
        "违反或不满足{heading}要求时，可能会如何处理？",
        "《{title}》中{heading}和其他流程有什么关联？",
        "我想快速确认{keyword}，应查看哪些制度依据？",
    ]
    qid = 1

    for section in sections:
        if len(questions) >= target:
            break
        keyword = section.keywords[0] if section.keywords else section.heading
        per_section = 2 if len(sections) * 2 <= target else 1
        for template in templates[:per_section]:
            if len(questions) >= target:
                break
            questions.append(
                {
                    "id": f"q{qid:03d}",
                    "question": template.format(
                        title=section.source_title,
                        heading=section.heading,
                        keyword=keyword,
                    ),
                    "source_file": section.source_file,
                    "source_title": section.source_title,
                    "heading": section.heading,
                    "keywords": section.keywords,
                    "expected_text_hint": short_context(section),
                    "category": classify(section),
                }
            )
            qid += 1

    # Add cross-document business questions that better exercise real retrieval.
    cross = [
        ("cross_001", "员工出差后报销超期了，应该同时看哪些报销期限和差旅制度要求？", ["差旅", "报销", "期限"]),
        ("cross_002", "一个项目没有合同但需要先立项，审批和后续风险控制应怎么处理？", ["无合同", "立项", "审批"]),
        ("cross_003", "销售奖励、项目激励和绩效考核之间，哪些情况下会影响个人收益？", ["销售奖励", "项目激励", "绩效"]),
        ("cross_004", "客户长期未回款时，应收账款管理和项目考核会带来哪些约束？", ["应收账款", "回款", "项目考核"]),
        ("cross_005", "新员工使用大模型开发测试平台前，需要关注哪些权限、培训或合规要求？", ["平台", "权限", "培训"]),
        ("cross_006", "员工请假、考勤异常和绩效评价之间有什么关系？", ["请假", "考勤", "绩效"]),
        ("cross_007", "渠道伙伴合作产生销售收入后，奖励或结算需要满足哪些条件？", ["渠道伙伴", "销售", "奖励"]),
        ("cross_008", "培训积分不足是否会影响绩效或资格评定？应该查哪些制度？", ["培训积分", "绩效", "资格"]),
        ("cross_009", "费用报销、差旅报销和日常报销期限有什么不同？", ["费用报销", "差旅", "期限"]),
        ("cross_010", "如果制度材料之间出现口径不一致，回答时应该优先引用哪些证据？", ["制度", "证据", "口径"]),
    ]
    for cid, question, keywords in cross:
        if len(questions) >= 100:
            break
        questions.append(
            {
                "id": cid,
                "question": question,
                "source_file": "",
                "source_title": "跨制度综合",
                "heading": "跨制度综合",
                "keywords": keywords,
                "expected_text_hint": "",
                "category": "cross_document",
            }
        )

    return questions[:100]


def classify(section: Section) -> str:
    text = section.source_title + section.heading + " ".join(section.keywords)
    if any(k in text for k in ["流程", "审批", "办理", "申请"]):
        return "procedure"
    if any(k in text for k in ["标准", "条件", "规则", "要求"]):
        return "rule"
    if any(k in text for k in ["考核", "绩效", "奖励", "积分"]):
        return "evaluation"
    if any(k in text for k in ["报销", "费用", "差旅", "账款"]):
        return "finance"
    return "fact"


def write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")


def write_csv(path: Path, rows: list[dict]) -> None:
    csv_path = path.with_suffix(".csv")
    fieldnames = [
        "id",
        "question",
        "category",
        "source_title",
        "heading",
        "keywords",
        "expected_text_hint",
    ]
    with csv_path.open("w", encoding="utf-8", newline="") as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        for row in rows:
            item = dict(row)
            item["keywords"] = " ".join(item.get("keywords", []))
            writer.writerow({k: item.get(k, "") for k in fieldnames})


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-dir", type=Path, default=DEFAULT_SOURCE_DIR)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    parser.add_argument("--count", type=int, default=QUESTION_TARGET)
    args = parser.parse_args()

    sections: list[Section] = []
    for path in sorted(args.source_dir.glob("*.md")):
        sections.extend(extract_sections(path))

    if not sections:
        raise SystemExit(f"no markdown sections found under {args.source_dir}")

    questions = make_questions(sections, max(80, min(100, args.count)))
    write_jsonl(args.out, questions)
    write_csv(args.out, questions)
    print(f"wrote {len(questions)} questions to {args.out}")
    print(f"also wrote {args.out.with_suffix('.csv')}")


if __name__ == "__main__":
    main()
