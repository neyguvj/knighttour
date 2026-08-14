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
    size       int
    transforms []Transform
    canonical  []int           // каноническая позиция для каждой клетки
    orbitSize  []int           // размер орбиты для каждой клетки
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
    graph *Graph
}
```

**Методы:**
- `NewDeadEndPruner(graph *Graph) *DeadEndPruner` — создание прунера
- `ShouldPrune(path path.Path) bool` — проверить, нужно ли отсечь путь
  - Проверяет изолированные непосещенные клетки (без непосещенных соседей)
  - Эффективен на поздних этапах поиска

**Тесты:**
- Table tests для различных состояний
- Проверка что pruner НЕ отсекает валидные продолжения

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
- `Each(f func(p path.Path, count int))` — итерация по всем записям

**Хэширование:**
- FNV-1a хэш от битового состояния пути
- Индекс шарда = hash % 64

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
   - Возвращает результат со счетчиками путей и отсечений

2. `CountPathsDFS(ctx context.Context, p path.Path) types.Result`
   - Рекурсивный DFS с вызовом DeadEndPruner
   - Использует кэш для ускорения (вызовы Get/Set в Counter)
   - Возвращает types.Result с TotalPathsFound и Pruned

3. `GenerateSubtasks(ctx context.Context, p path.Path, depth int) []path.Path`
   - Генерирует все пути глубины depth из заданного пути
   - Используется для предварительного разбиения задач

4. `GenerateSubtasksWithMetadata(ctx context.Context, start int, orbitSize int, depth int) []types.Subtask`
   - Генерирует подзадачи с учетом симметрий
   - Канонизирует каждый путь и подсчитывает количество дубликатов
   - Устанавливает `SymmetriesCount = orbitSize * countOfCanonicalForms`

5. `countPathsDFS(ctx context.Context, p path.Path, stopCondition func(path.Path) bool) types.Result`
   - Внутренняя рекурсивная функция поиска

6. `CountCenterPaths(ctx context.Context, cache *Cache, p path.Path, SymmetriesCount int) types.Result`
   - Специализированный поиск с остановкой в центре доски
   - Используется для кэширования промежуточных результатов

**Алгоритм:**
```
DFS(path):
    if stopCondition(path): return Result{TotalPathsFound: 1}
    
    count = 0, pruned = 0
    for each neighbor in graph.GetNeighbors(path.End()):
        if neighbor посещен: continue
        
        newState = path.State().Visit(neighbor)
        newPos = Path{state: newState, start: path.Start(), end: neighbor}
        
        if deadend.ShouldPrune(newPos):
            pruned++
            continue
        
        childResult = DFS(newPos)
        count += childResult.TotalPathsFound
        pruned += childResult.Pruned
    
    return Result{TotalPathsFound: count, Pruned: pruned}
```

**Типы данных:**
```go
type Path struct {
    state State  // битовая маска посещенных клеток
    start int    // начальная позиция (неизменяемая)
    end   int    // текущая позиция
}

type Result struct {
    TotalPathsFound int
    Pruned          int
}
```

**Тесты:**
- Известные значения для 5×5 (1728), 6×6 (нужно вычислить)
- Валидация найденных путей (каждый шаг — валидный ход коня, нет повторов)

### counter/counter.go
**Ответственность:** Агрегация и подсчет всех маршрутов с учетом симметрий

**Структура:**
```go
type Counter struct {
    cache    *Cache           // для мемоизации результатов
    graph    *Graph           // граф смежности
    symmetry *Symmetry        // для канонических групп и размеров орбит
    searcher *Searcher        // для выполнения поиска
}
```

**Константы:**
- `DefaultPrecomputeDepth = 0` — глубина предварительного разбиения (по умолчанию без разбиения)

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
     1. Получить группы канонических позиций: symmetry.GetCanonicalGroups()
     2. Для каждой группы сгенерировать подзадачи через GenerateSubtasksWithMetadata()
     3. Запустить worker pool с лимитом workers
     4. Для каждой подзадачи:
        - Создать path из task.State, task.Start, task.End
        - Вызвать CountPathsDFS
        - Умножить результат на SymmetriesCount
        - Регистрировать завершение в мониторинге
     5. Суммировать все результаты с учетом орбит
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
- `monitor.AddTasks(tasks...)` — добавить задачи для отслеживания
- `monitor.Start(ctx)` — запустить периодический вывод прогресса (каждую секунду)
- `monitor.RecordTaskCompletion(task, result)` — зарегистрировать завершение одной задачи
- `monitor.Finish()` — финальный отчет

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
    AddTasks(tasks ...types.Subtask)
    Start(ctx context.Context)
    Finish()
    RecordTaskCompletion(task types.Subtask, result types.Result)
}
```

**RealMonitor (реализация):**
```go
type RealMonitor struct {
    startTime          time.Time
    totalTasks         atomic.Uint64  // общее количество задач
    completed          atomic.Uint64  // завершенные задачи
    totalPaths         atomic.Uint64  // найденные пути (с учетом орбит)
    connectivityPruned atomic.Uint64  // отсечено dead-ends
}
```

**Методы:**
- `NewMonitor() *RealMonitor` — создание монитора
- `AddTasks(tasks ...types.Subtask)` — добавить задачи (увеличивает totalTasks)
- `Start(ctx context.Context)` — запустить периодический вывод прогресса (каждую секунду)
  - Вывод в формате:
    ```
    [00:XX:YY] Tasks: 6/6 (100.0%) | Total paths 1728 | Pruned: 116606
    ```
- `Finish()` — финальный отчет и завершение мониторинга
  - Вывод:
    ```
    === Final ===
    Time: XXs
    Tasks completed: 6/6
    Total paths: 1728
    Connectivity pruned: 116606
    ```
- `RecordTaskCompletion(task types.Subtask, result types.Result)` — зарегистрировать завершение задачи
  - Увеличивает completed на 1
  - Умножает TotalPathsFound на SymmetriesCount и добавляет к totalPaths

**FakeMonitor (для тестов):**
- Пустая реализация всех методов интерфейса

**Использование в Counter:**
```go
monitor := monitoring.NewMonitor()
monitor.Start(ctx)
defer monitor.Finish()

count := c.ParallelCountWithDepth(ctx, monitor, workers, depth)

// В worker'ах:
result := searcher.CountPathsDFS(ctx, p)
total.Add(uint64(result.TotalPathsFound * task.SymmetriesCount))
monitor.RecordTaskCompletion(task, result)
```

---

## План реализации

### Этап 1: Базовая инфраструктура (неделя 1)
1. ✅ `state/state.go` — битовые маски и операции
2. ✅ `symmetry/symmetry.go` — трансформации координат
3. 🔄 `graph/graph.go` — добавить Index, Coords, Degree, сортировку
### types/types.go
**Ответственность:** Типы данных для обмена между компонентами

```go
type Result struct {
    TotalPathsFound int  // найденные пути в поддереве
    Pruned          int  // количество отсечений
}

func (r *Result) Add(other Result)

type Subtask struct {
    State           state.State  // битовая маска состояния
    Start           int          // начальная позиция
    End             int          // конечная позиция
    Depth           int          // глубина предварительного разбиения
    SymmetriesCount int          // количество симметричных вариантов
}
```

---

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
