# Спеки: индекс и архитектура

Подсчёт всех открытых туров коня на досках 5×5–8×8. Полное ТЗ — `docs/requirements.md`.

## Правила ведения спек (обязательны)

- **Один факт = одно место.** Модульная спека описывает **текущую эталонную реализацию**:
  ответственность, публичный API и контракты, инварианты, edge cases, требования к тестам.
- В модульной спеке **нет**: истории («раньше было…»), таблиц бенчмарков, кода/примеров из
  других модулей, обоснований выбора. Всё это — ADR (`specs/decisions/`), планы
  (`specs/plans/`) и `specs/benchmarks.md`.
- **Замеры навсегда** живут в ADR: каждое решение несёт свои числа до/после. Спека ссылается
  на ADR строкой вида `(ADR-0NN)`.
- Пиши спеку по гайду skill `spec-writing`; план — по гайду `plan-writing`.

## Пайплайн (class mode)

```
graph.New(N) → counter.ParallelCountWithDepth(ctx, monitor, workers, depth)
  ├─ gen A: symmetry.GetCanonicalGroups() → searcher.GenerateRoots → аккумулятор A (D4-размещения)
  ├─ gen B: Drain(A) worklist → searcher.ExtendToClasses → аккумулятор M (классы форм D4⋉трансляции)
  └─ counting: DrainShard(M) per-shard → shapecount.CountShape(shape,ends) DP → total += Σ h(C)·M(C)
```

Все фазы параллельны (`errgroup`/атомарные курсоры), детерминированный итог через
`atomic.Uint64`. Детали — `specs/counter.md`.

## Компоненты

| Пакет | Спека | Ответственность |
|-------|-------|-----------------|
| state | [state.md](state.md) | Битборд посещённых клеток (uint64) и побитовые операции |
| path | [path.md](path.md) | Единый ключ `(state,end)` всех аккумуляторов |
| graph | [graph.md](graph.md) | Предвычисленный граф ходов коня + маски соседей |
| symmetry | [symmetry.md](symmetry.md) | D4-симметрии, канонизация пар и классов форм |
| types | [types.md](types.md) | `Result` — носитель статистики подзадачи |
| pruner | [pruner.md](pruner.md) | Отсечение тупиков (L0/L1 DFS, L2 DP, shape-фильтр) |
| cache | [cache.md](cache.md) | Аддитивный аккумулятор «ключ → Σ весов» |
| shapecount | [shapecount.md](shapecount.md) | DP `h(shape,ends)` по классу формы |
| searcher | [searcher.md](searcher.md) | DFS + генерационные фазы A/B |
| counter | [counter.md](counter.md) | Оркестрация трёх фаз, параллелизм, симметрии |
| monitoring | [monitoring.md](monitoring.md) | Прогресс по фазам (Real/Fake) |
| main | [main.md](main.md) | CLI, сборка, graceful shutdown |

## Куда писать что

| Тип знания | Место |
|------------|-------|
| API/поведение модуля сейчас | `specs/<пакет>.md` |
| Почему принято решение + замеры | `specs/decisions/NNN-*.md` (см. [индекс](decisions/README.md)) |
| Гипотезы оптимизации (гипотеза→дизайн→шаги→метрики→риски) | `specs/plans/NN-*.md` |
| Методология бенчей | `specs/benchmarks.md` |
| ТЗ, эталонные числа | `docs/requirements.md` |

## Проверки

```bash
make check   # fmt → vet → test -race → auto-fix idioms → golangci-lint
make bench   # см. specs/benchmarks.md
```
