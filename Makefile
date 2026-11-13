.PHONY: bootstrap doctor test test-dev

bootstrap:
	@sh scripts/go-tool.sh bootstrap

doctor:
	@sh scripts/go-tool.sh doctor

test: doctor
	@sh scripts/go-tool.sh test ./...

test-dev: doctor
	@sh scripts/go-tool.sh test ./tests/...
