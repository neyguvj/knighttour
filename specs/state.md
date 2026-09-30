# state: битовая маска посещённых клеток

## Ответственность

Пакет даёт тип `State` — битборд посещённых клеток доски, упакованный в `uint64`, и все
побитовые операции над ним. Это единственное место, где допустимы прямые битовые операции
(`<<`, `&^`, `math/bits`). Остальной код выражает те же действия методами `State`,
конструктором одного бита `Bit()` и итератором `AllVisited()`. Сам тип размера доски не знает:
границу методов задаёт аргумент `cellsCount`.

## Публичный API

```go
type State uint64                       // бит i выставлен, если клетка i посещена (index = row*size + col)

func NewState(visited ...int) State     // маска с битами на перечисленных в аргументах позициях
func Bit(pos int) State                 // маска одного бита 1 << pos — внешний способ собрать маску без методов

// проверка и модификация (методы возвращают новое значение, приёмник не меняется)
func (s State) IsVisited(pos int) bool   // клетка pos помечена посещённой
func (s State) IsUnvisited(pos int) bool // обратная проверка: бит pos не выставлен
func (s State) Visit(pos int) State      // маска, где клетка pos добавлена к посещённым
func (s State) Unvisit(pos int) State    // маска, где клетка pos снята с посещённых

// статистика и завершённость
func (s State) CountBits() int             // число посещённых клеток
func (s State) IsEmpty() bool              // не выставлено ни одного бита
func (s State) IsFull(cellsCount int) bool // все cellsCount младших битов выставлены
func (s State) TrailingZeroBits() uint     // индекс младшего установленного бита

// сложение масок
func (s State) Intersect(mask State) State // оставить общие биты: s & mask
func (s State) Union(mask State) State     // объединить две маски: s | mask
func (s State) AndNot(mask State) State    // убрать биты, входящие в mask: s &^ mask
func (s State) Invert(cellsCount int) State // дополнение в пределах cellsCount битов;
                                            // из посещённых клеток даёт маску непосещённых

// обход и представление
func (s State) AllVisited() iter.Seq[int]  // позиции установленных битов по возрастанию
func (s State) String() string             // двоичная запись без ведущих нулей, для отладочного вывода
```

## Инварианты

- Все методы работают со значением приёмника, поэтому исходная маска не меняется; каждая
  операция возвращает новый `State`.
- `Invert` обнуляет всё выше `cellsCount`, поэтому за границами доски не остаётся мусорных битов.
- Клетки нумеруются по строкам: клетка `(row, col)` занимает бит `row*size + col`.

## Ограничения и edge cases

- В `uint64` помещается не более 64 клеток, поэтому доски крупнее 8×8 не поддерживаются.
- `TrailingZeroBits` на пустой маске возвращает 64 — так определён `bits.TrailingZeros64`.

## Тесты

`state/state_test.go` покрывает пакет табличными тестами (testify), по одному на операцию:
`TestNewState`, `TestIsVisited`, `TestVisit`, `TestUnvisit`, `TestIsFull` (включая доску 8×8
со всеми 64 битами), `TestCountBits`, `TestIsUnvisited`, `TestIntersect`, `TestUnion`,
`TestIsEmpty`, `TestTrailingZeroBits` (включая пустую маску), `TestAndNot`, `TestInvert`,
`TestString`. Конструктор `Bit` и итератор `AllVisited` отдельных тестов в пакете не имеют;
их корректность проверяют тесты пакетов, которые их используют.

## Связанные

`specs/searcher.md` (горячий DFS). Пакет также используется в `specs/graph.md`,
`specs/cache.md`, `specs/symmetry.md` и `specs/pruner.md`.
