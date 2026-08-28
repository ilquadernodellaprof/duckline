# Build di produzione: identica a quella eseguita da GitHub Actions.
.PHONY: build
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/duckline .

.PHONY: build-local
build-local:
	go build -o bin/duckline .
