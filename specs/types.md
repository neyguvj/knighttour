# types: общий носитель статистики подзадачи

## Ответственность

`Result` — блок счётчиков одной подзадачи, собираемый локально у вызывающего (searcher) по
ходу горячего DFS и один раз репортимый контуром в мониторинг по завершении. Единый формат
пер-подзадачной статистики.

## Публичный API

```go
type Result struct {
    TotalPathsFound int   // reversal count-DFS: число дополнений подзадачи; генерация
                          // не заполняет (пути публикует контур через ReportPathsFound)

    CacheWrites int       // записи весов в таблицы (Set, включая слияние под одним ключом)

    // Попадания/промахи lookup'ов в task-cache на уровне стопа count-фазы.
    CacheHits   int
    CacheMisses int

    // Прунинг по видам (значения pruner.Reason); Pruned == сумма видов (Finalize).
    Pruned            int
    PrunedDeadEnd     int
    PrunedNoCont      int
    PrunedDisconn     int
    PrunedEndpoints   int
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

## Ограничения и edge cases

- Все поля экспортированы; логика минимальна (сложение/инкремент).
- `TotalPathsFound`, `CacheHits/CacheMisses` заполняет только count-фаза (reversal
  count-DFS); генерационные методы их не касаются.

## Тесты

`types/types_test.go`: `TestResultAdd` (покомпонентно), `TestResultCountPrune`
(вид → поле, `NoReason` no-op), `Finalize` (агрегат = сумма видов).

## Связанные

`specs/pruner.md` (`Reason`), `specs/monitoring.md` (`ReportSubtask`).
