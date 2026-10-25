# HTTP-каркас bank-bff

Перенесён из `fin-assist/go-service-template` main `6ea35cea4b0ff68b831acadd22e2bc77e79481bd` (FIN-246), адаптация — FIN-261. BFF по `contracts/docs/grpc-api.md` не хранит собственного состояния: SQL, PostgreSQL pools/migrations и входящий gRPC business server из шаблона не переносятся. Общие Go-библиотеки будут подключаться при появлении реального потребителя, например исходящих gRPC-клиентов. Локальных копий этих библиотек нет.

`cmd/bank-bff` читает конфигурацию, создаёт slog с именем сервиса и версией и передаёт контекст SIGINT/SIGTERM в `internal/app.Run`. Сборка сначала привязывает публичный TCP listener, затем operational. Ошибка второго bind освобождает первый. После обоих bind включается readiness. Ошибка любого Serve инициирует остановку обоих listeners и возвращается вызывающему; CLI завершается с кодом 1.

Публичный listener использует отдельный пустой `http.ServeMux`: любой путь, включая `/healthz`, `/readyz`, `/metrics` и `/v1/...`, даёт 404. Нет реализации бизнес-REST/SSE, JWT, валидации OpenAPI, переводов ошибок или gRPC-вызовов. Это работа отдельных задач BFF; готовность каркаса не означает готовности API продукта.

Operational listener публикует только `GET/HEAD /healthz`, `/readyz`, `/metrics`. `/healthz` сообщает о живом процессе, `/readyz` — о привязанных listeners до начала остановки; внешних зависимостей у каркаса нет. После SIGTERM readiness становится 503. Публичный listener продолжает обслуживать запросы на протяжении `SHUTDOWN_DELAY`, чтобы балансировщик успел убрать экземпляр. Затем оба HTTP-сервера последовательно получают один общий ограниченный контекст shutdown; по истечении срока активные соединения принудительно закрываются. Ошибки shutdown/close сохраняются, ожидание goroutine заканчивается после остановки обоих Serve.

Защита HTTP: ReadHeaderTimeout 5s, ReadTimeout 10s, WriteTimeout 30s, IdleTimeout 60s, MaxHeaderBytes 1 MiB; нет DefaultServeMux и pprof. Будущие SSE/upload-методы требуют отдельных настроек по контракту в своих задачах, а не механического использования этих лимитов. Логи — JSON по умолчанию, text для локального запуска; нет тел запросов, токенов и пользовательских данных. Метрики Prometheus — стандартные Go/process collectors, без user_id или свободного текста в labels.

Контракты — только `fin-assist/contracts` (`docs/api.md`, `openapi/openapi.yaml`, `docs/grpc-api.md`). Новые поля и коды ошибок сначала добавляются там. Для будущих REST-операций нужны HTTP API-тесты полной сборки с проверкой OpenAPI, соседи — реальные transport fixtures на bufconn.
