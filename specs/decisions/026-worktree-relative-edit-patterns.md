# ADR-026. Права агентов spec/benchmarker на feature-worktree — относительными паттернами `../kt-*`

## Статус
Принято (исправляет механику G3/G4 из ADR-024; замеры n/a — протокол проверки)

## Контекст

Приёмка фичи 04, фаза 1. Сессия оркестратора запущена в основном дереве, и субагент `spec` получает
абсолютные пути feature-worktree (контракт WORKTREE, ADR-024). Запись в `<worktree>/specs/...`
отклонялась permission-слоем, и агент обходил отказ через python3+bash. ADR-024 уже добавил строки
`~/work/kt-*/specs/**` и `~/work/kt-*/work/**` во frontmatter агентов `spec` и `benchmarker`, но они
не помогли. Тот же класс поломки ожидаем у G4: бенчмаркер пишет ADR тем же способом.

Разбор исходников opencode 1.18.31 (`tool/edit.ts`, `tool/write.ts`, `tool/apply_patch.ts`,
`permission/index.ts`, `util/wildcard.ts`) и эмпирический прогон показали причину:

- subject разрешения `edit` — **относительный** путь файла от корня проекта сессии:
  `path.relative(instance.worktree, filePath)`. Для соседнего worktree он выглядит так:
  `../kt-<NN>-<slug>/specs/…`;
- паттерн `~/work/kt-*/specs/**` при загрузке раскрывается в **абсолютный** путь и потому никогда не
  совпадает с таким subject. Относительный паттерн `specs/**` тоже не совпадает. Срабатывает общее
  правило `"*": deny` — оно оказывается единственным подошедшим;
- проверку `external_directory` (G2) это не затрагивает: её subject — абсолютный glob `<dir>/*`, и
  глобальный allow `~/work/kt-*/**` корректен именно там. Проблема — только в разрешении `edit`.

## Решение

Механизм — **только frontmatter агентов** (`.opencode/agent/spec.md`, `.opencode/agent/benchmarker.md`).
Ролевые исключения пишутся после общего правила в том же объекте: порядок ранжирования —
«дефолты → opencode.json → frontmatter агента», и выигрывает последнее совпадение. Поэтому глобальным
allow агентский `"*": deny` не поднять. Целевые блоки ниже — вход кодеру; остальной frontmatter не
меняется:

```yaml
# .opencode/agent/spec.md
permission:
  edit:
    "*": deny
    "specs/**": allow
    "work/**": allow
    "../kt-*/specs/**": allow
    "../kt-*/work/**": allow
  task: deny
```

```yaml
# .opencode/agent/benchmarker.md
permission:
  edit:
    "*": deny
    "specs/decisions/**": allow
    "work/**": allow
    "../kt-*/specs/decisions/**": allow
    "../kt-*/work/**": allow
  task: deny
```

Неработающие строки `~/work/kt-*` удаляются из обоих frontmatter. Файл `opencode.json` остаётся без
изменений: разрешение `external_directory` продолжает работать абсолютным паттерном
(`~/work/kt-*/**`), потому что subject там тоже абсолютный. Read-only агент reviewer и агент coder
не затрагиваются.

Отклонённые альтернативы:

- **абсолютные и тильдовые паттерны в `edit`** — не совпадают с относительным subject; поломка
  воспроизведена прогоном (см. «Замеры», runA);
- **allow в корневом `opencode.json`** — правила конфига стоят левее агентского deny и при
  last-match-wins его не переопределяют. Кроме того, такой allow расширил бы права всем агентам сразу;
- **plugin-хук на `permission.ask`** — это гейт с состоянием там, где проверяемый факт состояния не
  требует. Декларативного относительного паттерна достаточно.

## Последствия

- Контракт G3/G4: запись разрешена ролевым каталогам основного дерева и любого соседнего worktree
  `../kt-*`; оба пути отсчитываются от корня сессии. Всё остальное запрещено. Permission-слой
  сопоставляет только строки и не проверяет существование или «активность» worktree — проверки с
  состоянием остаются за G8.
- Паттерны завязаны на соглашение о соседних worktree (`../kt-<NN>-<slug>`, ADR-021) и на то, что
  сессия запущена в main (ADR-024). Смена соглашения о расположении worktree потребует переписать те
  же паттерны — связь зафиксирована в `specs/gates.md`.
- Edge cases:
  - **worktree удалён**. Паттерн продолжает совпадать: permission-слой не знает о существовании пути.
    Запись по устаревшему пути воссоздала бы каталоги — это принято. WORKTREE-контракт берёт путь из
    свежего `git worktree add`, а G8 самокорректирует своё множество активных worktree независимо;
  - **чужой каталог `~/work/<other>` не открывается**: сначала `external_directory` спрашивает (G2),
    затем разрешение на edit отказывает. Флаг `--auto` второе не снимает — проверено на runB/WRITE3;
  - **обход через bash** (`python3`, редиректы) возможен, как и в v1 у G7/G8: permission-гейт чинит
    только edit/write/apply_patch. Цель — перестать отклонять законные операции по ошибке, а не
    отражать злоумышленника.

## Замеры

n/a (процессное решение). Числа заменяет протокол проверки. Прогон выполнен на opencode 1.18.31
командой `opencode run --auto --log-level DEBUG` из основного дерева; правила инжектированы через
`OPENCODE_CONFIG_CONTENT`; песочница `~/work/kt-05-permtest/` (создана mkdir, удалена после прогона):

| # | правила | запись | итог |
|---|---------|--------|------|
| runA | текущие (`~/work/kt-*/specs/**`) | `<sandbox>/specs/a.md` | **denied** — `evaluated edit pattern=../kt-05-permtest/specs/a.md → action=deny` (баг воспроизведён) |
| runB | исправленные (`../kt-*/specs/**`) | `<sandbox>/specs/a.md` | **allowed** — матч `../kt-*/specs/**`, файл создан |
| runB | — | `<sandbox>/README.md` | denied (`*`) |
| runB | — | `~/work/other-dir-permtest/specs/x.md` | denied: external_directory → ask (автопройден), edit → deny |

Логи прогонов лежат в `work/05-spec-edit-pattern/run{A,B}.log` (локально). Приёмка фичи — сценарии
G3/G4 из `specs/gates.md`, прогнанные на реальном агентском файле со свежим процессом opencode.

## Ссылки

Код: `.opencode/agent/spec.md`, `.opencode/agent/benchmarker.md`, `opencode.json`.
Спеки: `specs/gates.md` (реестр G3/G4, секция сопоставления путей). ADR-022 (слои гейтов), ADR-024
(WORKTREE — его строки `~/work/kt-*` настоящим исправлены), ADR-021 (соседние worktree). Исходники
opencode: `packages/opencode/src/tool/{edit,write,apply_patch,external-directory}.ts`,
`packages/opencode/src/permission/index.ts`, `packages/core/src/util/wildcard.ts`.
