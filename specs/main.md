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
| `-mode` | `class` \| `reversal` | `class` |

Валидация (`parseArgs`): явный 0 у `-precompute-depth` → ошибка; неизвестный `-mode` →
ошибка; неизвестные флаги → ошибка (`flag.ContinueOnError`, вывод в stderr). Глубина разреза
глубже половины доски дуальна обращению тура.

## Структуры и функции

```go
type appArgs struct { mode string; size, workers, precomputeDepth, tailMemo int }

func parseArgs(args []string) (*appArgs, error)
func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 // graph+counter → счёт
```

Поле со ссылкой (`mode string`) — первым: порядок навязан `govet/fieldalignment`
(`make fix` переставляет иначе), набор полей — контрактом.

`run` маппит `-mode` в `counter.SetMode` (`class` → `ModeClass`, `reversal` → `ModeReversal`).

## Graceful shutdown (Ctrl+C)

`signal.NotifyContext(os.Interrupt, SIGTERM)`:

- первый сигнал отменяет контекст → воркеры завершаются по `ctx.Err()`,
  `ParallelCountWithDepth` возвращается;
- печатается сообщение о прерывании;
- отложенный `monitor.Finish()` печатает частичный финальный отчёт по фазам;
- второй Ctrl+C завершает мгновенно (`NotifyContext` сам снимает обработчик).

## Ограничения и edge cases

- Режим подсчёта выбирается флагом `-mode` (ADR-011); дефолт — `class`.
- Обработка сигналов проверяется вручную (`kill -INT <pid>` → частичный отчёт без паники).

## Тесты

`main_test.go`: `TestParseArgs` — табличные кейсы валидации всех флагов и границ, включая
неизвестный `-mode`; `TestRunCountMatchesReference` — `run` с FakeMonitor для 5×5 == 1728
в обоих режимах.

## Связанные

`specs/counter.md`, `specs/monitoring.md`.
