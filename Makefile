.PHONY: bootstrap doctor test test-canary test-dev test-patched qualification-host-build qualification-leadsandbox-build changelog-fold changelog-check

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

# The tests of the code behind the canary build tag, which make test does not
# build. They run with the tag, over every package that holds a Go file whose
# build constraint names it, found here by grep so that no list needs editing,
# and over ./tests/, which holds the checks of the tag. The grep reads every
# form Go builds with the tag: a go:build line or an old style plus build line,
# indented or not, with a space or a tab after it. It skips the folders that
# ./... skips (testdata and those that start with a dot or an underscore, such
# as a worktree under a hidden folder) and no folder named build. The
# placement test refuses a tagged file in any of those folders, so no tagged
# test goes unrun. With no such package the target runs ./tests/ alone. It
# builds no binary and runs nothing but tests.
test-canary: doctor
	@pkgs=$$(grep -rl --include='*.go' --exclude-dir='.?*' --exclude-dir='_*' --exclude-dir=testdata -E '^[[:space:]]*//(go:build|[[:space:]]*\+build)[[:space:]].*ataniq_canary' . | while IFS= read -r f; do dirname "$$f"; done | LC_ALL=C sort -u); \
	sh scripts/go-tool.sh test -tags ataniq_canary $$pkgs ./tests/

test-dev: doctor
	@sh scripts/go-tool.sh test ./tests/...

qualification-host-build: doctor
	@mkdir -p build
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-probe ./qualification/host/cmd/probe
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-qualify ./qualification/host/cmd/qualify

# The opt-in lead sandbox harness and the static probe it launches. The probe
# is the command in ./cmd/ataniq-probe. Building them runs nothing: the
# harness is run only by a coordinator, on a host the coordinator chose (see
# docs/qualification-lead-sandbox.md).
qualification-leadsandbox-build: doctor
	@mkdir -p build
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/ataniq-probe ./cmd/ataniq-probe
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -trimpath -o build/qualification-leadsandbox ./qualification/leadsandbox/cmd/qualify

# Changelog fragments: changelog.d/<section>-<slug>.md are folded into
# CHANGELOG.md by the release or hotfix branch, as its first fix commit after
# the changelog is dated (ARGS='--into VERSION' folds into the dated section;
# without it the fold writes to [Unreleased]), and again after a fix that adds
# a fragment, and the release checks that none are left. ARGS=--dry-run
# previews the fold. Neither target needs the Go toolchain.
changelog-fold:
	@sh scripts/changelog-fold.sh $(ARGS)

changelog-check:
	@sh scripts/changelog-fold.sh --check
