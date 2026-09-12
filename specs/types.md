# Компонент Types: Типы данных для обмена

## Назначение

Общие типы данных, используемые в разных компонентах системы.

## Result

Полная статистика одной подзадачи (или агрегат), собираемая searcher'ом по ходу
горячего DFS и репортимая контуром в мониторинг по завершении подзадачи.

```go
type Result struct {
    TotalPathsFound int // количество найденных путей в поддереве (сейчас никто не заполняет)

    // Прунинг по видам (значения pruner.Reason); Pruned == сумма по видам
    // (пересчитывается методом Finalize на выходе публичных методов searcher)
    Pruned           int
    PrunedDeadEnd    int // локальный dead-end / изолированная клетка
    PrunedNoCont     int // у last нет непосещённых соседей (нет продолжения)
    PrunedDisconn    int // G[unvisited] несвязен
    PrunedEndpoints  int // эвристика концов (degree-1 вершины)
    PrunedArticulation int // L2: сочленения графа состояния (только shapecount DP)
    PrunedForcedChain  int // L2: противоречие обязательных цепочек (только shapecount DP)

    CacheWrites int // число эмиссий в аккумуляторы (sink.Add, включая слияние в существующий ключ)

    DPStates int // заполняет shapecount: состояний DP, промахнувшихся по memo
                 // (метрика «узлов DP» для этапа 0 плана 04; searcher не ведёт)

    TailLookups int // shapecount only: обращений к persistent tail-мемо (план 03)
    TailHits    int // shapecount only: попаданий в tail-мемо (hit-rate = hits/lookups)

    FilteredShapes int // shapecount only: форм, убитых shape-фильтром до DP (план 02);
                       // отдельно от pruned* — исторические метрики прунинга не смешиваются
}

func (r *Result) Add(other *Result)
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
Поля `CacheHits/CacheMisses` удалены вместе с legacy prefix-cache reversal.

Замечание об учёте статистики: счётчики ведутся **локально у вызывающего** —
локальный аккумулятор `*Result` прокидывается через DFS без атомиков и
contention; конкурентные воркеры не делят общих счётчиков.

## Использование

```go
// В Searcher (фазы генерации):
result := searcher.GenerateRoots(ctx, sink, start, orbitSize, depth)
fmt.Printf("Cache writes %d, pruned %d\n", result.CacheWrites, result.Pruned)

result2 := searcher.ExtendToClasses(ctx, sink, p, weight, depth)
```

## Ограничения

- Типы простые и не имеют сложной логики
- All fields are public (exported) для удобства доступа
