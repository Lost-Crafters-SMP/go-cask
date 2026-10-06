#!/usr/bin/env python3
"""Profile a repository's recent commit history for Conventional Commits signals.

Descriptive only: this reports what the project's history looks like.
It never decides the type, scope, or wording of any new commit — that
requires understanding the actual change, which is the agent's job.

Usage:  python history_profile.py [repo-path]   (default: current directory)
Output: one compact JSON object on stdout; diagnostics on stderr.
Fails loudly with exit code 1 on any git/parse problem so callers can
fall back to reading `git log --oneline` directly.
"""

import json
import re
import subprocess
import sys

SAMPLE = 200
KNOWN_TYPES = {
    "feat",
    "fix",
    "docs",
    "style",
    "refactor",
    "perf",
    "test",
    "build",
    "ci",
    "chore",
    "revert",
    "improvement",
    "dep",
    "deps",
}
CC_HEADER = re.compile(
    r"^(?P<type>[a-z][a-z0-9_-]*)(?:\((?P<scope>[^)\s]+)\))?(?P<bang>!)?: (?P<desc>\S.*)$"
)
TRAILER = re.compile(r"^(BREAKING CHANGE|[A-Za-z][A-Za-z0-9-]*): \S")
DEPS_SUBJECT = re.compile(
    r"(bump|update|upgrade|pin|downgrade|rollback).{0,40}(dependen|from\b|to\b"
    r"|[/@]|\bv?\d)",
    re.I,
)
BOT_TRAILER = re.compile(r"(renovate\[bot\]|dependabot\[bot\])", re.I)


def git(repo, *args):
    r = subprocess.run(
        ["git", "-C", repo, *args],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if r.returncode != 0:
        sys.stderr.write(r.stderr.strip()[:300])
        sys.exit(1)
    return r.stdout


def profile(repo):
    raw = git(repo, "log", f"-{SAMPLE}", "--format=%x00%s%x00%b%x01")
    commits, merges = [], 0
    for rec in raw.split("\x01"):
        rec = rec.strip("\n").lstrip("\x00")
        if not rec.strip():
            continue
        parts = rec.split("\x00", 1)
        subject = parts[0]
        body = parts[1] if len(parts) > 1 else ""
        if subject.startswith("Merge "):
            merges += 1
            continue
        commits.append((subject, body))

    n = len(commits)
    if n == 0:
        return {
            "sampled": 0,
            "merges": merges,
            "verdict": "unknown",
            "hint": "no non-merge commits to sample",
        }

    types, scopes, upper = {}, {}, 0
    cc = 0
    scope_uses = 0
    bodies = 0
    footers = 0
    breaking = 0
    deps_patterns = {}

    for subject, body in commits:
        m = CC_HEADER.match(subject)
        if m and m.group("type") in KNOWN_TYPES:
            cc += 1
            t = m.group("type")
            types[t] = types.get(t, 0) + 1
            if m.group("scope"):
                scope_uses += 1
                s = m.group("scope")
                scopes[s] = scopes.get(s, 0) + 1
            if m.group("bang"):
                breaking += 1
            desc = m.group("desc")
            if desc[:1].isupper():
                upper += 1
            is_deps = bool(DEPS_SUBJECT.match(desc)) or "deps" in (
                m.group("scope") or ""
            )
            if is_deps:
                pat = t + (f"({m.group('scope')})" if m.group("scope") else "")
                deps_patterns[pat] = deps_patterns.get(pat, 0) + 1
        lines = [l for l in body.splitlines() if l.strip()]
        trailers = [l for l in lines if TRAILER.match(l)]
        non_trailer = [l for l in lines if not TRAILER.match(l)]
        if any("BREAKING CHANGE" in l for l in trailers):
            breaking += 1
        if trailers:
            footers += 1
        if non_trailer:
            bodies += 1
        if BOT_TRAILER.search(body):
            pat_cc = CC_HEADER.match(subject)
            if pat_cc:
                pat = pat_cc.group("type") + (
                    f"({pat_cc.group('scope')})" if pat_cc.group("scope") else ""
                )
                deps_patterns[pat] = deps_patterns.get(pat, 0) + 1

    cc_rate = round(cc / n, 2)
    scope_rate = round(scope_uses / cc, 2) if cc else 0.0
    recurring = {s: c for s, c in scopes.items() if c >= 2}

    if n < 5:
        verdict = "unknown"
    elif cc_rate >= 0.8:
        verdict = "strong"
    elif cc_rate >= 0.4:
        verdict = "mixed"
    else:
        verdict = "none"

    top_scopes = dict(sorted(scopes.items(), key=lambda kv: -kv[1])[:8])
    deps_pattern = (
        sorted(deps_patterns.items(), key=lambda kv: -kv[1])[0][0]
        if deps_patterns
        else None
    )

    hint = {
        "strong": "history is a reliable convention source: mirror its types/scopes/casing",
        "mixed": "conventions exist but are inconsistently applied: follow the dominant pattern, do not invent new vocabulary",
        "none": "history is noise (sloppy/non-conventional): ignore it as a style signal and apply the skill's defaults",
        "unknown": f"only {n} commits sampled: too few to establish a convention, apply the skill's defaults",
    }[verdict]

    out = {
        "sampled": n,
        "cc_match_rate": cc_rate,
        "types": dict(sorted(types.items(), key=lambda kv: -kv[1])),
        "scopes": top_scopes,
        "deps_commit_pattern": deps_pattern,
        "verdict": verdict,
        "hint": hint,
    }
    if merges:
        out["merges_excluded"] = merges
    if cc:
        out["scope_rate"] = scope_rate
        out["scope_vocabulary"] = (
            "established"
            if scope_rate >= 0.5 and len(recurring) >= 2
            else ("occasional" if scopes else "none")
        )
        if upper:
            out["capitalized_subject_rate"] = round(upper / cc, 2)
    if bodies:
        out["body_rate"] = round(bodies / n, 2)
    if footers:
        out["footer_rate"] = round(footers / n, 2)
    if breaking:
        out["breaking_change_commits"] = breaking
    return out


if __name__ == "__main__":
    repo = sys.argv[1] if len(sys.argv) > 1 else "."
    print(json.dumps(profile(repo), indent=None, separators=(", ", ": ")))
