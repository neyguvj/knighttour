.PHONY: all check fmt vet build test lint fix modernize bench clean

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

clean:
	go clean -testcache
