# state: битовые маски состояния

## Ответственность

Тип-битборд посещённых клеток доски (`uint64`, до 8×8) и все побитовые операции над
ним. Единственное место, где допустимы прямые битовые операции (`<<`, `&^`,
`math/bits`) — остальной код использует методы `State`, пакетную `Bit()` и итератор
`AllVisited()`.

## Публичный API

```go
type State uint64                       // бит i = клетка i (index = row*size + col)

func NewState(visited ...int) State     // маска с выставленными битами visited
func Bit(pos int) State                 // 1 << pos (единственный внешний способ собрать маску)

// проверка / модификация (значения, без мутации)
func (s State) IsVisited(pos int) bool
func (s State) IsUnvisited(pos int) bool   // инверсия IsVisited
func (s State) Visit(pos int) State
func (s State) Unvisit(pos int) State

// статистика / завершённость
func (s State) CountBits() int             // bits.OnesCount64
func (s State) IsEmpty() bool
func (s State) IsFull(cellsCount int) bool
func (s State) TrailingZeroBits() uint     // номер первого установленного бита

// маски
func (s State) Intersect(mask State) State // s & mask
func (s State) Union(mask State) State     // s | mask
func (s State) AndNot(mask State) State    // s &^ mask
func (s State) Invert(cellsCount int) State   // инверсия в пределах cellsCount битов
                                              // (она же маска непосещённых)

// обход
func (s State) AllVisited() iter.Seq[int]  // позиции установленных битов по возрастанию
func (s State) String() string             // двоичное представление
```

## Инварианты

- Все операции не мутируют приёмник и возвращают новый `State` (value type).
- `Invert` обнуляет биты выше `cellsCount` — за доской «мусора» нет.
- Нумерация клеток row-major: `index = row*size + col`.

## Ограничения и edge cases

- Максимум 64 клетки (`uint64`); доски > 8×8 не поддерживаются.
- `TrailingZeroBits` на пустой маске возвращает 64 (семантика `bits.TrailingZeros64`).

## Тесты

`state/state_test.go`: создание/Visit/Unvisit, IsVisited/IsUnvisited, IsFull full vs
partial, CountBits паттернов, маски Intersect/Union/AndNot/Invert,
TrailingZeroBits, AllVisited, String — по одному табличному тесту на операцию.

## Связанные

`specs/searcher.md` (горячий DFS).
