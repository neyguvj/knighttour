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
| `-tail-memo` | ≥ 0 (0 — выключен) | 0 |

Валидация (`parseArgs`): явный 0 у `-precompute-depth` → ошибка; неизвестные флаги → ошибка
(`flag.ContinueOnError`, вывод в stderr). Глубина разреза глубже половины доски дуальна
обращению тура.

## Структуры и функции

```go
type appArgs struct { size, workers, precomputeDepth int }

func parseArgs(args []string) (*appArgs, error)
func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 // graph+counter → счёт
```

## Graceful shutdown (Ctrl+C)

`signal.NotifyContext(os.Interrupt, SIGTERM)`:

- первый сигнал отменяет контекст → воркеры завершаются по `ctx.Err()`,
  `ParallelCountWithDepth` возвращается;
- печатается сообщение о прерывании;
- отложенный `monitor.Finish()` печатает частичный финальный отчёт по фазам;
- второй Ctrl+C завершает мгновенно (`NotifyContext` сам снимает обработчик).

## Ограничения и edge cases

- Режим подсчёта один (class mode), отдельных флагов нет.
- Обработка сигналов проверяется вручную (`kill -INT <pid>` → частичный отчёт без паники).

## Тесты

`main_test.go`: `TestParseArgs` — табличные кейсы валидации всех флагов и границ;
`TestRunCountMatchesReference` — `run` с FakeMonitor для 5×5 == 1728.

## Связанные

`specs/counter.md`, `specs/monitoring.md`.
