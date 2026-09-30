# Спеки: индекс и архитектура

Проект считает все открытые туры коня на досках 5×5–8×8. Полное техническое задание —
`docs/requirements.md`.

## Правила ведения спек (обязательны)

- **Один факт = одно место.** Модульная спека описывает **текущую эталонную реализацию**:
  ответственность, публичный API и контракты, инварианты, edge cases, требования к тестам.
- В модульной спеке **нет**: истории («раньше было…»), таблиц бенчмарков, кода/примеров из
  других модулей, обоснований выбора. Всё это — ADR (`specs/decisions/`), планы
  (`specs/plans/`) и `specs/benchmarks.md`.
- **Замеры живут в ADR навсегда**: каждое решение несёт свои числа до/после. Спека ссылается
  на ADR строкой вида `(ADR-0NN)`.
- Пиши спеку по гайду скилла `spec-writing`, план — по гайду `plan-writing`, прозу — по
  стандарту `doc-style`.

## Пайплайн (единственный конвейер, ADR-016)

```
graph.New(N) → counter.ParallelCountWithDepth(ctx, monitor, workers, depth)
  ├─ gen A: symmetry.GetCanonicalGroups() → searcher.GenerateTasks → промежуточная таблица (D4-размещения)
  ├─ gen B: View.All создаёт список работ (после Seal) → searcher.ExtendTask → task-cache (канонические префиксы, вес Σ orbitSize)
  └─ count: Seal(task-cache) → канал партий поверх View.All → searcher.CountPathsWithCacheReversal → total = Σ W(task)·f(task)
```

Все фазы распараллелены пакетом `workerpool`: `Run` — общий атомарный счётчик задач и барьер,
`Fanout` — передача значений от производителя потребителям по буферизованному каналу. Итог
собирается детерминированно через `atomic.Uint64`. Подробности — `specs/counter.md`,
`specs/workerpool.md`.

## Компоненты

| Пакет | Спека | Ответственность |
|-------|-------|-----------------|
| state | [state.md](state.md) | Битборд посещённых клеток (`uint64`) и побитовые операции |
| graph | [graph.md](graph.md) | Предвычисленный граф ходов коня и маски соседей |
| symmetry | [symmetry.md](symmetry.md) | Симметрии D4, каноническая форма «маска + класс конца», стартовые группы |
| types | [types.md](types.md) | `Result` — счётчики статистики подзадачи |
| pruner | [pruner.md](pruner.md) | Отсечение тупиков (L0/L1) и фильтр записей в кэш (L2) |
| cache | [cache.md](cache.md) | Единая аддитивная таблица «маска → гистограмма классов концов» (промежуточная gen A + task-cache) |
| searcher | [searcher.md](searcher.md) | DFS, генерационные фазы A/B, count-DFS с обращением тура |
| counter | [counter.md](counter.md) | Оркестрация трёх фаз, параллелизм, симметрии |
| workerpool | [workerpool.md](workerpool.md) | Механика параллелизма: `Run` (счётчик + барьер), `Fanout[T]` (канал + дочитывание) |
| monitoring | [monitoring.md](monitoring.md) | Прогресс по фазам (Real/Fake) |
| main | [main.md](main.md) | CLI, сборка компонентов, завершение по сигналу |

## Куда что писать

| Тип знания | Место |
|------------|-------|
| API/поведение модуля сейчас | `specs/<пакет>.md` |
| Почему принято решение + замеры | `specs/decisions/NNN-*.md` (см. [индекс](decisions/README.md)) |
| Гипотезы оптимизации (гипотеза → дизайн → шаги → метрики → риски) | `specs/plans/NN-*.md` |
| Методология бенчмарков | `specs/benchmarks.md` |
| Жёсткие гейты агентского пайплайна (реестр) | [gates.md](gates.md) |
| Переиспользуемые скрипты-утилиты | код `tools/*.py`, карточка `specs/tools/<tool>.md`, строка в [индекс](tools/README.md) |
| ТЗ, эталонные числа | `docs/requirements.md` |

## Проверки

```bash
make check   # fmt → vet → test -race → автофиксы идиом → golangci-lint
make bench   # см. specs/benchmarks.md
```
