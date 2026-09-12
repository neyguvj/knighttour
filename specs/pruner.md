# pruner: отсечение тупиковых ветвей

## Ответственность

Стейт-лесс компонент необходимых условий: отсекает ветви DFS/DP, заведомо не ведущие к
полному туру. Один тип `Pruner` с ярусами проверок (ADR-006). Не хранит статистику —
возвращает причину, учёт ведёт вызывающий через `types.Result`.

## Ярусы проверок

- **L0 — локальный dead-end**: изолированные клетки за O(deg(last)). Дешёвый первый блок.
- **L1 — глобальные** (`globalCheck`, всегда при `|unvisited| > 1`): нет продолжения у
  `last`; связность `G[unvisited]` (bitset flood-fill); эвристика концов гамильтова пути
  (degree-1 вершины обязаны стать концами).
- **L2 — усиленные** на состоянии `(cur, todo)`: сочленения (Tarjan lowlink) и обязательные
  цепочки. **Только из финального DP**, выключены по умолчанию (ADR-007).

## Публичный API

```go
type Reason uint8
const (NoReason; DeadEnd; NoContinuation; Disconnected; Endpoints; Articulation; ForcedChain Reason = iota...)

type L2Checks uint8
const (L2Articulation; L2ForcedChain; L2Endpoints L2Checks = 1<<iota...; L2None=0; L2All=L2Articulation|L2ForcedChain)
const DefaultMinL2 = 10

func New(g *graph.Graph) *Pruner                 // L0/L1 всегда, L2 выключен (minL2=DefaultMinL2)
func (p *Pruner) SetL2(mask L2Checks, minTodo int) // конфигурация ДО старта воркеров

// Горячий метод DFS: сразу после посещения last. Возвращает флаг + первую сработавшую
// причину (при false — NoReason). Инвариант: посещение last изолирует только соседей last.
func (p *Pruner) ShouldPruneAfterVisit(last int, unvisited state.State) (bool, Reason)

// L2 (только shapecount DP): порядок — сначала весь L0/L1, затем при включённой маске и
// todo.CountBits() >= minL2 проверки над H = G[todo ∪ {cur}]. O(V+E), без аллокаций.
func (p *Pruner) ShouldPruneState(cur int, todo state.State) (bool, Reason)

// Pre-DP фильтр реализуемости формы (ADR-008): «заведомо нет гамова пути по shape с
// концом end». Вызывается до выделения memo. Порядок дешёвое-сначала A→B→C.
func (p *Pruner) ShapeFeasible(end int, shape state.State, mask L2Checks) (bool, Reason)
```

## Алгоритм `ShouldPruneAfterVisit` (дешёвое сначала)

1. нет непосещённых → `(false, NoReason)`;
2. одна оставшаяся клетка: отсечь (`DeadEnd`), если она не соседняя к `last`;
3. локальный dead-end соседей `last` → `DeadEnd`;
4. `globalCheck` (при `|unvisited| > 1`): `NoContinuation` / `Disconnected` / `Endpoints`.

Маски соседей не копируются — `GetNeighborMask` читает предвычисленную маску за O(1).

## Инварианты и корректность

- Ни одна проверка не отсекает ветвь, содержащую полный тур (связность обязательна;
  эвристики концов / L2 — необходимые условия).
- Эквивариантен D4 — безопасен для канонизации.
- ForcedChain корректен **только при зафиксированном конце** (ровно одна вершина `todo`
  степени 1 в `H`); иначе пропускается.
- Стейт-лесс после `SetL2`: безопасен для конкурентного вызова из воркеров; счётчики —
  в `types.Result.CountPrune(reason)`.

## Ограничения и edge cases

- При `|unvisited| ≤ 1` глобальная проверка не выполняется (одинокая соседняя клетка —
  завершающий ход, не тупик).
- `ShapeFeasible` на несвязном `G[shape]`: корректно в обе стороны (несвязный ⇒ h=0).

## Тесты

`pruner/pruner_test.go`: локальные кейсы (нет unvisited / одна клетка reachable/unreachable
/ изолированные угол-центр-две / валидный путь не отсекается); вероятностная эквивалентность
полному скану (`MatchesFullScan`) и наивной BFS-реализации (`MatchesNaive`); табличные причины
(`TestPruneReasons`); L2 — `ShouldPruneState` ниже порога = base, articulation/forced-chain
кейсы, звуковость на случайных состояниях; `ShapeFeasible` — причины + звуковость против
brute-force. Замеры эффективности — в ADR-006/007/008, не здесь.

## Связанные

ADR-006, ADR-007, ADR-008, ADR-009; `specs/types.md`, `specs/shapecount.md`.
