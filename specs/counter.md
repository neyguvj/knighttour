# Компонент Counter: Агрегация и подсчет маршрутов

## Назначение

Организация полного подсчёта всех открытых маршрутов с учетом:
- Симметрий доски (уменьшение объема поиска)
- Параллельных вычислений (ускорение на многопроцессорных системах)
- Мониторинга прогресса (отображение статистики в реальном времени)

## Структура данных

```go
type Counter struct {
    graph    *Graph           // граф смежности
    symmetry *Symmetry        // для канонических групп и размеров орбит
    searcher *Searcher        // для выполнения поиска
}
```

## Константы

```go
const DefaultPrecomputeDepth = 1
// Глубина предварительного разбиения задач по умолчанию
```

## Инициализация

```go
func NewCounter(graph *Graph) *Counter
// Создает Counter с инициализацией симметрий и searcher
```

## Основные методы

### 1. CountFromPosition(ctx context.Context, start int) int

```go
func (c *Counter) CountFromPosition(ctx context.Context, start int) int
// Подсчет маршрутов из конкретной стартовой позиции
```

**Пример:**
```go
count := counter.CountFromPosition(context.Background(), 0)
fmt.Printf("Paths from position 0: %d\n", count)
```

### 2. ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64

```go
func (c *Counter) ParallelCount(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
) uint64
// Параллельный подсчет с использованием канонических групп и глубины по умолчанию
```

### 3. ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth int) uint64

```go
func (c *Counter) ParallelCountWithDepth(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
    precomputeDepth int,
) uint64
// Параллельный подсчет с предварительным разбиением задач через кэш
```

**Алгоритм:**
1. Создать кэш с помощью `cache.NewCache(symmetry)`
2. Получить группы канонических позиций: `symmetry.GetCanonicalGroups()`
3. Для каждой группы вызвать `searcher.GenerateSubtasks(ctx, cache, canonicalPos, orbitSize, depth)` для предварительного разбиения; генерация групп **параллельна** через тот же `errgroup` с `SetLimit(workers)` (раньше вся генерация шла в одной горутине)
4. Добавить количество групп в мониторинг через `monitor.AddTasks(len(groups))`
5. После генерации подзадач добавить их количество (`taskCache.ItemsCount()`)
6. Запустить worker pool с лимитом workers через `errgroup.Group`
7. Для каждой записи в кэше:
   - Получить каноническую пару `(state, end)` и агрегированный вес `Σ count·orbitSize`
   - Вызвать `CountPathsDFS` для подсчета продолжений из этого состояния
   - Умножить результат на вес и добавить к общему счетчику
     (`total += completions * weight`; умножение на размер орбиты уже зашито
     в вес при генерации, `start`/`GetOrbitSize` на этом этапе не нужны)
   - Зарегистрировать завершение через `monitor.ReportPathsFound()` и `monitor.ReportTaskCompleted()`
8. Вернуть суммарное количество путей

**Использование:**
```go
g := graph.New(5)
c := counter.NewCounter(g)

count := c.ParallelCountWithDepth(ctx, monitor, 8, 0) // с 8 воркерами
fmt.Printf("Total tours: %d\n", count)
```

## Бенчмарки

`counter/benchmark_test.go`:
- `BenchmarkCountAllToursParallel` — замеряет `ParallelCountWithDepth` на досках 5×5 и 6×6,
  перебирая глубины предподсчёта `depth = 1..size*size/2` (вложенные подбенчмарки `sizeN/depthD`),
  число воркеров равно `runtime.NumCPU()`.

## Мониторинг

Мониторинг выводит прогресс каждую секунду:

```
[00:XX:YY] Tasks: 6/6 (100.0%) | Total paths 1728 | Cached paths: 42
```

И финальный отчет:

```
=== Final ===
Time: XXs
Tasks completed: 6/6
Total paths: 1728
Cached paths: 42
```

**Методы интерфейса Monitor:**
```go
type Monitor interface {
    AddTasks(count int)
    Start(ctx context.Context)
    Finish()
    ReportTaskCompleted()
    ReportPathsFound(count int)
    ReportPathsCached(count int)
}
```

## Использование в main.go

```go
func main() {
    size := flag.Int("size", 5, "Board size (5-8)")
    workers := flag.Int("workers", 1, "Number of workers for parallel search")
    precomputeDepth := flag.Int("precompute-depth", counter.DefaultPrecomputeDepth, "Precompute depth for subtasks")

    flag.Parse()

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    realMonitor := monitoring.NewMonitor()
    realMonitor.Start(ctx)
    defer realMonitor.Finish()

    g := graph.New(*size)
    c := counter.NewCounter(g)
    c.ParallelCountWithDepth(ctx, realMonitor, *workers, *precomputeDepth)
}
```

**Примеры запуска:**
```bash
# Последовательный режим (workers=1)
go run main.go -size 5 -workers 1

# Параллельный режим с 4 воркерами
go run main.go -size 6 -workers 4

# Максимальная параллельность на доске 8×8
go run main.go -size 8 -workers 16
```

## Симметрии и канонизация

### CanonicalGroup

```go
type CanonicalGroup struct {
    Canonical int     // каноническая позиция (лексикографически минимальная)
    OrbitSize int     // размер орбиты (сколько симметричных позиций в классе)
    Positions []int   // все позиции в группе
}
```

### Использование:

```go
groups := symmetry.GetCanonicalGroups()
for _, group := range groups {
    // Группа канонических позиций с размером орбиты group.OrbitSize
    
    cache := cache.NewCache(symmetry)
    result := searcher.GenerateSubtasks(ctx, cache, group.Canonical, group.OrbitSize, depth)
    
    // Кэш содержит подзадачи с агрегированным весом Σ count·orbitSize
}
```

## Тесты

```go
func TestCounterCountAllTours(t *testing.T) {
    g := graph.New(5)
    counter := counter.NewCounter(g)

    count := counter.ParallelCountWithDepth(
        context.Background(), 
        monitoring.NewFakeMonitor(), 
        1,
        0,
    )

    // Проверка результата (с учетом орбит всех групп)
}

func TestCounterParallel(t *testing.T) {
    g := graph.New(5)
    counter := counter.NewCounter(g)

    countSeq := counter.ParallelCountWithDepth(
        context.Background(), 
        monitoring.NewFakeMonitor(), 
        1,
        0,
    )
    countPar := counter.ParallelCountWithDepth(
        context.Background(), 
        monitoring.NewFakeMonitor(), 
        4,
        0,
    )

    require.Equal(t, countSeq, countPar)
}

func TestCounterFromPosition(t *testing.T) {
    g := graph.New(5)
    counter := counter.NewCounter(g)

    count := counter.CountFromPosition(context.Background(), 0)

    require.Greater(t, count, 0)
}
```

## Ошибки и edge cases

### 1. Дублирование при отсутствии canonical check

```go
// НЕПРАВИЛЬНО:
total := uint64(0)
for start := 0; start < graph.GetTotalCells(); start++ {
    total += searcher.CountPaths(ctx, start).TotalPathsFound // каждый старт обрабатывается повторно
}

// ПРАВИЛЬНО:
groups := symmetry.GetCanonicalGroups()
for _, group := range groups {
    result := searcher.CountPaths(ctx, group.Canonical)
    total += uint64(result.TotalPathsFound * group.OrbitSize)
}
```

### 2. Race condition в параллельном коде

```go
// НЕПРАВИЛЬНО:
var total int
for _, task := range tasks {
    go func() {
        result := searcher.CountPaths(ctx, p)
        total += result.TotalPathsFound // race condition!
    }()
}

// ПРАВИЛЬНО:
total := atomic.Uint64{}
g.Go(func() error {
    result := searcher.CountPathsDFS(ctx, p)
    // weight = Σ count·orbitSize уже лежит в значении кэша
    total.Add(uint64(result.TotalPathsFound) * uint64(weight))
    return nil
})
```

## Ограничения и возможные улучшения

1. **Dynamic load balancing**: Перераспределение работы между workers на основе времени выполнения
2. **Checkpointing**: Промежуточное сохранение результатов (для долгих расчетов)
3. **Adaptive parallelism**: Количество workers зависит от размера доски и доступных ядер

## Заключение

Counter — финальный компонент, который объединяет симметрии, параллелизм и мониторинг для эффективного подсчёта всех маршрутов.
