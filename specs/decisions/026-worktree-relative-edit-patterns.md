# ADR-026. Права spec/benchmarker на feature-worktree — относительными паттернами `../kt-*`

## Статус
Принято (исправляет механику G3/G4 из ADR-024; замеры n/a — протокол проверки)

## Контекст

Приёмка фичи 04, фаза 1: сессия оркестратора живёт в main-дереве, субагент `spec` получает
абсолютные пути worktree (контракт WORKTREE, ADR-024) — но запись в `<worktree>/specs/...`
отклонялась permission-слоем, агент обходил это через python3+bash. ADR-024 уже добавил во
frontmatter `spec`/`benchmarker` строки `~/work/kt-*/specs/**` и `~/work/kt-*/work/**` — они не
помогли; тот же класс поломки ожидаем у G4 (бенчмаркер пишет ADR).

Разбор исходников opencode 1.18.31 (`tool/edit.ts`, `tool/write.ts`, `tool/apply_patch.ts`,
`permission/index.ts`, `util/wildcard.ts`) и эмпирический прогон показали причину:

- subject разрешения `edit` — **относительный** путь файла от корня проекта сессии:
  `path.relative(instance.worktree, filePath)`. Для соседнего worktree это `../kt-<NN>-<slug>/specs/…`;
- паттерн `~/work/kt-*/specs/**` при загрузке раскрывается в **абсолютный** путь и под такой
  subject не матчится никогда; относительный `specs/**` — тоже. Catch-all `"*": deny` срабатывает
  как единственный матчившийся;
- слой `external_directory` (G2) при этом проходил: его subject — абсолютный glob `<dir>/*`, и
  глобальный allow `~/work/kt-*/**` корректен именно там. Проблема — только в разрешении `edit`.

## Решение

Механизм — **только frontmatter агентов** (`.opencode/agent/spec.md`, `.opencode/agent/benchmarker.md`):
ролевые исключения пишутся после catch-all в том же объекте, т.к. правила ранжируются
«дефолты → opencode.json → frontmatter агента» и выигрывает последнее совпадение — глобальным
allow агентский `"*": deny` не поднять. Целевые блоки (вход кодеру, остальной frontmatter без изменений):

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

Мёртвые тильдовые строки `~/work/kt-*` из обоих frontmatter удаляются. `opencode.json` не меняется:
`external_directory` остаётся абсолютным (`~/work/kt-*/**`) — там абсолютный subject. Read-only
агенты (reviewer) и coder не затрагиваются.

Отклонённые альтернативы:

- **абсолютные/тильдовые паттерны в `edit`** — не матчатся относительному subject'у; воспроизведено
  прогоном (см. «Замеры», runA);
- **allow в корневом `opencode.json`** — правила конфига стоят левее агентского deny и при
  last-match-wins его не переопределяют; к тому же расширил бы права всем агентам сразу;
- **plugin-хук на `permission.ask`** — stateful-гейт там, где факт стейтлесс; декларативного
  относительного паттерна достаточно.

## Последствия

- Контракт G3/G4: запись разрешена ролевым каталогам в основном дереве (относительно сессии)
  И в любом соседнем `../kt-*` worktree; всё остальное — deny. «Активность» worktree permission не
  проверяет (матчинг строк) — за состоянием остаётся G8.
- Паттерны завязаны на соглашение о соседних worktree (`../kt-<NN>-<slug>`, ADR-021) и на то, что
  сессия запущена в main (ADR-024). Смена соглашения о расположении worktree потребует тех же
  паттернов — связь зафиксирована в `specs/gates.md`.
- Edge cases:
  - *worktree удалён*: паттерн продолжает матчить (существования permission не знает), запись по
    мёртвому пути воссоздала бы каталоги — принято; WORKTREE-контракт берёт путь из свежего
    `git worktree add`, G8 самоизлечивает своё множество независимо;
  - *чужой каталог `~/work/<other>`*: не открывается — `external_directory` ask (G2) плюс edit deny,
    и `--auto` не снимает второе (проверено runB/WRITE3);
  - *обход через bash* (`python3`, редиректы): как и в v1 G7/G8 — permission-гейт правит только
    edit/write/apply_patch; цель — снять «запретил по ошибке», не отразить злоумышленника.

## Замеры

n/a (процессное). Протокол проверки вместо чисел: opencode 1.18.31, `opencode run --auto
--log-level DEBUG` из main-дерева с инъекцией правил через `OPENCODE_CONFIG_CONTENT`, песочница
`~/work/kt-05-permtest/` (создана mkdir, удалена после):

| # | правила | запись | итог |
|---|---------|--------|------|
| runA | текущие (`~/work/kt-*/specs/**`) | `<sandbox>/specs/a.md` | **denied** — `evaluated edit pattern=../kt-05-permtest/specs/a.md → action=deny` (баг воспроизведён) |
| runB | исправленные (`../kt-*/specs/**`) | `<sandbox>/specs/a.md` | **allowed** — матч `../kt-*/specs/**`, файл создан |
| runB | — | `<sandbox>/README.md` | denied (`*`) |
| runB | — | `~/work/other-dir-permtest/specs/x.md` | denied: external_directory → ask (автопройден), edit → deny |

Логи прогонов: `work/05-spec-edit-pattern/run{A,B}.log` (локально). Приёмка фичи — сценарии G3/G4
в `specs/gates.md` на реальном агентском файле со свежим процессом opencode.

## Ссылки

Код: `.opencode/agent/spec.md`, `.opencode/agent/benchmarker.md`, `opencode.json`.
Спеки: `specs/gates.md` (реестр G3/G4, секция матчинга). ADR-022 (слои гейтов), ADR-024 (WORKTREE,
чьи строки `~/work/kt-*` исправлены), ADR-021 (соседние worktree). Исходники opencode:
`packages/opencode/src/tool/{edit,write,apply_patch,external-directory}.ts`,
`packages/opencode/src/permission/index.ts`, `packages/core/src/util/wildcard.ts`.
