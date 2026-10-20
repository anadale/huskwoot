# Duplicate protection for tasks extracted from email

## Overview

Tasks extracted from IMAP email (`MessageKindBatch`) go through `AutoTaskService`, the same way Telegram group messages do:

- each candidate is compared against open tasks, pending and rejected candidates, and tasks closed within the last 14 days;
- a clear duplicate is not created;
- a clearly new task is created;
- an uncertain candidate waits, and the owner gets a question in the Telegram private chat.

**Problem.** Today `Pipeline.processPromise` sends email tasks straight to `TaskService.CreateTasks` (`internal/pipeline/pipeline.go:314-322`). There is no duplicate check and no question. On 2026-10-20 an email created `inbox#93` («восстановить сайт на другую VM»), a duplicate of the open `inbox#91` («восстановить данные на другую виртуальную машину»), without asking the owner. `AGENTS.md:103` documents this as intended («IMAP retain their existing behavior»); this plan changes that decision.

**Durable intake.** On the current email path a failed `CreateTasks` is logged and swallowed, and notifiers still fire with the extracted tasks. In addition, the production `watchHandler` acknowledges email after enqueueing, before `Pipeline.Process` runs. This plan changes both boundaries: email with `AutoTaskService` is processed synchronously by the production handler, and submission errors reach IMAP before acknowledgement. On any handler failure, IMAP leaves the persisted pre-poll cursor untouched regardless of FETCH response order and retries on the next poll; committed input receipts prevent repeat inference and candidate creation for successful messages replayed from that poll.

**Without Telegram.** `AutoTaskService` is built only when Telegram is configured (`cmd/huskwoot/main.go:268-286`), because questions are sent by `TelegramChannel`. Without it, email keeps the current direct path.

**Acceptance criteria:**

- an uncertain email candidate waits for an owner decision; a clear duplicate creates no task or question, and a clearly new candidate creates a task and its usual notification;
- a receipt/submission failure through the production channel handler leaves the persisted pre-poll IMAP cursor untouched, even if a higher UID succeeded before a lower UID failed; remaining responses are drained without handling, and a subsequent successful poll retries failed input without repeating committed side effects;
- replay of a committed email within one mailbox generation does not repeat inference, candidates, tasks or notifications;
- equal mailbox/UID/body values in different `UIDVALIDITY` generations are distinct inputs, without changing project bindings, source snapshots or the decimal message UID;
- email without `AutoTaskService` retains its queued/direct processing path, and Telegram group/forward-batch behavior is unchanged; only errors actually returned to IMAP can prevent its cursor save.

**Out of scope:**

- duplicate checks for tasks created by the agent (DM, voice) and the REST API;
- adding the email subject or sender to the Telegram question text;
- changes to `AutoTaskService` itself or to the thresholds.

## Context (from discovery)

- `internal/pipeline/pipeline.go`:
  - `Process` (line ~113): for `MessageKindGroup` it requires `autoTasks` and checks `Processed(autoInputKey(...))` before the classifier; on `ClassSkip` it submits an empty submission to record the receipt;
  - `processPromise` (line ~220): clears `Topic` for groups; for groups it submits even an empty extraction; for groups it calls `autoTasks.Submit` and notifies `result.Created` with `SourceMessage = msg`; for everything else it calls `TaskService.CreateTasks`, logs errors and notifies anyway;
  - `autoInputKey(source, messageID, text)` (line 503) — receipt key from source kind, account, chat, message id and text hash; it currently has no mailbox generation.
- `cmd/huskwoot/main.go:346-358,573-596`: the production `watchHandler` calls `Pipeline.Process` synchronously only for groups. Email and other messages enter `msgCh` and return success immediately; the later queue consumer only logs processing errors. Pipeline error propagation alone cannot protect the IMAP cursor.
- `internal/channel/imap.go:189-245,280-325`: `pollOnce` gets `UIDVALIDITY` from mailbox selection. A handler error is logged and stops handling, but after draining the current implementation can still save the maximum successfully handled/filtered UID. A higher successful UID before a lower failed UID can therefore skip the failed email. A `UIDVALIDITY` change discards the cursor and reapplies `OnFirstConnect`: `monitor` saves `UIDNEXT-1` and skips existing messages; backfill starts searching at UID 1. `Message.ID` remains the decimal UID and `Source.ID` remains `username:folder`.
- `internal/model/types.go:132-170`: `Message` has no mailbox-generation field. `Source.AccountID` is empty for IMAP and must not be repurposed as a generation: project resolution uses `Source.ID`, and provenance must retain the existing source identity.
- The pinned local `github.com/emersion/go-imap v1.2.1` implementation (`client/cmd_selected.go:146-179`, `responses/fetch.go:17-57`) passes the caller's channel into FETCH and forwards matching responses immediately, without sorting by UID. The plan must not assume ascending response order.
- `internal/usecase/auto_tasks.go`: `Submit` is source-agnostic (`AutoTaskSubmission{InputKeys, ProjectID, Tasks, SourceText}`). Candidates keep the full `CreateTaskRequest`, including `Source` and `SourceDetails`, so a task created after an affirmative answer keeps its email source snapshot.
- `internal/channel/telegram_task_questions.go`: questions go to the owner's private chat (`sendTaskQuestions(ctx, ownerID)`); the text (`auto_tasks.question`) does not mention the source kind.
- Tests:
  - `internal/pipeline/pipeline_test.go` — `testBatchMsg()`, `testConfig()` (sets `AutoTasks: &pipelineAutoTasks{}` by default), `TestProcess_Batch_GoesToTaskService`, `TestProcess_Batch_TaskServiceError_NotifierStillCalled`, `TestProcess_ClassPromise_Batch_BypassOwnerCheck`, `TestProcess_OriginTopic_PreservedForBatch`, `TestProcess_BatchMessage_TaskSourcePreserved`;
  - `internal/pipeline/auto_tasks_test.go` — mock `pipelineAutoTasks` (records `submissions`, `processed` map, `err`, `result`; forwards to `tasks.CreateTasks` when `tasks` is set);
  - `internal/api/acceptance_test.go` — composes `pipeline.New` with a real `usecase.NewAutoTaskService`.
  - `internal/channel/imap_test.go` — `mockImapConn`, `newTestIMAPChannel`, `makeTestIMAPMessage`, `makeTestStatus`, `TestIMAPChannel_UIDValidityChange` and `TestIMAPChannel_HandlerError_StopsCursorAdvance` provide an isolated poll/cursor seam; no real IMAP server is needed;
  - `internal/pipeline/pipeline_forward_test.go` directly calls `autoInputKey` to seed a processed forward receipt and must migrate with its signature;
  - the acceptance harness currently has no duplicate checker and does not expose its automatic-task service; the new scenarios need a small explicit harness option for a hand-written scripted checker and candidate inspection.
- Docs describing the current behavior: `AGENTS.md` (flowchart lines 60-70, line 103, line 111), `README.md` section «Telegram Task Duplicate Protection», `docs/development/architecture.md` (flowchart, section «Automatic Telegram Tasks»).
- Review source: the confirmed findings in revmux round `03-tui` were checked against the current production callback, IMAP poll/reset behavior and receipt helper. This revision changes the plan only; implementation and a new review round remain separate actions.

## Development Approach

- **testing approach**: TDD, as required by the repository. First reproduce the consumer-visible failures: a reused UID across mailbox generations and a submission failure acknowledged by the production callback.
- Implement generation-aware identity before enabling email receipt checks; then change pipeline routing and the production acknowledgement boundary before end-to-end acceptance verification.
- Keep changes focused. Preserve the existing non-IMAP receipt representation exactly and migrate every `autoInputKey` caller; do not add a compatibility overload or alias.
- Permanent tests target replay, generation transitions, cursor retention, decisions, provenance and notifications—not a unit test for every function or assertions about copied/wired values alone.
- Run focused package checks after each completed task; run the full suite and static checks at the final integration boundary, with no concurrent source edits.
- Maintain backward compatibility: without `AutoTaskService`, email keeps the current queued/direct path, including its existing error and notification behavior.
- Update this plan when an agreed scope change occurs. All implementation checkboxes remain unchecked until implementation and verification actually happen.
- Repository rules (`AGENTS.md`): comments and docs in English, `gofmt`, hand-written mocks.

## Testing Strategy

- **pipeline regressions** (`internal/pipeline`): automatic email decisions, replay/skip/empty extraction, error propagation, notifications, topic/source preservation and generation-specific receipts; existing group/forward receipt strings remain unchanged.
- **production intake/cursor regressions** (`internal/channel/imap_test.go`): feed synthetic MIME messages through mocked `pollOnce` using the actual new `Pipeline.WatchHandler`, not `Pipeline.Process` or a test-only routing closure. Cover prior success followed by failure in both ascending and nonascending UID order, no cursor write on failure, draining without subsequent handling, retry with changed response order, committed replay and monitor/backfill epoch behavior. A duplicate-checker error is not a suitable failure injection: `AutoTaskService` saves it as pending.
- **acceptance** (`internal/api/acceptance_test.go`): real pipeline, `AutoTaskService` and SQLite with a scripted duplicate checker exercise the `inbox#93` pending/duplicate/created outcomes, replay and preserved email provenance. Direct pipeline calls do not count as cursor-boundary coverage.
- **local runtime smoke**: run a throwaway Go command under this module with temporary SQLite, the production `Pipeline.WatchHandler`, synthetic email and deterministic classifier/extractor/checker dependencies. Inject one receipt/submission failure, retry successfully, replay the committed input, then change only `UIDVALIDITY`. Observe the error returned by `WatchHandler`, empty queue, receipt/candidate/task state and notification counts; the new generation must reach classification even if task deduplication subsequently skips it. `pollOnce` itself logs handler errors instead of returning them; its cursor/drain behavior is covered through the mocked poll regressions. No real IMAP, Telegram or model endpoint is used; remove the throwaway command after the run.
- **commands**: focused `go test ./internal/channel/... ./internal/pipeline/... ./internal/api/...`; final `go test ./...`, `go vet ./...`, `gofmt -l .`.
- **external verification**: real mailbox and Telegram checks require separate permission and stay in Post-Completion; they do not replace the isolated local smoke.

## Progress Tracking

- Mark completed implementation items `[x]` only after their stated checks pass.
- Record newly discovered work, blockers and agreed deviations in plain text, keeping task numbers sequential.
- Keep this plan in sync with actual work; correcting the plan does not mark implementation complete.

## Solution Overview

The pipeline decides per message whether automatic extraction goes through `AutoTaskService`:

- `MessageKindGroup` — always (as today; missing `AutoTaskService` stays an error);
- `MessageKindBatch` — when `AutoTaskService` is configured;
- everything else — no.

Every place in `Process` and `processPromise` that checks `msg.Kind == model.MessageKindGroup` for receipts, skip receipts, empty extractions and `Submit` uses this decision instead. Group-only behavior that is not about `AutoTaskService` stays group-only: clearing `Topic` and the hard error when `AutoTaskService` is missing.

The same private decision also governs channel acknowledgement. Move the existing production callback into `Pipeline.WatchHandler(queue)`: groups and email with `AutoTaskService` call `Process` synchronously; all other messages keep the existing context-aware queue send. `main.go` uses this callback rather than maintaining a second routing condition.

Carry the selected mailbox generation in an optional in-memory message field and use it only in IMAP receipt keys. Do not alter the UID, source account/channel identifiers, cursor format, project bindings or public source DTOs. This is smaller and safer than encoding the generation into `Source.AccountID` or changing `Message.ID`.

## Technical Details

- New helper in `pipeline.go`:

  ```go
  // usesAutoTasks reports whether automatic extraction from msg goes through
  // AutoTaskService. Group messages always do; email does when the service is
  // configured, otherwise it keeps the direct TaskService path.
  func (p *Pipeline) usesAutoTasks(msg model.Message) bool
  ```

- Mailbox generation:
  - add `Message.IMAPUIDValidity uint32` in `internal/model/types.go`; zero means unset for non-IMAP and synthetic messages;
  - in `IMAPChannel.pollOnce`, after conversion and before invoking the handler, set `converted.IMAPUIDValidity = status.UidValidity`; keep `convertIMAPMessage` and its existing signature unchanged;
  - extend `autoInputKey` explicitly to `autoInputKey(source model.Source, messageID, text string, imapUIDValidity uint32) string`. For IMAP, encode the decimal generation as a separate unambiguous key component. For all other source kinds, retain the current key bytes exactly;
  - regular receipt lookup, skip and submission callers pass `msg.IMAPUIDValidity`; forward-entry callers and their receipt-seeding test pass zero. No persisted column, REST/OpenAPI change or migration is required: receipt keys are already opaque strings.
- Production channel callback:
  - add `func (p *Pipeline) WatchHandler(queue chan<- model.Message) func(context.Context, model.Message) error` in `internal/pipeline/pipeline.go`;
  - the returned callback calls `p.Process` and returns its result when `p.usesAutoTasks(msg)`; otherwise it preserves the existing `select` between queue send and `ctx.Done()`;
  - groups remain synchronous even without the service, so their existing missing-service error is not converted into queued processing;
  - replace the local `watchHandler` closure in `cmd/huskwoot/main.go` with `pipe.WatchHandler(msgCh)`; keep the queue consumer for DM, GroupDirect, forward batches and email without the service;
  - preserve `UIDVALIDITY` and first-connect reset policy, but fix failure-time advancement in `IMAPChannel.pollOnce`: set a poll-local handler-failure flag before breaking on any handler error, drain the remaining FETCH channel without further handling, then receive and return any FETCH error as today;
  - save the maximum processed/filtered UID only when no handler failed. On failure, perform no cursor write: keep existing state untouched, including absent state or the old generation before backfill. Do not save a copied cursor, buffer responses, sort FETCH results or return early before draining;
  - retain current cursor-read, FETCH and eligible cursor-save error propagation. A FETCH error remains observable even if a handler also failed; handler errors themselves remain logged rather than returned by `pollOnce`;
  - this channel-level no-save guard applies to any returned handler error. Email without the service still acknowledges enqueueing, so its later queued processing failures do not become durable-intake retries.

- `Process`:
  - the receipt check (`Processed(autoInputKey(msg.Source, msg.ID, msg.Text, msg.IMAPUIDValidity))`) runs when `usesAutoTasks(msg)`; the «AutoTaskService is not configured» error stays only for groups;
  - on `ClassSkip` the empty submission (receipt only) is recorded when `usesAutoTasks(msg)`.
- `processPromise`:
  - the «no tasks extracted» early return applies when `!usesAutoTasks(msg)` (an empty extraction still records the receipt, as for groups);
  - `Topic` is cleared only for `MessageKindGroup`; email keeps the extracted topic;
  - `Submit` with `InputKeys: []string{autoInputKey(msg.Source, msg.ID, msg.Text, msg.IMAPUIDValidity)}`, `ProjectID`, `Tasks`, `SourceText: msg.Text` runs when `usesAutoTasks(msg)`; return a submission error to the production synchronous callback so IMAP performs no cursor write for that poll and retries from its persisted pre-poll position;
  - `result.Created` gets `SourceMessage = msg` and goes to `dispatchNotifiers`, as for groups;
  - the direct `TaskService.CreateTasks` path stays for email without `AutoTaskService`, unchanged.
- Log and error messages that say «group» in the shared code path become neutral («submitting task candidates», «checking input receipt», «saving skipped input»).
- `SourceDetailsFromMessage(msg, model.SourceViaMessage)` is already set for email; nothing changes there.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): message identity, IMAP generation propagation, pipeline and production callback changes, isolated checks, acceptance coverage and documentation in this repository.
- **Post-Completion** (no checkboxes): deployment and a manual check with a real mailbox.

## Implementation Steps

### Task 1: Distinguish mailbox generations in automatic input receipts

**Files:**
- Modify: `internal/model/types.go`
- Modify: `internal/channel/imap.go`
- Modify: `internal/channel/imap_test.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/auto_tasks_test.go`
- Modify: `internal/pipeline/pipeline_forward_test.go`

- [ ] Write failing receipt-identity regressions first: reused mailbox/UID/body across different `UIDVALIDITY` values must yield distinct keys, while the same generation and input retain the same key. The IMAP epoch regression must also fail until delivered messages carry the selected generation. Pipeline inference/replay is verified after email routing in Task 2.
- [ ] Add and populate `Message.IMAPUIDValidity` in `pollOnce` without changing `Message.ID`, `Source.ID`, `Source.AccountID`, cursor keys or project-binding identifiers.
- [ ] Extend `autoInputKey` and migrate every regular/forward/test caller. Encode the generation only for IMAP; preserve existing Telegram group and forward receipt strings byte-for-byte.
- [ ] Cover receipt-key equality within one generation and separation for different generations, a different UID or an edited body; zero-generation synthetic inputs stay deterministic. Retain existing group/forward replay behavior; different email receipts do not imply another task must be created.
- [ ] Extend the IMAP epoch-reset regression for backfill and monitor modes, asserting delivered generation and existing cursor policy. Run `go test ./internal/channel/... ./internal/pipeline/...`.

### Task 2: Route email through AutoTaskService in the pipeline

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `internal/pipeline/pipeline_test.go`
- Modify: `internal/pipeline/auto_tasks_test.go`

- [ ] Write `TestProcess_Batch_SubmitsToAutoTasks` first with a configured `pipelineAutoTasks` result and no `tasks` proxy: the email is submitted with its generation-aware key, project, requests and `SourceText`; the direct `TaskService` path is not used. Confirm failure before the routing fix.
- [ ] Add `usesAutoTasks` and use it for receipt lookup, skip receipts, empty extraction and submission; keep topic clearing and the missing-service error group-only and shared log/error text source-neutral.
- [ ] Cover committed replay skipping inference, `ClassSkip` and empty extraction recording receipts, receipt/submission errors returning from `Process`, and notifications containing only `result.Created` with `SourceMessage`.
- [ ] Preserve email topic, mailbox/subject/source snapshot and project binding across the new path. Move the two direct-path/error-notifier tests explicitly onto `cfg.AutoTasks = nil` and rename them `…_WithoutAutoTasks`; adjust owner-bypass, topic and source tests that relied on the mock proxy.
- [ ] Check the generation transition through `Process`: equal mailbox/UID/body in a new generation reaches classification, with stable source/project/provenance; same-generation replay does not. Run `go test ./internal/pipeline/...`, retaining existing group and forward semantics.

### Task 3: Acknowledge automatic email intake only after processing succeeds

**Files:**
- Modify: `internal/pipeline/pipeline.go`
- Modify: `cmd/huskwoot/main.go`
- Modify: `internal/channel/imap.go`
- Modify: `internal/channel/imap_test.go`
- Modify: `internal/pipeline/auto_tasks_test.go`

- [ ] Extract the current callback unchanged as `Pipeline.WatchHandler(queue)` and use it in `main.go`: preserve group-only synchronous processing and the queued path during this mechanical move. Then write `TestIMAPChannel_AutoTaskHandlerFailureKeepsCursorAndRetries` using mocked `pollOnce` and that actual callback. Seed a saved UID and deliver a failing email followed by another email. Confirm the test fails because the existing callback enqueues/acknowledges before processing, rather than using a test-only copy or treating an undefined method as the reproduction.
- [ ] Change the extracted callback to use `usesAutoTasks`: automatic email and groups process synchronously; no-service email, DM, GroupDirect and forward batches retain the context-aware queued path. The failing-first regression must retain the saved UID, leave the queue empty and prevent handling of later responses.
- [ ] Add cursor regressions for saved UID 1 with arrivals `2 success, 3 failure, 4` and `3 success, 2 failure, 4`. Both must leave cursor 1 untouched, with no cursor-save call and no handler call for UID 4. Confirm these fail before adding the poll-local failure flag and guarded cursor save; retain `TestIMAPChannel_HandlerError_StopsCursorAdvance`'s existing first-failure expectation.
- [ ] Inject a receipt lookup or submission failure, not a duplicate-checker error. For the out-of-order case, let UID 3 commit before UID 2 fails, then retry with fresh responses ordered `2,3,4`: UID 2 retries, committed UID 3 skips inference/submission/notification, UID 4 processes and the successful poll saves cursor 4. Assert unique receipts and exactly-once committed candidate/task/notification effects across both polls.
- [ ] Preserve unconditional FETCH draining and fetch-error handling. Include more trailing responses than the fetch channel's capacity, a handler failure together with a FETCH error, and failure with absent/changed-generation backfill state. Assert no cursor write, no subsequent handling, observable FETCH error and unchanged first-connect monitor/reset behavior; handler errors alone are still not returned by `pollOnce`.
- [ ] Reuse the existing mocked IMAP connection/state fixtures with fresh MIME readers on each poll; explicitly configure each poll's UID/message set because the search mock does not implement server filtering. Assert persisted cursor state and save-call counts rather than expecting the callback error from `pollOnce`.
- [ ] Cover the missing-service group error and cancellation on the queued path as observable boundaries; retain existing Telegram group cursor and no-service email behavior. Run `go test ./internal/channel/... ./internal/pipeline/...`.

### Task 4: Verify duplicate email decisions with the real automatic-task service

**Files:**
- Modify: `internal/api/acceptance_test.go`

- [ ] Add a focused acceptance-harness option for a hand-written scripted duplicate checker and access to pending candidates, without changing production service behavior.
- [ ] Model `inbox#91`/`inbox#93`: an open Inbox task and an uncertain email candidate produce no new task, one pending question and the original email source snapshot. Retain `TestAcceptanceEmailTaskReturnsSubject`.
- [ ] Replay the same committed email in the same generation: no second candidate, task or notification. Change only the generation with the same UID/body and prove the new input reaches classification/comparison; let the checker decide whether it creates another task.
- [ ] Cover `same` at or above the duplicate threshold producing no task/question and `different` below the review threshold creating a task and notifier delivery. These direct-pipeline scenarios supplement, not replace, Task 3's production-handler/cursor regression.
- [ ] Run `go test ./internal/api/...`.

### Task 5: Verify acceptance criteria

**Files:**
- Modify: `docs/plans/20261020-email-auto-task-dedup.md` — record verified outcomes

- [ ] Verify every Overview criterion against the decision, replay, epoch-transition and actual-handler/cursor scenarios, including higher-successful-UID-before-lower-failure and reversed-order retry; ensure group/forward receipt compatibility and the no-service queued/direct fallback.
- [ ] Run the local runtime smoke described in Testing Strategy using temporary SQLite and synthetic dependencies: observe an intake error, successful retry, idempotent replay and a newly classified mailbox generation without external calls.
- [ ] Run `go test ./...`, `go vet ./...`, and `gofmt -l .` at the final integration boundary; all tests/static checks pass and no Go formatting changes remain.

### Task 6: [Final] Update documentation

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/development/architecture.md`
- Modify: `docs/plans/20261020-email-auto-task-dedup.md` → `docs/plans/completed/20261020-email-auto-task-dedup.md`

- [ ] Update `AGENTS.md` flow and automatic-task rules: email uses `AutoTaskService` when configured and its production callback waits for processing before acknowledgement; only no-service email keeps the old queued/direct fallback. Document generation-aware receipts, stable source/project identities and no cursor write after any IMAP handler failure.
- [ ] Rename README «Telegram Task Duplicate Protection» to «Task Duplicate Protection» and explain email, the Telegram requirement for questions, synchronous automatic intake, replay after a failed poll and unchanged first-connect/UIDVALIDITY reset policy.
- [ ] Update the architecture flowchart and «Automatic Telegram Tasks» section for email routing, acknowledgement boundary, receipts, failure-safe cursor advancement without assuming FETCH order, and the monitor/backfill distinction after `UIDVALIDITY` changes.
- [ ] After all implementation and verification tasks are complete, move this plan to `docs/plans/completed/20261020-email-auto-task-dedup.md`.

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deployment:** no migration and no config change. Existing cursors and Telegram receipts remain valid. Emails behind the saved cursor are not proactively replayed by deployment; normal later delivery follows the configured cursor policy. After a `UIDVALIDITY` change, monitor mode skips existing emails and backfill starts at UID 1; the new generation keeps their receipt identities separate.

**Manual verification:**

- send an email that clearly duplicates an open task → no task and no question;
- use an email candidate assessed as uncertain → a Telegram private-chat question arrives; verify approval on one candidate and rejection on another: «Добавить» creates a task with the email source, «Не добавлять» creates none;
- send an email with a clearly new task → the task is created and the usual notification arrives;
- the existing duplicate `inbox#93` stays as it is; cancel it by hand if it is not needed.
