#!/usr/bin/env python3
"""prefix_extrapolate.py — prefix-count curve from bench logs: fit, extrapolate, backtest.

Usage:
  python3 tools/prefix_extrapolate.py --size N [--to D] [--window W] [--holdout K]
      [--rss-from LOG2 --rss-size M --bpe BYTES] LOG [LOG ...]

Parses Go bench output (BenchmarkGenCensus / BenchmarkCountAllTours subtests named
size{N}/depth{D}) for the target board, reads writesB/op (prefix count P(d)), fits
ln r(d) = ln P(d)/P(d-1) linearly over the last W measured points, and extrapolates
P(d), gen-B wall time (calibrated ns-per-write from the same log) and — when a
reference full-run log is given — task-cache RSS via density transfer at equal
depth: E(d) = P(d)/density_ref(d), RSS ≈ E × bpe. --holdout K refits on points
below max-K and reports prediction error on the withheld tail (the model's honest
band). Output: markdown table, measured rows flagged M. Stdlib only.
"""

import argparse
import math
import re
import sys

LINE = re.compile(
    r"Benchmark\w*/size(\d+)/depth(\d+)-?\d*\s+\d+\s+(\S+)\s+ns/op\s+(.*)")


def parse_log(path, size):
    """Return {depth: (writesB, genB_ns, taskEntries|None)} for one board size."""
    rows = {}
    with open(path, encoding="utf-8") as fh:
        for ln in fh:
            m = LINE.match(ln)
            if not m or int(m.group(1)) != size:
                continue
            depth = int(m.group(2))
            ns = float(re.sub(r"[^0-9.eE+-]", "", m.group(3)))
            toks = m.group(4).split()
            met = {}
            for i in range(1, len(toks), 2):
                try:
                    met[toks[i]] = float(toks[i - 1])
                except ValueError:
                    continue
            if "writesB/op" not in met:
                continue
            rows[depth] = (met["writesB/op"], ns, met.get("taskEntries/op"))
    return rows


def fit_ln_r(ds, ps, window):
    """Least squares of ln r on depth over the last `window` ratios.

    Returns (a, b) with ln r(d) ≈ a + b·d, or None when fewer than 2 points.
    """
    pts = [(d, math.log(ps[d] / ps[d - 1]))
           for d in ds if d - 1 in ps and d >= max(ds) - window + 1]
    if len(pts) < 2:
        return None
    n = len(pts)
    sx = sum(p[0] for p in pts)
    sy = sum(p[1] for p in pts)
    sxx = sum(p[0] * p[0] for p in pts)
    sxy = sum(p[0] * p[1] for p in pts)
    b = (n * sxy - sx * sy) / (n * sxx - sx * sx)
    return ((sy - b * sx) / n, b)


def main():
    ap = argparse.ArgumentParser(
        prog="prefix_extrapolate.py", description=__doc__.splitlines()[0])
    ap.add_argument("--size", type=int, required=True, help="target board size")
    ap.add_argument("--to", type=int, default=0,
                    help="extrapolate up to depth (default size²/2)")
    ap.add_argument("--window", type=int, default=6,
                    help="measured ratios for the ln r fit")
    ap.add_argument("--holdout", type=int, default=0,
                    help="withhold the top K depths from the fit for backtest")
    ap.add_argument("--rss-from",
                    help="reference full-run log (taskEntries/op present)")
    ap.add_argument("--rss-size", type=int,
                    help="board size of the reference log")
    ap.add_argument("--bpe", type=float, default=33.0,
                    help="bytes per class-task entry (default 33)")
    ap.add_argument("logs", nargs="+")
    args = ap.parse_args()

    rows = {}
    for lg in args.logs:
        rows.update(parse_log(lg, args.size))
    if len(rows) < 3:
        print(f"error: fewer than 3 measured depths for size{args.size}",
              file=sys.stderr)
        return 1

    dens = None
    if args.rss_from:
        ref = parse_log(args.rss_from, args.rss_size)
        dens = {d: (w / e) for d, (w, _, e) in ref.items() if e} or None
        if dens is None:
            print("error: reference log has no taskEntries/op", file=sys.stderr)
            return 1

    ds = sorted(rows)
    ps = {d: rows[d][0] for d in ds}
    to = args.to or args.size * args.size // 2

    hold = set(ds[-args.holdout:]) if args.holdout else set()
    fit_ds = [d for d in ds if d not in hold]
    fit = fit_ln_r(fit_ds, ps, min(args.window, len(fit_ds)))
    if fit is None:
        print("error: need at least 2 consecutive depths to fit r(d)",
              file=sys.stderr)
        return 1

    a, b = fit
    tail = ds[-3:]
    cal_ns = sum(rows[d][1] for d in tail) / sum(ps[d] for d in tail)
    dens_d = max(dens) if dens else None

    def density(d):
        """Density transfer at equal depth; geometric extension past the range."""
        if not dens:
            return None
        if d in dens:
            return dens[d]
        if d < min(dens):
            return dens[min(dens)]
        steps = [dens[k] / dens[k - 1] for k in sorted(dens) if k - 1 in dens]
        g = sum(steps[-3:]) / len(steps[-3:])
        v = dens[dens_d]
        for _ in range(d - dens_d):
            v *= g
        return v

    print(f"fit ln r = {a:.4f} {b:+.4f}·d  (window {min(args.window, len(fit_ds))}, "
          f"calibration {cal_ns:.0f} ns/write wall)")
    print("| d | P(d) | r(d) | gen_s | RSS_GB | M/E |")
    print("|---|---|---|---|---|---|")
    prev = None
    for d in range(min(ds), to + 1):
        if d in ps:
            p, flag = ps[d], "M"
        elif prev is not None:
            p, flag = prev * math.exp(a + b * d), "E"
        else:
            continue
        rcol = f"{p / prev:.3f}" if prev else "-"
        rss = ""
        if dens:
            rss = f"{p / density(d) * args.bpe / 1e9:.1f}"
        print(f"| {d} | {p:.4g} | {rcol} | {p*cal_ns/1e9:.3g} | {rss} | {flag} |")
        prev = p

    if hold:
        print("\nbacktest on withheld depths:")
        for d in sorted(hold):
            pred = ps[d - 1] * math.exp(a + b * d) if d - 1 in ps else float("nan")
            err = (pred / ps[d] - 1) * 100
            print(f"  d{d}: predicted {pred:.4g} vs actual {ps[d]:.4g} ({err:+.1f}%)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
