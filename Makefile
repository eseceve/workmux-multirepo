.PHONY: build test smoke lint fmt tidy

build:
	go build -o bin/wmm ./cmd/wmm
	go build -o bin/workmux-multirepo ./cmd/workmux-multirepo

test:
	go test ./... -race -cover

smoke:
	WMM_SMOKE=1 go test ./internal/cli -run TestRealWorkmuxSmoke -v -count=1

lint:
	go vet ./...

fmt:
	gofmt -w cmd internal

tidy:
	go mod tidy
