.PHONY: bootstrap doctor test test-canary test-ownerchannel ownerchannel-build scratch-seat-build canary-build test-dev test-patched qualification-host-build qualification-leadsandbox-build changelog-fold changelog-check

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

# The tests of the code behind the owner channel's build tag, which make test
# does not build, in two runs: with that tag alone, and with it and the canary
# tag together, since no other target tests the pair. Each run covers every
# package that holds a Go file whose build constraint names a tag it sets, found
# here by the same grep as make test-canary (it reads the same folders and the
# same line forms), and ./tests/, which holds the checks of both tags. It builds
# no binary and runs nothing but tests (make ownerchannel-build and make
# scratch-seat-build build the daemon).
test-ownerchannel: doctor
	@own=$$(grep -rl --include='*.go' --exclude-dir='.?*' --exclude-dir='_*' --exclude-dir=testdata -E '^[[:space:]]*//(go:build|[[:space:]]*\+build)[[:space:]].*ataniq_ownerchannel' . | while IFS= read -r f; do dirname "$$f"; done | LC_ALL=C sort -u); \
	both=$$(grep -rl --include='*.go' --exclude-dir='.?*' --exclude-dir='_*' --exclude-dir=testdata -E '^[[:space:]]*//(go:build|[[:space:]]*\+build)[[:space:]].*ataniq_(canary|ownerchannel)' . | while IFS= read -r f; do dirname "$$f"; done | LC_ALL=C sort -u); \
	sh scripts/go-tool.sh test -tags ataniq_ownerchannel $$own ./tests/ && \
	sh scripts/go-tool.sh test -tags ataniq_ownerchannel,ataniq_canary $$both ./tests/

# The daemon with the owner channel, built with the owner channel's build tag
# into build/owner/, which git ignores, and nothing else: no console, no holder,
# no other command. The owner channel is provisional and unqualified, so this
# is a build for the owner's own trial, never a release's: no release, no CI job
# and no other target uses it, and none depends on it. It is the one target that
# builds a binary with that tag alone (scratch-seat-build builds the daemon with
# it and the canary tag together); tests/ownerchannel_tag_test.go refuses any
# other recipe that names it, and any line in this one but the mkdir and the
# build. Building it runs nothing.
ownerchannel-build: doctor
	@mkdir -p build/owner
	@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -trimpath -o build/owner/ataniqd ./cmd/ataniqd

# The daemon with the owner channel and the scratch leads, built with both
# build tags, the owner channel's and the canary's, into build/scratch-seat/,
# which git ignores, and nothing else: no console, no holder, no other command.
# It is for the owner's own trial of real scratch leads with the seat gate;
# both are provisional and unqualified, so this is never a release's build: no
# release, no CI job and no other target uses it, and none depends on it. It is
# the one target that builds a binary with the pair of tags (ownerchannel-build
# builds with the owner channel's tag alone, canary-build with the canary's
# alone); tests/ownerchannel_tag_test.go holds its recipe to exactly these two
# lines and its prerequisite to doctor alone. Building it runs nothing.
scratch-seat-build: doctor
	@mkdir -p build/scratch-seat
	@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd

# The binaries of the attended canary run, built with the canary build tag into
# build/canary/, which git ignores: the daemon, the holder, the provisioning
# command, the stand-in agent, the console, the intake and the probe. They are
# what qualification/canary/run/canary-run.sh runs and no more. This is the one
# target that builds binaries with the tag alone (scratch-seat-build builds the
# daemon with it and the owner channel's tag together); no release and no CI
# job uses it.
# The stand-in and the probe are static, because both run inside the sandbox.
# Building them runs nothing: the script is run only by the owner's hand (see
# qualification/canary/run/README.md).
canary-build: doctor
	@mkdir -p build/canary
	@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniqd ./cmd/ataniqd
	@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-holder ./cmd/ataniq-holder
	@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-canary ./cmd/ataniq-canary
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-canary-standin ./cmd/ataniq-canary-standin
	@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-console ./cmd/ataniq-console
	@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-intake ./cmd/ataniq-intake
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/canary/ataniq-probe ./cmd/ataniq-probe

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
