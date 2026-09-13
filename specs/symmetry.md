# symmetry: симметрии доски (D4) и канонизация

## Ответственность

Группа симметрий квадрата D4 для сокращения поиска: канонические стартовые группы с
размерами орбит, D4-канонизация пар `(state, end)`. Горячий путь работает через
предвычисленные LUT, без замыканий и деления.

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
```

## Инварианты

- `Canonicalize` — инвариант орбиты: `Canonicalize(g(st),g(end)) == Canonicalize(st,end)`
  для любой `g ∈ D4` (умножение на g — биекция группы). Лексминимум кортежа `(t(state),t(end))`.
- `start` в канонизации не участвует: число продолжений зависит только от маски и конца
  (ADR-005).
- Горячий путь (`Canonicalize`, transformState) использует только подстановку в LUT
  `perms`; замыкания `Transform` вызываются ровно один раз при построении LUT.

## Ограничения и edge cases

- Центр нечётной доски: орбита = 1 (симметричен себе).
- `TransformStates`/`CanonicalFromStates`/`CanonicalizeWithOrbitSize` — пакетный путь для
  групповой канонизации `(mask, u₁..uₖ)` с общей маской; результат идентичен поштучному.

## Тесты

`symmetry/symmetry_test.go`: инволютивность преобразований, GetCanonicalPosition/OrbitSize
(углы/рёбра/центр), идемпотентность и D4-инвариант `Canonicalize`, идентичность пакетной
канонизации (`TransformStates`+`CanonicalFromStates`) поштучной.

## Связанные

ADR-005, ADR-016; `specs/path.md`.
