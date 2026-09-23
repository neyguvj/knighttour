#!/usr/bin/env python3
"""Render `go test -bench` output into markdown tables, one per board size.

Usage: make bench-table LOG=bench.log  (or: python3 tools/bench_table.py bench.log)

Parses lines like:
    BenchmarkCountAllTours/size6/depth13-14   10   68600000 ns/op   22.1 cnt_ms/op ...
Groups by `size{N}`, sorts depths descending, and prints a markdown table with
counters humanized to K/M/G and durations (`ns/op`, `genA_ms/op`, …) on the
readable s/ms/µs/ns scale.
"""

import re
import sys

BENCH_RE = re.compile(r"^(Benchmark\w+)/size(\d+)/depth(\d+)-\d+\s+")

SUFFIX = [(1e9, "G"), (1e6, "M"), (1e3, "K")]

# Nanosecond multiplier per time unit; a unit's time stem is the last
# `_`-separated segment before `/op` (`ns/op`, `cnt_ms/op`, `genB_ms/op`).
TIME_UNITS = {"ns": 1, "µs": 1_000, "ms": 1_000_000, "s": 1_000_000_000}


def format_ns(ns: float) -> str:
    """Render a nanosecond duration on the readable s/ms/µs/ns scale."""
    for limit, suffix in [(1e9, "s"), (1e6, "ms"), (1e3, "µs")]:
        if abs(ns) >= limit:
            return f"{ns / limit:.2f} {suffix}"
    return f"{ns:.0f} ns"


def humanize(value: float, unit: str) -> str:
    """Render a metric with K/M/G scaling; time units get their own scale."""
    stem = unit.removesuffix("/op").rsplit("_", 1)[-1]
    if stem in TIME_UNITS:
        return format_ns(value * TIME_UNITS[stem])
    if unit.endswith("/op"):
        for limit, suffix in SUFFIX:
            if abs(value) >= limit:
                return f"{value / limit:.2f}{suffix}"
        return f"{value:.1f}" if value != int(value) else f"{int(value)}"
    return f"{value:g} {unit}"


def parse(text: str):
    """Yield (size, depth, [(value, unit), ...]) for every benchmark line."""
    for line in text.splitlines():
        match = BENCH_RE.match(line)
        if not match:
            continue
        size, depth = int(match.group(2)), int(match.group(3))
        tokens = line[match.end():].split()
        if len(tokens) < 3 or not tokens[0].isdigit():  # "<iters> <value> <unit> …"
            continue
        pairs = []
        for i in range(1, len(tokens) - 1, 2):
            try:
                pairs.append((float(tokens[i]), tokens[i + 1]))
            except ValueError:
                break
        if pairs:
            yield size, depth, pairs


def render(entries):
    """Print one markdown table per board size, depths descending."""
    by_size = {}
    for size, depth, pairs in entries:
        by_size.setdefault(size, []).append((depth, {u: v for v, u in pairs}))

    units_seen = {}
    for rows in by_size.values():
        for _, metrics in rows:
            for unit in metrics:
                units_seen.setdefault(unit, None)
    units = list(units_seen)

    for size in sorted(by_size, reverse=True):
        rows = sorted(by_size[size], key=lambda r: -r[0])
        print(f"\n### {size}×{size}\n")
        print("| depth | " + " | ".join(units) + " |")
        print("|---" * (len(units) + 1) + "|")
        for depth, metrics in rows:
            cells = [humanize(metrics[u], u) if u in metrics else "" for u in units]
            print(f"| {depth} | " + " | ".join(cells) + " |")


def main(argv):
    if len(argv) != 2:
        sys.exit(__doc__)
    with open(argv[1], encoding="utf-8", errors="replace") as handle:
        entries = list(parse(handle.read()))
    if not entries:
        sys.exit("no benchmark lines found in " + argv[1])
    render(entries)


if __name__ == "__main__":
    main(sys.argv)
