# Qase 90 / FIN-437

Отдельный API-сценарий `TestAPIQase90CommittedDismissalSurvivesCompositionOutage`
проверяет реальный HTTP BFF → BSD → Analytics и production outbox worker на двух
изолированных БД PostgreSQL 18. Это дополнение к обычным unit/integration/API
и независимым total80, без изменения их selectors, coverage или guard-ов.

## Запуск

Нужны Go из `go.mod`, Python 3 и отдельный локальный PostgreSQL 18. Все строки ledger
синтетические; общая stage/production БД и реальные выписки не используются.
URL БД допускает только loopback hostname без host/hostaddr/service overrides.

Чистые checkout-ы с origin `https://github.com/fin-assist/<repo>.git`:

- `bank-statement-data`: `f6d7c9a5e6ef62c6e97797f6899aec574b8f6ea1`;
- `analytics`: `e00c6114d011f33576355dee19ecd77e1e7c4e1b`.

```sh
TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable \
  make qase90 BSD_SOURCE=../bank-statement-data ANALYTICS_SOURCE=../analytics
```

Runner проверяет origin, точный HEAD и чистоту обоих соседей до компиляции. `_test.go`
overlays из `testdata/qase90` подключаются только к их `internal/app` через `go test
-overlay`; исходники соседей не меняются. Готовые race binaries передаются отдельному
tagged BFF API-тесту. Прямой `go test -tags=qase90` без runner/fixtures падает.
Отсутствие PostgreSQL, checkout или зависимости также является ошибкой, а не skip.
Каждая команда runner ограничена 240 секундами; timeout/interrupt убивает process
group с дочерними fixtures. Go ограничивает исполнение сценария тремя минутами.
Отдельный workflow `.github/workflows/qase90.yml` (pull_request и push main), job
`Qase 90 cross-service`, запускает тот же runner; публичные соседи не требуют
PAT/секретов. Checkout credentials не сохраняются; workflow token только read.

## Oracle и границы

BSD переиспользует API OFX-import и существующий TLS object-store fixture. После
импорта dev assembly закрывается; на той же test-owned БД запускается штатная
assembly с настоящей проверкой подписанных user/service JWT. Analytics и BFF также
проверяют подписи. Только user signing-key peer — существующий fixture; финансовые
RPC выполняют реальные сервисы. Test binaries общаются по закрытому набору команд
через анонимные pipes, без production endpoint/export/флага.

Сначала исходный реальный пересчёт доставляется в Analytics. Закрытая команда
Analytics fixture временно переименовывает только test-owned `recalculation_marker`.
BSD сохраняет assignment, target, revision, recalc queue и durable true-job;
последующий настоящий Apply проходит projection validation и получает ошибку
хранилища своей транзакции. Штатный handler возвращает канонический
500/retryable=true. Состояние, attempts и future next_attempt_at проверяются
в PostgreSQL после HTTP-ответа. Это отказ реальной Apply-транзакции после BSD
commit; выполнение самой composition closure на этом пути не заявляется. Ошибка
кодирования внутри composition отдельно покрыта существующим
`TestAPIApplyCompositionFailureRollsBackMarker`. Bare network Unavailable без
service retry advice для обязательного retryable=true не подходит.

После восстановления имени таблицы реальный dispatcher получает ACK. В Analytics
проверяются ровно один marker и точный первоначальный target. Его BSD event
исчезает, но financial queue и applied version ещё прежние: существующий
RecalculationClock не получает tick. GET уже видит true marker. Явный повтор PUT
возвращает полный блок с прежним target; timestamp, target, revision, references,
queue generation и jobs совпадают со снимком после ACK. Только затем разрешается
tick полного пересчёта. Итоговый GET имеет applied version ≥ target и false marker.
Независимое ожидание регулярной месячной суммы ledger: Cloud 29900 + Taxi 100000
minor; после исключения Cloud остаётся один Taxi на 100000. Проверяются точные
opaque payment IDs из seed PostgreSQL, title/value/value_kind каждой строки, сумма,
RUB, ready status, count в contract-defined description и отсутствие items_more
для этих коротких списков. Это месячная регулярная сумма, а не фактическое отдельное
сентябрьское списание Taxi 720000.

Это один сценарий временного отказа Apply storage, не проверка рестарта
процесса/потери диска и не stage/deployment acceptance. Обычные layer-тесты BSD
покрывают lost ACK, rollback до commit, повтор после расчёта и purge отдельно.
Обновление pinned neighbors требует повторного полного Q90; статусы Qase и задачи
меняются по фактическим результатам, а не по наличию файлов теста.
