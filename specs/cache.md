# cache: две разделяемые таблицы «ключ → вес»

## Ответственность

Потокобезопасные таблицы аддитивных весов всего алгоритма. Две реализации с разными
контрактами чтения:

- `Accumulator` — **аддитивное накопление** (`Add`) с чтением только drain'ом после
  завершения фазы записи (class-mode пайплайн).
- `Cache` — task-cache с **конкурентным чтением через `Get`** поверх живущих записей
  (reversal-режим: значения читаются count-воркерами, пока таблица цела).

Нули не хранятся ни в одной таблице: запись создаётся только при положительном вкладе.
Пакет ничего не знает о симметриях — канонизацию выполняет писатель/читатель (ADR-005).

Экземпляры в пайплайне counter: class mode — промежуточный аккумулятор gen A (ключ —
D4-канон размещения префикса, вес — Σ orbitSize) и аккумулятор M gen B (ключ —
нормализованная форма дополнения, вес — Σ weight листьев); reversal mode — task-cache
(ключ — D4-канон префикса `(state, end)`, вес — Σ orbitSize), живущий до конца count-фазы.

## Публичный API

```go
type Entry struct { Path path.Path; Weight uint64 } // общая запись обеих таблиц

// --- Accumulator: additive, drain-only reads ---------------------------------

func NewAccumulator() *Accumulator            // 128 шардов map[path.Path]uint64 под Mutex

func (a *Accumulator) Add(p path.Path, weight uint64) // data[k] += w под локом шарда
func (a *Accumulator) ItemsCount() int

// Drain-чтение — только после остановки всех писателей (фазы барьерятся errgroup).
func (a *Accumulator) NumShards() int
func (a *Accumulator) DrainShard(i int) []Entry // забирает шард i и освобождает его map
func (a *Accumulator) Drain() []Entry           // конкатенация всех шардов (маленький worklist gen A)

// Писатели — через локальный буфер (не разделять между горутинами).
func (a *Accumulator) Local() *LocalSink
type LocalSink struct{ ... }
func (s *LocalSink) Add(p path.Path, weight uint64) // локально схлопывает дубликаты
func (s *LocalSink) Flush()                          // догружает буфер пачкой

// --- Cache: task-cache, concurrent Get ----------------------------------------

func NewCache() *Cache                        // 128 шардов map[path.Path]uint64 под RWMutex

func (c *Cache) Set(p path.Path, weight uint64) // data[k] += w; weight == 0 — no-op
func (c *Cache) Get(p path.Path) (uint64, bool) // конкурентно-safe чтение поверх писателей
func (c *Cache) ItemsCount() int

// Диспатч count-фазы без полного снапшота: NumShards + ленивые снимки по шарду.
// В отличие от Drain — данные НЕ изымаются: Get обязан видеть все шарды до конца фазы.
func (c *Cache) NumShards() int
func (c *Cache) SnapshotShard(i int) []Entry // копия шарда i под RLock
```

## Инварианты

- **Хэш шарда — только от `State`** (`h = state * 0x9E3779B97F4A7C15 >> (64-7)`), общий
  хелпер для обеих таблиц: у Accumulator все концы одной формы лежат в одном шарде →
  counting группирует per-shard без общего снимка (ADR-004); у Cache это лишь разнесение
  конкуренции.
- Сложение коммутативно — порядок слияния/флюша/записи на итог не влияет.
- `LocalSink`: локальный `map[path.Path]uint64`, порог `localFlushLimit = 1024`; при
  переполнении `Flush` догружает каждый уникальный ключ одним захватом лока шарда;
  `defer sink.Flush()` гарантирует остаток (ADR-002).
- `Cache.Set` с нулевым весом не создаёт запись (иначе нули появились бы в таблице).
- Записанные веса `Cache` — точные для своего ключа сразу после `Set` (видимость под
  локом шарда); count-фаза читает то, что записала генерация до барьера.

## Ограничения и edge cases

- Accumulator: чтение во время активной фазы записи не определено (drain после барьера);
  `DrainShard(i)` залочен только на время отдачи своего шарда.
- Cache: `Get` конкурентен с `Set` (RLock/RUnlock), но корректность подсчёта опирается на
  фазовый барьер errgroup — до него записей нужной глубины может не быть (miss законен).
- `SnapshotShard(i)` копирует один шард под RLock и освобождает его до возврата:
  пик диспатча = O(число шардов в полёте), а не O(таблица). Данные остаются — повторный
  снимок того же шарда законен.

## Тесты

`cache/accumulator_test.go`: Add/Drain суммирование под одним ключом и отсутствие нулей;
`DrainShard` — разбиение таблицы; инвариант «одинаковый State → один шард»; LocalSink ==
прямой Add (дубликаты, порог, явный Flush); конкурентные sinks под `-race` — сумма
инвариантна.

`cache/cache_test.go`: Set складывает веса под одним ключом и игнорирует нулевой; Get hit
видит сумму, miss → `(0,false)`; объединение `SnapshotShard` по всем `NumShards` == полная
таблица без дублей, данные после снимка на месте (Get видит); одинаковый State → один шард;
конкурентные Set/Get разных ключей под `-race` — все записи прочитаны.

## Связанные

ADR-002, ADR-003, ADR-004, ADR-011; `specs/path.md`, `specs/counter.md`.
