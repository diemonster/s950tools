# s950-tools — task orchestration across the Go backend + Wails Svelte
# frontend. Run `make` (or `make help`) to see all targets.
#
# Conventions:
#   - All recipes assume they're invoked from the repo root.
#   - Frontend tasks dispatch into cmd/s950-gui/frontend via a `cd`
#     so the npm scripts there stay as the single source of truth.
#   - `make check` is the CI-default target: runs every lint + every
#     test and exits non-zero if anything fails.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Sub-directory holding the Wails frontend (npm workspace).
FRONTEND := cmd/s950-gui/frontend

# cgo build environment shared by every native macOS build.
#  - MACOSX_DEPLOYMENT_TARGET=11.0 aligns the cgo SDK target with the
#    Wails linker default so we don't get "object file built for newer
#    macOS version" warnings. 11.0 is the Apple-Silicon-cutover floor
#    and the realistic minimum for any shipped build today.
#  - CGO_CXXFLAGS silences a benign RtMidi C++ VLA warning we can't
#    patch upstream (gomidi/midi/v2/drivers/rtmididrv/imported/rtmidi).
MAC_BUILD_ENV := MACOSX_DEPLOYMENT_TARGET=11.0 CGO_CXXFLAGS="-Wno-vla-cxx-extension"

# ---------- Help ----------
.PHONY: help
help: ## Show available targets
	@awk 'BEGIN { FS = ":.*?## " } \
	     /^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' \
	     $(MAKEFILE_LIST)

# ---------- Tests ----------
.PHONY: test test-go test-frontend
test: test-go test-frontend ## Run all tests (Go + frontend)

test-go: ## Run Go unit tests across every package
	@echo "→ Go tests"
	@go test ./...

test-frontend: ## Run frontend Vitest suite
	@echo "→ Frontend tests"
	@cd $(FRONTEND) && npm test --silent

# ---------- Lint + type checks ----------
.PHONY: lint lint-go lint-frontend
lint: lint-go lint-frontend ## Run all linters + type checks

lint-go: ## go vet + staticcheck
	@echo "→ go vet"
	@go vet ./...
	@echo "→ staticcheck"
	@staticcheck ./...

lint-frontend: ## svelte-check + ESLint
	@echo "→ svelte-check"
	@cd $(FRONTEND) && npm run check --silent
	@echo "→ ESLint"
	@cd $(FRONTEND) && npm run lint --silent

# ---------- Combined ----------
.PHONY: check
check: lint test ## Run lint + test together (CI default)

# ---------- Build ----------
.PHONY: build build-cli build-gui build-gui-universal
build: build-cli build-gui ## Build CLI binary + Wails desktop app

build-cli: ## Build the s950-tools CLI binary
	@echo "→ Building CLI"
	@$(MAC_BUILD_ENV) go build ./cmd/s950-tools

build-gui: ## Build the Wails desktop app for the host arch (fast, local dev)
	@echo "→ Building GUI (wails build, host arch)"
	@cd cmd/s950-gui && $(MAC_BUILD_ENV) wails build

build-gui-universal: ## Build a universal macOS .app (arm64 + amd64) for distribution
	@echo "→ Building GUI (wails build, darwin/universal)"
	@cd cmd/s950-gui && $(MAC_BUILD_ENV) wails build -platform darwin/universal
	@echo "→ Output: cmd/s950-gui/build/bin/s950-gui.app"
	@echo "  See BUILDING.md for signing + notarization steps before sharing."

# ---------- Doctor ----------
.PHONY: doctor
doctor: ## Verify required CLI tools are installed
	@bash scripts/doctor.sh

# ---------- Icon ----------
.PHONY: icon
icon: ## Regenerate build/appicon.png from the source SVG
	@bash scripts/regen-icon.sh

# ---------- Clean ----------
.PHONY: clean
clean: ## Remove build artifacts + frontend dist
	@echo "→ Cleaning"
	@rm -f s950-tools s950-gui
	@rm -rf $(FRONTEND)/dist
	@rm -rf cmd/s950-gui/build/bin
