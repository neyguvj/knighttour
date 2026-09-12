# searcher: DFS по битовым маскам и генерационные фазы

## Ответственность

Чистый слой обхода: DFS backtracking по битмаскам с прунингом и две генерационные фазы
class-mode пайплайна — gen A (префиксы) и gen B (классы дополнений). Пишет веса в
аккумуляторы; подсчёт живёт в shapecount. Никаких мемо-таблиц внутри.

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
```

## Горячий DFS: два рекурсивных метода

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

## Ограничения и edge cases

- `depth = 0` в `GenerateRoots` — одна эмиссия (сам старт).
- Отклонённые эвристики (цветовой прунинг, Warnsdorff) не реализованы — ADR-009.

## Тесты

`searcher/searcher_test.go`: независимый brute-force == 1728 на 5×5; `GenerateRoots` с
`depth=totalCells` → сумма весов по группам == эталон; `ExtendToClasses` с
`depth=totalCells-1` → тождество `Σ h(C)·M(C)`; эмиссии = префиксы глубины depth (веса кратно
орбите), `depth=0` — одна запись; вырожденный вход `bits==depth` эмитит сразу.

## Связанные

ADR-001, ADR-005, ADR-009; `specs/pruner.md`, `specs/cache.md`, `specs/shapecount.md`.
