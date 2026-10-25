# Проверки каркаса bank-bff

FIN-261 переносит инфраструктуру HTTP BFF из go-service-template, сохраняя независимые gates FIN-238. Все три набора инструментируют одинаковый весь production-модуль (`-coverpkg=./...`), без исключений. Каждый требует total80 statements через закреплённый `go-test-coverage v2.20.1`; принятое округление 79,95% → 80,0%. Combined — справочное объединение штатным инструментом, не компенсация провала уровня.

- `TestUnit…`: конфигурация и её документация; operational handlers без сети; command usage/ошибки конфигурации; lifecycle на фейках consumer-интерфейса — отмена, отказ Serve, ограниченный shutdown, сохранение ошибок, снятие readiness.
- `TestIntegration…`: настоящие HTTP/TCP listeners полной команды, неверный HTTP, независимость портов, ошибки bind, освобождение частично привязанных портов, конфигурация и логи в реальной сборке.
- `TestAPI…`: полная команда и настоящий HTTP: health/readiness/metrics на operational порту; 404 на всех публичных путях; отсутствие operational endpoint на публичном порту; остановка и закрытие соединений; readiness 503 перед закрытием; отказ неверной конфигурации и занятого порта, отсутствие необходимости в БД/dev-auth.

`make test` проверяет tidy/vet, все Go-тесты с race/count1/timeout и Python guard/release tests. `make cover` собирает каждый набор и требует total80 для каждого. Пустой/skipped/failed suite, отсутствующий/пустой/header-only/zero-statement профиль — ошибка; пакеты не исключаются ради процента. `make test-names` до каждого collector отвергает имена вне трёх selectors. Отчёты и JSON/Go-профили публикуются артефактами CI, required job `go` остаётся строгим. Сервис не использует библиотечный N/A.

PostgreSQL и testcontainers каркасу без БД не нужны; интеграционные тесты используют настоящий TCP/HTTP. При появлении stateful зависимости тесты должны использовать её настоящую реализацию. UI-тесты не входят в эту задачу.

CI также запускает lint и сборку distroless/nonroot образа с HTTP smoke-проверкой обоих портов и SIGTERM. Версии инструментов — Makefile и workflow; toolchain — `go.mod`. Публикация GHCR/deploy и версия сервиса выполняются отдельно.

Для Python regression-тестов workflow нужен Node.js 20+ (`node` в PATH). Проверяется реальный read-only JS gate: fork/удалённый head/чужая base/закрытый PR/ошибка API не могут включить write-enabled job; отдельные issues не запускают ревью.
