# counter: оркестрация подсчёта

## Ответственность

Высокоуровневый контур подсчёта двух режимов (ADR-001, ADR-011): **class mode** — генерация
префиксов двумя фазами и финальный DP-проход по классам форм; **reversal mode** — та же
двухфазная генерация в task-cache и count-фаза с ранним стопом через мемо по точному
состоянию. Режим — переключатель контура, параллелизм/мониторинг общие.

## Публичный API

```go
const TwoPhaseBaseDepth = 5                 // глубина промежуточного аккумулятора фазы A
const DefaultGCPercentReversal = 40         // GOGC на время reversal-конвейера (ADR-014)

func DefaultPrecomputeDepth(size int) int   // {5:6, 6:10, 7:20, 8:14}; fallback TwoPhaseBaseDepth+1

type Mode int                               // режим подсчёта
const (
    ModeClass     Mode = iota               // pipeline по классам форм (дефолт)
    ModeReversal                            // task-cache + count-DFS с обращениями
)

func NewCounter(g *graph.Graph) *Counter

// Параллельный подсчёт всех открытых туров активным режимом; разрез meet-in-the-middle
// на precomputeDepth (валидируется в main.go в [1, size²/2]). Суммирование — atomic.Uint64.
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor,
    workers, precomputeDepth int) uint64

// Обёртка: глубина по умолчанию для текущей доски.
func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64

// Диагностика/эксперименты:
func (c *Counter) SetMode(m Mode)                       // режим; по умолчанию ModeClass
func (c *Counter) SetGCPercent(p int)                   // GOGC на время reversal-конвейера (ADR-014);
                                                        // p > 0 — debug.SetGCPercent(p) на входе и
                                                        // восстановление прежнего при выходе;
                                                        // p == 0 — не трогать. Значение по умолчанию
                                                        // конвейера — DefaultGCPercentReversal;
                                                        // class mode значение игнорирует
func (c *Counter) SetShapeFilter(mask pruner.L2Checks)  // pre-DP фильтр class mode (ADR-008)
func (c *Counter) SetTailMemo(k, slots int)             // tail-мемо counting class mode (план 03)
func (c *Counter) SetShapeDump(fn func(shape state.State, ends []int, allZero bool))
```

Сеттеры class-mode-специфики (`SetShapeFilter`, `SetTailMemo`, `SetShapeDump`) в reversal
mode игнорируются.

## Алгоритм class mode (три фазы)

1. **gen A** — параллельно по каноническим стартовым группам (`errgroup` + `SetLimit(workers)`):
   `searcher.GenerateRoots(ctx, sink, canonical, orbitSize, a)`, где
   `a = min(precomputeDepth, TwoPhaseBaseDepth)`; воркеры пишут через свои `LocalSink`.
   `Drain()` — worklist фазы B (тысячи задач вместо ~10 групп → полная утилизация).
2. **gen B** — чанк-воркеры (`min(len(entries), workers)`, задачи тянутся атомарным
   индексом), каждая задача — `searcher.ExtendToClasses(...)` в аккумулятор M через LocalSink.
3. **counting** — два stage'а per-shard без общего снимка M (ADR-010): grouping шардов →
   LPT-стек job'ов → dispatch батчами; для формы — `shapecount.CountShapeWithTail`,
   `total += Σ_end h·M`.

При `precomputeDepth ≤ TwoPhaseBaseDepth` фаза B вырождается (эмиссия из промежуточных
записей), но остаётся.

## Алгоритм reversal mode (те же фазы, другой финал)

1. **gen A** — идентичен class mode (промежуточный аккумулятор тех же ключей/весов).
2. **gen B** — чанк-воркеры тянут worklist gen A и зовут `searcher.ExtendTask(...)` с
   записью напрямую в `cache.Cache` task-cache (без LocalSink). Фаза вырождается так же,
   через запись самой записи.
3. **counting** — прямой обход task-cache без копирования (ADR-013): `taskCache.Each`
   с `workers` параллельными горутинами на шарды; колбэк для каждой записи `(task, w)` —
   `searcher.CountPathsWithCacheReversal(ctx, task, taskCache, precomputeDepth)`,
   `w · paths` складывается в общий `atomic.Uint64`. Диспатч не аллоцирует; колбэк
   работает под RLock шарда при отсутствии писателей (контракт `cache.Each`). Отмена ctx
   проверяется перед каждым шардом и каждой задачей.

Весь конвейер reversal работает под пониженным GOGC (`SetGCPercent`, ADR-014): значение
применяется на входе и восстанавливается при выходе — пик фазы это живая task-cache,
headroom над ней дорог. Class mode GC не трогает.

## Инварианты и корректность

- Каждая запись глубины `a` проходит ровно через один канонический ключ (D4-эквивариантность
  графа/прунера); эмиссия M — точная перегруппировка `total = Σ_C h(C)·M(C)`; task-cache
  reversal даёт `total = Σ_tasks W(task)·f(task)` на том же множестве ключей (ADR-011).
- Общий счёт — `atomic.Uint64`; аккумуляторы шардированы; sinks не разделяются между
  горутинами. Итог не зависит от числа воркеров и глубины разреза (тест инвариантности)
  и **от режима**: ModeClass == ModeReversal на всех допустимых глубинах.
- Глубина разреза сверху ограничена `size²/2`: для class mode размером M (выше —
  экспоненциальный рост), для reversal это точка дуальности обращения (`2d ≤ totalCells`).

## Ограничения и edge cases

- Память class mode: главный потребитель — аккумулятор M; истинный пик в конце gen B.
  Подробности — ADR-003/004/010.
- Память reversal mode: task-cache живёт до конца count-фазы и не дренируется по шардам;
  на низких глубинах (большое q) он дороже M — точка OOM фиксируется как результат A/B.
  Пониженный GOGC на время конвейера — штатное поведение по умолчанию (ADR-014).
- Две конкурентные reversal-трубы в одном процессе восстанавливают GC-процент в порядке
  «последний пишет» — значение глобально для рантайма; тесты последовательны.
- Метрики форм (`monitor.ReportShapeStats`) публикует только class mode.

## Тесты

`counter/counter_test.go`: итог на всех допустимых глубинах == эталон (1728 / 6 637 920)
в **обоих режимах** (таблица: mode × depth); sequential == parallel; инвариантность к числу
воркеров; `TestDefaultPrecomputeDepth`; веса промежуточных записей кратны орбите и
останавливаются на base-глубине; tail-мемо == эталон; counting class mode публикует прунинг
и shape-статы; reversal публикует hits/misses в счётчики фазы `counting` и не вызывает
ReportShapeStats. GC-ручка (ADR-014): итог reversal не зависит от `SetGCPercent(0|40)`;
после завершения конвейера процент рантайма восстановлен (значения до/после в тесте);
class mode не меняет процент.


## Связанные

ADR-001, ADR-010, ADR-011, ADR-013, ADR-014; `specs/searcher.md`, `specs/cache.md`,
`specs/shapecount.md`, `specs/monitoring.md`. Методология замеров — `specs/benchmarks.md`.
