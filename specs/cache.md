# Компонент Cache: Мемоизация с симметрией и шардингом

## Назначение

Мемоизация (кэширование) промежуточных результатов для избежания повторных вычислений одинаковых состояний поиска. Особенно эффективно когда в дереве поиска есть дублирующиеся поддеревья.

Ключевые особенности текущей реализации:
- **Шардинг**: Разделение кэша на 64 шарда для параллельного доступа
- **Симметрия**: Ключом является каноническая пара `(state, end)` — без `start`.
  Число продолжений маршрута зависит только от посещённой маски и текущей клетки,
  поэтому префиксы из разных стартовых орбит с симметричными `(state, end)`
  сливаются в одну подзадачу
- **Вес**: value хранит агрегированный вес `Σ count·orbitSize` (см. ниже)
- **Потокобезопасность**: RWMutex для каждого шарда

## Структура данных

```go
type shard struct {
    data map[path.Path]int  // canonical (state, end) → weight (первым — минимум pointer-префикса, fieldalignment)
    sync.RWMutex
}

type Cache struct {
    shards   [numShards]shard  // numShards = 64
    symmetry *Symmetry         // для канонизации состояний
}
```

- **key**: `path.Path` — задача поиска, содержащая:
  - `State()` (uint64) — битовая маска посещенных клеток
  - `End()` — текущая позиция
  - (`Start()` из ключа удалён: продолжения от `(state, end)` не зависят от старта)
- **value**: `int` — агрегированный вес подзадачи: сумма по всем слившимся
  префиксам `count_g · orbitSize_g`, где `g` — стартовая орбита префикса.
  Итоговый вклад подзадачи в ответ: `completions(key) * weight`
- **shards**: Массив из 64 шардов, каждый со своим мьютексом

### Почему вес зашивается при записи, а не при чтении

Раньше multiplier орбиты применялся на потреблении через `GetOrbitSize(p.Start())`.
Поскольку ключ больше не содержит `start` и может объединять префиксы из разных
орбит, единственный корректный момент умножения — генерация: каждая группа
пишет свои префиксы с весом `group.OrbitSize`, а шард просто суммирует вклады
под одним каноническим ключом.

### Альтернативная интерпретация веса: мультипликативность по fiber

То же значение `W(K)` можно читать не как «сумму орбит», а как **число всех реальных
префиксных путей, попадающих в орбиту узла** `K`: каждый D4-орбит путей даёт вклад ровно
`|orbit|`, т.к. из канонического старта перечисляется `|Stab(c)|/|Stab(p)|` образов пути, а
вес каждого — `8/|Stab(c)|`; стабилизаторы сокращаются:

```
W(K) = Σ_{(S',e') ∈ fiber(K)} h(S',e') = |fiber(K)| · h(U,u),   деление точное
```

где `h(U,u)` — число путей коня, покрывающих ровно `U` и заканчивающихся в `u`. Это
используется досрочным завершением (см. searcher.md): `h = W(canon)/fiberSize(canon)`,
а итоговая формула `Σ_K W(K)·f(K)` остаётся прежней — обе интерпретации дают один и тот же
подсчёт ориентированных туров (у каждого тура ровно один префикс глубины d).

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
// Получить агрегированный вес для канонической пары (state, end)
// Возвращает: (weight, found)

func (c *Cache) GetCanonical(p path.Path) (int, bool)
// То же, но ключ уже канонический (повторная канонизация не выполняется).
// Используется досрочным завершением searchera, которое получает канонический
// ключ и размер его орбиты одним проходом symmetry.CanonicalizeWithOrbitSize.

func (c *Cache) Set(path path.Path, weight int)
// Добавить вес к записи для канонической пары (state, end)
// При совпадении ключей веса суммируются: data[key] += weight

func (c *Cache) Has(path path.Path) bool
// Проверка наличия записи

func (c *Cache) Delete(path path.Path)
// Удаление записи из кэша

func (c *Cache) ItemsCount() int
// Возвращает общее количество записей во всех шардах

func (c *Cache) Each(ctx context.Context, workers int, f func(ctx context.Context, p path.Path, count int))
// Итерация по всем записям во всех шардах (параллельно через errgroup).
// ВАЖНО: f вызывается с удержанным RLock шарда — повторные чтения из того же
// кэша внутри f безопасны только при отсутствии писателей (invariant фазы
// подсчёта: генерация уже завершена, кэш read-only).
```

## Симметрия и канонизация

Все операции с кэшем используют `symmetry.Canonicalize(state, end)` для сведения пары к лексикографическому минимуму орбиты D4:

```go
canonical := c.symmetry.Canonicalize(p.State(), p.End())
shardIdx := c.getShardKey(canonical)

// Далее работа с canonical в конкретном шарде
```

Это позволяет:
- Объединять эквивалентные состояния, полученные через повороты и отражения доски
- Сливаться подзадачам из **разных** стартовых орбит (ключ не содержит start),
  если их `(state, end)` симметричны — число продолжений у них одинаково
- Снижать количество уникальных записей в кэше

## Использование в Searcher/Counter

```go
// При генерации подзадач (вес = орбита стартовой группы):
cache := cache.NewCache(symmetry)
result := searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)

// При параллельном подсчете:
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    // вес уже включает orbitSize каждой внесшей вклад группы
    total.Add(uint64(result.TotalPathsFound) * uint64(weight))
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

### 3. Суммирование весов при совпадении

При `Set()` веса суммируются, а не заменяются — это и есть механизм слияния
префиксов разных групп под одним ключом:

```go
func (c *Cache) Set(p path.Path, weight int) {
    canonical := c.symmetry.Canonicalize(p.State(), p.End())
    shardIdx := c.getShardKey(canonical)
    c.shards[shardIdx].Lock()
    defer c.shards[shardIdx].Unlock()
    c.shards[shardIdx].data[canonical] += weight  // суммирование весов!
}
```

## Тесты

```go
func TestCacheBasic(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    p := path.New(state.NewState(0, 1), 1)

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

            p := path.New(state.NewState(i%25), i%25)
            cache.Set(p, i*100)
        }(i)
    }

    wg.Wait()

    // Проверяем что записано (симметричные ключи канонизируются и могут сливаться)
    for i := range 10 {
        p := path.New(state.NewState(i%25), i%25)
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

    p := path.New(state.NewState(0, 1), 1)
    cache.Set(p, 42)

    require.Equal(t, 1, cache.ItemsCount())

    cache.Clear()
    require.Equal(t, 0, cache.ItemsCount())
}

func TestCacheEach(t *testing.T) {
    sym := symmetry.NewSymmetry(5)
    cache := cache.NewCache(sym)

    p := path.New(state.NewState(0, 1), 1)
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
