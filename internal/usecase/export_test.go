package usecase

import (
	"context"
	"time"

	"github.com/anadale/huskwoot/internal/model"
)

// SetClientVoiceTimings overrides the ack grace and the hard deadline of a
// consumer built by NewClientVoiceConsumer, so tests can drive the synthetic
// reply race without waiting for the production timings.
func SetClientVoiceTimings(consumer model.VoiceConsumer, ackGrace, hardDeadline time.Duration) {
	c := consumer.(*clientVoiceConsumer)
	c.ackGrace = ackGrace
	c.hardDeadline = hardDeadline
}

// ErrAgentFailedForTest exports errAgentFailed so tests can produce agent failures.
var ErrAgentFailedForTest = errAgentFailed

// AppendTouchedTasksForTest records task ids in the touched set carried by ctx,
// the way the task service does after a mutation, so mock chat services can
// simulate the tasks an agent call touched.
func AppendTouchedTasksForTest(ctx context.Context, ids ...string) {
	tasks := make([]model.Task, 0, len(ids))
	for _, id := range ids {
		tasks = append(tasks, model.Task{ID: id})
	}
	appendTouchedTasks(ctx, tasks)
}
