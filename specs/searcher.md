# searcher: DFS по битовым маскам и генерационные фазы

## Ответственность

Чистый слой обхода: DFS backtracking по битмаскам с прунингом, генерационные фазы обоих
режимов подсчёта (specs/counter.md) и count-DFS reversal-режима с ранним стопом в общий
task-cache. Сам внутри мемо-таблиц не держит — таблицы принадлежат вызывающему контуру.

## Публичный API

```go
func NewSearcher(g *graph.Graph, sym *symmetry.Symmetry) *Searcher

// Фаза A: DFS от канонического старта до глубины depth; на листьях — эмиссия
// Canonicalize(state,end) с весом orbitSize в промежуточный аккумулятор.
// SholdSkip(start) → пустой результат (фильтр чётности нечётных досок).
func (s *Searcher) GenerateRoots(ctx context.Context, sink *cache.LocalSink,
    start int, orbitSize uint64, depth int) types.Result

// Фаза B: спуск от записи p до depth; на листьях U = full\T и для каждого
// u ∈ N(t)∩U — эмиссия KeyFromPrepared(U,u) в аккумулятор M с весом weight.
func (s *Searcher) ExtendToClasses(ctx context.Context, sink *cache.LocalSink,
    p path.Path, weight uint64, depth int) types.Result

// --- reversal mode (specs/counter.md, ADR-011) --------------------------------

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

## Горячий DFS: рекурсивные методы по режимам

Спуск (кандидаты, прунинг, `ctx.Err()`, условие `CountBits(st) >= depth`) общий; различие —
**в базовом случае**:

| | `dfsGen` (фаза A) | `dfsEmit` (фаза B) |
|---|---|---|
| Смысл листа | префикс — готовая запись аккумулятора A | префикс — заготовка; считается его **дополнение** |
| Ключ эмиссии | размещение: `Canonicalize(st,end)` | форма дополнения: `U=full\T`, для каждого `u∈N(t)∩U` — `KeyFromPrepared(&sc,u)` |
| Эмиссий на лист | 1 | `|N(t)∩U|` (0…8), все с одним весом |
| Нормализация | `Canonicalize` на лист | `PrepareShape(U,&sc)` **один раз на лист** + дешёвый `KeyFromPrepared` на конец |
| Источник веса | `orbitSize` стартовой группы (сверху) | накопленный вес `W` промежуточной записи |
| Пустой `cand` | префикс эмится всё равно | **ноль эмиссий**: дополнение не замыкается, вклад 0 (семантически верно) |
| Порядок проверок | глубина до `unvisited`/`cand` | `unvisited`/`cand` до глубины (нужны в базовом случае) |

Спуски не объединяются колбэком: рекурсия с function-параметром не инлайнится, а базовый
случай — единственная разница; дублирование цикла дешевле.

Reversal mode добавляет ещё два спуска той же семьи (общие: кандидаты, прунинг, `ctx.Err()`):

| | `dfsTask` (генерация task-cache) | `dfsCount` (count с reversal) |
|---|---|---|
| Стоп | `CountBits(st) >= depth` | `bits == stopLevel` — ответ через `Completions`; `bits >= totalCells` → 1 |
| Действие на стопе | `c.Set(Canonicalize(st,end), weight)` | Σ по `u ∈ N(end)∩U`: `Get(canon(U,u))`, hit → `+ w/orbitSize`, промах → 0 |
| Статистика | `CacheWrites` | `CacheHits`/`CacheMisses` на каждую проверку кэша, `TotalPathsFound` — число дополнений |

`Completions` корректен из дуальности обращения тура: каждое дополнение `(T,t)` обращается в
суффикс, покрывающий ровно `U = full\T` и кончающийся соседом `t`, поэтому
`f(T,t) = Σ_{u∈U, u~t} h(U,u)`; веса task-cache — суммы орбит канонических префиксов, отсюда
деление на `orbitSize`.

Общее: `ctx.Err()` на входе; `unvisited := st.Invert(totalCells)`; кандидаты
`GetNeighborMask(end).Intersect(unvisited)`, перебор `AllVisited()`; прунинг
`ShouldPruneAfterVisit(n,newUnvisited)` с `res.CountPrune(reason)`. Без аллокаций в горячем
цикле (`ShapeCtx` на стеке).

## Инварианты и корректность

- `dfsGen` отвечает за **размещения** (сколько раз встретился префикс с точностью до D4),
  `dfsEmit` — за **формы дополнений**; веса невзаимозаменяемы, эмиссии dfsEmit умножаются
  на степень конца.
- `ExtendToClasses` эмитит при `CountBits(st) >= depth`, включая вырожденный вход
  `CountBits(p.State()) == depth` (генерация останавливается уже на промежуточных записях).
  `SholdSkip` не проверяется: корни отфильтрованы фазой A.
- Перегруппировка точна: `total = Σ_листьев weight·Σ_u h(U,u) = Σ_C h(C)·M(C)` (ADR-001).
- `ExtendTask` при `CountBits(p.State()) >= depth` пишет сам `p` с весом `weight` — иначе
  вырожденная фаза B (precomputeDepth ≤ base) теряла бы записи; ниже этого порога спуск
  продолжается и листья пишутся канонизованными.
- Тождество reversal mode: `total = Σ_tasks W(task) · f(task)`, где `f` — count-DFS со стопом
  на уровне `totalCells − d`; мемо отвечает по **точному** состоянию, деление `W/orbitSize`
  точно, т.к. вес ключа — сумма размеров орбит (ADR-011).

## Ограничения и edge cases

- `depth = 0` в `GenerateRoots` — одна эмиссия (сам старт).
- Отклонённые эвристики (цветовой прунинг, Warnsdorff) не реализованы — ADR-009.

## Тесты

`searcher/searcher_test.go`: независимый brute-force == 1728 на 5×5; `GenerateRoots` с
`depth=totalCells` → сумма весов по группам == эталон; `ExtendToClasses` с
`depth=totalCells-1` → тождество `Σ h(C)·M(C)`; эмиссии = префиксы глубины depth (веса кратно
орбите), `depth=0` — одна запись; вырожденный вход `bits==depth` эмитит сразу.
Reversal: таблично по всем допустимым d на 5×5 — `Σ w·CountPathsWithCacheReversal(task)` по
task-cache из `GenerateTasks` == brute-force; вырожденный `ExtendTask(bits==depth)` пишет
запись как есть; `c == nil` и `2d > totalCells` → тождество полному спуску; hits+misses ==
числу проверок кэша (hit'ы только на уровне стопа).

## Связанные

ADR-001, ADR-005, ADR-009; `specs/pruner.md`, `specs/cache.md`, `specs/shapecount.md`.
