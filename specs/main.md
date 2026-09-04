# main.go — точка входа и CLI

## Назначение

Разбор аргументов командной строки, сборка компонентов (graph → counter),
запуск мониторинга и поиска, корректное завершение по сигналу.

## Флаги командной строки

```go
size := fs.Int("size", 5, "Board size (5-8)")
workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
precomputeDepth := fs.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Root/subtask generation depth")
oracleDepth := fs.Int("oracle-depth", 0, "Shape-oracle reversal mask size (0 = legacy prefix-cache reversal)")
```

Валидация (`parseArgs`, табличные тесты в `main_test.go`):

- `-size` — целое от 5 до 8;
- `-workers` — не менее 1;
- `-precompute-depth` — от 1 до `size*size/2`;
- `-oracle-depth` — 0 (по умолчанию: legacy prefix-cache reversal при достижимом
  уровне, как до появления oracle; без реверса иначе) или от 1 до
  `size*size - precompute-depth`. Верхняя граница — из достижимости stop-level:
  oracle прекращает спуск на уровне `totalCells − oracleDepth`, и этот уровень
  должен быть не глубже корней подзадач (`precompute-depth`), иначе реверс молча
  не сработает, а legacy-режим при `oracle-depth > 0` уже выключен — тихая
  деградация до чистого DFS. Привязка `2·depth ≤ n²` снята (см. oracle.md);
- неизвестные флаги → ошибка (`flag.ContinueOnError`, вывод в stderr).

Смысл развязки глубин: `-precompute-depth` — корни подзадач (параллелизм и дедуп
весов), `-oracle-depth` — размер множества в reversal-тождестве (определяет память
и время deep-хвоста подсчёта). Пример экономии памяти на 8×8:
`-precompute-depth 10 -oracle-depth 14`.

## Структуры и функции

```go
type appArgs struct {
    size            int
    workers         int
    precomputeDepth int
    oracleDepth     int
}

func parseArgs(args []string) (*appArgs, error)

// run собирает graph + counter и запускает подсчёт; возвращает число маршрутов.
func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64
```

## Обработка Ctrl+C (graceful shutdown)

`main.go` перехватывает `SIGINT`/`SIGTERM` через `signal.NotifyContext`:

- первый сигнал отменяет контекст → воркеры searcher/cache завершаются по
  `ctx.Err()`, `ParallelCountWithDepth` возвращается;
- в stdout печатается сообщение о прерывании;
- отложенный `monitor.Finish()` печатает финальный отчёт с накопленными на
  момент прерывания данными (по фазам, включая неполные счётчики);
- второй Ctrl+C завершает процесс мгновенно (`NotifyContext` сам снимает
  обработчик после первого сигнала — дефолтное поведение Go).

```go
func main() {
    args, err := parseArgs(os.Args[1:])
    if err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    realMonitor := monitoring.NewMonitor()
    realMonitor.Start(ctx)
    defer realMonitor.Finish()

    run(ctx, realMonitor, args)

    if err := ctx.Err(); errors.Is(err, context.Canceled) {
        fmt.Println("\nInterrupted: showing partial results")
    }
}
```

### Пример прерывания (SIGINT на доске 8×8)

```
[3.742s] Phase counting | Tasks: 0/4436 (0.0%) | ... | ETA --
Interrupted: showing partial results
=== Final ===
Total time: 3.741932875s
Phase generation [858.125µs]: tasks 10/10 | paths 0 | writes 6020 | pruned 173
Phase counting [3.741037875s]: tasks 906/4436 | paths 57349936 | writes 0 | pruned 310590871
Total paths: 57349936
```

## Примеры запуска

```bash
# Запуск с 1 воркером (последовательный режим)
go run main.go -size 5 -workers 1

# Запуск с 4 воркерами в параллельном режиме
go run main.go -size 6 -workers 4

# Максимальная параллельность на доске 8×8
go run main.go -size 8 -workers 16
```

## Тестирование

`main_test.go`:

- `TestParseArgs` — табличные тесты валидации флагов;
- `TestRunCountMatchesReference` — `run` с FakeMonitor для 5×5 = 1728.

Обработка сигналов проверяется вручную: запуск + `kill -INT <pid>` →
в выводе сообщение о прерывании и финальный отчёт с частичными данными.
