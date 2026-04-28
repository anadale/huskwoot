package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// ForwardBatchSweeper periodically checks the forward buffer for chat entries
// that have been waiting longer than timeout and submits them to the dispatcher.
//
// Sweeper-sourced signals carry only ChatID and Source="sweeper". The dispatcher
// enriches them with ReplyFn and DeleteFn via its EnrichFn callback.
type ForwardBatchSweeper struct {
	buffer     model.ForwardBuffer
	dispatcher *ForwardDispatcher
	timeout    time.Duration
	tickEvery  time.Duration
	now        func() time.Time
	logger     *slog.Logger
}

// NewForwardBatchSweeper creates a ForwardBatchSweeper.
// tickEvery defaults to 5s when zero. now defaults to time.Now when nil.
func NewForwardBatchSweeper(
	buffer model.ForwardBuffer,
	dispatcher *ForwardDispatcher,
	timeout time.Duration,
	tickEvery time.Duration,
	now func() time.Time,
	logger *slog.Logger,
) *ForwardBatchSweeper {
	if tickEvery == 0 {
		tickEvery = 5 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ForwardBatchSweeper{
		buffer:     buffer,
		dispatcher: dispatcher,
		timeout:    timeout,
		tickEvery:  tickEvery,
		now:        now,
		logger:     logger,
	}
}

// Run periodically sweeps the forward buffer for timed-out batches.
// Returns immediately when timeout is zero (feature disabled).
func (s *ForwardBatchSweeper) Run(ctx context.Context) {
	if s.timeout == 0 {
		return
	}

	ticker := time.NewTicker(s.tickEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *ForwardBatchSweeper) sweep(ctx context.Context) {
	before := s.now().Add(-s.timeout)
	chats, err := s.buffer.ListPendingChats(ctx, before)
	if err != nil {
		s.logger.WarnContext(ctx, "forward sweeper: ListPendingChats failed", "error", err)
		return
	}
	for _, chatID := range chats {
		s.dispatcher.Submit(model.ForwardDispatchSignal{
			ChatID: chatID,
			Source: "sweeper",
		})
	}
}
