# Компонент Searcher: Основной алгоритм поиска

## Назначение

Реализация DFS backtracking алгоритма для поиска всех открытых маршрутов коня с
использованием Advanced pruning (dead-end + связность + эвристика концов).

## Структура данных

```go
type Searcher struct {
    graph  *Graph           // граф смежности
    sym    *Symmetry        // для канонизации путей в кэше
    pruner *AdvancedPruner  // отсечение тупиков + связность + эвристика концов
}
```

## Инициализация

```go
func NewSearcher(graph *Graph, sym *Symmetry) *Searcher
// Создает searcher с заданным графом и симметриями
```

## Основные методы

### 1. CountPaths(ctx context.Context, start int) types.Result

```go
func (s *Searcher) CountPaths(ctx context.Context, start int) types.Result
// Создает начальный путь с посещенной стартовой клеткой и вызывает CountPathsDFS
```

**Пример:**
```go
result := searcher.CountPaths(context.Background(), 0)
fmt.Printf("Found %d paths from position 0\n", result.TotalPathsFound)
```

### 2. GenerateSubtasks(ctx context.Context, cache *Cache, start int, orbitSize int, depth int) types.Result

```go
func (s *Searcher) GenerateSubtasks(
    ctx context.Context,
    cache *Cache,
    start int,
    orbitSize int,
    depth int,
) types.Result
// Генерирует подзадачи и сохраняет их в кэш для последующего использования
```

**Алгоритм:**
1. Если `SholdSkip(start)` — вернуть пустой результат (проверка вынесена наверх, т.к. start неизменен)
2. DFS по префиксам (`dfs`): как только `CountBits(state) >= depth`,
    сохранить задачу в кэш с весом орбиты (`cache.Set(path.New(st, end), orbitSize)`)
    и прекратить спуск; ветви режет `ShouldPruneAfterVisit` (тот же инвариант, что и в основном DFS).
    `start` в ключ не попадает — вместо него вклад группы кодируется весом `orbitSize`
3. Возвращает `types.Result.CachedPaths` — количество записей, отправленных в кэш

### 3. CountPathsDFS(ctx context.Context, p path.Path) types.Result

```go
func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) types.Result
// Считает полные маршруты из состояния p; если состояние уже полное — возвращает 1
```

**Единый горячий DFS (`dfs`) — по маскам, без колбэков:**

Полный подсчёт и генерация префиксов выполняются одним рекурсивным методом; различие
сводится к базовому случаю:

```go
func (s *Searcher) dfs(ctx context.Context, st State, end, depth int, c *Cache, weight int, cached *int) int
```

1. Базовый случай: `CountBits(st) >= depth` → если `c != nil`, сохранить
   `path.New(st, end)` в кэш с весом `weight` и инкрементировать `*cached`; вернуть 1
2. `unvisited := fullMask &^ state`; кандидаты: `neighborMasks[end] & unvisited`
3. Перебор кандидатов через итератор `for n := range cand.AllVisited()` (без прямых битовых операций)
4. Если после хода остались непосещённые и `pruner.ShouldPruneAfterVisit(n, newUnvisited)` → continue
5. Рекурсия, сумма результатов — возвращаемое значение

Вызовы:
- полный подсчёт (`CountPathsDFS`): `depth = totalCells`, `c = nil` — лист (полная
  доска) даёт 1 через базовый случай;
- префиксы (`GenerateSubtasks`): `depth = precomputeDepth`, `c != nil` — возврат
  из базового случая игнорируется, спуск ниже `depth` не происходит.

Проверка отмены контекста — на входе в каждый рекурсивный вызов (`ctx.Err() != nil`).

**Без аллокаций в горячем цикле:** `unvisited` строится через `st.Invert(totalCells)`, кандидаты —
через `graph.GetNeighborMask(end).Intersect(unvisited)`; перебор — итератором `AllVisited()`.
Ни колбэков, ни сканов доски внутри рекурсии нет. Цена унификации — два лишних
параметра (`depth`, `weight`) и nil-check кэша на узел.

## Экспериментальные прунинги (результат замеров на 5×5/6×6, НЕ реализованы)

### Цветовой (бипартитный) prune — бесполезен

Идея: граф коня двудольный, проверять баланс цветов оставшихся клеток. Замер
(отладочная версия со счётчиками): **0 срабатываний**. Инвариант выполняется
автоматически в любом DFS-узле, доходимом легальными ходами (путь чередует цвета),
а для нечётных досок стартовые клетки нужного чёта уже отсекает `SholdSkip`.

### Warnsdorff-порядок кандидатов — не реализован

Идея: перебор кандидатов по возрастанию свободных выходов. Замер: **число узлов дерева
не меняется вообще** (dead-end prune локален и не зависит от порядка), а время растёт
на ~10–20% из-за сортировки на каждом узле. Реализация удалена как бесполезная.


## Типы данных

### Path (задача поиска)

```go
type Path struct {
    state State  // битовая маска посещенных клеток
    end   uint8  // текущая позиция (uint8)
}

func New(state State, end int) Path

func (p Path) State() State
func (p Path) End() int
func (p Path) String() string
```

Поле `start` удалено: число продолжений из состояния зависит только от
`(state, end)`, а вклад стартовой орбиты перенесён в вес записи кэша.

### Result

```go
type Result struct {
    TotalPathsFound int  // количество найденных путей в поддереве
    CachedPaths     int  // количество закэшированных путей (в GenerateSubtasks)
}

func (r *Result) Add(other Result)
// Суммирует поля result и other
```

## Использование в Counter

```go
// Генерация подзадач через кэш:
cache := cache.NewCache(c.symmetry)
result := c.searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)

// Параллельный подсчет из кэша:
total := atomic.Uint64{}
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    // weight уже содержит Σ count·orbitSize по всем внесшим вклад группам
    total.Add(uint64(result.TotalPathsFound) * uint64(weight))
})
```

## Прунинг

В горячем DFS используется `pruner.ShouldPruneAfterVisit(last, unvisited)`: сначала
дешёвый локальный dead-end за O(deg(last)) (изолированной после посещения `last` могла
стать только клетка из её непосещённых соседей), затем — глобальные проверки на битовых
масках: связность `G[unvisited]`, обязательный сосед у `last` и эвристика концов
(вершины степени ≤1). Подробности — в `pruner.md`.

## Тесты

```go
func TestSearcherCountPaths(t *testing.T) {
    graph := graph.New(5)
    sym := symmetry.NewSymmetry(5)
    searcher := searcher.NewSearcher(graph, sym)
    
    result := searcher.CountPaths(context.Background(), 0)
    
    require.Equal(t, 1728/4, result.TotalPathsFound) // угол → орбита размера 4
}

func TestSearcherGenerateSubtasks(t *testing.T) {
    graph := graph.New(5)
    sym := symmetry.NewSymmetry(5)
    searcher := searcher.NewSearcher(graph, sym)
    
    cache := cache.NewCache(sym)
    result := searcher.GenerateSubtasks(context.Background(), cache, 0, 4, 3)
    
    require.Greater(t, result.CachedPaths, 0)
}

func TestSearcherDeadEndPrune(t *testing.T) {
    graph := graph.New(5)
    deadend := pruner.NewDeadEndPruner(graph)
    
    // После посещения 2 клетка 0 изолирована среди непосещённых
    unvisited := state.NewState(0, 2, 4, 6)
    
    shouldPrune := deadend.ShouldPruneAfterVisit(7, unvisited)
    require.True(t, shouldPrune)  // есть изолированная клетка
}
```

## Заключение

Searcher — центральный компонент, объединяющий DFS backtracking с Dead-end pruning. Канонизация путей через Symmetry позволяет эффективно использовать Cache для мемоизации результатов.
