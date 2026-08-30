# Компонент State: Битовые маски состояния

## Назначение

Представление текущего состояния поиска маршрута коня с использованием битовых масок. Каждый бит в `uint64` соответствует клетке доски — 1 означает "посещена", 0 — "не посещена".

## Структура данных

```go
type State uint64
```

- Тип `State` — это `uint64`, где каждый бит (от 0 до N²-1) соответствует клетке доски
- Нумерация клеток: `index = row * size + col`
- Для досок > 8×8 потребуется `big.Int` или массив `uint64[]`

## Основные операции

### Создание и проверка состояния

```go
func NewState(visited ...int) State
// Возвращает начальное состояние с посещенными клетками из списка visited
// Пример: NewState(0, 1, 5) создает состояние с посещенными клетками 0, 1 и 5

func (s State) IsVisited(pos int) bool
// Проверяет, посещена ли клетка с индексом pos
```

### Модификация состояния

```go
func (s State) Visit(pos int) State
// Возвращает новое состояние с отмеченной клеткой pos как посещенной

func (s State) Unvisit(pos int) State
// Возвращает новое состояние с сброшенной клеткой pos
```

### Проверка завершения и статистика

```go
func (s State) IsFull(cellsCount int) bool
// Проверяет, все ли cellsCount клеток посещены

func (s State) CountBits() int
// Считает количество посещенных клеток (количество единичных битов)
// Использует bits.OnesCount64 для оптимальной производительности

func (s State) IsUnvisited(pos int) bool
// Проверяет, что клетка pos НЕ посещена (инверсия IsVisited)

func (s State) IsEmpty() bool
// Проверяет, что состояние пустое (все биты = 0)
```

### Операции с масками

```go
func (s State) GetUnvisitedMask(cellsCount int) State
// Возвращает маску всех непосещенных клеток (инверсия состояния в пределах доски)

func (s State) UnvisitedMask(cellsCount int) State
// Алиас для GetUnvisitedMask

func (s State) Intersect(mask State) State
// Пересечение двух состояний (побитовое И)

func (s State) Union(mask State) State
// Объединение двух состояний (побитовое ИЛИ)

func (s State) AndNot(mask State) State
// Исключение битов mask из s (s & ^mask)
```

### Быстрый поиск и сдвиги

```go
func (s State) TrailingZeroBits() uint
// Возвращает номер первого установленного бита (LSB)
// Использует bits.TrailingZeros64 для оптимальной производительности

func (s State) AllVisited() iter.Seq[int]
// Итератор по позициям всех посещенных клеток (по возрастанию)
// Позволяет обходить множество без прямых битовых операций в коде вызывающих:
//   for pos := range st.AllVisited() { ... }

func Bit(pos int) State
// Пакетная функция: маска с одним установленным битом pos (1 << pos)
// Единственное разрешенное место создания побитовых масок извне пакета state

func (s State) ShiftLeft(n int) State
// Сдвиг влево на n битов

func (s State) ShiftRight(n int) State
// Сдвиг вправо на n битов

func (s State) IsBitSet(pos int) bool
// Алиас для IsVisited
```

### Дополнительные методы

```go
func (s State) Invert(cellsCount int) State
// Инвертирует состояние в пределах cellsCount битов

func (s State) IsHalfway(cellsCount int) bool
// Проверяет, находится ли текущий счетчик битов на половине или ниже

func (s State) HalfwayPoint(cellsCount int) int
// Возвращает индекс половины или -1 если не достигнута

func (s State) String() string
// Возвращает строковое представление в двоичном виде
```

## Использование в поиске

State — основной тип горячего цикла DFS (см. `searcher.dfs`), упрощённо (без depth/кэша):

```go
func dfs(st state.State, end int) int {
    // маска непосещенных клеток и кандидаты хода
    unvisited := st.Invert(graph.GetTotalCells())
    cand := graph.GetNeighborMask(end).Intersect(unvisited)

    found := 0
    for n := range cand.AllVisited() { // итератор без прямых битовых операций
        newUnvisited := unvisited.Unvisit(n)
        if !newUnvisited.IsEmpty() && deadend.ShouldPruneAfterVisit(n, newUnvisited) {
            continue
        }
        found += dfs(st.Visit(n), n)
    }
    return found
}
```

## Преимущества битовых масок

| Операция | С bool[] | С uint64 |
|----------|----------|----------|
| Проверка посещенности | O(1) с разыменованием | O(1) побитовая операция |
| Копирование состояния | O(N²) копирование массива | O(1) присваивание |
| Хэш функция | сложная (нужно хэшировать массив) | тривиальная (само состояние как хэш) |
| Память для 8×8 | 64 байта (bool) или ~8 байт (uint64) | 8 байт |

## Ограничения

- Максимум 64 клетки (8×8 доска с `uint64`)
- Для больших досок нужен массив `[]uint64` или `big.Int`
- Битовые манипуляции менее читаемы чем массивы

## Тесты

```go
func TestStateNew(t *testing.T) {
    s := NewState(0, 5, 10)
    require.True(t, s.IsVisited(0))
    require.True(t, s.IsVisited(5))
    require.True(t, s.IsVisited(10))
    require.False(t, s.IsVisited(1))
}

func TestStateVisit(t *testing.T) {
    s := NewState()
    s = s.Visit(5)
    require.True(t, s.IsVisited(5))
    require.False(t, s.IsVisited(3))
}

func TestStateIsFull(t *testing.T) {
    // Для 9 клеток (доска 3×3)
    full := State((1 << 9) - 1)
    require.True(t, full.IsFull(9))
    
    partial := full ^ (1 << 0)  // сброшен первый бит
    require.False(t, partial.IsFull(9))
}

func TestStateCountBits(t *testing.T) {
    s := State(0b1011)
    require.Equal(t, 3, s.CountBits())
}

func TestStateMasks(t *testing.T) {
    s := NewState(0, 1, 2)
    mask := s.GetUnvisitedMask(9)
    
    require.False(t, mask.IsVisited(0))
    require.False(t, mask.IsVisited(1))
    require.True(t, mask.IsVisited(3))
    require.True(t, mask.IsVisited(8))
}

func TestStateIntersectUnion(t *testing.T) {
    s1 := NewState(0, 1, 2)
    s2 := NewState(1, 2, 3)
    
    intersect := s1.Intersect(s2)
    union := s1.Union(s2)
    
    require.True(t, intersect.IsVisited(1))
    require.False(t, intersect.IsVisited(0))
    require.True(t, union.IsVisited(0))
    require.True(t, union.IsVisited(3))
}
```

## Возможные улучшения

- Использовать встроенные функции Go: `bits.OnesCount(uint64)` из пакета `math/bits` (уже используется)
- Для досок >8×8 использовать срез `[]uint64` и расширить интерфейс
- Кэширование результата CountBits если вызывается часто
