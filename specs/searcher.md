# Компонент Searcher: Основной алгоритм поиска

## Назначение

Реализация DFS backtracking алгоритма для поиска всех открытых маршрутов коня с использованием Dead-end pruning.

## Структура данных

```go
type Searcher struct {
    graph   *Graph           // граф смежности
    sym     *Symmetry        // для канонизации путей в кэше
    deadend *DeadEndPruner   // отсечение тупиковых ветвей
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
1. Создает начальный путь из стартовой клетки
2. Запускает DFS с пользовательской функцией остановки:
   - Если `SholdSkip(start)` — пропустить эту позицию
   - Если глубина достигнута (`depth == 0` или CountBits >= depth) — сохранить в кэш и вернуться
3. Возвращает `types.Result.CachedPaths` — количество закэшированных путей

### 3. CountPathsDFS(ctx context.Context, p path.Path) types.Result

```go
func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) types.Result
// Рекурсивный DFS с проверкой Dead-end pruner и возвратом результата
```

**Алгоритм:**
1. Если достигнута целевая клетка (все посещено), увеличить `TotalPathsFound`
2. Для каждого непосещенного соседа:
   - Проверить Dead-end pruner на новом состоянии
   - Если не отсечен, рекурсивно вызвать DFS
3. Возвращает результат с найденными путями

### 4. countPathsDFS(ctx context.Context, p path.Path, onResult func(path.Path) (stop bool))

```go
func (s *Searcher) countPathsDFS(
    ctx context.Context,
    p path.Path,
    onResult func(path.Path) (stop bool),
)
// Внутренняя рекурсивная функция с пользовательской обратной связью
```

**Параметры:**
- `onResult`: функция, возвращающая true когда нужно прекратить спуск и вернуться

## Типы данных

### Path

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
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    group := c.symmetry.GetCanonicalGroupByPosition(p.Start())
    
    total.Add(uint64(result.TotalPathsFound * count * group.OrbitSize))
})
```

## Dead-end Pruning

Dead-end pruning проверяет изолированные непосещенные клетки:

```go
type DeadEndPruner struct {
    graph *Graph
}

func (p *DeadEndPruner) ShouldPrune(path path.Path) bool {
    totalCells := p.graph.GetTotalCells()
    s := path.State()
    unvisitedMask := s.UnvisitedMask(totalCells)
    
    if unvisitedMask.IsEmpty() {
        return false  // все посещено
    }
    
    if unvisitedMask.CountBits() == 1 {
        lastPos := int(unvisitedMask.TrailingZeroBits())
        currentMask := state.NewState().Visit(path.End())
        if p.graph.GetNeighborMask(lastPos).Intersect(currentMask).IsEmpty() {
            return true  // нельзя дойти до последней клетки
        }
        return false
    }
    
    for i := 0; i < totalCells; i++ {
        if s.IsUnvisited(i) {
            neighborMask := p.graph.GetNeighborMask(i)
            if neighborMask.Intersect(unvisitedMask).IsEmpty() {
                return true  // изолированная клетка
            }
        }
    }
    
    return false
}
```

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
    
    // Путь где одна клетка изолирована
    s := state.NewState(0, 1, 2)
    p := path.New(s, 0, 2)
    
    shouldPrune := deadend.ShouldPrune(p)
    require.False(t, shouldPrune)  // не должно быть prune пока есть пути
}
```

## Заключение

Searcher — центральный компонент, объединяющий DFS backtracking с Dead-end pruning. Канонизация путей через Symmetry позволяет эффективно использовать Cache для мемоизации результатов.
