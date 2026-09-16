.PHONY: all check fmt vet build test lint fix modernize bench bench-deep bench-size bench-8x8 bench-table clean

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

# Render a markdown table from a benchmark log: make bench-table LOG=bench.log
bench-table:
	@test -n "$(LOG)" -a -f "$(LOG)" || { echo "usage: make bench-table LOG=<file>"; exit 2; }
	python3 tools/bench_table.py "$(LOG)"

clean:
	go clean -testcache
