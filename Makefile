.PHONY: all check fmt vet build test lint fix modernize bench bench-oracle-full clean

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

bench-oracle-full:
	ORACLE_FULL_SWEEP=1 go test -v -run=^$$ -bench=Oracle -benchmem -benchtime=1x -timeout=90m ./counter/

clean:
	go clean -testcache
