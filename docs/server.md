# Устройство bank-bff

Перенесён из `fin-assist/go-service-template` main `6ea35cea4b0ff68b831acadd22e2bc77e79481bd` (FIN-246), адаптация каркаса — FIN-261, публичный REST/SSE-API входа, профиля и помощника — [FIN-268](https://finassist.youtrack.cloud/issue/FIN-268), каталог пользовательских ошибок — часть [FIN-225](https://finassist.youtrack.cloud/issue/FIN-225) (объединённая задача [FIN-331](https://finassist.youtrack.cloud/issue/FIN-331)). BFF по `contracts/docs/grpc-api.md` не хранит собственного состояния: SQL, PostgreSQL, миграций и входящего gRPC business server нет. Домены, квоты и идемпотентные эффекты остаются в сервисах; BFF проверяет запрос по контракту, аутентифицирует пользователя и переводит gRPC ↔ REST.

## Жизненный цикл

`cmd/bank-bff` читает конфигурацию, создаёт slog с именем сервиса и версией и передаёт контекст SIGINT/SIGTERM в `internal/app.Run`. В `stage`/`prod` сначала собирается публичный API (`internal/app/api.go`): контракт, идентичность `go-identity` v0.2.0 `NewAssembly`, ленивые gRPC-соединения `go-grpc` v0.2.0 `client.Dial` к `user` и `assistant`, метрики и маршруты. Сборка ничего не загружает по сети и не запускает горутин; при ошибке всё уже созданное освобождается, а старт завершается exit 1 до привязки портов. Затем привязываются публичный и operational TCP listeners; ошибка второго bind освобождает первый. После обоих bind включается readiness и запускается обновление ключей идентичности (`Assembly.Run`: JWKS пользователя из `user.GetSigningKeys`, в `prod` — ключи API-сервера). Это единственный фоновый цикл BFF; он принадлежит библиотеке идентичности и останавливается вместе с listeners. Бизнес-заданий нет.

После SIGTERM readiness становится 503, публичный listener обслуживает запросы `SHUTDOWN_DELAY`, затем оба HTTP-сервера получают общий ограниченный контекст shutdown. Открытые SSE-потоки при этом сразу завершаются без терминального события (клиент переподключается и первым получает текущее состояние), поэтому shutdown не ждёт их до своего срока; по истечении срока оставшиеся соединения закрываются. Потом останавливается обновление ключей и закрываются gRPC-соединения. В `dev` публичный mux пустой (любой путь — 404): `NewAssembly` не поддерживает `dev`, и обхода авторизации нет.

Operational listener публикует только `GET/HEAD /healthz`, `/readyz`, `/metrics`; публичный — только `/v1/...`. Pprof и `DefaultServeMux` не используются.

## Публичные операции (FIN-268)

| Операция | Метод сервиса | Токен | `Idempotency-Key` |
|---|---|---|---|
| `POST /v1/auth/phone` | `user.AuthService/StartPhoneVerification` | нет | да |
| `GET /v1/auth/phone/events` (SSE) | `user.AuthService/WatchPhoneVerification` | `X-Verification-Token` → поле запроса | — |
| `POST /v1/auth/register` | `user.AuthService/Register` | нет | да |
| `POST /v1/auth/token` | `user.AuthService/RefreshTokens` | нет | да |
| `POST /v1/auth/logout` | `user.AuthService/Logout` | да | — |
| `GET /v1/consents/{type}` | `user.ConsentDocumentService/GetConsentDocument` | только `assistant` | — |
| `GET /v1/profile` | `user.ProfileService/GetProfile` | да | — |
| `PUT /v1/profile/assistant-consent` | `user.ProfileService/GrantAssistantConsent` | да | — |
| `DELETE /v1/profile/assistant-consent` | `user.ProfileService/RevokeAssistantConsent` | да | — |
| `GET /v1/assistant/messages` | `assistant.AssistantService/ListMessages` | да | — |
| `POST /v1/assistant/messages` | `assistant.AssistantService/AskQuestion` | да | да |
| `GET /v1/assistant/messages/{answer_id}/events` (SSE) | `assistant.AssistantService/WatchAnswer` | да | — |
| `POST /v1/assistant/messages/{answer_id}/retry` | `assistant.AssistantService/RetryAnswer` | да | да |
| `GET /v1/assistant/limit` | `assistant.AssistantService/GetLimit` | да | — |

Остальные пути и методы `/v1` (`PATCH`/`DELETE /v1/profile`, выписки, операции, категории, аналитика) этой задачей не реализованы и отвечают JSON 404 `NOT_FOUND`; готовность всего продукта к prod они не подтверждают. Браузерный preflight `OPTIONS` (веб-клиент вне MVP) не обрабатывается.

## Порядок проверок запроса

Для каждого запроса `/v1` в порядке `docs/api.md`, «Ошибки»:

1. `X-Request-Id`: UUID клиента возвращается эхом и уходит в `x-request-id`; иное значение заменяется новым UUID. Свежий `Date` ставит `net/http` в каждом ответе, включая ошибки и воспроизведённые сервисом ответы.
2. `MAINTENANCE=on` → 503 `MAINTENANCE` с `Retry-After` и `until`.
3. `X-Client-Version` (ровно один заголовок, формат и минимальная версия платформы) → 400 `CLIENT_VERSION_UNSUPPORTED`.
4. Неизвестный путь или метод → 404 `NOT_FOUND`; `HEAD` (его нет в контракте, а шаблон `GET` в `ServeMux` его принимает) — тоже 404 до маршрутизации, без вызова сервиса и без слота SSE.
5. `Authorization` у защищённых операций — `identity.HTTP` (подпись, `exp`, `iss`, `aud`, непустой `sub`) → 401 `INVALID_TOKEN`. Если свежих ключей нет (сбой `user`), токен не оценивается: 500 `INTERNAL_ERROR`, пользователь не выходит из аккаунта (FIN-293).
6. Тело: не больше `JSON_BODY_LIMIT` (по `Content-Length` и по фактическому чтению) → 413 `REQUEST_TOO_LARGE` ([FIN-339](https://finassist.youtrack.cloud/issue/FIN-339)); у операций без тела любое тело — 400, читается не больше одного байта.
7. Схема `openapi.yaml` (kin-openapi): параметры пути и query, заголовки, `Content-Type`, тело; `Idempotency-Key` обязателен и должен быть UUID. В обработчик попадают только члены тела с точными именами схемы: `encoding/json` сопоставляет имена без учёта регистра, и `{"text":"ok","Text":""}` иначе заменил бы проверенный текст. Все нарушения — один 400 `VALIDATION_ERROR` с `details[]` (`REQUIRED`, `INVALID_FORMAT`, `OUT_OF_RANGE`). Длина строк считается в кодовых точках Unicode, как «символы» `docs/api.md` (kin-openapi считает единицы UTF-16). Неизвестный `transfer_mode` вопроса — 400.
8. Вызов сервиса с дедлайном 5 с (меньше `WriteTimeout`, поэтому ответ об истечении успевает уйти).

Идемпотентность, лимиты попыток, согласия и проверки токенов запроса выполняют сервисы; BFF ключ не хранит и запрос не повторяет.

## Исходящие вызовы и metadata

`client.Dial` создаёт свежий outgoing metadata с сервисным токеном (`x-service-authorization`) и `x-request-id`; затем `identity.UnaryClient`/`StreamClient` добавляют `authorization` только с проверенным этой библиотекой access-токеном пользователя (непроверенный заголовок анонимной операции не пробрасывается); затем `rest.UnaryHints`/`StreamHints` добавляют разрешённый список: `x-client-ip` (адрес клиента, см. ниже), `x-client-version`, `x-read-primary: 1` (только для `X-Read-Primary: 1`), `idempotency-key` (только у операций, где он обязателен). Адрес клиента — адрес TCP-пира. Если пир входит в `TRUSTED_PROXIES` (nginx стенда, ingress), `X-Forwarded-For` читается справа налево и адресом клиента считается первый элемент вне доверенных сетей: каждый прокси дописывает увиденный адрес, поэтому верить можно только элементам, дописанным доверенными прокси. Всё левее первого недоверенного элемента пишет клиент, и BFF этого не читает: неразборчивый или длинный префикс результат не меняет. Если не разбирается элемент, дописанный доверенными прокси, или просмотрено больше 16 элементов, адресом остаётся последний доверенный узел; нет заголовка — адрес пира. От недоверенного пира заголовок игнорируется. При пустом `TRUSTED_PROXIES` за прокси у всех клиентов один адрес — адрес прокси, и предел `SSE_MAX_STREAMS_PER_IP` становится общим. Этот же адрес используют предел SSE-потоков и журнал. Другие заголовки HTTP в сервисы не попадают; `X-Verification-Token` передаётся полем `WatchPhoneVerificationRequest`. Повторы на `UNAVAILABLE` — только чтения: `GetSigningKeys`, `GetConsentDocument`, `GetProfile`, `ListMessages`, `GetLimit`.

## Ошибки

`internal/rest` переводит ошибку сервиса по `ErrorInfo.reason` через `finerr.ToREST` (`gen/go/v0.5.0`): HTTP-статус реестра, `details[]` из `BadRequest` (только `field` ограниченной формы и `code` из `REQUIRED`/`INVALID_FORMAT`/`OUT_OF_RANGE`, иной код — `INVALID_FORMAT`; `message` — собственный текст BFF, `description` сервиса клиенту не передаётся), `retry_at`/`resets_at` из metadata, `Retry-After` из `RetryInfo` (свежая длительность сервиса, не пересчёт сохранённой). Неполная ошибка, неизвестный reason, gRPC-код, не совпадающий с реестром для reason, голые `UNAVAILABLE`/`DEADLINE_EXCEEDED`/`PERMISSION_DENIED`, а также статус реестра, которого операция не объявляет в `openapi.yaml`, — 500 `INTERNAL_ERROR` и `finassist_unmapped_error_total`. `status.message` и тексты сервисов клиенту не передаются и не журналируются.

`user_message`, `severity` и диагностический `message` задаёт единый каталог BFF (`internal/catalog`, FIN-225). Он фиксированный: в текст не подставляются ввод пользователя, сообщения сервисов, SQL, ответы провайдеров или модели. Неизвестный код получает общий безопасный текст. Для помощника: терминальное событие `error` и failed-сообщения истории получают `user_message`/`severity` и прежние `text`/`error_text` одним текстом каталога; `retryable` — разрешение `RetryAnswer` от сервиса (с presence у истории). Неизвестный legacy-код `AnswerErrorCode` — консервативный `INTERNAL_ERROR`.

FIN-342: каждый REST-отказ нового BFF содержит `error.retryable`, включая явный `false`. `finerr.ToREST` сохраняет optional `RequestRetryInfo` сервиса; `true` выдаётся только при явном разрешении и отдельной декларации `Route.ReplaySafe` для всего внешнего вызова. Сейчас такая декларация есть у чтений GET и идемпотентных PUT/DELETE согласия помощника. Она не переносится автоматически на новые маршруты и не означает безопасность только одного дочернего RPC. Все POST пока получают `false`: BFF не хранит достоверный возраст/срок ключа, а UUID не доказывает окно 24 часа или 60 секунд refresh. Отсутствующий advice, неизвестный/некорректный статус и собственные отказы проверок BFF также дают `false`. Собственный временный отказ лимитера SSE — исключение с явным решением транспортного обработчика: `Call.Refuse(error, true)` соединяет его permission с той же доказанной безопасностью всего GET. Отказ происходит до RPC и не занимает слот; клиент может переоткрыть поток после свежего ожидания. Положительный `Retry-After` сам по себе ничего не разрешает. Корректирующие сценарии авторизации/согласия/техработ остаются отдельными правилами контракта.

`AnswerError.status` терминального события и `Message.error` failed-истории разбираются общим `finerr.DecodeResultStatus`. При отсутствии Status применяется прежний адаптер. Присутствующая корректная причина открыта и имеет приоритет над legacy enum; неизвестная каталогу причина сохраняется с общим безопасным текстом помощника. Присутствующий некорректный Status даёт `INTERNAL_ERROR`, а legacy enum не читается. `RequestRetryInfo` и относительный `RetryInfo` во вложенном результате запрещены самим декодером. Ни raw `Status.message`, ни metadata не становятся пользовательским текстом. Совпадение причины с REST `MAINTENANCE`/`CONSENT_REQUIRED` не превращает успешное чтение в HTTP-отказ и не создаёт REST-восстановление.

Внешнее разрешение новой генерации сохраняется независимо от Status: `AnswerError.retryable` авторитетно, включая `false`; у `Message.retryable` сохраняется presence (отсутствие, false, true). По пустому/непустому тексту или коду разрешение не угадывается. Старый сервис без FIN-159 proto3 bool различить не позволяет: необходима граница выкладки из `docs/config.md`. Переход producers на dual-write и их usecase-owned unary advice выполняется после обновления декодеров; этот PR не меняет сервисы и не подтверждает фактическую выкладку. Импорт и его `ImportStreamError.status` остаются вне реализованных 14 операций: отсутствующие HTTP-маршруты не добавляются ради декодера.

## Ответы и SSE

Моменты форматируются в поясе ответа (`time_zone` сервиса; профиль — `profile.timezone`; при отсутствии пояса — `Europe/Moscow`), RFC 3339 со смещением. Перечисления переводятся по имени через `protoreflect`; неизвестное значение обязательного поля — по правилу поля: тема → `system`, статус ответа → `failed`, покрытие точки → `partial`, роль → сообщение не показывается, чип с неизвестным экраном не передаётся, блок, который BFF не может перевести, остаётся на своей позиции с типом `unsupported` и `alt_text`. `changed_since_text` и `profile.consents[]` собирает BFF. Пустая `currency` → `RUB` и `finassist_empty_required_field_total`.

SSE-потоки (`WatchPhoneVerification`, `WatchAnswer`): ошибка до первого сообщения сервиса — обычный HTTP JSON-ответ (404 чужого ответа, 401/429 заявки); первым событием идёт текущее состояние; терминальное событие закрывает поток. Сбой сервиса или транспорта после начала потока закрывает его без терминального события. Heartbeat `: ping` — каждые 15 с. Поток продлевает собственные read/write deadline соединения (`http.ResponseController`) до своего срока плюс 10 с ещё до ожидания первого сообщения сервиса (ответ 200 при этом не фиксируется), поэтому `ReadTimeout`/`WriteTimeout` unary-запросов не обрывают ни поток, ни поздний JSON-отказ сервиса; собственный срок — 4 мин для ответа помощника и 6 мин для заявки, после него поток закрывается без терминального события. Отключение клиента отменяет поток к сервису; генерация ответа от него не зависит. Пределы `SSE_MAX_STREAMS` и `SSE_MAX_STREAMS_PER_IP` дают 429 `RATE_LIMITED` со свежим `retry_at` до вызова сервиса.

## Защита HTTP

ReadHeaderTimeout 5s, ReadTimeout 10s, WriteTimeout 30s, IdleTimeout 60s, MaxHeaderBytes 1 MiB. Непрочитанный остаток тела после раннего ответа (413, 401, 404) отбрасывается в пределах 256 КиБ, чтобы клиент, ещё пишущий тело, получил ответ, а не сброс соединения. net/http делает это сам только для keep-alive соединений; тело с объявленным `Content-Length` больше 256 КиБ не читается вовсе, его соединение всё равно закрывается. Логи — одна строка `request` на запрос: `request_id`, метод, шаблон маршрута, статус, длительность, метка клиента; без путей с ID, query, заголовков, тел, токенов и телефонов.

## Метрики

| Метрика | Метки |
|---|---|
| `finassist_http_requests_total` | `route` (шаблон), `status`, `client` (платформа из `CLIENT_MIN_VERSIONS`: версия любой допустимой формы прошла бы шлюз и засорила бы метку) |
| `finassist_http_request_duration_seconds` | `route` |
| `finassist_unmapped_error_total` | `reason` (форма реестра, не больше 32, затем `other`; `none`, `invalid`), `grpc_code` |
| `finassist_untranslated_code_total` | `field`, `code` (`unknown`, `content`) |
| `finassist_empty_required_field_total` | `field`: `Message.currency`, `AnswerSnapshot.currency` (ряды с 0 при старте) |
| `finassist_sse_streams` | `route` |
| `finassist_sse_rejected_total` | `route` |
| `grpc_client_handled_total`, `grpc_client_handling_seconds` | `peer`, `method`, `code` (go-grpc) |
| `finassist_identity_rejected_total`, `finassist_identity_jwks_skipped_keys` | go-identity |

Плюс стандартные Go/process collectors. `user_id`, телефонов и свободного текста в метках нет.

## Контракт

Копия `openapi.yaml` встроена в бинарь (`internal/apispec`, источник — `apispec.Source`: `fin-assist/contracts` `8b3b1b6`, включает FIN-224). При обновлении контракта копия заменяется в PR вместе с кодом. Два примера этой версии невалидны (FIN-339), поэтому при загрузке не проверяются примеры; схемы запросов и ответов проверяются полностью. Используемый Go-модуль контрактов — `gen/go/v0.5.0` (опубликованный тег на `471482b1dd0d8ec556325ef5d57d5658502f8570`). Встроенный REST-контракт обновляется независимо: FIN-339 в эту доработку не входит.
