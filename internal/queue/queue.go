package queue

import (
	"context"
	"fmt"
	"strconv"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/settings"
)

const (
	streamKey    = "resize:jobs"
	jobKeyPrefix = "resize:job:"
)

// submitScript atomically records a queued state and appends the job to the
// stream unless a state for the job already exists.
// KEYS: state key, stream key.
// ARGV: state TTL in ms, id, url, width, height.
var submitScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if not status then
	redis.call('HSET', KEYS[1], 'status', 'queued')
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
	redis.call('XADD', KEYS[2], '*', 'id', ARGV[2], 'url', ARGV[3], 'width', ARGV[4], 'height', ARGV[5])
	return 'created'
end
if status == 'failed' then
	return 'failed'
end
return 'active'
`)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusFailed     Status = "failed"
)

type State struct {
	Status Status
	Error  string
}

type Submission int

const (
	Created Submission = iota
	Active
	Failed
)

type Queue struct {
	client   *redis.Client
	settings *settings.Settings
}

func New(client *redis.Client, settings *settings.Settings) *Queue {
	return &Queue{client: client, settings: settings}
}

// Submit queues job unless a state for its ID already exists. It reports
// Failed while a failed state is still retained, so callers can surface the
// failure instead of retrying it.
func (q *Queue) Submit(ctx context.Context, job model.Job) (Submission, error) {
	res, err := submitScript.Run(ctx, q.client,
		[]string{jobKeyPrefix + job.ID, streamKey},
		q.settings.Service.JobStateTTL.Milliseconds(),
		job.ID, job.URL,
		strconv.FormatUint(uint64(job.Width), 10),
		strconv.FormatUint(uint64(job.Height), 10),
	).Text()
	if err != nil {
		return Failed, fmt.Errorf("submitting job %s: %w", job.ID, err)
	}

	switch res {
	case "created":
		return Created, nil
	case "failed":
		return Failed, nil
	default:
		return Active, nil
	}
}

// State returns the stored state of a job and whether one exists.
func (q *Queue) State(ctx context.Context, id string) (State, bool, error) {
	fields, err := q.client.HGetAll(ctx, jobKeyPrefix+id).Result()
	if err != nil {
		return State{}, false, fmt.Errorf("reading job state %s: %w", id, err)
	}
	if len(fields) == 0 {
		return State{}, false, nil
	}

	return State{Status: Status(fields["status"]), Error: fields["error"]}, true, nil
}

// Depth returns the number of entries in the job stream.
func (q *Queue) Depth(ctx context.Context) (int64, error) {
	n, err := q.client.XLen(ctx, streamKey).Result()
	if err != nil {
		return 0, fmt.Errorf("reading queue depth: %w", err)
	}

	return n, nil
}
