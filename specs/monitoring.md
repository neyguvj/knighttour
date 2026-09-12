# Мониторинг прогресса

## Назначение

Отслеживание и отображение прогресса подсчета маршрутов в реальном времени
**с разбивкой по фазам выполнения**:
- Время выполнения (общее и каждой фазы)
- **ETA** — оценка оставшегося времени до завершения **текущей фазы**
- Количество обработанных/оставшихся задач текущей фазы
- Количество найденных путей
- Количество **эмиссий в аккумуляторы** (cache writes: префиксы gen A, классы M
  в gen B). Reversal-lookup'ов больше нет — сегмент `Hits/Misses` удалён вместе
  с legacy prefix-cache
- Количество ветвей, отсечённых прунером, **с разбивкой по видам**
  (deadend / noContinuation / disconnected / endpoints)
- Итоги классов форм (classes/shapes/zeros, где zeros — число форм с `h == 0` по
  всем концам, т.е. «бесполезных»: маршрутов внутри класса не найдено) — безусловно

Фазы запуска: `gen A` → `gen B` → `counting` (при `precomputeDepth ≤ base`
фаза B вырождается в эмиссию из промежуточных записей, но остаётся).

Статистика приходит в мониторинг **от завершённых подзадач**: контур (`counter`) после
каждой завершённой задачи вызывает `ReportSubtask(types.Result)` (см. types.md,
searcher.md). Монитор только агрегирует — внутри него нет счётчиков поиска.

## Требования

### Частота обновления
- **Интервал:** 1 секунда
- **Формат:** Строковый вывод в stdout; каждая строка начинается с ANSI-последовательности
  `\x1b[2K\r` (стереть строку + каретка в начало) — живая строка затирается целиком, без «хвостов»
- Длительности в живой строке округляются до миллисекунд (`1.234s`)
- Живая строка показывает **только активную фазу**

### Информация в отчёте

Сегмент `Writes` условен: печатается, когда в фазе были эмиссии в аккумуляторы
(gen A/gen B). Сегмент `Tail hits/lookups (pct%)` условен: печатается, когда в
фазе были обращения к persistent tail-мемо финального прохода (план 03;
hit-rate — метрика потенциала переиспользования). Так живая строка не зарастает
нулями.

```
[время] Phase [имя] | Tasks: [выполнено]/[всего] (%) | Paths [найдено] [| Writes N] [| Tail 123/456 (27%)] | Pruned [отсечено] | ETA [оценка]
```

`ETA` — оставшееся время **текущей фазы**, линейная оценка по средней скорости фазы:
`elapsed_phase * (total - completed) / completed`.

- `completed == 0` или `total == 0` → `ETA --` (оценка неизвестна, «бесконечность»; ASCII вместо `∞`)
- `completed >= total` → `ETA 0s`

**Примеры:**
```
[1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334 | ETA 3.953s
[2.100s] Phase counting | Tasks: 500/95224 (0.5%) | Paths 3200000 | Pruned 88123 | ETA 12.400s
```

### Финальный отчёт

Отдельная строка на каждую фазу + итоги классов форм + итоги. Разбивка прунинга —
в скобках по видам (только ненулевые виды).

```
=== Final ===
Total time: 63ms
Phase generation [41ms]: tasks 6/6 | paths 0 | writes 2795 | pruned 12034 (deadend 8211, nocont 302, disconn 2901, endpoints 620)
Phase counting [22ms]: tasks 57457/57457 | paths 6637920 | pruned 18823 (deadend 12001, disconn 5900, endpoints 922)
Shapes: classes=250054 shapes=57457 zeros=49880
Total paths: 6637920
```

`Shapes:` печатается **безусловно** (без env-флага) — class mode единственный.

## Интерфейс Monitor

```go
type Monitor interface {
    Start(ctx context.Context)
    Finish()
    BeginPhase(name string)        // начать новую фазу (см. ниже — только между фазами)
    AddTasks(count int)            // задачи активной фазы
    ReportTaskCompleted()          // задача активной фазы завершена
    ReportPathsFound(count int)    // пути (в counting — взвешенные weight'ом)
    ReportSubtask(r types.Result)  // статистика завершённой подзадачи: writes,
                                   // разбивка прунинга по видам
    ReportShapeStats(classes, shapes, zeroShapes int) // один раз после финального прохода (class mode)
}
```

`ReportSubtask` — единственный репорт пер-подзадачной статистики (заменил
`ReportCacheWrites`/`ReportPruned`); `TotalPathsFound` из `Result` им не
используется: в counting пути публикуются умноженными на вес записи кэша через
`ReportPathsFound`.

### Контракт BeginPhase

Фазы выполняются **последовательно**: `BeginPhase` вызывается контуром
(`counter`) строго между фазами — когда воркеры предыдущей фазы уже завершены
(`errgroup.Wait()` / `Cache.Each` вернулись). Поэтому:
- добавление элемента в `phases` и смена активной фазы не конкурентны с репортами;
- репорты воркеров читают активную фазу через `atomic.Pointer` — без мьютексов.

Репорты до первого `BeginPhase` (активной фазы нет) — no-op.

### Общее ядро monitor

`RealMonitor` и `FakeMonitor` исполняют **один и тот же код**: всё накопление,
ведение фаз и логика отчётов живут в неявляемом ядре `monitor`, а различие
реализаций — ровно одно поле-флаг `verbose` (печатать или нет посекундный и
финальный отчёт).

```go
type phaseStats struct {
    name        string
    startTime   time.Time // начало фазы (для тайминга в финальном отчёте)
    endTime     time.Time // конец фазы (фиксируется при BeginPhase следующей или Finish)
    tasks           atomic.Uint64
    completed       atomic.Uint64
    subtasks        atomic.Uint64 // свёрнутых ReportSubtask (в живую строку не печатается)
    pathsFound      atomic.Uint64 // найденные пути (только counting, взвешенные)
    cacheWrites     atomic.Uint64 // эмиссии в аккумуляторы (gen A / gen B)
    prunedDeadEnd   atomic.Uint64 // мёртвые концы / изолированные клетки
    prunedNoCont    atomic.Uint64 // нет продолжения у last
    prunedDisconn   atomic.Uint64 // несвязный остаток
    prunedEndpoints atomic.Uint64 // эвристика degree-1 концов
    prunedArtic     atomic.Uint64 // L2 сочленения (только counting)
    prunedChain     atomic.Uint64 // L2 обязательные цепочки (только counting)
}

// monitor — общее ядро обоих мониторингов.
type monitor struct {
    verbose bool // false => ни живой строки, ни финального отчёта (FakeMonitor)

    startTime time.Time
    started   atomic.Bool
    active    atomic.Pointer[phaseStats] // активная фаза
    phasesMu  sync.Mutex                 // защищает срез phases от append
    phases    []*phaseStats              // порядок следования фаз

    shapeSet     atomic.Bool   // ReportShapeStats вызывался
    shapeClasses atomic.Uint64 // записей аккумулятора M
    shapeShapes  atomic.Uint64 // различных форм (задач финального прохода)
    shapeZeros   atomic.Uint64 // форм с h == 0 по всем концам
}

type RealMonitor struct{ monitor } // verbose: true
type FakeMonitor struct{ monitor } // verbose: false
```

Все счётчики используют `atomic.Uint64` для потокобезопасного доступа без мьютексов.
Суммарный `pruned` фазы — сумма четырёх видов (не отдельный счётчик).

### Методы

#### 1. NewMonitor() *RealMonitor / NewFakeMonitor() *FakeMonitor

Оба конструктора инициализируют общее ядро (параметры не нужны, фаз ещё нет) и
отличаются только `verbose`: `true` у `RealMonitor`, `false` у `FakeMonitor`.

#### 2. Start(ctx context.Context)

Фиксирует `startTime`/`started` **всегда** (у фейка — чтобы `Finish` тоже работал
по общему пути), а горутину периодического отчёта каждую секунду (ticker +
`ctx.Done()`; при остановке/отмене — последний `report()`) заводит только при
`verbose`.

```go
func (m *monitor) Start(ctx context.Context) {
    m.startTime = time.Now()
    m.started.Store(true)
    if !m.verbose {
        return // FakeMonitor: только накопление, без тикера и вывода
    }
    go m.loop(ctx)
}
```

#### 3. BeginPhase(name string)

Фиксирует `endTime` предыдущей фазы (если была), создаёт новую, делает её
активной. Вызывается **только между фазами** (см. контракт). Фазы складываются в
срез — повторный `BeginPhase("gen A")` даёт **новую** запись, а не переиспользует
старую (одинаково для обоих реализаций).

#### 4. report()

Строка активной фазы (без перевода строки — `\x1b[2K\r` затирает предыдущий вывод);
при `verbose == false` или отсутствии активной фазы — no-op. Деление на ноль
защищено (`tasks == 0` → 0%, `ETA --`).

```go
const clearLine = "\x1b[2K\r" // erase line + каретка в начало

// estimateRemaining — линейная оценка оставшегося времени фазы по её средней скорости.
func estimateRemaining(elapsed time.Duration, completed, total uint64) (time.Duration, bool)

// fmtDur — формат длительности с точностью до миллисекунды ("1.234s").
func fmtDur(d time.Duration) string { return d.Round(time.Millisecond).String() }

func (m *monitor) report() {
    if !m.verbose {
        return
    }
    ph := m.active.Load()
    if ph == nil {
        return
    }
    // ... tasks/pct/ETA — как раньше ...

    var b strings.Builder
    fmt.Fprintf(&b, "[%s] Phase %s | Tasks: %d/%d (%.1f%%) | Paths %d",
        fmtDur(time.Since(m.startTime)), ph.name, completed, totalTasks, pct, ph.pathsFound.Load())
    if w := ph.cacheWrites.Load(); w > 0 {
        fmt.Fprintf(&b, " | Writes %d", w)
    }
    fmt.Fprintf(&b, " | Pruned %d | ETA %s", ph.prunedTotal(), eta)

    fmt.Print(clearLine + b.String())
}
```

#### 5. AddTasks / ReportTaskCompleted / ReportPathsFound / ReportSubtask

Все — `Add` в счётчик **активной** фазы; при отсутствии активной фазы — no-op.
`subtasks` считается всегда (в консоль не печатается) — это метрика для
`FakeMonitor`:

```go
func (m *monitor) ReportSubtask(r *types.Result) {
    ph := m.active.Load()
    if ph == nil {
        return
    }
    ph.subtasks.Add(1)
    ph.cacheWrites.Add(uint64(r.CacheWrites))
    ph.prunedDeadEnd.Add(uint64(r.PrunedDeadEnd))
    ph.prunedNoCont.Add(uint64(r.PrunedNoCont))
    ph.prunedDisconn.Add(uint64(r.PrunedDisconn))
    ph.prunedEndpoints.Add(uint64(r.PrunedEndpoints))
    ph.prunedArtic.Add(uint64(r.PrunedArticulation))
    ph.prunedChain.Add(uint64(r.PrunedForcedChain))
    ph.filteredShapes.Add(uint64(r.FilteredShapes)) // plan 02, отдельно от pruned*
}
```

Счётчик `filteredShapes` (фазы counting) — формы, убитые pre-DP фильтром
реализуемости (план 02); в финальной сводке печатается отдельным сегментом
`| filtered N` и **не входит** в `pruned` (исторические метрики прунинга
сопоставимы).

#### 6. ReportShapeStats(classes, shapes, zeroShapes int)

Вызывается один раз после финального прохода class mode (см. counter.md):
`classes` — записей аккумулятора M, `shapes` — различных форм (задач прохода),
`zeroShapes` — форм с `h == 0` по всем концам. Вызов идемпотентен последним
(Store + `shapeSet=true`).

#### 7. Finish()

Общий путь у обеих реализаций: закрыть активную фазу и (только при `verbose`)
напечатать финальный отчёт с переводом строки — тайминг и метрики каждой фазы,
итоги классов форм, общее число путей. У фейка вызов так же валиден: он лишь
фиксирует `endTime` последней фазы.

```go
func (m *monitor) Finish() {
    if !m.started.Swap(false) {
        return
    }
    m.report() // no-op без verbose
    if prev := m.active.Load(); prev != nil {
        prev.endTime = time.Now()
    }
    if !m.verbose {
        return
    }

    fmt.Printf("\n=== Final ===\n")
    fmt.Printf("Total time: %s\n", time.Since(m.startTime))

    var totalPaths uint64
    for _, ph := range m.phases {
        // строка фазы: tasks/paths + условные writes + pruned с разбивкой
        fmt.Printf("Phase %s [%s]: %s\n", ph.name, ph.endTime.Sub(ph.startTime), ph.summary())
        totalPaths += ph.pathsFound.Load()
    }
    if m.shapeSet.Load() {
        fmt.Printf("Shapes: classes=%d shapes=%d zeros=%d\n",
            m.shapeClasses.Load(), m.shapeShapes.Load(), m.shapeZeros.Load())
    }
    fmt.Printf("Total paths: %d\n", totalPaths)
}
```

`phaseStats.summary()` — общая сборка строки фазы для финального отчёта
(сегменты условны так же, как в живой строке; разбивка pruned перечисляет только
ненулевые виды в фиксированном порядке `deadend, nocont, disconn, endpoints,
artic, chain`: `pruned 12034 (deadend 8211, nocont 302, disconn 2901, endpoints 620)`;
`artic`/`chain` появляются только в фазе counting — их заполняет L2 из shapecount).

## Типы данных

### Result (types)

См. types.md — единый носитель пер-подзадачной статистики
(`CacheWrites`, `Pruned*` по видам), который контур кладёт в `ReportSubtask`.

## Использование в Counter

### Генерация (фаза A / фаза B)

```go
// фаза A: по одной горутине на стартовую группу;
result := c.searcher.GenerateRoots(ctx, sink, p, group.OrbitSize, baseDepth)
monitor.ReportSubtask(result) // writes + разбивка прунинга — одним вызовом
monitor.ReportTaskCompleted()

// фаза B: чанк-воркеры тянут записи снапша атомарным индексом;
result := c.searcher.ExtendToClasses(gctx, sink, e.Path, e.Weight, precomputeDepth)
monitor.ReportSubtask(result)
monitor.ReportTaskCompleted()
```

### Подсчёт (финальный проход по формам)

```go
monitor.BeginPhase("counting")
// воркеры атомарно разбирают индексы шардов; каждый свой срез группирует сам:
entries := acc.DrainShard(i)
tasks := groupSorted(entries) // сортировка по State внутри шарда
monitor.AddTasks(len(tasks))  // инкрементально, по мере группировки

var stats types.Result
hs := sc.CountShape(e[0].Path.State(), ends, &stats) // прунинг DP — в stats
contribution := Σ hs[j]·e[j].Weight
totalPaths.Add(contribution)
monitor.ReportPathsFound(int(contribution))
monitor.ReportSubtask(stats)     // разбивка pruned фазы counting
monitor.ReportTaskCompleted()

// безусловно (без env):
monitor.ReportShapeStats(classes, len(tasks), zeroShapes)
```

## Связанные спеки

Описание CLI и обработки Ctrl+C (graceful shutdown в `main.go`) — см. `specs/main.md`.

## FakeMonitor (для тестов и бенчмарков)

Это то же общее ядро `monitor` с `verbose == false`: накопление, фазы и контракты
идентичны `RealMonitor`, нет только посекундного тикера и финального отчёта.
Тесты/бенчмарки читают накопленное через `Phase(name)` / `Totals()` / `ShapeStats()`.
Отчёты без активной фазы (до первого `BeginPhase`) игнорируются — как у `RealMonitor`.

```go
// PhaseStats – снимок счётчиков фаз, зеркало живой/финальной строки.
type PhaseStats struct {
	Tasks, Completed, Subtasks, PathsFound, CacheWrites uint64
	Pruned                                              uint64 // сумма видов
	PrunedDeadEnd, PrunedNoCont                         uint64
	PrunedDisconn, PrunedEndpoints                      uint64
	PrunedArticulation, PrunedForcedChain               uint64 // L2 (только counting)
	FilteredShapes                                      uint64 // pre-DP фильтр форм (только counting, план 02)
	Duration                                            time.Duration // 0, если фаза не закрыта
}

type FakeMonitor struct{ monitor } // verbose: false

func NewFakeMonitor() *FakeMonitor

// Доступ для проверок (определён на ядре, доступен и у RealMonitor):
func (m *monitor) Phase(name string) PhaseStats // сумма одноимённых фаз; нулевой снимок если их не было
func (m *monitor) Totals() PhaseStats           // сумма по всем фазам
func (m *monitor) ShapeStats() (classes, shapes, zeros uint64)
```

`Phase(name)` **суммирует** все фазы с таким именем (`BeginPhase` всегда создаёт
новую запись), `Totals()` — сумму по всем фазам, `ShapeStats` — last-write-wins.
Снимки аддитивны и не сбрасываются: переиспользование одного монитора на N прогонов
даёт N-кратные суммы счётчиков, поэтому для метрик «на прогон» мониторинг создают
заново (как в `BenchmarkCountAllToursClass`).

Назначение снимков `FakeMonitor`: тесты и бенчмарки читают счётчики фаз (tasks,
subtasks, writes, pruned) и `ShapeStats` — например, классы M как оценку памяти class
mode (~24 Б на запись, payload без оверхеда map). Бенчмарк публикует их через
`b.ReportMetric` **с разбивкой по фазам**, см. counter.md.

Тайминг фаз (`PhaseStats.Duration`) фиксируется только при закрытии фазы:
`BeginPhase` следующей или `Finish()`. Поэтому для чтения длительности **последней**
фазы читатель обязан вызвать `Start(ctx)` + `Finish()` (у фейка оба тихие, оверхед —
два `time.Now`).


## Требования к точности

1. **Пути:** Счётчик путей должен быть точным (атомарные операции обеспечивают потокобезопасность)
2. **Задачи:** Количество выполненных задач — целые числа, считаются отдельно на фазу
3. **Время:** Показания времени могут иметь погрешность ±1 секунда из-за интервала таймера; тайминги фаз точные (фиксируются в BeginPhase/Finish); длительности в живой строке округлены до миллисекунд
4. **ETA:** Линейная оценка по средней скорости активной фазы; `--` пока ни одна задача не завершена, `0s` когда фаза завершена; погрешность — ±интервал тикера и неравномерность скоростей задач
5. **Writes:** число эмиссий в аккумуляторы за фазу (не уникальные ключи)

## Обработка ошибок

- Нет критических зависимостей от мониторинга
- Остановка мониторинга не влияет на результат подсчёта
- Потеря отчётов в тикере не приводит к сбоям
- Отмена контекста корректно останавливает горутину мониторинга
- Репорты без активной фазы — no-op (нет паники)

## Производительность

- Overhead мониторинга: <1% (атомарные операции имеют минимальный contention)
- Мьютексы не нужны для обновления счётчиков (один mutex только на append фазы)
- `subtasks` — один атомарный инкремент на завершённую подзадачу (не на шаг DFS),
  поэтому на hot path не влияет
- Таймер с таймаутом не накапливает задержки
- Финальный отчёт выводится один раз при завершении

## Тестирование

`monitoring/monitor_test.go`:
- компилируемые проверки реализации интерфейса `RealMonitor`/`FakeMonitor`;
- репорты без активной фазы — no-op;
- агрегация счётчиков по фазам (BeginPhase переключает накопление);
- `ReportSubtask` раскладывает `types.Result` по видам прунинга и hit/miss;
- `estimateRemaining` — табличные тесты: нет завершённых/нет всего → «бесконечность»,
  фаза завершена → 0, линейный пересчёт;
- формат живой строки (перехват stdout): `\x1b[2K\r`, точность до мс, `ETA --` / `ETA 0s`,
  условность сегмента `Writes`;
- формат финального отчёта: разбивка pruned по видам (только ненулевые), секция
  `Shapes:` только после `ReportShapeStats`;
- **общность кода**: один и тот же сценарий репортов прогоняется над `NewMonitor()`
  и `NewFakeMonitor()`, снимки `Totals()`/`Phase(name)` совпадают; при этом stdout
  фейка пуст (ни живой строки, ни финального отчёта), у реального — непустой;
- повторный `BeginPhase` с тем же именем — новая фаза, `Phase(name)` суммирует;
- конкурентные репорты под `-race`.
