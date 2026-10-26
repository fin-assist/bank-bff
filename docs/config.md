# Конфигурация bank-bff

Настройки читаются при `bank-bff serve`. Пустое значение означает значение по умолчанию. Таблица проверяется по `internal/config.Vars` тестом `TestUnitDocIsCurrent`.

| Переменная | По умолчанию | Что |
|---|---|---|
| `SERVICE_NAME` | `bank-bff` | Имя сервиса в логах и ServiceAccount для исходящих вызовов |
| `ENV` | `prod` | dev, stage или prod; в dev публичный API не регистрируется |
| `PUBLIC_ADDR` | `:8080` | Публичный HTTP listener REST/SSE |
| `HTTP_ADDR` | `:9091` | Отдельный operational listener: /healthz, /readyz, /metrics |
| `LOG_LEVEL` | `info` | debug, info, warn или error |
| `LOG_FORMAT` | `json` | json или text |
| `SHUTDOWN_DELAY` | `5s` | Пауза после снятия readiness для балансировщика |
| `SHUTDOWN_TIMEOUT` | `20s` | Общий предел graceful shutdown обоих listeners |
| `USER_GRPC_ADDR` | — | gRPC-адрес сервиса user (`dns:///user.finassist.svc.cluster.local:9090`); обязателен в stage/prod |
| `ASSISTANT_GRPC_ADDR` | — | gRPC-адрес сервиса assistant; обязателен в stage/prod |
| `AUTH_USER_ISSUER` | — | `iss` access JWT пользователя, как у user; обязателен в stage/prod |
| `AUTH_USER_AUDIENCE` | — | `aud` access JWT пользователя, как у user; обязателен в stage/prod |
| `SERVICE_TOKEN_FILE` | — | prod: projected ServiceAccount token с audience finassist-internal |
| `AUTH_KUBERNETES_ISSUER` | — | prod: issuer ServiceAccount; пусто — значение go-identity |
| `AUTH_KUBERNETES_JWKS_URL` | — | prod: JWKS API-сервера; пусто — значение go-identity |
| `AUTH_KUBERNETES_CA_FILE` | — | prod: CA для JWKS API-сервера; пусто — значение go-identity |
| `AUTH_KUBERNETES_API_TOKEN_FILE` | — | prod: токен пода для запроса JWKS; пусто — значение go-identity |
| `AUTH_KUBERNETES_NAMESPACE` | — | prod: namespace вызывающих; пусто — значение go-identity |
| `AUTH_STAGE_ISSUER` | — | stage: issuer сервисных JWT стенда |
| `AUTH_STAGE_SIGNING_KEY_FILE` | — | stage: PKCS#8 Ed25519 ключ этого сервиса |
| `AUTH_STAGE_PUBLIC_KEYS_FILE` | — | stage: JWKS ключей сервисов стенда |
| `CLIENT_MIN_VERSIONS` | — | Минимальные версии клиента: `android=0.2.5,ios=0.1.0`; обязательна в stage/prod |
| `MAINTENANCE` | `off` | on — все запросы /v1 получают 503 MAINTENANCE |
| `MAINTENANCE_UNTIL` | — | Ожидаемое окончание техработ, RFC 3339; пусто — неизвестно |
| `MAINTENANCE_RETRY_AFTER` | `5m` | Retry-After техработ, пока окончание неизвестно или прошло; 1s–24h |
| `JSON_BODY_LIMIT` | `65536` | Предел JSON-тела запроса в байтах; 65536–1048576 |
| `SSE_MAX_STREAMS` | `2000` | Предел одновременных SSE-потоков процесса |
| `SSE_MAX_STREAMS_PER_IP` | `20` | Предел одновременных SSE-потоков с одного адреса клиента |
| `SSE_BUSY_RETRY_AFTER` | `10s` | Retry-After отказа 429 по пределу SSE-потоков; 1s–5m |
| `TRUSTED_PROXIES` | — | CIDR доверенных прокси (nginx стенда, ingress) через запятую; только от них адрес клиента берётся из X-Forwarded-For; пусто — адрес TCP-пира |

Адрес — `host:port` с числовым портом 0–65535; `:0` выбирает свободный порт для тестов. Фактические адреса публикуются в строке `serving`. Задержка остановки допустима от 0 до 1 минуты, timeout — больше 0 и до 5 минут. Некорректные настройки дают exit 1 до запуска listeners.

`HTTP_ADDR` доступен только внутри инфраструктуры; его нельзя публиковать через ingress. TLS завершается на ingress. Метрики и readiness не являются пользовательским API. Значения по умолчанию слушают все интерфейсы (это нужно в контейнере), поэтому вне контейнера адреса задаются явно. Compose привязывает оба порта к loopback хоста; конфигурация Compose — только `compose.yaml` без `compose.override.yaml`, `.env` в корне (через `COMPOSE_FILE` он подключает другие файлы), `include`, `extends` и `networks`.

### Стенд под systemd

Пример `deploy/systemd/bank-bff.service` рассчитан на стенд за nginx на том же хосте (FIN-149).

- Оба listener привязаны к `127.0.0.1`. Адреса принадлежат юниту: `EnvironmentFile=` перекрывает `Environment=`, поэтому ни `/etc/finassist/bank-bff/env`, ни drop-in (`/etc/systemd/system/bank-bff.service.d/*.conf`) не задают `PUBLIC_ADDR`/`HTTP_ADDR`, не снимают их (`UnsetEnvironment=`) и не сбрасывают `IPAddressAllow`/`IPAddressDeny`.
- nginx проксирует на `127.0.0.1:8080`, а не на `localhost` (иначе сначала пробуется `[::1]`). Внешний мониторинг с другого хоста `/metrics` и `/readyz` не достанет. Предпочтительно — локальный агент на том же хосте: юнит менять не нужно. Если нужен сбор с другого хоста, это осознанное исключение, и оно оформляется изменением самого юнита в репозитории (PR с задачей FIN), а не env-файлом или drop-in: `HTTP_ADDR` привязывается к адресу приватной сети (не `0.0.0.0`), к `IPAddressAllow=localhost` добавляется отдельная строка `IPAddressAllow=<адрес агента>/32` (или `/128` для IPv6), `IPAddressDeny=any` остаётся. Подсеть вместо конкретного адреса — только с обоснованием в PR: `IPAddressAllow` действует в обе стороны, и к `/metrics` и `/readyz` получил бы доступ любой хост подсети. Без этой строки фильтры отрежут агента даже при правильной привязке. В том же PR обновляются точные ожидаемые значения в `scripts/test_deploy_examples.py` (`EXACT_SERVICE`) и комментарий юнита о том, что operational-порт недоступен снаружи хоста.
- `IPAddressAllow=localhost` и `IPAddressDeny=any` — дополнительная защита поверх привязки; будущим исходящим gRPC-вызовам нужны явные разрешения. Без cgroup eBPF systemd один раз за загрузку предупреждает «Proceeding WITHOUT firewalling in effect!» (для первого такого юнита, не обязательно `bank-bff`) и работает без фильтров.
- После раскатки: адреса — по строке лога `serving` или `ss -ltn`: `PUBLIC_ADDR` только на `127.0.0.1`, `HTTP_ADDR` на `127.0.0.1` или, при оформленном исключении для мониторинга, только на указанном приватном адресе; фильтры — две обязательные проверки, обе от root: (1) `systemctl show bank-bff -p IPAddressAllow -p IPAddressDeny` — действующая конфигурация самого `bank-bff` с учётом drop-in, но не доказательство применения. systemd выводит не `localhost`/`any`, а развёрнутые префиксы: ожидается `IPAddressAllow=127.0.0.0/8 ::1/128` (при исключении для мониторинга — ещё `<адрес агента>/32` или `/128`) и `IPAddressDeny=0.0.0.0/0 ::/0`; порядок элементов может отличаться. Отклонение — любой префикс в allow сверх перечисленных и deny, в котором нет обоих `0.0.0.0/0` и `::/0` (в том числе пустой); лишние префиксы в deny безвредны. Списки systemd неявно объединяет со списками родительских slice (systemd.resource-control(5)), поэтому `IPAddressAllow=any` на slice отменил бы deny юнита: `systemctl show bank-bff -p Slice` должен показать `Slice=system.slice`, а `systemctl show -p IPAddressAllow -- system.slice -.slice` (опции до `--`, иначе `-.slice` разбирается как опция) — пустые `IPAddressAllow=` для обоих slice; (2) пара проб на `PUBLIC_ADDR`, который всегда на `127.0.0.1` (operational-порт при исключении для мониторинга на loopback не слушает), подтверждает, что хост применяет cgroup-фильтры: контрольная `systemd-run --wait -q curl -sS -o /dev/null -m 3 http://127.0.0.1:8080/` должна пройти (ответ 404 без `-f` даёт код 0), а та же команда с `-p IPAddressDeny=any` — завершиться ошибкой. Если проба с deny проходит, причина — отсутствие eBPF или allow-правило на родительском slice (см. проверку (1)). Если контрольная проба не проходит, пара ничего не доказывает. Проба во внешнюю сеть не отличает работающие фильтры от отсутствия сети.

`scripts/test_deploy_examples.py` — allowlist: в секции `[Service]` юнита (с учётом `\`-продолжений) допустимы только перечисленные директивы, а `ExecStart`, `EnvironmentFile`, `Environment`, `IPAddressAllow`, `IPAddressDeny` должны совпадать точно; в `compose.yaml` допустимы только перечисленные ключи и порты вида `"127.0.0.1:N:N"`, это единственный compose-файл в корне и `.env` там нет; оба файла — только ASCII, чтобы разбор совпадал с systemd и YAML. Env-файл стенда и drop-in тест не видит — для них действуют правила выше.

### Публичный API (FIN-268)

В `stage` и `prod` публичный listener обслуживает 14 операций входа, профиля и помощника (`docs/server.md`). Обязательны `USER_GRPC_ADDR`, `ASSISTANT_GRPC_ADDR`, `AUTH_USER_ISSUER`, `AUTH_USER_AUDIENCE` и `CLIENT_MIN_VERSIONS`; без них старт завершается exit 1 до привязки портов. Идентичность собирает `go-identity` v0.2.0 `NewAssembly`: в `prod` — projected ServiceAccount token (`SERVICE_TOKEN_FILE`, отличный от API-токена пода) и JWKS API-сервера по HTTPS с CA из `AUTH_KUBERNETES_*` (пустые значения — значения по умолчанию библиотеки), в `stage` — три `AUTH_STAGE_*` файла; файлы стенда в `prod` — ошибка старта. Ключи проверки access JWT пользователя берутся из `user.GetSigningKeys` по `USER_GRPC_ADDR`. За прокси (nginx стенда, ingress prod) задайте `TRUSTED_PROXIES` сетями этих прокси: иначе адресом клиента для `x-client-ip` и предела `SSE_MAX_STREAMS_PER_IP` у всех будет адрес прокси.

В `dev` публичный mux пустой: любой путь — 404, как в каркасе FIN-261. `NewAssembly` не поддерживает `dev`, а обхода авторизации нет; локальная проверка API — только с настройками `stage`. БД, миграций, `AUTH_MODE` и входящего gRPC у BFF нет.

`CLIENT_MIN_VERSIONS` — список `платформа=MAJOR.MINOR.PATCH` через запятую; платформа, которой нет в списке, получает 400 `CLIENT_VERSION_UNSUPPORTED`. `MAINTENANCE=on` переводит все запросы `/v1` в 503 `MAINTENANCE` с `Retry-After` (до `MAINTENANCE_UNTIL`, если он в будущем, иначе `MAINTENANCE_RETRY_AFTER`); режим меняется перезапуском с новым env, без релиза. `JSON_BODY_LIMIT` не может быть меньше 64 КиБ (FIN-339). SSE-пределы ограничивают одновременные потоки процесса и одного адреса клиента; отказ — 429 `RATE_LIMITED` со свежим `retry_at`.

На стенде (`ENV=stage` в юните) env-файл должен задать peer-адреса, `AUTH_*` и `CLIENT_MIN_VERSIONS`. Исходящие gRPC-вызовы к сервисам на `127.0.0.1` проходят фильтр `IPAddressAllow=localhost`; peer на другом хосте потребует изменения юнита.

### Совместимая выкладка ошибок FIN-342

Go-зависимость BFF — `contracts/gen/go v0.5.0`, тег на `471482b1dd0d8ec556325ef5d57d5658502f8570`. Это версия типов и декодера, она не доказывает возможности запущенного `assistant`. Отдельной переменной среды или эвристики по `text`/`error_text` для определения возможностей нет.

Нижняя поддерживаемая исходная ревизия `assistant` для этого BFF — `3ef9e08632af3d27564bd8d4f1278478db74684a` (merge PR #15, FIN-220/FIN-161) или её потомок с сохранённой семантикой FIN-159. В ней терминальный `AnswerError.retryable` передаёт явный `false`, история заполняет presence-aware `Message.retryable`, а `RetryAnswer` проверяет сохранённое разрешение сервиса. См. [mapper.go](https://github.com/fin-assist/assistant/blob/3ef9e08632af3d27564bd8d4f1278478db74684a/internal/adapter/grpc/dialogue/mapper.go#L480) и [asking.go](https://github.com/fin-assist/assistant/blob/3ef9e08632af3d27564bd8d4f1278478db74684a/internal/adapter/postgres/asking.go#L98). На момент проверки 26.10.2026 именованных тегов и GitHub releases у сервиса нет, поэтому граница фиксируется SHA, без выдуманной версии релиза.

Перед выкладкой владелец проверяет provenance всех обслуживающих экземпляров `assistant` и разрешённых rollback-образов относительно этой границы. Proto3 bool терминального события не отличает старое отсутствие поля от явного `false`; наличие текста, enum или одного нового protobuf-типа эту проверку не заменяет. Фактическая выкладка и состав работающих экземпляров этим PR не подтверждаются.

Порядок миграции — contracts `docs/grpc-api.md` 5.5:

1. Обновить все декодеры BFF/потребителей до поддержки optional canonical Status; до этого producers сохраняют прежние поля и UI-строки.
2. После фиксации нижней границы отката обновить producers до `v0.5.0`: добавлять canonical Status с dual-write, а unary `RequestRetryInfo` — только по решению соответствующего usecase. Новые причины не подменяются legacy `MODEL_ERROR` ради кнопки повтора.
3. Проверить наличие применимого advice на реальных отказах и replay-путях. Старый producer без детали даёт REST `false`; включение прежних автоматических повторов нельзя считать выполненным одним обновлением BFF. В частности, текущий `go-idempotency` пересоздаёт сохранённый телефонный 429 и теряет дополнительные details: до исправления отсутствие advice остаётся `false`.
4. Проверить unknown/malformed каноническую причину, внешний `retryable=false` новой попытки, свежий `Date`/`Retry-After` и отсутствие сырых сервисных текстов в ответах. Только после обновления всех BFF и фиксации rollback-границы producers могут перестать заполнять прежние UI-строки; protobuf-поля сохраняются.

В BFF не появляется хранилище возраста ключей. Все POST пока получают REST `false`, даже с service advice `true`; для положительного решения нужен отдельный доказанный механизм срока всего исходного запроса. HTTP-маршруты импорта пока отсутствуют, поэтому миграция `ImportStreamError.status` потребует реализации соответствующего сценария отдельно.
