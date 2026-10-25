# Instructions for code agents

`bank-bff` is the stateless HTTP backend for frontend of «Понятные финансы», adapted from
`fin-assist/go-service-template` main 6ea35ce (FIN-246) in FIN-261. Read README.md first.
The public mux currently has no business routes. Do not invent REST fields, RPCs or error codes.
Contracts live in `fin-assist/contracts`: docs/api.md, openapi/openapi.yaml, docs/grpc-api.md.
The owner's latest decision and contracts take precedence over copied template assumptions.

## Repository map

- cmd/bank-bff: serve command, logger, exit status, SIGINT/SIGTERM.
- internal/app: explicit assembly of public and operational HTTP listeners and bounded shutdown.
- internal/config: all environment settings in Vars; docs/config.md table is checked by tests.
- internal/ops: private health/readiness and Prometheus Go/process metrics.
- docs/server.md and docs/testing.md: lifecycle and test scope.
- Dockerfile, compose.yaml, deploy/systemd/bank-bff.service: packaging and stand example.
- Makefile, .github/workflows/ci.yml, .golangci.yml: checks.
- CHANGELOG.md, docs/releasing.md, .github/workflows/release.yml, scripts/changelog.py: service releases.

Keep README, AGENTS and docs current in the same PR. Code, comments and commits are English;
docs and PR title/body are Russian. Every exported identifier has a doc comment.
Use standard library net/http, slog, errors and testing; no frameworks, ORM, DI container,
third party logger or assertion/mocking library. A dependency needs a concrete reason in the PR.
Small interfaces are owned by their consumer, with explicit wiring. Usecases do not import adapters.
Shared Go infrastructure belongs in tagged fin-assist libraries; do not copy their internals.
Stateless BFF has no PostgreSQL, migration command, incoming gRPC server or background jobs.
Add an infrastructure dependency only for a real endpoint/usecase; no dummy code for coverage.

## Security

- Keep operational endpoints on their own private listener; never attach metrics, readiness or
  pprof to the public mux. Production ingress exposes only PUBLIC_ADDR and terminates TLS.
- No public business route exists until its contract validation, authorization, rate limits and
  failure mapping are implemented together. Do not add a dev authentication bypass.
- User identity comes from verified credentials, not an input user_id. Propagate only verified
  identity and explicitly allowlisted metadata to internal services. Error translation follows
  finerr and contracts; unknown internal failures are generic 500, never raw error text.
- Secrets, tokens, personal data and request bodies stay out of logs. No user_id/free-text metric labels.
- All listeners have header/read/write/idle limits. Future uploads/SSE require contract-specific
  limits and tests; do not silently reuse unary limits for long streams.
- Bind both listeners before readiness. On failure release the other listener. Drain on SIGTERM,
  bound shutdown and close active connections after timeout. Readiness drops before shutdown.
- CI untrusted input never becomes shell code; fork PRs have no comment write permissions.

## SQL construction in Go

If a future task introduces SQL, fixed-structure queries use plain SQL with parameters ($1, $2, …).
Use github.com/Masterminds/squirrel only for dynamic query structure (optional predicates/columns),
with PlaceholderFormat(sq.Dollar). Values remain parameters; identifiers/structure are constants
or explicit allowlists. Changing values alone is not dynamic SQL. Explain a builder limitation
beside any necessary manual construction. No SQL or database is needed in the current BFF.

## Tests

All new logic has unit tests without network or database. Integration tests use real transports
and dependencies. API tests send real HTTP through the full BFF command/assembly; direct handler
calls are unit tests. Each future REST endpoint must have API tests against openapi.yaml using
kin-openapi, success and every contract error, authentication, idempotency, headers and SSE as
applicable. Peer services use transport fixtures on bufconn, not mocked generated clients.

FIN-238: each of unit/integration/API independently requires >=80% statements of the same whole
production module, using pinned go-test-coverage v2.20.1 with agreed 79.95% -> 80.0% rounding.
No excluded packages/helpers/generated files for percentages. Combined is informational only.
Test names start TestUnit…, TestIntegration… or TestAPI…; make test-names checks race-enabled
inventory before each collector. Existing jq guards reject empty/skipped/failed suites and absent,
empty, header-only or zero-statement profiles. Missing coverage fails required go; no service N/A.
Network/TCP tests must actually bind and exchange HTTP; permission errors are failures, not skips.
This stateless scaffold requires no PostgreSQL. Future real database functionality must use real
PostgreSQL18 for integration/API tests and must not turn missing dependencies into successful skips.

## Workflow

Branch from fetched main, PR to main. Titles/descriptions are Russian and reference the FIN issue.
Run make test, make lint and independent make cover gates with a process timeout. Toolchain comes
from go.mod; GOTOOLCHAIN=auto downloads it. No UI test execution is authorized.
Before review, make separate Security and Tests passes. Request in Russian:

@codex @claude проведите ревью этого PR по Code Review Rules в AGENTS.md, включая Security и Tests.
Замечания — на русском, inline на изменённых строках. Особое внимание: <риски изменения>.

Request Claude only when its workflow already exists on main; otherwise request only Codex.
Review the current SHA, fix verified findings, reply/resolve threads and request review again.
Never merge without the owner's explicit command. Do not change protections or suppress red checks.
Tags are created only by the owner; release publication, image publication and deployment are separate.

## Code Review Rules

Report verified findings inline in Russian. Make separate Security and Tests passes.
Security: authorization boundaries, private operational listener, no data leakage, bounded HTTP and
shutdown, startup failure cleanup, trusted metadata, safe CI and fork permissions.
Tests: meaningful behavior coverage at all three levels; real HTTP API and transport dependencies;
negative startup/shutdown scenarios; unchanged strict guards and independent total80.
No business REST/SSE or database functionality is part of FIN-261. Do not require absent business
handlers solely to increase coverage, and do not treat passing scaffold tests as implemented APIs.
