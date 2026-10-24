# Instructions for code agents

`bank-bff` is currently an empty scaffold of «Понятные финансы». Read `README.md` first.
Contracts live in `fin-assist/contracts`; do not invent REST fields, RPCs or errors here.

## SQL construction in Go

Applies when handwritten Go code in this repository needs to build SQL queries.

- **SQL construction:** write fixed-structure queries as plain SQL with parameters (`$1`, `$2`, … for
  PostgreSQL). Use [Squirrel](https://github.com/Masterminds/squirrel) (`github.com/Masterminds/squirrel`) only
  when assembling a query dynamically, such as optional predicates or varying column lists. Changing argument
  values alone does not make a query dynamic. This limited use is an approved exception to "standard library
  first". Parameterize values; keep identifiers and structural fragments constant or from an explicit allowlist.
  Use `PlaceholderFormat(sq.Dollar)` for PostgreSQL builders. If a dynamic query cannot be expressed by the
  builder, explain the limitation beside its manual construction and preserve parameterization.

## Workflow

Branch from `main`, open PRs to `main`; titles and descriptions are Russian and reference the FIN issue.
Commits and code are English. Never merge without the owner's explicit command.
CI and `make test` run all Python script tests and check CHANGELOG format.
FIN-238: CI uses pinned go-test-coverage v2.20.1 with independent unit/integration/API total80 statements gates.
The owner accepts its rounding of 79.95% to 80.0%. Every suite instruments all `./...`, including generated code
and ordinary `.go` test helpers, without exclusions. Name tests TestUnit…, TestIntegration… or TestAPI…;
`make test-names` rejects tests omitted by these selectors. Unit has no network/DB, integration uses real
transport/dependencies/jobs, API calls the full BFF assembly over HTTP/REST/SSE. Direct handlers are not API.
check-suite.sh uses jq to require an executed passing Test and reject skips/failures, plus a minimal positive
statement-block guard. It does not calculate coverage. PostgreSQL absence fails (CI=true). Built-in comma-profile
merge is informational combined only. Missing Go module/suite/profile or low coverage fails the existing required
job go. The scaffold is incomplete, not a passing empty service. Profiles/logs/native reports are CI artifacts;
fork PRs never write comments. Keep README and CI instructions current.
Run tests with a process timeout; no UI test execution is authorized.

## Service releases

`CHANGELOG.md`, `docs/releasing.md` and `.github/workflows/release.yml` define repeatable GitHub Releases for existing version tags. The scaffold starts with empty Unreleased; do not invent released versions or Go/application code. The owner alone pushes tags. Run `make release-check VERSION=vX.Y.Z` against the prepared release section before tagging. Release publication is separate from image/deployment work.

## Code Review Rules

Report verified findings inline in Russian. Make separate Security and Tests passes.
Security: untrusted PR input must never become shell code; fork PRs do not write coverage comments.
Tests: verify empty/skipped/failed suites, missing/empty/zero-statement profiles, selector classification and accepted rounding. Every suite independently meets total80; combined never compensates for a failed level.
No application behaviour or public API changes are part of this CI-only increment.
