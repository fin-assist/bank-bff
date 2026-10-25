# Конфигурация bank-bff

Настройки читаются при `bank-bff serve`. Пустое значение означает значение по умолчанию. Таблица проверяется по `internal/config.Vars` тестом `TestUnitDocIsCurrent`.

| Переменная | По умолчанию | Что |
|---|---|---|
| `SERVICE_NAME` | `bank-bff` | Имя сервиса в логах |
| `ENV` | `prod` | dev, stage или prod |
| `PUBLIC_ADDR` | `:8080` | Публичный HTTP listener; бизнес-маршруты пока не зарегистрированы |
| `HTTP_ADDR` | `:9091` | Отдельный operational listener: /healthz, /readyz, /metrics |
| `LOG_LEVEL` | `info` | debug, info, warn или error |
| `LOG_FORMAT` | `json` | json или text |
| `SHUTDOWN_DELAY` | `5s` | Пауза после снятия readiness для балансировщика |
| `SHUTDOWN_TIMEOUT` | `20s` | Общий предел graceful shutdown обоих listeners |

Адрес — `host:port` с числовым портом 0–65535; `:0` выбирает свободный порт для тестов. Фактические адреса публикуются в строке `serving`. Задержка остановки допустима от 0 до 1 минуты, timeout — больше 0 и до 5 минут. Некорректные настройки дают exit 1 до запуска listeners.

`HTTP_ADDR` доступен только внутри инфраструктуры; его нельзя публиковать через ingress. Compose привязывает оба порта к loopback хоста. Значения по умолчанию слушают все интерфейсы (это нужно в контейнере), поэтому вне контейнера адреса задаются явно: пример `deploy/systemd/bank-bff.service` для стенда за nginx ставит `127.0.0.1` обоим listeners. Это проверяет `scripts/test_deploy_examples.py`. TLS завершается на ingress. Метрики и readiness не являются пользовательским API.

В stateless-каркасе нет БД, миграций, `AUTH_MODE`, входящего gRPC, peer-адресов или service-токенов. Они не читаются. Пока публичный mux не имеет маршрутов и отвечает 404 на любой путь; авторизация будущих REST/SSE-методов добавляется вместе с ними по `fin-assist/contracts`, до регистрации маршрута. Шаблон не включает dev-обхода авторизации.
