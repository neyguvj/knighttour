# types: общий носитель статистики подзадачи

## Ответственность

`Result` — широкий блок счётчиков одной подзадачи, собираемый локально у вызывающего
(searcher/shapecount) по ходу горячего DFS/DP и один раз репортимый контуром в
мониторинг по завершении. Единый формат пер-подзадачной статистики.

## Публичный API

```go
type Result struct {
    TotalPathsFound int   // ЗАРЕЗЕРВИРОВАН: production не заполняет
                          // (counting публикует взвешенные пути через ReportPathsFound)

    CacheWrites int       // эмиссии в аккумуляторы (sink.Add, включая слияние в ключ)

    // Прунинг по видам (значения pruner.Reason); Pruned == сумма видов (Finalize).
    Pruned            int
    PrunedDeadEnd     int
    PrunedNoCont      int
    PrunedDisconn     int
    PrunedEndpoints   int
    PrunedArticulation int // L2, только shapecount DP
    PrunedForcedChain  int // L2, только shapecount DP

    DPStates      int // shapecount: вычисленные состояния DP (промахи memo)
    TailLookups   int // shapecount: обращения к persistent tail-мемо
    TailHits      int // shapecount: попадания tail-мемо
    FilteredShapes int // shapecount: форм, убитых pre-DP фильтром (отдельно от pruned*)
}

func (r *Result) Add(other *Result)         // покомпонентное сложение (pointer — wide block)
func (r *Result) CountPrune(reason pruner.Reason) // горячий путь: ровно один инкремент вида
func (r *Result) Finalize()                 // Pruned = Σ видов; один раз перед возвратом
```

## Инварианты

- Счётчики ведёт **локальный** `*Result` одного воркера — без атомиков и contention;
  конкурентные воркеры не делят общих счётчиков.
- `CountPrune` не трогает агрегат `Pruned` (лишний store на каждом prune); сводку
  считает `Finalize` на выходе публичных методов.
- `NoReason` в `CountPrune` игнорируется (означает «не отсечено»).
- `FilteredShapes` держится отдельно от `Pruned*`, чтобы исторические метрики прунинга
  оставались сопоставимы (ADR-008).

## Ограничения и edge cases

- Все поля экспортированы; логика минимальна (сложение/инкремент).
- `TotalPathsFound` — зарезервированное поле, в текущем пайплайне не заполняется.

## Тесты

`types/types_test.go`: `TestResultAdd` (покомпонентно), `TestResultCountPrune`
(вид → поле, `NoReason` no-op), `Finalize` (агрегат = сумма видов).

## Связанные

`specs/pruner.md` (`Reason`), `specs/monitoring.md` (`ReportSubtask`).
