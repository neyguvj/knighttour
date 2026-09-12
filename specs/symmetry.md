# symmetry: симметрии доски (D4) и канонизация

## Ответственность

Группа симметрий квадрата D4 для сокращения поиска: канонические стартовые группы с
размерами орбит, D4-канонизация пар `(state, end)` и нормализация классов форм
(D4 ⋉ трансляции). Горячий путь работает через предвычисленные LUT, без замыканий и
деления.

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

## Публичный API

```go
const NumTransforms = 8

type Transform func(x, y, size int) (int, int)
func GetSymmetries(size int) []Transform      // 8 преобразований (только при построении LUT)

type CanonicalGroup struct {
    Positions []int
    Canonical int
    OrbitSize int
}

func NewSymmetry(size int) *Symmetry

// позиции / орбиты
func (s *Symmetry) GetCanonicalPosition(pos int) int
func (s *Symmetry) IsCanonicalPosition(pos int) bool
func (s *Symmetry) GetOrbitSize(pos int) int
func (s *Symmetry) GetCanonicalGroups() []CanonicalGroup
func (s *Symmetry) GetCanonicalGroupByPosition(pos int) CanonicalGroup

// D4-канонизация пары (state, end) -> path.Path
func (s *Symmetry) Canonicalize(st state.State, end int) path.Path
func (s *Symmetry) CanonicalizeWithOrbitSize(st state.State, end int) (path.Path, int)
func (s *Symmetry) TransformStates(st state.State) [NumTransforms]state.State
func (s *Symmetry) CanonicalFromStates(states [NumTransforms]state.State, end int) (path.Path, int)

// класс формы: D4 ⋉ трансляции (амортизированно на все концы маски)
type ShapeCtx struct{ ... }                                        // стек, без аллокаций
func (s *Symmetry) PrepareShape(st state.State, sc *ShapeCtx)      // одна нормализация маски
func (s *Symmetry) KeyFromPrepared(sc *ShapeCtx, end int) path.Path // ключ на каждый конец
func (s *Symmetry) CanonicalizeShape(st state.State, end int) path.Path // Prepare+Key обёртка
```

## Инварианты

- `Canonicalize` — инвариант орбиты: `Canonicalize(g(st),g(end)) == Canonicalize(st,end)`
  для любой `g ∈ D4` (умножение на g — биекция группы). Лексминимум кортежа `(t(state),t(end))`.
- `start` в канонизации не участвует: число продолжений зависит только от маски и конца
  (ADR-005).
- Нормализация формы: bbox к (0,0) + лексминимум пар `(shape, end_rel)` по 8 ориентациям,
  tie-break по `end`. `PrepareShape` — один раз на маску, `KeyFromPrepared` — на конец
  (амортизация нормализации по степеням конца).
- Размер bbox ≤ N×N ⇒ нормализованная маска помещается в `uint64` при N ≤ 8.
- Горячий путь (`Canonicalize`, transformState) использует только подстановку в LUT
  `perms`; замыкания `Transform` вызываются ровно один раз при построении LUT.

## Ограничения и edge cases

- Центр нечётной доски: орбита = 1 (симметричен себе).
- `TransformStates`/`CanonicalFromStates`/`CanonicalizeWithOrbitSize` — пакетный путь для
  групповой канонизации `(mask, u₁..uₖ)` с общей маской; результат идентичен поштучному.

## Тесты

`symmetry/symmetry_test.go`: инволютивность преобразований, GetCanonicalPosition/OrbitSize
(углы/рёбра/центр), идемпотентность и D4-инвариант `Canonicalize`, инвариант нормализации
формы (одинаковый ключ для всех D4/трансляций), перебор форм размера ≤ 4 против орбит
группы, переиспользование `ShapeCtx`.

## Связанные

ADR-005; `specs/shapecount.md` (зачем инвариантность к трансляциям), `specs/path.md`.
