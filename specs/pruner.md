# pruner: отсечение тупиковых ветвей

## Ответственность

Стейт-лесс компонент необходимых условий: отсекает ветви DFS, заведомо не ведущие к полному
туру. Один тип `Pruner` (ADR-006). Не хранит статистику — возвращает причину, учёт ведёт
вызывающий через `types.Result`.

## Ярусы проверок

- **L0 — локальный dead-end**: изолированные клетки за O(deg(last)). Дешёвый первый блок.
- **L1 — глобальные** (`globalCheck`, всегда при `|unvisited| > 1`): нет продолжения у
  `last`; связность `G[unvisited]` (bitset flood-fill); эвристика концов гамильтова пути
  (degree-1 вершины обязаны стать концами).

## Публичный API

```go
type Reason uint8
const (NoReason; DeadEnd; NoContinuation; Disconnected; Endpoints Reason = iota...)

func New(g *graph.Graph) *Pruner

// Горячий метод DFS: сразу после посещения last. Возвращает флаг + первую сработавшую
// причину (при false — NoReason). Инвариант: посещение last изолирует только соседей last.
func (p *Pruner) ShouldPruneAfterVisit(last int, unvisited state.State) (bool, Reason)
```

## Алгоритм `ShouldPruneAfterVisit` (дешевое сначала)

1. нет непосещённых → `(false, NoReason)`;
2. одна оставшаяся клетка: отсечь (`DeadEnd`), если она не соседняя к `last`;
3. локальный dead-end соседей `last` → `DeadEnd`;
4. `globalCheck` (при `|unvisited| > 1`): `NoContinuation` / `Disconnected` / `Endpoints`.

Маски соседей не копируются — `GetNeighborMask` читает предвычисленную маску за O(1).

## Инварианты и корректность

- Ни одна проверка не отсекает ветвь, содержащую полный тур (связность обязательна;
  эвристика концов — необходимое условие).
- Эквивариантен D4 — безопасен для канонизации.
- Стейт-лесс: безопасен для конкурентного вызова из воркеров; счётчики —
  в `types.Result.CountPrune(reason)`.

## Ограничения и edge cases

- При `|unvisited| ≤ 1` глобальная проверка не выполняется (одинокая соседняя клетка —
  завершающий ход, не тупик).

## Тесты

`pruner/pruner_test.go`: локальные кейсы (нет unvisited / одна клетка reachable/unreachable
/ изолированные угол-центр-две / валидный путь не отсекается); вероятностная эквивалентность
полному скану (`MatchesFullScan`) и наивной BFS-реализации (`MatchesNaive`); табличные причины
(`TestPruneReasons`). Замеры эффективности — в ADR-006, не здесь.

## Связанные

ADR-006, ADR-009, ADR-016; `specs/types.md`.
