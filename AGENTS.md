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
CI runs coverage script tests; when a root `go.mod` exists it also tests the Go module once with coverage.
Without a module the report explicitly has no data. Keep README and CI instructions current.
Run tests with a process timeout; no UI test execution is authorized.

## Code Review Rules

Report verified findings inline in Russian. Make separate Security and Tests passes.
Security: untrusted PR input must never become shell code; fork PRs do not write coverage comments.
Tests: cover report percentages, empty scaffolds, multiple modules and the changed-line threshold separately.
No application behaviour or public API changes are part of this CI-only increment.
