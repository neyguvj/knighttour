# Мониторинг прогресса

## Назначение

Отслеживание и отображение прогресса подсчета маршрутов в реальном времени
**с разбивкой по фазам выполнения**:
- Время выполнения (общее и каждой фазы)
- **ETA** — оценка оставшегося времени до завершения **текущей фазы**
- Количество обработанных/оставшихся задач текущей фазы
- Количество найденных путей
- Количество **записей в кэш** (cache writes, вызовов `Set`) и их hit/miss на
  reversal-lookup'ах фазы counting (`hits (rate) misses`)
- Количество ветвей, отсечённых прунером, **с разбивкой по видам**
  (deadend / noContinuation / disconnected / endpoints)
- Итоги shape-oracle (lookups/computes/classes/zeros, где zeros — число классов с
  `h == 0`, т.е. «бесполезных»: маршрутов внутри класса не найдено) — безусловно,
  при oracle-режиме

Фазы запуска:
- `generation` — однофазная генерация подзадач (`precomputeDepth ≤ TwoPhaseBaseDepth`)
- либо `gen A` + `gen B` — двухфазная генерация
- `counting` — фаза подсчёта по записям кэша

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

Сегменты `Writes` и `Hits/Misses` условны: `Writes` печатается, когда в фазе
были записи кэша (генерация), `Hits … Misses …` — когда были lookup'и
(counting с legacy prefix-cache reversal). Так живая строка не зарастает нулями.

```
[время] Phase [имя] | Tasks: [выполнено]/[всего] (%) | Paths [найдено] [| Writes N] [| Hits N (P%) Misses M] | Pruned [отсечено] | ETA [оценка]
```

`ETA` — оставшееся время **текущей фазы**, линейная оценка по средней скорости фазы:
`elapsed_phase * (total - completed) / completed`.

- `completed == 0` или `total == 0` → `ETA --` (оценка неизвестна, «бесконечность»; ASCII вместо `∞`)
- `completed >= total` → `ETA 0s`

**Примеры:**
```
[1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334 | ETA 3.953s
[2.100s] Phase counting | Tasks: 500/95224 (0.5%) | Paths 3200000 | Hits 812340 (76.2%) Misses 253900 | Pruned 88123 | ETA 12.400s
```

### Финальный отчёт

Отдельная строка на каждую фазу + oracle-итоги + итоги. Разбивка прунинга —
в скобках по видам (только ненулевые виды). Hit-rate считается по сумме хитов и
промахов фазы; lookup'ов нет → сегмент отсутствует.

```
=== Final ===
Total time: 63ms
Phase generation [41ms]: tasks 6/6 | paths 0 | writes 2795 | pruned 12034 (deadend 8211, nocont 302, disconn 2901, endpoints 620)
Phase counting [22ms]: tasks 95224/95224 | paths 6637920 | hits 120034 (78.4%) misses 33210 | pruned 180322 (deadend 140311, disconn 30011, endpoints 1000)
Oracle: lookups=51234 computes=987 classes=654 zeros=219
Total paths: 6637920
```

`Oracle:` печатается **безусловно** (без env-флага), если oracle использовался
(`oracleDepth > 0`); в legacy-режиме секции нет.

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
                                   // hits/misses, разбивка прунинга по видам
    ReportOracleStats(lookups, computes, classes, zeros int) // один раз после counting
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

### Структура данных RealMonitor

```go
type phaseStats struct {
    name        string
    startTime   time.Time // начало фазы (для тайминга в финальном отчёте)
    endTime     time.Time // конец фазы (фиксируется при BeginPhase следующей или Finish)
    tasks         atomic.Uint64
    completed     atomic.Uint64
    pathsFound    atomic.Uint64 // найденные пути (только counting, взвешенные)
    cacheWrites   atomic.Uint64 // записи в кэш (generation)
    cacheHits     atomic.Uint64 // попадания reversal-lookup'ов (counting)
    cacheMisses   atomic.Uint64 // промахи reversal-lookup'ов (counting)
    prunedDeadEnd atomic.Uint64 // мёртвые концы / изолированные клетки
    prunedNoCont  atomic.Uint64 // нет продолжения у last
    prunedDisconn atomic.Uint64 // несвязный остаток
    prunedEndpoints atomic.Uint64 // эвристика degree-1 концов
}

type RealMonitor struct {
    startTime time.Time
    started   atomic.Bool
    active    atomic.Pointer[phaseStats] // активная фаза
    phasesMu  sync.Mutex                 // защищает срез phases от append
    phases    []*phaseStats              // порядок следования фаз

    oracleSet               atomic.Bool // ReportOracleStats вызывался
    oracleLookups, computes atomic.Uint64
    oracleClasses           atomic.Uint64
    oracleZeros             atomic.Uint64 // классы с h == 0 (бесполезные)
}
```

Все счётчики используют `atomic.Uint64` для потокобезопасного доступа без мьютексов.
Суммарный `pruned` фазы — сумма четырёх видов (не отдельный счётчик).

### Методы

#### 1. NewMonitor() *RealMonitor

Инициализация мониторинга (параметры не нужны, фаз ещё нет).

#### 2. Start(ctx context.Context)

Запуск периодического отчёта каждую секунду (ticker + `ctx.Done()`; при
остановке/отмене — последний `report()`), как раньше.

#### 3. BeginPhase(name string)

Фиксирует `endTime` предыдущей фазы (если была), создаёт новую, делает её
активной. Вызывается **только между фазами** (см. контракт).

#### 4. report()

Строка активной фазы (без перевода строки — `\x1b[2K\r` затирает предыдущий вывод);
при отсутствии активной фазы — no-op. Деление на ноль защищено (`tasks == 0` → 0%, `ETA --`).

```go
const clearLine = "\x1b[2K\r" // erase line + каретка в начало

// estimateRemaining — линейная оценка оставшегося времени фазы по её средней скорости.
func estimateRemaining(elapsed time.Duration, completed, total uint64) (time.Duration, bool)

// fmtDur — формат длительности с точностью до миллисекунды ("1.234s").
func fmtDur(d time.Duration) string { return d.Round(time.Millisecond).String() }

func (m *RealMonitor) report() {
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
    hits, misses := ph.cacheHits.Load(), ph.cacheMisses.Load()
    if hits+misses > 0 {
        fmt.Fprintf(&b, " | Hits %d (%.1f%%) Misses %d", hits, hitRate(hits, misses), misses)
    }
    fmt.Fprintf(&b, " | Pruned %d | ETA %s", ph.prunedTotal(), eta)

    fmt.Print(clearLine + b.String())
}
```

#### 5. AddTasks / ReportTaskCompleted / ReportPathsFound / ReportSubtask

Все — `Add` в счётчик **активной** фазы; при отсутствии активной фазы — no-op:

```go
func (m *RealMonitor) ReportSubtask(r types.Result) {
    ph := m.active.Load()
    if ph == nil {
        return
    }
    ph.cacheWrites.Add(uint64(r.CacheWrites))
    ph.cacheHits.Add(uint64(r.CacheHits))
    ph.cacheMisses.Add(uint64(r.CacheMisses))
    ph.prunedDeadEnd.Add(uint64(r.PrunedDeadEnd))
    ph.prunedNoCont.Add(uint64(r.PrunedNoCont))
    ph.prunedDisconn.Add(uint64(r.PrunedDisconn))
    ph.prunedEndpoints.Add(uint64(r.PrunedEndpoints))
}
```

#### 6. ReportOracleStats(lookups, computes, classes, zeros int)

Вызывается один раз после завершения counting (см. counter.md). Значения — из
`oracle.Stats()`; `zeros` — число вставленных классов с `h == 0` (в классе не
найдено ни одного маршрута; инкрементируется только при фактической вставке).
Вызов идемпотентен последним (Store + `oracleSet=true`).

#### 7. Finish()

Финальный отчёт с переводом строки: тайминг и метрики каждой фазы, oracle-итоги,
общее число путей.

```go
func (m *RealMonitor) Finish() {
    if !m.started.Swap(false) {
        return
    }
    m.report()
    if prev := m.active.Load(); prev != nil {
        prev.endTime = time.Now()
    }

    fmt.Printf("\n=== Final ===\n")
    fmt.Printf("Total time: %s\n", time.Since(m.startTime))

    var totalPaths uint64
    for _, ph := range m.phases {
        // строка фазы: tasks/paths + условные writes, hits/misses + pruned с разбивкой
        fmt.Printf("Phase %s [%s]: %s\n", ph.name, ph.endTime.Sub(ph.startTime), ph.summary())
        totalPaths += ph.pathsFound.Load()
    }
    if m.oracleSet.Load() {
        fmt.Printf("Oracle: lookups=%d computes=%d classes=%d zeros=%d\n",
            m.oracleLookups.Load(), m.oracleComputes.Load(),
            m.oracleClasses.Load(), m.oracleZeros.Load())
    }
    fmt.Printf("Total paths: %d\n", totalPaths)
}
```

`phaseStats.summary()` — общая сборка строки фазы для финального отчёта
(сегменты условны так же, как в живой строке; разбивка pruned перечисляет только
ненулевые виды: `pruned 12034 (deadend 8211, nocont 302, disconn 2901, endpoints 620)`).

## Типы данных

### Result (types)

См. types.md — единый носитель пер-подзадачной статистики
(`CacheWrites`, `CacheHits/CacheMisses`, `Pruned*` по видам), который контур
кладёт в `ReportSubtask`.

## Использование в Counter

### Генерация подзадач (однофазная / фаза A / фаза B)

```go
g.Go(func() error {
    result := c.searcher.GenerateSubtasks(ctx, taskCache, p, group.OrbitSize, precomputeDepth)
    monitor.ReportSubtask(result) // writes + разбивка прунинга — одним вызовом
    monitor.ReportTaskCompleted()
    return nil
})
```

### Подсчёт

```go
monitor.BeginPhase("counting")
monitor.AddTasks(taskCache.ItemsCount())

total := atomic.Uint64{}
taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) error {
    result := c.searcher.CountPathsWithReversal(ctx, p, revOracle, oracleDepth) // или cache-variant

    paths := uint64(result.TotalPathsFound) * uint64(weight) // вес уже содержит орбиты
    total.Add(paths)
    monitor.ReportPathsFound(int(paths))
    monitor.ReportSubtask(result) // hits/misses + прунинг counting-фазы
    monitor.ReportTaskCompleted()
    return nil
})

// безусловно (без env), если oracle использовался:
if useOracle {
    lookups, computes, classes, zeros := revOracle.Stats()
    monitor.ReportOracleStats(lookups, computes, classes, zeros)
}
```

## Связанные спеки

Описание CLI и обработки Ctrl+C (graceful shutdown в `main.go`) — см. `specs/main.md`.

## FakeMonitor (для тестов)

Пустая реализация всех методов интерфейса для тестов:

```go
type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor { return &FakeMonitor{} }

func (*FakeMonitor) Start(ctx context.Context)                             {}
func (*FakeMonitor) Finish()                                               {}
func (*FakeMonitor) BeginPhase(name string)                                {}
func (*FakeMonitor) AddTasks(count int)                                    {}
func (*FakeMonitor) ReportTaskCompleted()                                  {}
func (*FakeMonitor) ReportPathsFound(count int)                            {}
func (*FakeMonitor) ReportSubtask(r types.Result)                          {}
func (*FakeMonitor) ReportOracleStats(lookups, computes, classes, zeros int) {}
```

## Требования к точности

1. **Пути:** Счётчик путей должен быть точным (атомарные операции обеспечивают потокобезопасность)
2. **Задачи:** Количество выполненных задач — целые числа, считаются отдельно на фазу
3. **Время:** Показания времени могут иметь погрешность ±1 секунда из-за интервала таймера; тайминги фаз точные (фиксируются в BeginPhase/Finish); длительности в живой строке округлены до миллисекунд
4. **ETA:** Линейная оценка по средней скорости активной фазы; `--` пока ни одна задача не завершена, `0s` когда фаза завершена; погрешность — ±интервал тикера и неравномерность скоростей задач
5. **Hit-rate:** `hits / (hits + misses) * 100`, одна цифра после точки; при нулевом знаменателе сегмент не печатается

## Обработка ошибок

- Нет критических зависимостей от мониторинга
- Остановка мониторинга не влияет на результат подсчёта
- Потеря отчётов в тикере не приводит к сбоям
- Отмена контекста корректно останавливает горутину мониторинга
- Репорты без активной фазы — no-op (нет паники)

## Производительность

- Overhead мониторинга: <1% (атомарные операции имеют минимальный contention)
- Мьютексы не нужны для обновления счётчиков (один mutex только на append фазы)
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
  условность сегментов `Writes` и `Hits/Misses`;
- формат финального отчёта: разбивка pruned по видам (только ненулевые), секция
  `Oracle:` только после `ReportOracleStats`;
- конкурентные репорты под `-race`.
