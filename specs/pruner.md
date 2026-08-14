# Компонент Pruner: Отсечение тупиковых ветвей

## Назначение

Dead-end pruning — отсечение ветвей дерева поиска, которые заведомо не приведут к решению. Основан на проверке изолированных непосещенных клеток.

## DeadEndPruner

```go
type DeadEndPruner struct {
    graph *Graph  // граф смежности для получения соседей
}
```

### Методы

#### NewDeadEndPruner(graph *Graph) *DeadEndPruner

Создает новый pruner с заданным графом.

#### ShouldPrune(path path.Path) bool

Проверяет, нужно ли отсечь путь. Возвращает `true` если:
- Есть изолированная непосещенная клетка (без непосещенных соседей)
- Осталась одна клетка и её нельзя достичь из текущей позиции

**Алгоритм:**
1. Если все клетки посещены → не отсекать
2. Если осталась одна клетка:
   - Проверить достижимость через neighborMasks
   - Если текущая позиция и последняя клетка не соединены → отсечь
3. Для каждой непосещенной клетки проверить, есть ли у нее непосещенные соседи
4. Если найдена изолированная клетка → отсечь

## Использование в Searcher

```go
type Searcher struct {
    graph   *Graph
    deadend *DeadEndPruner
}

func (s *Searcher) countPathsDFS(ctx context.Context, p path.Path, stopCondition func(path.Path) bool) types.Result {
    if stopCondition(p) {
        return types.Result{TotalPathsFound: 1}
    }
    
    var result types.Result
    
    for _, neighbor := range s.graph.GetNeighbors(p.End()) {
        if p.State().IsVisited(neighbor) {
            continue
        }
        
        newState := p.State().Visit(neighbor)
        newPos := path.New(newState, p.Start(), neighbor)
        
        if s.deadend.ShouldPrune(newPos) {
            result.Pruned++
            continue
        }
        
        childResult := s.countPathsDFS(ctx, newPos, stopCondition)
        result.Add(childResult)
    }
    
    return result
}
```

## Примеры

### Изолированная клетка

```go
// Доска 5×5
graph := graph.New(5)

// Посещены: 0, 1, 2. Остались непосещенные.
s := state.NewState(0, 1, 2)
p := path.New(s, 0, 2)

deadend := pruner.NewDeadEndPruner(graph)
shouldPrune := deadend.ShouldPrune(p)  // true если есть изолированные клетки
```

### Одна оставшаяся клетка

```go
// Доска 5×5, почти полная
s := state.State((1 << 25) - 2)  // все кроме клетки 0
p := path.New(s, 6, 6)           // в позиции 6, осталось посетить только 0

deadend.ShouldPrune(p)  // true если нет пути из 6 в 0 через непосещенные
```

## Тесты

```go
func TestDeadEndPruner_EmptyState(t *testing.T) {
    graph := graph.New(5)
    pruner := pruner.NewDeadEndPruner(graph)
    
    s := state.State(0)  // пустое состояние
    p := path.New(s, 0, 0)
    
    require.False(t, pruner.ShouldPrune(p))
}

func TestDeadEndPruner_FullState(t *testing.T) {
    graph := graph.New(5)
    pruner := pruner.NewDeadEndPruner(graph)
    
    s := state.State((1 << 25) - 1)  // все клетки посещены
    p := path.New(s, 0, 0)
    
    require.False(t, pruner.ShouldPrune(p))
}

func TestDeadEndPruner_IsolatedCell(t *testing.T) {
    graph := graph.New(5)
    pruner := pruner.NewDeadEndPruner(graph)
    
    // Посещены: все кроме клетки 3 и её соседей. Клетка 3 изолирована.
    s := state.State((1 << 25) - (1 << 3))  // все посещены кроме 3
    p := path.New(s, 0, 0)
    
    shouldPrune := pruner.ShouldPrune(p)  // true если 3 изолирована
    
    require.True(t, shouldPrune)
}

func TestDeadEndPruner_LastCellUnreachable(t *testing.T) {
    graph := graph.New(5)
    pruner := pruner.NewDeadEndPruner(graph)
    
    // Осталась одна клетка 10, но она не соединена с текущей позицией
    s := state.State((1 << 25) - (1 << 10))
    p := path.New(s, 0, 0)  // текущая позиция 0
    
    shouldPrune := pruner.ShouldPrune(p)
    
    require.True(t, shouldPrune)
}
```

## Эффективность

- **Поздние этапы поиска**: высока (когда осталось мало клеток)
- **Ранние этапы поиска**: низкая (мало изолированных клеток)

## Заключение

DeadEndPruner — простой и эффективный метод отсечения, который проверяет доступность всех непосещенных клеток. Ключевое преимущество — быстрая реализация с использованием битовых масок.
