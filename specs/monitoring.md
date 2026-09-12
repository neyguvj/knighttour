# monitoring: прогресс по фазам

## Ответственность

Отслеживание и отображение прогресса подсчёта **с разбивкой по фазам** (`gen A` → `gen B`
→ `counting`): время, ETA активной фазы, задачи, найденные пути, эмиссии в аккумуляторы,
попадания/промахи task-cache, прунинг с разбивкой по видам, итоги форм. Монитор только
агрегирует — счётчиков поиска внутри нет; статистика приходит от завершённых подзадач через
`ReportSubtask`.

## Публичный API

```go
type Monitor interface {
    Start(ctx context.Context)
    Finish()
    BeginPhase(name string)              // новая активная фаза — ТОЛЬКО между фазами
    AddTasks(count int)                  // задачи активной фазы
    ReportTaskCompleted()
    ReportPathsFound(count int)          // пути (counting — взвешенные weight'ом)
    ReportSubtask(r *types.Result)       // складывает writes + прунинг по видам подзадачи
    ReportShapeStats(classes, shapes, zeroShapes int) // один раз после финального прохода
}

func NewMonitor() *RealMonitor           // verbose: живой строкой и финальным отчётом
func NewFakeMonitor() *FakeMonitor       // verbose: false — только накопление

// Снимки для тестов/бенчмарков (на общем ядре, доступны и у RealMonitor):
type PhaseStats struct { Tasks, Completed, Subtasks, PathsFound, CacheWrites uint64;
    CacheHits, CacheMisses uint64;
    Pruned uint64; PrunedDeadEnd, PrunedNoCont, PrunedDisconn, PrunedEndpoints uint64;
    PrunedArticulation, PrunedForcedChain uint64; TailLookups, TailHits uint64;
    FilteredShapes uint64; Duration time.Duration }

func (m *monitor) Phase(name string) PhaseStats  // сумма одноимённых фаз
func (m *monitor) Totals() PhaseStats            // сумма по всем фазам
func (m *monitor) ShapeStats() (classes, shapes, zeros uint64)
```

## Контракты

- **Общее ядро**: `RealMonitor` и `FakeMonitor` исполняют один код; различие — ровно поле
  `verbose`. Фейк не заводит тикер и не печатает, но накапливает идентично.
- **BeginPhase** вызывается контуром строго между фазами (воркеры предыдущей завершены):
  append фаз не конкурентен с репортами; репорты читают активную фазу через `atomic.Pointer`.
  Повторный `BeginPhase("gen A")` даёт **новую** запись, `Phase(name)` суммирует одноимённые.
- Репорты до первого `BeginPhase` (нет активной фазы) — no-op.
- `ReportSubtask` — единственный репорт пер-подзадачной статистики; `TotalPathsFound` из
  `Result` им игнорируется (counting публикует взвешенные пути через `ReportPathsFound`).
- Все счётчики — `atomic.Uint64`; суммарный `pruned` фазы = сумма видов.

## Формат вывода (verbose)

Живая строка (каждую секунду, только активная фаза, `\x1b[2K\r` затирает предыдущую):
```
[1.234s] Phase gen B | Tasks: 1200/5041 (23.8%) | Paths 0 | Writes 447520 | Pruned 129334 | ETA 3.953s
```
Финальный отчёт (`Finish`): строка на фазу + `Shapes:` (безусловно после ReportShapeStats) +
итог; разбивка pruned в скобках перечисляет только ненулевые виды в фиксированном порядке
`deadend, nocont, disconn, endpoints, artic, chain`.

ETA — линейная оценка `elapsed_phase·(total−completed)/completed`; при `completed==0` или
`total==0` → `ETA --`, при `completed>=total` → `ETA 0s`. Сегменты `Writes`/`Tail` условны
(печатаются при ненулевых значениях). Сегмент `Hits x/y` (попадания/промахи task-cache из
`ReportSubtask`) печатается после `Writes`, когда хотя бы одно ненулевое.

## Инварианты и edge cases

- Overhead < 1% (атомарные операции; mutex только на append фазы).
- Отмена контекста останавливает горутину мониторинга; потеря отчётов в тикере не влияет на
  результат подсчёта. `Finish` без `Start` не паникует.
- Снимки аддитивны и не сбрасываются: переиспользование монитора на N прогонов даёт
  N-кратные суммы — для метрик «на прогон» монитор создают заново.
- `Duration` фиксируется при закрытии фазы (`BeginPhase` следующей или `Finish`) — для
  чтения длительности последней фазы нужно вызвать `Start`+`Finish`.

## Тесты

`monitoring/monitor_test.go`: репорты без активной фазы no-op; агрегация по фазам;
`ReportSubtask` раскладывает по видам (включая hits/misses); `estimateRemaining` таблично;
формат живой/финальной строки (перехват stdout: `\x1b[2K\r`, мс, `ETA --`/`0s`, условность
сегментов `Writes`/`Tail`/`Hits`); **общность
кода** — один сценарий на Real и Fake даёт совпадающие снимки, stdout фейка пуст; повторный
BeginPhase — новая фаза; конкурентные репорты под `-race`.

## Связанные

`specs/types.md` (`Result`), `specs/counter.md`, `specs/main.md`.
