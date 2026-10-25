# Changelog

## [Unreleased]

### Добавлено

- FIN-268 (FIN-331): публичный REST/SSE-API входа по телефону, регистрации, токенов, выхода, документов согласий, профиля и согласия помощника, сообщений, ответа и лимита помощника — 14 операций `openapi.yaml`. Проверки в порядке контракта (MAINTENANCE → версия клиента → токен → размер и схема тела → сервис), проверка JWT через go-identity v0.2.0, разрешённый список metadata в gRPC, перевод ошибок через `finerr.ToREST`, неизвестная ошибка — 500. SSE с heartbeat, собственными сроками и пределами потоков. Новые настройки — `docs/config.md`.
- FIN-225 (часть, FIN-331): единый каталог `user_message`/`severity` ошибок BFF, включая тексты ошибок ответа помощника; REST `retryable` и вложенный `google.rpc.Status` ждут тега `gen/go` с FIN-224.
- FIN-290 (FIN-331): `make test-names` проверяет и `Fuzz…`, отклоняет `Example…`; collectors выбирают Fuzz-цели своего уровня.
- FIN-261: Go-каркас stateless HTTP BFF из go-service-template: два изолированных listeners, конфигурация, логи, health/readiness/метрики, ограниченный shutdown, Docker/systemd и три уровня автотестов. Бизнес-REST/SSE остаются в отдельных задачах.

### Исправлено

- FIN-286: пример systemd-юнита для стенда привязывает публичный и operational listeners к `127.0.0.1`: метрики и HTTP без TLS не публикуются на внешних интерфейсах VPS; юнит дополнительно разрешает только loopback-трафик (`IPAddressAllow=localhost`, `IPAddressDeny=any`), env-файл стенда адреса listeners не задаёт.
- FIN-261: workflow ревью Claude проверяет происхождение PR отдельным read-only job и не выдаёт права записи PR из форков; ошибку API и удалённый head обрабатывает как отказ.
