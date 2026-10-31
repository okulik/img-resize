package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/okulik/img-resize/internal/model"
	"github.com/okulik/img-resize/internal/settings"
)

const (
	streamKey    = "resize:jobs"
	jobKeyPrefix = "resize:job:"
	doneChannel  = "resize:done"
	groupName    = "resize-workers"
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

// Delivery is a job handed to a consumer. It stays pending until the consumer
// calls Complete or Fail.
type Delivery struct {
	Job       model.Job
	MessageID string
	Attempts  int64
}

// EnsureGroup creates the consumer group and the stream if they are missing.
// The group starts at the beginning of the stream so jobs queued before the
// first worker started are not skipped.
func (q *Queue) EnsureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("creating consumer group: %w", err)
	}

	return nil
}

// Read waits up to block for a new job. It returns nil, nil when none arrives
// or when it dropped a malformed entry; callers simply read again.
func (q *Queue) Read(ctx context.Context, consumer string, block time.Duration) (*Delivery, error) {
	streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: consumer,
		Streams:  []string{streamKey, ">"},
		Count:    1,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading job: %w", err)
	}
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return nil, nil
	}

	msg := streams[0].Messages[0]
	job, err := parseJob(msg)
	if err != nil {
		return nil, q.drop(ctx, msg.ID, err)
	}

	return &Delivery{Job: job, MessageID: msg.ID, Attempts: 1}, nil
}

// Reclaim takes over one job that has been pending longer than JobClaimIdle,
// which is one of a crashed or stuck worker. Every claim counts as a delivery,
// so a job is claimed only when the caller can start it, and deliveries never
// holds more than one. A job already delivered JobMaxAttempts times is failed
// and counted in abandoned instead of being returned. Reclaim keeps going after
// a per-job error and returns the delivery it did claim together with the
// joined errors, so callers must process the delivery even when err is set.
func (q *Queue) Reclaim(ctx context.Context, consumer string) (deliveries []Delivery, abandoned int, err error) {
	var errs []error
	start := "0-0"
	for {
		msgs, next, claimErr := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   streamKey,
			Group:    groupName,
			Consumer: consumer,
			MinIdle:  q.settings.Service.JobClaimIdle,
			Start:    start,
			Count:    1,
		}).Result()
		if claimErr != nil {
			errs = append(errs, fmt.Errorf("claiming idle jobs: %w", claimErr))
			break
		}

		for _, msg := range msgs {
			job, parseErr := parseJob(msg)
			if parseErr != nil {
				if dropErr := q.drop(ctx, msg.ID, parseErr); dropErr != nil {
					errs = append(errs, dropErr)
				}
				continue
			}

			attempts, countErr := q.deliveryCount(ctx, msg.ID)
			if countErr != nil {
				errs = append(errs, countErr)
				continue
			}
			if attempts == 0 {
				// Acknowledged by another worker since the claim.
				continue
			}

			d := Delivery{Job: job, MessageID: msg.ID, Attempts: attempts}
			if attempts > q.settings.Service.JobMaxAttempts {
				log.Printf("abandoning job %s after %d attempts", job.ID, attempts-1)
				if failErr := q.Fail(ctx, d, "fetch failed"); failErr != nil {
					errs = append(errs, failErr)
					continue
				}
				abandoned++
				continue
			}
			deliveries = append(deliveries, d)
		}

		// Claim again only if this pass produced nothing to run, so dropped
		// and abandoned entries cannot hold up real work. Jobs left
		// unclaimed keep their delivery count for the next call.
		if len(deliveries) > 0 || next == "0-0" || next == "" {
			break
		}
		start = next
	}

	return deliveries, abandoned, errors.Join(errs...)
}

// MarkProcessing records that a worker started the job. The state's TTL is
// applied in the same transaction so a state that expired while the job was
// queued is not recreated without one.
func (q *Queue) MarkProcessing(ctx context.Context, id string) error {
	key := jobKeyPrefix + id
	_, err := q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key, "status", string(StatusProcessing))
		p.PExpire(ctx, key, q.settings.Service.JobStateTTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("marking job %s processing: %w", id, err)
	}

	return nil
}

// Complete acknowledges and removes a finished job, deletes its state and
// announces the ID on the done channel. It does not need the state to exist.
func (q *Queue) Complete(ctx context.Context, d Delivery) error {
	_, err := q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAck(ctx, streamKey, groupName, d.MessageID)
		p.XDel(ctx, streamKey, d.MessageID)
		p.Del(ctx, jobKeyPrefix+d.Job.ID)
		p.Publish(ctx, doneChannel, d.Job.ID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("completing job %s: %w", d.Job.ID, err)
	}

	return nil
}

// Fail records message as the job's error with FailedJobTTL, then
// acknowledges and removes the job and announces the ID on the done channel.
// All of it happens in one transaction, so a failed job is never left pending
// without its state or the reverse.
func (q *Queue) Fail(ctx context.Context, d Delivery, message string) error {
	key := jobKeyPrefix + d.Job.ID
	_, err := q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, key, "status", string(StatusFailed), "error", message)
		p.PExpire(ctx, key, q.settings.Service.FailedJobTTL)
		p.XAck(ctx, streamKey, groupName, d.MessageID)
		p.XDel(ctx, streamKey, d.MessageID)
		p.Publish(ctx, doneChannel, d.Job.ID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failing job %s: %w", d.Job.ID, err)
	}

	return nil
}

// deliveryCount returns how many times the group delivered a message, or 0
// if the message is no longer pending.
func (q *Queue) deliveryCount(ctx context.Context, messageID string) (int64, error) {
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamKey,
		Group:  groupName,
		Start:  messageID,
		End:    messageID,
		Count:  1,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("reading delivery count of %s: %w", messageID, err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	return pending[0].RetryCount, nil
}

// drop logs a malformed entry, then acknowledges and removes it so it is not
// delivered again.
func (q *Queue) drop(ctx context.Context, messageID string, cause error) error {
	log.Printf("dropping malformed queue entry %s: %v", messageID, cause)

	_, err := q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAck(ctx, streamKey, groupName, messageID)
		p.XDel(ctx, streamKey, messageID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("dropping malformed entry %s: %w", messageID, err)
	}

	return nil
}

func parseJob(msg redis.XMessage) (model.Job, error) {
	id, _ := msg.Values["id"].(string)
	url, _ := msg.Values["url"].(string)
	if id == "" || url == "" {
		return model.Job{}, errors.New("missing id or url")
	}

	width, err := parseDimension(msg.Values["width"])
	if err != nil {
		return model.Job{}, fmt.Errorf("bad width: %w", err)
	}
	height, err := parseDimension(msg.Values["height"])
	if err != nil {
		return model.Job{}, fmt.Errorf("bad height: %w", err)
	}

	return model.Job{ID: id, URL: url, Width: width, Height: height}, nil
}

func parseDimension(v any) (uint, error) {
	s, _ := v.(string)
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}

	return uint(n), nil
}
