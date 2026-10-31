# bank-bff

Stateless HTTP BFF «Понятных финансов». Каркас перенесён из [`go-service-template`](https://github.com/fin-assist/go-service-template/tree/6ea35cea4b0ff68b831acadd22e2bc77e79481bd) (FIN-246), адаптация — [FIN-261](https://finassist.youtrack.cloud/issue/FIN-261). Есть Go-модуль, запуск/остановка HTTP, конфигурация, логи, health/readiness, метрики и упаковка. [FIN-268](https://finassist.youtrack.cloud/issue/FIN-268) (в составе [FIN-331](https://finassist.youtrack.cloud/issue/FIN-331)) добавляет публичный REST/SSE-API входа по телефону, токенов, документов согласий, профиля (чтение и согласие помощника) и помощника — 14 операций `openapi.yaml`, перечень и порядок проверок — [docs/server.md](docs/server.md). FIN-342 добавляет шесть операций импорта OFX: конфигурацию, потоковую загрузку, прогресс SSE, историю, удаление и непрочитанные строки. FIN-361 добавляет изменение настроек и удаление аккаунта (`PATCH`/`DELETE /v1/profile`) через готовый `user.ProfileService`. FIN-360 добавляет `GET /v1/analytics` и `GET /v1/analytics/periods` через `analytics.AnalyticsService`. FIN-359 завершает все 30 HTTP-операций контракта: ленту/поиск, детали/смену категории, справочник категорий и «Не подписка» через `bank-statement-data`. Неизвестные пути и методы отвечают 404. В `ENV=dev` публичный API не собирается: обхода авторизации нет.

Контракты — [`fin-assist/contracts`](https://github.com/fin-assist/contracts): `docs/api.md`, `openapi/openapi.yaml`, `docs/grpc-api.md`. BFF не хранит состояние; PostgreSQL/миграций, входящего gRPC business server, бизнес-заданий и обхода авторизации нет. Исходящие вызовы — `go-grpc` v0.2.0 к `user`, `assistant`, `bank-statement-data` и `analytics`, идентичность — `go-identity` v0.2.0, ошибки — `finerr` из `contracts/gen/go` v0.8.0, проверка запросов по встроенной копии `openapi.yaml` — `kin-openapi`.

FIN-342 добавляет REST `error.retryable`: явное разрешение источника отказа (сервиса или транспортного обработчика BFF) соединяется с доказанной безопасностью всего исходного REST-запроса. Без одной из гарантий — `false`; UUID не доказывает возраст ключа. В ошибках результата помощника канонический вложенный `google.rpc.Status` имеет приоритет над прежним enum, а разрешение новой генерации остаётся отдельным свойством сервиса. Условия совместимой выкладки — [docs/config.md](docs/config.md), правила и границы — [docs/server.md](docs/server.md).

FIN-364 передаёт согласие ПДн, точную версию и отдельное подтверждение 14+ уже в StartPhoneVerification. Структурные ошибки отклоняются до RPC, а false и неактуальная непустая версия доходят USER с сохранением порядка ключ/лимиты/проверки. Долговечное доказательство до провайдера хранит USER; BFF передаёт только проверенный IP и остальные прежние разрешённые metadata. Это совместная миграция Android/BFF/USER, без безопасного отката на прежний телефонный вход после переключения.

```bash
make run    # Go toolchain из go.mod, HTTP :8080 и operational :9091
make up     # то же в Docker; порты хоста только на 127.0.0.1
make down
make test   # tidy, vet, race/count1/timeout, Python guards, CHANGELOG
make lint
make cover  # >=80% всего production-модуля отдельно unit/integration/API
```

На operational порту: `GET /healthz`, `/readyz`, `/metrics`. На публичном порту этих маршрутов нет. Вне локального запуска operational порт должен оставаться внутренним. По SIGTERM readiness снимается, затем после паузы сервер завершает активные запросы с общим timeout и закрывает соединения.

- `cmd/bank-bff/` — команда `serve`, SIGTERM/SIGINT, exit 0/1/2 и логгер.
- `internal/app/` — явная сборка обоих HTTP listeners, публичного API (`api.go`: контракт, идентичность, gRPC-клиенты) и shutdown.
- `internal/rest/` — общий HTTP-слой: X-Request-Id, MAINTENANCE и версия клиента, тело и схема, перевод gRPC-ошибок, SSE, метрики и журнал запросов.
- `internal/endpoint/` — 30 операций: REST ↔ gRPC.
- `internal/catalog/` — тексты и severity пользовательских ошибок (FIN-225).
- `internal/apispec/` — встроенная копия `openapi.yaml` из `contracts`.
- `internal/testpeer/` — тестовая инфраструктура: фейковые `user`/`assistant`/`bank-statement-data`/`analytics` на bufconn, ключи, проверка ответов по контракту.
- `internal/config/` — настройки; таблица документации проверяется по `Vars`.
- `internal/ops/` — Go/process metrics, liveness/readiness.
- [docs/config.md](docs/config.md), [docs/server.md](docs/server.md), [docs/testing.md](docs/testing.md) — настройки, устройство и границы покрытия.
- `Dockerfile`, `compose.yaml`, `deploy/systemd/bank-bff.service` — упаковка и пример стенда; публикация/deploy отдельно.
- [AGENTS.md](AGENTS.md) — правила разработки и ревью.

Три порога FIN-238 сохранены: `make collect-*`, `make check-*`/`make cover-*`, `make cover-combined`. Все используют один полный `./...` denominator, без исключений; каждый набор обязан реально выполнить тесты без skip/fail и дать профиль с statements. Закреплённый `go-test-coverage v2.20.1` проверяет каждый total80; принятое округление 79,95% → 80,0%. Combined не заменяет отдельные пороги. JSON-логи, профили и штатные отчёты сохраняются в CI. Required `go`, lint и image проверяют текущий SHA. Покрытие не подтверждает реализацию нереализованных операций контракта.

[CHANGELOG.md](CHANGELOG.md), [docs/releasing.md](docs/releasing.md), `.github/workflows/release.yml` определяют выпуск сервиса. Тег ставит владелец после PR и post-merge CI; publication GHCR/deployment — отдельные этапы. Например, `make release-check VERSION=vX.Y.Z` проверяет подготовленную секцию до создания тега.

Ревью Claude можно запускать после появления `.github/workflows/claude.yml` в main и настройки секрета владельцем. Настройка Renovate/controller, секретов и защит не входит в FIN-261; автоматически включённой политики обновлений каркас не заявляет.

Для Python regression-тестов workflow нужен Node.js 20+ (`node` в PATH). Проверяется реальный read-only JS gate: fork/удалённый head/чужая base/закрытый PR/ошибка API не могут включить write-enabled job; отдельные issues не запускают ревью.

## Артефакты stage

[Получение и проверка бинарей](docs/stage-artifacts.md) (FIN-148): CI извлекает бинарь из проверенного образа, сохраняет SHA и контрольную сумму; фактическая выкладка остаётся FIN-149.

## Общий Go CI и линтеры (FIN-372)

В CI `GO_MODULES_TOKEN` передаётся только при доверенном push в `main`, включая сборку образа. Ни один PR, в том числе из того же репозитория, этот секрет не получает. Текущие модули fin-assist публичны. Одного добавления секрета недостаточно для проверки PR с приватными Go-модулями: доступ в PR этой конфигурацией не предоставляется.

Общие команды и тесты скриптов требуют Python 3.11+ с PyYAML.

`.finassist-ci.json` закрепляет общий источник `fin-assist/go-ci` полным SHA и объявляет профиль/исключения. CI вызывает reusable workflows того же SHA; `make lint`, `make fmt`, `make lint-config` и `make lint-migrations BASE_REF=origin/main` используют его через `scripts/common_ci.py`. Конфигурации создаются в игнорируемом `.finassist-ci/generated/`; локальные копии правил не редактируются. Первый локальный вызов, включая `make test` и Python discovery, требует доступ к GitHub для получения закреплённого источника; после загрузки неизменённый кэш используется без сети. Отсутствующий или повреждённый кэш без сети останавливает весь запуск, включая script tests. Обновление workflow и manifest pin приходит одним PR Renovate; откат возвращает прежние SHA вместе. [Источник, профили, обновление и откат](https://github.com/fin-assist/go-ci/blob/main/docs/shared-go-ci.md). Проверки тестов/coverage, race и совместимость сохраняются; BFF по-прежнему не использует PostgreSQL, Squawk и миграции. Для обновлений добавлен корневой `renovate.json` с `automerge: false`; управляемый контроллер Renovate не добавляется.


Если новая ревизия меняет `common/bootstrap.py`, в том же PR нужно синхронизировать его точную копию `scripts/common_ci.py`; Renovate сам обновляет только SHA. При откате возвращаются вместе pins и соответствующая копия bootstrap. До синхронизации byte-equality guard оставляет PR красным. Ручное изменение копии не считается bot-only обновлением по FIN-211.
