# ADR-016. Удаление class mode: reversal — единственный конвейер подсчёта

## Статус

Принято (2026-09-13, пользователь; шаг 7 плана 06 закрыт). Перенос дефолта `-mode` на
reversal закреплён ранее коммитом `0f70414`; удаление class mode — этот отдельный коммит.
Заменяет ADR-001 (class mode как схема подсчёта); механика ADR-007/ADR-008 (L2-прунинг и
pre-DP фильтр финального DP) снимается вместе с потребителем — их замеры остаются в тех ADR
навсегда.

## Контекст

Свод ADR-011 (6×6 полностью, 7×7 d14/d18/d22): на целевой семёрке при d ≥ 18 reversal быстрее
class на 29–63% при пике памяти на 51–95% ниже; на 6×6 на штатной глубине паритет по времени,
но плоский по памяти и аллокациям (единицы МБ против сотен МБ – десятков ГБ); class не влезает
на 8×8. Дефолт уже перенесён (`0f70414`), т.е. class-путь больше не обслуживается штатными
прогонами, но продолжает платить свою долю: второй финал генерации (gen B в аккумулятор M),
пакет shapecount, L2/фильтр прунера, DP-поля `types.Result`, сегменты мониторинга, class-метрики
и env-ручка бенч-харнесса, эквивалентностные тесты режимов и двойная поддержка в CI. Отдельно:
таблица `DefaultPrecomputeDepth` — оптимумы class-прогонов, для reversal-режима не пересъёмлены.

## Решение

Один конвейер: gen A (промежуточный аккумулятор D4-канон размещений) → gen B (`ExtendTask` в
task-cache) → counting (`Cache.Each` + `CountPathsWithCacheReversal`). Удаляется всё, что служит
только второму финалу:

- пакет `shapecount/` (DP h(shape,ends), tail-мемо, shape-фильтр — весь);
- `counter`: `Mode`/`SetMode`, class-ветка `ParallelCountWithDepth`, `countShapes`/`groupShards`/
  `processShapes`/`shardJob`/`shapeTask`/`groupSorted`/`claimBatch`, сеттеры `SetShapeFilter`,
  `SetTailMemo`, `SetShapeDump` и их поля;
- `searcher`: `ExtendToClasses` и спуск `dfsEmit` (базовый случай «форма дополнения»);
- `symmetry`: классы форм — `ShapeCtx`, `PrepareShape`, `KeyFromPrepared`, `CanonicalizeShape`
  (`symmetry/shape.go`); `TransformStates`/`CanonicalFromStates` остаются — они на hot-пути
  reversal-`completions`;
- `pruner`: ярус L2 целиком — `SetL2`, `ShouldPruneState`, `ShapeFeasible`, `L2Checks`,
  `DefaultMinL2`, причины `Articulation`/`ForcedChain` (подтверждено grep: потребители были
  только в shapecount);
- `types.Result`: поля `PrunedArticulation`, `PrunedForcedChain`, `DPStates`, `TailLookups`,
  `TailHits`, `FilteredShapes`;
- `monitoring`: `ReportShapeStats`/`ShapeStats()`, секция `Shapes:` финального отчёта, сегмент
  `Tail`, позиции `artic`/`chain` в разбивке pruned и соответствующие поля `PhaseStats`;
- `cache.Accumulator`: сиротевшее публичное per-shard чтение — `NumShards()` удаляется,
  `DrainShard` становится внутренней механикой `Drain()` (публичное чтение — только `Drain()`);
- CLI: флаги `-mode` и `-tail-memo` (единственный потребитель второго — shapecount), поле
  `mode`/`tailMemo` в `appArgs`, маппинг `counterMode`;
- тесты: эквивалентность режимов, class-специфика (shape-статы, tail-мемо, L2, shape-фильтр,
  `TestShapeFeasibilityClassification`), `counter/dump_test.go`; в searcher — phase-B oracle на
  shapecount (корректность закрепляют brute-force и тождество reversal);
- бенч-харнесс: env `BENCH_MODE` и `SHAPE_FILTER` удаляются, `BenchmarkCountAllToursClass`
  переименовывается в `BenchmarkCountAllTours` (синхронно фильтры `-bench=` в Makefile
  (`bench-size`, `bench-8x8`) и docstring `tools/bench_table.py`), class-only метрики
  (`classes/op`, `shapes/op`, `zeros/op`, `filtered/op`, `prunedDP_artic/op`,
  `prunedDP_chain/op`) из публикации исключаются.

Вместе с удалением (решение пользователя этим изменением): **пересъёмка `DefaultPrecomputeDepth`
под reversal** — полный свип глубин 5×5/6×6 и точечно 7×7; таблица `{5:6, 6:10, 7:20, 8:14}` —
кандидат на замену по замерам (для 8×8 точка не пересъёмляется: доска вне штатных прогонов,
ADR-015). Новые значения фиксируются в `counter.go`, `specs/counter.md` и подразделе «Замеры»
этой ADR.

README-showcase переписывается кодером в этом же изменении по свежему reversal-свипу
(`make bench`, снимает benchmarker) — текущие таблицы class-прогонов после удаления невоспроизводимы.

## Последствия

- Нельзя построить A/B «class ↔ reversal» на живом коде; сравнимость исторических свипов
  зафиксирована таблицами ADR-011 навсегда. Возврат схемы возможен только из истории git.
- Корректность единственного пути закрепляется brute-force оракулом (5×5), тождеством
  `Σ w·f(task)` по всем допустимым глубинам и инвариантностью итога к воркерам/глубине;
  независимый phase-B oracle уходит вместе с class-трубой.
- `types.Result` сжимается до счётчиков одного пути; мониторинг печатает разбивку pruned из
  четырёх видов.
- Бенч-харнесс меряет один конвейер: `make bench`/`make bench-size`/`bench-8x8` без env-режима,
  гейты `BENCH_DEEP`/`BENCH_8X8` сохраняются (для 8×8 формулировка «только reversal» теряет
  смысл — режим теперь один).
- Дефолты глубины после пересъёмки могут сдвинуться (ADR-011: на 6×6 окно минимума reversal —
  d12–d14 против штатного 10); это ожидаемое изменение поведения CLI, README обновляется тем же
  коммитом.

## Замеры

Удаление не трогает исполняемый путь reversal-конвейера — производительность выжившего пути не
меняется by construction; отдельного before/after по удалению не требуется (вердикт бенчмаркера
на свипе до/после ожидается NOISE). Числа, порождаемые этим изменением:

- **Пересъёмка `DefaultPrecomputeDepth` под reversal** (свип 5×5/6×6 все глубины, 7×7 точечно —
  окно d14…d22): заполняется бенчмаркером в этом же изменении; итоговая таблица adopted-значений —
  сюда же и в `specs/counter.md`.
- **Свежий reversal-свип для README** (`make bench`, штатные гейты): лог и таблицы — по
  методологии `specs/benchmarks.md`; штамп свежести README ссылается на него.

## Ссылки

Код: `counter/counter.go`, `searcher/searcher.go`, `symmetry/shape.go`, `pruner/pruner.go`,
`types/types.go`, `monitoring/monitor.go`, `cache/accumulator.go`, `main.go`,
`counter/benchmark_test.go`, Makefile, `tools/bench_table.py` — удаление; пакет
`shapecount/` — целиком.
Спеки: counter, searcher, cache, types, monitoring, pruner, symmetry, path, main, benchmarks.
Планы: 06 (шаг 7). Смежные ADR: 001 (заменяется), 002–005 (переживают — общее хранилище/ключ),
007/008 (механика снимается), 010 (снимается с class-counting), 011 (свод-основание),
012/013/014 (переживают), 015 (гейт 8×8 упрощается).
