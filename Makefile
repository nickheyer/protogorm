.PHONY: gen build test lint

BUF_IMAGE := bufbuild/buf:latest
BUF_RUN := docker run --rm \
	--volume "$(shell pwd):/workspace" \
	--workdir /workspace \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	$(BUF_IMAGE)

gen:
	rm -rf gen
	$(BUF_RUN) generate

build:
	go build ./...

test:
	go test ./...

lint:
	$(BUF_RUN) lint
	go vet ./...
