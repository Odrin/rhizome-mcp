#!/usr/bin/env python3
"""Snapshot the live rhizome-mcp tracker state needed to write an execution plan.

Prints (markdown):
  1. non-terminal issues grouped by epic, with status/priority/blocker count/claimability and
     whether an "Execution notes" comment already exists;
  2. live `blocks` edges between non-terminal issues;
  3. the claimable pick order an orchestrator will see (priority, then sequence number);
  4. per-epic child summary.

Uses only the read-only CLI (`rhizome-mcp issue list|graph|search`), so it needs the binary on PATH
(built from the current checkout). Run from the repository root:

    python3 .github/skills/rhizome-execution-plan/scripts/plan_snapshot.py [--notes] [--json]

  --notes   also print the body of every existing "Execution notes" comment (search snippets are
            capped at 1000 runes; a note that hits the cap is marked TRUNCATED — read the rest with
            the MCP get_work_context tool when it matters).
  --json    machine-readable output instead of markdown.
Forward extra global flags (e.g. --data-root) through the RHIZOME_ARGS environment variable.
"""
import re
import json
import os
import subprocess
import sys
from collections import defaultdict

PRIORITY_RANK = {"critical": 0, "high": 1, "medium": 2, "low": 3}
TERMINAL = {"done", "cancelled"}
NOTE_MARKER = "Execution notes"


def cli(*args):
    extra = os.environ.get("RHIZOME_ARGS", "").split()
    cmd = ["rhizome-mcp", *extra, *args, "--format", "json"]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(f"{' '.join(cmd)} failed: {proc.stderr.strip()}\n")
        if "schema is newer" in proc.stderr:
            sys.stderr.write("hint: rebuild the binary from the current checkout (CGO_ENABLED=0 go build -o rhizome-mcp .)\n")
        sys.exit(1)
    return json.loads(proc.stdout)


def list_all(statuses):
    items, cursor = [], None
    while True:
        args = ["issue", "list", "--limit", "100"]
        for s in statuses:
            args += ["--status", s]
        if cursor:
            args += ["--cursor", cursor]
        page = cli(*args)
        items += page["items"]
        if not page.get("has_more"):
            return items
        cursor = page["next_cursor"]


def blocks_edges(epic_ids, by_id):
    edges = set()
    for root in epic_ids:
        graph = cli("graph", root, "--depth", "5", "--max-nodes", "500", "--include-hierarchy",
                    "--direction", "both", "--include-terminal")
        names = {n["ID"]: n["DisplayID"] for n in graph["nodes"]}
        for e in graph["edges"]:
            if e["type"] != "blocks":
                continue
            s, t = names.get(e["source_issue_id"]), names.get(e["target_issue_id"])
            if s in by_id and t in by_id:
                edges.add((s, t))
    return sorted(edges, key=lambda st: (int(st[1].split("-")[1]), int(st[0].split("-")[1])))


def notes_by_issue():
    """Map issue ULID -> list of (plan_date, body, truncated) for Execution notes comments."""
    found, cursor = defaultdict(list), None
    while True:
        args = ["search", NOTE_MARKER, "--limit", "100", "--entity-type", "comment", "--snippet-length", "1000"]
        if cursor:
            args += ["--cursor", cursor]
        page = cli(*args)
        for item in page.get("results", page.get("items", [])):
            body = re.sub(r"\[([^\]]*)\]", r"\1", item.get("snippet") or "")  # strip FTS hit markers
            if NOTE_MARKER.lower() not in body.lower():
                continue
            date = re.search(r"plan (\d{4}-\d{2}-\d{2})", body)
            found[item.get("issue_id")].append((date.group(1) if date else "", body.strip(), len(body) >= 990))
        if not page.get("has_more"):
            return found
        cursor = page["next_cursor"]


def recently_closed(limit=25):
    done = list_all(["done", "cancelled"])
    done.sort(key=lambda i: i.get("updated_at") or "", reverse=True)
    return done[:limit]


def git_status():
    proc = subprocess.run(["git", "status", "--short"], capture_output=True, text=True)
    return proc.stdout.strip() if proc.returncode == 0 else ""


def main():
    as_json = "--json" in sys.argv
    with_notes = "--notes" in sys.argv
    open_items = list_all(["open", "ready", "blocked", "review"])
    by_id = {i["display_id"]: i for i in open_items}
    ulid_to_display = {i["id"]: i["display_id"] for i in open_items}
    epics = [i for i in open_items if i["type"] == "epic"]
    notes = notes_by_issue()
    noted_ulids = set(notes)
    plan_dates = sorted({d for entries in notes.values() for d, _, _ in entries if d})
    closed = recently_closed()
    in_review = [i for i in open_items if i["status"] == "review"]
    dirty = git_status()

    children = defaultdict(list)
    orphans = []
    for i in open_items:
        if i["type"] == "epic":
            continue
        parent = ulid_to_display.get(i.get("parent_id"))
        (children[parent] if parent else orphans).append(i)

    edges = blocks_edges([e["display_id"] for e in epics] + [i["display_id"] for i in orphans], by_id)
    blocked_by = defaultdict(list)
    unblocks = defaultdict(list)
    for s, t in edges:
        blocked_by[t].append(s)
        unblocks[s].append(t)

    # The orchestrator loop filters status=ready, so review-status items are listed separately.
    claimable = sorted((i for i in open_items if i.get("is_claimable") and i["status"] == "ready"),
                       key=lambda i: (PRIORITY_RANK[i["priority"]], i["sequence_no"]))

    def row(i):
        flags = []
        if blocked_by.get(i["display_id"]):
            flags.append("blocked by " + ", ".join(blocked_by[i["display_id"]]))
        elif i["status"] == "blocked":
            flags.append("status blocked (on hold / manual)")
        if i.get("is_claimable"):
            flags.append("claimable")
        if unblocks.get(i["display_id"]):
            flags.append("unblocks " + ", ".join(unblocks[i["display_id"]]))
        note = "note" if i["id"] in noted_ulids else "NO NOTE"
        return f"| {i['display_id']} | {i['type']} | {i['status']} | {i['priority']} | {note} | {'; '.join(flags)} | {i['title'][:80]} |"

    if as_json:
        print(json.dumps({
            "issues": open_items, "blocks": edges,
            "claimable_order": [i["display_id"] for i in claimable],
            "issues_with_notes": sorted(ulid_to_display[u] for u in noted_ulids if u in ulid_to_display),
            "notes": {ulid_to_display[u]: [b for _, b, _ in v] for u, v in notes.items() if u in ulid_to_display},
            "plan_dates": plan_dates, "in_review": [i["display_id"] for i in in_review],
            "recently_closed": [{"id": i["display_id"], "title": i["title"], "updated_at": i.get("updated_at")} for i in closed],
            "git_status": dirty,
        }, indent=1))
        return

    print(f"# Tracker snapshot — {len(open_items)} non-terminal issues ({len(epics)} epics)\n")
    print(f"Previous plan dates found in notes: {', '.join(plan_dates) or 'none'}")
    last_plan = plan_dates[-1] if plan_dates else None
    closed_since = [i for i in closed if last_plan and (i.get("updated_at") or "") >= last_plan]
    print(f"Closed since the last plan ({last_plan or 'n/a'}): {', '.join(i['display_id'] for i in closed_since) or 'none'}")
    print("Recently closed (newest first): " + ", ".join(f"{i['display_id']} ({(i.get('updated_at') or '')[:10]})" for i in closed[:12]))
    if dirty:
        print(f"\nUncommitted working-tree changes (may belong to an open issue — check before routing):\n```\n{dirty}\n```")
    print()
    header = "| issue | type | status | priority | notes | flags | title |\n|---|---|---|---|---|---|---|"
    for epic in sorted(epics, key=lambda e: e["sequence_no"]):
        kids = sorted(children.get(epic["display_id"], []), key=lambda i: i["sequence_no"])
        print(f"## {epic['display_id']} {epic['title'][:90]} — {epic['status']}, {len(kids)} open children\n")
        if kids:
            print(header)
            for k in kids:
                print(row(k))
            print()
    if orphans:
        print("## Without epic\n")
        print(header)
        for k in sorted(orphans, key=lambda i: i["sequence_no"]):
            print(row(k))
        print()

    print("## Live `blocks` edges among non-terminal issues\n")
    for s, t in edges:
        print(f"- {s} blocks {t}")
    print("\n## Claimable pick order (priority, then sequence) — what the orchestrator sees\n")
    for n, i in enumerate(claimable, 1):
        print(f"{n}. {i['display_id']} {i['priority']} — {i['title'][:80]}")
    if in_review:
        print("\n## In `review` (maintainer sign-off; excluded from the orchestrator's ready loop)\n")
        for i in in_review:
            print(f"- {i['display_id']} {i['priority']} — {i['title'][:80]}")
    missing = [i["display_id"] for i in open_items if i["type"] != "epic" and i["id"] not in noted_ulids]
    print(f"\n## Items without an Execution notes comment: {', '.join(missing) or 'none'}")
    if with_notes:
        print("\n## Existing Execution notes (latest search snippet per issue; ≤1000 runes)\n")
        for i in sorted(open_items, key=lambda x: x["sequence_no"]):
            for date, body, truncated in notes.get(i["id"], []):
                print(f"### {i['display_id']} (plan {date or '?'}){' — TRUNCATED, read via get_work_context' if truncated else ''}\n")
                print(body + "\n")


if __name__ == "__main__":
    main()
