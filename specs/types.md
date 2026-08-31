# Компонент Types: Типы данных для обмена

## Назначение

Общие типы данных, используемые в разных компонентах системы.

## Result

```go
type Result struct {
    TotalPathsFound int  // количество найденных путей в поддереве
    CacheWrites     int  // число записей в кэш (вызовов cache.Set, включая обновление существующих ключей)
    Pruned          int  // число ветвей, отсечённых прунером ShouldPruneAfterVisit
}

func (r *Result) Add(other Result)
// Суммирует поля result и other покомпонентно
```

Историческая справка: поле `CachedPaths` называло счётчик `Set()` «закэшированными
путями», хотя это именно **записи в кэш** (одно и то же состояние может быть
записано многократно из разных префиксов) — переименовано в `CacheWrites`.

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
