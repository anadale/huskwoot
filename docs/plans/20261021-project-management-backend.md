# Project management: backend contracts and isolated acceptance

## Overview

Implement the backend half of project creation, editing, synchronized palette-slot color and deletion. The server remains authoritative: deleting a project atomically transfers every open/done/cancelled task to Inbox with new Inbox numbers, reassigns automatic candidates and removes channel bindings. Deliver uncached snapshot project reads and ordered durable HTTP SSE before the dependent macOS implementation.

This is a native **single-checkout** plan. All persistent code, tests, documentation and task commit scope are inside `/Users/anadale/Development/github/anadale/huskwoot`. No task permits persistent writes in another checkout. The historical scope source is [the original plan](../../../huskwoot-macos/docs/plans/20261020-project-management.md); leave that file unchanged and never move it. The next dependent scope is [the macOS native plan](../../../huskwoot-macos/docs/plans/20261021-project-management-macos.md). Sibling paths are context/prerequisites, never Files/commit scope. This backend plan has no prerequisite on implementation of the client.

Acceptance is observable through real local HTTP, real isolated SQLite and deterministic regressions: color survives reopening; Inbox rejects name/slug changes and deletion; generated slugs avoid collisions; deletion moves every task in old-number order and leaves candidates/bindings consistent; automatic Submit re-evaluates Inbox and preserves receipt/Finalize atomicity; missing destinations produce actual HTTP404 on both task-create boundaries; snapshots cannot return stale cached projects beyond their cursor; HTTP replay/live frames follow durable seq independently of Notify order, including cursor0, reset and error boundaries. Actual purge frames and ordered event sequences become evidence consumed by client acceptance. This plan does not claim UI, Swift decoding, client acknowledgement or cross-component acceptance complete.

Non-goals: offline project mutations, manual project ordering, agent tools for delete/color, changing slug when renaming, UI description/alias editing, fixing lost-success task-create idempotency (existing project Idempotency-Key stays), transactional server snapshots, all closed-history synchronization, retention TTL/SQL changes or reservations, new outbox, global event-source serialization, network RW gate, deployment or real-account integrations.

## Context (from discovery)

Sources read: the complete original 749-line plan (including long raw lines), backend `AGENTS.md`, canonical `planning-make/references/plan-format.md`, and representative completed backend plan `docs/plans/completed/20261020-email-auto-task-dedup.md` (format/approach excerpt). The source plan records the following implementation seams; these are carried forward, not a claim that implementation has occurred:

- `internal/api/projects.go`: project DTO, POST/PATCH request types and list/get/create/update handlers; `server.go` routes/config; `errors.go` classification; `api/openapi.yaml` is authoritative and changes before implementation.
- `internal/model/types.go`, `service.go`, `interfaces.go`, `event.go`: Project/ProjectUpdate/CreateProjectRequest, project service/store interfaces and domain events. Interface extensions migrate all hand-written mocks in the same task.
- `internal/usecase/projects.go`: transaction-owning project service, snapshots/changedFields, cache invalidation, channel resolver/binding; `tasks.go`: task moves, typed missing-project error seam and wrapped task payloads; `auto_tasks.go`: revision/read/assess/commit, replay and transactional Decide.
- `internal/storage/task_store.go`: Inbox found by name `Inbox`, DefaultProjectID, tx-aware project/task operations, MoveTaskTx allocating target task_counter; `cached_task_store.go` caches ListProjects. Renaming Inbox while preserving slug currently breaks initialization on restart rather than creating a second Inbox.
- Candidate FK lacks cascade; `auto_tasks.go` storage owns both project_id and JSON payload, and reads Task.ProjectID from payload. MetaStore owns channel_projects (`project:<channelID>`), without a project FK. Aliases cascade; foreign_keys is enabled. voice_queue.project_id is a nonbinding hint already tolerant of a missing project.
- `cmd/huskwoot/main.go` currently constructs AutoTaskStore only inside Telegram setup after ProjectService. Raw sqliteTaskStore already exists; API TaskStore is currently cached. `internal/api/sync.go` reads MaxSeq then projects then open tasks; this remains nontransactional.
- `internal/events/store.go` reads SinceSeq ORDER BY seq ASC. Broker carries payloads in Notify arrival order, buffer64; voice consumes Event.Kind directly. HTTP currently mixes replay/live, subscribes too late and does not replay explicit0. `DeleteOlderThan` uses created_at, which can be source-supplied, so non-prefix holes cannot all be detected by MinSeq.
- Existing local HTTP composition: `internal/api/events_test.go`, `server_test.go`, `auth_test.go` use real SQLite/services/broker and httptest. Normal daemon config requires Telegram or IMAP; a fake token is not safe API-only isolation.
- Backend rules require English documentation/comments, idiomatic Go, gofmt, OpenAPI-first API changes, TDD and hand-written mocks. This overrides the original plan's generic code-then-tests wording. Planning creates no implementation, commits, branch/init/startup or settings changes.

## Development Approach

Retain original implementation boundaries and dependencies: Tasks1→2→3→4→5; acceptance6 after implementation; documentation7 last. Write regression tests before implementation as backend rules require. Workers skip builds, lint, tests and formatters mid-flight. At the writer-free end of **each task**, the integration owner runs its required checks and actual changed-path smoke, records truthful proof and only then creates its authorized task commit. Task6 repeats full acceptance on the integrated result; it does not defer proof required for earlier commits. No implementation verification runs while drafting this plan.

Clean cutover: HTTP SSE stops forwarding Broker payloads, snapshot stops using cached ProjectService, and every affected interface implementation migrates in its owning task. Broker API/voice consumers remain unchanged. Requests omitting color stay valid; older client response schemas may omit color, while this server always emits it. Material scope deviations require agreement and a recorded mapping, not silently dropped criteria.

## Testing Strategy

Use table-driven Go tests, real SQLite transaction fixtures, real HTTP handlers, channel barriers and context deadlines—not sleep as ordering proof. Permanent regressions target behavioral boundaries, rollback, missing destinations, event ordering, candidate decisions, replay and retention/errors, not source-text/wiring assertions.

At each implementation task's writer-free boundary run `go test ./...` as required by the original plan; format changed Go files and check `gofmt -l .` before the task commit. Exercise the changed storage/use-case/HTTP path against isolated synthetic SQLite or loopback HTTP and record its observable result, not compilation alone. Task6 runs the final `go test ./...`, `go vet ./...`, and `gofmt -l .` (empty output), including `TestOpenAPIYAMLIsValid`. Focused suites may isolate failures: `go test ./internal/storage/... ./internal/usecase/... ./internal/api/... ./internal/agent/... ./internal/pipeline/...`. No daemon or external AI/Telegram/IMAP/push/voice/reminders/retention runner starts.

Mandatory runtime smoke uses a disposable Go main inside this module, temporary file-backed SQLite, real api.New(Config).Handler(), ProjectService/TaskService/AutoTaskStore/broker, uncached API TaskStore, synthetic device and random bearer token hashed for auth, and httptest loopback HTTP. No Chat/PairingService/VoiceQueue or deliveries are connected. Reopen server on the same database to prove persistence. Capture redacted responses and actual SSE frames; never save tokens, hashes or private data in documentation. Remove harness and database before any commit. The macOS plan separately owns the original cross-component HTTP→APIClient/SyncEngine smoke and may recreate an ephemeral Go fixture here solely for verification, removed before commit; it may not persist backend changes.

## Progress Tracking

All checklist items start unchecked. Record actual command results, named regression scenarios and redacted HTTP/frame evidence in this plan only after execution. Never mark client acceptance from backend-only evidence. Record blockers and agreed deviations in plain English, preserve task numbering and criterion IDs. Completion is implementation plus evidence, and any workflow-required authorized task commit; no premature completion or source-plan edits.

## Solution Overview

Keep transaction ownership in use cases and table ownership in stores. Delete reassigns candidates, removes bindings, moves tasks and deletes project, records task_moved events then project_deleted, commits, invalidates cache and notifies. This avoids FK failures and stale candidate payloads. Automatic Submit treats a vanished non-Inbox destination as a reason to repeat the full revision/reference/assessment cycle for Inbox, not mutate already assessed candidates.

Use theme-slot names rather than concrete colors. Generated slugs choose a free suffix in the create transaction; explicit collisions remain409. Use the existing uncached API TaskStore seam instead of adding config or unwrapping decorators. HTTP SSE treats Broker/heartbeat as wakeups and SQL as the sole payload authority; this orders retained events without changing voice/Broker interfaces or introducing an outbox. A transactional server snapshot and retention redesign are explicitly not substituted for the bounded original scope.

## Technical Details

**Color and REST contracts.** `color` is `""` (Auto) or `blue`, `purple`, `teal`, `orange`, `pink`; slot indexes0…4, System-light reference colors respectively #007AFF/#AF52DE/#30B0C7/#FF9500/#FF2D55. Themes reinterpret slots (Spruce blue is spruce green), never store a hex color. Migration `018_project_color.sql`: `ALTER TABLE projects ADD COLUMN color TEXT NOT NULL DEFAULT ''`, with goose Down. `model.Project.Color string`, `ProjectUpdate.Color *string`, `CreateProjectRequest.Color string`. All project SELECTs—GetProject/GetProjectTx, ListProjects, FindProjectByName—and insert/update include color; ensureInbox relies on default empty string.

Project response schema declares color optional for compatibility, but server DTO/projectSnapshot always emit `json:"color"`. POST/PATCH absent color retains defaults/current state; empty string resets; invalid value is ErrColorInvalid→422 `color_invalid`. Color-only PATCH counts as provided, avoiding `no fields provided`. changedFields exact order: name, description, slug, color, aliases. Inbox determined by DefaultProjectID rejects any supplied Name or Slug update with ErrInboxProtected→403 `inbox_protected`; Color/Description remain allowed. Ordinary rename never changes slug. Agent update_project must expose a comprehensible localized protected error, not500; add paired locale strings only if necessary.

Both projects.name and projects.slug are UNIQUE. For inferred slug in CreateProject/EnsureChannelProject, find first unused Slugify(name), then -2, -3… in the same transaction. Rename Работа→Дом then create Работа yields rabota-2; different names yielding x allocate x/x-2. Explicit occupied slug remains409.

DELETE `/v1/projects/{id}` returns200 `{"movedTasks":[Task]}` using full REST task DTOs in old-number ascending order,403 `inbox_protected`,404 `not_found`. `EventProjectDeleted = "project_deleted"`, entity_id project UUID, payload `{"id":"<uuid>"}`; no push notification. task_moved uses wrapTaskPayload(task,nil). Existing task_deleted remains `{"task":{...}}`, not top-level id; project_deleted is intentionally different. Actual PurgeClosedOlderThan→event store→HTTP SSE supplies Swift fixture data.task.id. GET task is full open/done/cancelled; soft DELETE→GET200 cancelled, purge→GET404. No new GET endpoint or snapshot wire shape.

POST `/v1/tasks` absent destination must be404 `not_found` before initial project read and when deletion wins before tx write. CreateTasks GetProject nil returns/wraps ErrProjectNotFound with English text; preserve errors.Is and existing HTTP classifier/storage path. No Russian-string heuristic, new public error code, or interpreting arbitrary500 as deletion. 422 means invalid input.

**Store ownership and deletion.** Add comments and exact methods:

- `AutoTaskStore.ReassignProjectTx(ctx, tx, fromProjectID, toProjectID string) error`: select candidates of every status; decode payload, set Task.ProjectID, encode and update JSON and project_id, preserving question/decision metadata.
- `MetaStore.DeleteProjectBindingsTx(ctx, tx, projectID string) error`: DELETE channel_projects WHERE project_id=?, other bindings unchanged.
- `TaskStore.DeleteProjectTx(ctx, tx, projectID, inboxID string) ([]string,error)`: select every task id in old number order regardless status; MoveTaskTx each to Inbox allocating after current Inbox task_counter; DELETE project, zero rows is not-found; return ordered ids. Alias rows cascade with foreign_keys ON. CachedTaskStore embeds method; use case invalidates after commit.
- `ProjectService.DeleteProject(ctx,id) ([]model.Task,error)` and required `ProjectServiceDeps.AutoTasks model.AutoTaskStore`. Construct `autoTaskStore := storage.NewSQLiteAutoTaskStore(db)` before NewProjectService, unconditionally even without Telegram, and share with AutoTaskServiceDeps.Store. Real deletion fixtures use SQLiteAutoTaskStore, including dormant candidates from past Telegram runs.

Use-case tx order: reject Inbox; ReassignProjectTx→DeleteProjectBindingsTx→DeleteProjectTx (project last due candidate FK)→GetTaskTx each moved id→one task_moved per task→project_deleted. Missing project maps ErrProjectNotFound; commit, then invalidateProjectCache and Notify events in order. All writes/events/push behavior use existing atomic use-case pattern; unknown project_deleted templates drop push. Failure rolls back all related state.

**Binding/resolver.** EnsureChannelProject distinguishes name absent at external FindProjectByName from found P. Found P must be re-read via existing GetProjectTx within bind transaction: error propagates, nil returns ErrProjectNotFound/rollback, no SetTx/new P/project event/Notify; bind using fresh object. Initially absent name retains valid create plus inferred slug algorithm. No FindProjectByNameTx or silent Inbox redirection for explicit set_project. ResolveProjectForChannel empty binding→Inbox; nonempty→uncached GetProject, nil→Inbox, error propagates. Resolver is read-only, never blindly deletes a binding that might now point to Q. Deletion after successful resolver but before explicit task create remains ErrProjectNotFound; only Submit has retry-to-Inbox. Ordinary agent create_task must use real resolver in regression, not substitute Submit.

**Automatic Submit revision/replay/Finalize.** Internal errAutoTaskDestinationMissing only on GetProjectTx returning nil,nil—not generic FK/SQLite/string errors. readSubmission first handles original InputKeys, receipts/replay and Finalize, so committed replay after project deletion needs no old destination and does not assess again. New input reads destination before ReferencesTx, assessment stays outside tx. Submit holds effective projectID: missing non-Inbox at read or commit switches to Inbox and repeats full revision→refs→assess→commit with new candidates and within-batch references. Never patch assessed candidates. A changed revision repeats current destination; next read detects deletion. Preserve Tasks/InputKeys/SourceText/Finalize and extraction; do not re-extract. commitSubmission checks revision→project→candidates/tasks/events/receipts/Finalize atomically. If Submit commits first, deletion moves saved data; if deletion first, reassess Inbox. Missing Inbox, cancellation and unrelated errors terminate, not loop. Decide retains fresh GetCandidateTx and no redirection; reassignment covers all statuses and question metadata. Forward confirmation uses result.Created[0].ProjectID to name actual Inbox, not captured name, with no extra Submit or postcommit GET.

**Raw snapshot dependency injection.** Only api.Config.TaskStore in main becomes sqliteTaskStore, same DB as Events. Pipeline/ProjectService/agent remain cached. `syncHandler.projects func(context.Context) ([]model.Project,error)`; constructor `newSyncHandler(projects func(context.Context) ([]model.Project,error), tasks model.TaskService, events model.EventStore, logger *slog.Logger) *syncHandler`; pass cfg.TaskStore.ListProjects. Snapshot route requires Events/Tasks/TaskStore, not Projects; voice GetProject stays compatible, project REST unchanged. Read order S=MaxSeq→uncached projects→open tasks remains, SQL error→500, no cached fallback, runtime decorator extraction or new Config field. Collections can describe different moments after S; client detects contradictions atomically, original S is not a transactional instant. Targeted late task GET may be newer than S; client keeps S and ordered replay may temporarily regress then restore state or stop on an unapplicable intermediate event. Do not promise all closed history or exact S.

**Ordered durable HTTP SSE.** Implement internal `parseLastEventID(header string) (seq int64, valid bool)` and `drainThrough(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cursor int64, target int64) (delivered int64, continueStream bool)`. Valid nonnegative int64 including0 means replay. Missing/malformed/negative/overflow preserves new-events-only policy from initial MaxSeq. Subscribe and establish initial mark before200/first Flush. Broker messages and keepalive each capture fixed target=MaxSeq and read SQL SinceSeq pages. For EVERY page load into memory, then check MinSeq against page-start cursor and MaxSeq before any frame: min>0 && min>cursor+1 or cursor>Max triggers reset/close. Deliver only seq<=target; do not acknowledge above-target rows, reread next wake. Advance cursor only after successful write. Numeric gaps alone are not loss. Empty page before target requires rereading bounds, reset on detectable loss, otherwise close without advancement. At target return to select instead of chasing endless commits. Late Notify<=cursor is empty wake; heartbeat detects commit without Notify. Closed/overflowed broker, context cancellation, DB/write failure ends HTTP without arrival-order payload fallback or false acknowledgement. Preserve EventStore interfaces, Broker Subscribe/Notify/IsActive, buffer64 and voice payload consumers.

Reset remains `event: reset`, `id: MaxSeq`, `data: {"last_seq":MaxSeq}` for cursor>Max or min>cursor+1; requires resync, not database acknowledgement. Keepalive is not acknowledgement. Default retention168h and DeleteOlderThan(created_at) unchanged: source timestamp inversions allow non-prefix deletion invisible to MinSeq. Guarantee seq order for retained durable events and detectable boundaries on each page, not absolute completeness under arbitrary non-prefix retention; no hidden TTL/SQL reservation changes.

## What Goes Where

Implementation Steps cover backend-only code/tests/docs and all available local server acceptance. The dependent client plan owns client implementation, client test/lint suites, UI Preview review and the original shared local HTTP→Swift smoke; that smoke remains mandatory local acceptance, not Post-Completion. It may read backend source and instantiate/remove a verification-only harness but cannot commit backend files. Task6 provides redacted reproducible source evidence (regression names, actual captured frames and setup instructions); client acceptance consumes actual frames, never hand-invents duplicated data or claims decoder success from backend tests.

Original task mapping:

| Original scope | Backend owner | Other owner |
|---|---|---|
| Tasks1–5, including all sub-bullets | Tasks1–5 respectively, each with its own pre-commit proof; final acceptance repeated in6 | none |
| Tasks6–13 | none | client Tasks1–8 respectively |
| Task14 server checks and server parts of shared checks | Task6 mapping below | client Task9 owns every client half and combined end-to-end evidence |
| Task15 backend AGENTS/README | Task7 | client Task10 owns CLAUDE/design and its own move |
| Task15 original plan move | intentionally replaced by each native plan moving itself | original historical source remains unchanged |

Task14's 22 top-level criteria are numbered by source order for unambiguous ownership:

| Source criterion | Backend Task6 obligation | Client Task9 obligation |
|---|---|---|
|14.1 CRUD/UI/Inbox|real server CRUD and Inbox protection|store/UI behavior|
|14.2 deletion/all statuses/Q|real all-status moves and exact Inbox numbers|UI confirmation, stale Q hydration|
|14.3 candidates/bindings|candidate approval and resolver Inbox|external Telegram only remains manual|
|14.4 color/restart/Auto/old server|REST color persistence/reopen/reset|theme Auto and old-server decoding|
|14.5 late project responses|provide real project event contract|all freshness/Project?/UI/local-log assertions|
|14.6 late DELETE/local journal|full movedTasks and ordered events|journal, logical deletion, pending/FK/hydration|
|14.7 resync ownership|server interfaces/order remain compatible|entire lifecycle/drainer-generation criterion|
|14.8 stale successful task response|real task DTO semantics unchanged|atomic acknowledgement/pending/id persistence|
|14.9 atomic snapshot/slug/raw|uncached snapshot/window/error500; no transactional claim|atomic apply/retry/slug/full mark|
|14.10 unapplied event/reset/cursor|actual cursor0/reset wire semantics|acknowledgement, bounded pause and lifecycle|
|14.11 pending/id collision|no client implementation|all pending/selection/correlation checks|
|14.12 real raw snapshot/ordered HTTP|all server barrier/page/retention/error scenarios and actual ordered frames|apply real frames through engine/repository, final state/cursor|
|14.13 actual purge/wrapped frame|PurgeClosedOlderThan→durable event→real HTTP frame/GET404|Swift decoder, cached-row deletion, malformed wrapper, blocked pending lifecycle|
|14.14 late GET/S/replay guarantee|nontransactional S and retained-order/retention limits documented and demonstrated|original S, transient replay, stop N/N+1 and bounded recovery|
|14.15 real POST404 boundaries/recovery|real HTTP404 at both boundaries, GETproject404/GET200 and422 distinctions|before-SSE recovery, conditional errors/queue/pause/Refresh|
|14.16 Submit/binding|all reassessment/replay/Finalize/Decide/binding/resolver-agent scenarios|consume resulting contracts, no replacement proof|
|14.17 isolated HTTP CRUD/restart|complete isolated real-server smoke below|reuse exact setup for cross-component fixture|
|14.18 same environment HTTP→Swift|prove POST old-id404 plus GET404; deterministic second boundary in5|owns full APIClient/SyncEngine pending rewrite→Inbox smoke, ephemeral backend harness only|
|14.19 fresh DB S0/event/header0|prove real S0→commit before connect→replay0|consume prerequisite and deterministic client reset/error/stop/atomicity tests|
|14.20 visual Preview|none; not server evidence|all Preview surfaces, not restart/SSE evidence|
|14.21 server commands|go test ./..., gofmt -l ., plus repo go vet|none|
|14.22 client commands|none|all five Swift packages and lint|

Post-Completion contains only original manual/external deployment/account/device/App-build checks; none of the local server or cross-component smoke obligations move there.

## Implementation Steps

### Task 1: Persist and expose project palette-slot color

**Files:**
- Create: `internal/storage/migrations/018_project_color.sql`
- Modify: `internal/model/types.go`
- Modify: `internal/model/service.go`
- Modify: `internal/storage/task_store.go`
- Modify: `internal/usecase/projects.go`
- Modify: `internal/api/projects.go`
- Modify: `internal/api/errors.go`
- Modify: `api/openapi.yaml`
- Modify: `internal/storage/task_store_test.go`
- Modify: `internal/usecase/projects_test.go`
- Modify: `internal/api/projects_test.go`

- [ ] Change OpenAPI first for Project/create/update color; write failing-regression expectations first, then implement migration018 with Down, model fields and every project read/write including FindProjectByName and default Inbox.
- [ ] Implement exact allowed slots/ErrColorInvalid, absent versus empty reset semantics, color-only PATCH acceptance, DTO/snapshot color and changedFields order name/description/slug/color/aliases.
- [ ] Add storage migration/persistence/read tests, create/color-only PATCH/reset/invalid422 tests and project_updated color/order regressions.
- [ ] After all Task1 writers finish, the integration owner formats changed Go, runs `go test ./...` and checks `gofmt -l .`; exercise color persistence/reopen and color-only PATCH/reset/invalid422 on isolated storage/HTTP. Record checks and actual smoke before the Task1 commit; workers do not run checks mid-flight.

### Task 2: Protect Inbox and allocate unused generated slugs

**Files:**
- Modify: `internal/usecase/projects.go`
- Modify: `internal/api/projects.go`
- Modify: `internal/api/errors.go`
- Modify: `api/openapi.yaml`
- Modify: `internal/usecase/projects_test.go`
- Modify: `internal/api/projects_test.go`
- Modify: `internal/agent/tool_update_project.go`
- Modify: `internal/i18n/locales/ru.json`
- Modify: `internal/i18n/locales/en.json`

- [ ] OpenAPI-first document inferred-slug allocation and PATCH403; implement transaction-local Slugify/-2/-3 allocation in CreateProject/EnsureChannelProject only when slug inferred, explicit collision409 unchanged.
- [ ] Add rename Работа→Дом/recreate Работа→rabota-2, colliding inferred names→x/x-2 and explicit occupied slug409 regressions.
- [ ] Add ErrInboxProtected for any Inbox Name/Slug request, retain Color/Description; map403 inbox_protected; test Inbox rename failure/color success and ordinary rename.
- [ ] Check actual agent update_project protected-error path and add meaningful localized error handling/paired locale text only if needed; do not turn protected operation into500.
- [ ] After all Task2 writers finish, the integration owner runs `go test ./...` and formatting checks; exercise inferred versus explicit slug collisions and Inbox protection/color allowance through isolated project-service/HTTP calls before the Task2 commit. No worker checks mid-flight.

### Task 3: Implement transaction-aware project deletion store operations

**Files:**
- Modify: `internal/model/interfaces.go`
- Modify: `internal/storage/task_store.go`
- Modify: `internal/storage/auto_tasks.go`
- Modify: `internal/storage/meta_store.go`
- Modify: `internal/reminder/builder_test.go`
- Modify: `internal/usecase/tasks_test.go`
- Modify: `internal/usecase/projects_test.go`
- Modify: `internal/api/chat_voice_test.go`
- Modify: `internal/storage/task_store_test.go`
- Modify: `internal/storage/auto_tasks_test.go`
- Modify: `internal/storage/meta_store_test.go`

- [ ] Add documented TaskStore.DeleteProjectTx, AutoTaskStore.ReassignProjectTx and MetaStore.DeleteProjectBindingsTx with exact Technical Details contracts; migrate every hand-written implementation in the same task, locating additional implementations by signatures before editing.
- [ ] Write and implement all-status ordered moves after Inbox counter, empty/missing project behavior and alias cascade with foreign_keys ON.
- [ ] Cover candidate column AND decoded payload reassignment of every status and preserved metadata; cover deleted-project bindings empty while unrelated bindings remain.
- [ ] After all Task3 writers finish, the integration owner runs `go test ./...` and formatting checks; exercise actual SQLite deletion/reassignment/binding operations with all task statuses and verify new Inbox numbers, payloads and unrelated bindings before the Task3 commit. No worker checks mid-flight. If implementation search finds additional mock files, record exact backend-local paths in Files before editing them.

### Task 4: Delete projects atomically and re-evaluate vanished automatic destinations

**Files:**
- Modify: `internal/model/event.go`
- Modify: `internal/model/service.go`
- Modify: `internal/usecase/projects.go`
- Modify: `cmd/huskwoot/main.go`
- Modify: `internal/agent/tools_test.go`
- Modify: `internal/pipeline/pipeline_test.go`
- Modify: `internal/usecase/projects_test.go`
- Modify: `internal/usecase/auto_tasks.go`
- Modify: `internal/usecase/tasks.go`
- Modify: `internal/usecase/tasks_test.go`
- Create: `internal/usecase/auto_tasks_project_deletion_test.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_forward_test.go`

- [ ] Add EventProjectDeleted/comment, service DeleteProject and required AutoTasks dependency; unconditionally instantiate/share SQLiteAutoTaskStore before project service even without Telegram; real deletion fixtures and ProjectService mocks migrate.
- [ ] Implement exact transaction order candidates→bindings→ordered task moves/project delete→GetTaskTx/wrapped task_moved→top-level-id project_deleted; reject Inbox/missing; commit then cache invalidate/ordered Notify. Add seq/payload/cache/rollback regressions.
- [ ] Cover all candidate statuses/question metadata, pending→delete→approval and approval→delete; repeated Decide produces exactly one Inbox task and reads fresh tx candidate.
- [ ] Implement narrow destination-missing signal and replay-first readSubmission/Finalize, revision→project→writes commit, effective projectID and full Inbox reference/within-batch assessment restart. Preserve inputs/extraction, terminal missing Inbox/cancel/unrelated errors, changed revision does not redirect.
- [ ] Add deterministic before-read/during-assess/before-commit deletion races with started/resume channels and one-shot DeviceStore.ListActiveIDs wrapper scoped only to AutoTaskService (does not block DeleteProject). Original-project different versus Inbox-only same reference must change to duplicate/no new task; rebuild within-group references.
- [ ] Prove replay after deletion skips assessor, keeps original receipt keys and executes Finalize; forwarding snapshot deletes only captured IDs, new forward remains. Finalize failure rolls back tasks/candidates/events/receipts. Forward confirmation names actual Inbox from result.Created with no second Submit/GET.
- [ ] Recheck externally found P with GetProjectTx before bind; nil/error rollback without SetTx/newP/event/Notify, use fresh object; initially absent name still creates. Read-only uncached resolver verifies nonempty binding→nil Inbox/error outward without blind binding delete or explicit set_project fallback. Update fixtures to contain real found/resolved projects.
- [ ] Add searchP→DELETEcommit→bind rollback barrier and reverse-order deletion cleanup; GetProject/GetProjectTx errors, stale binding and actual ordinary agent create_task through real resolver→Inbox (not Submit). Later explicit-create deletion stays typed not-found.
- [ ] Change preliminary CreateTasks nil destination to English wrapped ErrProjectNotFound with errors.Is regression; do not add API string heuristics or alter unrelated errors.
- [ ] After all Task4 writers finish, the integration owner runs `go test ./...` and formatting checks; exercise real isolated service deletion, candidate approval/replay and stale-binding resolution, observing task/event order and retained receipt/Finalize behavior before the Task4 commit. No worker checks mid-flight.

### Task 5: Expose deletion with uncached snapshots and ordered durable HTTP SSE

**Files:**
- Modify: `internal/api/projects.go`
- Modify: `internal/api/server.go`
- Modify: `api/openapi.yaml`
- Modify: `internal/api/projects_test.go`
- Modify: `internal/api/tasks_test.go`
- Modify: `internal/api/events.go`
- Modify: `internal/api/events_test.go`
- Modify: `README.md`
- Modify: `cmd/huskwoot/main.go`
- Modify: `internal/api/sync.go`
- Modify: `internal/api/sync_test.go`
- Modify: `internal/api/acceptance_test.go`

- [ ] OpenAPI-first add DELETE operation/DeleteProjectResponse, project_deleted and distinct wrapped task_deleted schemas; implement route/handler full REST movedTasks200/Inbox403/notfound404; add success/empty/Inbox/unknown handler tests and preserve OpenAPI validity test. README lists rename/color/delete.
- [ ] Add actual TaskService/SQLite HTTP POST regressions: already deleted destination404 not_found and barrier after preliminary GetProject→deletion before tx→same404. Do not mock a handler error or rely on SSE.
- [ ] Switch only API TaskStore DI to raw sqliteTaskStore and syncHandler function reader, exact route prerequisites/read order/error500; update sync/events/acceptance fixtures, keep voice and other cached consumers unchanged, no new config/unwrapping/fallback.
- [ ] Add real warm-cache regression with Invalidate held AFTER DELETE commit: snapshot cursor includes deletion and P absent while cached ListProjects still contains P; release barrier. SQL read failure500 never falls back.
- [ ] Implement parseLastEventID/drainThrough exact subscription-before-response, explicit0, new-only initial mark, fixed target/per-page load→bounds check→write, page-empty/error/reset/keepalive rules; SQL payload only, no false advancement or endless catchup. Keep EventStore/Broker/voice/buffer64/retention unchanged; document non-prefix completeness limitation in OpenAPI.
- [ ] Add real SQL+HTTP barriers: held PATCH10 Notify then DELETE11 Notify then delayed10 yields10,11; held move20/delete21 then earlier completion22 Notify yields20,21,22. Barriers hold Notify after commit, not tx. Cover commit during subscription/drain and after target before last page, commit without Notify on keepalive, strictly ascending/no duplicates.
- [ ] Cover explicit0 snapshotS0→event-before-connect replay,0 with MinSeq>1 reset; absent/malformed/negative/overflow header new-only mark established after subscription, cursor>Max reset. Multi-page numeric gaps, retention between pages, above-target rows, empty-before-target, broker overflow/close, cancellation and DB/write errors do not falsely acknowledge.
- [ ] Prove actual soft task DELETE→GET200 cancelled and PurgeClosedOlderThan→GET404 plus durable wrapped task_deleted HTTP frame. Retain redacted actual frame evidence for Swift fixture consumption, not a new generator or manually repeated invented payload.
- [ ] After all Task5 writers finish, the integration owner runs `go test ./...` and `gofmt -l .`; exercise real loopback DELETE/raw snapshot/ordered SSE/cursor0 and actual purge frames before the Task5 commit, retaining sanitized evidence for Swift. No worker checks mid-flight.

### Task 6: Verify acceptance criteria

**Files:**
- Modify: `docs/plans/20261021-project-management-backend.md`

- [ ] At a single integration boundary with all source editors finished, run `go test ./...`, `go vet ./...`, `gofmt -l .` (empty), including OpenAPI validity. Record results and named tests covering every Tasks1–5 behavior and source14 mapping; no client-success claims.
- [ ] Prove color create/PATCH-only/reset/invalid422, all project reads/migration/reopen, generated versus explicit slug collisions, Inbox name/slug/delete403 with color/description allowed and comprehensible agent error.
- [ ] Prove transactional all-status deletion/empty/missing/aliases/order/new Inbox numbers, candidate column+payload/question metadata/Decide idempotence, bindings cleanup, cache invalidation and rollback. Prove Submit all three race boundaries, changed Inbox references/within-group duplicate result, replay/no inference, original InputKeys/Finalize snapshot isolation/rollback/terminal errors and actual forward confirmation.
- [ ] Prove binding search/delete/tx race and reverse order, propagated read errors, read-only stale resolver and real ordinary-agent create→Inbox; actual POST missing destination404 on both boundaries plus GETproject404, not a generic500 or422 interpretation.
- [ ] Prove warm cached project held past DELETEcommit never appears in raw HTTP snapshot after delete cursor; read failure500 no fallback; document S→collections nontransactional/late GET and original-S limitations without claiming client atomic import.
- [ ] Execute all real SQL/HTTP ordered replay/live scenarios from5, including cursor0, no-header mark, target/pages/gaps/retention/reset/keepalive/noNotify/broker/cancel/DB/write errors. Capture actual PATCH→delete and move→delete→done frames and actual purge wrapped task_deleted frame with synthetic identities. Record source test names, reproducible setup and redacted frame/response evidence here so client Task9 can consume actual server sequences and verify final repository state/cursor. Client decoder/acknowledgement/pause remains client-owned.
- [ ] Run the isolated loopback HTTP smoke specified in Testing Strategy: temporary main under module, temporary file SQLite, real services/broker/AutoTaskStore/raw API TaskStore, hashed random synthetic bearer auth. POST project/color→PATCH name→reopen server on same DB→GET color before reset/delete→reset color→create open/done/cancelled→DELETE200/full movedTasks→GETproject404→verify exact Inbox numbers/all statuses and P absent from uncached snapshot. Record sanitized results; no daemon/external handlers/services/accounts.
- [ ] In that isolated smoke POST task to deleted project before providing SSE to any client→404 not_found and GETproject404; deterministic Task5 tests prove deletion-between-check/write boundary. Mark only backend half of source14.18; dependent client9 must run actual APIClient/SyncEngine with separate client SQLite to GET404 and resend rewritten pending create to Inbox preserving title/later fields.
- [ ] In a separate fresh smoke DB obtain snapshotS0, commit event before connecting, then connect explicit Last-Event-ID0 and read that exact retained event. Preserve actual purge and ordered frames for client consumption. Remove all temporary harnesses/databases/credentials before commit; never persist secret-bearing evidence. Client9 may recreate a backend ephemeral fixture but no persistent backend changes.
- [ ] Reconcile all22 source14 mapping rows against backend evidence and explicit client/manual ownership, with no unchecked server obligation silently treated as UI work. No mid-flight verification in this task: execute only after all editing workers have landed.

### Task 7: [Final] Update documentation

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/plans/20261021-project-management-backend.md` → `docs/plans/completed/20261021-project-management-backend.md`

- [ ] Update English AGENTS server contracts: project403/422 and typed CreateTasks404 on both boundaries; color/changedFields order and storage methods; project_deleted top-level id versus task_deleted task.id; candidate/binding deletion, transactional bind recheck/read-only resolver, explicit-destination versus Submit boundaries, Submit replay/Finalize/Decide.
- [ ] Document API Config.TaskStore same raw DB, snapshot S→uncached collections not transactional; SQL-only HTTP replay/live, subscribe+initial mark before flush, per-page fixed target/bounds/keepalive/errors, unchanged Broker/voice, explicit0/reset/new-only policy and non-prefix retention completeness limits without changing TTL.
- [ ] Update README project features and prerequisite server fixes (raw snapshot and ordered durable SSE including0) before client rollout. Link dependent client plan as context only; retain actual local server evidence without claiming UI done. These are all original15 backend documentation obligations; client CLAUDE/design remain in client10.
- [ ] After prior tasks and evidence are complete, have the native completion helper move **only this plan** to `docs/plans/completed/20261021-project-management-backend.md`, preserving name and sibling links. Never move/edit original20261020 or the macOS plan; no automatic commit/push/deploy.
- [ ] After documentation writers finish, the integration owner verifies documented API/event contracts against the accepted implementation and exercises the relevant isolated HTTP smoke on the final state before the final task commit. Record truthful checks/smoke proof; Task6 evidence is reused only where the runtime state is unchanged. No checks mid-flight or new operational actions.

## Post-Completion

- Deploy/merge/release backend separately under backend rules and explicit authorization, before client: migration018, raw snapshot, ordered durable replay/live with explicit0 are prerequisites. Previous cursor0 leaves setup window; Notify arrival order is insufficient. Old server DELETE405 becomes client save failure and color may be absent. Retention policy remains unchanged.
- Full ordinary Debug app with local API/SSE/restart requires separately prepared isolated macOS user/VM or verified separate SQLite/defaults/Keychain. serverURL alone is not isolation; normal daemon has no API-only startup and fake Telegram credentials are unsafe. In-memory Preview is not this check.
- Original external/manual client checks remain dependent scope: Cyrillic project/color/rename/Auto across all themes; delete active/completed with confirmation/new Inbox numbers/selection; delete/recreate same name then second Mac snapshot; second-device SSE create/color/delete; real Telegram bound group and pending candidate approval after delete; offline error; Inbox color-only menu; App xcodebuild outside available sandbox. Real accounts/devices require separate authorization.
- Other future clients must support color and project_deleted contracts. No additional external actions are implied; local server and cross-component HTTP smoke remain mandatory local acceptance, not deferred here.
