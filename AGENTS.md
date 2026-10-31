# Instructions for code agents

`bank-bff` is the stateless HTTP backend for frontend of «Понятные финансы», adapted from
`fin-assist/go-service-template` main 6ea35ce (FIN-246) in FIN-261. Read README.md first.
FIN-268 (FIN-331) serves 14 public REST/SSE operations of auth, consents, profile reads/assistant
consent and the assistant; FIN-342 adds six import operations, FIN-361 adds PATCH/DELETE profile
and FIN-360 adds analytics/period reads. FIN-359 completes operations, categories and regular-payment dismissals
(30 operations total, docs/server.md). Unknown paths/methods answer 404.
Do not invent REST fields, RPCs or error codes.
Contracts live in `fin-assist/contracts`: docs/api.md, openapi/openapi.yaml, docs/grpc-api.md.
The owner's latest decision and contracts take precedence over copied template assumptions.

## Repository map

- cmd/bank-bff: serve command, logger, exit status, SIGINT/SIGTERM.
- internal/app: explicit assembly of public and operational HTTP listeners and bounded shutdown;
  api.go builds the public API (embedded contract, go-identity Assembly, go-grpc peers) before bind.
- internal/rest: request ID, gates (MAINTENANCE, X-Client-Version), auth, body limit and kin-openapi
  validation, finerr.ToREST translation and whole-request retry safety, SSE pump/limits, metrics and access log.
- internal/endpoint: the 30 operations, REST <-> gRPC conversion; internal/catalog: user_message and
  severity texts (FIN-225); internal/apispec: embedded openapi.yaml copy with its contracts commit.
- internal/testpeer: test infrastructure only — bufconn fakes of user/assistant/statements/analytics, keys, contract checks.
- internal/config: all environment settings in Vars; docs/config.md table is checked by tests.
- internal/ops: private health/readiness and Prometheus Go/process metrics.
- docs/server.md and docs/testing.md: lifecycle and test scope.
- Dockerfile, compose.yaml, deploy/systemd/bank-bff.service: packaging and stand example.
- Makefile, .github/workflows/ci.yml, .finassist-ci.json: checks and pinned shared lint profile.
- CHANGELOG.md, docs/releasing.md, .github/workflows/release.yml, scripts/changelog.py: service releases.

Keep README, AGENTS and docs current in the same PR. Code, comments and commits are English;
docs and PR title/body are Russian. Every exported identifier has a doc comment.
Use standard library net/http, slog, errors and testing; no frameworks, ORM, DI container,
third party logger or assertion/mocking library. A dependency needs a concrete reason in the PR.
Small interfaces are owned by their consumer, with explicit wiring. Usecases do not import adapters.
Shared Go infrastructure belongs in tagged fin-assist libraries; do not copy their internals.
Stateless BFF has no PostgreSQL, migration command, incoming gRPC server or business jobs. The only
background loop is go-identity Assembly.Run key refresh, started after bind and stopped with listeners.
ENV=dev builds no public API (NewAssembly refuses dev): no auth bypass. Pre-stream SSE failures are
JSON; streams extend their own connection deadlines and have their own deadline and caps.
Add an infrastructure dependency only for a real endpoint/usecase; no dummy code for coverage.

Current tagged contracts/gen/go is v0.9.0 (FIN-413, merged d827664). FIN-342 REST retryable is service RequestRetryInfo (or explicit local transport permission) AND
the declared safety of the entire REST operation; an unknown keyed POST replay window stays false.
Do not infer permission from an error code, HTTP status, RetryInfo, text or UUID. Nested result Status
is decoded only by finerr.DecodeResultStatus: valid canonical reason wins, present-invalid never reads
legacy code, absent uses the legacy adapter. Result retryable is the service's separate new-attempt
permission, including false. Do not detect assistant capability from legacy text or proto3 bool.
Rollout/rollback prerequisites are in docs/config.md; import routes use STATEMENTS_GRPC_ADDR and six StatementsService RPCs.

FIN-364: all four StartPhoneVerification REST fields are structurally required; copy consent/version/age assertions exactly to RPC. False booleans and outdated nonempty versions are USER policy after key/rate-limit gates, never local true checks before RPC. Receipt timestamps, account binding and provider calls are USER-owned. Only the existing trusted-proxy-derived x-client-ip is receipt metadata; never forward arbitrary HTTP X-Client-IP, client clocks or user agent. Register passes its original receipt assertions without fetching the current document. No hidden Start RPC retry or BFF receipt cache; all POST retryable remains conservatively false without proof of the key window.

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
  Claude first resolves the live PR in a read-only job without checkout/secrets; only an open
  same-repository head/base enables the write job. Standalone issues and API errors fail closed.

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
Test names start TestUnit…, TestIntegration… or TestAPI… (fuzz targets FuzzUnit… etc.); make test-names
checks race-enabled Test/Fuzz/Example inventory before each collector and rejects Example (FIN-290).
Legacy scenarios in internal/app/scenarios_test.go are shared: integration assembles
newPublicAPI behind httptest, API runs app.Run; every response is validated with testpeer.Contract.
New FIN-368/FIN-339 source/encoding flows are explicitly API-only helpers without a starter
parameter. Their integration tests compose transport/mapper or HTTP/JWT/rest/probe-RPC
components separately and never call the full public API assembly.
Existing jq guards reject empty/skipped/failed suites and absent, empty, header-only or zero-statement profiles. Missing coverage fails required go; no service N/A.
Network/TCP tests must actually bind and exchange HTTP; permission errors are failures, not skips.
This stateless scaffold requires no PostgreSQL. Future real database functionality must use real
PostgreSQL18 for integration/API tests and must not turn missing dependencies into successful skips.

## Workflow

Branch from fetched main, PR to main. Titles/descriptions are Russian and reference the FIN issue.
Run make test, make lint and independent make cover gates with a process timeout. Toolchain comes
from go.mod; GOTOOLCHAIN=auto downloads it. Python workflow tests require Node.js20+ in PATH.
No UI test execution is authorized.
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
All 30 FIN-268/FIN-342/FIN-361/FIN-360/FIN-359 contract operations exist; do
not treat passing tests as implementing unimplemented contract operations. Each served operation needs
API tests for success, its declared errors, token, Idempotency-Key, schema, client version and SSE.

## Stage artifacts (FIN-148)

`scripts/stage_artifact.py` extracts the binary from the smoke-tested image; `docs/stage-artifacts.md` describes provenance, checks and retention. Keep its Python tests and CI image job in sync. Artifacts do not authorize deployment; only a successful main push CI is eligible for stage. GHCR/production delivery is still pending.

## Analytics reads (FIN-360)

`ANALYTICS_GRPC_ADDR` is required in stage/prod. Both analytics reads are protected, bounded unary calls,
retry-safe only with explicit service advice. The service chooses periods, blocks, texts and states;
BFF forwards `date` as `period_key`, `supports` and `exp`. Unknown request period/transfer codes and malformed
`type@positive-int32`/`key:variant` syntax fail with 400. Unknown primitive/status/reason/action strings and
Struct data pass through unchanged; do not inspect or rewrite nested data. Top-level empty lock text gets
«Пока недоступно» and a counter; empty currency gets RUB only when has_data=true (FIN-98).

## Operations (FIN-359)

Operations/Categories/RegularPayments clients share STATEMENTS_GRPC_ADDR with imports. A known filter's
presence selects SearchOperations; before+filter and all_time+from/to fail before RPC. Search alone has a
10s deadline and a 16 MiB receive limit; other new calls keep 5 s/default 4 MiB. Required unknown operation
kind/status or invalid typed moments fail closed with bounded diagnostics. List projections omit details
and spoken account fields; details keep service text, currencies and exact RFC3339Nano timestamps.
PATCH category and PUT/DELETE dismissal are assignments, replay-safe only with explicit service advice.
Their transport calls are never automatically retried by BFF: an applied split/join can replace the request
ID and a lost response must not turn into NOT_FOUND from a hidden replay. This BFF exception preserves
results even though grpc-api.md §6/9 permits retries of construction-idempotent effects in general.
No financial calculation, pairing, category choice or tenant authorization moves from the service to BFF.

## Assistant source freshness (FIN-368)

FIN-368: source conversion preserves open `unverified`/`time_zone` codes and ready Russian fallback text in history, snapshots and source events. Internal revision vectors never become REST fields.

An unmapped gRPC freshness enum also produces `unverified` with its ready text and the bounded translation metric, including mixed known/unknown reasons. Empty `changed_since` is reserved for confirmed equality. The new full public source/encoding flows are API-only; component integration composes neighbor gRPC/source mapping or HTTP/JWT/rest validation with a probe RPC, without the full public API assembly.

FIN-339 canonical OpenAPI adoption validates examples/defaults and rejects unsupported, empty or repeated Content-Encoding before JSON body reads on the six agreed operations only. Invalid encoding requests connection close and skips the application drain; auth/gates and other body/upload policies retain their order.

## Shared Go CI (FIN-372)

Shared lint and script tests require Python 3.11+ with PyYAML.

`.finassist-ci.json` pins `fin-assist/go-ci` by full SHA and declares the lint profile/overrides. CI workflow calls use the same SHA. `make lint`, `make fmt`, `make lint-config` and `make lint-migrations BASE_REF=origin/main` resolve that source with `scripts/common_ci.py`; generated linter configs live in ignored `.finassist-ci/generated/`. Do not restore copied root linter configs. Preserve coverage/test/API/security gates. bank-bff declares squawk_smallint: false; the smallint exception belongs to bank-statement-data only. Update and rollback all pins in one PR. Details: https://github.com/fin-assist/go-ci/blob/main/docs/shared-go-ci.md.


If a pinned revision changes common/bootstrap.py, synchronize its exact scripts/common_ci.py copy in the same PR; Renovate updates SHA references only. Rollback restores both pins and the matching bootstrap bytes. Byte equality keeps an incomplete update red; a manual copy update is not a bot-only FIN-211 update.

FIN-411: REST analytics/periods accept rolling_year and rolling_quarter, pass the exact request and both transfer modes to analytics, and preserve opaque financial blocks/edge ranges unchanged. New singleton producer keys are validated against their type; navigation is absent. Assistant chips remain calendar-only. Canonical contract is d827664 / gen/go v0.9.0; rollout analytics → producer → BFF/Android, no deployment in this PR.
