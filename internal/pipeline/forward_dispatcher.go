package pipeline

import (
	"context"
	"log/slog"

	"github.com/anadale/huskwoot/internal/model"
)

// EnrichFn supplies reply and delete callbacks for a DM chat ID.
// Used to enrich sweeper-sourced signals that arrive without those callbacks.
type EnrichFn func(chatID string) (
	deleteFn func(context.Context, []string) error,
	replyFn func(context.Context, string) error,
	err error,
)

// ForwardDispatcher processes ForwardDispatchSignal events one at a time.
// A single goroutine consumer ensures race-free per-chat processing.
type ForwardDispatcher struct {
	buffer   model.ForwardBuffer
	signal   chan model.ForwardDispatchSignal
	pipeline *Pipeline
	enrichFn EnrichFn
	logger   *slog.Logger
}

// NewForwardDispatcher creates a ForwardDispatcher that reads from the given channel.
// enrichFn is called for sweeper-sourced signals that have no reply/delete callbacks.
func NewForwardDispatcher(
	buffer model.ForwardBuffer,
	signal chan model.ForwardDispatchSignal,
	pipeline *Pipeline,
	enrichFn EnrichFn,
	logger *slog.Logger,
) *ForwardDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &ForwardDispatcher{
		buffer:   buffer,
		signal:   signal,
		pipeline: pipeline,
		enrichFn: enrichFn,
		logger:   logger,
	}
}

// Run reads signals sequentially until ctx is cancelled or the channel is closed.
func (d *ForwardDispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-d.signal:
			if !ok {
				return
			}
			d.processSignal(ctx, sig)
		}
	}
}

// Submit sends sig to the dispatcher channel without blocking.
// Logs a warning and drops the signal when the channel is full.
func (d *ForwardDispatcher) Submit(sig model.ForwardDispatchSignal) {
	select {
	case d.signal <- sig:
	default:
		d.logger.Warn("forward dispatcher channel full, signal dropped",
			"chat_id", sig.ChatID, "source", sig.Source)
	}
}

// processSignal enriches the signal (if necessary), loads the buffer, and
// calls pipeline.Process with a MessageKindForwardBatch message.
func (d *ForwardDispatcher) processSignal(ctx context.Context, sig model.ForwardDispatchSignal) {
	replyFn := sig.ReplyFn
	deleteFn := sig.DeleteFn

	if replyFn == nil && d.enrichFn != nil {
		enDelFn, enReplyFn, err := d.enrichFn(sig.ChatID)
		if err != nil {
			d.logger.WarnContext(ctx, "forward dispatcher: enrichFn failed",
				"chat_id", sig.ChatID, "error", err)
			return
		}
		deleteFn = enDelFn
		replyFn = enReplyFn
	}

	forwards, err := d.buffer.LoadBatch(ctx, sig.ChatID)
	if err != nil {
		d.logger.ErrorContext(ctx, "forward dispatcher: LoadBatch failed",
			"chat_id", sig.ChatID, "error", err)
		return
	}
	if len(forwards) == 0 {
		d.logger.DebugContext(ctx, "forward dispatcher: empty buffer, skipping",
			"chat_id", sig.ChatID, "source", sig.Source)
		return
	}

	counterpart := mostFrequentCounterpart(forwards)

	payload := model.ForwardBatchPayload{
		ChatID:      sig.ChatID,
		Forwards:    forwards,
		TriggerText: sig.TriggerText,
		TriggerID:   sig.TriggerID,
		DeleteFn:    deleteFn,
		ReplyFn:     replyFn,
		Counterpart: counterpart,
	}

	msg := model.Message{
		Kind:         model.MessageKindForwardBatch,
		ForwardBatch: &payload,
		Source:       model.Source{Kind: "telegram", ID: sig.ChatID},
	}

	if err := d.pipeline.Process(ctx, msg); err != nil {
		d.logger.ErrorContext(ctx, "forward dispatcher: pipeline.Process failed",
			"chat_id", sig.ChatID, "error", err)
	}
}

// mostFrequentCounterpart returns the ForwardFromName that appears most often
// among non-self-authored entries. Returns "" when all are self-authored or
// all counterpart names are empty.
func mostFrequentCounterpart(forwards []model.ForwardEntry) string {
	freq := make(map[string]int)
	for _, e := range forwards {
		if !e.IsSelfAuthored && e.ForwardFromName != "" {
			freq[e.ForwardFromName]++
		}
	}
	var best string
	var bestCount int
	for name, count := range freq {
		if count > bestCount || (count == bestCount && name < best) {
			best = name
			bestCount = count
		}
	}
	return best
}
