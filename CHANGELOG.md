# Changelog

## [Unreleased]

### Добавлено

- FIN-261: Go-каркас stateless HTTP BFF из go-service-template: два изолированных listeners, конфигурация, логи, health/readiness/метрики, ограниченный shutdown, Docker/systemd и три уровня автотестов. Бизнес-REST/SSE остаются в отдельных задачах.

### Исправлено

- FIN-286: пример systemd-юнита для стенда привязывает публичный и operational listeners к `127.0.0.1`: метрики и HTTP без TLS не публикуются на внешних интерфейсах VPS; юнит дополнительно разрешает только loopback-трафик (`IPAddressAllow=localhost`, `IPAddressDeny=any`), env-файл стенда адреса listeners не задаёт.
- FIN-261: workflow ревью Claude проверяет происхождение PR отдельным read-only job и не выдаёт права записи PR из форков; ошибку API и удалённый head обрабатывает как отказ.
