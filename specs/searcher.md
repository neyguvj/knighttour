# Компонент Searcher: Основной алгоритм поиска

## Назначение

Реализация DFS backtracking алгоритма для поиска всех открытых маршрутов коня с
использованием Advanced pruning (dead-end + связность + эвристика концов).

## Структура данных

```go
type Searcher struct {
    graph  *Graph         // граф смежности
    sym    *Symmetry      // для канонизации путей в кэше
    pruner *pruner.Pruner // отсечение тупиков + связность + эвристика концов
}
```

## Инициализация

```go
func NewSearcher(graph *Graph, sym *Symmetry) *Searcher
// Создает searcher с заданным графом и симметриями
```

## Основные методы

### 1. CountPaths(ctx context.Context, start int) types.Result

```go
func (s *Searcher) CountPaths(ctx context.Context, start int) types.Result
// Создает начальный путь с посещенной стартовой клеткой и вызывает CountPathsDFS
```

**Пример:**
```go
result := searcher.CountPaths(context.Background(), 0)
fmt.Printf("Found %d paths from position 0\n", result.TotalPathsFound)
```

### 2. GenerateSubtasks(ctx context.Context, cache *Cache, start int, orbitSize int, depth int) types.Result

```go
func (s *Searcher) GenerateSubtasks(
    ctx context.Context,
    cache *Cache,
    start int,
    orbitSize int,
    depth int,
) types.Result
// Генерирует подзадачи и сохраняет их в кэш для последующего использования
```

**Алгоритм:**
1. Если `SholdSkip(start)` — вернуть пустой результат (проверка вынесена наверх, т.к. start неизменен)
2. DFS по префиксам (`dfs`): как только `CountBits(state) >= depth`,
    сохранить задачу в кэш с весом орбиты (`cache.Set(path.New(st, end), orbitSize)`)
    и прекратить спуск; ветви режет `ShouldPruneAfterVisit` (тот же инвариант, что и в основном DFS).
    `start` в ключ не попадает — вместо него вклад группы кодируется весом `orbitSize`
3. Возвращает заполненный `types.Result`: `CacheWrites` (число вызовов `Set`) и
   разбивку прунинга по видам (`Pruned*`, сумма — `Pruned`). На генерации
   hits/misses нет (lookup'и — только в count-фазе)

### 2.1 ExtendSubtask(ctx context.Context, cache *Cache, p path.Path, weight int, depth int) types.Result

```go
func (s *Searcher) ExtendSubtask(
    ctx context.Context,
    cache *Cache,
    p path.Path,
    weight int,
    depth int,
) types.Result
// Догенерация подзадач от уже канонической записи (state, end) до целевой глубины.
// Применяется второй фазой двухфазной генерации (см. counter.md): p — запись
// промежуточного кэша глубины baseDepth, weight — её агрегированный вес,
// depth > CountBits(p.State()) — целевая precomputeDepth.
```

**Алгоритм:** тот же hot `dfs` с `c != nil`, `weight = weight(entry)`, `rev = nil`;
спуск от `p.State()/p.End()` до `depth`. `SholdSkip` не проверяется: корни уже
отфильтрованы фазой A.

**Корректность весов.** Для канонического ключа `J` все конкретные префиксы его
fiber'а — D4-образы канонического представителя, а продолжения образов
взаимно-однозначно отображаются с точностью до канонизации (граф и прунинг
D4-эквивариантны). Поэтому одно продолжение `J` с весом `W(J)` даёт те же вклады
в листовые ключи, что и перебор всех конкретных префиксов fiber'а поштучно:
итоговый кэш побитово совпадает с однофазной генерацией до `depth`.

### 3. CountPathsDFS / CountPathsWithReversal

```go
func (s *Searcher) CountPathsDFS(ctx context.Context, p path.Path) types.Result
// Считает полные маршруты из состояния p; если состояние уже полное — возвращает 1.
// Без досрочного завершения (эквивалент CountPathsWithReversal(ctx, p, nil, 0)).

func (s *Searcher) CountPathsWithReversal(
    ctx context.Context,
    p path.Path,
    o *oracle.Oracle,
    oracleDepth int,
) types.Result
// То же + досрочное завершение через shape-oracle: при достижении уровня
// CountBits(state) == totalCells - oracleDepth спуск прекращается и ответ
// берётся из оракула h (см. «Досрочное завершение через реверс»).
// Ошибки конфигурации (o == nil, oracleDepth вне [1, totalCells), stopLevel ниже
// текущей глубины p) обрабатывает newOracleReversal — он возвращает nil и
// CountPathsWithReversal считает чистым полным DFS.

func (s *Searcher) CountPathsWithCacheReversal(
    ctx context.Context,
    p path.Path,
    c *cache.Cache,
    d int,
) types.Result
// Legacy-быстрый путь: тот же уровень totalCells - d, но h = W(canon)/orbitSize
// читается из префикс-кэша генерации (бесплатно, т.к. генерация уже посчитала
// прогулки). Требует 2d ≤ n²; иначе newCacheReversal возвращает nil — чистый
// полный DFS. Быстрее oracle при том же d ценой экспоненциального кэша глубины d.
```

### 4. Досрочное завершение через реверс (reversal early stop)

**Тождество.** Для узла `(T, t)` с `U = fullMask &^ T` (`|U| = d`): число продолжений
равно числу обращённых суффиксов — путей, покрывающих ровно `U` и входящих в `t`:

```
f(T, t) = Σ_{u ∈ U, u ~ t} h(U, u),      h(U,u) = #(путей, покрывающих U и кончающихся в u)
```

Биекция: продолжение `u1…uk` обращается в путь `uk→…→u1`, заканчивающийся соседом `u1 ~ t`.
Тождество выполняется для любого `d < n²` — ограничение `2d ≤ n²` из старой схемы с
префикс-кэшем снято.

**Источник значения.** Два режима (выбирает counter):
- **Legacy prefix-cache** — `h = W(canon)/orbitSize` из префикс-кэша генерации.
  Быстрее всего (генерация уже посчитала прогулки), но требует кэш глубины d и
  `2d ≤ n²`.
- **Shape-oracle** — истинное `h(U,u)` мемоизируется по классам формы с точностью
  до D4 ⋉ трансляции (обоснование инвариантности — oracle.md). Работает для любой
  позиции формы и любого уровня, память таблицы — классы вместо пар. Дороже lookup'а
  из-за пересчёта h на класс; оптимален, когда префикс-кэш глубины d не помещается.

**Безопасность прунинга.** Тождество биективно и не зависит от генерации: oracle
считает все покрывающие прогулки независимо от того, порождались ли они фазой
генерации префиксов и как их резал прунер.

**Стоимость.** Одно сравнение `CountBits(st) == stopLevel` на узел count-DFS; lookup'и — только
на уровне досрочного завершения: маска `U` нормализуется один раз на узел
(`oracle.ShapeCtx`/`Prepare`, без аллокаций), для каждого соседа `u` — аргмин по готовым
образам (`GetPrepared`) + чтение шарда.

**Единый горячий DFS (`dfs`) — по маскам, без колбэков:**

Полный подсчёт и генерация префиксов выполняются одним рекурсивным методом; различие
сводится к базовому случаю:

```go
// rev != nil включает досрочное завершение count-DFS (см. раздел выше).
// res — аккумулятор статистики подзадачи (writes/hits/misses/прунинг по видам);
// заменяет прежний dfsStats: тот же единственный указатель, но типизированный
// types.Result — наружу уходит без раскладки.
func (s *Searcher) dfs(ctx context.Context, st State, end, depth int, c *Cache, weight int, res *types.Result, rev *Reversal) int

// Reversal — активный компонент: владеет и конфигурацией досрочного завершения,
// и всей логикой подсчёта обратных путей. Поля неэкспортируемы; конструируется
// только фабриками newOracleReversal/newCacheReversal (nil = режим выключен,
// валидация параметров живёт там же).
type Reversal struct {
    graph     *graph.Graph      // neighborMask для кандидатов u ∈ U
    sym       *symmetry.Symmetry // канонизация маски для legacy-кэша
    oracle    *oracle.Oracle    // если != nil — h из мемоизации по классам форм
    cache     *cache.Cache      // иначе — legacy W/orbitSize из префикс-кэша
    stopLevel int               // при CountBits(st) == stopLevel — lookup вместо спуска
}

// Completions отвечает f(T,end) на уровне stopLevel: Σ h(U,u) по соседям u ∈ U.
// Выбор источника — nil-check'ом (без интерфейсов): oracle нормализует маску
// один раз на узел через Prepare/GetPrepared; legacy-кэш — TransformStates +
// CanonicalFromStates и W/orbitSize на запись. В legacy-ветке каждый lookup
// помечает попадание/промах в res (CacheHits/CacheMisses) — счёт у вызывающего,
// конкурентно-безопасный без атомиков.
func (r *Reversal) Completions(unvisited State, end int, res *types.Result) int
```

1. Базовый случай: `CountBits(st) >= depth` → если `c != nil`, сохранить
   `path.New(st, end)` в кэш с весом `weight` и инкрементировать `res.CacheWrites`; вернуть 1
2. Досрочное завершение: `rev != nil && CountBits(st) == rev.stopLevel` →
   `return rev.Completions(unvisited, end, res)` (весь подсчёт обратных путей — внутри Reversal)
3. `unvisited := fullMask &^ state`; кандидаты: `neighborMasks[end] & unvisited`
4. Перебор кандидатов через итератор `for n := range cand.AllVisited()` (без прямых битовых операций)
5. Если после хода остались непосещённые и `pruner.ShouldPruneAfterVisit(n, newUnvisited)`
   вернул prune → `res.CountPrune(reason)` и continue
6. Рекурсия, сумма результатов — возвращаемое значение

Вызовы:
- полный подсчёт (`CountPathsDFS`): `depth = totalCells`, `c = nil`, `rev = nil`;
- полный подсчёт с реверсом (`CountPathsWithReversal`/`CountPathsWithCacheReversal`): то же +
  `rev != nil` (counter включает его, когда stopLevel достижим; иначе уровень просто не встречается);
- префиксы (`GenerateSubtasks`): `depth = precomputeDepth`, `c != nil`, `rev = nil` — возврат
  из базового случая игнорируется, спуск ниже `depth` не происходит;
- догенерация (`ExtendSubtask`): старт от канонической записи промежуточного кэша,
  `depth = precomputeDepth`, `c != nil`, `rev = nil`, `weight` — вес записи.

Проверка отмены контекста — на входе в каждый рекурсивный вызов (`ctx.Err() != nil`).

**Без аллокаций в горячем цикле:** `unvisited` строится через `st.Invert(totalCells)`, кандидаты —
через `graph.GetNeighborMask(end).Intersect(unvisited)`; перебор — итератором `AllVisited()`.
Ни колбэков, ни сканов доски внутри рекурсии нет. Цена унификации — лишние
параметры (`depth`, `weight`, `rev`) и nil-check'и на узел.

## Экспериментальные прунинги (результат замеров на 5×5/6×6, НЕ реализованы)

### Цветовой (бипартитный) prune — бесполезен

Идея: граф коня двудольный, проверять баланс цветов оставшихся клеток. Замер
(отладочная версия со счётчиками): **0 срабатываний**. Инвариант выполняется
автоматически в любом DFS-узле, доходимом легальными ходами (путь чередует цвета),
а для нечётных досок стартовые клетки нужного чёта уже отсекает `SholdSkip`.

### Warnsdorff-порядок кандидатов — не реализован

Идея: перебор кандидатов по возрастанию свободных выходов. Замер: **число узлов дерева
не меняется вообще** (dead-end prune локален и не зависит от порядка), а время растёт
на ~10–20% из-за сортировки на каждом узле. Реализация удалена как бесполезная.


## Типы данных

### Path (задача поиска)

```go
type Path struct {
    state State  // битовая маска посещенных клеток
    end   uint8  // текущая позиция (uint8)
}

func New(state State, end int) Path

func (p Path) State() State
func (p Path) End() int
func (p Path) String() string
```

Поле `start` удалено: число продолжений из состояния зависит только от
`(state, end)`, а вклад стартовой орбиты перенесён в вес записи кэша.

### Result — аккумулятор статистики подзадачи

```go
type Result struct {
    TotalPathsFound int
    CacheHits, CacheMisses          int
    Pruned                          int
    PrunedDeadEnd, PrunedNoCont     int
    PrunedDisconn, PrunedEndpoints  int
    CacheWrites                     int
}

func (r *Result) Add(other Result)
func (r *Result) CountPrune(reason pruner.Reason) // горячий путь: один инкремент за prune
func (r *Result) Finalize()                       // Pruned = Σ по видам, один раз на возврат
```

Полное описание полей — types.md. Публичные методы вызывают `Finalize()` перед
возвратом; внутри DFS `Pruned` не поддерживается (лишний store на каждом prune).

Горячий `dfs` агрегирует метрики через указатель на аккумулятор (`*types.Result`)
— единственный параметр вместо прежнего внутреннего `dfsStats` (тип удалён):
публичные методы (`GenerateSubtasks`, `ExtendSubtask`, `CountPathsWithReversal`,
`CountPathsDFS`) создают локальный `var result types.Result`, передают его в
`dfs`/`Completions` и возвращают наружу. За счёт этого **прунинг считается в обеих
фазах**: и на генерации префиксов, и на подсчёте; hits/misses — на legacy-reversal
lookup'ах фазы counting.

## Использование в Counter

```go
// Генерация подзадач через кэш:
cache := cache.NewCache(c.symmetry)
result := c.searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)

// Параллельный подсчет из кэша:
total := atomic.Uint64{}
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) {
    result := c.searcher.CountPathsDFS(ctx, p)
    // weight уже содержит Σ count·orbitSize по всем внесшим вклад группам
    total.Add(uint64(result.TotalPathsFound) * uint64(weight))
})
```

## Прунинг

В горячем DFS используется `pruner.ShouldPruneAfterVisit(last, unvisited)`: сначала
дешёвый локальный dead-end за O(deg(last)) (изолированной после посещения `last` могла
стать только клетка из её непосещённых соседей), затем — глобальные проверки на битовых
масках: связность `G[unvisited]`, обязательный сосед у `last` и эвристика концов
(вершины степени ≤1). Подробности — в `pruner.md`.

## Тесты

```go
func TestSearcherCountPaths(t *testing.T) {
    graph := graph.New(5)
    sym := symmetry.NewSymmetry(5)
    searcher := searcher.NewSearcher(graph, sym)
    
    result := searcher.CountPaths(context.Background(), 0)
    
    require.Equal(t, 1728/4, result.TotalPathsFound) // угол → орбита размера 4
}

func TestSearcherGenerateSubtasks(t *testing.T) {
    graph := graph.New(5)
    sym := symmetry.NewSymmetry(5)
    searcher := searcher.NewSearcher(graph, sym)
    
    cache := cache.NewCache(sym)
    result := searcher.GenerateSubtasks(context.Background(), cache, 0, 4, 3)
    
    require.Greater(t, result.CacheWrites, 0)
}

func TestSearcherDeadEndPrune(t *testing.T) {
    graph := graph.New(5)
    p := pruner.New(graph)

    // После посещения 7 клетка 0 изолирована среди непосещённых
    unvisited := state.NewState(0, 2, 4, 6)

    shouldPrune, reason := p.ShouldPruneAfterVisit(7, unvisited)
    require.True(t, shouldPrune)            // есть изолированная клетка
    require.Equal(t, pruner.DeadEnd, reason)
}
```

## Заключение

Searcher — центральный компонент, объединяющий DFS backtracking с Dead-end pruning. Канонизация путей через Symmetry позволяет эффективно использовать Cache для мемоизации результатов.
