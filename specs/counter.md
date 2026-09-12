# counter: оркестрация подсчёта

## Ответственность

Высокоуровневый контур class-mode пайплайна (ADR-001): генерация префиксов двумя фазами и
финальный DP-проход по классам форм, параллельно, с учётом симметрий и мониторингом.
Единственный режим подсчёта.

## Публичный API

```go
const TwoPhaseBaseDepth = 5                 // глубина промежуточного аккумулятора фазы A

func DefaultPrecomputeDepth(size int) int   // {5:6, 6:10, 7:20, 8:14}; fallback TwoPhaseBaseDepth+1

func NewCounter(g *graph.Graph) *Counter

// Параллельный подсчёт всех открытых туров; разрез meet-in-the-middle на precomputeDepth
// (валидируется в main.go в [1, size²/2]). Суммирование — atomic.Uint64.
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor,
    workers, precomputeDepth int) uint64

// Обёртка: глубина по умолчанию для текущей доски.
func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64

// Диагностика/эксперименты:
func (c *Counter) SetShapeFilter(mask pruner.L2Checks)  // pre-DP фильтр (ADR-008)
func (c *Counter) SetTailMemo(k, slots int)             // persistent tail-мемо counting (план 03)
func (c *Counter) SetL2(mask pruner.L2Checks, minTodo int)
func (c *Counter) SetShapeDump(fn func(shape state.State, ends []int, allZero bool))
```

## Алгоритм (три фазы)

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

## Инварианты и корректность

- Каждая запись глубины `a` проходит ровно через один канонический ключ (D4-эквивариантность
  графа/прунера); эмиссия M — точная перегруппировка `total = Σ_C h(C)·M(C)`.
- Общий счёт — `atomic.Uint64`; аккумуляторы шардированы; sinks не разделяются между
  горутинами. Итог не зависит от числа воркеров и глубины разреза (тест инвариантности).
- Глубина разреза сверху ограничена `size²/2` размером M (выше — экспоненциальный рост).

## Ограничения и edge cases

- Память: главный потребитель — аккумулятор M; истинный пик в конце gen B. Подробности —
  ADR-003/004/010.
- Метрики форм публикуются безусловно: `monitor.ReportShapeStats(classes, shapes, zeroShapes)`.

## Тесты

`counter/counter_test.go`: итог на всех допустимых глубинах == эталон (1728 / 6 637 920);
sequential == parallel; инвариантность к числу воркеров; `TestDefaultPrecomputeDepth`;
веса промежуточных записей кратны орбите и останавливаются на base-глубине; tail-мемо ==
эталон; counting публикует прунинг и shape-статы.

## Связанные

ADR-001, ADR-010; `specs/searcher.md`, `specs/cache.md`, `specs/shapecount.md`,
`specs/monitoring.md`. Методология замеров — `specs/benchmarks.md`.
