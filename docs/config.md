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

`HTTP_ADDR` доступен только внутри инфраструктуры; его нельзя публиковать через ingress. TLS завершается на ingress. Метрики и readiness не являются пользовательским API. Значения по умолчанию слушают все интерфейсы (это нужно в контейнере), поэтому вне контейнера адреса задаются явно. Compose привязывает оба порта к loopback хоста; конфигурация Compose — только `compose.yaml` без `compose.override.yaml`, `include`, `extends` и `networks`.

### Стенд под systemd

Пример `deploy/systemd/bank-bff.service` рассчитан на стенд за nginx на том же хосте (FIN-149).

- Оба listener привязаны к `127.0.0.1`. Адреса принадлежат юниту: `EnvironmentFile=` перекрывает `Environment=`, поэтому ни `/etc/finassist/bank-bff/env`, ни drop-in (`/etc/systemd/system/bank-bff.service.d/*.conf`) не задают `PUBLIC_ADDR`/`HTTP_ADDR`, не снимают их (`UnsetEnvironment=`) и не сбрасывают `IPAddressAllow`/`IPAddressDeny`.
- nginx проксирует на `127.0.0.1:8080`, а не на `localhost` (иначе сначала пробуется `[::1]`). Внешний мониторинг с другого хоста `/metrics` и `/readyz` не достанет. Предпочтительно — локальный агент на том же хосте: юнит менять не нужно. Если нужен сбор с другого хоста, это осознанное исключение, и оно оформляется изменением самого юнита в репозитории (PR с задачей FIN), а не env-файлом или drop-in: `HTTP_ADDR` привязывается к адресу приватной сети (не `0.0.0.0`), к `IPAddressAllow=localhost` добавляется отдельная строка `IPAddressAllow=<адрес или подсеть агента>`, `IPAddressDeny=any` остаётся. Без этой строки фильтры отрежут агента даже при правильной привязке. В том же PR обновляются точные ожидаемые значения в `scripts/test_deploy_examples.py` (`EXACT_SERVICE`).
- `IPAddressAllow=localhost` и `IPAddressDeny=any` — дополнительная защита поверх привязки; будущим исходящим gRPC-вызовам нужны явные разрешения. Без cgroup eBPF systemd один раз за загрузку предупреждает «Proceeding WITHOUT firewalling in effect!» (для первого такого юнита, не обязательно `bank-bff`) и работает без фильтров.
- После раскатки: адреса — по строке лога `serving` или `ss -ltn` (оба порта только на `127.0.0.1`); применение фильтров — пробой `systemd-run --wait -p IPAddressDeny=any curl -m 3 http://1.1.1.1`, она должна завершиться ошибкой. `systemctl show bank-bff -p IPAddressDeny` показывает только конфигурацию.

`scripts/test_deploy_examples.py` — allowlist: в секции `[Service]` юнита (с учётом `\`-продолжений) допустимы только перечисленные директивы, а `ExecStart`, `EnvironmentFile`, `Environment`, `IPAddressAllow`, `IPAddressDeny` должны совпадать точно; в `compose.yaml` допустимы только перечисленные ключи и порты вида `"127.0.0.1:N:N"`, и это единственный compose-файл в корне. Env-файл стенда и drop-in тест не видит — для них действуют правила выше.

В stateless-каркасе нет БД, миграций, `AUTH_MODE`, входящего gRPC, peer-адресов или service-токенов. Они не читаются. Пока публичный mux не имеет маршрутов и отвечает 404 на любой путь; авторизация будущих REST/SSE-методов добавляется вместе с ними по `fin-assist/contracts`, до регистрации маршрута. Шаблон не включает dev-обхода авторизации.
