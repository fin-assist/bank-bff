# The service is still a scaffold; absence of Go code/suites is a failing prerequisite.
GO_TEST_COVERAGE_VERSION := v2.20.1
COVERAGE := go run github.com/vladopajic/go-test-coverage/v2@$(GO_TEST_COVERAGE_VERSION)
SHELL := /bin/bash
export GOPRIVATE := github.com/fin-assist/*

.PHONY: test release-check test-names cover collect-unit collect-integration collect-api check-unit check-integration check-api cover-unit cover-integration cover-api cover-combined

test-names: ## reject tests accidentally omitted from the three selectors
	go test -list '^Test' -timeout=8m ./... > test-names.txt
	@if grep -E '^Test' test-names.txt | grep -Ev '^Test(Unit|Integration|API).+'; then \
	  echo 'Name every test TestUnit..., TestIntegration... or TestAPI...' >&2; exit 1; fi

# All suites instrument the same whole module. No production files are excluded.
# Ordinary packages without tests may be reported as skipped; skipped Test events fail.
collect-unit collect-integration collect-api:
	@set -euo pipefail; level='$(@:collect-%=%)'; case "$$level" in unit) prefix=Unit;; integration) prefix=Integration;; api) prefix=API;; esac; \
	rm -f "$$level.out" "$$level.jsonl"; \
	CI=true go test -json -race -count=1 -timeout=8m -coverpkg=./... \
	  -coverprofile="$$level.out" -run "^Test$$prefix" ./... | tee "$$level.jsonl"; \
	bash scripts/check-suite.sh "$$level.jsonl" "$$level.out"

check-unit check-integration check-api:
	@set -euo pipefail; level='$(@:check-%=%)'; \
	bash scripts/check-suite.sh "$$level.jsonl" "$$level.out"; \
	$(COVERAGE) --config .testcoverage.yml --profile "$$level.out" \
	  --breakdown-file-name "$$level.coverage.txt" | tee "$$level.report.txt"

cover-unit: collect-unit
	$(MAKE) check-unit
cover-integration: collect-integration
	$(MAKE) check-integration
cover-api: collect-api
	$(MAKE) check-api

cover-combined: ## informational union; the tool merges profiles, never percentages
	@set -euo pipefail; for level in unit integration api; do bash scripts/check-suite.sh "$$level.jsonl" "$$level.out"; done
	set -euo pipefail; $(COVERAGE) --profile unit.out,integration.out,api.out \
	  --breakdown-file-name combined.coverage.txt | tee combined.report.txt

cover: collect-unit collect-integration collect-api ## collect all profiles before enforcing the three gates
	@status=0; for level in unit integration api; do $(MAKE) check-$$level || status=1; done; \
	$(MAKE) cover-combined || status=1; exit "$$status"


test: ## script regression tests and CHANGELOG format; no Go module exists yet
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/changelog.py check

release-check: ## VERSION=vX.Y.Z: validate the CHANGELOG section before tagging
	@test -n "$(VERSION)" || { echo "usage: make release-check VERSION=vX.Y.Z"; exit 2; }
	python3 scripts/changelog.py check
	test "$$(python3 scripts/changelog.py latest)" = "$(VERSION)"
