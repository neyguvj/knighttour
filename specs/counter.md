# counter: оркестрация подсчёта

## Ответственность

Высокоуровневый контур подсчёта: двухфазная генерация префиксов в task-cache и count-фаза
с ранним стопом через мемо по точному состоянию (ADR-011, единственный конвейер — ADR-016).
Параллелизм, симметрии стартов и мониторинг — общие для фаз.

## Публичный API

```go
const TwoPhaseBaseDepth = 5                 // глубина промежуточного аккумулятора фазы A
const DefaultGCPercentReversal = 40         // GOGC на время конвейера (ADR-014)

func DefaultPrecomputeDepth(size int) int   // контрактные значения таблицы ниже;
                                            // fallback TwoPhaseBaseDepth+1 для неизвестных досок

func NewCounter(g *graph.Graph) *Counter

// Параллельный подсчёт всех открытых туров; разрез meet-in-the-middle на precomputeDepth
// (валидируется в main.go в [1, size²/2]). Суммирование — atomic.Uint64.
func (c *Counter) ParallelCountWithDepth(ctx context.Context, monitor monitoring.Monitor,
    workers, precomputeDepth int) uint64

// Обёртка: глубина по умолчанию для текущей доски.
func (c *Counter) ParallelCount(ctx context.Context, monitor monitoring.Monitor, workers int) uint64

// Диагностика:
func (c *Counter) SetGCPercent(p int)       // GOGC на время конвейера (ADR-014);
                                            // p > 0 — debug.SetGCPercent(p) на входе и
                                            // восстановление прежнего при выходе;
                                            // p == 0 — не трогать. Значение по умолчанию —
                                            // DefaultGCPercentReversal
```

Контрактные значения `DefaultPrecomputeDepth` — глобальный минимум времени на измеренном
окне глубин (критерий «чистое время» — ADR-017; замеры пересъёмки — ADR-016 §«Замеры», план 10;
`5: 6` — плато d2..d12, неразличимое над шумом повторов, смена не оценима, ADR-016;
`8: 14` — placeholder без пересъёмки, ADR-015):

| size | 5 | 6 | 7 | 8 |
|---|---|---|---|---|
| depth | 6 | 14 | 22 | 14 |

Память не ограничивает выбор глубины (пик RSS — справочная метрика, ADR-017); пониженный
GOGC на время конвейера сохраняется (ADR-014).

## Алгоритм (три фазы)

1. **gen A** — параллельно по каноническим стартовым группам (`errgroup` + `SetLimit(workers)`):
   `searcher.GenerateRoots(ctx, sink, canonical, orbitSize, a)`, где
   `a = min(precomputeDepth, TwoPhaseBaseDepth)`; воркеры пишут через свои `LocalSink`.
   `Drain()` — worklist фазы B (тысячи задач вместо ~10 групп → полная утилизация).
2. **gen B** — чанк-воркеры (`min(len(entries), workers)`, задачи тянутся атомарным
   индексом), каждая задача — `searcher.ExtendTask(...)` с записью напрямую в `cache.Cache`
   task-cache (без LocalSink: профиль записей иной, hit'ы читаются в том же ране, что
   пишутся). При `precomputeDepth ≤ TwoPhaseBaseDepth` фаза вырождается — запись самой
   записи.
3. **counting** — прямой обход task-cache без копирования (ADR-013): `taskCache.Each`
   с `workers` параллельными горутинами на шарды; колбэк для каждой записи `(task, w)` —
   `searcher.CountPathsWithCacheReversal(ctx, task, taskCache, precomputeDepth)`,
   `w · paths` складывается в общий `atomic.Uint64`. Диспатч не аллоцирует; колбэк
   работает под RLock шарда при отсутствии писателей (контракт `cache.Each`). Отмена ctx
   проверяется перед каждым шардом и каждой задачей.

Весь конвейер работает под пониженным GOGC (`SetGCPercent`, ADR-014): значение применяется на
входе и восстанавливается при выходе — пик фазы это живая task-cache, headroom над ней дорог.

## Инварианты и корректность

- Каждая запись глубины `a` проходит ровно через один канонический ключ (D4-эквивариантность
  графа/прунера); тождество итога `total = Σ_tasks W(task)·f(task)` на множестве канонических
  префиксов (ADR-011).
- Общий счёт — `atomic.Uint64`; таблицы шардированы; sinks не разделяются между горутинами.
  Итог не зависит от числа воркеров и глубины разреза (тест инвариантности).
- Глубина разреза сверху ограничена `size²/2`: точка дуальности обращения (`2d ≤ totalCells`),
  глубже — дублирование двойственного разреза.

## Ограничения и edge cases

- Task-cache живёт до конца count-фазы и не дренируется по шардам; на низких глубинах
  (большое q) счёт дорожает. Пониженный GOGC на время конвейера — штатное поведение
  по умолчанию (ADR-014).
- Две конкурентные трубы в одном процессе восстанавливают GC-процент в порядке
  «последний пишет» — значение глобально для рантайма; тесты последовательны.

## Тесты

`counter/counter_test.go`: итог на всех допустимых глубинах == эталон (1728 / 6 637 920);
sequential == parallel; инвариантность к числу воркеров; `TestDefaultPrecomputeDepth`;
веса промежуточных записей кратны орбите и останавливаются на base-глубине; counting
публикует hits/misses в счётчики фазы `counting`. GC-ручка (ADR-014): итог не зависит от
`SetGCPercent(0|40)`; после завершения конвейера процент рантайма восстановлен (значения
до/после в тесте).

## Связанные

ADR-011, ADR-013, ADR-014, ADR-016, ADR-017; `specs/searcher.md`, `specs/cache.md`,
`specs/monitoring.md`. Методология замеров — `specs/benchmarks.md`.
