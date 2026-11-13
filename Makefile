.PHONY: bootstrap doctor test test-dev

bootstrap:
	@command -v go >/dev/null || { echo 'missing: Go bootstrap driver' >&2; exit 1; }
	GOENV=off GOWORK=off GOTOOLCHAIN=go$(shell cat .go-version)+auto go version
	GOENV=off GOWORK=off GOTOOLCHAIN=go$(shell cat .go-version)+auto go mod download

doctor:
	@sh scripts/go-tool.sh doctor

test:
	@sh scripts/go-tool.sh test ./...

test-dev:
	@sh scripts/go-tool.sh test ./tests/...
