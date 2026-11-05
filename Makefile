GO ?= go
GORELEASER ?= goreleaser
VERSION ?= dev
GOFLAGS := -buildvcs=false -trimpath

.PHONY: build test check compare dist

build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags '-s -w -X main.version=$(VERSION)' -o bin/repo-health .

test:
	$(GO) test -buildvcs=false -race ./...
	python3 -B -m unittest discover -s reference -p test_repo_health.py

check:
	$(GO) vet -buildvcs=false ./...
	test -z "$$(gofmt -l *.go)"

compare: build
	python3 -B scripts/compare.py

dist:
	$(GORELEASER) check
	$(GORELEASER) release --snapshot --clean --skip=publish
