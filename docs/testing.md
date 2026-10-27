# Проверки bank-bff

FIN-261 перенёс инфраструктуру HTTP BFF из go-service-template с независимыми gates FIN-238; FIN-268 добавил публичный API. Все три набора инструментируют одинаковый весь production-модуль (`-coverpkg=./...`), без исключений. Каждый требует total80 statements через закреплённый `go-test-coverage v2.20.1`; принятое округление 79,95% → 80,0%. Combined — справочное объединение штатным инструментом, не компенсация провала уровня.

- `TestUnit…`: конфигурация и её документация; operational handlers без сети; command usage/ошибки конфигурации; lifecycle на фейках consumer-интерфейса — отмена, отказ Serve, ограниченный shutdown, сохранение ошибок, снятие readiness.
- `TestIntegration…`: настоящие HTTP/TCP listeners полной команды, неверный HTTP, независимость портов, ошибки bind, освобождение частично привязанных портов, конфигурация и логи в реальной сборке.
- `TestUnit…` публичного API: `internal/rest` (порядок проверок, валидация по схеме, перевод gRPC-ошибок, SSE), `internal/endpoint` (каждая операция на фейках интерфейсов клиентов: успех, все ветки перевода, неизвестные значения перечислений), `internal/catalog`, `internal/apispec`, сборка `newPublicAPI` без сети и её отказы.
- `TestIntegration…` и `TestAPI…` публичного API — один набор сценариев (`internal/app/scenarios_test.go`) на двух сборках: integration — `newPublicAPI` за `httptest`-сервером, API — полный `app.Run` с настоящими listeners; сервисы `user`, `assistant` и `bank-statement-data` — фейковые gRPC-серверы на bufconn, проверяющие сервисный stage-токен BFF и metadata. Каждый ответ проверяется по `openapi.yaml` через kin-openapi (статус должен быть объявлен операцией, обязателен `Date`), SSE — по событиям `x-sse-events`. Сценарии: успех всех 30 операций; каждая ошибка реестра, которую операция объявляет, и неизвестный reason → 500; отсутствующий, неверный и просроченный токен; сбой ключей (500, не 401); отсутствующий и неверный `Idempotency-Key`; тело вне схемы и слишком большое тело; неподдерживаемая и отсутствующая версия клиента; MAINTENANCE; X-Request-Id; разрешённый список metadata; SSE — первое событие, heartbeat, терминальные события, ошибка до потока, предел потоков, отключение клиента, поток длиннее `WriteTimeout`, поздний JSON-отказ сервиса после `WriteTimeout` для обоих потоков, собственный срок потока, завершение потоков при shutdown; пооперационная матрица отказов сервиса (`OperationErrors`: объявленные ошибки сохраняют статус, необъявленные и транспортные — 500, без токена — 401 без вызова); порядок «токен → тело» (без токена большое тело и тело вне схемы — 401); доверенные прокси (`X-Forwarded-For` только от `TRUSTED_PROXIES`, цепочка прокси, предел потоков на адрес клиента); регистрозависимое чтение тела. Integration-сборка поднимает `httptest`-сервер с теми же таймаутами и shutdown, что `app.Run`. `cmd/bank-bff` дополнительно запускает команду `serve` в `stage` с пирами по TCP.
- FIN-342: `StreamLimit` проверяет локальный 429 с `retryable=true` у обоих SSE-маршрутов без вызова сервиса и успешное переоткрытие после освобождения ёмкости. `RequestRetryAdvice` на обоих транспортных уровнях проверяет serialized service advice (нет/false/true), безопасность полного GET/PUT/DELETE, unknown-age keyed POST, локальный отказ и JSON-отказ до первого SSE с сохранённым ожиданием. `ResultStatus` проверяет canonical/legacy приоритет, открытые и совпадающие с REST причины, present-invalid (пустой Status, request advice, относительное ожидание, дубликаты details), сохранение true/false новой попытки и отсутствие утечек диагностики в SSE/историю. Ответы и события проходят тот же независимый валидатор OpenAPI. Unit-тесты дополнительно проверяют JSON-presence и закрытый по умолчанию маршрут без доказанной safety.
- 413 `REQUEST_TOO_LARGE` пока не объявлен во встроенном контракте ([FIN-339](https://finassist.youtrack.cloud/issue/FIN-339)); тест проверяет этот ответ без валидации по схеме и явно фиксирует расхождение.
- `make test-names` (FIN-290): в inventory входят `Test…`, `Fuzz…` и `Example…`; неклассифицированные Test/Fuzz и любой Example отклоняются, collectors выбирают `^(Test|Fuzz)<уровень>`.
- `TestAPI…`: полная команда и настоящий HTTP: health/readiness/metrics на operational порту; 404 на публичных путях вне реализованных операций и в `ENV=dev`; отсутствие operational endpoint на публичном порту; остановка и закрытие соединений; readiness 503 перед закрытием; отказ неверной конфигурации и занятого порта, отсутствие необходимости в БД/dev-auth.

`make test` проверяет tidy/vet, все Go-тесты с race/count1/timeout и Python guard/release tests. `make cover` собирает каждый набор и требует total80 для каждого. Пустой/skipped/failed suite, отсутствующий/пустой/header-only/zero-statement профиль — ошибка; пакеты не исключаются ради процента. `make test-names` до каждого collector отвергает имена вне трёх selectors. Отчёты и JSON/Go-профили публикуются артефактами CI, required job `go` остаётся строгим. Сервис не использует библиотечный N/A.

PostgreSQL и testcontainers каркасу без БД не нужны; интеграционные тесты используют настоящий TCP/HTTP. При появлении stateful зависимости тесты должны использовать её настоящую реализацию. UI-тесты не входят в эту задачу.

CI также запускает lint и сборку distroless/nonroot образа с HTTP smoke-проверкой обоих портов и SIGTERM. Версии инструментов — Makefile и workflow; toolchain — `go.mod`. Публикация GHCR/deploy и версия сервиса выполняются отдельно.

Для Python regression-тестов workflow нужен Node.js 20+ (`node` в PATH). Проверяется реальный read-only JS gate: fork/удалённый head/чужая base/закрытый PR/ошибка API не могут включить write-enabled job; отдельные issues не запускают ревью.

FIN-342: сценарии ImportRead/ImportUpload/ImportWatch/ImportErrors запускаются на обеих транспортных сборках. Проверяются все шесть маршрутов, raw имя (включая явно пустое при непустом opaque content и запрет отсутствующего параметра filename), финальный EOF и чанки, отклонение лишней части с заголовком 5 МиБ без финализации. Unit проверяет фактический предел чтения при первом заголовке 0/60 КиБ, а также при уже отрицательном остатке после первого заголовка 65 000 байт и файла допускается только ранее выполненное чтение вперёд MIME до 4 КиБ, но не продолжение разбора; transport-наборы отдельно проверяют 400 и отсутствие service EOF/commit. Также проверяются предел файла и формы, отсутствие финализации неверной формы, ID/токены/metadata, взаимоисключающие фильтры, enum fallback, Status presence/invalid/unknown, safe catalog, конечные события и transport-close без выдуманного результата. Хранилище и парсер проверяются в bank-statement-data отдельно.

ImportUnknownReason проверяет обязательный reason: UNSPECIFIED и неизвестное число сохраняют всю строку и reason_name с транспортным reason=unknown; известные reason/reason_name возвращаются без изменения. Unit дополнительно проверяет одну ограниченную серию метрики unknown и отсутствие изменения protobuf-ответа сервиса.

ImportEarlyLength отправляет только HTTP-заголовки с Content-Length 1 ГиБ и получает 413 без RPC. ImportStreamingBeforeEOF доказывает отправку gRPC header до завершения HTTP-тела. ImportCapacity проверяет независимые global/per-IP upload лимиты, отсутствие RPC при 429, retryable=false, свободный SSE и освобождение ёмкости. UnitInitialHeaderReadBudget ограничивает фактические прочитанные байты большого MIME-заголовка; чанки, усечённая граница и отсутствие прогресса проверяются отдельно.

ImportShutdown проверяет начатую upload-загрузку через реальный HTTP: после остановки listener она завершается202 до ShutdownTimeout без отмены RPC; незавершённое тело после срока закрывается принудительно и отменяет RPC. Integration-harness вызывает http.Server.Close после bounded Shutdown, как Run; один httptest.Server.Close сам по себе ждёт активные запросы. ImportCapacity дополнительно проверяет существующий finassist_sse_rejected_total по route=/v1/statements на обоих уровнях.

ImportMultipartEdges наблюдает синхронную инициацию реального gRPC client stream: запаздывающий dispatch предыдущего отменённого запроса не может исказить проверку отсутствия RPC у следующей неверной формы. Отмена уже начатого truncated-запроса проверяется отдельно; реальный HTTP/bufconn и проверки отсутствия фиксации сохранены.

FIN-361: `ProfileMutations` на обоих транспортных уровнях проверяет `{}`, каждую тему, отдельный и совместный `timezone`, presence в сериализованном protobuf, точные имена полей, ответ и смещение времени, metadata и unary-дедлайн. Некорректные типы/null/тема/JSON/Content-Type отклоняются до RPC; неверный IANA-пояс возвращает безопасный структурированный `VALIDATION_ERROR` сервиса. `ProfileDeletion` проверяет пустой 204 и повтор с живым токеном, последующий `TOKEN_REVOKED`, независимость другого токена, отказ тела и неверных/чужих/просроченных токенов до RPC. `OperationErrors`, `RequestRetryAdvice`, `Gates` и `Maintenance` включают оба новых маршрута: ошибки реестра и неизвестные ошибки, explicit advice (PATCH остаётся retryable=false), версия клиента и техработы. Unit-тест сверяет map тем с enum встроенного OpenAPI и проверяет закрытый отказ до RPC при дрейфе схемы. Это bufconn-фейки `user`, а не доказательство его SQL/lifecycle/outbox; фактическая доставка удаления соседям проверяется отдельно в FIN-362.

FIN-360: `Analytics`, `AnalyticsPeriods`, `AnalyticsErrors` выполняются как integration и API по настоящим
HTTP и gRPC/bufconn. Проверяются month/quarter/year, точный period_key, оба transfer_mode, supports/exp,
verified service/user identity и allowlist metadata/read-primary, unary deadline, отсутствие user_id из query,
полная открытая проекция всех примитивов и вложенного Struct, фильтры/selection, денежные значения,
неизвестные коды, наносекунды и смещение времени, gaps/navigation/state, ready/tentative/locked/none/unavailable,
пустые списки и has_data=false. Пустые currency и верхний lock имеют контрактные fallback; вложенный lock
не меняется. Ошибки400/401/429/500 и транспортный сбой, explicit retry advice/Retry-After, недоверенный токен,
неверная query/body до RPC проверяются на обоих уровнях; Gates/Maintenance включают оба GET.
Unit покрывает преобразования/presence, malformed syntax/overflow, неизвестный обязательный enum и неполный
ответ сервиса, все новые config/startup ветки. Реальные снимки/формулы/SQL analytics не подменяются этими
фейками: они проверяются самим analytics, полная stage-цепочка — FIN-362.

Ревью FIN-360: `AnalyticsMalformed` на обоих транспортных уровнях и соответствующие unit-кейсы проверяют
отсутствующие/невалидные required Timestamp и диапазоны (включая gaps и ListPeriods), точные поля/codes
диагностики и безопасный INTERNAL_ERROR. Unknown coverage имеет fallback partial и ограниченный счётчик.
Полученные пустые currency/верхний lock.text считаются даже перед отказом другого поля; legacy MetricLock.text
остаётся нулём, вложенный Struct не исследуется. Binary protobuf roundtrip с NaN/±Inf проходит gRPC и HTTP:
Struct.AsMap преобразует их в строки NaN/Infinity/-Infinity, тело200 остаётся корректным JSON.

`AnalyticsEnvelope` и unit-регрессии проверяют формат каждого календарного ключа (params, periods,
присутствующие navigation), каждый optional Timestamp при наличии и отсутствие optional без подстановки.
Неполные ListPeriods закрепляют точные пары field/code и прирост счётчика на всех трёх уровнях.

`AnalyticsLocalYearZero` и соответствующий unit-кейс сохраняют валидный UTC0001 в поясе Etc/GMT+5
как RFC3339 год0000 с offset -05:00, включая required state и optional lock/filter; ответ валиден по OpenAPI
и не увеличивает invalid. Присутствующая пустая optional-навигация имеет invalid; nil остаётся отсутствием.

FIN-359: Operations/OperationMutations/OperationsErrorsAndGates/OperationLargeSearch/OperationMalformed
запускаются на обоих транспортных уровнях. Проверены лента/курсор, все фильтры и presence, оба режима
переводов, точные nano timestamps/смещение, списки без подробностей, подробности/пары и готовые тексты,
полные результаты правки с replaced_id, повторные назначения и 204 без тела, справочник с открытыми кодами,
пустые массивы, signed user/service metadata/read-primary, отсутствие сырых user_id/cookie/key, все
ошибки реестра по объявленным статусам, Retry-After и explicit retryable, чужие/отозванные данные,
токен/версия/JSON/query/body до RPC и отсутствие финансовых данных в логах. Ответ поиска >4 МиБ проходит,
такой же ответ деталей не проходит; поиск >16 МиБ ограничен. Неизвестные обязательные enum, неполная
оболочка и invalid typed Timestamp отклоняются 500; правила и преобразования покрыты отдельными unit.
Финансовые правила, persistence и фактическая изоляция SQL проверяются в bank-statement-data (FIN-356),
настоящая сквозная цепочка stage — FIN-362; BFF-тесты используют реальные HTTP/gRPC и bufconn peer-фейки.

Ревью FIN-359: OperationMutationTransportLoss воспроизводит применение split/join и потерю ответа
UNAVAILABLE; BFF отвечает безопасным 500 после ровно одного RPC, не скрывает правку за повторным 404,
а следующее чтение возвращает все изменённые записи. PUT/DELETE отметки также не повторяются скрыто.
Malformed search items/range, change items и nil/пустой идентификатор категории дают 500 с проверкой
точного ограниченного счётчика на всех трёх уровнях; repeated nil-message protobuf на транспорте
становится пустым сообщением и проверяется как Category.id/absent. Отсутствие idempotency-key проверено
у всех новых RPC, а unit фиксирует пять наблюдений длинного дедлайна. Обратный разбор kind строится
по тому же generated descriptor, что и исходящий code; каждый известный ненулевой code проходит roundtrip.
