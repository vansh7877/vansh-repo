.PHONY: run build test bench sample demo docker

run:
	go run ./cmd/fraudlens

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/fraudlens ./cmd/fraudlens

test:
	go test ./...

bench:
	go test ./internal/engine -run xxx -bench . -benchmem

# 60 days × 80 CSPs of synthetic data with every fraud pattern injected.
sample:
	mkdir -p testdata
	go run ./cmd/gen -out testdata/sample.csv

# Stream synthetic transactions to a running server's webhook.
demo:
	go run ./cmd/gen -post http://localhost:8080/api/v1/transactions -rate 300

docker:
	docker build -t fraudlens .
