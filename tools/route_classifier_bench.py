#!/usr/bin/env python3
"""Benchmark router-classifier candidates against labeled routing cases."""

from __future__ import annotations

import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


STRONG_KEYWORDS = {
    "architecture",
    "audit",
    "billing",
    "bgp",
    "concurrency",
    "customer-facing",
    "database",
    "dns",
    "firewall",
    "incident",
    "kubernetes",
    "migration",
    "opnsense",
    "production",
    "refactor",
    "root cause",
    "security",
    "stripe",
    "terraform",
}


@dataclass(frozen=True)
class Case:
    id: str
    expected: str
    prompt: str
    max_tokens: int

    @property
    def prompt_chars(self) -> int:
        return len(self.prompt)


def load_cases(path: Path) -> list[Case]:
    raw_cases = json.loads(path.read_text())
    cases: list[Case] = []
    for raw in raw_cases:
        prompt = str(raw.get("prompt", ""))
        repeat = int(raw.get("repeat", 1))
        cases.append(
            Case(
                id=str(raw["id"]),
                expected=str(raw["expected"]),
                prompt=prompt * repeat,
                max_tokens=int(raw.get("max_tokens", 512)),
            )
        )
    return cases


def guardrail_candidate(case: Case, *, large_prompt_chars: int, high_output_tokens: int) -> tuple[str, float]:
    if case.prompt_chars >= large_prompt_chars or case.max_tokens > high_output_tokens:
        return "strong", 1.0
    return "fast", 0.55


def keyword_candidate(case: Case, *, large_prompt_chars: int, high_output_tokens: int) -> tuple[str, float]:
    label, confidence = guardrail_candidate(
        case,
        large_prompt_chars=large_prompt_chars,
        high_output_tokens=high_output_tokens,
    )
    if label == "strong":
        return label, confidence

    text = case.prompt.lower()
    hits = [
        keyword
        for keyword in STRONG_KEYWORDS
        if keyword in text and not negated_keyword(text, keyword)
    ]
    if hits:
        return "strong", min(0.99, 0.68 + len(hits) * 0.06)
    return "fast", 0.72


def negated_keyword(text: str, keyword: str) -> bool:
    return any(
        phrase in text
        for phrase in (
            f"no {keyword}",
            f"not {keyword}",
            f"non-{keyword}",
            f"without {keyword}",
        )
    )


def llm_system_prompt(targets: list[str]) -> str:
    return (
        "You route coding-agent requests to the cheapest adequate model. "
        "Return only JSON with target_model set to one of: "
        + ", ".join(targets)
        + ". Choose the stronger model for architecture, debugging, security, migrations, "
        "production risk, broad refactors, ambiguous planning, long-context synthesis, or high requested output. "
        "Choose the faster model for routine edits, short questions, status checks, formatting, simple commands, "
        "and low-risk local changes."
    )


def llm_user_prompt(case: Case, targets: list[str]) -> str:
    text = case.prompt.lower()
    if len(text) > 6000:
        text = text[:3000] + "\n...[middle omitted]...\n" + text[-3000:]
    return (
        "/no_think\n"
        f"Allowed targets: {', '.join(targets)}\n"
        f"Prompt chars: {case.prompt_chars}\n"
        f"Requested max output tokens: {case.max_tokens}\n"
        f"Request text:\n{text}\n\n"
        'Return only: {"target_model":"<one allowed target>"}'
    )


def openai_candidate(case: Case, args: argparse.Namespace) -> tuple[str, float, str, str]:
    targets = ["fast", "strong"]
    payload = {
        "model": args.openai_model,
        "messages": [
            {"role": "system", "content": llm_system_prompt(targets)},
            {"role": "user", "content": llm_user_prompt(case, targets)},
        ],
        "temperature": 0,
        "stream": False,
        "max_tokens": args.openai_max_tokens,
    }
    data = json.dumps(payload).encode()
    url = args.openai_base_url.rstrip("/") + "/chat/completions"
    request = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"})
    if args.openai_api_key:
        request.add_header("Authorization", "Bearer " + args.openai_api_key)

    with urllib.request.urlopen(request, timeout=args.timeout) as response:
        body = response.read().decode()

    target, parse_source = parse_target(body, targets)
    return target or "unknown", 1.0 if target else 0.0, body[:2000], parse_source


def parse_target(text: str, targets: list[str]) -> tuple[str | None, str]:
    try:
        payload = json.loads(text)
        choices = payload.get("choices") or []
        combined = "\n".join(
            str(choice.get("text", ""))
            + "\n"
            + str((choice.get("message") or {}).get("content", ""))
            + "\n"
            + str((choice.get("message") or {}).get("reasoning_content", ""))
            for choice in choices
        )
    except json.JSONDecodeError:
        combined = text

    match = re.search(r'\{\s*"target_model"\s*:\s*"([^"]+)"\s*\}', combined)
    if match and match.group(1) in targets:
        return match.group(1), "json"
    for target in targets:
        if target in combined:
            return target, "substring"
    return None, "none"


def run_candidate(name: str, case: Case, args: argparse.Namespace) -> dict[str, Any]:
    started = time.perf_counter()
    error = ""
    raw = ""
    parse_source = ""
    try:
        if name == "guardrails":
            prediction, confidence = guardrail_candidate(
                case,
                large_prompt_chars=args.large_prompt_chars,
                high_output_tokens=args.high_output_tokens,
            )
        elif name == "keywords":
            prediction, confidence = keyword_candidate(
                case,
                large_prompt_chars=args.large_prompt_chars,
                high_output_tokens=args.high_output_tokens,
            )
        elif name == "openai":
            prediction, confidence, raw, parse_source = openai_candidate(case, args)
        else:
            raise ValueError(f"unknown candidate {name!r}")
    except (OSError, TimeoutError, urllib.error.URLError, ValueError) as exc:
        prediction = "error"
        confidence = 0.0
        error = str(exc)

    duration_ms = round((time.perf_counter() - started) * 1000, 3)
    return {
        "case_id": case.id,
        "candidate": name,
        "expected": case.expected,
        "prediction": prediction,
        "correct": prediction == case.expected,
        "confidence": confidence,
        "duration_ms": duration_ms,
        "prompt_chars": case.prompt_chars,
        "max_tokens": case.max_tokens,
        "error": error,
        "parse_source": parse_source,
        "raw_sample": raw,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cases", default="test/bench/router-routing.cases.json")
    parser.add_argument("--candidates", default="guardrails,keywords")
    parser.add_argument("--large-prompt-chars", type=int, default=80000)
    parser.add_argument("--high-output-tokens", type=int, default=8192)
    parser.add_argument("--openai-base-url", default="http://llm-srv-01.mfsoho.linkridge.net:18080/v1")
    parser.add_argument("--openai-model", default="local-coder-fast")
    parser.add_argument("--openai-api-key", default="")
    parser.add_argument("--openai-max-tokens", type=int, default=192)
    parser.add_argument("--timeout", type=float, default=120)
    args = parser.parse_args()

    cases = load_cases(Path(args.cases))
    candidates = [candidate.strip() for candidate in args.candidates.split(",") if candidate.strip()]
    results = [run_candidate(candidate, case, args) for candidate in candidates for case in cases]

    for result in results:
        print(json.dumps(result, separators=(",", ":")))

    for candidate in candidates:
        subset = [result for result in results if result["candidate"] == candidate]
        correct = sum(1 for result in subset if result["correct"])
        total = len(subset)
        avg_ms = sum(float(result["duration_ms"]) for result in subset) / total if total else 0
        print(
            f"summary candidate={candidate} accuracy={correct}/{total} avg_ms={avg_ms:.3f}",
            file=sys.stderr,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
