# Jeeves — персональный трекер обещаний

## Overview
Jeeves — фоновый сервис на Go, который мониторит каналы коммуникаций (Telegram-группы, IMAP-почту) и автоматически отслеживает обещания пользователя. Обещания распознаются двухступенчато: быстрая модель фильтрует поток сообщений, умная модель извлекает структурированную задачу. Найденные задачи сохраняются в Obsidian-vault и отправляются уведомлением в Telegram DM.

**Проблема:** обещания, данные в чатах и на встречах, теряются — нет единого места, куда они попадают автоматически.

**Решение:** пассивный мониторинг каналов → AI-распознавание → автосохранение задачи + уведомление.

## Context (из брейнсторма)
- Проект с нуля в директории `/Users/anadale/Development/experiments/jeeves/`
- Стек: Go, go-telegram-bot-api, emersion/go-imap, sashabaranov/go-openai, BurntSushi/toml
- Архитектура: Watcher → Promise Detector (fast) → Task Extractor (smart) → Pipeline (Sink + Notifier)
- История сообщений: интерфейс History (in-memory, потом Redis)
- Состояние каналов: интерфейс StateStore (файловая система)
- Конфиг: TOML
- Развёртывание: Docker + docker-compose

## Development Approach
- **Подход к тестированию**: TDD (сначала тесты, потом реализация)
- Каждый компонент тестируется через моки благодаря интерфейсной архитектуре
- Завершить каждую задачу полностью перед переходом к следующей
- Маленькие, сфокусированные изменения
- **CRITICAL: каждая задача ДОЛЖНА включать тесты** для изменённого кода
- **CRITICAL: все тесты должны проходить перед переходом к следующей задаче**
- **CRITICAL: обновлять план при изменении скоупа**

## Testing Strategy
- **Unit-тесты**: обязательны для каждой задачи. Интерфейсы мокаются вручную (Go-стиль, без фреймворков)
- **Table-driven тесты**: стандартный подход Go для покрытия множества кейсов
- **Интеграционные тесты**: для pipeline (с моками внешних сервисов)
- Команда запуска: `go test ./...`

## Progress Tracking
- Отмечать `[x]` сразу по завершении
- ➕ — новые обнаруженные задачи
- ⚠️ — блокеры
- Обновлять план при отклонении от скоупа

## Implementation Steps

### Task 1: Инициализация проекта и базовые типы

**Files:**
- Create: `go.mod`
- Create: `cmd/jeeves/main.go`
- Create: `internal/model/types.go`
- Create: `internal/model/types_test.go`

- [x] Инициализировать Go-модуль (`go mod init github.com/anadale/jeeves`)
- [x] Создать `internal/model/types.go` — все типы: `Message`, `Reaction`, `Task`, `Source`, `Cursor`
- [x] Создать `internal/model/interfaces.go` — все интерфейсы: `Watcher`, `Detector`, `Extractor`, `Sink`, `Notifier`, `History`, `StateStore`
- [x] Написать тесты для типов (конструкторы, валидация если есть)
- [x] Создать минимальный `cmd/jeeves/main.go` (заглушка с graceful shutdown через `signal.NotifyContext`)
- [x] `go build ./...` и `go test ./...` — должны проходить

### Task 2: Конфигурация (TOML)

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `config.example.toml`

- [x] Написать тесты: загрузка валидного TOML, обработка отсутствующих полей, подстановка переменных окружения
- [x] Реализовать `Config` struct со всеми секциями: `User`, `AI` (fast/smart), `Watchers` (telegram + массив imap), `History`, `Sinks`, `Notify`, `State`
- [x] Реализовать `Load(path string) (*Config, error)` — загрузка + валидация + подстановка `${ENV_VAR}`
- [x] Создать `config.example.toml` с комментариями
- [x] Написать тесты для edge cases: пустой файл, невалидный TOML, отсутствующий обязательный параметр
- [x] `go test ./internal/config/...` — должны проходить

### Task 3: StateStore (файловая система)

**Files:**
- Create: `internal/state/store.go`
- Create: `internal/state/file.go`
- Create: `internal/state/file_test.go`

- [x] Написать тесты: сохранение/чтение курсора, несуществующий канал возвращает nil, конкурентный доступ
- [x] Реализовать `FileStateStore` — хранит JSON-файлы в указанной директории (`{channelID}.json`)
- [x] Реализовать `GetCursor` и `SaveCursor` с блокировкой через `sync.Mutex`
- [x] Написать тесты для edge cases: повреждённый файл, отсутствующая директория (создаётся автоматически)
- [x] `go test ./internal/state/...` — должны проходить

### Task 4: History (in-memory)

**Files:**
- Create: `internal/history/history.go`
- Create: `internal/history/memory.go`
- Create: `internal/history/memory_test.go`

- [x] Написать тесты: добавление сообщений, получение последних N, лимит max_messages, TTL (сообщения старше TTL не возвращаются), разделение по source
- [x] Написать тесты для `RecentActivity`: определение «окна активности» (сообщения после периода тишины), fallback на N из конфига если тишину определить не удалось
- [x] Реализовать `MemoryHistory` — `map[string][]Message` с `sync.RWMutex`
- [x] Реализовать `RecentActivity(ctx, source string, silenceGap time.Duration, fallbackLimit int) ([]Message, error)` — ищет последний временной участок (сообщения после паузы >= silenceGap), если не найден — возвращает последние fallbackLimit сообщений
- [x] Реализовать TTL-очистку через периодический sweep (горутина)
- [x] Написать тесты для конкурентного доступа (несколько горутин пишут/читают)
- [x] `go test ./internal/history/...` — должны проходить

### Task 5: AI-клиент (обёртка над OpenAI-совместимым API)

**Files:**
- Create: `internal/ai/client.go`
- Create: `internal/ai/client_test.go`

- [x] Написать тесты с httptest-сервером: успешный ответ, ошибка API, таймаут
- [x] Реализовать `Client` — обёртка над `sashabaranov/go-openai` с настраиваемым `base_url`, `model`, `api_key`
- [x] Метод `Complete(ctx, systemPrompt, userPrompt string) (string, error)` — вызов ChatCompletion
- [x] Метод `CompleteJSON[T any](ctx, systemPrompt, userPrompt string) (T, error)` — вызов + парсинг JSON-ответа
- [x] Добавить `go-openai` в зависимости
- [x] `go test ./internal/ai/...` — должны проходить

### Task 6: Promise Detector (fast model)

**Files:**
- Create: `internal/ai/detector.go`
- Create: `internal/ai/detector_test.go`

- [x] Написать тесты с мок-клиентом: явное обещание ("сделаю завтра"), реакция 👍 на просьбу, обычное сообщение (не обещание), ответ "ок" на просьбу, сообщение не от owner
- [x] Реализовать `PromiseDetector` — формирует промпт из Message + ReplyTo/Reaction, вызывает fast-модель
- [x] Промпт должен включать: имя пользователя, aliases, текст сообщения, текст родительского сообщения (если reply/reaction)
- [x] Парсинг ответа: ожидаем "yes"/"no" (или JSON), обработка неожиданного формата
- [x] `go test ./internal/ai/...` — должны проходить

### Task 7: Task Extractor (smart model)

**Files:**
- Create: `internal/ai/extractor.go`
- Create: `internal/ai/extractor_test.go`

- [x] Написать тесты с мок-клиентом: извлечение задачи с дедлайном ("сделаю завтра"), без дедлайна ("посмотрю"), из транскрипта встречи ("Григорий обещал реализовать...")
- [x] Реализовать `TaskExtractor` — формирует промпт из Message + history (полученной через `RecentActivity`), вызывает smart-модель
- [x] Промпт запрашивает JSON: `{summary, deadline, confidence}`
- [x] Преобразование относительных дедлайнов ("завтра", "вечером", "через полчаса") в абсолютные даты
- [x] Обработка случая, когда модель не может извлечь задачу (confidence < порога)
- [x] `go test ./internal/ai/...` — должны проходить

### Task 8: Pipeline (оркестрация)

**Files:**
- Create: `internal/pipeline/pipeline.go`
- Create: `internal/pipeline/pipeline_test.go`

- [x] Написать тесты с моками всех зависимостей: полный happy path (detect → history → extract → sink + notify), сообщение не от owner — пропускается, detector вернул false — пропускается, extractor вернул низкую confidence — пропускается, ошибка sink — notifier всё равно вызывается, ошибка обоих — логируется
- [x] Реализовать `Pipeline` struct с зависимостями: `Detector`, `Extractor`, `History`, `[]Sink`, `[]Notifier`, `ownerID`, `aliases`
- [x] Метод `Process(ctx, msg Message) error` — основной цикл обработки
- [x] Sink и Notifier вызываются параллельно (`errgroup`)
- [x] Логирование через `log/slog`
- [x] `go test ./internal/pipeline/...` — должны проходить

### Task 9: Obsidian Sink

**Files:**
- Create: `internal/sink/obsidian.go`
- Create: `internal/sink/obsidian_test.go`

- [x] Написать тесты: создание файла по шаблону, создание директории если не существует, имя файла из даты + summary (транслитерация/slug), задача с дедлайном и без
- [x] Реализовать `ObsidianSink` — рендерит Go-шаблон из конфига, записывает в `vault_path/folder/`
- [x] Формат имени файла: `YYYY-MM-DD-slug.md`
- [x] Обработка дублей: если файл существует — добавить суффикс
- [x] `go test ./internal/sink/...` — должны проходить

### Task 10: Telegram Notifier

**Files:**
- Create: `internal/sink/telegram_notifier.go`
- Create: `internal/sink/telegram_notifier_test.go`

- [x] Написать тесты с httptest: отправка уведомления, ошибка API, форматирование сообщения
- [x] Реализовать `TelegramNotifier` — отправляет DM через Bot API
- [x] Формат сообщения: задача, источник, дедлайн (если есть), ссылка на оригинал
- [x] Добавить `go-telegram-bot-api` в зависимости
- [x] `go test ./internal/sink/...` — должны проходить

### Task 11: Telegram Watcher

**Files:**
- Create: `internal/watcher/telegram.go`
- Create: `internal/watcher/telegram_test.go`

- [x] Написать тесты: преобразование Update в Message, обработка reply, обработка реакции, фильтрация по группам, backfill vs monitor режим
- [x] Реализовать `TelegramWatcher` — long polling через go-telegram-bot-api
- [x] Преобразование `tgbotapi.Update` → `model.Message` (включая ReplyTo и Reaction)
- [x] Интеграция с `StateStore` для сохранения позиции (update_id)
- [x] Поддержка `on_join`: "backfill" (запрос истории) / "monitor" (только новые)
- [x] `FetchHistory` — через Bot API `getUpdates` (ограничен, но достаточен для контекста)
- [x] `go test ./internal/watcher/...` — должны проходить

### Task 12: IMAP Watcher

**Files:**
- Create: `internal/watcher/imap.go`
- Create: `internal/watcher/imap_test.go`

- [x] Написать тесты с мок IMAP-сервером: получение новых писем, фильтрация по senders, обработка UIDVALIDITY change, on_first_connect backfill/monitor
- [x] Реализовать `IMAPWatcher` — периодический poll через emersion/go-imap
- [x] Подключение, SELECT папки, FETCH новых сообщений (UID > cursor)
- [x] Проверка UIDVALIDITY — если изменился, сброс курсора
- [x] Фильтрация по списку `senders` из конфига
- [x] Интеграция с `StateStore` для сохранения позиции (UID + UIDVALIDITY)
- [x] Поддержка нескольких IMAP-подключений (по конфигу)
- [x] `FetchHistory` — не требуется для IMAP (вся информация в теле письма)
- [x] Добавить `emersion/go-imap` в зависимости
- [x] `go test ./internal/watcher/...` — должны проходить

### Task 13: Сборка приложения (main.go)

**Files:**
- Modify: `cmd/jeeves/main.go`

- [x] Добавить `spf13/cobra` в зависимости
- [x] Реализовать корневую команду Cobra с флагом `--config` (по умолчанию `config.toml`) и переменной `JEEVES_CONFIG`
- [x] Инициализация всех компонентов: AI clients (fast/smart), Detector, Extractor, History, StateStore, Sinks, Notifiers, Pipeline
- [x] Запуск Watchers в отдельных горутинах, все пишут в общий `chan Message`
- [x] Основной цикл: чтение из канала → `Pipeline.Process`
- [x] Graceful shutdown: `context.WithCancel` + `signal.Notify` (SIGINT, SIGTERM)
- [x] Логирование через `slog` с настраиваемым уровнем
- [x] `go build ./cmd/jeeves/` — должен компилироваться
- [x] Ручной smoke-test с моковым LLM (Ollama или httptest) — manual (skipped - not automatable)

### Task 14: Docker и docker-compose

**Files:**
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `.dockerignore`

- [x] Multi-stage Dockerfile: build (golang:1.26) → run (alpine:latest)
- [x] docker-compose.yml: jeeves + redis (для будущего), volume для state и config
- [x] .dockerignore: .venv, .git, docs, *.md
- [x] `docker build -t jeeves .` — должен собираться
- [x] `docker compose up` — manual (skipped - docker-compose plugin недоступен в окружении)

### Task 15: Проверка приёмочных критериев

- [x] Все требования из Overview реализованы
- [x] Telegram Watcher: получает сообщения, распознаёт reply и реакции как обещания
- [x] IMAP Watcher: получает письма, фильтрует по senders, распознаёт обещания в транскриптах
- [x] Pipeline: detect → extract → save + notify работает end-to-end
- [x] Obsidian Sink: создаёт корректные Markdown-файлы
- [x] Telegram Notifier: отправляет DM с описанием задачи
- [x] StateStore: позиция каналов сохраняется между перезапусками
- [x] Graceful shutdown работает корректно
- [x] `go test ./...` — все тесты проходят
- [x] `go vet ./...` — без замечаний

### Task 16: [Final] Документация

- [x] Создать README.md: описание, быстрый старт, конфигурация, архитектура
- [x] Создать CLAUDE.md с паттернами проекта (интерфейсы, структура, соглашения)
- [x] Переместить план в `docs/plans/completed/`

## Technical Details

### Промпт для Promise Detector (fast model)
```
Ты анализируешь сообщения в групповом чате. Пользователь — {{.UserName}} (также известен как: {{.Aliases}}).

Определи, содержит ли действие пользователя обещание что-то сделать.

Сообщение, на которое пользователь отвечает:
{{if .ReplyTo}}"{{.ReplyTo.Text}}" (от {{.ReplyTo.Author}}){{else}}(нет){{end}}

Действие пользователя:
{{if .Reaction}}Реакция {{.Reaction.Emoji}}{{else}}"{{.Text}}"{{end}}

Ответь только: yes или no
```

### Промпт для Task Extractor (smart model)
```
Извлеки задачу из диалога. Пользователь — {{.UserName}}.

История диалога:
{{range .History}}
[{{.Timestamp}}] {{.Author}}: {{.Text}}
{{end}}

Обещание пользователя:
{{if .Reaction}}Реакция {{.Reaction.Emoji}} на "{{.ReplyTo.Text}}"{{else}}"{{.Text}}"{{end}}

Верни JSON:
{
  "summary": "краткое описание задачи (что нужно сделать)",
  "deadline": "ISO 8601 дата/время или null если не указано",
  "confidence": 0.0-1.0
}

Текущая дата и время: {{.Now}}
```

### Формат курсора StateStore
```json
{
  "message_id": "12345",
  "folder_id": "1234567890",
  "updated_at": "2026-04-10T23:00:00Z"
}
```

## Post-Completion

**Ручная проверка:**
- Создать Telegram-бота через @BotFather, добавить в тестовую группу
- Настроить IMAP-доступ к почтовому ящику
- Проверить сценарий: написать "сделаю завтра" в группе → задача появляется в Obsidian + DM
- Проверить сценарий: реакция 👍 на просьбу → задача появляется
- Проверить сценарий: транскрипт встречи с упоминанием → задача появляется

**Будущие улучшения:**
- Redis-реализация History
- Дедупликация задач (если одно обещание обнаружено в нескольких каналах)
- Web UI для просмотра и управления задачами
- Подтверждение от пользователя перед сохранением (inline-кнопки в DM)
- Периодическое напоминание о невыполненных задачах
