#!/usr/bin/env python3
"""Run the 10 /help questions against the real web application, incrementally.

No credentials are read here: the running server owns the model configuration.
Gold expectations are used only AFTER the request, never sent to the model.
"""
import argparse
import hashlib
import http.cookiejar
import json
from pathlib import Path
import subprocess
import sys
import time
from datetime import datetime, timezone
from urllib.request import Request, build_opener, HTTPCookieProcessor
from urllib.error import HTTPError


def now():
    return datetime.now(timezone.utc).isoformat()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--url", default="http://127.0.0.1:8080")
    p.add_argument("--questions", default="docs/help-questions.json")
    p.add_argument("--output", default="artifacts/help31")
    p.add_argument("--model", default="")
    p.add_argument("--resume", action="store_true")
    args = p.parse_args()
    root = Path(__file__).resolve().parents[1]
    dataset = (root / args.questions).read_bytes()
    questions = json.loads(dataset)
    if len(questions) != 10:
        raise RuntimeError("Expected exactly 10 questions")
    dataset_hash = hashlib.sha256(dataset).hexdigest()
    branch = subprocess.check_output(["git", "branch", "--show-current"], cwd=root, text=True).strip()
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    target = root / args.output
    target.mkdir(parents=True, exist_ok=True)
    path = target / "results.json"
    if path.exists():
        if not args.resume:
            raise RuntimeError("Report exists; use --resume or a new --output")
        report = json.loads(path.read_text())
        if (report["datasetHash"], report["branch"], report["commit"], report["url"], report["requestedModel"]) != (dataset_hash, branch, commit, args.url, args.model):
            raise RuntimeError("Cannot resume a different experiment")
    else:
        report = dict(startedAt=now(), complete=False, datasetHash=dataset_hash,
                      branch=branch, commit=commit, url=args.url,
                      requestedModel=args.model, cases=[], failures=[])

    def save():
        tmp = target / "results.tmp"
        tmp.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        tmp.replace(path)

    done = {case["question"]["id"] for case in report["cases"]}
    for q in questions:
        if q["id"] in done:
            continue
        opener = build_opener(HTTPCookieProcessor(http.cookiejar.CookieJar()))
        body = {"message": "/help " + q["query"]}
        if args.model:
            body["model"] = args.model
        request = Request(args.url.rstrip("/") + "/api/chat",
                          data=json.dumps(body).encode(),
                          headers={"Content-Type": "application/json", "X-Codex-Chat": "1"})
        start = time.monotonic()
        try:
            with opener.open(request, timeout=420) as response:
                value = json.load(response)
        except HTTPError as exc:
            # Keep failed attempts in the report; a resumed run does not hide them.
            report["failures"].append(dict(id=q["id"], at=now(), status=exc.code, response=exc.read().decode("utf-8", "replace")))
            save()
            raise RuntimeError(f"{q['id']}: HTTP {exc.code}; failure saved") from None
        messages = value.get("messages", [])
        assistant = next(m for m in reversed(messages) if m["role"] == "assistant")
        retrieval = assistant.get("retrieval", {})
        grounding = retrieval.get("grounding", {})
        sources = grounding.get("sources", [])
        answer = value.get("answer", "")
        lower = answer.lower()
        checks = {
            "projectMode": bool(retrieval.get("project")),
            "mcpSuccess": any(r["name"] == "git_branch" and not r["error"] for r in assistant.get("toolRuns", [])),
            "noInvalidCitations": not retrieval.get("invalidCitations"),
            "expectedFacts": all(any(term.lower() in lower for term in alternatives) for alternatives in q["requiredFacts"]),
        }
        if q.get("unknown"):
            checks["honestUnknown"] = grounding.get("status") == "unknown" and "не знаю" in lower
        else:
            checks["sourcesPresent"] = bool(sources)
            checks["quotesPresent"] = bool(grounding.get("quotes"))
            checks["expectedSource"] = any(s["source"] in q["sources"] for s in sources)
            checks["supportedClaims"] = bool(grounding.get("checks")) and all(c["verdict"] == "supported" for c in grounding["checks"])
        if q.get("liveGit"):
            checks["liveBranchAndCommit"] = branch in answer and commit in answer
        case = dict(question=q, seconds=round(time.monotonic()-start, 3),
                    model=value.get("model"), answer=answer, metrics=value.get("metrics"),
                    retrieval=retrieval, toolRuns=assistant.get("toolRuns"),
                    checks=checks, passed=all(checks.values()))
        report["cases"].append(case)
        save()
        print(q["id"], "PASS" if case["passed"] else "FAIL", case["seconds"], flush=True)
    report["complete"] = len(report["cases"]) == 10
    report["completedAt"] = now()
    report["passed"] = report["complete"] and all(c["passed"] for c in report["cases"])
    save()
    if not report["passed"]:
        return 1
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
