.PHONY: bootstrap doctor test test-dev test-patched qualification-host-build changelog-fold changelog-check

# The patched charmbracelet modules in third_party/patched are directory
# replacements, so ./... does not reach them; they are tested by package name
# from the main module, which gives them its dependency set. The packages run
# are x/vt, x/ansi and its parser, kitty and iterm2 subpackages. The x/ansi
# sixel package is not listed: it needs a module that is not cached.
PATCHED_PACKAGES = github.com/charmbracelet/x/vt github.com/charmbracelet/x/ansi github.com/charmbracelet/x/ansi/parser github.com/charmbracelet/x/ansi/kitty github.com/charmbracelet/x/ansi/iterm2

bootstrap:
	@sh scripts/go-tool.sh bootstrap

doctor:
	@sh scripts/go-tool.sh doctor

test: doctor
	@sh scripts/go-tool.sh test ./...
	@sh scripts/go-tool.sh test $(PATCHED_PACKAGES)

test-patched: doctor
	@sh scripts/go-tool.sh test $(PATCHED_PACKAGES)

test-dev: doctor
	@sh scripts/go-tool.sh test ./tests/...

qualification-host-build: doctor
	@mkdir -p build
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-probe ./qualification/host/cmd/probe
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-qualify ./qualification/host/cmd/qualify

# Changelog fragments: changelog.d/<section>-<slug>.md are folded into
# CHANGELOG.md in the release branch's first commit, and the release checks
# that none are left. ARGS=--dry-run previews the fold. Neither target needs
# the Go toolchain.
changelog-fold:
	@sh scripts/changelog-fold.sh $(ARGS)

changelog-check:
	@sh scripts/changelog-fold.sh --check
