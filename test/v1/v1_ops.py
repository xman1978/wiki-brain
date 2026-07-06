#!/usr/bin/env python3
"""
Small helper for the manual V1 learning loop: run Study, inspect pending
actions, and confirm selected ActivationLink/Wiki candidates.
"""

from __future__ import annotations

import argparse
import json
import urllib.parse
import urllib.request
from typing import Any


def request_json(base_url: str, method: str, path: str, payload: dict[str, Any] | None = None) -> Any:
    data = None
    headers = {}
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base_url.rstrip("/") + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=180) as resp:
        raw = resp.read()
        return json.loads(raw.decode("utf-8")) if raw else {}


def pretty(obj: Any) -> None:
    print(json.dumps(obj, ensure_ascii=False, indent=2))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8800")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("run-study")
    sub.add_parser("pending-promotions")
    sub.add_parser("candidate-links")
    confirm_link = sub.add_parser("confirm-link")
    confirm_link.add_argument("link_id")
    reject_link = sub.add_parser("reject-link")
    reject_link.add_argument("link_id")
    sub.add_parser("wiki-candidates")
    compile_wiki = sub.add_parser("compile-wiki")
    compile_wiki.add_argument("concept_id")
    compile_wiki.add_argument("--result-id", default="")
    compile_wiki.add_argument("--page-type", default="concept", choices=["concept", "topic"])
    publish = sub.add_parser("publish-wiki")
    publish.add_argument("page_id")
    sub.add_parser("wiki-pages")
    sub.add_parser("concept-candidates")
    args = parser.parse_args()

    if args.cmd == "run-study":
        pretty(request_json(args.base_url, "POST", "/study/run", {}))
    elif args.cmd == "pending-promotions":
        q = urllib.parse.urlencode({"action": "promote", "status": "pending_confirm", "limit": 100})
        pretty(request_json(args.base_url, "GET", f"/study/results?{q}"))
    elif args.cmd == "candidate-links":
        pretty(request_json(args.base_url, "GET", "/activation-links?status=candidate&limit=100"))
    elif args.cmd == "confirm-link":
        pretty(request_json(args.base_url, "POST", f"/activation-links/{args.link_id}/confirm", {}))
    elif args.cmd == "reject-link":
        pretty(request_json(args.base_url, "POST", f"/activation-links/{args.link_id}/reject", {}))
    elif args.cmd == "wiki-candidates":
        q = urllib.parse.urlencode({"action": "wiki_candidate", "status": "pending_confirm", "limit": 100})
        pretty(request_json(args.base_url, "GET", f"/study/results?{q}"))
    elif args.cmd == "compile-wiki":
        payload = {"concept_id": args.concept_id, "page_type": args.page_type}
        if args.result_id:
            payload["result_id"] = args.result_id
        pretty(request_json(args.base_url, "POST", "/wiki/compile", payload))
    elif args.cmd == "publish-wiki":
        pretty(request_json(args.base_url, "POST", f"/wiki/pages/{args.page_id}/publish", {}))
    elif args.cmd == "wiki-pages":
        pretty(request_json(args.base_url, "GET", "/wiki/pages?limit=100"))
    elif args.cmd == "concept-candidates":
        pretty(request_json(args.base_url, "GET", "/concepts/candidates?status=pending_confirm"))


if __name__ == "__main__":
    main()
