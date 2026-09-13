# searcher: DFS по битовым маскам и генерационные фазы

## Ответственность

Чистый слой обхода: DFS backtracking по битмаскам с прунингом, генерационные фазы конвейера
(specs/counter.md) и count-DFS с ранним стопом в общий task-cache. Сам внутри мемо-таблиц не
держит — таблицы принадлежат вызывающему контуру.

## Публичный API

```go
func NewSearcher(g *graph.Graph, sym *symmetry.Symmetry) *Searcher

// Фаза A: DFS от канонического старта до глубины depth; на листьях — эмиссия
// Canonicalize(state,end) с весом orbitSize в промежуточный аккумулятор.
// SholdSkip(start) → пустой результат (фильтр чётности нечётных досок).
func (s *Searcher) GenerateRoots(ctx context.Context, sink *cache.LocalSink,
    start int, orbitSize uint64, depth int) types.Result

// Генерация task-cache: тот же спуск, что dfsGen, но запись — канонический
// префикс напрямую в cache.Cache (без sink). SholdSkip(start) → пустой результат.
func (s *Searcher) GenerateTasks(ctx context.Context, c *cache.Cache,
    start int, orbitSize uint64, depth int) types.Result

// Продолжение task-генерации от канонической записи p с весом weight до depth;
// запись уже на depth (или глубже) пишется как есть (вырожденная фаза B).
func (s *Searcher) ExtendTask(ctx context.Context, c *cache.Cache,
    p path.Path, weight uint64, depth int) types.Result

// Число полных дополнений p с ранним стопом на уровне totalCells-d: остаток
// U = full\T answering'ется суммой W(canon(U,u))/orbitSize по u ∈ N(t)∩U из c.
// c == nil или 2d > totalCells → полный спуск без обращения.
func (s *Searcher) CountPathsWithCacheReversal(ctx context.Context, p path.Path,
    c *cache.Cache, d int) types.Result
```

## Горячий DFS: рекурсивные методы

Спуск общий (кандидаты, прунинг, `ctx.Err()`); различие — **в базовом случае**:

| | `dfsGen` (фаза A) | `dfsTask` (генерация task-cache) | `dfsCount` (count с reversal) |
|---|---|---|---|
| Стоп | `CountBits(st) >= depth` | `CountBits(st) >= depth` | `bits == stopLevel` — ответ через `completions`; `bits >= totalCells` → 1 |
| Действие на стопе | эмиссия `Canonicalize(st,end)` в sink с весом `orbitSize` | `c.Set(Canonicalize(st,end), weight)` | Σ по `u ∈ N(end)∩U`: `Get(canon(U,u))`, hit → `+ w/orbitSize`, промах → 0 |
| Статистика | `CacheWrites` | `CacheWrites` | `CacheHits`/`CacheMisses` на каждую проверку кэша, `TotalPathsFound` — число дополнений |

Спуски не объединяются колбэком: рекурсия с function-параметром не инлайнится, а базовый
случай — единственная разница; дублирование цикла дешевле.

`completions` корректен из дуальности обращения тура: каждое дополнение `(T,t)` обращается в
суффикс, покрывающий ровно `U = full\T` и кончающийся соседом `t`, поэтому
`f(T,t) = Σ_{u∈U, u~t} h(U,u)`; веса task-cache — суммы орбит канонических префиксов, отсюда
деление на `orbitSize`. Канонизация `(U,u)` на уровне стопа — групповой приём
`TransformStates`/`CanonicalFromStates` (общая маска для всех концов).

Общее: `ctx.Err()` на входе; `unvisited := st.Invert(totalCells)`; кандидаты
`GetNeighborMask(end).Intersect(unvisited)`, перебор `AllVisited()`; прунинг
`ShouldPruneAfterVisit(n,newUnvisited)` с `res.CountPrune(reason)`. Без аллокаций в горячем
цикле.

## Инварианты и корректность

- `dfsGen` отвечает за **размещения** (сколько раз встретился префикс с точностью до D4).
- `GenerateTasks`/`ExtendTask` пишут канонические префиксы глубины `depth`; `ExtendTask` при
  `CountBits(p.State()) >= depth` пишет сам `p` с весом `weight` — иначе вырожденная фаза B
  (precomputeDepth ≤ base) теряла бы записи; ниже порога спуск продолжается и листья пишутся
  канонизованными. `SholdSkip` в `ExtendTask` не проверяется: корни отфильтрованы фазой A.
- Тождество count-фазы: `total = Σ_tasks W(task) · f(task)`, где `f` — count-DFS со стопом
  на уровне `totalCells − d`; мемо отвечает по **точному** состоянию, деление `W/orbitSize`
  точно, т.к. вес ключа — сумма размеров орбит (ADR-011).

## Ограничения и edge cases

- `depth = 0` в `GenerateRoots` — одна эмиссия (сам старт).
- Отклонённые эвристики (цветовой прунинг, Warnsdorff) не реализованы — ADR-009.

## Тесты

`searcher/searcher_test.go`: независимый brute-force == 1728 на 5×5; `GenerateRoots` с
`depth=totalCells` → сумма весов по группам == эталон; эмиссии = префиксы глубины depth
(веса кратно орбите), `depth=0` — одна запись. Reversal: таблично по всем допустимым d на 5×5 —
`Σ w·CountPathsWithCacheReversal(task)` по task-cache из `GenerateTasks` == brute-force;
вырожденный `ExtendTask(bits==depth)` пишет запись как есть; `c == nil` и `2d > totalCells` →
тождество полному спуску; hits+misses == числу проверок кэша (hit'ы только на уровне стопа).

## Связанные

ADR-005, ADR-009, ADR-011, ADR-016; `specs/pruner.md`, `specs/cache.md`.
