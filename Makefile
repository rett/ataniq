.PHONY: bootstrap doctor test test-dev qualification-host-build

bootstrap:
	@sh scripts/go-tool.sh bootstrap

doctor:
	@sh scripts/go-tool.sh doctor

test: doctor
	@sh scripts/go-tool.sh test ./...

test-dev: doctor
	@sh scripts/go-tool.sh test ./tests/...

qualification-host-build: doctor
	@mkdir -p build
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-probe ./qualification/host/cmd/probe
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-qualify ./qualification/host/cmd/qualify
