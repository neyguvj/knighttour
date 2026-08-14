# Компонент Symmetry: Работа с симметриями доски

## Назначение

Использование геометрических симметрий квадратной доски для сокращения объема поиска. Квадратная доска обладает группой симметрий D4 (8 элементов), что позволяет уменьшить количество стартовых позиций, из которых нужно искать маршруты.

## Группа D4

Группа симметрий квадрата состоит из 8 преобразований:

| № | Название | Описание | Формула для (x,y) |
|---|----------|----------|------------------|
| 0 | Identity | Без изменений | (x, y) |
| 1 | Rotate 90° | Поворот на 90° по часовой | (y, size-1-x) |
| 2 | Rotate 180° | Поворот на 180° | (size-1-x, size-1-y) |
| 3 | Rotate 270° | Поворот на 270° | (size-1-y, x) |
| 4 | Flip horizontal | Отражение по горизонтали | (x, size-1-y) |
| 5 | Flip vertical | Отражение по вертикали | (size-1-x, y) |
| 6 | Flip diag1 | Отражение по главной диагонали | (y, x) |
| 7 | Flip diag2 | Отражение по побочной диагонали | (size-1-y, size-1-x) |

## Структура данных

```go
type Transform func(x, y, size int) (int, int)

type Symmetry struct {
    size                int
    transforms          []Transform        // 8 преобразований
    canonical           []int              // каноническая позиция для каждой клетки
    canonicalTransforms []Transform        // лучшее преобразование для каждой клетки
    orbitSize           []int              // размер орбиты для каждой клетки
    bestTransforms      [][]Transform      // оптимальное преобразование для пар (start, end)
}
```

## Основные методы

### 1. Создание симметрий

```go
func NewSymmetry(size int) *Symmetry
// Создает симметрии и предварительно вычисляет канонические позиции для всех клеток
// Выполняет:
// - Вычисление канонической позиции и трансформации для каждой клетки
// - Расчет размера орбиты для каждой клетки
// - Построение bestTransforms для всех пар (start, end)
```

### 2. Доступ к каноническим позициям

```go
func (s *Symmetry) GetCanonicalPosition(pos int) int
// Возвращает каноническую позицию из линейного индекса

func (s *Symmetry) IsCanonicalPosition(pos int) bool
// Проверяет, является ли позиция канонической
```

### 3. Размер орбиты

```go
func (s *Symmetry) GetOrbitSize(pos int) int
// Возвращает количество симметричных позиций в классе эквивалентности
```

### 4. Канонические группы

```go
type CanonicalGroup struct {
    Canonical int     // каноническая позиция
    OrbitSize int     // размер орбиты
    Positions []int   // все позиции в группе
}

func (s *Symmetry) GetCanonicalGroups() []CanonicalGroup
// Возвращает все группы канонических позиций
```

### 5. Канонизация пути

```go
func (s *Symmetry) CanonicalizePath(p path.Path) path.Path
// Преобразует путь в лексикографически минимальную форму среди всех симметрий
// Алгоритм:
// 1. Находит оптимальное преобразование для пары (start, end)
// 2. Применяет это преобразование к start, end и state
// 3. Возвращает канонический путь
```

## Использование в Counter

```go
groups := symmetry.GetCanonicalGroups()
for _, group := range groups {
    // group.Canonical — стартовая позиция для поиска
    // group.OrbitSize — множитель для результатов
    
    subtasks := searcher.GenerateSubtasksWithMetadata(
        ctx,
        group.Canonical,
        group.OrbitSize,  // передается как orbitSize
        depth,
    )
    
    for _, task := range subtasks {
        // task.SymmetriesCount = orbitSize * countOfCanonicalForms
    }
}
```

## Использование в Cache

```go
// Кэш использует CanonicalizePath для объединения симметричных путей:
canonicalPath := c.symmetry.CanonicalizePath(path)
shardIdx := c.getShardKey(canonicalPath)

if count, found := c.shards[shardIdx].data[canonicalPath]; found {
    // кэш-попадание
}
```

## Использование в Searcher

```go
// В GenerateSubtasksWithMetadata:
for _, task := range rawSubtasks {
    canonicalState := s.sym.CanonicalizePath(task)
    canonizedTasks[canonicalState]++
}

for task, count := range canonizedTasks {
    subtasks = append(subtasks, types.Subtask{
        SymmetriesCount: orbitSize * count,
        // ...
    })
}
```

## Примеры размеров орбит

### Доска 5×5:
- Угловые клетки (0, 4, 20, 24): орбита = 4
- Реберные неугловые: орбита = 4  
- Центральная (не на диагонали): орбита = 8
- Центральная клетка (12): орбита = 1

### Доска 8×8:
- Угловые клетки: орбита = 4
- Клетки в центре ребер (0,3), (0,4), (7,3), (7,4): орбита = 4
- Центральные не на диагонали: орбита = 8
- На главной/побочной диагонали (кроме центра): орбита = 4

## Эффективность по типу позиции

| Тип позиции | Примеры | Размер орбиты | Уменьшение |
|-------------|---------|---------------|------------|
| Центральная (не на диагонали) | (2,3), (3,2) на 8×8 | 8 | в 8 раз |
| На главной/побочной диагонали | (2,2), (2,5) на 8×8 | 4 | в 4 раза |
| Центр ребра | (0,3), (3,7) на 8×8 | 4 | в 4 раза |
| Угловая | (0,0), (0,7), (7,0), (7,7) | 4 | в 4 раза |
| Центр доски нечетного размера | (2,2) на 5×5 | 1 | в 1 раз |

## Трансформации

```go
type Transform func(x, y, size int) (int, int)

func GetSymmetries(size int) []Transform
// Возвращает массив из 8 преобразований для доски заданного размера
```

**Примеры:**
- Identity: `(x, y)` → без изменений
- Rotate 90°: `(y, size-1-x)`
- Flip horizontal: `(x, size-1-y)`
- Flip diag1: `(y, x)`

## Тесты

```go
func TestSymmetryNew(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    
    require.Equal(t, 8, len(sym.GetCanonicalGroups()))
}

func TestSymmetryGetCanonicalPosition(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    
    // Все угловые позиции должны нормализоваться в одну каноническую
    canonical0 := sym.GetCanonicalPosition(0)
    canonical4 := sym.GetCanonicalPosition(4)
    
    require.Equal(t, canonical0, canonical4)
}

func TestSymmetryGetOrbitSize(t *testing.T) {
    sym := symmetry.NewSymmetry(8)
    
    // Угловая позиция
    orbit0 := sym.GetOrbitSize(0)
    require.Equal(t, 4, orbit0)
    
    // Центральная (не на диагонали)
    orbit21 := sym.GetOrbitSize(21)  // (2,5)
    require.Equal(t, 8, orbit21)
}

func TestSymmetryCanonicalizePath(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    
    p := path.New(state.State(0).Visit(6), 0, 6)
    canonical := sym.CanonicalizePath(p)
    
    // Проверяем что каноническая позиция start минимальна
    require.True(t, sym.IsCanonicalPosition(canonical.Start()))
}
```

## Ограничения и особенности

- Для досок нечетного размера центральная клетка симметрична самой себе (orbitSize=1)
- При использовании кэша нужно учитывать, что состояния из симметричных позиций объединяются
- Предварительное вычисление канонических позиций занимает O(N²) времени и памяти

## Заключение

Symmetry — компонент для работы с геометрическими свойствами доски. Канонизация путей через sym.CanonicalizePath позволяет эффективно объединять симметричные состояния в кэше и уменьшать объем поиска.
