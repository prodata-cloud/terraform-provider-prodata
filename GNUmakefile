default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	cd tools; go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

# Sweepers remove leaked acceptance-test resources (those carrying the "tfacc-"
# name prefix). Requires the same PRODATA_* credentials as the acceptance tests.
SWEEP ?= prodata
sweep:
	go test ./internal/provider/... -v -timeout 60m -sweep=$(SWEEP) -sweep-run=$(SWEEP_RUN)

.PHONY: fmt lint test testacc sweep build install generate
