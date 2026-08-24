# Мониторинг прогресса

## Назначение

Отслеживание и отображение прогресса подсчета маршрутов в реальном времени:
- Время выполнения
- Количество обработанных/оставшихся задач (канонических позиций)
- Количество найденных путей
- Эффективность pruning (отсечение по Dead-end)

## Требования

### Частота обновления
- **Интервал:** 1 секунда
- **Формат:** Строковый вывод в stdout (в формате ProgressBar с \r)

### Информация в отчете
Каждый отчёт должен содержать:
```
[время] Tasks: [выполнено]/[всего] (%) | Total paths [всего] | Pruned: [отсечений]
```

**Пример:**
```
[00:05:23] Tasks: 12/24 (50.0%) | Total paths 1564839 | Pruned: 23
```

## Интерфейс Monitor

### Структура данных RealMonitor

```go
type RealMonitor struct {
    startTime          time.Time
    totalTasks         atomic.Uint64  // общее количество задач (subtasks)
    completed          atomic.Uint64  // завершенные задачи
    totalPaths         atomic.Uint64  // найденные пути (с учетом SymmetriesCount)
    connectivityPruned atomic.Uint64  // отсечено по Dead-end pruner
}
```

Все счётчики используют `atomic.Uint64` для потокобезопасного доступа без мьютексов.

**Примечание:** В текущей реализации `connectivityPruned` на самом деле содержит счетчик `DeadEndPruner` отсечений.

### Методы

#### 1. NewMonitor() *RealMonitor

Инициализация мониторинга (параметры не нужны).

```go
func NewMonitor() *RealMonitor {
    return &RealMonitor{}
}
```

#### 2. Start(ctx context.Context)

Запуск периодического отчёта каждую секунду.

Использует `context.Context` для отмены вместо возврата канала.

```go
func (m *RealMonitor) Start(ctx context.Context) {
    m.startTime = time.Now()
    go func() {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                m.report()
            case <-ctx.Done():
                m.report()
                return
            }
        }
    }()
}
```

#### 3. report()

Формирование строки отчёта (без перевода строки — используется \r для перезаписи):

```go
func (m *RealMonitor) report() {
    elapsed := time.Since(m.startTime)
    completed := m.completed.Load()
    totalTasks := m.totalTasks.Load()

    pct := float64(completed) / float64(totalTasks) * 100

    fmt.Printf(
        "\r[%s] Tasks: %d/%d (%.1f%%) | Total paths %d | Cached paths: %d",
        elapsed.String(),
        completed, totalTasks,
        pct,
        m.totalPaths.Load(),
        m.cachedPaths.Load(),
    )
}
```

#### 4. AddTasks(count int)

Добавить задачи в мониторинг (увеличивает totalTasks на count).

```go
func (m *RealMonitor) AddTasks(count int) {
    m.totalTasks.Add(uint64(count))
}
```

#### 5. ReportTaskCompleted()

Регистрация завершения одной задачи.

```go
func (m *RealMonitor) ReportTaskCompleted() {
    m.completed.Add(1)
}
```

#### 6. ReportPathsFound(count int)

Зарегистрировать найденные пути (с учетом орбит).

```go
func (m *RealMonitor) ReportPathsFound(count int) {
    m.totalPaths.Add(uint64(count))
}
```

#### 7. ReportPathsCached(count int)

Зарегистрировать закэшированные пути.

```go
func (m *RealMonitor) ReportPathsCached(count int) {
    m.cachedPaths.Add(uint64(count))
}
```

#### 8. Finish()

Финальный отчёт с переводом строки:

```go
func (m *RealMonitor) Finish() {
    m.report()  // выводит последнее состояние
    fmt.Printf("\n=== Final ===\n")
    fmt.Printf("Time: %s\n", time.Since(m.startTime))
    fmt.Printf("Tasks completed: %d/%d\n", m.completed.Load(), m.totalTasks.Load())
    fmt.Printf("Total paths: %d\n", m.totalPaths.Load())
    fmt.Printf("Cached paths: %d\n", m.cachedPaths.Load())
}
```

## Типы данных

### Result

```go
type Result struct {
    TotalPathsFound int  // найденные пути в поддереве
    Pruned          int  // отсечено по Dead-end pruner
}

func (r *Result) Add(other Result)
// Суммирует поля result и other
```

## Использование в Counter

### Генерация подзадач через кэш

### Генерация подзадач через кэш

```go
func (c *Counter) generateSubTasks(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
    precomputeDepth int,
) *cache.Cache {
    cache := cache.NewCache(c.symmetry)
    groups := c.symmetry.GetCanonicalGroups()
    monitor.AddTasks(len(groups))

    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(workers)
    g.Go(func() error {
        for _, group := range groups {
            p := group.Canonical
            result := c.searcher.GenerateSubtasks(ctx, cache, p, group.OrbitSize, precomputeDepth)
            monitor.ReportPathsCached(result.CachedPaths)
            monitor.ReportTaskCompleted()
        }
        return nil
    })
    _ = g.Wait()

    return cache
}
```

### Параллельный подсчет с кэшем

```go
func (c *Counter) ParallelCountWithDepth(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
    precomputeDepth int,
) uint64 {
    taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)
    monitor.AddTasks(taskCache.ItemsCount())

    total := atomic.Uint64{}
    taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
        result := c.searcher.CountPathsDFS(ctx, p)
        group := c.symmetry.GetCanonicalGroupByPosition(p.Start())

        total.Add(uint64(result.TotalPathsFound * count * group.OrbitSize))
        monitor.ReportPathsFound(result.TotalPathsFound * count * group.OrbitSize)
        monitor.ReportTaskCompleted()
    })

    return total.Load()
}
```

## Использование

### Флаги командной строки (main.go)

```go
workers := flag.Int("workers", 1, "Number of workers for parallel search")
precomputeDepth := flag.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Precompute depth")
```

### Примеры

```bash
# Запуск с 1 воркером (последовательный режим)
go run main.go -size 5 -workers 1

# Запуск с 4 воркерами в параллельном режиме
go run main.go -size 6 -workers 4

# Максимальная параллельность на доске 8×8
go run main.go -size 8 -workers 16
```

## FakeMonitor (для тестов)

```go
type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
    return &FakeMonitor{}
}

func (*FakeMonitor) Start(ctx context.Context)      {}
func (*FakeMonitor) Finish()                        {}
func (*FakeMonitor) AddTasks(count int)             {}
func (*FakeMonitor) ReportTaskCompleted()           {}
func (*FakeMonitor) ReportPathsFound(count int)     {}
func (*FakeMonitor) ReportPathsCached(count int)    {}
```

Пустая реализация всех методов интерфейса для тестов.

## Требования к точности

1. **Пути:** Счётчик путей должен быть точным (атомарные операции обеспечивают потокобезопасность)
2. **Задачи:** Количество выполненных задач — целые числа
3. **Время:** Показания времени могут иметь погрешность ±1 секунда из-за интервала таймера

## Обработка ошибок

- Нет критических зависимостей от мониторинга
- Остановка мониторинга не влияет на результат подсчёта
- Потеря отчётов в тикере не приводит к сбоям
- Отмена контекста корректно останавливает горутину мониторинга

## Производительность

- Overhead мониторинга: <1% (атомарные операции имеют минимальный contention)
- Мьютексы не нужны для обновления счётчиков
- Таймер с таймаутом не накапливает задержки
- Финальный отчёт выводится один раз при завершении
