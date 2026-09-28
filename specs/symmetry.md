# symmetry: симметрии доски (D4) и канонизация

## Ответственность

Группа симметрий квадрата D4 для сокращения поиска: канонические стартовые группы с
размерами орбит и канонизация размещения `(маска, конец)` в форму «каноническая маска +
класс конца» — единую функцию писателя и читателя таблиц (specs/cache.md). Пакет не хранит
весов и ничего не знает о таблицах: он отдаёт `K`, тег класса `rep` и `orbitSize`, которые
вызываются писателю/читателю как есть. Горячий путь — только подстановка в предвычисленные
LUT `perms`/`compose`: без замыканий, аллокаций и рекурсии.

## Группа D4 (8 преобразований)

| № | Название | Формула (x,y) |
|---|----------|---------------|
| 0 | Identity | (x, y) |
| 1 | Rotate 90° | (y, size-1-x) |
| 2 | Rotate 180° | (size-1-x, size-1-y) |
| 3 | Rotate 270° | (size-1-y, x) |
| 4 | Flip horizontal | (x, size-1-y) |
| 5 | Flip vertical | (size-1-x, y) |
| 6 | Flip diag1 | (y, x) |
| 7 | Flip diag2 | (size-1-y, size-1-x) |

## Каноническая форма пары `(маска, конец)`

Канонический ключ размещения — **каноническая маска** `K = min_t t(маска)` и **класс конца**
внутри `K`: два конца одной маски эквивалентны ⟺ связаны её стабилизатором
`Stab(K) = {t ∈ D4 : t(K) = K}`. Пара `(K, rep)` — лексминимум `(t(маска), t(конец))` по всем
8 преобразованиям: при нетривиальном стабилизаторе минимум маски достигается несколькими
кадрами `t`, но сведение конца к репрезентанту stab-орбиты поглощает эту неоднозначность —
результат не зависит от выбора кадра (tie-break исключён конструктивно). Число продолжений `h`
и `orbitSize = |D4·(K,e)| = 8/|Stab(K,e)|` постоянны на классе; `orbitSize` вычисляется из
`perms` на чтении и нигде не хранится.

## Публичный API

```go
const NumTransforms = 8

type Transform func(x, y, size int) (int, int)
func GetSymmetries() []Transform              // 8 преобразований (только при построении LUT)

type CanonicalGroup struct {
    Positions []int
    Canonical int
    OrbitSize int
}

func NewSymmetry(size int) *Symmetry

// позиции / орбиты клеток доски (стартовые группы)
func (s *Symmetry) GetCanonicalPosition(pos int) int
func (s *Symmetry) IsCanonicalPosition(pos int) bool
func (s *Symmetry) GetOrbitSize(pos int) int
func (s *Symmetry) GetCanonicalGroups() []CanonicalGroup

// CanonicalMaskFrame — канонический фрейм маски: K = min_t t(st); frame — индекс любого
// преобразования, достигшего минимума (детерминированно — наименьший такой индекс); stab —
// битовая маска стабилизатора Stab(K) (бит i ⟺ преобразование i фиксирует K; содержит
// тождество и замкнута — это подгруппа). Множество {t : t(st) == K} есть смежный класс
// {s∘frame : s ∈ stab}.
func (s *Symmetry) CanonicalMaskFrame(st state.State) (K state.State, frame uint8, stab uint8)

// TransformCell — образ клетки при преобразовании t (подсмотр в LUT perms); переводит конец
// из системы координат st в систему координат K.
func (s *Symmetry) TransformCell(t uint8, cell int) int

// ClassRep — репрезентант класса клетки: минимум орбиты {s(cell) : s ∈ stab}. Идемпотентен.
// Тип uint8 — упаковочный тег rep:8 значения таблицы (specs/cache.md).
func (s *Symmetry) ClassRep(stab uint8, cell int) uint8

// CellOrbitSize — размер D4-орбиты пары (K, cell): 8 / |{s ∈ stab : s(cell) == cell}|;
// постоянен на классе клетки.
func (s *Symmetry) CellOrbitSize(stab uint8, cell int) int

// CanonicalClass — общая функция писателя и читателя: каноническая маска, тег класса конца,
// размер орбиты пары. Композиция CanonicalMaskFrame → TransformCell(frame, end) →
// ClassRep/CellOrbitSize; результат не зависит от выбора кадра argmin.
func (s *Symmetry) CanonicalClass(st state.State, end int) (K state.State, rep uint8, orbitSize int)
```

Горячий путь с общей маской: `CanonicalMaskFrame` один раз на маску, затем на каждый конец —
`TransformCell(frame, ·)` + `ClassRep` / `CellOrbitSize`; одиночный вызов — `CanonicalClass`.
Писатель и читатель вызывают одни и те же функции, поэтому рассогласование канонизации
невозможно по построению.

## Инварианты

- `(K, int(rep)) == ` лексминимум пар `(t(st), t(end))` по D4; `orbitSize` равен числу
  различных таких пар. Результат `CanonicalClass` инвариантен к орбите: одинаков для
  `(g·st, g·end)` при любой `g ∈ D4`.
- Пары `(K, e)`, `(K, e')` одного класса ⟺ `∃s ∈ Stab(K): s(e) = e'`; на классе постоянны
  `rep`, число продолжений `h` и `orbitSize` (стабилизаторы связанных концов сопряжены,
  значит равномощны).
- `stab`, возвращённый `CanonicalMaskFrame`, — подгруппа, фиксирующая `K`; при тривиальном
  стабилизаторе (`stab == 1<<0`) `ClassRep` — тождество на transformed-клетке, а
  `CellOrbitSize == 8`.
- `start` в канонизации не участвует: число продолжений зависит только от маски и конца
  (ADR-005).
- Горячий путь (`CanonicalMaskFrame`, `TransformCell`, `ClassRep`, `CellOrbitSize`,
  `CanonicalClass`) использует только LUT `perms`/`compose` (таблица индексов композиции D4,
  строится вместе с `perms`); замыкания `Transform` вызываются ровно один раз при построении LUT.

## Ограничения и edge cases

- Центр нечётной доски: орбита клетки = 1 (симметричен себе); при любой маске, содержащей
  центр, его класс размера 1 — центр фиксирован всем D4, `CellOrbitSize` даёт орбиту пары
  по стабилизатору маски.
- `CanonicalClass`/`ClassRep` корректны для любого индекса клетки; контракт конвейера —
  `end ∈ st` (конец посещённой маски), вызывающий отвечает за это.
- Клетки доски ≤ 8×8 ⇒ индексы < 64, тег `rep` и packed-формат значения (`rep:8`) валидны.
- `CellOrbitSize` — степень двойки из `{1, 2, 4, 8}`; деление накопленного веса класса на
  него точно (математика ADR-011, перенесённая с пары на класс).

## Тесты

`symmetry/symmetry_test.go`: инволютивность преобразований, GetCanonicalPosition/OrbitSize
(углы/рёбра/центр), составление стартовых групп. Новое контрактное ядро:

- `CanonicalMaskFrame`: `K` — brute-force минимум образов; `frame ∈ argmin`; `stab` —
  подгруппа (содержит тождество, замкнута относительно композиции через LUT) и совпадает с
  brute-force `{t : t(K)==K}`; независимость от argmin: для всех минимизирующих кадров
  downstream-результат (`ClassRep`/`CellOrbitSize` от transformed-конца) одинаков.
- `ClassRep`: идемпотентность, равенство репрезентантов у связанных стабилизатором клеток,
  отличие — у несвязанных (brute-force классы), минимум по орбите.
- `CellOrbitSize`: постоянен на классе; совпадает с brute-force числом различных пар
  `(t(st), t(end))`; degenerate: тривиальный stab → 8.
- `CanonicalClass` == brute-force лексминимум пары + число различных пар (таблично, случайные
  маски 5×5–8×8; seed фиксирован); совпадает с композицией frame-пути; golden-непрерывность:
  значения `(K, rep, orbitSize)` совпадают с каноническими парами/орбитами до перехода на
  форму «маска+класс» (бит-идентичность итогов конвейера).

## Связанные

ADR-005, ADR-016; план 18; `specs/cache.md`.
