#!/usr/bin/env python3
"""Extract token usage (and cost, if the agent reports it) from an agent run.

Usage: runner/usage.py <workspace>  -> prints a JSON object, {} if nothing found.
Understands Claude Code --output-format json (total_cost_usd, usage), Codex
--json events (turn.completed usage), and Gemini -o json (stats, best effort).
"""
import json
import os
import sys

ws = sys.argv[1]
g = os.path.join(ws, ".gauntlet")
out = {}


def add(key, v):
    if isinstance(v, (int, float)):
        out[key] = out.get(key, 0) + v


p = os.path.join(g, "agent-output.json")
if os.path.exists(p):
    try:
        d = json.load(open(p))
    except Exception:
        d = {}
    if isinstance(d, dict):
        if "total_cost_usd" in d:
            add("costUSD", d["total_cost_usd"])
        u = d.get("usage") or {}
        for k in ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"):
            add(k, u.get(k))
        stats = d.get("stats")
        if isinstance(stats, dict):  # Gemini: walk for token counters
            def walk(x):
                if isinstance(x, dict):
                    for k, v in x.items():
                        if isinstance(v, (int, float)) and "token" in k.lower():
                            add("gemini_" + k, v)
                        else:
                            walk(v)
            walk(stats)

p = os.path.join(g, "agent-events.jsonl")
if os.path.exists(p):
    for line in open(p):
        try:
            e = json.loads(line)
        except Exception:
            continue
        if e.get("type") == "turn.completed":
            for k, v in (e.get("usage") or {}).items():
                add(k, v)

print(json.dumps(out))
