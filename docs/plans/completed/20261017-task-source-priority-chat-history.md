# Task source, priority and agent chat history for the client redesign

## Overview

The macOS client is being redesigned (variant D, see `huskwoot-macos/docs/design/20261017-redesign-variant-d.md`). The new UI needs three things the server does not provide today:

1. **Where a promise came from.** The task panel shows the source of a promise: channel kind, chat title or email subject, the author and text of the promise message, the message it replied to, the time, and a deep link when possible. Today tasks store only `source_kind` and `source_id` (chat id or device id). The original message lives only in memory while the pipeline runs and is lost after task creation.
2. **Task priority.** The client renders priority markers (`!`, `!!`, `!!!`), lets the user change priority, and the agent should understand "urgent" or "raise the priority". There is no priority anywhere on the server.
3. **A richer agent chat history.** The client gets an "Agent" tab: the conversation from this device with text and voice messages, agent replies with cards of the tasks they touched (for "Undo"), and voice recognition failures.
   - `GET /v1/chat/history` exists but returns only `authorName`, `text` and `timestamp`.
   - The `messages` table has no role, kind, audio or task references, even though `ChatReply.TasksTouched` and the event payloads already carry this information.

All changes are additive and backward compatible. New fields are optional in responses; old clients ignore them, and requests without new fields behave as before.

## Context (from discovery)

- **Task model and storage:**
  - `internal/model/types.go:167` — `Task` has `Source` (Kind, ID, Name, AccountID) and an in-memory `SourceMessage`. No priority.
  - `internal/model/types.go:126` — `Message`. Client chat (`internal/api/chat.go:165`) and client voice (`internal/usecase/client_voice_consumer.go:139`) build identical DM messages: `Source{Kind: "client", ID: deviceID}`, nothing marks a voice origin.
  - `internal/storage/migrations/001_baseline.sql` — `tasks.source_kind`, `tasks.source_id`.
  - `internal/storage/task_store.go:344` — INSERT and SELECT column lists include the source columns.
- **Task creation paths:**
  - `internal/pipeline/pipeline.go:314` — `CreateTasks` with `taskReqs` built from the extracted message.
  - automatic tasks (groups and forwards): `AutoTaskService.Submit` in `internal/usecase/auto_tasks.go`; `createAutoTaskTx` (`:308`) builds `model.Task` field by field from `CreateTaskRequest`; candidates keep the whole request in `auto_task_candidates.payload` JSON (`015_auto_tasks.sql`, `internal/storage/auto_tasks.go:47`) and become tasks through the same function on approval (`Decide`).
  - forward batches: `processForwardBatch` in `internal/pipeline/pipeline.go:378`. The batch is formatted by `FormatForwardBatch` (`internal/pipeline/forward_format.go:22`, sorts entries by original date) into one synthetic message; `ForwardTaskExtractor.Extract` (`internal/ai/extractor.go:132`) returns tasks with no link to a particular `ForwardEntry`. `forward_dispatcher.go` only builds the envelope.
  - agent: `internal/agent/tool_create_task.go:111` — `CreateTaskRequest` without `Source`;
  - REST: `internal/api/tasks.go:218` — `CreateTaskRequest` without `Source`;
  - client voice: `internal/usecase/client_voice_consumer.go:143`.
- **Requests and updates:**
  - `internal/model/service.go` — `CreateTaskRequest` (ProjectID, Summary, Details, Topic, Deadline, Source), `TaskUpdate`, `ChatReply.TasksTouched`.
  - `internal/usecase/tasks.go:57` — `taskSnapshot` for `task_*` event payloads (snake_case keys) and `changedFields`.
  - `internal/api/tasks.go:29` — `taskDTO` (camelCase, mirrors the OpenAPI `Task` schema); `allowedTaskUpdateFields` at `:402`. The sync snapshot (`internal/api/sync.go`) reuses `toTaskDTO`.
- **Agent:** `internal/agent/` — tools `create_task`, `update_task`, `get_task`, `list_tasks`, `search_tasks`, …; the tool loop is in `agent.go:106` (`Handle`); every history entry is rendered into the prompt as `[hh:mm] AuthorName: Text` (`agent.go:164`); prompts are in `internal/agent/prompts` (ru/en pairs).
- **Extractor:** `internal/ai/extractor.go` (`extractorModelResponse` is shared by the message and forward extractors), prompts `internal/ai/prompts/extractor_*_{ru,en}.tmpl`.
- **Chat history:**
  - `internal/model/interfaces.go:37` — `History` interface; hand-written implementations in `internal/channel/telegram_test.go` and `internal/usecase/client_voice_consumer_test.go`.
  - `internal/storage/history.go` — `SQLiteHistory.AddTx` inserts into `messages (source_id, author_name, text, timestamp, message_id)`; `Recent` (`:92`) and `RecentActivity` select `author_name, text, timestamp` by `source_id` only and feed the agent context.
  - `internal/api/chat.go` — client source id `client:<deviceID>`; `authorNameForRole` stores `"agent"` or the device id as `author_name`; `POST /v1/chat` stores the user and agent messages via `insertChatEvent(role, audioJobID)` and returns `tasksTouched`; `GET /v1/chat/history` at `:301`; `chatMessageCreatedPayload` at `:57` (messageId, source, role, text, audioJobId, createdAt). `newChatHandler` (`:86`) has no task service; it is constructed in `internal/api/server.go:203`.
  - `internal/usecase/client_voice_consumer.go` — voice jobs: user transcript (with audio job id), agent reply, `voice_failed` events. When the agent mutates a task and does not answer within `ackGrace` (1.5 s), or at the soft deadline, `emitAgentReply` stores a synthetic "Готово" reply and `drainHandleMessage` (`:281`) discards the real `ChatReply`. `insertChatEvent` (`:363`) returns the stored message id. `model.VoiceJob.Duration` holds the audio length.
- **API contract:** `api/openapi.yaml` is the source of truth for the REST API.

## Development Approach

- **testing approach**: Regular (code first, then tests in the same task)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change (`go test ./...`)
- maintain backward compatibility: new JSON fields are optional; requests without them behave as before
- follow `AGENTS.md`:
  - English comments and docs;
  - `gofmt`;
  - hand-written mocks (update every implementation when an interface grows);
  - `fmt.Errorf("...: %w", err)`;
  - i18n strings and paired ru/en prompts

## Testing Strategy

- **unit tests:**
  - migrations: up/down on a fresh DB and on a DB with existing rows, including the role backfill;
  - store round-trips for priority and source details;
  - pipeline, automatic tasks and agent fill source details and priority;
  - API handlers: table-driven create/update/validation/history cases with `httptest`;
  - event payloads;
  - extractor parsing of `priority` and of the forward message index.
- **e2e tests:** the project has no UI e2e suite; HTTP-level handler tests cover the API.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

**Source details are a snapshot, not a reference.** Channel history in `messages` is trimmed by TTL and `MaxMessages`, so pointing at a message would rot. Instead each task stores a small JSON snapshot `source_details` at creation time:

- channel title or email subject;
- author and text of the promise message;
- author and text of the message it replied to;
- time and an optional deep link;
- how the task was created (`via`).

Texts are capped to keep rows small. Existing `source_kind` and `source_id` stay as they are; tasks from the agent and REST now fill them too.

**Priority is an integer 0–3** (none, low, medium, high), stored in `tasks.priority`. It is exposed everywhere a task is exposed: DTO, events, sync snapshot, agent tool output. It can be set on create and update, by the REST API and by the agent. The extractor may infer it conservatively (default 0); the value reaches the task on both the direct and the automatic path.

**Chat history gets real metadata.** New columns on `messages`:

- `role` — `user`, `agent` or `system`;
- `kind` — `text`, `voice` or `error`;
- `audio_job_id`, `audio_duration_ms`;
- `task_ids` — JSON array of task UUIDs.

They are filled by the client chat and voice paths only; channel history rows keep defaults. Existing client rows get their role from `author_name` in the migration. The agent context (`Recent`, `RecentActivity`) skips `system` rows and is otherwise unchanged. `GET /v1/chat/history` gains cursor pagination and returns the new fields plus compact task cards resolved at read time. `chat_message_created` carries `kind` and task ids so clients can render live without refetching.

## Technical Details

**Migration `016_task_priority_source.sql`:**

```sql
ALTER TABLE tasks ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN source_details TEXT NOT NULL DEFAULT '';
```

**Migration `017_chat_message_metadata.sql`:**

```sql
ALTER TABLE messages ADD COLUMN role TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN kind TEXT NOT NULL DEFAULT 'text';
ALTER TABLE messages ADD COLUMN audio_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN audio_duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN task_ids TEXT NOT NULL DEFAULT '';

-- Existing client chat rows: author_name is "agent" for agent replies and the device id otherwise.
UPDATE messages
SET role = CASE WHEN author_name = 'agent' THEN 'agent' ELSE 'user' END
WHERE source_id LIKE 'client:%';
```

Channel rows keep `role = ''`; they are never returned by the client history endpoint.

**Model:**

```go
// TaskPriority is the task importance: 0 none, 1 low, 2 medium, 3 high.
type TaskPriority int

// TaskSourceDetails is a snapshot of the message a task was created from.
type TaskSourceDetails struct {
    Name    string           `json:"name,omitempty"`    // chat title or mailbox; empty for client sources
    Subject string           `json:"subject,omitempty"` // email subject
    Author  string           `json:"author,omitempty"`
    Text    string           `json:"text,omitempty"`    // capped at 1000 runes
    At      *time.Time       `json:"at,omitempty"`
    ReplyTo *TaskSourceQuote `json:"replyTo,omitempty"` // author + text, text capped at 500 runes
    Link    string           `json:"link,omitempty"`    // deep link when the channel can build one
    Via     string           `json:"via"`               // "message" | "forward" | "chat" | "voice" | "api"
}
```

- `Task` gains `Priority TaskPriority`, `SourceDetails *TaskSourceDetails` and an in-memory `ForwardIndex int` (1-based message number inside a forward batch, 0 when unknown; never persisted).
- `Message` gains `VoiceJobID string` — set by the client voice consumer, empty everywhere else.
- `CreateTaskRequest` gains `Priority` and `SourceDetails`.
- `TaskUpdate` gains `Priority *TaskPriority`.
- `HistoryEntry` gains `MessageID`, `Role`, `Kind`, `AudioJobID`, `AudioDurationMs`, `TaskIDs`.

**Source kind and name per creation path:**

| Path | `Source` | `via` | Author / text / time |
|---|---|---|---|
| pipeline message (Telegram, IMAP) | from the message, as today | `message` | message author, text, time; reply-to; IMAP subject |
| forward batch | from the batch, as today | `forward` | the matching `ForwardEntry` (see below) |
| agent, Telegram DM | origin message `Source` (`telegram`) | `chat` | user text and time |
| agent, client chat | origin message `Source` (`client`, device id) | `chat` | user text and time |
| agent, client voice | origin message `Source` (`client`, device id) | `voice` (origin `VoiceJobID` set) | transcript and time |
| REST `POST /v1/tasks` | `{Kind: "client", ID: DeviceIDFromContext(ctx)}` | `api` | none |

`Name` stays empty for client sources: the client knows its own device names.

**Forward batches.** `FormatForwardBatch` prefixes every message with its 1-based number (`[1]`, `[2]`, …) in the sorted order and also returns the sorted entries. The forward extractor prompts (ru and en) ask the model to return `"message": <number>` for every task — the message that contains the promise. `extractorModelResponse` gains `Message int` (ignored by the message extractor); `ForwardTaskExtractor` copies it into `Task.ForwardIndex`. In `processForwardBatch`:

- a valid index maps to the sorted entry: `Author` = `ForwardFromName` (the owner's name when `IsSelfAuthored`), `Text` = entry text, `At` = `OriginalDate`, `Link` empty (forwarded DMs have no public link);
- a missing or out-of-range index falls back to batch-level details: `Name` = counterpart, `At` = the latest `OriginalDate`, no author or text.

**API `Task` (camelCase, OpenAPI):**

- `priority` (integer 0–3, always present, 0 when unset);
- `source` (object, omitted when the task has neither details nor a known kind):
  - `kind` (`telegram` | `imap` | `client`, from `Source.Kind`);
  - `name`, `subject`, `author`, `text`, `at`;
  - `replyTo { author, text }`;
  - `link`, `via`.

**Requests:**

- `POST /v1/tasks` accepts optional `priority`.
- `PATCH /v1/tasks/{id}` accepts `priority` (in `allowedTaskUpdateFields`).
- Values outside 0–3 or non-integers return 422 `unprocessable` with a message.
- `changedFields` gets `"priority"`.

**Event payload `taskSnapshot` (snake_case):** adds `priority` and `source`. `source` has `kind` (from `Source.Kind`) plus the details fields with the same names as the details struct (`replyTo` included).

**Telegram deep links.** Built only for supergroups and channels (`chat_id` starts with `-100`) as `https://t.me/c/<chat_id without -100>/<message_id>`. Empty otherwise (private chats and basic groups have no stable public link). IMAP: no link, `Subject` is set.

**Agent origin message.** `Agent.Handle` puts the incoming `model.Message` into the context (`agent.WithOriginMessage(ctx, msg)`). `create_task` reads it: `Source` = origin `Source`, `Via` = `"voice"` when `VoiceJobID` is set, otherwise `"chat"`, plus the user's text or transcript and time.

**Agent replies in the voice consumer.** The race loop already watches task mutation events. The broker delivers every event to every subscriber (the Telegram pipeline, other devices' REST calls), so these events only arm the ack timer. The task ids come from a `touchedSet` scoped to the agent call: the consumer puts it into the context it passes to `HandleMessage` (`withTouched` reuses sets already present in the context, so `ChatService` fills the same set), and the task service adds every task it mutates. When the timer fires and the set is still empty, the event came from another source and the race goes on.

- The synthetic "Готово" reply (both the `ackGrace` branch and the soft-deadline branch) stores the ids from that set as `TaskIDs` and sends them in the live payload. Without touched tasks no synthetic reply is sent; the soft deadline then fails the job as an agent timeout.
- `emitAgentReply` takes the task ids and returns the stored message id.
- `drainHandleMessage` gets the message id; when the real `ChatReply` arrives before the hard deadline, it merges `reply.TasksTouched` into the stored row with `History.SetTaskIDs(ctx, messageID, ids)` (a background context bounded by the hard deadline). No extra SSE event: the full list is visible in `GET /v1/chat/history`.
- The normal reply path stores `reply.TasksTouched` directly.

**`GET /v1/chat/history`:**

- **Query:** `limit` (default 50, max 500) and `before` (message id cursor, exclusive).
- **Page:** the newest `limit` entries older than `before`, ordered oldest to newest inside the page.
- **Response:**

```json
{
  "entries": [
    {
      "id": "…",
      "role": "user",
      "kind": "voice",
      "text": "…",
      "createdAt": "…",
      "audio": { "jobId": "…", "durationMs": 9000 },
      "tasks": [
        { "id": "…", "ref": "inbox-91", "summary": "…", "status": "open", "deadline": "…" }
      ],
      "authorName": "…",
      "timestamp": "…"
    }
  ],
  "nextBefore": "…"
}
```

- `authorName` and `timestamp` stay for backward compatibility.
- `tasks` are resolved by id at read time through the task service; deleted tasks are skipped.
- `nextBefore` is omitted on the last page.
- A `before` id that does not exist in this device's history returns 400 `bad_request`.

**`chat_message_created` payload** adds `kind`, `audioDurationMs` (voice) and `taskIds` (agent replies).

**Voice failures** are written to history as `role: "system"`, `kind: "error"`, `AuthorName: "system"` with the user-facing message, in the same transaction as the `voice_failed` event. `Recent` and `RecentActivity` add `AND role <> 'system'`, so failures never reach the agent prompt or displace conversation rows from its limit.

**Retention.** Client chat history is capped per source at 1000 messages: after each client write the oldest rows beyond the cap are deleted in the same transaction. Channel history keeps its existing TTL and `MaxMessages` rules.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): changes in this repository — migrations, model, storage, pipeline, agent, extractor, API, OpenAPI, tests, docs.
- **Post-Completion** (no checkboxes): client work in `huskwoot-macos`, deployment, manual checks against real Telegram and IMAP sources.

## Implementation Steps

### Task 1: Migrations for priority, source details and chat metadata

**Files:**
- Create: `internal/storage/migrations/016_task_priority_source.sql`
- Create: `internal/storage/migrations/017_chat_message_metadata.sql`
- Modify: `internal/storage/migrations/migrations_test.go`

- [x] add migration 016 (`tasks.priority`, `tasks.source_details`) with a `Down` section
- [x] add migration 017 (`messages.role`, `kind`, `audio_job_id`, `audio_duration_ms`, `task_ids`) with the role backfill for `client:%` sources and a `Down` section
- [x] write migration tests: up on a fresh DB; up on a DB with existing tasks and messages (defaults applied; client rows get `agent` / `user` from `author_name`; channel rows keep `''`); down restores the previous schema
- [x] run `go test ./internal/storage/...` — must pass before task 2

### Task 2: Model and task store for priority and source details

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/model/service.go`
- Modify: `internal/storage/task_store.go`
- Modify: `internal/storage/task_store_test.go`

- [x] add `TaskPriority`, `TaskSourceDetails`, `TaskSourceQuote`; add `Priority`, `SourceDetails` and `ForwardIndex` to `Task`, `VoiceJobID` to `Message`, `Priority` and `SourceDetails` to `CreateTaskRequest`, and `Priority` to `TaskUpdate`
- [x] persist and read `priority` and `source_details` (JSON, empty string means nil) in all INSERT and SELECT paths of `task_store.go`, including `CreateTaskTx`; apply `Priority` in the update path
- [x] add a helper that caps texts (1000 runes for text, 500 for reply-to text) before saving
- [x] write store tests: round-trip with and without details, priority update, capping, invalid JSON in the column yields nil details without failing the read
- [x] run `go test ./internal/storage/... ./internal/model/...` — must pass before task 3

### Task 3: Task services, events and changed fields

**Files:**
- Modify: `internal/usecase/tasks.go`
- Modify: `internal/usecase/auto_tasks.go` (`createAutoTaskTx`)
- Modify: `internal/usecase/tasks_test.go`
- Modify: `internal/usecase/auto_tasks_test.go`

- [x] pass `Priority` and `SourceDetails` through `CreateTask` and `CreateTasks`
- [x] copy `Priority` and `SourceDetails` from the request in `createAutoTaskTx`, so immediate automatic tasks and approved candidates get them
- [x] validate priority range (0–3) in `UpdateTask` and record `"priority"` in `changedFields`
- [x] add `priority` and `source` (with `kind`) to `taskSnapshot` (`makeTaskSnapshot`)
- [x] write tests: create with priority and details; automatic task created immediately and approved from a candidate keep priority and details (the candidate payload round-trips them); update priority (changed and unchanged); out-of-range priority error; snapshot JSON contains `priority`, `source.kind` and the details keys
- [x] ➕ `CreateTasks` also rejects an out-of-range priority with `usecase.ErrInvalidTaskPriority` (shared with `UpdateTask`; the API maps it in Task 5); `createAutoTaskTx` does not validate, the extractor clamps in Task 7; `"priority"` is appended after `"status"` in `changedFields`
- [x] run `go test ./internal/usecase/...` — must pass before task 4

### Task 4: Fill source details on every creation path

**Files:**
- Create: `internal/model/source_details.go` (builder from `model.Message`)
- Modify: `internal/pipeline/pipeline.go` (direct tasks and `processForwardBatch`)
- Modify: `internal/pipeline/forward_format.go`
- Modify: `internal/ai/extractor.go` (`Message` index in `extractorModelResponse`, `ForwardIndex`)
- Modify: `internal/ai/prompts/extractor_forward_system_{ru,en}.tmpl`
- Modify: `internal/usecase/client_voice_consumer.go` (`Message.VoiceJobID`)
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/tool_create_task.go`
- Modify: `internal/api/tasks.go` (REST `Source` and `via`)
- Modify: tests next to each file, including `internal/pipeline/forward_format_test.go`

- [x] implement `SourceDetailsFromMessage(msg, via)`: name from `Source.Name`, IMAP subject, author, text, time, reply-to, Telegram deep link for `-100…` chats
- [x] set details for direct pipeline tasks (`via: "message"`)
- [x] forward batches: number messages in `FormatForwardBatch` and return the sorted entries; ask for `"message"` in the forward extractor prompts (ru and en); map `ForwardIndex` to the entry in `processForwardBatch` with the batch-level fallback from Technical Details (`via: "forward"`)
- [x] set `VoiceJobID` on the message the voice consumer hands to the agent
- [x] put the origin message into the agent context in `Agent.Handle`; let `create_task` set `Source` and details from it (`via: "chat"` or `"voice"`)
- [x] set `Source{Kind: "client", ID: device id}` and `via: "api"` for REST-created tasks
- [x] write tests:
  - builder: Telegram supergroup link, private chat without link, IMAP subject, reply-to, capping;
  - `FormatForwardBatch` numbering follows the sorted order;
  - forward: valid index picks the right author, text and time; missing and out-of-range index fall back to batch-level details;
  - pipeline and automatic paths store details;
  - agent `create_task`: client chat origin gives `kind: client`, `via: chat`; voice origin gives `via: voice`; Telegram DM origin gives `kind: telegram`;
  - REST create stores `kind: client` with the device id and `via: api`
- [x] ➕ `FormatForwardBatch` output is `[N] [author, date] text`; the forward classifier prompts (ru and en) describe the `[N]` prefix too. `extractorModelResponse.Message` uses a lenient integer (number or numeric string; anything else becomes 0) so a malformed index never fails the whole response; negative values become 0. `Name` is the counterpart in both forward cases. `Name` and `Author` stay empty for client sources; `Author` falls back to the author id when the display name is empty
- [x] run `go test ./...` — must pass before task 5

### Task 5: REST API and OpenAPI for priority and source

**Files:**
- Modify: `internal/api/tasks.go`
- Modify: `internal/api/tasks_test.go`
- Modify: `api/openapi.yaml`

- [x] add `priority` and `source` to `taskDTO` and `toTaskDTO` (the sync snapshot picks them up through `toTaskDTO`); `source.kind` comes from `Source.Kind`
- [x] accept `priority` in `createTaskRequest` and in `allowedTaskUpdateFields` / `buildTaskUpdate`; 422 for values outside 0–3 or non-integers
- [x] update OpenAPI: `Task.priority`, `Task.source` (new `TaskSource` and `TaskSourceQuote` schemas), `priority` in `CreateTaskRequest` and `UpdateTaskRequest`, the "Supported fields" description
- [x] write handler tests: GET returns the new fields, POST with and without priority, PATCH priority (success, 0, out of range, wrong type), unknown fields are still rejected, sync snapshot contains the fields
- [x] ➕ `priority: null` is rejected with 422 on both POST and PATCH (0 is the way to clear it); `priority` is listed in `Task.required` because it is always present; `usecase.ErrInvalidTaskPriority` from the service is also mapped to 422
- [x] run `go test ./internal/api/...` — must pass before task 6

### Task 6: Agent tools and prompts understand priority

**Files:**
- Modify: `internal/agent/tool_create_task.go`
- Modify: `internal/agent/tool_update_task.go`
- Modify: `internal/agent/tool_get_task.go`, `tool_list_tasks.go`, `tool_search_tasks.go` (output)
- Modify: `internal/agent/prompts/agent_system_{ru,en}.tmpl`
- Modify: `internal/i18n/locales/ru.json`, `en.json` (if tool output strings are localized)
- Modify: tests next to each tool

- [x] add an optional `priority` parameter (integer 0–3 with a description of each level) to `create_task` and `update_task`
- [x] show priority in task output of `get_task`, `list_tasks` and `search_tasks` (`!`, `!!`, `!!!` before the summary)
- [x] extend the agent system prompts (ru and en) with guidance: "срочно", "важно", "в первую очередь", "urgent", "high priority" map to 3; "подними или опусти приоритет" adjusts by one; never set priority without an explicit signal
- [x] write tool tests: create with priority, update priority, invalid priority rejected with a readable error, output formatting
- [x] ➕ tool output keeps `summary` untouched and adds `priority` and `priority_marker` (`!`…`!!!`) only for priority > 0 (also in `create_task` and `update_task` responses); the prompts tell the model to render the marker before the summary. The `priority` argument accepts an integer or a numeric string in 0–3; `null` or a missing value means "not set"; anything else returns the localized `agent_priority_invalid` error before any service call. The prompts also map "сними приоритет" / "remove the priority" to 0. The `create_task` and `update_task` rows in `AGENTS.md` mention priority
- [x] run `go test ./internal/agent/... ./internal/i18n/...` — must pass before task 7

### Task 7: Extractor infers priority conservatively

**Files:**
- Modify: `internal/ai/extractor.go`
- Modify: `internal/ai/prompts/extractor_system_{ru,en}.tmpl`, `extractor_forward_system_{ru,en}.tmpl`
- Modify: `internal/ai/extractor_test.go`
- Modify: `internal/pipeline/pipeline.go` (pass the value into `CreateTaskRequest` on the direct and forward paths)

- [x] add an optional `priority` (0–3) to `extractorModelResponse` and to the parsing of both extractors; missing or invalid values become 0
- [x] update the extractor prompts: set priority only for explicit urgency ("срочно", "горит", "ASAP", "urgent"), otherwise 0
- [x] pass the extracted priority into every `CreateTaskRequest` the pipeline builds; automatic tasks get it through `createAutoTaskTx` (Task 3)
- [x] write tests: parsing with and without priority, out-of-range clamped to 0, prompt templates render in both languages, an urgent group promise ends up as a task with priority 3
- [x] ➕ `priority` reuses the lenient integer of `message` (number or numeric string); null, fractions, words and values outside 0–3 become 0. The prompts (message and forward, ru and en) ask only for 3 or 0: 3 when urgency is stated explicitly in the promise or in the request it answers, never inferred from a close deadline, topic or tone. The end-to-end group test runs the real `TaskExtractor` and `AutoTaskService` on SQLite
- [x] run `go test ./internal/ai/... ./internal/pipeline/... ./internal/usecase/...` — must pass before task 8

### Task 8: Store chat metadata in history

**Files:**
- Modify: `internal/model/types.go` (`HistoryEntry`)
- Modify: `internal/model/interfaces.go` (`History.SetTaskIDs`)
- Modify: `internal/storage/history.go`
- Modify: `internal/api/chat.go`
- Modify: `internal/usecase/client_voice_consumer.go`
- Modify: `internal/channel/telegram_test.go` (hand-written `History` mock)
- Modify: `internal/storage/history_test.go`, `internal/api/chat_test.go`, `internal/usecase/client_voice_consumer_test.go` (tests and its `History` mock)

- [x] write the new `HistoryEntry` fields in `AddTx` (defaults for channel history)
- [x] add `History.SetTaskIDs(ctx, messageID, ids)` that merges ids into the row's `task_ids`; update both hand-written mocks
- [x] write role and kind for user and agent messages in `POST /v1/chat`, and store `reply.TasksTouched` as the agent entry's `TaskIDs`
- [x] in the voice consumer:
  - store the user transcript as `kind: "voice"` with the audio job id and duration;
  - collect task ids from mutation events in the race loop; the synthetic reply stores them, `emitAgentReply` returns the message id, `drainHandleMessage` merges the late `TasksTouched` through `SetTaskIDs`;
  - store the normal reply with `reply.TasksTouched`;
  - store a `role: "system"`, `kind: "error"`, `AuthorName: "system"` entry next to every `voice_failed` event
- [x] add the per-source cap (1000) for client chat sources, applied in the same transaction after each client write
- [x] exclude `role = 'system'` rows from `Recent` and `RecentActivity`
- [x] write tests: metadata round-trip; defaults for channel rows; error entry on voice failure; synthetic reply carries the ids from mutation events and gets the late `TasksTouched` merged; `SetTaskIDs` merges without duplicates; cap trims only the oldest rows of the same source; `Recent` and `RecentActivity` skip system rows and otherwise return the same rows as before
- [x] ➕ the cap lives in `SQLiteHistory.AddTx` for `client:` sources (exported as `storage.ClientHistoryCap`), so the plain `Add` fallback is capped too; `Recent` and `RecentActivity` now also return `MessageID` and the new fields; `SetTaskIDs` on an unknown message returns an error wrapping `sql.ErrNoRows`; malformed `task_ids` read as nil. The agent reply row of a voice job is a plain text row (the live payload keeps `audioJobId`); the system error row keeps the audio job id and duration. A history write failure in `Fail` is logged and does not suppress the `voice_failed` event. The ack grace and hard deadline became consumer fields (overridden in tests through `internal/usecase/export_test.go`)
- [x] run `go test ./...` — must pass before task 9

### Task 9: History API v2 and live payloads

**Files:**
- Modify: `internal/storage/history.go` (`ListClient(ctx, source, before, limit)`)
- Modify: `internal/model/interfaces.go` (`History.ListClient`)
- Modify: `internal/channel/telegram_test.go` (hand-written `History` mock)
- Modify: `internal/usecase/client_voice_consumer_test.go` (hand-written `History` mock)
- Modify: `internal/api/chat.go` (`newChatHandler` takes `model.TaskService`)
- Modify: `internal/api/server.go` (pass the task service to `newChatHandler`)
- Modify: `internal/usecase/client_voice_consumer.go` (payload)
- Modify: `api/openapi.yaml`
- Modify: `internal/api/chat_test.go`, `internal/storage/history_test.go`

- [x] implement `ListClient` with a message-id cursor: newest `limit` entries older than `before`, returned oldest first, plus the next cursor; update both hand-written mocks
- [x] give the chat handler the task service (constructor and `server.go`)
- [x] extend `GET /v1/chat/history` with the `before` parameter, the new entry fields, `tasks` resolved through the task service (deleted tasks skipped) and `nextBefore`; keep `authorName` and `timestamp`; 400 for an unknown `before`
- [x] add `kind`, `audioDurationMs` and `taskIds` to `chatMessageCreatedPayload` in both writers (chat handler and voice consumer)
- [x] update OpenAPI: `ChatHistoryEntry`, `ChatHistoryTask`, `ChatHistoryResponse.nextBefore`, the `before` parameter and its 400 response, the `chat_message_created` payload description
- [x] write tests: first page, cursor paging to the end, unknown `before` returns 400, isolation between devices, tasks resolved and deleted tasks skipped, rows written before migration 017 come back with their backfilled role, payload JSON contains the new keys
- [x] ➕ `ListClient` returns `(entries, nextBefore, err)`, includes system rows and pages by `(timestamp, id)` (one extra row tells whether an older page exists); an unknown `before` (or one from another source) wraps the new `model.ErrHistoryCursorNotFound`, mapped to 400. `before` is an opaque string, not a UUID: rows backfilled by migration 013 have hex ids. A client row without a stored role gets it from `author_name` (the migration 017 rule). Task cards are resolved once per id per request; a task service error returns 500 and a server without a task service omits the cards. `audio` is present whenever the row has an audio job id (system error rows too), with `durationMs` 0 when unknown. OpenAPI gained `ChatHistoryResponse` and `ChatHistoryAudio`; `kind` in `ChatMessageCreatedEvent` is optional because replayed events from older servers lack it. The `chat_message_created` line in `AGENTS.md` lists the new keys
- [x] run `go test ./...` — must pass before task 10

### Task 10: Verify acceptance criteria

- [x] a task created from a Telegram group message returns `source` with kind, chat title, author, text, reply-to and a deep link (supergroup) in GET, sync snapshot and `task_created` event
- [x] a task from a forwarded conversation names the author and text of the message with the promise
- [x] a task created from email returns `source.subject`; from client chat or voice — `kind: "client"`, `via: "chat"` or `"voice"` and the user's text; from REST — `kind: "client"`, `via: "api"`
- [x] priority can be set and changed via REST and by the agent ("сделай срочной"), is inferred for urgent group promises, appears in DTO, events and agent output, and is validated
- [x] `GET /v1/chat/history` returns roles (including old rows), kinds, voice metadata, task cards (also after a slow voice reply) and failures with working pagination; old fields are still present; failures do not appear in the agent context
- [x] old clients keep working: requests without new fields, responses with unknown fields
- [x] ➕ end-to-end acceptance tests in `internal/api/acceptance_test.go` run the real pipeline, extractors, agent, chat service, voice consumer and SQLite stores behind the HTTP API (only model calls are scripted) and cover every criterion above together with the unit tests of tasks 1–9; the slow voice reply test waits for the production ack grace (1.5 s). `gofmt -w` was applied to 8 pre-existing files this branch never touched (whitespace only) so that `gofmt -l .` is empty
- [x] run the full suite: `go test ./...`
- [x] run `gofmt -l .` (no output) and `go vet ./...`

### Task 11: [Final] Update documentation

- [x] update `README.md` (REST API section: priority, source, chat history) and `AGENTS.md` if new patterns appeared (origin message in the agent context, source details snapshot, forward message numbering)
- [x] make sure `api/openapi.yaml` matches the handlers
- [x] move this plan to `docs/plans/completed/` (skipped - the exec harness moves the plan after all phases finish)
- [x] ➕ README: priority, source and agent chat history paragraphs in "HTTP API and Push Notifications", a priority bullet in "DM Agent", forward message numbering in the forwarded messages section. AGENTS.md: new "Task source details and priority" and "Origin message" sections, forward numbering and `ForwardIndex` in the forward batch flow, `priority`/`source_details` on `SQLiteTaskStore`, the migration 017 columns, the client cap, `SetTaskIDs`, `ListClient` and system-row filtering on `SQLiteHistory`, task and chat history API notes, voice failure history rows, `priority` and `source` in `taskSnapshot` and `"priority"` in the `changedFields` order. OpenAPI: `unsupported_format` added to the `VoiceFailedEvent.code` enum (the voice consumer already emitted it), and the `POST /v1/chat/voice` description mentions the failure entry in the chat history

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification:**

- Create promises in a real Telegram supergroup, a basic group, a private chat, a forwarded conversation and an email. Check the source snapshot and that the deep link opens the right message.
- Ask the agent "сделай задачу срочной" and "что у меня горит" and check priority handling in both languages.
- Record a voice note that creates a task with a slow agent reply and check the task card in `GET /v1/chat/history`.
- Send a quiet voice note and check that the failure appears in `GET /v1/chat/history` and not in the next agent answer.

**Client work (`huskwoot-macos`, stage 4 of the redesign roadmap):**

- add `priority` and `source` to the task DTO and SQLite schema in HwSync/HwDB; show the source in the task panel and the row, priority markers and picker;
- use `GET /v1/chat/history` and the extended `chat_message_created` payload for the Agent tab (stage 3).

**Deployment:** migrations 016 and 017 run automatically on start; take a database backup before upgrading production.
