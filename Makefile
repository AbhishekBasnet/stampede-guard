.PHONY: build test race vet fmt e2e up down experiment clean

build:
	go build ./...

test:
	go test ./...

race: vet
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

e2e:
	go test -tags e2e ./internal/e2e/ -v -count=1

up:
	./scripts/up.sh

down:
	./scripts/down.sh

experiment:
	go run ./cmd/experiment -scenario $(or $(SCENARIO),all)

clean:
	rm -rf .run data results
