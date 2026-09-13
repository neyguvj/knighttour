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

bench:
	go test -v -run=^$$ -bench=. -benchmem -benchtime=10x ./counter/

# Full sweep including the gated 7x7 board: one 7x7 measurement costs ~9 min..hours,
# so the whole run takes days.
bench-deep:
	BENCH_DEEP=1 go test -v -run=^$$ -bench=. -benchmem -benchtime=1x -timeout=48h ./counter/

# One board size in its own process — required for a meaningful peakRSS_MB/op
# (it is a per-process maximum). Usage: make bench-size N=7 [DEPTHS=20,22]
bench-size:
	@if [ -z "$(N)" ]; then echo "usage: make bench-size N=<5|6|7|8> [DEPTHS=10,12]"; exit 2; fi
	env BENCH_DEEP=$(if $(filter 7,$(N)),1,) BENCH_8X8=$(if $(filter 8,$(N)),1,) \
		BENCH_DEPTHS="$(DEPTHS)" \
		go test -v -run='^$$' -bench=CountAllTours/size$(N) -benchmem -benchtime=1x -timeout=48h ./counter/

# Gated 8x8 point run (hours per depth, ADR-015): pass DEPTHS explicitly,
# e.g. make bench-8x8 DEPTHS=32
bench-8x8:
	BENCH_8X8=1 BENCH_DEPTHS="$(DEPTHS)" go test -v -run='^$$' \
		-bench=CountAllTours/size8 -benchmem -benchtime=1x -timeout=24h ./counter/

# Render a markdown table from a benchmark log: make bench-table LOG=bench.log
bench-table:
	@test -n "$(LOG)" -a -f "$(LOG)" || { echo "usage: make bench-table LOG=<file>"; exit 2; }
	python3 tools/bench_table.py "$(LOG)"

clean:
	go clean -testcache
