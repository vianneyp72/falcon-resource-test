VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test ui build image e2e
test:
	go test ./...
ui:
	cd ui && npm ci && npm run build
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/footprint ./cmd/footprint
image:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t falcon-footprint:dev .
e2e:
	./test/e2e.sh
