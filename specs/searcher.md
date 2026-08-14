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

### 2. CountPathsDFS(ctx context.Context, p path.Path) types.Result

```go
func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) types.Result
// Рекурсивный DFS с проверкой Dead-end pruner и возвратом результата
```

**Алгоритм:**
1. Если достигнута целевая клетка (все посещено), вернуть результат {TotalPathsFound: 1}
2. Для каждого непосещенного соседа:
   - Проверить Dead-end pruner на новом состоянии
   - Если не отсечен, рекурсивно вызвать DFS
   - Суммировать результаты и отсечения

### 3. countPathsDFS(ctx context.Context, p path.Path, stopCondition func(path.Path) bool) types.Result

```go
func (s *Searcher) countPathsDFS(ctx context.Context, p path.Path, stopCondition func(path.Path) bool) types.Result
// Внутренняя рекурсивная функция с пользовательским условием остановки
```

**Параметры:**
- `stopCondition`: функция, возвращающая true когда нужно прекратить спуск и вернуть результат

### 4. GenerateSubtasks(ctx context.Context, p path.Path, depth int) []path.Path

```go
func (s *Searcher) GenerateSubtasks(ctx context.Context, p path.Path, depth int) []path.Path
// Генерирует все пути глубины depth из заданного пути
```

**Алгоритм:**
- Рекурсивно строит дерево поиска
- Когда `depth == 0` или достигнута заданная глубина (по CountBits), путь добавляется в результат

### 5. GenerateSubtasksWithMetadata(ctx context.Context, start int, orbitSize int, depth int) []types.Subtask

```go
func (s *Searcher) GenerateSubtasksWithMetadata(
    ctx context.Context,
    start int,
    orbitSize int,
    depth int,
) []types.Subtask
// Генерирует подзадачи с учетом симметрий
```

**Алгоритм:**
1. Создает начальный путь из стартовой клетки
2. Генерирует все пути глубины depth через GenerateSubtasks
3. Для каждого пути находит каноническую форму через symmetry.CanonicalizePath
4. Подсчитывает количество дубликатов каждой канонической формы
5. Создает Subtask с `SymmetriesCount = orbitSize * count`

### 6. CountCenterPaths(ctx context.Context, cache *Cache, p path.Path, SymmetriesCount int) types.Result

```go
func (s *Searcher) CountCenterPaths(
    ctx context.Context,
    cache *Cache,
    p path.Path,
    SymmetriesCount int,
) types.Result
// Специализированный поиск с остановкой в центре доски и кэшированием результата
```

**Алгоритм:**
- Продолжает поиск пока не достигнет центра (totalCells/2)
- При достижении центра сохраняет результат в кэш с указанным SymmetriesCount

## Типы данных

### Path

```go
type Path struct {
    state State  // битовая маска посещенных клеток
    start int    // начальная позиция (неизменяемая)
    end   int    // текущая позиция
}

func New(state State, start int, end int) Path

func (p Path) State() State
func (p Path) Start() int
func (p Path) End() int
```

### Result

```go
type Result struct {
    TotalPathsFound int  // количество найденных путей в поддереве
    Pruned          int  // количество отсечений (Dead-end pruner)
}

func (r *Result) Add(other Result)
// Суммирует поля result и other
```

### Subtask

```go
type Subtask struct {
    State           state.State  // битовая маска состояния
    Start           int          // начальная позиция
    End             int          // конечная позиция
    Depth           int          // глубина предварительного разбиения (0 по умолчанию)
    SymmetriesCount int          // количество симметричных вариантов
}
```

## Использование в Counter

```go
func (c *Counter) ParallelCountWithDepth(
    ctx context.Context,
    monitor monitoring.Monitor,
    workers int,
    precomputeDepth int,
) uint64 {
    groups := c.symmetry.GetCanonicalGroups()
    
    var allSubtasks []types.Subtask
    for _, group := range groups {
        subtasks := c.searcher.GenerateSubtasksWithMetadata(
            ctx, 
            group.Canonical, 
            group.OrbitSize, 
            precomputeDepth,
        )
        allSubtasks = append(allSubtasks, subtasks...)
    }
    
    monitor.AddTasks(allSubtasks...)
    
    total := atomic.Uint64{}
    g, _ := errgroup.WithContext(ctx)
    g.SetLimit(workers)
    
    for _, task := range allSubtasks {
        g.Go(func() error {
            p := path.New(task.State, task.Start, task.End)
            result := c.searcher.CountPathsDFS(ctx, p)
            
            total.Add(uint64(result.TotalPathsFound * task.SymmetriesCount))
            monitor.RecordTaskCompletion(task, result)
            return nil
        })
    }
    
    _ = g.Wait()
    return total.Load()
}
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
    
    // Если осталась одна клетка, проверяем достижимость
    if unvisitedMask.CountBits() == 1 {
        lastPos := int(unvisitedMask.TrailingZeroBits())
        currentMask := state.NewState().Visit(path.End())
        if p.graph.GetNeighborMask(lastPos).Intersect(currentMask).IsEmpty() {
            return true  // нельзя дойти до последней клетки
        }
        return false
    }
    
    // Проверяем изолированные клетки (нет непосещенных соседей)
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
    
    p := path.New(state.State(0).Visit(0), 0, 0)
    subtasks := searcher.GenerateSubtasks(context.Background(), p, 3)
    
    require.NotEmpty(t, subtasks)
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
