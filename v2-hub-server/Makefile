.PHONY: run test build lint

run:
	go run ./cmd/hub -f etc/hub-server.yaml

test:
	go test ./... -race -count=1

build:
	CGO_ENABLED=0 go build -trimpath -o bin/hub ./cmd/hub

lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; else echo "lint: golangci-lint 未安装, 跳过"; fi
