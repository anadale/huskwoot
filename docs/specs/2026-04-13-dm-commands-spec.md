# Поддержка DM-команд в Huskwoot

> **Статус:** Реализовано через маршрутизацию Pipeline (Route-based routing).
> Первоначальный подход с `CommandRouter`/`CommandHandler` был заменён на декларативную маршрутизацию с параметризуемыми промптами.

**Цель:** Реализовать приём и обработку команд от пользователя через прямые сообщения (DM) в Telegram, позволяя создавать задачи-обещания с использованием натурального языка — например, «Сегодня вечером опубликую новую версию бекенда приложения Помощь».

**Контекст:** Huskwoot использует унифицированную цепочку обработки сообщений через `pipeline`, `detector`, `extractor`, `sink`. DM-команды интегрированы в эту цепочку через механизм маршрутов (`Route`), где каждый маршрут определяет свой набор детектора и экстрактора со специализированными промптами.

---

## Требования

1. **Приём команд в DM**
   - `TelegramWatcher` обрабатывает личные сообщения (DM) от владельца бота.
   - DM-сообщения передаются в `pipeline` наравне с сообщениями из групп.

2. **Идентификация DM-сообщений**
   - В `TelegramWatcher`, `convertMessage` определяет DM по `m.Chat.Type == "private"` и `m.From.ID` совпадает с одним из `OwnerIDs`.
   - DM-сообщения получают `source.Kind = "telegram"`, `source.ID = "dm"`, `source.Name = "DM"`.
   - Поле `msg.Author` устанавливается в `m.From.ID`.
   - DM от неизвестного пользователя (не в `OwnerIDs`) отфильтровываются.

3. **Маршрутизация в Pipeline (Route-based)**
   - `Pipeline` получает `[]Route` при создании. Каждый маршрут содержит предикат `Match`, флаг `UseHistory`, и экземпляры `Detector`/`Extractor`.
   - DM-маршрут: `Match` проверяет `Source.Kind == "telegram" && Source.ID == "dm"`, `UseHistory: false`.
   - IMAP-маршрут: `Match` проверяет `Source.Kind == "imap"`, `UseHistory: false`.
   - Group-маршрут: `Match` проверяет `Source.Kind == "telegram"`, `UseHistory: true`.
   - Порядок: DM → IMAP → Group (первый совпавший выигрывает).

4. **DM-специфичные промпты**
   - `DMDetectorSystemTemplate` — промпт детектора, ориентированный на команды-обещания от первого лица.
   - `DMExtractorSystemTemplate` — промпт экстрактора для извлечения задач из прямых команд пользователя.
   - Шаблоны передаются через `DetectorConfig.SystemTemplate` и `ExtractorConfig.SystemTemplate`.

5. **Безопасность**
   - Команды обрабатываются только от пользователя с ID, указанным в `OwnerIDs` конфигурации.
   - Проверка осуществляется в `TelegramWatcher` при создании источника сообщения.
   - Pipeline дополнительно проверяет `isOwner(msg.Author)` для не-IMAP источников.

## Архитектура

```
                           ┌─────────────┐
                           │   Pipeline   │
                           │              │
TelegramWatcher ──────────►│  matchRoute  │
(DM: Source.ID="dm")       │     │        │
                           │     ▼        │
                           │  ┌────────┐  │
                           │  │ Route  │  │     ┌──────────────┐
                           │  │  DM    │──│────►│ DM Detector  │
                           │  └────────┘  │     │ DM Extractor │
                           │  ┌────────┐  │     └──────────────┘
IMAPWatcher ──────────────►│  │ Route  │  │     ┌──────────────┐
                           │  │  IMAP  │──│────►│   Detector   │
                           │  └────────┘  │     │   Extractor  │
                           │  ┌────────┐  │     └──────────────┘
TelegramWatcher ──────────►│  │ Route  │  │     ┌──────────────┐
(Group: Source.ID=chatID)  │  │ Group  │──│────►│   Detector   │
                           │  └────────┘  │     │   Extractor  │
                           └──────────────┘     └──────────────┘
                                  │
                                  ▼
                           ┌──────────────┐
                           │ Sinks +      │
                           │ Notifiers    │
                           └──────────────┘
```

## Реализованные изменения

- **`internal/pipeline/pipeline.go`** — структура `Route`, метод `matchRoute`, конструктор `New` принимает `[]Route`
- **`internal/ai/detector.go`** — параметризуемые шаблоны в `DetectorConfig`, `DMDetectorSystemTemplate`
- **`internal/ai/extractor.go`** — параметризуемые шаблоны в `ExtractorConfig`, `DMExtractorSystemTemplate`
- **`internal/watcher/telegram.go`** — распознавание DM в `convertMessage` по `OwnerIDs`
- **`cmd/huskwoot/main.go`** — сборка маршрутов DM → IMAP → Group

## Отличия от первоначальной спецификации

Первоначальная спецификация предполагала создание `CommandRouter` (отдельный экстрактор для DM) и `CommandHandler` (отдельный sink). Реализация использует более простой подход — маршрутизацию с параметризуемыми промптами:

- Вместо `CommandRouter` — DM-маршрут со специализированными промптами детектора и экстрактора.
- Вместо `CommandHandler` — те же Sink и Notifier, что и для остальных маршрутов.
- Вместо `TaskManager`/`TaskStore` — не потребовались, задачи обрабатываются единообразно.
- Вместо if-ветвления по `Source.ID == "dm"` в Pipeline — предикатная цепочка Route.
