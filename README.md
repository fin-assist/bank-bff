# bank-bff

Stateless HTTP BFF «Понятных финансов». Каркас перенесён из [`go-service-template`](https://github.com/fin-assist/go-service-template/tree/6ea35cea4b0ff68b831acadd22e2bc77e79481bd) (FIN-246), адаптация — [FIN-261](https://finassist.youtrack.cloud/issue/FIN-261). Есть Go-модуль, запуск/остановка HTTP, конфигурация, логи, health/readiness, метрики и упаковка. Бизнес-REST/SSE пока не реализованы: публичный listener отвечает 404 на все пути.

Контракты — [`fin-assist/contracts`](https://github.com/fin-assist/contracts): `docs/api.md`, `openapi/openapi.yaml`, `docs/grpc-api.md`. BFF не хранит состояние; из шаблона не добавлены PostgreSQL/миграции, входящий gRPC business server, фиктивные jobs или обход авторизации. Исходящие gRPC-клиенты и shared библиотеки добавляются вместе с реальными endpoint-задачами.

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
- `internal/app/` — явная сборка обоих HTTP listeners и shutdown.
- `internal/config/` — настройки; таблица документации проверяется по `Vars`.
- `internal/ops/` — Go/process metrics, liveness/readiness.
- [docs/config.md](docs/config.md), [docs/server.md](docs/server.md), [docs/testing.md](docs/testing.md) — настройки, устройство и границы покрытия.
- `Dockerfile`, `compose.yaml`, `deploy/systemd/bank-bff.service` — упаковка и пример стенда; публикация/deploy отдельно.
- [AGENTS.md](AGENTS.md) — правила разработки и ревью.

Три порога FIN-238 сохранены: `make collect-*`, `make check-*`/`make cover-*`, `make cover-combined`. Все используют один полный `./...` denominator, без исключений; каждый набор обязан реально выполнить тесты без skip/fail и дать профиль с statements. Закреплённый `go-test-coverage v2.20.1` проверяет каждый total80; принятое округление 79,95% → 80,0%. Combined не заменяет отдельные пороги. JSON-логи, профили и штатные отчёты сохраняются в CI. Required `go`, lint и image проверяют текущий SHA. Покрытие каркаса не подтверждает реализацию REST-контракта продукта.

[CHANGELOG.md](CHANGELOG.md), [docs/releasing.md](docs/releasing.md), `.github/workflows/release.yml` определяют выпуск сервиса. Тег ставит владелец после PR и post-merge CI; publication GHCR/deployment — отдельные этапы. Например, `make release-check VERSION=vX.Y.Z` проверяет подготовленную секцию до создания тега.

Ревью Claude можно запускать после появления `.github/workflows/claude.yml` в main и настройки секрета владельцем. Настройка Renovate/controller, секретов и защит не входит в FIN-261; автоматически включённой политики обновлений каркас не заявляет.
