# Техническое задание: Поиск всех открытых маршрутов коня

## Общее описание

Реализовать программу для подсчета количества всех открытых маршрутов коня на досках размером от 5×5 до 8×8.

**Открытый маршрут** — это последовательность ходов коня, при которой он посещает каждую клетку доски ровно один раз (не возвращаясь в начальную клетку).

## Входные и выходные данные

- **Вход**: Размер доски N (целое число от 5 до 8), количество воркеров для параллельного поиска
- **Выход**: Количество открытых маршрутов коня (64-bit целое число)

### Примеры
```
$ go run main.go -size 5 -workers 1
Запуск последовательного поиска на доске 5×5

$ go run main.go -size 8 -workers 8
Запуск параллельного поиска с 8 воркерами на доске 8×8
```

## Архитектура

Система разбита на независимые компоненты с четкими обязанностями:

| Компонент | Файл | Ответственность |
|-----------|------|----------------|
| Graph | graph/graph.go | Предварительно вычисленный граф смежности с масками соседей |
| State | state/state.go | Битовая маска посещенных клеток (uint64) |
| Symmetry | symmetry/symmetry.go | Работа с симметриями доски и канонизация путей |
| DeadEndPruner | pruner/deadend.go | Отсечение тупиковых ветвей поиска |
| Cache | cache/cache.go | Мемоизация промежуточных результатов с шардингом (64 шарда) |
| Searcher | searcher/searcher.go | DFS backtracking алгоритм |
| Counter | counter/counter.go | Агрегация и подсчет маршрутов с учетом симметрий |
| Monitor | monitoring/monitor.go | Отображение прогресса поиска в реальном времени |
| Main | main.go | CLI, сборка компонентов, graceful shutdown (см. `specs/main.md`) |

## Оптимизации

### 1. Битовые маски
- Использование `uint64` для представления состояния (до 8×8 = 64 клетки)
- Быстрые операции: `&`, `|`, `<<`, `>>`
- Компактное хранение и эффективная хэш-функция
- Использование `bits.OnesCount64` для быстрого подсчета битов

### 2. Симметрия доски (группа D4)
- 8 трансформаций (4 поворота + 4 отражения)
- Предварительное вычисление канонических позиций для всех клеток
- Уменьшение объема поиска в зависимости от типа позиции:
  - Центральные (не на диагонали): в 8 раз
  - На диагоналях/ребрах: в 4 раза
  - Угловые: в 4 раза

### 3. Dead-end pruning
- Проверка непосещенных клеток без соседей (изолированные)
- Эффективен на поздних этапах поиска

## Компоненты системы

### state/state.go
**Ответственность:** Представление текущего состояния поиска

**Структуры:**
```go
type State uint64
```

**Методы:**
- `NewState(visited ...int) State` — создание начального состояния с посещенными клетками
- `IsVisited(pos int) bool` — проверка посещенности клетки
- `Visit(pos int) State` — отметить клетку как посещенную
- `Unvisit(pos int) State` — сбросить флаг посещенности
- `IsFull(cellsCount int) bool` — все клетки посещены?
- `CountBits() int` — количество посещенных клеток (использует bits.OnesCount64)
- `GetUnvisitedMask(cellsCount int) State` — получить маску непосещенных клеток
- `IsUnvisited(pos int) bool` — проверка, что клетка не посещена
- `Intersect(mask State) State` — пересечение двух состояний
- `Union(mask State) State` — объединение двух состояний
- `IsEmpty() bool` — пустое состояние?
- `TrailingZeroBits() uint` — номер первого установленного бита
- `AllVisited() iter.Seq[int]` — итератор по посещенным клеткам
- `Bit(pos int) State` — (пакетная функция) маска с одним битом pos

**Правило:** прямые битовые операции (`<<`, `&^`, `m &= m-1`, `math/bits`) допустимы
только внутри маленьких функций пакета state; остальной код использует методы State,
`Bit()` и `AllVisited()`.

**Тесты:**
- Создание состояний с различными посещенными клетками
- Проверка IsVisited/Visit корректности
- IsFull для полного и частичного состояния
- CountBits для различных паттернов
- Масочные операции (Intersect, Union, GetUnvisitedMask)

---

### symmetry/symmetry.go
**Ответственность:** Работа с симметриями доски для уменьшения объема поиска

**Группа D4 (8 трансформаций):**
1. Identity (без изменений)
2. Rotate 90°
3. Rotate 180°
4. Rotate 270°
5. Flip horizontal
6. Flip vertical
7. Flip diag1 (y=x)
8. Flip diag2 (y=-x)

**Структура:**
```go
type Transform func(x, y, size int) (int, int)

type Symmetry struct {
    size         int
    perms        [8][64]uint8  // LUT: образ каждой клетки при каждом преобразовании
    canonical    []int         // каноническая позиция для каждой клетки
    canonicalIdx []uint8       // индекс лучшего преобразования для каждой клетки
    orbitSize    []int         // размер орбиты для каждой клетки
    bestIdx      [][]uint8     // оптимальное преобразование для пар (start, end)
    groups       []CanonicalGroup
}
```

**Методы:**
- `NewSymmetry(size int) *Symmetry` — создание симметрий и предварительное вычисление канонических позиций
- `GetCanonicalPosition(pos int) int` — вернуть каноническую позицию из линейного индекса
- `IsCanonicalPosition(pos int) bool` — является ли позиция канонической?
- `GetOrbitSize(pos int) int` — размер орбиты (сколько симметричных позиций в классе)
- `GetCanonicalGroups() []CanonicalGroup` — получить все группы канонических позиций
  ```go
  type CanonicalGroup struct {
      Canonical int    // каноническая позиция
      OrbitSize int    // размер орбиты
      Positions []int  // все позиции в группе
  }
  ```
- `CanonicalizePath(p path.Path) path.Path` — канонизировать путь (найти лексикографически минимальную форму)

**Использование:**
- При старте поиска использовать `GetCanonicalGroups()` для получения уникальных стартовых позиций
- Для каждой канонической позиции умножать результат на `OrbitSize`
- Канонизация путей в кэше для объединения симметричных состояний

**Тесты:**
- Проверка инверсности трансформаций
- GetCanonicalPosition для углов, ребер и центра
- GetOrbitSize для различных позиций
- CanonicalizePath для путей

### pruner/deadend.go
**Ответственность:** Отсечение тупиковых ветвей поиска

**DeadEndPruner:**
```go
type DeadEndPruner struct {
    graph *graph.Graph  // граф смежности (хранится ссылка)
}
```

**Методы:**
- `NewDeadEndPruner(graph *graph.Graph) *DeadEndPruner` — создание прунера (O(1), без предвычисления масок)
- `ShouldPruneAfterVisit(last int, unvisited state.State) bool` — горячий метод для DFS:
  локальная проверка за O(deg(last)) сразу после посещения клетки `last`

**Тесты:**
- Отдельные тесты на каждый случай (пустое состояние, одна клетка, изолированные вершины)
- Проверка что pruner НЕ отсекает валидные продолжения
- Вероятностный тест эквивалентности полному скану (`TestShouldPruneAfterVisit_MatchesFullScan`)

---

### cache/cache.go
**Ответственность:** Мемоизация промежуточных результатов для избежания повторных вычислений

**Структура:**
```go
type shard struct {
    sync.RWMutex
    data map[path.Path]int  // canonical path → countOfSolutions
}

type Cache struct {
    shards   [64]shard      // шардинг для параллельного доступа
    symmetry *Symmetry       // для канонизации путей
}
```

**Методы:**
- `NewCache(sym *Symmetry) *Cache` — создание кэша с 64 шардами
- `Get(path path.Path) (int, bool)` — получить результат для канонического пути
- `Set(path path.Path, val int)` — сохранить результат (суммирует при совпадении)
- `Has(path path.Path) bool` — проверка наличия записи
- `Delete(path path.Path)` — удаление записи
- `Clear()` — очистка всех шардов
- `ItemsCount() int` — количество записей в кэше
- `Each(ctx context.Context, workers int, f func(ctx context.Context, p path.Path, count int))` — параллельная итерация по всем записям (errgroup)

**Хэширование:**
- Мультипликативный хэш без аллокаций (умножение на константы золотого сечения)
- Индекс шарда = старшие 6 бит хэша (numShards = 64)

**Использование:**
- При рекурсивном вызове сначала проверить кэш
- После подсчета всех путей сохранить в кэш
- Канонизация путей объединяет симметричные состояния

**Ограничения:**
- Для 8×8 количество возможных состояний огромно (2^64)
- Эффективен для поддеревьев с малым числом непосещенных клеток

**Тесты:**
- Проверка Get/Set корректности
- Параллельный доступ (мультипоточный test)
- Hit/miss ratio для известных паттернов

### searcher/searcher.go
**Ответственность:** Основной алгоритм поиска с использованием всех оптимизаций

**Структура:**
```go
type Searcher struct {
    graph   *Graph
    sym     *Symmetry       // для канонизации путей
    deadend *DeadEndPruner  // отсечение тупиков
}
```

**Методы:**

1. `CountPaths(ctx context.Context, start int) types.Result`
   - Создает начальный путь с посещенной стартовой клеткой
   - Вызывает CountPathsDFS для рекурсивного поиска
   - Возвращает types.Result с TotalPathsFound

2. `CountPathsDFS(ctx context.Context, p path.Path) types.Result`
   - Считает полные маршруты из состояния p; если состояние уже полное — возвращает 1
   - Внутри — горячий рекурсивный метод `dfs` по битовым маскам с prune через
     `deadend.ShouldPruneAfterVisit`

3. `GenerateSubtasks(ctx context.Context, c *cache.Cache, start int, orbitSize int, depth int) types.Result`
   - Генерирует префиксы глубины depth из стартовой позиции и сохраняет их в кэш
   - Позиции, пропускаемые по цветовому правилу (`SholdSkip`), дают пустой результат
   - Возвращает types.Result с CacheWrites (число записей в кэш) и Pruned (отсечённые ветви)

4. `dfs(ctx context.Context, st state.State, start, end, depth int, c *cache.Cache, stats *dfsStats, rev *Reversal) int`
   - Единый внутренний горячий DFS: используется и для полных маршрутов
     (`depth = totalCells`, `c = nil`), и для генерации префиксов
     (`depth = precomputeDepth`, `c != nil`)

**Алгоритм:**
```
dfs(state, end, depth, cache, weight, stats, rev):
    if ctx.Err() != nil: return 0

    if CountBits(state) >= depth:          // достигнута глубина префикса
        if cache != nil:
            cache.Set(Path(state, end), weight)
            stats.cacheWrites++
        return 1

    unvisited = Invert(state)
    count = 0
    for n in NeighborMask(end) & unvisited:   // итератор AllVisited()
        newUnvisited = unvisited - {n}

        if !newUnvisited.IsEmpty() && deadend.ShouldPruneAfterVisit(n, newUnvisited):
            stats.pruned++
            continue

        count += dfs(state.Visit(n), n, depth, cache, weight, stats, rev)

    return count
```

**Типы данных:**
```go
type Path struct {
    state State  // битовая маска посещенных клеток
    start uint8  // начальная позиция (неизменяемая, uint8)
    end   uint8  // текущая позиция (uint8)
}

func New(state State, start int, end int) Path

func (p Path) State() State
func (p Path) Start() int
func (p Path) End() int
func (p Path) String() string
```

Типы для обмена между компонентами:
```go
type Result struct {
    TotalPathsFound int  // количество найденных путей в поддереве
    CacheWrites     int  // число записей в кэш (вызовов cache.Set)
    Pruned          int  // ветви, отсечённые прунером
}

func (r *Result) Add(other Result)
```

**Тесты:**
- Известные значения для 5×5 (1728), 6×6 (нужно вычислить)
- Валидация найденных путей (каждый шаг — валидный ход коня, нет повторов)

### counter/counter.go
**Ответственность:** Агрегация и подсчет всех маршрутов с учетом симметрий

**Структура:**
```go
type Counter struct {
    graph    *Graph           // граф смежности
    symmetry *Symmetry        // для канонических групп и размеров орбит
    searcher *Searcher        // для выполнения поиска
}
```

**Константы:**
- `DefaultPrecomputeDepth = 1` — глубина предварительного разбиения по умолчанию

**Методы:**

1. `NewCounter(graph *Graph) *Counter`
   - Создает симметрии и searcher для заданной доски

2. `CountFromPosition(ctx context.Context, start int) int`
   - Подсчет маршрутов из конкретной стартовой позиции
   - Используется для отладки и проверки отдельных позиций

3. `ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64`
   - Параллельный подсчет с использованием канонических групп
   - Вызов `ParallelCountWithDepth` с глубиной по умолчанию

4. `ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor, workers int, precomputeDepth int) uint64`
   - Параллельный подсчет с предварительным разбиением задач
   - Алгоритм:
     ```go
     1. generateSubTasks: для каждой канонической группы (параллельно через errgroup
        с SetLimit(workers)) вызвать searcher.GenerateSubtasks(...) и сохранить
        префиксы в кэш; добавить len(groups) задач в мониторинг
     2. Добавить taskCache.ItemsCount() задач в мониторинг
     3. Пройтись по кэшу через Each(ctx, workers, ...):
        - Вызвать CountPathsDFS для каждой записи
        - Умножить результат на count * symmetry.GetOrbitSize(p.Start())
        - Регистрировать завершение в мониторинге
     4. Суммировать все результаты через atomic.Uint64 без блокировок
     ```
   - Использует `atomic.Uint64` для суммирования без блокировок

**Использование:**
```go
g := graph.New(5)
c := counter.NewCounter(g)

count := c.ParallelCount(ctx, monitor, 8) // параллельно с 8 воркерами
fmt.Printf("Total tours: %d\n", count)
```

**Мониторинг:**
- `monitor.BeginPhase(name)` — начать новую фазу (`generation` / `gen A` / `gen B` / `counting`)
- `monitor.AddTasks(count int)` — увеличить количество задач активной фазы
- `monitor.Start(ctx)` — запустить периодический вывод прогресса (каждую секунду)
- `monitor.ReportPathsFound(count)` / `monitor.ReportCacheWrites(count)` / `monitor.ReportPruned(count)` — зарегистрировать метрики активной фазы
- `monitor.ReportTaskCompleted()` — зарегистрировать завершение одной задачи
- `monitor.Finish()` — финальный отчет по фазам

**Тесты:**
- Проверка что сумма (count × orbit_size) дает полный результат
- Сравнение sequential vs parallelCount (должны совпасть)
- Для 5×5: проверить что результат соответствует известному значению

---

### monitoring/monitor.go
**Ответственность:** Отображение прогресса поиска в реальном времени

**Интерфейс:**
```go
type Monitor interface {
    Start(ctx context.Context)
    Finish()
    BeginPhase(name string)
    AddTasks(count int)
    ReportTaskCompleted()
    ReportPathsFound(count int)
    ReportCacheWrites(count int)
    ReportPruned(count int)
}
```

**RealMonitor (реализация):**
```go
type phaseStats struct {
    name        string
    startTime   time.Time
    endTime     time.Time
    tasks       atomic.Uint64 // задач в фазе
    completed   atomic.Uint64 // завершённых задач фазы
    pathsFound  atomic.Uint64 // найденные пути (с учетом орбит)
    cacheWrites atomic.Uint64 // записей в кэш
    pruned      atomic.Uint64 // отсечённые прунером ветви
}

type RealMonitor struct {
    startTime time.Time
    started   atomic.Bool
    active    atomic.Pointer[phaseStats] // активная фаза
    phasesMu  sync.Mutex                 // append фаз
    phases    []*phaseStats
}
```

**Методы:**
- `NewMonitor() *RealMonitor` — создание монитора (фаз ещё нет)
- `BeginPhase(name string)` — новая активная фаза; вызывается только между фазами
- `AddTasks(count int)` — добавить задачи активной фазе
- `Start(ctx context.Context)` — запустить периодический вывод прогресса (каждую секунду)
  - Вывод в формате:
    ```
    [1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334 | ETA 3.953s
    ```
- `Finish()` — финальный отчет и завершение мониторинга
  - Вывод:
    ```
    === Final ===
    Total time: 63ms
    Phase generation [41ms]: tasks 6/6 | paths 0 | writes 2795 | pruned 12034
    Phase counting [22ms]: tasks 95224/95224 | paths 6637920 | writes 0 | pruned 180322
    Total paths: 6637920
    ```
- `ReportTaskCompleted()` — зарегистрировать завершение задачи активной фазы
- `ReportPathsFound(count int)` — зарегистрировать найденные пути
- `ReportCacheWrites(count int)` — зарегистрировать записи в кэш (не «закэшированные пути»)
- `ReportPruned(count int)` — зарегистрировать отсечённые прунером ветви

**FakeMonitor (для тестов):**
```go
func (*FakeMonitor) Start(ctx context.Context)        {}
func (*FakeMonitor) Finish()                          {}
func (*FakeMonitor) BeginPhase(name string)           {}
func (*FakeMonitor) AddTasks(count int)               {}
func (*FakeMonitor) ReportTaskCompleted()             {}
func (*FakeMonitor) ReportPathsFound(count int)       {}
func (*FakeMonitor) ReportCacheWrites(count int)      {}
func (*FakeMonitor) ReportPruned(count int)           {}
```

**Использование в Counter:**
```go
monitor := monitoring.NewMonitor()
monitor.Start(ctx)
defer monitor.Finish()

count := c.ParallelCountWithDepth(ctx, monitor, workers, depth)

// В worker'ах (для кэширования подзадач):
result := searcher.GenerateSubtasks(ctx, cache, p, orbitSize, depth)
monitor.ReportCacheWrites(result.CacheWrites)
monitor.ReportPruned(result.Pruned)
monitor.ReportTaskCompleted()

// При параллельном подсчете из кэша:
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    orbits := c.symmetry.GetOrbitSize(p.Start())

    total.Add(uint64(result.TotalPathsFound * count * orbits))
    monitor.ReportPathsFound(result.TotalPathsFound * count * orbits)
    monitor.ReportTaskCompleted()
})
```

## План реализации

### Этап 1: Базовая инфраструктура (неделя 1)
1. ✅ `state/state.go` — битовые маски и операции
2. ✅ `symmetry/symmetry.go` — трансформации координат
3. ✅ `graph/graph.go` — предварительный расчет соседей и масок

### Этап 2: Оптимизация поиска (неделя 2)
4. ✅ `pruner/deadend.go` — отсечение тупиковых ветвей
5. ✅ `cache/cache.go` — мемоизация с шардингом и канонизацией
6. ✅ `searcher/searcher.go` — DFS backtracking

### Этап 3: Интеграция и параллелизм (неделя 3)
7. ✅ `counter/counter.go` — агрегация с учетом симметрий
8. ✅ `main.go` — интеграция всех компонентов, CLI

### Этап 4: Тестирование и финализация (неделя 3-4)
9. ✅ Tests для каждого компонента
10. Проверка на известных значениях (5×5 = 1728)
11. Оптимизация производительности

---

## Ожидаемые результаты

| Доска | Открытых маршрутов | Время (с оптимизацией) |
|-------|---------------------|------------------------|
| 5×5   | 1728                | < 1 сек                |
| 6×6   | ~122000             | 1-10 сек               |
| 7×7   | ~8.5×10⁹            | 10-300 сек             |
| 8×8   | ~1.9×10⁻²⁴          | 300+ сек (или больше) |

*Примечание: точные значения для больших досок требуют проверки и могут быть неизвестны в литературе*

---

## Ресурсы

- **Warnsdorff, H. C. (1823)** — первое описание эвристики
- **Schwenk, A. J. (1991)** — "Which Rectangular Chessboards Have a Knight's Tour?"
- [Knight's Tour Wikipedia](https://en.wikipedia.org/wiki/Knight%27s_tour)
- [GeeksforGeeks: Knight Tour](https://www.geeksforgeeks.org/knight-tour-problem/)
