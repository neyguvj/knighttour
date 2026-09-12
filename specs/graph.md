# graph: граф смежности ходов коня

## Ответственность

Предвычисленный граф ходов коня на доске N×N: списки соседей и битовые маски для
быстрых проверок в поиске. Строится один раз, далее только чтение (потокобезопасно).

## Публичный API

```go
func New(size int) *Graph

func (g *Graph) Size() int              // N
func (g *Graph) GetTotalCells() int     // N²
func (g *Graph) GetNeighbors(pos int) []int        // соседи клетки pos (0..N²-1)
func (g *Graph) GetDegree(pos int) int             // len(GetNeighbors(pos))
func (g *Graph) GetNeighborMask(pos int) state.State // битовая маска соседей
func (g *Graph) SholdSkip(pos int) bool            // фильтр чётности для нечётных досок
```

`SholdSkip(pos)` возвращает `true`, если `size%2 != 0 && pos%2 != 0` — старты этого
цвета дают нулевой вклад на нечётной доске (см. ADR-009).

## Инварианты

- Соседи упорядочены по фиксированному обходу `possibleMoves`:
  `(-2,-1),(-2,+1),(-1,-2),(-1,+2),(+1,-2),(+1,+2),(+2,-1),(+2,+1)` — без специальной
  сортировки (Warnsdorff отклонён, ADR-009).
- Граф симметричен: `b ∈ GetNeighbors(a) ⟺ a ∈ GetNeighbors(b)`.
- `GetNeighborMask` читает предвычисленную маску за O(1) — прунер/поиск не копируют маски.

## Ограничения и edge cases

- Только доски до 8×8 (маска — `state.State` = uint64).
- Нет преобразования координат ↔ индекс (не нужны потребителям).

## Тесты

`graph/graph_test.go`: Size/GetTotalCells, соседи угла (клетка 0 на 5×5 → {6,9}, deg 2),
центр (8 соседей), валидность границ, симметрия рёбер, эквивалентность маски и списка.

## Связанные

`specs/state.md`, `specs/pruner.md`, ADR-009.
