.PHONY: all check fmt vet build test lint fix modernize bench bench-deep bench-size bench-8x8 bench-count-census bench-memo-census bench-zdd-census bench-split-census bench-zdd-half-census bench-pairs-probe bench-table clean

all: check

check: fmt vet test fix lint

fmt:
	go fmt ./...

vet:
	go vet ./...

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fix: modernize
	golangci-lint run --fix ./...

modernize:
	go fix ./...

comma := ,

# Board and depth selection lives here, not in the benchmark code (ADR-020):
# DEPTH_FILTER translates DEPTHS=a,b into the subtest filter ^depth(a|b)$, an
# empty DEPTHS leaves the full descending sweep of the matched board. The
# anchors matter: -bench segments match unanchored, so without ^...$ a point
# like DEPTHS=1 would also drag in depth10..19.
DEPTH_FILTER = $(if $(DEPTHS),/^depth($(subst $(comma),|,$(DEPTHS)))$$,)

# A full 8x8 descent from depth 32 is many hours per point, so the size8
# targets default to these depths until real measurements fix a proper floor
# (plan 05, ADR-015); DEPTHS overrides the cap with any point.
DEPTH_FILTER_8X8 = $(if $(DEPTHS),$(DEPTH_FILTER),/^depth(32|30)$$)

# target_filter selects the depth filter for one board size argument.
target_filter = $(if $(filter 8,$1),$(DEPTH_FILTER_8X8),$(DEPTH_FILTER))

bench:
	go test -v -run=^$$ -bench='BenchmarkCountAllTours/size[56]' -benchmem -benchtime=10x ./counter/

# Full sweep including the slow 7x7 board: one 7x7 measurement costs ~9 min..hours,
# so the whole run takes days.
bench-deep:
	go test -v -run=^$$ -bench='BenchmarkCountAllTours/size[567]' -benchmem -benchtime=1x -timeout=48h ./counter/

# One board size in its own process — required for a meaningful peakRSS_MB/op
# (it is a per-process maximum). Usage: make bench-size N=<5|6|7|8> [DEPTHS=10,12]
bench-size:
	@if [ -z "$(N)" ]; then echo "usage: make bench-size N=<5|6|7|8> [DEPTHS=10,12]"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkCountAllTours/size$(N)$(call target_filter,$(N))' -benchmem -benchtime=1x -timeout=48h ./counter/

# 8x8 point run (hours per depth, ADR-015): pass DEPTHS explicitly,
# e.g. make bench-8x8 DEPTHS=32; without it the default cap ^depth(32|30)$ runs.
bench-8x8:
	go test -v -run='^$$' -bench='BenchmarkCountAllTours/size8$(DEPTH_FILTER_8X8)' -benchmem -benchtime=1x -timeout=24h ./counter/

# Sampling count-phase census (plan 20): real task-cache, then a fixed sample of class
# tasks with per-task time budgets. Heavy boards run under ../kt-bench.lock like other
# long points. Usage: make bench-count-census N=<5|6|7|8> [DEPTHS=10,12]
bench-count-census:
	@if [ -z "$(N)" ]; then echo "usage: make bench-count-census N=<5|6|7|8> [DEPTHS=10,12]"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkCountCensus/size$(N)$(call target_filter,$(N))' -benchmem -benchtime=1x -timeout=48h ./counter/

# State-reuse census (plan 22): replica count-DFS over a stride sample of the real task-cache,
# states below the cut window folded into one Bloom filter. Heavy boards run under
# ../kt-bench.lock like other long points. Depth selection is the memoCensusPoints table in
# the benchmark code — NOT target_filter: its ADR-015 default (^depth(32|30)$ for size8) names
# depths the census table does not define, which would silently run zero points on 8×8. Only an
# explicit DEPTHS narrows the table here (specs/benchmarks.md «Ценз переиспользования состояний»).
# Usage: make bench-memo-census N=<5|6|7|8> [DEPTHS=10,12]
bench-memo-census:
	@if [ -z "$(N)" ]; then echo "usage: make bench-memo-census N=<5|6|7|8> [DEPTHS=10,12]"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkMemoReuseCensus/size$(N)$(DEPTH_FILTER)' -benchmem -benchtime=1x -timeout=48h ./counter/

# ZDD live-node census (plan 23): reduced-diagram builds of the Hamiltonian families on
# 5×5–7×7 boards, one board per process for an honest peakRSS_MB/op. Heavy points run
# under ../kt-bench.lock like other long points. No DEPTHS: the builder has no cut depth —
# family and order are selected by the {paths|cycles}/{row|narrow} subtest segments and
# the size filter only (specs/benchmarks.md «Ценз живых узлов ZDD», ADR-020).
# Usage: make bench-zdd-census N=<5|6|7>
bench-zdd-census:
	@if [ -z "$(N)" ]; then echo "usage: make bench-zdd-census N=<5|6|7>"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkZDDCensus/size$(N)' -benchmem -benchtime=1x -timeout=48h ./zdd/

# Half-cut split census (plan 26): half-board layer DP down to interface states σ
# and connectivity gluing on 5×5–7×7 boards, one board per process for an honest
# peakRSS_MB/op. Heavy points run under ../kt-bench.lock like other long points.
# No DEPTHS: the half engine has no cut depth — family and seam variant are selected
# by the {paths|cycles}/{straight|mirror} subtest segments and the size filter only
# (specs/benchmarks.md «Ценз разреза пополам со склейкой», ADR-020).
# Usage: make bench-split-census N=<5|6|7>
bench-split-census:
	@if [ -z "$(N)" ]; then echo "usage: make bench-split-census N=<5|6|7>"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkSplitCensus/size$(N)' -benchmem -benchtime=1x -timeout=48h ./split/

# ZDD half census (plan 27): open-seam half-board ZDD builds, σ distributions and
# gluing on 5×5–7×7 boards, one board per process for an honest peakRSS_MB/op.
# Heavy points run under ../kt-bench.lock like other long points.
# No DEPTHS: the open-seam mode has no cut depth — family and seam variant are
# selected by the {paths|cycles}[/mirror] subtest segments and the size filter
# only (specs/benchmarks.md «Ценз ZDD половин», ADR-020).
# Usage: make bench-zdd-half-census N=<5|6|7>
bench-zdd-half-census:
	@if [ -z "$(N)" ]; then echo "usage: make bench-zdd-half-census N=<5|6|7>"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkZDDHalfCensus/size$(N)' -benchmem -benchtime=1x -timeout=48h ./zdd/

# Pair-count probe (plan 28): MITM confirmation of a pairs.Matrix row, one
# board per process for an honest peakRSS_MB/op. Heavy points run under
# ../kt-bench.lock like other long points. No DEPTHS: the cut boundary is the
# k{M} subtest segment and the point table in the benchmark code (plan 28 step 3).
# Usage: make bench-pairs-probe N=<5|6>
bench-pairs-probe:
	@if [ -z "$(N)" ]; then echo "usage: make bench-pairs-probe N=<5|6>"; exit 2; fi
	go test -v -run='^$$' -bench='BenchmarkPairsProbe/size$(N)' -benchmem -benchtime=1x -timeout=24h ./pairs/

# Render a markdown table from a benchmark log: make bench-table LOG=bench.log
bench-table:
	@test -n "$(LOG)" -a -f "$(LOG)" || { echo "usage: make bench-table LOG=<file>"; exit 2; }
	python3 tools/bench_table.py "$(LOG)"

clean:
	go clean -testcache
