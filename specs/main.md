# main.go — точка входа и CLI

## Назначение

Разбор аргументов командной строки, сборка компонентов (graph → counter),
запуск мониторинга и поиска, корректное завершение по сигналу.

## Флаги командной строки

```go
size := fs.Int("size", 5, "Board size (5-8)")
workers := fs.Int("workers", runtime.NumCPU(), "Number of workers for parallel search")
precomputeDepth := fs.Int("precompute-depth", 0, "Root/subtask generation depth (default: per board size)")
tailMemo := fs.Int("tail-memo", 0, "Persistent per-worker tail memo in counting: max popcount(todo) stored (0 = off)")
```

Валидация (`parseArgs`, табличные тесты в `main_test.go`):

- `-size` — целое от 5 до 8;
- `-workers` — не менее 1;
- `-precompute-depth` — от 1 до `size*size / 2` (meet-in-the-middle: разрез
  глубже половины доски дуален обращению тура); если флаг не передан
  (sentinel 0) — `counter.DefaultPrecomputeDepth(size)`; явный 0 → ошибка;
- `-tail-memo` — ≥ 0 (0 — persistent tail-мемо финального прохода выключен,
  см. shapecount.md/план 03; значение — максимум `popcount(todo)`, который
  сохраняется в таблицу воркера);
- неизвестные флаги → ошибка (`flag.ContinueOnError`, вывод в stderr).

Режим один (class mode, см. counter.md/shapecount.md), отдельных флагов нет.
Глубина по умолчанию — таблица `{5: 6, 6: 10, 7: 20, 8: 14}` (эмпирика свипов;
для 8×8 — осторожное значение, глубже аккумулятор M рискует не влезть в память).

## Структуры и функции

```go
type appArgs struct {
    size            int
    workers         int
    precomputeDepth int
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
