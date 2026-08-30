# Компонент Pruner: Отсечение тупиковых ветвей

## Назначение

Dead-end pruning — отсечение ветвей дерева поиска, которые заведомо не приведут к решению. Основан на проверке изолированных непосещенных клеток.

## DeadEndPruner

```go
type DeadEndPruner struct {
    graph *graph.Graph  // граф смежности (хранится ссылка, без копирования)
}
```

### Методы

#### NewDeadEndPruner(graph *graph.Graph) *DeadEndPruner

Создает новый pruner: сохраняет ссылку на граф (O(1), без предвычисления масок).

#### ShouldPruneAfterVisit(last int, unvisited state.State) bool

Горячий метод для DFS-прунинга за O(deg(last)) вместо O(N²). Вызывается сразу после
посещения клетки `last` родителем, который сам прошёл проверку (инвариант: среди
непосещённых нет изолированных, кроме случая одной оставшейся клетки).

**Инвариант/корректность:** посещение `last` может изолировать только непосещённых
соседей самой клетки `last`, поэтому полный скан всех клеток не требуется.

**Алгоритм:**
1. Если непосещённых нет → не отсекать
2. Если осталась одна клетка: отсечь, если она не соседняя к `last` (текущему концу пути)
3. Иначе: для каждого непосещённого соседа `u` клетки `last` проверить
   `neighborMask(u) & unvisited == 0`; если нашёлся → отсечь

Маски соседей прунер не копирует — каждый вызов `GetNeighborMask` читает предвычисленную
маску графа за O(1).

## Использование в Searcher

`Searcher` хранит прунер в поле `deadend *pruner.DeadEndPruner` (создаётся один раз в
`NewSearcher`). В горячем DFS (`dfs`) проверка выполняется сразу после попытки посещения
клетки `n`, до рекурсии — если остались непосещённые клетки и ветвь тупиковая, она
отсекается:

```go
func (s *Searcher) dfs(ctx context.Context, st state.State, start, end, depth int, c *cache.Cache, cached *int) int {
    if ctx.Err() != nil {
        return 0
    }

    if st.CountBits() >= depth { // достигнута глубина префикса — пишем в кэш
        if c != nil {
            c.Set(path.New(st, start, end), 1)
            *cached++
        }
        return 1
    }

    unvisited := st.Invert(s.graph.GetTotalCells())
    cand := s.graph.GetNeighborMask(end).Intersect(unvisited)

    found := 0
    for n := range cand.AllVisited() {
        newUnvisited := unvisited.Unvisit(n)
        if !newUnvisited.IsEmpty() && s.deadend.ShouldPruneAfterVisit(n, newUnvisited) {
            continue // пропускаем отсеченную ветвь
        }
        found += s.dfs(ctx, st.Visit(n), start, n, depth, c, cached)
    }
    return found
}
```

Проверка `!newUnvisited.IsEmpty()` вынесена в условие: если после посещения `n`
непосещённых не осталось — это завершённый маршрут, а не тупик.

## Примеры

### Изолированная клетка

```go
// Доска 5×5, только что посетили клетку 7
graph := graph.New(5)
deadend := pruner.NewDeadEndPruner(graph)

// Непосещённые: 0, 2, 4, 6. Клетка 0 изолирована и является соседом 7.
unvisited := state.NewState(0, 2, 4, 6)
shouldPrune := deadend.ShouldPruneAfterVisit(7, unvisited) // true
```

### Одна оставшаяся клетка

```go
// Доска 5×5, осталась только клетка 12 (центр), последняя позиция 24
unvisited := state.Bit(12)
deadend.ShouldPruneAfterVisit(24, unvisited) // true — 12 не соседняя к 24
```

## Тесты (`pruner/deadend_test.go`)

Отдельные тесты на каждый случай:

- `TestShouldPruneAfterVisit_NoUnvisited` — нет непосещённых клеток → не отсекать
- `TestShouldPruneAfterVisit_SingleUnvisitedReachable` / `..._Unreachable` — одна оставшаяся клетка (соседняя / недоступная)
- `TestShouldPruneAfterVisit_IsolatedVertexCorner` / `..._Center` / `..._TwoIsolatedVertices` — изолированные клетки
- `TestShouldPruneAfterVisit_ValidPathNotPruned` — валидные продолжения не отсекаются

Эквивалентность локальной проверки полному скану всех непосещённых клеток
(`fullScanShouldPrune`) проверяется вероятностным тестом
`TestShouldPruneAfterVisit_MatchesFullScan` (2000 случайных путей, seed 42).

## Эффективность

- **Поздние этапы поиска**: высока (когда осталось мало клеток)
- **Ранние этапы поиска**: низкая (мало изолированных клеток)

## Заключение

DeadEndPruner — простой и эффективный метод отсечения, который проверяет доступность всех непосещенных клеток. Ключевое преимущество — быстрая реализация с использованием битовых масок.
