# Рефакторинг структуры Task: убрать Origin, сделать Task плоской

**Дата:** 2026-04-15  
**Статус:** Approved

## Проблема

`Task.Origin` — это структура с полями `Subject`, `Account`, `Topic`, `Context` — исторически росла без чёткой концепции. В результате:

- `Origin.Account` хранит имя **проекта**, хотя называется «Account» (изначально — имя email-аккаунта)
- `Origin` смешивает классификацию задачи (`Project`, `Topic`) с вспомогательными данными (`Context`, `Subject`)
- `task.OriginMessage` неточно называет сообщение-источник

## Решение

Убрать `Origin` как вложенную структуру. Перенести её смысловые поля напрямую в `Task`. Переименовать `OriginMessage` → `SourceMessage`.

## Новая структура Task

```go
type Task struct {
    ID            string
    Summary       string     // что нужно сделать
    Details       string     // контекст/детали (было: Origin.Context)
    Project       string     // проект (было: Origin.Account)
    Topic         string     // тема (было: Origin.Topic)
    Deadline      *time.Time
    Confidence    float64
    Source        Source
    SourceMessage Message    // исходное сообщение (было: OriginMessage)
    CreatedAt     time.Time
}
```

`Origin.Subject` (тема письма IMAP) отдельным полем не нужен — доступен через `task.SourceMessage.Subject`. Копирование `tasks[i].Origin.Subject = msg.Subject` в pipeline убирается.

Структура `Origin` удаляется полностью.

## Затронутые файлы

| Файл | Изменение |
|---|---|
| `internal/model/types.go` | Удалить `Origin`, обновить `Task` |
| `internal/pipeline/pipeline.go` | `Origin.Account/Topic` → `Project/Topic`; убрать `Origin.Subject =`; `OriginMessage` → `SourceMessage` |
| `internal/ai/extractor.go` | Заполнять `Project`, `Details`, `Topic` напрямую; `OriginMessage` → `SourceMessage` |
| `internal/sink/super_productivity.go` | `Origin.Account` → `Project`; `Origin.Context` → `Details` |
| `internal/sink/telegram_notifier.go` | `Origin.Subject` → `SourceMessage.Subject`; `Origin.Account` → `Project`; `Origin.Context` → `Details` |
| `internal/sink/obsidian.go` | Те же замены + `Origin.Topic` → `Topic` |
| `*_test.go` | Обновить создание Task и ассерты |
| `CLAUDE.md` | Обновить описание структур |

## Характер изменений

Исключительно механическое переименование. Никакая логика не меняется. Компилятор выдаст все затронутые места после изменения `model/types.go`.

## Что НЕ меняется

- Интерфейс `model.Extractor` и его сигнатура
- Логика pipeline, classifiers, sinks
- Конфигурация, MetaStore, State
- Поведение системы в runtime
