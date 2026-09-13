# main: точка входа и CLI

## Ответственность

Разбор и валидация аргументов, сборка компонентов (graph → counter), запуск мониторинга и
подсчёта, корректное завершение по сигналу.

## Флаги командной строки

| Флаг | Диапазон | По умолчанию |
|------|----------|--------------|
| `-size` | 5–8 | 5 |
| `-workers` | ≥ 1 | `runtime.NumCPU()` |
| `-precompute-depth` | 1 … `size²/2` (sentinel 0 → авто) | `counter.DefaultPrecomputeDepth(size)` |
| `-gc-percent` | ≥ 0 (0 — не трогать GC рантайма) | 40 (`counter.DefaultGCPercentReversal`) |

Валидация (`parseArgs`): явный 0 у `-precompute-depth` → ошибка; отрицательный
`-gc-percent` → ошибка (режим «GC off» не допускается); неизвестные флаги → ошибка
(`flag.ContinueOnError`, вывод в stderr). Глубина разреза глубже половины доски дуальна
обращению тура.

## Структуры и функции

```go
type appArgs struct { size, workers, precomputeDepth, gcPercent int }

func parseArgs(args []string) (*appArgs, error)
func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 // graph+counter → счёт
```

`run` собирает `graph.New(size)` + `counter.NewCounter`, передаёт `-gc-percent` в
`counter.SetGCPercent` (ADR-014) и запускает `ParallelCountWithDepth`. Внешний env `GOGC`
в прогоне перезаписывается этим значением.

## Graceful shutdown (Ctrl+C)

`signal.NotifyContext(os.Interrupt, SIGTERM)`:

- первый сигнал отменяет контекст → воркеры завершаются по `ctx.Err()`,
  `ParallelCountWithDepth` возвращается;
- печатается сообщение о прерывании;
- отложенный `monitor.Finish()` печатает частичный финальный отчёт по фазам;
- второй Ctrl+C завершает мгновенно (`NotifyContext` сам снимает обработчик).

## Ограничения и edge cases

- Единственный конвейер подсчёта (ADR-016); выбор схемы флагом не предусмотрен.
- `-gc-percent` эффективен по умолчанию: штатный прогон идёт конвейером с пониженным
  GOGC (ADR-014).
- Обработка сигналов проверяется вручную (`kill -INT <pid>` → частичный отчёт без паники).

## Тесты

`main_test.go`: `TestParseArgs` — табличные кейсы валидации всех флагов и границ, включая
отрицательный `-gc-percent` и дефолтные значения; `TestRunCountMatchesReference` — `run` с
FakeMonitor для 5×5 == 1728.

## Связанные

`specs/counter.md`, `specs/monitoring.md`.
