# Компонент Graph: Граф смежности с масками соседей

## Назначение

Предоставление предварительно вычисленного графа смежности для ходов коня на доске размером N×N.
Граф используется в поиске маршрутов для быстрого получения списка соседних клеток.

## Структура данных

```go
type Graph struct {
    neighbors     [][]int // Списки соседей для каждой клетки (0..N²-1) — первыми, минимизация pointer-префикса (fieldalignment)
    neighborMasks []State // Битовые маски соседей для быстрой проверки
    size          int     // Размер доски N
    totalCells    int     // Общее количество клеток (N²)
}
```

## Основные методы

### 1. Создание графа

```go
func New(size int) *Graph
// Создает граф и предварительно вычисляет соседей для всех клеток доски
// Порядок соседей соответствует порядку обхода possibleMoves:
// (-2,-1), (-2,+1), (-1,-2), (-1,+2), (+1,-2), (+1,+2), (+2,-1), (+2,+1)
```

### 2. Доступ к данным

```go
func (g *Graph) Size() int
// Возвращает размер доски N

func (g *Graph) GetTotalCells() int
// Возвращает общее количество клеток (N²)

func (g *Graph) SholdSkip(pos int) bool
// Проверяет, нужно ли пропустить позицию (для симметрии на досках нечетного размера)
// Возвращает true если size%2 != 0 и pos%2 != 0

func (g *Graph) GetNeighbors(pos int) []int
// Возвращает список соседей для позиции pos (0..N²-1)
// Пустой слайс если neighbors[pos] не инициализирован

func (g *Graph) GetDegree(pos int) int
// Возвращает количество соседей (степень вершины)
// Эквивалентно len(g.GetNeighbors(pos))

func (g *Graph) GetNeighborMask(pos int) State
// Возвращает битовую маску всех соседей для позиции pos
```

## Инициализация

При создании графа:

1. Создается массив `neighbors` размером totalCells
2. Для каждой клетки (x, y):
   - Вычисляются все возможные ходы коня
   - Проверяется валидность координат (0 ≤ nx < size, 0 ≤ ny < size)
   - Новые позиции добавляются в `neighbors[pos]`
3. Создается массив `neighborMasks` того же размера
   - Для каждой позиции формируется битовая маска всех соседей

## Пример использования

```go
// Создание графа для доски 5×5
graph := graph.New(5)

// Получение соседей для позиции (угол)
neighbors := graph.GetNeighbors(0) // → [6, 9]

// Проверка количества соседей
degree := graph.GetDegree(0) // → 2

// Битовая маска соседей
mask := graph.GetNeighborMask(0) // биты 6 и 9 установлены

// Пропуск позиции (для симметрии на досках нечетного размера)
shouldSkip := graph.SholdSkip(3) // → true если pos%2 != 0 и size%2 != 0

// Обход соседей в поиске
for _, neighbor := range graph.GetNeighbors(currentPos) {
    if !state.IsVisited(neighbor) {
        // рекурсивный вызов с neighbor
    }
}
```

## Особенности реализации

1. **Порядок соседей**: Соседи упорядочены по порядку обхода possibleMoves, без специальной сортировки (в отличие от предложенной в старой версии)

2. **Битовые маски**: neighborMasks позволяют быстро проверять соседство через операции `&` и `IsVisited`

3. **Мемоизация**: Все соседи вычисляются один раз при создании графа, что ускоряет поиск

## Ограничения

- Поддерживает только доски до 8×8 (64 клетки, помещаются в uint64)
- Порядок соседей не оптимизирован по Warnsdorff
- Нет методов для преобразования координат ↔ индекс (Index/Coords отсутствуют)

## Тесты

```go
func TestGraphSize(t *testing.T) {
    g := graph.New(5)
    require.Equal(t, 5, g.Size())
    require.Equal(t, 25, g.GetTotalCells())
}

func TestGraphNeighborsCorner(t *testing.T) {
    g := graph.New(5)
    neighbors := g.GetNeighbors(0) // (0,0)
    require.Contains(t, neighbors, 6) // (1,2)
    require.Contains(t, neighbors, 9) // (2,1)
    require.Equal(t, 2, len(neighbors))
}

func TestGraphNeighborsCenter(t *testing.T) {
    g := graph.New(5)
    neighbors := g.GetNeighbors(12) // (2,2) - центр
    require.Equal(t, 8, len(neighbors)) // максимум 8 соседей для коня
}

func TestGraphNeighborMask(t *testing.T) {
    g := graph.New(5)
    mask := g.GetNeighborMask(0)
    require.True(t, mask.IsVisited(6))
    require.True(t, mask.IsVisited(9))
}
```

## Заключение

Graph — основной компонент для работы с топологией доски. Предоставляет быстрый доступ к списку соседей и битовым маскам для каждой клетки.
