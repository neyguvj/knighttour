# cache: аддитивный аккумулятор весов

## Ответственность

Единственная потокобезопасная таблица «ключ → агрегированный вес» всего алгоритма.
Семантика — **аддитивное накопление** (`Add`), чтение только drain'ом после завершения
фазы записи. Мемо-кэша (Get/Set/Each) нет и не нужно (ADR-002).

Два экземпляра живут в пайплайне counter: промежуточный аккумулятор gen A (ключ —
D4-канон размещения префикса, вес — Σ orbitSize) и аккумулятор M gen B (ключ —
нормализованная форма дополнения, вес — Σ weight листьев).

## Публичный API

```go
type Entry struct { Path path.Path; Weight uint64 }

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
```

## Инварианты

- **Хэш шарда — только от `State`** (`h = state * 0x9E3779B97F4A7C15 >> (64-7)`): все
  концы одной формы лежат в одном шарде → counting группирует per-shard без глобальной
  сортировки и без общего снимка (ADR-004).
- Нули по построению не хранятся: запись создаётся только при `Add`.
- Сложение коммутативно — порядок слияния/флюша на результат не влияет.
- `LocalSink`: локальный `map[path.Path]uint64`, порог `localFlushLimit = 1024`; при
  переполнении `Flush` догружает каждый уникальный ключ одним захватом лока шарда;
  `defer sink.Flush()` гарантирует остаток (ADR-002).
- Пакет ничего не знает о симметриях — канонизацию выполняет писатель.

## Ограничения и edge cases

- Чтение во время активной фазы записи не определено (drain после барьера).
- `DrainShard(i)` залочен только на время отдачи своего шарда.

## Тесты

`cache/accumulator_test.go`: Add/Drain суммирование под одним ключом и отсутствие нулей;
`DrainShard` — разбиение таблицы; инвариант «одинаковый State → один шард»; LocalSink ==
прямой Add (дубликаты, порог, явный Flush); конкурентные sinks под `-race` — сумма
инвариантна. Оценка памяти хранилища — ADR-003/004, не здесь.

## Связанные

ADR-002, ADR-003, ADR-004; `specs/path.md`, `specs/counter.md`.
