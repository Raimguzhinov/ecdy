# CLAUDE.md — ecdy

> **ecdy** (от *ecdysis* — линька: членистоногое сбрасывает старый панцирь, чтобы расти).
> Умный слой поверх настоящей оболочки: ты печатаешь в zsh как обычно, а ecdy сам понимает,
> что введено — команда или промпт — и отправляет промпт **любому** агенту через Agent Client Protocol (ACP).

Этот файл — главный источник правды для агента, который пишет проект. Читай его целиком перед
каждой задачей. Если что-то здесь противоречит коду — сначала обсуди с человеком, потом правь либо код, либо этот файл.

---

## 1. Видение

Сейчас стек выглядит так: эмулятор терминала → мультиплексер (tmux) → shell (zsh) → агент (claude, codex, opencode, pi).
Shell и агент живут раздельно: агент не видит, что ты делал в shell, а в агенте нет твоего zsh
(автодополнения, подсветки, алиасов, истории).

ecdy склеивает эти два слоя **без замены zsh**:

- Ты остаёшься в своём zsh с конфигом, плагинами, completion и подсветкой.
- На Enter ecdy классифицирует строку. Команду исполняет zsh как обычно. Промпт уходит агенту.
- Агент подключается через ACP, поэтому годится любой ACP-совместимый: Claude Code, Codex, Gemini CLI, OpenCode, Pi и другие.
- Агент получает контекст сессии: cwd, последние команды, коды возврата, git-ветку.
- Используются логины и подписки самих агентов. Никакого своего облака, никакой перепродажи токенов.

### Не-цели (не делать, даже если «было бы круто»)

- ❌ Не эмулятор терминала.
- ❌ Не новый язык оболочки и не реимплементация POSIX/zsh.
- ❌ Не свой агент и не свой LLM-клиент: ecdy — только ACP-клиент.
- ❌ Никакого облака, аккаунтов и телеметрии по умолчанию.
- ❌ Не заменять zsh как login shell: ecdy — плагин + бинарь.

---

## 2. Инварианты безопасности (нарушать запрещено)

1. **Промпт никогда не исполняется как команда.** Если классификатор сомневается, он не исполняет строку, а спрашивает.
   Ошибка «команду отправили агенту» дешёвая. Ошибка «промпт исполнили как команду» опасная.
   Канонический пример: `rm everything in tmp except configs` — первое слово валидная команда, и `rm` удалит файл `configs`, если он существует.
2. **Сбой ecdy не ломает shell.** Если бинарь отсутствует, падает или превышает дедлайн, zsh ведёт себя как ванильный
   (обычный `accept-line`). Это поведение задокументировано в README.
3. **ecdy сам ничего не исполняет от имени агента без явного разрешения пользователя.** Все `session/request_permission`
   показываются человеку. Режим «разрешать всё» включается только явным флагом на сессию и отображается в UI.
4. **Секреты не уходят агенту по умолчанию.** Контекст сессии проходит через редактор секретов
   (токены, `*_KEY=`, `*_TOKEN=`, `Authorization:` и т.п.). Захват вывода команд выключен по умолчанию.
5. **Сокеты и state** лежат в `$XDG_RUNTIME_DIR/ecdy/` (права 0700) и `$XDG_STATE_HOME/ecdy/`. Никогда в `/tmp` с 0777.
6. **Никаких сетевых вызовов из самого ecdy.** В сеть ходят только процессы агентов, которые запустил пользователь.

---

## 3. Архитектура

```
┌──────────────── терминал / tmux ────────────────┐
│  zsh (конфиг пользователя, completion, подсветка)│
│   └─ ecdy.plugin.zsh                             │
│        • обёртка accept-line (ZLE widget)        │
│        • хуки preexec/precmd → журнал сессии     │
│        • zshaddhistory → в историю пишется       │
│          исходная строка                         │
│          │                                       │
│          ├─ `ecdy classify`  (быстро, на каждый Enter)
│          └─ `ecdy ask -- "<промпт>"` (обычный    │
│              foreground-процесс: TTY, Ctrl+C,    │
│              job control бесплатно)              │
└──────────┼───────────────────────────────────────┘
           │ unix socket ($XDG_RUNTIME_DIR/ecdy/<session>.sock)
           ▼
     ecdy daemon (по одному на shell-сессию, запускается лениво)
           │ держит живой процесс агента → разговор продолжается между промптами
           │ JSON-RPC 2.0 по stdio (ACP)
           ▼
     ACP-агент: claude-agent-acp | codex-acp | gemini --acp | ...
```

### Ключевое решение: перезапись буфера, а не собственный line editor

Когда классификатор говорит `prompt`, ZLE-виджет **переписывает `$BUFFER`** в `ecdy ask -- <quoted>` и вызывает
`zle .accept-line`. Благодаря этому:

- `ecdy ask` становится обычным foreground-процессом с TTY, сигналами и job control, ничего не надо изобретать;
- ввод до Enter полностью остаётся за zsh, поэтому completion, подсветка, autosuggestions и vi-mode работают как раньше;
- в историю через хук `zshaddhistory` пишется **исходная** строка, а не `ecdy ask ...`.

Эскиз (реализовать, проверить и покрыть тестами; это не финальный код):

```zsh
_ecdy_accept_line() {
  emulate -L zsh
  local verdict
  verdict=$(command ecdy classify --shell=zsh --first-kind="$(_ecdy_first_kind)" -- "$BUFFER" 2>/dev/null) \
    || verdict=cmd            # инвариант 2: сбой → ванильное поведение
  case $verdict in
    prompt) _ECDY_ORIG=$BUFFER; BUFFER="ecdy ask -- ${(q)BUFFER}" ;;
    ask)    _ecdy_disambiguate; return ;;   # инвариант 1: не исполнять
  esac
  zle .accept-line
}
zle -N accept-line _ecdy_accept_line
bindkey '^[^M' _ecdy_force_command   # Alt+Enter — исполнить как команду без классификации
```

`_ecdy_first_kind` определяет тип первого слова через `whence -w`, предварительно пропустив присваивания (`FOO=1`)
и precommand-модификаторы (`sudo`, `noglob`, `command`, `builtin`, `exec`, `nocorrect`, `env`, `time`).
**Только shell знает свои алиасы и функции**, поэтому этот факт вычисляется в zsh и передаётся в Go.

Совместимость, которую надо проверить тестами: `zsh-syntax-highlighting`, `zsh-autosuggestions`
(оба оборачивают виджеты), `zsh-vi-mode`, `fzf-tab`, `atuin`. Порядок source и переопределение `accept-line`
описать в README.

---

## 4. Классификатор (сердце проекта)

Чистая функция в Go без I/O:
`Classify(input Input) Verdict`, где `Verdict ∈ {Cmd, Prompt, Ask}` плюс причина (`Reason`) для отладки.

`Input` содержит: строку, `FirstKind` (alias/function/builtin/command/reserved/hashed/none — из zsh),
cwd (для проверки существования путей), конфиг.

### Каскад правил (порядок важен, первое сработавшее побеждает)

1. **Пустая строка / только пробелы** → `Cmd` (сквозной проход).
2. **Явный override**: префикс `?` в начале строки → `Prompt` (префикс срезается). Alt+Enter → `Cmd` в обход классификатора.
   Префиксы настраиваются в конфиге.
3. **Первое слово неизвестно** (`FirstKind=none`, не путь `./x` или `/x`, не присваивание):
   - похоже на опечатку известной команды (Damerau–Levenshtein ≤ 1, остаток похож на аргументы, например `gti status`)
     → `Ask` с предложением «did you mean git?»;
   - иначе → `Prompt`.
4. **Первое слово известно.** Считаем сигналы естественного языка в остатке строки (вне кавычек):
   - буквы не из ASCII (кириллица и т.п.);
   - стоп-слова (en: the, a, all, and, please, why, how, what, every, except…; ru: как, что, почему, все, кроме, пожалуйста…);
   - `?` в конце строки;
   - ≥ 3 «словесных» аргумента, которые не флаги, не пути, не существующие файлы и не glob'ы;
   - строка не парсится `mvdan.cc/sh/v3/syntax` (bash-диалект как эвристика; zsh-специфику не считать ошибкой).

   Нет сигналов → `Cmd`. Есть сильные сигналы → `Ask`, а не `Prompt`: слово известно, поэтому решает человек.
5. **Опасная команда + любой NL-сигнал** (`rm`, `dd`, `mkfs*`, `kill`, `pkill`, `chmod -R`, `chown -R`, `git reset --hard`,
   `git clean`, `git push --force`, `shutdown`, `reboot`, `truncate`, `find ... -delete`) → всегда `Ask`,
   и в диалоге по умолчанию выбрано «отправить агенту».

### Диалог `Ask`

Одна строка под вводом, выбор одной клавишей:
`⏎ агенту · r выполнить · e редактировать · Esc отмена`. По умолчанию Enter = агенту (безопасный вариант).

### Бюджет производительности

`ecdy classify` вызывается на каждый Enter. Цель: **p99 < 15 мс** на холодном старте бинаря. Бенчмарк обязателен.
Тяжёлые проверки (существование файлов) ограничены по времени и количеству. Если бюджет не держится, делаем
быстрый путь через сокет демона (`zmodload zsh/net/socket`), но только после замеров.

### Тесты классификатора

- Golden-таблица `internal/classify/testdata/cases.tsv`: `input<TAB>first_kind<TAB>expected<TAB>comment`.
  **Минимум 200 случаев** до того, как классификатор подключается к zsh, включая русский язык,
  опечатки, пайпы, heredoc, `sudo`, присваивания, опасные команды и кейсы из раздела 2.
- Каждый баг классификатора сначала становится строкой в таблице, потом чинится.
- Fuzz-тест: `Classify` не паникует на произвольном вводе.

---

## 5. ACP-клиент

Спецификация: https://agentclientprotocol.com (читать `protocol/overview`, `initialization`, `session-setup`,
`prompt-turn`, `tool-calls`, `file-system`, `terminals`). JSON-RPC 2.0, stdio, все пути абсолютные.

Методы, которые нам нужны:

- **Client → Agent**: `initialize`, `authenticate` (если агент требует), `session/new`, `session/load`
  (если есть capability `loadSession`), `session/prompt`, `session/cancel` (notification), `session/set_mode` (опционально).
- **Agent → Client**: `session/update` (message chunks, tool calls, plans, mode changes),
  `session/request_permission`; при объявленных capabilities также `fs/read_text_file`, `fs/write_text_file`,
  `terminal/create|output|wait_for_exit|kill|release`.

### Go SDK

Использовать готовый SDK, не писать JSON-RPC руками. Кандидаты: `github.com/coder/acp-go-sdk`
(есть пример моста к Claude Code), `github.com/kdlbs/acp-go-sdk`, `github.com/keepmind9/acp-sdk-go`.
**Перед выбором** проверь, какой из них указан на странице libraries на agentclientprotocol.com, на какую версию
схемы он сгенерирован и жив ли репозиторий. Выбор оформи ADR (раздел 9).
**Не выдумывай API SDK**: сначала читай исходники или godoc, потом пиши код.

### Пресеты агентов (`~/.config/ecdy/config.toml`)

```toml
default_agent = "claude"

[agents.claude]
command = ["npx", "-y", "@agentclientprotocol/claude-agent-acp"]

[agents.codex]
command = ["npx", "-y", "@zed-industries/codex-acp"]

[agents.gemini]
command = ["gemini", "--acp"]

# opencode, pi и другие — сверить команды запуска с ACP Registry перед добавлением пресета
```

Аутентификацией владеет агент: если пользователь залогинен в CLI агента, этот логин и используется.
ecdy ключи не хранит.

### Рендеринг в `ecdy ask`

- Текстовые чанки агента стримятся в stdout сразу. Markdown рендерится по мере возможности, без ожидания конца ответа.
- Tool calls выводятся одной строкой статуса (`⚙ Read src/main.go`, `$ go test ./...` → ✓/✗), подробности по флагу `-v`.
- Permission prompt читается с `/dev/tty` в raw mode (`golang.org/x/term`), варианты берутся из запроса агента.
- `Ctrl+C` → `session/cancel`, ожидание stop reason, выход с кодом 130. Второй `Ctrl+C` — жёсткое завершение.
- Код возврата `ecdy ask`: 0 — ход завершён, 1 — ошибка агента или протокола, 130 — отмена.

### Контекст сессии

Хуки `preexec`/`precmd` пишут в `$XDG_STATE_HOME/ecdy/sessions/<ECDY_SESSION>.jsonl`:
команду, cwd, код возврата, длительность и время. При `session/prompt` к промпту добавляется ограниченный блок
(последние N=20 команд, ≤ 4 КБ, после редактора секретов) плюс cwd и git-ветка.
Вывод команд **не** захватывается по умолчанию. Опционально — через `tmux capture-pane`, если задан `$TMUX`
(решение отдельным ADR).

---

## 6. Структура репозитория

```
cmd/ecdy/            main: подкоманды init, classify, ask, daemon, use, new, agents, doctor, log, version
internal/classify/   классификатор (чистый, без I/O кроме инжектируемого FS) + testdata/
internal/acpclient/  обёртка над выбранным ACP SDK, запуск агента, маппинг событий
internal/daemon/     демон на сессию, unix socket, жизненный цикл агента, idle-timeout
internal/render/     вывод в терминал: стрим, tool calls, статусы
internal/tty/        raw-mode диалоги (permission, Ask)
internal/sessionlog/ журнал команд, редактор секретов, сборка контекста
internal/config/     загрузка TOML, дефолты, валидация
internal/testutil/   fake ACP-агент для тестов (скриптуемый), PTY-хелперы
shell/zsh/           ecdy.plugin.zsh (встраивается в бинарь через embed, отдаётся `ecdy init zsh`)
docs/adr/            архитектурные решения (NNNN-title.md)
docs/STATUS.md       текущий milestone, что сделано, что дальше
flake.nix            devShell (go, gopls, golangci-lint, zsh, tmux, nodejs для npx-агентов) + package
```

Установка для пользователя (как у atuin/zoxide): `eval "$(ecdy init zsh)"` в `.zshrc`.

---

## 7. Стек и зависимости

- **Go**: последняя стабильная версия из nixpkgs, модуль `github.com/<owner>/ecdy`.
- **Разрешённые зависимости** (всё остальное — только после вопроса человеку):
  - ACP SDK (один, по ADR);
  - `mvdan.cc/sh/v3` — парсер shell-синтаксиса для эвристик;
  - `github.com/creack/pty` — PTY в интеграционных тестах;
  - `golang.org/x/term` — raw mode;
  - `github.com/pelletier/go-toml/v2` — конфиг;
  - `github.com/spf13/cobra` — CLI;
  - `github.com/charmbracelet/lipgloss` (+ `glamour` при необходимости) — рендеринг.
- Логи: `log/slog` в файл `$XDG_STATE_HOME/ecdy/ecdy.log`, уровень через `ECDY_LOG`. **Никогда** не писать логи в stdout или stderr во время ввода.

---

## 8. Milestones

Работать строго по порядку. Каждый milestone — отдельная ветка и PR. Не начинать следующий, пока DoD текущего не выполнен
и `docs/STATUS.md` не обновлён.

**M0 — Скелет.** go.mod, flake.nix devShell, cobra-скелет, `ecdy version`, CI (GitHub Actions: `go test -race ./...`,
`golangci-lint`, `go vet`), LICENSE (Apache-2.0 — уточнить у человека), README-заглушка.
*DoD:* `nix develop -c go test ./...` зелёный, CI зелёный.

**M1 — Классификатор.** `internal/classify` + golden-таблица ≥ 200 случаев + fuzz + бенчмарк + CLI `ecdy classify`
(`--first-kind`, `--json` выводит verdict и reason).
*DoD:* таблица проходит; p99 < 15 мс по бенчмарку холодного запуска; все примеры из разделов 2 и 4 есть в таблице.

**M2 — Интеграция с zsh.** `shell/zsh/ecdy.plugin.zsh`, `ecdy init zsh`, обёртка accept-line, `?`-префикс, Alt+Enter,
`zshaddhistory`, диалог `Ask`, fail-open при отсутствии бинаря. На этом этапе `ecdy ask` — заглушка, которая печатает промпт.
*DoD:* PTY-тесты (`zsh -f` + плагин) на: команду, промпт, Ask, fail-open, запись истории, работу рядом с
zsh-syntax-highlighting и zsh-autosuggestions (подключаются в тесте из vendored-копий или из nix).

**M3 — ACP one-shot.** `ecdy ask` без демона: spawn агента → initialize → session/new → session/prompt → стрим → выход.
Permission-диалог, Ctrl+C → cancel.
*DoD:* тесты против fake-агента из `internal/testutil` (стрим текста, tool call, permission allow/reject, cancel, падение агента);
ручная проверка с claude-agent-acp и codex-acp, результат записан в STATUS.md.

**M4 — Демон и непрерывность.** Демон на сессию (`ECDY_SESSION` экспортируется плагином), ленивый старт, unix socket 0700,
завершение по `zshexit` и idle-timeout. Разговор продолжается между промптами. `ecdy new` — новая ACP-сессия,
`ecdy use <agent>` — смена агента.
*DoD:* второй промпт видит контекст первого (тест с fake-агентом); нет осиротевших процессов после выхода из shell (тест);
смена агента работает.

**M5 — Контекст сессии.** preexec/precmd-журнал, редактор секретов (таблица тестов), блок контекста в промпте,
`ecdy log` для просмотра.
*DoD:* тесты редактора секретов; размер блока ограничен; контекст виден агенту (fake-агент проверяет).

**M6 — Client capabilities.** ADR: реализуем ли `fs/*` и `terminal/*` или оставляем агенту его собственные инструменты.
Если реализуем, то `terminal/*` запускает команды в PTY с env и cwd пользователя и показывает их в терминале.
*DoD:* по ADR.

**M7 — UX.** Живой индикатор классификации при наборе (`zle-line-pre-redraw` + RPROMPT, только дешёвые проверки на стороне zsh),
markdown-рендеринг, `ecdy doctor` (проверка PATH, агентов, логина, версии zsh, конфликтов плагинов).

**M8 — Другие оболочки.** bash (через ble.sh или `bind -x`), затем fish. Классификатор общий, интеграция своя для каждой оболочки.

**Позже (не трогать без запроса):** локальная маленькая модель для случаев `Ask`; обучение на исправлениях пользователя;
агрегатор нескольких агентов в одной сессии.

---

## 9. Как работать в этом репозитории (правила для агента)

- **Не выдумывай API.** Для ACP, SDK и нетривиальных zsh-фич (ZLE, хуки, `whence`, `(q)`/`(z)`-флаги)
  сначала найди документацию или исходник и оставь ссылку в комментарии рядом с неочевидным кодом.
  Не уверен — скажи об этом прямо, не угадывай.
- **Тесты сначала** для классификатора и редактора секретов. Для остального — тесты в том же PR.
- **Маленькие диффы.** Один логический шаг на коммит, Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`).
- **ADR** (`docs/adr/NNNN-title.md`: контекст → решение → последствия) для выбора SDK, модели демона,
  client capabilities, захвата вывода и любых изменений инвариантов.
- **Спроси человека**, прежде чем: добавить зависимость вне списка; изменить поведение классификатора по умолчанию;
  сделать что-либо, что исполняет команды без подтверждения; изменить раздел 2.
- **Обновляй `docs/STATUS.md`** в конце каждой сессии: что сделано, что сломано, следующий шаг.
- Код и комментарии — на английском, документация для пользователя — на английском, этот файл — на русском.
- Go: без глобального состояния, `context.Context` первым аргументом, ошибки оборачивать через `%w`,
  без `panic` вне `main`. `golangci-lint` без исключений без комментария «почему».

### Команды

```sh
nix develop                                  # окружение
go test ./...                                # все тесты
go test -race ./...                          # перед PR
go test ./internal/classify -run TestGolden  # таблица классификатора
go test ./internal/classify -bench . -benchmem
go test ./internal/classify -fuzz FuzzClassify -fuzztime 60s
golangci-lint run
go run ./cmd/ecdy classify --json --first-kind=command -- 'rm everything in tmp except configs'
zsh -f -c 'eval "$(go run ./cmd/ecdy init zsh)"; ...'   # ручная проверка плагина
```

---

## 10. Открытые вопросы (решать через ADR, не молча)

1. Смена cwd посреди ACP-сессии: `session/new` получает cwd. Если пользователь ушёл в другой проект — новая сессия
   автоматически, по вопросу или только контекст? Черновой вариант: при выходе за git-root предлагать `ecdy new`.
2. Нужно ли держать демон, если агент поддерживает `session/load`? Возможно, для некоторых агентов достаточно one-shot + load.
3. Как показывать длинные ответы агента, не засоряя scrollback (сворачивание, pager, `ecdy last`)?
4. Разрешения «allow always» агентов vs политика ecdy: чья важнее и где хранить.
5. Имя: проверить `ecdy` на занятость в GitHub, crates/npm/nixpkgs и доменах до первого публичного релиза.
