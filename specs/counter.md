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
const DefaultPrecomputeDepth = 5
// Глубина предварительного разбиения задач по умолчанию
// (совпадает с TwoPhaseBaseDepth, поэтому по умолчанию используется
// однофазная генерация)

const TwoPhaseBaseDepth = 5
// Порог двухфазной генерации: при precomputeDepth > неё используется
// двухфазная схема (см. «Генерация подзадач»)
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

### 3. ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth, oracleDepth int) uint64

```go
func (c *Counter) ParallelCountWithDepth(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
    precomputeDepth int,
    oracleDepth int,
) uint64
// Параллельный подсчет с предварительным разбиением задач через кэш (глубина
// precomputeDepth) и досрочным завершением count-DFS через shape-oracle
// (oracle.Oracle, глубина маски oracleDepth; см. oracle.md).
```

Глубины разведены: `precomputeDepth` — корни подзадач (параллелизм/дедуп),
`oracleDepth` — размер множества в reversal-тождестве (память/время deep-хвоста).
Связка `2·d ≤ n²` из старой схемы снята: oracle корректен для любого
`1 ≤ oracleDepth < totalCells`.

**Алгоритм:**
1. Сгенерировать подзадачи (см. «Генерация подзадач» ниже) — `generateSubTasks` выбирает стратегию по глубине
2. Создать `oracle.New(graph)` (один на прогон, читается всеми воркерами)
3. `monitor.BeginPhase("counting")`, добавить количество подзадач в мониторинг
   (`monitor.AddTasks(taskCache.ItemsCount())`)
4. Запустить worker pool с лимитом workers через `errgroup.Group` (`taskCache.Each`)
5. Режим реверса: `oracleDepth > 0` — shape-oracle на глубине маски
   `oracleDepth` (режим включается всегда, если stop-level достижим из корня);
   `oracleDepth == 0` — legacy prefix-cache reversal (`W/orbitSize` из
   taskCache) при `2·precomputeDepth ≤ totalCells`, иначе обычный спуск
   (см. searcher.md, «Досрочное завершение через реверс»)
6. Для каждой записи в кэше:
   - Получить каноническую пару `(state, end)` и агрегированный вес `Σ count·orbitSize`
   - Вызвать `CountPathsWithReversal` (с oracle при включённом реверсе) для подсчета
     продолжений из этого состояния
    - Умножить результат на вес и добавить к общему счетчику
      (`total += completions * weight`; умножение на размер орбиты уже зашито
      в вес при генерации, `start`/`GetOrbitSize` на этом этапе не нужны)
    - Зарегистрировать завершение через `monitor.ReportPathsFound()`,
      `monitor.ReportPruned(result.Pruned)` и `monitor.ReportTaskCompleted()`
7. Вернуть суммарное количество путей

**Метрики oracle:** при установленном `KNIGHTTOUR_ORACLE_STATS` в stderr печатается
строка `oracle: lookups=… computes=… classes=…` — отношение классов к числу пар
показывает фактическое трансляционное сжатие (см. oracle.md).

## Генерация подзадач

### Однофазная (precomputeDepth ≤ TwoPhaseBaseDepth)

Пока глубина мала, число групп стартов («корней генерации») достаточен источник
параллелизма:

1. Создать кэш `cache.NewCache(symmetry)`
2. Получить группы канонических позиций: `symmetry.GetCanonicalGroups()`
3. Для каждой группы вызвать `searcher.GenerateSubtasks(ctx, cache, canonicalPos, orbitSize, depth)`;
   генерация групп **параллельна** через `errgroup` с `SetLimit(workers)`
4. Мониторинг: `monitor.BeginPhase("generation")` (или имя фазы от вызывающего),
   `monitor.AddTasks(len(groups))`, на группу — `ReportCacheWrites` + `ReportPruned` +
   `ReportTaskCompleted`

### Двухфазная (precomputeDepth > TwoPhaseBaseDepth)

Проблема однофазной генерации на большой глубине: канонических стартов сильно
меньше, чем workers (на 8×8 — ~10 групп), поэтому генерация упирается в ~10
горизонтально параллельных DFS и воркеры простаивают.

Константы:

```go
const TwoPhaseBaseDepth = 5
// Глубина промежуточного кэша фазы A; при precomputeDepth <= неё — однофазный путь.
```

**Фаза A.** Однофазная генерация до глубины `TwoPhaseBaseDepth` → промежуточный
кэш (быстрая: группы хватает для параллелизма, поддеревья мелкие).

**Фаза B.** Снимок промежуточного кэша через `cache.Entries()`; каждая запись —
независимая задача: `searcher.ExtendSubtask(ctx, taskCache, entry.Path, entry.Weight, precomputeDepth)`
в **отдельный финальный кэш**, параллельно через `errgroup` с `SetLimit(workers)`.
Задач — тысячи записей вместо ~10 групп → полная утилизация воркеров.

**Корректность.** Каждый префикс целевой глубины проходит ровно через один
промежуточный канонический ключ; продолжения симметричных образов совпадают с
точностью до канонизации (граф/прунер D4-эквивариантны), поэтому финальный кэш
(ключи и веса) идентичен однофазной генерации — reversal-фаза не замечает разницы.
Подробное обоснование — searcher.md, «ExtendSubtask».

**Мониторинг:** фазы `"gen A"` и `"gen B"` (`monitor.BeginPhase` перед каждой,
причём `BeginPhase("gen B")` — после полного завершения фазы A);
`AddTasks(len(groups))` (фаза A) + `AddTasks(intermediateCount)` (фаза B);
на каждую завершённую задачу — `ReportCacheWrites` (записи в целевой кэш),
`ReportPruned` и `ReportTaskCompleted` текущей фазы.

**Память:** промежуточный кэш живёт только время генерации и освобождается до
фазы подсчёта. Oracle переживает обе фазы, но его таблица — классы форм размера
`oracleDepth` (в ~10–20 раз меньше числа конкретных пар; см. oracle.md), поэтому
память прогона определяется корнями `precomputeDepth`, а не deep-хвостом.

**Использование:**
```go
g := graph.New(5)
c := counter.NewCounter(g)

count := c.ParallelCountWithDepth(ctx, monitor, 8, 5, 10) // 8 воркеров, корни на 5, oracle на 10
fmt.Printf("Total tours: %d\n", count)
```

## Бенчмарки

`counter/benchmark_test.go`:
- `BenchmarkCountAllToursParallel` — замеряет `ParallelCountWithDepth` на досках 5×5 и 6×6,
  перебирая глубины предподсчёта `depth = 1..size*size/2` (вложенные подбенчмарки `sizeN/depthD`),
  число воркеров равно `runtime.NumCPU()`.

## Мониторинг

Мониторинг выводит прогресс каждую секунду — **только текущую фазу**:

```
[1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334 | ETA 3.953s
```

И финальный отчет — по строке на каждую фазу + итоги:

```
=== Final ===
Total time: 63ms
Phase generation [41ms]: tasks 6/6 | paths 0 | writes 2795 | pruned 12034
Phase counting [22ms]: tasks 95224/95224 | paths 6637920 | writes 0 | pruned 180322
Total paths: 6637920
```

Фазы запуска: `generation` (depth ≤ TwoPhaseBaseDepth) либо `gen A` + `gen B`,
затем `counting`.

**Методы интерфейса Monitor:**
```go
type Monitor interface {
    Start(ctx context.Context)
    Finish()
    BeginPhase(name string) // переключает мониторинг на новую фазу (между фазами, при остановленных воркерах)
    AddTasks(count int)     // в текущую фазу
    ReportTaskCompleted()   // текущая фаза
    ReportPathsFound(count int)
    ReportCacheWrites(count int) // число записей в кэш (не «закэшированные пути»)
    ReportPruned(count int)      // отсечённые прунером ветви
}
```

Все репорты относятся к **активной** фазе; счётчики фаз — атомарные.

## Использование в main.go

main.go разбит на тестируемые части: структура аргументов, их парсинг и запуск подсчёта.

```go
// appArgs – распарсенные и провалидированные параметры запуска.
type appArgs struct {
    size            int
    workers         int
    precomputeDepth int
    oracleDepth     int
}

// parseArgs разбирает аргументы командной строки (без имени программы) и
// валидирует их: size 5–8, workers >= 1, precomputeDepth в [1, size^2/2],
// oracleDepth = 0 (legacy prefix-cache reversal) либо в [1, size^2 - precomputeDepth]
// (stop-level totalCells - oracleDepth должен быть достижим из корней).
// Возвращает ошибку вместо log.Fatal — это делает парсинг тестируемым.
func parseArgs(args []string) (*appArgs, error)

// run строит граф и счётчик и выполняет параллельный подсчёт.
// Монитор передаётся параметром, чтобы в тестах использовать FakeMonitor.
func run(ctx context.Context, monitor monitoring.Monitor, args *appArgs) uint64 {
    g := graph.New(args.size)
    c := counter.NewCounter(g)
    return c.ParallelCountWithDepth(ctx, monitor, args.workers, args.precomputeDepth, args.oracleDepth)
}

func main() {
    args, err := parseArgs(os.Args[1:])
    if err != nil {
        log.Fatal(err)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    realMonitor := monitoring.NewMonitor()
    realMonitor.Start(ctx)
    defer realMonitor.Finish()

    run(ctx, realMonitor, args)
}
```

**Тестуемость (main_test.go):**
- `parseArgs` — табличные тесты: дефолты (`workers = runtime.NumCPU()`,
  `precomputeDepth = counter.DefaultPrecomputeDepth`), валидные доски 5–8,
  ошибки размера/глубины/workers.
- `run` с `FakeMonitor` на доске 5×5 должен возвращать `1728`.

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
        5,
        8,
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
        5,
        8,
    )
    countPar := counter.ParallelCountWithDepth(
        context.Background(), 
        monitoring.NewFakeMonitor(), 
        4,
        5,
        8,
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
