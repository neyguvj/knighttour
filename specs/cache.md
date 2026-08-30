# Компонент Cache: Мемоизация с симметрией и шардингом

## Назначение

Мемоизация (кэширование) промежуточных результатов для избежания повторных вычислений одинаковых состояний поиска. Особенно эффективно когда в дереве поиска есть дублирующиеся поддеревья.

Ключевые особенности текущей реализации:
- **Шардинг**: Разделение кэша на 64 шарда для параллельного доступа
- **Симметрия**: Использование канонических представлений путей для уменьшения дублирующихся вычислений
- **Потокобезопасность**: RWMutex для каждого шарда

## Структура данных

```go
type shard struct {
    sync.RWMutex
    data map[path.Path]int  // canonical path → countOfSolutions
}

type Cache struct {
    shards   [numShards]shard  // numShards = 64
    symmetry *Symmetry         // для канонизации путей
}
```

- **key**: `path.Path` — объект пути, содержащий:
  - `State()` (uint64) — битовая маска посещенных клеток
  - `Start()` — начальная позиция
  - `End()` — текущая позиция
- **value**: `int` — количество решений из этого канонического состояния
- **shards**: Массив из 64 шардов, каждый со своим мьютексом

## Хэширование и шардинг

Ключ кэша преобразуется в индекс шарда мультипликативным хэшем без аллокаций:

```go
func (c *Cache) getShardKey(p path.Path) int {
    // Бесаллокационный хэш: умножение на золотое сечение, старшие биты.
    h := uint64(p.State())*0x9E3779B97F4A7C15 + uint64(p.End())*0xC2B2AE3D27D4EB4F
    return int(h >> (64 - 6)) // numShards = 64
}
```

## Основные операции

### Создание и управление кэшем

```go
func NewCache(sym *Symmetry) *Cache
// Создает кэш с 64 шардами и указанными симметриями
// Каждый шард инициализируется пустой картой

func (c *Cache) Clear()
// Очищает все шарды (удаляет все записи)
```

### Операции с кэшем

```go
func (c *Cache) Get(path path.Path) (int, bool)
// Получить результат для канонического пути
// Возвращает: (count, found)

func (c *Cache) Set(path path.Path, val int)
// Сохранить результат для канонического пути
// При совпадении ключей значения суммируются

func (c *Cache) Has(path path.Path) bool
// Проверка наличия записи

func (c *Cache) Delete(path path.Path)
// Удаление записи из кэша

func (c *Cache) ItemsCount() int
// Возвращает общее количество записей во всех шардах

func (c *Cache) Each(ctx context.Context, workers int, f func(ctx context.Context, p path.Path, count int))
// Итерация по всем записям во всех шардах (параллельно через errgroup)
```

## Симметрия и канонизация

Все операции с кэшем используют `symmetry.CanonicalizePath()` для преобразования пути в каноническое представление:

```go
canonicalPath := c.symmetry.CanonicalizePath(path)
shardIdx := c.getShardKey(canonicalPath)

// Далее работа с canonicalPath в конкретном шарде
```

Это позволяет:
- Объединять эквивалентные состояния, полученные через повороты и отражения доски
- Снижать количество уникальных записей в кэше
- Повышать hit ratio

## Использование в Searcher/Counter

```go
// При генерации подзадач:
cache := cache.NewCache(symmetry)
result := searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)

// При параллельном подсчете:
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, count int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    orbits := uint64(c.symmetry.GetOrbitSize(p.Start()))

    total.Add(uint64(result.TotalPathsFound*count) * orbits)
})
```

## Эффективность кэширования

### Когда кэш эффективен:

| Ситуация | Эффект |
|----------|--------|
| Много ветвей с одинаковыми состояниями | Высокий hit ratio благодаря канонизации |
| Глубокое дерево поиска | Многократные переиспользования |
| Доски ≤6×6 | Кэш ускоряет на 2-3x |
| Шардинг с 64 шардами | Хорошая параллелизация |

### Когда кэш неэффективен:

| Ситуация | Причина |
|----------|---------|
| Доски ≥7×7 | Число возможных состояний ≈ 2^49 и более |
| Каждое состояние уникально (даже с канонизацией) | Hit ratio < 1% |

## Ограничения и оптимизации

### 1. Ограничение на размер кэша

Текущая реализация не имеет явного ограничения на размер.

### 2. Использование только для поддеревьев

Рекомендация: использовать кэш только когда осталось <30 клеток (можно добавить флаг или проверку в Searcher).

### 3. Суммирование при совпадении

При `Set()` значения суммируются, а не заменяются:

```go
func (c *Cache) Set(path path.Path, val int) {
    canonicalPath := c.symmetry.CanonicalizePath(path)
    shardIdx := c.getShardKey(canonicalPath)
    c.shards[shardIdx].Lock()
    defer c.shards[shardIdx].Unlock()
    c.shards[shardIdx].data[canonicalPath] += val  // суммирование!
}
```

## Тесты

```go
func TestCacheBasic(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    p := path.New(state.NewState(0, 1), 0, 1)

    // Пустой кэш не содержит ничего
    count, ok := cache.Get(p)
    require.False(t, ok)
    require.Equal(t, 0, count)

    // Записываем значение
    cache.Set(p, 42)

    // Считываем обратно
    count, ok = cache.Get(p)
    require.True(t, ok)
    require.Equal(t, 42, count)
}

func TestCacheConcurrent(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    var wg sync.WaitGroup

    for i := range 10 {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()

            p := path.New(state.NewState(i%25), i%25, i%25)
            cache.Set(p, i*100)
        }(i)
    }

    wg.Wait()

    // Проверяем что записано (симметричные ключи канонизируются и могут сливаться)
    for i := range 10 {
        p := path.New(state.NewState(i%25), i%25, i%25)
        count, ok := cache.Get(p)
        require.True(t, ok)
        if !sym.IsCanonicalPosition(i % 25) {
            continue // значение могло слиться с каноническим ключом
        }
        require.Equal(t, i*100, count)
    }
}

func TestCacheItemsCount(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    p := path.New(state.NewState(0, 1), 0, 1)
    cache.Set(p, 42)

    require.Equal(t, 1, cache.ItemsCount())

    cache.Clear()
    require.Equal(t, 0, cache.ItemsCount())
}

func TestCacheEach(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    p := path.New(state.NewState(0, 1), 0, 1)
    cache.Set(p, 100)

    count := 0
    cache.Each(context.Background(), 1, func(ctx context.Context, p path.Path, v int) {
        count++
        require.Equal(t, 100, v)
    })

    require.Equal(t, 1, count)
}
```

## Возможные улучшения

- **LRU Cache**: Удалять старые записи при переполнении
- **Bloom Filter**: Быстрая предварительная проверка наличия в кэше перед захватом мьютекса
- **Compression**: Сжатие ключей для экономии памяти
- **Hierarchical Cache**: Малый быстрый L1-кэш + большой L2-кэш

## Заключение

Кэширование с шардингом и симметрией — мощная оптимизация для досок ≤6×6. Шардинг 64x обеспечивает хорошую масштабируемость при параллельном поиске, а канонизация через `Symmetry` значительно снижает количество уникальных состояний.
