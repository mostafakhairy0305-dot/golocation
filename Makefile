.PHONY: test race cover cover-html vet crosscheck all

all: vet test cover

test:
	go test ./...

race:
	go test -race ./...

# The per-package coverage gate. scripts/cover.sh owns the floor and the
# exclusions; CI runs the same script.
cover:
	./scripts/cover.sh

cover-html:
	./scripts/cover.sh --html

vet:
	go vet ./...

# The three native adapters compile only on their own operating system, so a
# darwin-only vet says nothing about two thirds of the provider feature.
crosscheck:
	GOOS=linux go vet ./...
	GOOS=windows go vet ./...
	GOOS=darwin go vet ./...
