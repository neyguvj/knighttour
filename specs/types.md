# Компонент Types: Типы данных для обмена

## Назначение

Общие типы данных, используемые в разных компонентах системы.

## Result

```go
type Result struct {
    TotalPathsFound int  // количество найденных путей в поддереве
    CachedPaths     int  // количество закэшированных путей (в GenerateSubtasks)
}

func (r *Result) Add(other Result)
// Суммирует поля result и other: total += other.total, cached += other.cached
```

## Использование

```go
// В Searcher:
result := searcher.CountPaths(ctx, start)
fmt.Printf("Found %d paths\n", result.TotalPathsFound)

result2 := searcher.GenerateSubtasks(ctx, cache, start, orbitSize, depth)
fmt.Printf("Cached %d paths\n", result2.CachedPaths)
```

## Ограничения

- Типы простые и не имеют сложной логики
- All fields are public (exported) для удобства доступа
