# Устройство bank-bff

Перенесён из `fin-assist/go-service-template` main `6ea35cea4b0ff68b831acadd22e2bc77e79481bd` (FIN-246), адаптация каркаса — FIN-261, публичный REST/SSE-API входа, профиля и помощника — [FIN-268](https://finassist.youtrack.cloud/issue/FIN-268), каталог пользовательских ошибок — часть [FIN-225](https://finassist.youtrack.cloud/issue/FIN-225) (объединённая задача [FIN-331](https://finassist.youtrack.cloud/issue/FIN-331)). BFF по `contracts/docs/grpc-api.md` не хранит собственного состояния: SQL, PostgreSQL, миграций и входящего gRPC business server нет. Домены, квоты и идемпотентные эффекты остаются в сервисах; BFF проверяет запрос по контракту, аутентифицирует пользователя и переводит gRPC ↔ REST.

## Жизненный цикл

`cmd/bank-bff` читает конфигурацию, создаёт slog с именем сервиса и версией и передаёт контекст SIGINT/SIGTERM в `internal/app.Run`. В `stage`/`prod` сначала собирается публичный API (`internal/app/api.go`): контракт, идентичность `go-identity` v0.2.0 `NewAssembly`, ленивые gRPC-соединения `go-grpc` v0.2.0 `client.Dial` к `user`, `assistant`, `bank-statement-data` и `analytics`, метрики и маршруты. Сборка ничего не загружает по сети и не запускает горутин; при ошибке всё уже созданное освобождается, а старт завершается exit 1 до привязки портов. Затем привязываются публичный и operational TCP listeners; ошибка второго bind освобождает первый. После обоих bind включается readiness и запускается обновление ключей идентичности (`Assembly.Run`: JWKS пользователя из `user.GetSigningKeys`, в `prod` — ключи API-сервера). Это единственный фоновый цикл BFF; он принадлежит библиотеке идентичности и останавливается вместе с listeners. Бизнес-заданий нет.

После SIGTERM readiness становится 503, публичный listener обслуживает запросы `SHUTDOWN_DELAY`, затем оба HTTP-сервера получают общий ограниченный контекст shutdown. Открытые SSE-потоки при этом сразу завершаются без терминального события (клиент переподключается и первым получает текущее состояние), поэтому shutdown не ждёт их до своего срока; по истечении срока оставшиеся соединения закрываются. Потом останавливается обновление ключей и закрываются gRPC-соединения. В `dev` публичный mux пустой (любой путь — 404): `NewAssembly` не поддерживает `dev`, и обхода авторизации нет.

Operational listener публикует только `GET/HEAD /healthz`, `/readyz`, `/metrics`; публичный — только `/v1/...`. Pprof и `DefaultServeMux` не используются.

## Публичные операции (FIN-268, FIN-342, FIN-361, FIN-360, FIN-359)

| Операция | Метод сервиса | Токен | `Idempotency-Key` |
|---|---|---|---|
| `GET /v1/operations` | `statementdata.OperationsService/ListFeed` без фильтров, иначе `SearchOperations` | да | — |
| `GET /v1/operations/{id}` | `statementdata.OperationsService/GetOperation` | да | — |
| `PATCH /v1/operations/{id}` | `statementdata.OperationsService/ChangeCategory` | да | — |
| `GET /v1/categories` | `statementdata.CategoriesService/ListCategories` | да | — |
| `PUT /v1/analytics/regular-payments/{id}/dismissal` | `statementdata.RegularPaymentsService/DismissRegularPayment` | да | — |
| `DELETE /v1/analytics/regular-payments/{id}/dismissal` | `statementdata.RegularPaymentsService/RestoreRegularPayment` | да | — |
| `GET /v1/analytics` | `analytics.AnalyticsService/GetAnalytics` | да | — |
| `GET /v1/analytics/periods` | `analytics.AnalyticsService/ListPeriods` | да | — |
| `POST /v1/auth/phone` | `user.AuthService/StartPhoneVerification` | нет | да |
| `GET /v1/auth/phone/events` (SSE) | `user.AuthService/WatchPhoneVerification` | `X-Verification-Token` → поле запроса | — |
| `POST /v1/auth/register` | `user.AuthService/Register` | нет | да |
| `POST /v1/auth/token` | `user.AuthService/RefreshTokens` | нет | да |
| `POST /v1/auth/logout` | `user.AuthService/Logout` | да | — |
| `GET /v1/consents/{type}` | `user.ConsentDocumentService/GetConsentDocument` | только `assistant` | — |
| `GET /v1/profile` | `user.ProfileService/GetProfile` | да | — |
| `PATCH /v1/profile` | `user.ProfileService/UpdateProfile` | да | — |
| `DELETE /v1/profile` | `user.ProfileService/DeleteAccount` | да | — |
| `PUT /v1/profile/assistant-consent` | `user.ProfileService/GrantAssistantConsent` | да | — |
| `DELETE /v1/profile/assistant-consent` | `user.ProfileService/RevokeAssistantConsent` | да | — |
| `GET /v1/assistant/messages` | `assistant.AssistantService/ListMessages` | да | — |
| `POST /v1/assistant/messages` | `assistant.AssistantService/AskQuestion` | да | да |
| `GET /v1/assistant/messages/{answer_id}/events` (SSE) | `assistant.AssistantService/WatchAnswer` | да | — |
| `POST /v1/assistant/messages/{answer_id}/retry` | `assistant.AssistantService/RetryAnswer` | да | да |
| `GET /v1/assistant/limit` | `assistant.AssistantService/GetLimit` | да | — |
| `GET /v1/statements/config` | `statementdata.StatementsService/GetImportConfig` | да | — |
| `POST /v1/statements` | `statementdata.StatementsService/UploadStatement` | да | да |
| `GET /v1/statements/{upload_id}/progress` (SSE) | `statementdata.StatementsService/WatchUpload` | да | — |
| `GET /v1/statements` | `statementdata.StatementsService/ListUploads` | да | — |
| `DELETE /v1/statements/{upload_id}` | `statementdata.StatementsService/DeleteUpload` | да | — |
| `GET /v1/statements/unread-lines` | `statementdata.StatementsService/ListUnreadLines` | да | — |

Все 30 операций встроенного OpenAPI доступны. Неизвестные пути и методы `/v1` отвечают JSON 404 `NOT_FOUND`; наличие HTTP-адаптеров не подтверждает сквозную готовность продукта к prod. Браузерный preflight `OPTIONS` (веб-клиент вне MVP) не обрабатывается.

## Порядок проверок запроса

Для каждого запроса `/v1` в порядке `docs/api.md`, «Ошибки»:

1. `X-Request-Id`: UUID клиента возвращается эхом и уходит в `x-request-id`; иное значение заменяется новым UUID. Свежий `Date` ставит `net/http` в каждом ответе, включая ошибки и воспроизведённые сервисом ответы.
2. `MAINTENANCE=on` → 503 `MAINTENANCE` с `Retry-After` и `until`.
3. `X-Client-Version` (ровно один заголовок, формат и минимальная версия платформы) → 400 `CLIENT_VERSION_UNSUPPORTED`.
4. Неизвестный путь или метод → 404 `NOT_FOUND`; `HEAD` (его нет в контракте, а шаблон `GET` в `ServeMux` его принимает) — тоже 404 до маршрутизации, без вызова сервиса и без слота SSE.
5. `Authorization` у защищённых операций — `identity.HTTP` (подпись, `exp`, `iss`, `aud`, непустой `sub`) → 401 `INVALID_TOKEN`. Если свежих ключей нет (сбой `user`), токен не оценивается: 500 `INTERNAL_ERROR`, пользователь не выходит из аккаунта (FIN-293).
6. У шести JSON-операций FIN-339 `Content-Encoding` отсутствует либо содержит один `identity` без учёта регистра и HTTP OWS. Unsupported/пустой/список/повторный заголовок → 400 `VALIDATION_ERROR` с единственным detail `Content-Encoding` / `INVALID_FORMAT`, до размера, чтения, декодирования и проверки `Idempotency-Key`, без RPC. BFF ставит `Connection: close` и пропускает свой application drain; заголовки проверяются после приоритетных gates и `Authorization`.
7. JSON-тело: не больше `JSON_BODY_LIMIT` (по `Content-Length` и по фактическому чтению) → 413 `REQUEST_TOO_LARGE` ([FIN-339](https://finassist.youtrack.cloud/issue/FIN-339)); у операций без тела любое тело — 400, читается не больше одного байта.
8. Схема `openapi.yaml` (kin-openapi): параметры пути и query, заголовки, `Content-Type`, тело; затем `Idempotency-Key` обязателен для соответствующих операций и должен быть UUID. В обработчик попадают только члены тела с точными именами схемы: `encoding/json` сопоставляет имена без учёта регистра, и `{"text":"ok","Text":""}` иначе заменил бы проверенный текст. Все нарушения — один 400 `VALIDATION_ERROR` с `details[]` (`REQUIRED`, `INVALID_FORMAT`, `OUT_OF_RANGE`). Длина строк считается в кодовых точках Unicode, как «символы» `docs/api.md` (kin-openapi считает единицы UTF-16). Неизвестный `transfer_mode` вопроса — 400.
9. Вызов сервиса с дедлайном 5 с (SearchOperations — 10 с, UploadStatement — 60 с, WatchUpload — 4 мин) (меньше `WriteTimeout`, поэтому ответ об истечении успевает уйти).

Идемпотентность, лимиты попыток, согласия и проверки токенов запроса выполняют сервисы; BFF ключ не хранит и запрос не повторяет.

FIN-364: POST /v1/auth/phone требует четыре поля: phone, pd_consent_accepted, pd_consent_version, age_confirmed. Отсутствие/null/тип и версия вне 1–64 Unicode-символов отклоняются схемой до USER. Boolean false структурно допустим: USER сначала обрабатывает ключ, лимиты/занятость, затем возраст/формат, согласие и актуальность версии. Поэтому BFF передаёт false без подмены и проецирует 400 (age_confirmed/REQUIRED), 403 CONSENT_REQUIRED и 422 CONSENT_OUTDATED через finerr. Локальный bool==true gate отменил бы приоритет409/429.

Доказательство, серверные моменты получения согласия/возраста и commit перед телефонным провайдером принадлежат USER. В RPC идут четыре исходных значения и прежняя allowlist metadata: trusted-proxy-derived x-client-ip, ключ и версия клиента. Произвольный HTTP X-Client-IP, клиентские timestamps и User-Agent доказательством не становятся. Register передаёт исходные PD assertions; текущий ConsentDocument заново не читает. BFF не вычисляет новый expires_at/TTL и не кэширует ответ: expires_in/RetryInfo приходят свежими от USER, Date формируется HTTP-сервером, все ответы no-store, скрытого повтора Start нет.

## Исходящие вызовы и metadata

`client.Dial` создаёт свежий outgoing metadata с сервисным токеном (`x-service-authorization`) и `x-request-id`; затем `identity.UnaryClient`/`StreamClient` добавляют `authorization` только с проверенным этой библиотекой access-токеном пользователя (непроверенный заголовок анонимной операции не пробрасывается); затем `rest.UnaryHints`/`StreamHints` добавляют разрешённый список: `x-client-ip` (адрес клиента, см. ниже), `x-client-version`, `x-read-primary: 1` (только для `X-Read-Primary: 1`), `idempotency-key` (только у операций, где он обязателен). Адрес клиента — адрес TCP-пира. Если пир входит в `TRUSTED_PROXIES` (nginx стенда, ingress), `X-Forwarded-For` читается справа налево и адресом клиента считается первый элемент вне доверенных сетей: каждый прокси дописывает увиденный адрес, поэтому верить можно только элементам, дописанным доверенными прокси. Всё левее первого недоверенного элемента пишет клиент, и BFF этого не читает: неразборчивый или длинный префикс результат не меняет. Если не разбирается элемент, дописанный доверенными прокси, или просмотрено больше 16 элементов, адресом остаётся последний доверенный узел; нет заголовка — адрес пира. От недоверенного пира заголовок игнорируется. При пустом `TRUSTED_PROXIES` за прокси у всех клиентов один адрес — адрес прокси, и предел `SSE_MAX_STREAMS_PER_IP` становится общим. Этот же адрес используют предел SSE-потоков и журнал. Другие заголовки HTTP в сервисы не попадают; `X-Verification-Token` передаётся полем `WatchPhoneVerificationRequest`. Повторы на `UNAVAILABLE` — только чтения: `GetSigningKeys`, `GetConsentDocument`, `GetProfile`, `ListMessages`, `GetLimit`, `GetImportConfig`, `ListUploads`, `ListUnreadLines`.

## Ошибки

`internal/rest` переводит ошибку сервиса по `ErrorInfo.reason` через `finerr.ToREST` (`gen/go/v0.7.0`): HTTP-статус реестра, `details[]` из `BadRequest` (только `field` ограниченной формы и `code` из `REQUIRED`/`INVALID_FORMAT`/`OUT_OF_RANGE`, иной код — `INVALID_FORMAT`; `message` — собственный текст BFF, `description` сервиса клиенту не передаётся), `retry_at`/`resets_at` из metadata, `Retry-After` из `RetryInfo` (свежая длительность сервиса, не пересчёт сохранённой). Неполная ошибка, неизвестный reason, gRPC-код, не совпадающий с реестром для reason, голые `UNAVAILABLE`/`DEADLINE_EXCEEDED`/`PERMISSION_DENIED`, а также статус реестра, которого операция не объявляет в `openapi.yaml`, — 500 `INTERNAL_ERROR` и `finassist_unmapped_error_total`. `status.message` и тексты сервисов клиенту не передаются и не журналируются.

`user_message`, `severity` и диагностический `message` задаёт единый каталог BFF (`internal/catalog`, FIN-225). Он фиксированный: в текст не подставляются ввод пользователя, сообщения сервисов, SQL, ответы провайдеров или модели. Неизвестный код получает общий безопасный текст. Для помощника: терминальное событие `error` и failed-сообщения истории получают `user_message`/`severity` и прежние `text`/`error_text` одним текстом каталога; `retryable` — разрешение `RetryAnswer` от сервиса (с presence у истории). Неизвестный legacy-код `AnswerErrorCode` — консервативный `INTERNAL_ERROR`.

FIN-342: каждый REST-отказ нового BFF содержит `error.retryable`, включая явный `false`. `finerr.ToREST` сохраняет optional `RequestRetryInfo` сервиса; `true` выдаётся только при явном разрешении и отдельной декларации `Route.ReplaySafe` для всего внешнего вызова. Сейчас такая декларация есть у чтений GET, идемпотентных DELETE аккаунта, PUT/DELETE согласия помощника, DELETE загрузки, PATCH категории и PUT/DELETE «Не подписка». Она не переносится автоматически на новые маршруты и не означает безопасность только одного дочернего RPC. PATCH профиля получает `false`: docs/api.md 2.2 не объявляет безопасность повтора всего REST-вызова. Все POST пока получают `false`: BFF не хранит достоверный возраст/срок ключа, а UUID не доказывает окно 24 часа или 60 секунд refresh. Отсутствующий advice, неизвестный/некорректный статус и собственные отказы проверок BFF также дают `false`. Собственный временный отказ лимитера SSE — исключение с явным решением транспортного обработчика: `Call.Refuse(error, true)` соединяет его permission с той же доказанной безопасностью всего GET. Отказ происходит до RPC и не занимает слот; клиент может переоткрыть поток после свежего ожидания. Положительный `Retry-After` сам по себе ничего не разрешает. Корректирующие сценарии авторизации/согласия/техработ остаются отдельными правилами контракта.

`AnswerError.status` терминального события и `Message.error` failed-истории разбираются общим `finerr.DecodeResultStatus`. При отсутствии Status применяется прежний адаптер. Присутствующая корректная причина открыта и имеет приоритет над legacy enum; неизвестная каталогу причина сохраняется с общим безопасным текстом помощника. Присутствующий некорректный Status даёт `INTERNAL_ERROR`, а legacy enum не читается. `RequestRetryInfo` и относительный `RetryInfo` во вложенном результате запрещены самим декодером. Ни raw `Status.message`, ни metadata не становятся пользовательским текстом. Совпадение причины с REST `MAINTENANCE`/`CONSENT_REQUIRED` не превращает успешное чтение в HTTP-отказ и не создаёт REST-восстановление.

Внешнее разрешение новой генерации сохраняется независимо от Status: `AnswerError.retryable` авторитетно, включая `false`; у `Message.retryable` сохраняется presence (отсутствие, false, true). По пустому/непустому тексту или коду разрешение не угадывается. Старый сервис без FIN-159 proto3 bool различить не позволяет: необходима граница выкладки из `docs/config.md`. Переход producers на dual-write и их usecase-owned unary advice выполняется после обновления декодеров; этот PR не меняет сервисы и не подтверждает фактическую выкладку. Импорт использует `ImportStreamError.status`: отсутствие — legacy enum, корректный открытый reason сохраняется, некорректный присутствующий Status — PROCESSING_FAILED без восстановления legacy CANCELLED. Каталог импорта заново формирует безопасные user_message/severity.

## Ответы и SSE

Моменты форматируются в поясе ответа (`time_zone` сервиса; профиль — `profile.timezone`; при отсутствии пояса — `Europe/Moscow`), RFC 3339 со смещением. Перечисления переводятся по имени через `protoreflect`; неизвестное значение обязательного поля — по правилу поля: тема → `system`, статус ответа → `failed`, покрытие точки → `partial`, роль → сообщение не показывается, чип с неизвестным экраном не передаётся, блок, который BFF не может перевести, остаётся на своей позиции с типом `unsupported` и `alt_text`. `changed_since_text` и `profile.consents[]` собирает BFF. FIN-368 добавляет открытые коды `unverified` и `time_zone`: готовые тексты «Актуальность данных этого ответа не подтверждена» и «Обновились настройки часового пояса» одинаковы в истории, snapshot и source потока; при сочетании с прежними кодами тексты соединяются точкой. Неизвестная gRPC-причина означает `unverified` с готовым текстом и прежней метрикой; пустой `changed_since` означает подтверждённое равенство. Векторы версий остаются внутренними данными сервисов. Пустая `currency` → `RUB` и `finassist_empty_required_field_total`.

SSE-потоки (`WatchPhoneVerification`, `WatchAnswer`, `WatchUpload`): ошибка до первого сообщения сервиса — обычный HTTP JSON-ответ (404 чужого ответа, 401/429 заявки); первым событием идёт текущее состояние; терминальное событие закрывает поток. Сбой сервиса или транспорта после начала потока закрывает его без терминального события. Heartbeat `: ping` — каждые 15 с. Поток продлевает собственные read/write deadline соединения (`http.ResponseController`) до своего срока плюс 10 с ещё до ожидания первого сообщения сервиса (ответ 200 при этом не фиксируется), поэтому `ReadTimeout`/`WriteTimeout` unary-запросов не обрывают ни поток, ни поздний JSON-отказ сервиса; собственный срок — 4 мин для ответа помощника и импорта, 6 мин для заявки, после него поток закрывается без терминального события. Отключение клиента отменяет поток к сервису; генерация ответа от него не зависит. Пределы `SSE_MAX_STREAMS` и `SSE_MAX_STREAMS_PER_IP` дают 429 `RATE_LIMITED` со свежим `retry_at` до вызова сервиса.

## Защита HTTP

ReadHeaderTimeout 5s, ReadTimeout 10s, WriteTimeout 30s, IdleTimeout 60s, MaxHeaderBytes 1 MiB. Непрочитанный остаток тела после раннего ответа (413, 401, 404) отбрасывается в пределах 256 КиБ, чтобы клиент, ещё пишущий тело, получил ответ, а не сброс соединения. net/http делает это сам только для keep-alive соединений; тело с объявленным `Content-Length` больше 256 КиБ не читается вовсе, его соединение всё равно закрывается. Логи — одна строка `request` на запрос: `request_id`, метод, шаблон маршрута, статус, длительность, метка клиента; без путей с ID, query, заголовков, тел, токенов и телефонов.

## Метрики

| Метрика | Метки |
|---|---|
| `finassist_http_requests_total` | `route` (шаблон), `status`, `client` (платформа из `CLIENT_MIN_VERSIONS`: версия любой допустимой формы прошла бы шлюз и засорила бы метку) |
| `finassist_http_request_duration_seconds` | `route` |
| `finassist_unmapped_error_total` | `reason` (форма реестра, не больше 32, затем `other`; `none`, `invalid`), `grpc_code` |
| `finassist_untranslated_code_total` | `field` (константы вызывающего кода), `code` (`unknown`, `content`, `absent`, `invalid`, `legacy_mismatch`) |
| `finassist_empty_required_field_total` | `field`: `Block.lock.text`, `MetricLock.text`, `GetAnalyticsResponse.currency`, `Message.currency`, `AnswerSnapshot.currency` (ряды с 0 при старте) |
| `finassist_sse_streams` | `route` |
| `finassist_sse_rejected_total` | `route`: SSE и POST /v1/statements, отклонённые локальным бюджетом |
| `grpc_client_handled_total`, `grpc_client_handling_seconds` | `peer`, `method`, `code` (go-grpc) |
| `finassist_identity_rejected_total`, `finassist_identity_jwks_skipped_keys` | go-identity |

Плюс стандартные Go/process collectors. `user_id`, телефонов и свободного текста в метках нет.

## Контракт

Копия `openapi.yaml` встроена в бинарь (`internal/apispec`, источник — `apispec.Source`: `fin-assist/contracts` `928bac003ce1a9ea4f71e34a0f51335b748d9ac3`, включает FIN-367). При обновлении контракта копия заменяется в PR вместе с кодом. При загрузке полностью проверяются схемы, значения по умолчанию и документационные примеры. Используемый Go-модуль контрактов — `gen/go/v0.7.0` (опубликованный тег на `928bac003ce1a9ea4f71e34a0f51335b748d9ac3`). FIN-339: у шести согласованных JSON-операций проверка всех Content-Encoding предшествует размеру, чтению и декодированию. Недопустимый encoding даёт 400 без чтения тела приложением и без RPC; BFF пропускает свой drain и запрашивает закрытие HTTP/1-соединения. Остальные bodyless/upload правила сохраняются.


## Потоковый импорт (FIN-342)

POST /v1/statements выбирает Route.Input: общая JSON-ветка остальных операций не меняется. До endpoint проверяются OAS заголовки/параметры и UUID ключа; endpoint принимает ровно одну часть file. Файл ограничен фактическими 10 МиБ, оболочка multipart — отдельно 64 КиБ; до разбора первого и возможного второго MIME-заголовка действует оставшийся бюджет оболочки (включая уже прочитанные буфером байты), затем тот же предел ограничивает epilogue; Content-Length всей формы не считается размером файла. Заявленный Content-Length выше их суммы отклоняется 413 до RPC, меньшая заявленная длина не заменяет фактическую проверку. Content-Disposition разбирается mime.ParseMediaType: имя сохраняется целиком, без Part.FileName()/Base, усечения, нормализации Unicode, регистра или пробелов. Явный filename="" допустим и передаётся пустым по FIN-297; отсутствие параметра filename — VALIDATION_ERROR до RPC. NUL и неверный UTF-8 — VALIDATION_ERROR. Расширение и содержимое проверяет сервис.

В RPC сначала идёт header, затем полные chunks по 256 КиБ и последний хвост не больше 256 КиБ. BFF не буферизует полный файл, не сохраняет его и не пишет содержимое/имя в логи. gRPC EOF отправляется лишь после проверки всей формы; лишняя часть, повреждение или превышение лимита отменяют RPC до финального hash lookup/эффекта. FILE_TOO_LARGE — 413 до lookup в сервисе. Service обязан не фиксировать загрузку до EOF. Ранний typed отказ сервиса из Send/Recv сохраняется как обычный HTTP отказ. Хеш SHA256(SHA256(content) || raw filename UTF-8) и replay принадлежат сервису; BFF не вычисляет его и не обновляет ключ.

ImportResult не содержит currency в proto; REST получает предусмотренный текущим контрактом RUB. Семь известных feature и две notice получают фиксированные BFF подписи, open/пороги остаются данными сервиса. У неизвестных feature/notice нет текста в текущем proto — пропускается только элемент и считается ограниченная метрика. UnreadLine.reason — открытый статистический код: неизвестное число (включая UNSPECIFIED) получает транспортный fallback unknown и ограниченную метрику UnreadLine.reason/unknown. Вся строка и готовый reason_name сохраняются; пустая подпись остаётся пустой. Это не новая доменная причина и не основание для действий клиента. Неизвестный Upload.status отображается как done по явному правилу REST 3.4. Пара period_from/period_to выводится вместе. Непрочитанные строки допускают только upload_id XOR from+to с from<to.

GET и известный идемпотентный DELETE объявляют безопасность всей операции; положительный REST retryable требует explicit service advice. POST всегда false без доказанного окна исходного ключа. SSE transport failure после 200 только закрывает соединение; терминальное error — исключительно сохранённый бизнес-результат сервиса.

Загрузки имеют отдельный от SSE бюджет UPLOAD_MAX_STREAMS=16 и UPLOAD_MAX_STREAMS_PER_IP=2. Слот берётся до чтения формы и RPC; локальный 429 содержит свежее ожидание UPLOAD_BUSY_RETRY_AFTER и retryable=false, потому что срок исходного POST-ключа не доказан. Отмена, ошибка и успех освобождают слот; при shutdown активные upload HTTP-запросы дренируются в пределах SHUTDOWN_TIMEOUT; они не отменяются вместе с SSE. После срока HTTP-соединение закрывается принудительно и отменяет связанный RPC.

Разбор первого MIME-заголовка получает временный read-бюджет 64 КиБ + 1 байт до NextRawPart: stdlib иначе допускает заголовки до 10 МиБ. После завершения заголовка этот бюджет снимается, остаются общий wire-limit и окончательный предел оболочки. StreamingInput проверяет клон с http.NoBody: kin-openapi 0.133 читает тело в security validation даже с ExcludeRequestBody; реальная identity уже проверена, headers/query/path остаются исходными.

## Настройки и удаление профиля (FIN-361)

`PATCH /v1/profile` передаёт в `UpdateProfile` только заданные `theme` и `timezone`: `{}` не меняет настройки, `system` остаётся явно заданной темой, неизвестные поля и имена с другим регистром не подменяют проверенные значения. Закрытый набор тем и типы проверяет OpenAPI; существование IANA-пояса проверяет `user`. BFF не вводит задержку 72 часа — это правило клиента. Ответ — профиль из RPC, включая пояс и согласия; время регистрации форматируется в возвращённом поясе. Пересчёт и lifecycle/outbox остаются ответственностью сервисов.

`DELETE /v1/profile` вызывает `DeleteAccount` без тела и отвечает `204` без JSON. BFF проверяет подпись действующего access-токена и не кеширует удаление: повтор до истечения токена снова доходит до идемпотентного RPC и получает `204`. Последующие операции профиля получают `TOKEN_REVOKED` от `user`; новый запрет токенов на стороне BFF не вводится. Отзыв refresh-токенов и доставка удаления всем соседям выполняются в `user`.

Оба маршрута используют verified identity, разрешённые metadata, unary-дедлайн 5 секунд и общую безопасную трансляцию `finerr`. У них нет `Idempotency-Key` и автоматического gRPC-повтора; DELETE объявлен безопасным для повтора полного REST-вызова, но `error.retryable=true` по-прежнему требует явного разрешения сервиса. PATCH сохраняет `retryable=false` даже при положительном advice: контракт 2.2 не обещает безопасность полного повтора. Текущий `user.Update` не создаёт повторное событие для той же настройки, но BFF не расширяет гарантию REST на основании одной реализации.

## Аналитика (FIN-360)

Оба GET передают проверенный токен пользователя и `X-Read-Primary` общей цепочкой metadata.
`period` по умолчанию month, `transfer_mode` — with; неизвестный код запроса отклоняется400,
поскольку enum текущего protobuf его не представляет. `date` передаётся как `period_key` без календарного
преобразования; выбор фактического периода остаётся в analytics. `supports=type@v` разбирается в список
с положительным int32 v, `exp=key:variant` — в map; неизвестные типы/эксперименты передаются сервису.
Неверная форма этих пар отклоняется400 до RPC. Каждый вызов ограничен5с, оба RPC входят в read-retry allowlist.

Ответ переносит `blocks` в исходном порядке, включая неизвестные примитивы, версии и строковые коды.
`data` — открытый JSON-объект без разбора вложенных сумм, basis, lock, filters и действий. Типизированные
моменты period/state/верхнего lock/action.filter форматируются в time_zone ответа с сохранением наносекунд;
календарные ключи и уже сформированные строки Struct не меняются. Период включает gaps и coverage,
соседние ключи — явный null при отсутствии, пустые списки — `[]`. Старые поля tiles/insights не читаются.
`has_data=false` даёт только этот флаг и не считает currency дефектом. Неизвестная coverage — partial;
непредставимый обязательный params.period — безопасный500 и ограниченная untranslated-метрика.

Пустая currency при наличии данных временно заменяется на RUB (FIN-98); пустой верхний Block.lock.text —
на «Пока недоступно». Каждый случай увеличивает `finassist_empty_required_field_total`; все пять контрактных
серий (`Block.lock.text`, `MetricLock.text`, `GetAnalyticsResponse.currency`, `Message.currency`,
`AnswerSnapshot.currency`) создаются нулями при старте. Вложенные lock.text внутри data не меняются.
Финансовые формулы, пороги, блокировки, пересчёт и самостоятельные fallback-снимки в BFF отсутствуют.

FIN-400 использует contracts/gen/go v0.8.0 и встроенную спецификацию FIN-409 (`22c5213`).
Оба GET принимают `period=all_time`; необязательный `date` переносится как есть, включая точный
singleton `all_time`. Смешанные календарные ключи и all_time валидирует analytics с каноническими
`date`/`INVALID_FORMAT`, а BFF сохраняет HTTP400 и не раскрывает внутреннее описание ошибки.
Обязательный ответный ключ ALL_TIME должен быть `all_time`, календарный ответ не принимает этот
singleton. Границы, режим переводов, состояние, навигация и состав блоков остаются данными сервиса.
`tile_group@2` и действия точек внутри Struct передаются без изменений. Необязательные типизированные
`BlockAction.period`/`period_key` проецируются как `period`/`date`; неизвестный enum не становится
разрешённым клиентским переходом.

Чипы помощника не согласовывают возможности периода с клиентом. Поэтому all_time, неизвестный тип
периода и некорректный календарный ключ исключают только чип, сохраняя ответ и остальные чипы.
Одинаковый mapper применяется к истории, reconnect-snapshot и событиям `chip`; календарные переходы
с точным ключом month/quarter/year сохраняются. Это не разрешает assistant формировать all_time-чипы.

Обязательные моменты period.range/state и границ gaps/periods проверяются до HTTP200: отсутствие или
некорректный Timestamp даёт безопасный500 и ограниченную диагностику поля/absent/invalid. Отсутствующие
params/period/state/bounds и пустой period_key также диагностируются по точному полю. Неизвестная coverage
сохраняет fallback partial и увеличивает untranslated по соответствующему полю ответа.
Empty-required считает полученные пустые currency/верхние lock.text и до отказа из-за другого поля, как
требует grpc-api5.3. Серия MetricLock.text сохранена по контракту, но постоянно нулевая: устаревшая typed
модель метрик не обслуживается BFF. Она не измеряет вложенные TileLock внутри Struct; их проверяет клиент.

Calendar keys params.period_key, periods.key и присутствующие navigation.previous/next проверяются по
формату PeriodKey встроенного OpenAPI; пустой обязательный ключ даёт absent, неверный формат — invalid и безопасный500.
В navigation nil означает отсутствие; присутствующая пустая строка — invalid, как прочий неверный формат.
Присутствующие optional Timestamp нового ответа также проходят CheckValid: bounds.data_from/data_to,
state.last_operation_at, верхний lock.available_from и action.filter.from/to. Отсутствие остаётся отсутствием;
невалидное значение —500 с точным proto-путём/invalid. Диагностика TimeRange использует proto-имена start/end,
а не REST from/to. Вложенные Struct-поля остаются непрозрачными; другие converters не менялись.

Граница деградации: правило клиента «не удалось разобрать блок — пропустить» применяется к чтению уже
полученного REST-ответа и неизвестным примитивам/действиям. Оно не требует BFF отправлять невалидный
RFC3339 date-time при преобразовании типизированного protobuf Timestamp. Если typed-момент верхнего lock
или action.filter невозможно корректно перевести, BFF отвечает безопасным500 на весь запрос и считает
диагностику; блоки самостоятельно не пересобирает и не отбрасывает. Это не полная валидация схемы всех
свойств Block: v/payment_id/amount и opaque Struct по-прежнему переносятся по существующим правилам.
Другие converters здесь не меняются; общий JSON-каталог блоков не означает новую общую реализацию.

UTC Timestamp ограничен protobuf диапазоном0001–9999. После перевода в пояс ответа локальный год0000
допустим по RFC3339 (`date-fullyear = 4DIGIT`); отрицательный или пятизначный год не допускается.
[Формат RFC3339](https://www.rfc-editor.org/rfc/rfc3339#section-5.6),
[диапазон Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp).

## Операции, категории и «Не подписка» (FIN-359)

Все семь RPC используют существующее соединение `STATEMENTS_GRPC_ADDR`, проверенный access-токен и
allowlist metadata. Наличие любого фильтра (`from`, `to`, `all_time`, `q`, `category_id`, `kind`,
`amount_from`, `amount_to`, `transfer_mode`, `selection`) выбирает поиск; `before` с фильтрами и
`all_time` с границами периода отклоняются 400 до RPC. Неизвестные kind/transfer_mode отклоняются 400,
selection передаётся непрозрачно, selection_name не используется. Семантику времени before, диапазонов,
поиска и selection проверяет сервис. SearchOperations ограничен 10 с/16 МиБ ответа только для этого вызова;
остальные новые RPC — 5 с/4 МиБ. Для ChangeCategory/Dismiss/Restore BFF не делает скрытых transport-повторов: это локальное исключение BFF
из общего разрешения grpc-api.md §6/9 для идемпотентных эффектов. При split/join ChangeCategory заменяет
ID запроса; после применённой правки и потери ответа повтор старого ID вернул бы NOT_FOUND вместо
изменённых items/replaced_id. Идемпотентность эффекта не обещает стабильный ответ на такой повтор.
REST retryable остаётся отдельным решением и требует explicit RequestRetryInfo сервиса.

Время возвращается в time_zone сервиса через RFC3339Nano без потери долей секунды. Лента/поиск не
отдают description/posted_at/note_details/bank_category/mcc/source_name и spoken; детали и результат
правки отдают их по наличию. Счета пары, отрицательные итоговые суммы после возвратов, валюты и тексты
переносятся без финансовых вычислений. Неизвестный обязательный enum операции, отсутствующий/невалидный
момент, отрицательная сумма операции или неполная оболочка ленты дают безопасный 500 и ограниченный
счётчик finassist_untranslated_code_total (field/code из констант); пустая валюта операции также 500,
временный RUB fallback FIN-98 относится только к оговорённым ответам analytics/assistant. Неизвестные
направления категории пропускаются со счётчиком; остальные поля категорий, включая icon, сохраняются.
PUT/DELETE отметки принимают только каноничный lowercase UUID и отвечают 204 без тела.

Некорректный optional `BlockAction.period_key` опускается с диагностикой `Block.action.period_key/invalid`; корректный по форме ключ сохраняется, а применимость сочетания period/date решает клиент. Ключ самого analytics-ответа и списка периодов обязан точно соответствовать типу периода. Фильтрация analytics-чипа учитывается как `Chip.params/unsupported`, отдельно от неизвестного screen.
