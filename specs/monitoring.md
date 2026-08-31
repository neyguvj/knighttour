# Мониторинг прогресса

## Назначение

Отслеживание и отображение прогресса подсчета маршрутов в реальном времени
**с разбивкой по фазам выполнения**:
- Время выполнения (общее и каждой фазы)
- Количество обработанных/оставшихся задач текущей фазы
- Количество найденных путей
- Количество **записей в кэш** (cache writes, вызовов `Set`) — исторически поле
  называлось «cached paths», но это именно записи: один ключ может быть записан
  многократно из разных префиксов
- Количество ветвей, отсечённых прунером (pruned)

Фазы запуска:
- `generation` — однофазная генерация подзадач (`precomputeDepth ≤ TwoPhaseBaseDepth`)
- либо `gen A` + `gen B` — двухфазная генерация
- `counting` — фаза подсчёта по записям кэша

## Требования

### Частота обновления
- **Интервал:** 1 секунда
- **Формат:** Строковый вывод в stdout (в формате ProgressBar с \r)
- Живая строка показывает **только активную фазу**

### Информация в отчете
Каждый отчёт должен содержать:
```
[время] Phase [имя] | Tasks: [выполнено]/[всего] (%) | Paths [найдено] | Writes [записей в кэш] | Pruned [отсечено]
```

**Пример:**
```
[1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334
```

### Финальный отчёт
Отдельная строка на каждую фазу + итоги:
```
=== Final ===
Total time: 63ms
Phase generation [41ms]: tasks 6/6 | paths 0 | writes 2795 | pruned 12034
Phase counting [22ms]: tasks 95224/95224 | paths 6637920 | writes 0 | pruned 180322
Total paths: 6637920
```

## Интерфейс Monitor

```go
type Monitor interface {
    Start(ctx context.Context)
    Finish()
    BeginPhase(name string)        // начать новую фазу (см. ниже — только между фазами)
    AddTasks(count int)            // задачи активной фазы
    ReportTaskCompleted()          // задача активной фазы завершена
    ReportPathsFound(count int)
    ReportCacheWrites(count int)   // записей в кэш (cache.Set), не «закэшированные пути»
    ReportPruned(count int)        // ветви, отсечённые ShouldPruneAfterVisit
}
```

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
    startTime   time.Time     // начало фазы (для тайминга в финальном отчёте)
    endTime     time.Time     // конец фазы (фиксируется при BeginPhase следующей или Finish)
    tasks       atomic.Uint64 // задач в фазе
    completed   atomic.Uint64 // завершённых задач фазы
    pathsFound  atomic.Uint64 // найденные пути (только counting)
    cacheWrites atomic.Uint64 // записи в кэш (generation)
    pruned      atomic.Uint64 // отсечённые прунером ветви (обе фазы)
}

type RealMonitor struct {
    startTime time.Time
    started   atomic.Bool
    active    atomic.Pointer[phaseStats] // активная фаза
    phasesMu  sync.Mutex                 // защищает срез phases от append
    phases    []*phaseStats              // порядок следования фаз
}
```

Все счётчики используют `atomic.Uint64` для потокобезопасного доступа без мьютексов.

### Методы

#### 1. NewMonitor() *RealMonitor

Инициализация мониторинга (параметры не нужны, фаз ещё нет).

#### 2. Start(ctx context.Context)

Запуск периодического отчёта каждую секунду (тот же паттерн, что и раньше:
ticker + `ctx.Done()`; при остановке/отмене — последний `report()`).

```go
func (m *RealMonitor) Start(ctx context.Context) {
    m.startTime = time.Now()
    m.started.Store(true)
    go func() {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                if !m.started.Load() {
                    return
                }
                m.report()
            case <-ctx.Done():
                // Finish() уже напечатал финальный отчёт — повторяем только если он не вызывался
                if m.started.Load() {
                    m.report()
                }
                return
            }
        }
    }()
}
```

#### 3. BeginPhase(name string)

Фиксирует `endTime` предыдущей фазы (если была), создаёт новую, делает её
активной. Вызывается **только между фазами** (см. контракт).

```go
func (m *RealMonitor) BeginPhase(name string) {
    if prev := m.active.Load(); prev != nil {
        prev.endTime = time.Now()
    }
    ph := &phaseStats{name: name, startTime: time.Now()}
    m.phasesMu.Lock()
    m.phases = append(m.phases, ph)
    m.phasesMu.Unlock()
    m.active.Store(ph)
}
```

#### 4. report()

Строка активной фазы (без перевода строки — используется \r для перезаписи);
при отсутствии активной фазы — no-op. Деление на ноль защищено (`tasks == 0` → 0%).

```go
func (m *RealMonitor) report() {
    ph := m.active.Load()
    if ph == nil {
        return
    }
    elapsed := time.Since(m.startTime)
    completed := ph.completed.Load()
    totalTasks := ph.tasks.Load()

    pct := 0.0
    if totalTasks > 0 {
        pct = float64(completed) / float64(totalTasks) * 100
    }

    fmt.Printf(
        "\r[%s] Phase %s | Tasks: %d/%d (%.1f%%) | Paths %d | Writes %d | Pruned %d",
        elapsed.String(), ph.name,
        completed, totalTasks, pct,
        ph.pathsFound.Load(),
        ph.cacheWrites.Load(),
        ph.pruned.Load(),
    )
}
```

#### 5. AddTasks / ReportTaskCompleted / ReportPathsFound / ReportCacheWrites / ReportPruned

Все — `Add` в счётчик **активной** фазы; при отсутствии активной фазы — no-op:

```go
func (m *RealMonitor) ReportPruned(count int) {
    if ph := m.active.Load(); ph != nil {
        ph.pruned.Add(uint64(count))
    }
}
```

#### 6. Finish()

Финальный отчёт с переводом строки: тайминг и метрики каждой фазы + итоги.

```go
func (m *RealMonitor) Finish() {
    if !m.started.Load() {
        return
    }
    m.report()
    m.started.Store(false)
    if prev := m.active.Load(); prev != nil {
        prev.endTime = time.Now()
    }

    fmt.Printf("\n=== Final ===\n")
    fmt.Printf("Total time: %s\n", time.Since(m.startTime))

    var totalPaths uint64
    for _, ph := range m.phases {
        fmt.Printf(
            "Phase %s [%s]: tasks %d/%d | paths %d | writes %d | pruned %d\n",
            ph.name, ph.endTime.Sub(ph.startTime),
            ph.completed.Load(), ph.tasks.Load(),
            ph.pathsFound.Load(), ph.cacheWrites.Load(), ph.pruned.Load(),
        )
        totalPaths += ph.pathsFound.Load()
    }
    fmt.Printf("Total paths: %d\n", totalPaths)
}
```

## Типы данных

### Result (types)

```go
type Result struct {
    TotalPathsFound int  // найденные пути в поддереве
    CacheWrites     int  // число записей в кэш (вызовов cache.Set)
    Pruned          int  // ветви, отсечённые прунером
}

func (r *Result) Add(other Result)
// Суммирует поля result и other
```

## Использование в Counter

### Генерация подзадач (однофазная / фаза A)

```go
func (c *Counter) generateSubTasksSinglePhase(
    ctx context.Context,
    monitor monitoring.Monitor,
    phase string,
    workers int,
    precomputeDepth int,
) *cache.Cache {
    monitor.BeginPhase(phase) // "generation" или "gen A"
    taskCache := cache.NewCache(c.symmetry)
    groups := c.symmetry.GetCanonicalGroups()
    monitor.AddTasks(len(groups))

    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(workers)
    for _, group := range groups {
        p := group.Canonical
        g.Go(func() error {
            result := c.searcher.GenerateSubtasks(ctx, taskCache, p, group.OrbitSize, precomputeDepth)
            monitor.ReportCacheWrites(result.CacheWrites)
            monitor.ReportPruned(result.Pruned)
            monitor.ReportTaskCompleted()
            return nil
        })
    }
    _ = g.Wait()

    return taskCache
}
```

### Фаза B и подсчёт

```go
// Двухфазная генерация: после Wait фазы A — BeginPhase("gen B"),
// AddTasks(len(entries)), на ExtendSubtask — ReportCacheWrites/ReportPruned/ReportTaskCompleted.

func (c *Counter) ParallelCountWithDepth(...) uint64 {
    taskCache := c.generateSubTasks(ctx, monitor, workers, precomputeDepth)
    monitor.BeginPhase("counting")
    monitor.AddTasks(taskCache.ItemsCount())

    total := atomic.Uint64{}
    taskCache.Each(ctx, workers, func(ctx context.Context, p path.Path, weight int) {
        result := c.searcher.CountPathsWithReversal(ctx, p, taskCache, precomputeDepth)

        paths := uint64(result.TotalPathsFound) * uint64(weight) // вес уже содержит орбиты
        total.Add(paths)
        monitor.ReportPathsFound(int(paths))
        monitor.ReportPruned(result.Pruned) // прунинг считается и в фазе подсчёта
        monitor.ReportTaskCompleted()
    })

    return total.Load()
}
```

## Связанные спеки

Описание CLI и обработки Ctrl+C (graceful shutdown в `main.go`) — см. `specs/main.md`.

## FakeMonitor (для тестов)

```go
type FakeMonitor struct{}

func NewFakeMonitor() *FakeMonitor {
    return &FakeMonitor{}
}

func (*FakeMonitor) Start(ctx context.Context)        {}
func (*FakeMonitor) Finish()                          {}
func (*FakeMonitor) BeginPhase(name string)           {}
func (*FakeMonitor) AddTasks(count int)               {}
func (*FakeMonitor) ReportTaskCompleted()             {}
func (*FakeMonitor) ReportPathsFound(count int)       {}
func (*FakeMonitor) ReportCacheWrites(count int)      {}
func (*FakeMonitor) ReportPruned(count int)           {}
```

Пустая реализация всех методов интерфейса для тестов.

## Требования к точности

1. **Пути:** Счётчик путей должен быть точным (атомарные операции обеспечивают потокобезопасность)
2. **Задачи:** Количество выполненных задач — целые числа, считаются отдельно на фазу
3. **Время:** Показания времени могут иметь погрешность ±1 секунда из-за интервала таймера; тайминги фаз точные (фиксируются в BeginPhase/Finish)

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
- конкурентные репорты под `-race`.
