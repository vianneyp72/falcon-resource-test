VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test ui build image e2e
test:
	go test ./...
ui:
	cd ui && npm ci && npm run build
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/falcon-resource-test ./cmd/falcon-resource-test
image:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t falcon-resource-test:dev .
e2e:
	./test/e2e.sh
