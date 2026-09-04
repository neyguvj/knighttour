# Компонент Types: Типы данных для обмена

## Назначение

Общие типы данных, используемые в разных компонентах системы.

## Result

Полная статистика одной подзадачи (или агрегат), собираемая searcher'ом по ходу
горячего DFS и репортимая контуром в мониторинг по завершении подзадачи.

```go
type Result struct {
    TotalPathsFound int // количество найденных путей в поддереве

    // Кэш префиксов (reversal-lookup'ы считаются у места вызова GetCanonical)
    CacheHits   int // попадания в кэш на уровнях досрочного завершения
    CacheMisses int // промахи (нет записи → h = 0 для этого конца)

    // Прунинг по видам (значения pruner.Reason); Pruned == сумма по видам
    // (пересчитывается методом Finalize на выходе публичных методов searcher)
    Pruned           int
    PrunedDeadEnd    int // локальный dead-end / изолированная клетка
    PrunedNoCont     int // у last нет непосещённых соседей (нет продолжения)
    PrunedDisconn    int // G[unvisited] несвязен
    PrunedEndpoints  int // эвристика концов (degree-1 вершины)

    CacheWrites int // число записей в кэш (вызовов cache.Set, включая обновление существующих ключей)
}

func (r *Result) Add(other Result)
// Суммирует поля result и other покомпонентно

func (r *Result) CountPrune(reason pruner.Reason)
// Горячий путь: ровно один инкремент поля вида по причине из
// ShouldPruneAfterVisit; NoReason игнорируется. Индексную сводку Pruned не
// трогает — для этого есть Finalize

func (r *Result) Finalize()
// Pruned = сумма счётчиков по видам; вызывается один раз перед возвратом Result
```

Историческая справка: поле `CachedPaths` называло счётчик `Set()` «закэшированными
путями», хотя это именно **записи в кэш** (одно и то же состояние может быть
записано многократно из разных префиксов) — переименовано в `CacheWrites`.

Замечание об учёте hits/misses: счётчики ведутся **локально у вызывающего**
(`Reversal.Completions`), а не внутри `Cache`: воркеры фазы counting работают
конкурентно, и глобальные атомики кэша нельзя корректно разложить по подзадачам
(окна снапшотов пересекаются). Локальный аккумулятор `*Result` прокидывается
через DFS без атомиков и contention.

## Использование

```go
// В Searcher:
result := searcher.CountPaths(ctx, start)
fmt.Printf("Found %d paths\n", result.TotalPathsFound)

result2 := searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)
fmt.Printf("Cache writes %d, pruned %d\n", result2.CacheWrites, result2.Pruned)
```

## Ограничения

- Типы простые и не имеют сложной логики
- All fields are public (exported) для удобства доступа
