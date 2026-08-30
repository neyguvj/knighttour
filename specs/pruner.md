# Компонент Pruner: Отсечение тупиковых ветвей

## Назначение

Отсечение ветвей дерева поиска, которые заведомо не приведут к решению. Два уровня:

- `DeadEndPruner` — локальная проверка изолированных клеток за O(deg(last));
- `AdvancedPruner` — надстройка: глобальная связность оставшихся клеток + анализ
  вершин степени ≤1 (эвристика концов гамильтова пути). Включён всегда.

## DeadEndPruner

Локальное отсечение: проверяет, не появились ли изолированные непосещённые клетки
после последнего хода.

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

## AdvancedPruner

```go
type AdvancedPruner struct {
    deadend *DeadEndPruner
    graph   *graph.Graph
}

func NewAdvancedPruner(g *graph.Graph) *AdvancedPruner

func (p *AdvancedPruner) ShouldPruneAfterVisit(last int, unvisited state.State) bool
```

Композиция проверок (все — на битовых масках, включены всегда):

1. **Dead-end** — делег в `DeadEndPruner` (дешёвая проверка сначала).
2. **Глобальные проверки** (`globalCheck`, только при `|unvisited| > 1`):
   - **нет продолжения** — у `last` нет непосещённых соседей → prune;
   - **связность `G[unvisited]`** — оставшийся путь живёт только в непосещённых
     клетках, поэтому они обязаны образовывать одну связную компоненту.
     Проверка — bitset flood-fill от младшего бита: расширение фронта ИЛИ-ем
     предвычисленных масок соседей; если покрыты не все → prune. По ходу флуда
     считается степень каждой клетки в `G[unvisited]` (popcount по unvisited);
   - **эвристика концов** — гамильтов путь на `G[unvisited]` имеет ровно 2 конца,
     причём стартовый обязан быть соседом `last`. Вершины степени 1 обязаны стать
     концами, поэтому: ≥3 вершин степени 1 → prune; ровно 2 и ни одна не соседна
     `last` → prune.

Замеры на M4 Max (`-benchtime=3x`, время полного подсчёта):

| доска | только dead-end | advanced (порог K=20) | advanced (всегда) |
|-------|-----------------|----------------------|-------------------|
| 5×5   | 3.20 мс         | 0.69 мс              | ~0.7 мс           |
| 6×6   | 16.7 с          | 2.09 с               | ~1.0 с            |

Порог по остатку (K=20) ускорял 5×5 на ~8%, но на 6×6 проигрывал ~2×; выбран
вариант «всегда» — он же соответствует решению не иметь переключателей.

Корректность: ни одна проверка не отсекает ветвь, содержащую полный маршрут —
связность обязательна (путь не может покинуть компоненту без возврата в
посещённые), эвристика концов — необходимое условие существования гамова пути.

## Использование в Searcher

`Searcher` хранит прунер в поле `pruner *pruner.AdvancedPruner` (создаётся один раз в
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
        if !newUnvisited.IsEmpty() && s.pruner.ShouldPruneAfterVisit(n, newUnvisited) {
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

### Тесты AdvancedPruner (`pruner/advanced_test.go`)

- `TestAdvancedConnectivity` — табличные кейсы: несвязный unvisited → prune,
  связный → нет; одиночная клетка-остров в другой компоненте.
- `TestAdvancedDegreeHeuristic` — 3 вершины степени 1 → prune; 2 и обе не соседни
  `last` → prune; 2 и одна соседняя → не prune.
- `TestAdvanced_SingleCellSkipsGlobalCheck` — при `|unvisited| <= 1` глобальная
  проверка не выполняется (одинокая соседняя клетка — завершающий ход).
- `TestAdvancedMatchesNaive` — вероятностный equivalence: наивная реализация
  (BFS по спискам соседей + явный подсчёт степеней) против битсет-версии,
  2000 случайных состояний, seed 42.

## Эффективность

- **Поздние этапы поиска**: высока (когда осталось мало клеток)
- **Ранние этапы поиска**: низкая (мало изолированных клеток)

## Заключение

DeadEndPruner — простой и эффективный метод отсечения, который проверяет доступность всех непосещенных клеток. Ключевое преимущество — быстрая реализация с использованием битовых масок.
