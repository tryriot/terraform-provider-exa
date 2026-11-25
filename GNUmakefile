default: fmt vet install generate

# Tests run terraform from a temp dir, where the mise shim can't find mise.toml.
export TF_ACC_TERRAFORM_PATH ?= $(shell mise which terraform 2>/dev/null)

build:
	go build -v ./...

install: build
	go install -v ./...

vet:
	go vet ./...

generate:
	go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -timeout=120s -parallel=10 ./...

testacc:
	TF_ACC=1 go test -v -timeout 120m ./...

.PHONY: fmt vet test testacc build install generate
