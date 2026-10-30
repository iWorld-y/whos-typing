.PHONY: build run

build:
	@mkdir -p output
	@CGO_ENABLED=0 GOWORK=off go build -o output/ ./cmd/whos-typing ./cmd/whos-typing-cli

run: build
	@./output/whos-typing
