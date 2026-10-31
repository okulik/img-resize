# Fly.io Scaling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split img-resize into a web service and a fleet of queue-driven workers on Fly.io, with crash-safe jobs, per-prefix throttling, metrics and capped autoscaling.

**Architecture:** One binary runs as `web` or `worker`. Web validates, throttles and adds jobs to a Redis Stream; workers read through a consumer group, resize, and store results in Redis. Job state lives in Redis too, so any machine can answer a poll. fly-autoscaler scales workers on backlog time, up to 5.

**Tech Stack:** Go 1.27.3, chi v5, go-redis v9, Redis Streams and Lua, `prometheus/client_golang`, Upstash Redis, Fly.io, fly-autoscaler.

**Spec:** `docs/superpowers/specs/2026-10-31-fly-scaling-design.md`. Read it before starting any task.

## Global Constraints

- Work on branch `feat/fly-scaling`, cut from `main`.
- One fix per commit. Each commit message is a single imperative line, with no co-author line or other trailer.
- Tests first: watch each test fail for the expected reason before implementing.
- Every commit must pass `make test-redis` and `make lint` on its own.
- Follow the surrounding style: `t.Fatalf`-based tests, literal expected values, `SVC_*` settings through `envconfig`.
- Fly region is `fra`. Worker cap is 5. Workers run on 256 MB machines.
- Client-visible failure text is only `fetch failed`, `invalid image` or `image too large`. Detail goes to logs.
- Never touch the owner's running `resizer` compose stack. Local checks use compose project `resizer-verify` and other ports.
- **Tasks 2 and 15 need the owner's explicit approval before starting.** Task 2 creates a billable database; Task 15 deploys.

## Review Focus

1. **Redis unavailable mid-request:** web returns 503, never a misleading 404 or a panic. Test in Task 9.
2. **Two web machines submit the same image at once:** exactly one stream entry exists, and a batch repeating one URL creates one job. Tests in Tasks 4 and 8.
3. **Job state evicted or expired while the job is still queued:** the worker still completes it and doesn't error on the missing state. Test in Task 7.
4. **A malformed stream entry:** the worker acknowledges and drops it instead of crashing or retrying forever. Test in Task 5.
5. **A sync client disconnects while waiting:** the wait ends and leaves no registered waiter behind, and the job keeps running. Test in Task 10.

---

### Task 1: Commit the pending work and create the branch

**Files:**
- Modify: `Dockerfile`, `Makefile` (already changed, uncommitted)
- Add: `docs/superpowers/specs/2026-10-31-fly-scaling-design.md`, this plan

- [ ] **Step 1: Create the branch**

Run: `git switch -c feat/fly-scaling`
Expected: the uncommitted changes carry over.

- [ ] **Step 2: Commit the image change**

```bash
git add Dockerfile Makefile
git commit -m "Slim the Docker image down to a scratch base"
```

- [ ] **Step 3: Commit the docs**

```bash
git add docs
git commit -m "Add the Fly.io scaling design and plan"
```

### Task 2: Check Upstash supports what the design needs (approval required)

No code. Ask the owner for approval first, because it creates a billable database, and ask them to run `! fly auth login`.

- [ ] **Step 1: Create the database**

Use `fly redis create --help` to confirm current flags, then create `img-resize` in `fra` on the fixed 250 MB plan, with eviction on and auto-upgrade off.
Expected: `fly redis status img-resize` shows the plan and a Private URL.

- [ ] **Step 2: Connect**

Run: `fly redis connect` and use `redis-cli -p 16379` from a second shell.

- [ ] **Step 3: Exercise each command**

Against a test stream, confirm `XGROUP CREATE ... MKSTREAM`, `XADD`, `XREADGROUP ... BLOCK 5000`, `XPENDING`, `XAUTOCLAIM`, `XACK`, `XDEL`, `EVAL`, and `PUBLISH` with `SUBSCRIBE` in a second client. Delete the test keys afterwards.
Expected: each command works, and `XREADGROUP` with `BLOCK` really waits.

- [ ] **Step 4: Confirm the over-limit behaviour**

Check Upstash's docs or support for what happens above the plan's 50 GB, and confirm in the console that auto-upgrade is off.
Expected: traffic is slowed, not charged.

- [ ] **Step 5: Record the results**

Report to the owner. If `BLOCK` is unavailable, Task 5's `Read` sleeps 500ms between non-blocking reads instead. If bandwidth above the limit is billed, stop and raise it, because the spec's bandwidth section depends on it.

### Task 3: Limit the longer source side

**Files:**
- Modify: `internal/settings/settings.go`, `internal/image/resizer.go`
- Test: `internal/image/resizer_test.go`

**Interfaces:**
- Produces: `settings.Service.MaxImageSourceDimension uint`, `SVC_MAX_IMG_SOURCE_DIMENSION`, default `8192`.

- [ ] **Step 1: Write the failing test**

`TestProcessSourceDimensionLimit`, table-driven with `MaxImageSourceDimension = 100` in the settings and a `Width: 32` request. Add `MaxImageSourceDimension: 1024` to `buildSettings()`.

| Source | Want |
|---|---|
| 100×10 | `success` |
| 10×100 | `success` |
| 101×10 | `failure` |
| 10×101 | `failure` |

- [ ] **Step 2: Run it and watch it fail**

Run: `APP_ENV=test go test ./internal/image -run SourceDimension`
Expected: FAIL, the above-limit cases return `success`.

- [ ] **Step 3: Add the check in `resize`**

After `DecodeConfig` and before decoding: reject when either side exceeds `MaxImageSourceDimension`.

- [ ] **Step 4: Run the suite**

Run: `make test && make lint`
Expected: PASS, `0 issues.`

- [ ] **Step 5: Commit**

```bash
git add internal
git commit -m "Reject images whose longer side exceeds the source limit"
```

### Task 4: Real-Redis tests, cache changes and job submission

**Files:**
- Create: `internal/testredis/testredis.go`, `internal/queue/queue.go`, `internal/queue/queue_test.go`
- Modify: `internal/cache/redis_image_cache.go`, `internal/cache/redis_image_cache_test.go`, `internal/settings/settings.go`, `Makefile`, `.github/workflows/build.yaml`

**Interfaces:**
- Produces:
  - `testredis.Client(t *testing.T, db int) *redis.Client`: reads `SVC_TEST_REDIS_ADDR`, calls `t.Skip` if unset, flushes the DB. Each package uses its own DB: cache 1, queue 2, worker 3, rest 4, ratelimit 5.
  - `model.Job{ID, URL string; Width, Height uint}`
  - `model.ImageID(url string, width, height uint) string`, moved from `genImageID` unchanged.
  - `queue.New(client *redis.Client, settings *settings.Settings) *Queue`
  - `(*Queue).Submit(ctx, model.Job) (queue.Submission, error)`, where `Submission` is `Created`, `Active` or `Failed`.
  - `(*Queue).State(ctx, id string) (queue.State, bool, error)`, with `State{Status queue.Status; Error string}` and statuses `queued`, `processing`, `failed`.
  - `(*Queue).Depth(ctx) (int64, error)`, from `XLEN`.
  - Settings: `JobStateTTL` (`SVC_JOB_STATE_TTL`, `1h`), `FailedJobTTL` (`SVC_FAILED_JOB_TTL`, `5m`).

- [ ] **Step 1: Add the test Redis wiring**

Add `make test-redis`: start `redis:8.10.3-alpine` as container `img-resize-test-redis` on `127.0.0.1:16380`, run `make test` with `SVC_TEST_REDIS_ADDR=127.0.0.1:16380`, and always remove the container. In `build.yaml`, add a `redis:8.10.3-alpine` service on port 6379 and set `SVC_TEST_REDIS_ADDR=localhost:6379` on the test step.

- [ ] **Step 2: Write the failing cache tests**

In `redis_image_cache_test.go`: `TestRedisImageCacheContainsUsesExists` replaces the existing Contains test with `mock.ExpectExists("resize:image:foo").SetVal(1)`. Update the other tests to expect `resize:image:` keys. Add `TestRedisImageCacheContainsMissing` with `SetVal(0)`.

- [ ] **Step 3: Run and watch them fail**

Run: `APP_ENV=test go test ./internal/cache`
Expected: FAIL on unexpected commands.

- [ ] **Step 4: Change the cache**

Prefix keys with `resize:image:`. `Contains` uses `EXISTS`. Don't log `redis.Nil` as an error in `Get`.

- [ ] **Step 5: Commit the cache change and test wiring**

```bash
git add Makefile .github internal/cache internal/testredis
git commit -m "Check the image cache with EXISTS and prefix its keys"
```

- [ ] **Step 6: Write the failing queue tests**

```go
func TestSubmitCreatesOneJob(t *testing.T)            // Created; Depth 1; State queued
func TestSubmitDuplicateIsActive(t *testing.T)        // second Submit Active; Depth still 1
func TestSubmitConcurrentCreatesOneJob(t *testing.T)  // 50 goroutines; one Created; Depth 1
func TestSubmitRepeatsAfterFailedStateExpires(t *testing.T)
func TestSubmitFailedStateReportsFailed(t *testing.T) // failed state present: Failed; Depth 0
func TestStateMissing(t *testing.T)                   // State returns found=false
```

- [ ] **Step 7: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL, the package doesn't compile until stubs exist. Add empty method stubs, then expect assertion failures.

- [ ] **Step 8: Implement `queue.go`**

Keys: `resize:jobs`, `resize:job:<id>`. Fields: `status`, `error`. Submit is one Lua script: read state; if missing, `HSET`, `EXPIRE` to `JobStateTTL`, `XADD` with fields `id`, `url`, `width`, `height`, and return created; otherwise return `failed` or `active`.

- [ ] **Step 9: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS with no skips in `internal/queue`.

```bash
git add internal
git commit -m "Add a Redis Streams job queue with atomic submission"
```

### Task 5: Reading, acknowledging and reclaiming jobs

**Files:**
- Modify: `internal/queue/queue.go`, `internal/queue/queue_test.go`, `internal/settings/settings.go`

**Interfaces:**
- Consumes: `Queue`, `model.Job` from Task 4.
- Produces:
  - `queue.Delivery{Job model.Job; MessageID string; Attempts int64}`
  - `(*Queue).EnsureGroup(ctx) error`: creates `resize-workers` at ID `0` with `MKSTREAM`, ignoring `BUSYGROUP`. It must not start at `$`, or jobs queued before the first worker started would be skipped.
  - `(*Queue).Read(ctx, consumer string, block time.Duration) (*queue.Delivery, error)`: `nil, nil` when nothing arrives.
  - `(*Queue).Reclaim(ctx, consumer string) (deliveries []queue.Delivery, abandoned int, err error)`
  - `(*Queue).MarkProcessing(ctx, id string) error`
  - `(*Queue).Complete(ctx, queue.Delivery) error`: `XACK`, `XDEL`, delete state, publish the ID to `resize:done`, in that order.
  - `(*Queue).Fail(ctx, queue.Delivery, message string) error`: sets `failed`, `error`, TTL `FailedJobTTL`, then `XACK`, `XDEL`, publish.
  - Settings: `JobClaimIdle` (`SVC_JOB_CLAIM_IDLE`, `60s`), `JobMaxAttempts` (`SVC_JOB_MAX_ATTEMPTS`, `3`).

- [ ] **Step 1: Write the failing tests**

```go
func TestReadReturnsSubmittedJob(t *testing.T)            // fields match; Attempts 1
func TestReadReturnsNilWhenEmpty(t *testing.T)
func TestReadDeliversOnlyOnce(t *testing.T)               // two consumers, one job
func TestCompleteRemovesJob(t *testing.T)                 // Depth 0; state gone; nothing pending
func TestFailStoresError(t *testing.T)                    // State failed, "fetch failed"; Depth 0
func TestReclaimReturnsIdleJob(t *testing.T)              // ClaimIdle 10ms; other consumer gets it, Attempts 2
func TestReclaimIgnoresBusyJob(t *testing.T)              // ClaimIdle 1h; nothing returned
func TestReclaimAbandonsAfterMaxAttempts(t *testing.T)    // third crash, then next Reclaim marks failed; Depth 0
func TestReadDropsMalformedEntry(t *testing.T)            // raw XADD with a bad width: acked, Depth 0, Read returns nil
func TestEnsureGroupIsRepeatable(t *testing.T)
func TestEnsureGroupSeesExistingJobs(t *testing.T)     // Submit before EnsureGroup: Read still returns the job
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL for missing behaviour, after adding stubs.

- [ ] **Step 3: Implement**

`Reclaim` runs `XAUTOCLAIM` with `MinIdle: JobClaimIdle`, then looks up each message's delivery count with `XPENDING`. Above `JobMaxAttempts`, call `Fail` with `fetch failed`, leave it out of the deliveries and add it to `abandoned`, so the job runs at most `JobMaxAttempts` times. `Read` and `Reclaim` acknowledge, delete and log malformed entries.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Add job delivery with crash recovery and a retry limit"
```

### Task 6: Classify resize failures

**Files:**
- Create: `internal/image/failure.go`
- Modify: `internal/image/resizer.go`, `internal/image/fetch.go`
- Test: `internal/image/resizer_test.go`, `internal/image/fetch_test.go`

**Interfaces:**
- Produces:
  - `image.Failure{Reason string; Permanent bool; Err error}` implementing `error` and `Unwrap`.
  - Reasons: `image.ReasonFetch` = `fetch failed`, `image.ReasonInvalid` = `invalid image`, `image.ReasonTooLarge` = `image too large`.
  - `(*Resizer).Resize(ctx, url string, width, height uint) ([]byte, error)`: fetch plus resize. Every error is a `*Failure`. No cache access.

- [ ] **Step 1: Write the failing test**

`TestResizeClassifiesFailures`, table-driven with `errors.As` on `*image.Failure`:

| Case | Reason | Permanent |
|---|---|---|
| source returns 404 | fetch failed | true |
| source returns 503 | fetch failed | false |
| source times out | fetch failed | false |
| body isn't a JPEG | invalid image | true |
| body exceeds `MaxImageSize` | image too large | true |
| over the pixel limit | image too large | true |
| over the source side limit | image too large | true |
| derived size over the limit | image too large | true |
| internal address (`AllowInternalURLs` false) | fetch failed | true |

- [ ] **Step 2: Run and watch it fail**

Run: `APP_ENV=test go test ./internal/image -run ClassifiesFailures`
Expected: FAIL, `Resize` is undefined until stubbed.

- [ ] **Step 3: Implement**

Add sentinel `errInternalAddress`, wrapped by `rejectInternalAddress`, and detect it with `errors.Is`. Read `MaxImageSize + 1` bytes so an oversized body is detected instead of truncated. `processImageResize` calls `Resize` so existing tests keep passing until Task 8.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Classify resize failures as permanent or transient"
```

### Task 7: The worker

**Files:**
- Create: `internal/worker/worker.go`, `internal/worker/worker_test.go`
- Modify: `internal/settings/settings.go`

**Interfaces:**
- Consumes: `queue.Queue` from Tasks 4–5, `(*image.Resizer).Resize` from Task 6, `cache.ImageCacheAdapter`.
- Produces:
  - `worker.New(settings, *queue.Queue, cache.ImageCacheAdapter, *image.Resizer) *Worker`
  - `(*Worker).Run(ctx context.Context) error`: blocks until `ctx` is cancelled, then finishes current jobs and returns.
  - Settings: `WorkerConcurrency` (`SVC_WORKER_CONCURRENCY`, `1`), `JobReclaimInterval` (`SVC_JOB_RECLAIM_INTERVAL`, `15s`).

- [ ] **Step 1: Write the failing tests**

Use real Redis, a real `Resizer` and an `httptest` source with `AllowInternalURLs: true`.

```go
func TestWorkerStoresResizedImage(t *testing.T)        // image cached; Depth 0; state gone
func TestWorkerSkipsCachedImage(t *testing.T)          // source request count stays 0
func TestWorkerRecordsPermanentFailure(t *testing.T)   // 404 source: failed, "fetch failed", one source request
func TestWorkerRetriesTransientFailure(t *testing.T)   // 503 then 200: image cached after a reclaim
func TestWorkerGivesUpAfterMaxAttempts(t *testing.T)   // always 503: failed; 3 source requests
func TestWorkerRecoversJobFromDeadConsumer(t *testing.T) // Read by a fake consumer, never acked; worker finishes it
func TestWorkerCompletesJobWithoutState(t *testing.T)  // state deleted before read: image still cached, no error
func TestWorkerRetriesWhenCacheWriteFails(t *testing.T) // rejecting cache: job left unacknowledged
func TestWorkerFinishesCurrentJobOnShutdown(t *testing.T) // cancel mid-fetch: Run waits; job complete
func TestWorkerSurvivesRedisErrors(t *testing.T)       // closed client: Run keeps looping until cancelled
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL, stubs do nothing.

- [ ] **Step 3: Implement**

Consumer name is `FLY_MACHINE_ID`, else the hostname. Start `WorkerConcurrency` read loops (`Read` with a 5s block), plus one loop running `Reclaim` every `JobReclaimInterval`, sending reclaimed jobs through the same handler. Job work uses a context not cancelled by shutdown. Read and Redis errors log and wait one second before retrying.

Handler order: cached → `Complete`. Otherwise `MarkProcessing`, `Resize`, `cache.Add`, `Complete`. Permanent failure → `Fail` with `Failure.Reason`. Transient failure or `Add` returning false → log and leave unacknowledged. Log the full error; store only the reason.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Add the queue worker"
```

### Task 8: Submit async jobs from the web handler

**Files:**
- Modify: `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`, `internal/service/router.go`, `internal/service/router_test.go`, `internal/service/service.go`, `internal/service/service_test.go`, `internal/image/resizer.go`, `internal/image/resizer_test.go`, `internal/settings/settings.go`, `cmd/img-resize/main.go`
- Delete: `internal/image/resizing_progress.go`, `internal/image/resizing_progress_test.go`, `internal/image/resizer_adapter.go`

**Interfaces:**
- Consumes: `queue.Queue`, `model.ImageID`.
- Produces:
  - `rest.NewResizerHandler(settings, cache.ImageCacheAdapter, *queue.Queue) *ResizerHandler`
  - `service.NewRouter(settings, cache.ImageCacheAdapter, *queue.Queue) *chi.Mux`
  - `image.NewResizer(settings) *Resizer`, cache parameter removed.
  - Setting `MaxQueueDepth int64` (`SVC_MAX_QUEUE_DEPTH`, `10000`).
- Removes: `SVC_ASYNC_RESIZE`, `SVC_IMG_RESIZE_TIMEOUT`, `Process`, `ProcessAsync`, `ImageResizer`, `ResizingProgress`.

- [ ] **Step 1: Write the failing tests**

Replace the mock-resizer tests with real-Redis handler tests:

```go
func TestResizeEnqueuesJobs(t *testing.T)            // 200; result "enqueued" per URL; Depth equals URL count
func TestResizeReportsCachedImage(t *testing.T)      // "success", cached true; Depth 0
func TestResizeRepeatedURLCreatesOneJob(t *testing.T) // batch [a, a]: two results, Depth 1
func TestResizeReportsFailedJob(t *testing.T)        // existing failed state: "failure" with the id
func TestResizeRejectsFullQueue(t *testing.T)        // MaxQueueDepth 1 and one job queued: 503, Retry-After set
func TestResizeAcceptsAsyncParameter(t *testing.T)   // ?async=true behaves the same
```

Keep `TestResizeImageDimensionLimit` with its async case, now expecting 200 for both within-limit paths. Move the `Process` tests in `resizer_test.go` to `Resize`, dropping those duplicated by Task 7's tests.

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL, the handler has the old constructor until stubbed.

- [ ] **Step 3: Implement and remove the old path**

Check `Depth` once per request, before submitting. `main.go` runs an in-process worker goroutine for now, so the service still works between tasks. Task 11 replaces this. Remove `TestServiceFinishesInFlightRequestsOnShutdown` for now, because nothing stays in flight until sync mode exists. Task 10 restores it.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add -A internal cmd
git commit -m "Make resize requests asynchronous through the Redis queue"
```

### Task 9: Report job state from `GET /v1/image/{id}`

**Files:**
- Modify: `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`

**Interfaces:**
- Consumes: `(*Queue).State`, `cache.ImageCacheAdapter`.
- Produces: `ImageCacheAdapter` changes to `Get(ctx, key) ([]byte, bool, error)` and `Contains(ctx, key) (bool, error)`, so handlers can tell a Redis failure from a miss. A miss returns `false, nil`. Update all callers, including the worker, and the LRU adapter.

- [ ] **Step 1: Write the failing tests**

```go
func TestGetImageReturnsImage(t *testing.T)        // 200; image/jpeg; Cache-Control "private, max-age=900"
func TestGetImageWhileQueued(t *testing.T)         // 202; result "enqueued"
func TestGetImageWhileProcessing(t *testing.T)     // 202
func TestGetImageFailed(t *testing.T)              // 422; error "invalid image"
func TestGetImageUnknown(t *testing.T)             // 404
func TestGetImageRedisDown(t *testing.T)           // closed client: 503, not 404
func TestResizeRedisDown(t *testing.T)             // closed client: POST returns 503
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL on status codes.

- [ ] **Step 3: Implement**

Set headers before `WriteHeader`. This also fixes the misplaced `content-type` in `writeImageResponse`. The call never blocks.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Report job state when fetching an image"
```

### Task 10: Sync mode

**Files:**
- Create: `internal/queue/wait.go`, `internal/queue/wait_test.go`
- Modify: `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`, `internal/service/service.go`, `internal/service/service_test.go`, `internal/settings/settings.go`

**Interfaces:**
- Consumes: `resize:done` messages from `Complete` and `Fail`.
- Produces:
  - `(*Queue).Listen(ctx)`: holds one subscription and dispatches to waiters; reconnects on error. Each web machine runs one.
  - `(*Queue).Wait(ctx, id string, timeout time.Duration) (queue.Outcome, error)`, where `Outcome` is `Done`, `Failed` (with `Error string`) or `TimedOut`.
  - Setting `SyncWaitTimeout` (`SVC_SYNC_WAIT_TIMEOUT`, `15s`).

- [ ] **Step 1: Write the failing tests**

```go
func TestWaitReturnsWhenJobCompletes(t *testing.T)
func TestWaitReturnsFailure(t *testing.T)             // Error "invalid image"
func TestWaitTimesOut(t *testing.T)
func TestWaitSeesJobFinishedBeforeWaiting(t *testing.T) // no state, Listen idle: Done
func TestWaitRecoversFromLostMessage(t *testing.T)    // state removed without publish: Done within ~1s
func TestWaitServesSeveralWaiters(t *testing.T)       // two waiters, one job, both return
func TestWaitStopsWhenClientDisconnects(t *testing.T) // cancelled ctx returns; waiter count 0; job still queued

func TestSyncReturnsImage(t *testing.T)               // running worker; 200; image/jpeg bytes
func TestSyncReturnsCachedImage(t *testing.T)         // no worker; 200
func TestSyncRequiresOneURL(t *testing.T)             // 0 and 2 URLs: 400
func TestSyncTimeoutReturnsID(t *testing.T)           // no worker; 202; id present
func TestSyncFailureReturnsError(t *testing.T)        // 422, "fetch failed"
```

Restore `TestServiceFinishesInFlightRequestsOnShutdown` using `?sync=true` with a worker running.

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL, stubs return `TimedOut`.

- [ ] **Step 3: Implement `wait.go`**

Register the waiter first, then check state, so a finish in between isn't missed. Recheck state once a second. No state means finished: `Done`, and the handler then reads the image. If the image is gone by then, return 202 with the ID. Always remove the waiter on return.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Add sync mode that waits for the resized image"
```

### Task 11: Separate web and worker roles

**Files:**
- Create: `internal/worker/service.go`, `internal/worker/service_test.go`
- Modify: `cmd/img-resize/main.go`, `internal/settings/settings.go`, `internal/settings/settings_test.go`, `compose.yml`, `Makefile`, `README.md`

**Interfaces:**
- Consumes: `(*Worker).Run`, `(*Queue).Listen`.
- Produces:
  - Setting `Role string` (`SVC_ROLE`, `web`).
  - `worker.NewService(settings, *Worker) *Service`, with `Start() error`: runs `Run`, serves `/health` on `HTTP_SERVER_PORT`, and on SIGTERM stops reading and finishes the current job.

- [ ] **Step 1: Write the failing tests**

```go
func TestSettingsLoadRole(t *testing.T)               // default "web"; "worker" from env
func TestWorkerServiceHealth(t *testing.T)            // /health 200
func TestWorkerServiceFinishesJobOnSIGTERM(t *testing.T) // job completes, Start returns nil
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL.

- [ ] **Step 3: Implement**

`main.go` switches on `Role`, and an unknown role exits with an error. Remove the in-process worker from Task 8. `compose.yml` adds a `worker` service with `SVC_ROLE=worker`, sharing the Redis settings. Add `make run-worker`. Update the README for async-by-default, `?sync=true`, the two roles and new settings.

- [ ] **Step 4: Run the suite**

Run: `make test-redis && make lint`
Expected: PASS.

- [ ] **Step 5: End-to-end crash check**

Render compose as before with ports 14000 and 16379 under project `resizer-verify`, then `up -d --build --scale worker=2`. Submit a slow source, `docker kill` the worker holding it, and poll.
Expected: the other worker finishes after `SVC_JOB_CLAIM_IDLE` and `GET` returns 200. Afterwards run `down -v --rmi local`, and confirm `resizer-redis-1` is unaffected.

- [ ] **Step 6: Commit**

```bash
git add cmd internal compose.yml Makefile README.md
git commit -m "Run web and worker as separate roles"
```

### Task 12: Throttling image requests per CIDR prefix

**Files:**
- Create: `internal/ratelimit/ratelimit.go`, `internal/ratelimit/ratelimit_test.go`
- Modify: `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`, `internal/settings/settings.go`

**Interfaces:**
- Produces:
  - `ratelimit.ClientIP(r *http.Request, trustFly bool) string`
  - `ratelimit.PrefixKey(ip string, ipv4Bits, ipv6Bits int) string`
  - `ratelimit.New(client *redis.Client, now func() time.Time) *Limiter`
  - `(*Limiter).Allow(ctx, bucket, key string, amount, limit int64) (allowed bool, retryAfter time.Duration, err error)`: keys `resize:<bucket>:<key>:<minute>`, TTL 2m.
  - Settings: `RateLimitPerMinute int64` (`SVC_RATE_LIMIT_PER_MINUTE`, `120`), `RateLimitIPv4Prefix int` (`SVC_RATE_LIMIT_IPV4_PREFIX`, `32`), `RateLimitIPv6Prefix int` (`SVC_RATE_LIMIT_IPV6_PREFIX`, `64`), `TrustFlyClientIP bool` (`SVC_TRUST_FLY_CLIENT_IP`, `false`).
  - `rest.NewResizerHandler` and `service.NewRouter` gain a `*ratelimit.Limiter` parameter.

- [ ] **Step 1: Write the failing tests**

`TestPrefixKey`, table-driven with bits 32 and 64 unless noted:

| IP | Want |
|---|---|
| `203.0.113.7` | `203.0.113.7/32` |
| `203.0.113.7` (bits 24) | `203.0.113.0/24` |
| `2001:db8:1:2:aaaa::1` | `2001:db8:1:2::/64` |
| `2001:db8:1:2:bbbb::9` | `2001:db8:1:2::/64` |
| `::ffff:203.0.113.7` | `203.0.113.7/32` |
| `not-an-ip` | `not-an-ip` |

```go
func TestClientIPIgnoresHeaderByDefault(t *testing.T) // spoofed Fly-Client-IP: RemoteAddr
func TestClientIPUsesHeaderWhenTrusted(t *testing.T)
func TestClientIPFallsBackWithoutHeader(t *testing.T) // trusted, header missing: RemoteAddr
func TestAllowUpToLimit(t *testing.T)                 // 120 allowed, 121st not
func TestAllowCountsAmount(t *testing.T)              // amount 100 twice with limit 120: second refused
func TestAllowResetsNextMinute(t *testing.T)          // fake clock
func TestAllowReportsRetryAfter(t *testing.T)         // 30s into a minute: 30s
func TestAllowSharedBetweenLimiters(t *testing.T)     // two Limiter instances share the count

func TestResizeThrottled(t *testing.T)                // 429; Retry-After; no job added
func TestResizeThrottleCountsURLs(t *testing.T)       // limit 3, 4-URL batch: 429
func TestResizeThrottleSharesPrefix(t *testing.T)     // two addresses in one /64 share the limit
func TestResizeThrottleRedisDown(t *testing.T)        // closed client: 503
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL.

- [ ] **Step 3: Implement**

`Allow` uses a transaction of `INCRBY` and `EXPIRE`. Use `netip.ParseAddr`, `Unmap` and `Prefix`. Run the check in the handler after authentication and parsing, once the URL count is known.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Throttle image requests per CIDR prefix"
```

### Task 13: Throttling downloads by bytes

**Files:**
- Modify: `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`, `internal/settings/settings.go`

**Interfaces:**
- Consumes: `(*Limiter).Allow` with bucket `bytes`.
- Produces: setting `RateLimitBytesPerMinute int64` (`SVC_RATE_LIMIT_BYTES_PER_MINUTE`, `104857600`).

- [ ] **Step 1: Write the failing tests**

```go
func TestGetImageThrottledByBytes(t *testing.T)   // limit below 2 images: first 200, second 429 with Retry-After
func TestSyncThrottledByBytes(t *testing.T)       // sync image bytes count too
func TestGetImageStateIsNotCounted(t *testing.T)  // polling a queued job uses no byte budget
```

- [ ] **Step 2: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL, second request returns 200.

- [ ] **Step 3: Implement**

After reading the image, call `Allow` with `len(data)`, before writing any response.

- [ ] **Step 4: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add internal
git commit -m "Throttle image downloads by bytes per CIDR prefix"
```

### Task 14: Metrics

**Files:**
- Create: `internal/metrics/metrics.go`, `internal/metrics/metrics_test.go`
- Modify: `internal/worker/worker.go`, `internal/worker/worker_test.go`, `internal/worker/service.go`, `internal/rest/resizer_handler.go`, `internal/rest/resizer_handler_test.go`, `internal/service/service.go`, `internal/settings/settings.go`, `go.mod`, `go.sum`

**Interfaces:**
- Produces:
  - `metrics.New() *Metrics`, with its own registry, exposing the spec's names: `resize_queue_depth`, `resize_job_duration_seconds`, `resize_jobs_total{result}`, `resize_throttled_total{limit}`, `resize_rejected_total{reason}`, `resize_bandwidth_bytes_total{direction}`.
  - `(*Metrics).Serve(ctx, port uint) error`: `/metrics` on its own listener.
  - `(*Metrics).SampleQueueDepth(ctx, depth func(context.Context) (int64, error))`: updates the gauge every 5 seconds.
  - `rest.NewResizerHandler`, `service.NewRouter` and `worker.New` gain a `*metrics.Metrics` parameter.
  - Setting `MetricsPort uint` (`SVC_METRICS_PORT`, `9091`).

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/prometheus/client_golang@latest && go mod tidy`
Expected: `go.mod` lists it as direct. Include this in the commit.

- [ ] **Step 2: Write the failing tests**

```go
func TestServeExposesMetrics(t *testing.T)          // scrape contains resize_queue_depth
func TestSampleQueueDepth(t *testing.T)             // gauge equals the function's value
func TestSampleKeepsLastValueOnError(t *testing.T)
func TestWorkerRecordsResults(t *testing.T)         // success, failure, retried, abandoned counts
func TestWorkerRecordsDuration(t *testing.T)        // histogram count 1
func TestHandlerRecordsThrottling(t *testing.T)     // limit="requests" and limit="bytes"
func TestHandlerRecordsQueueFull(t *testing.T)      // reason="queue_full"
func TestRecordsBandwidth(t *testing.T)             // direction="stored" and "served" equal the image size
func TestPublicPortHidesMetrics(t *testing.T)       // GET /metrics on the router: 404
```

- [ ] **Step 3: Run and watch them fail**

Run: `make test-redis`
Expected: FAIL.

- [ ] **Step 4: Implement and wire both roles**

Web runs `Serve` and `SampleQueueDepth`. Worker runs `Serve`. Count `retried` from reclaimed deliveries and `abandoned` from `Reclaim`'s count.

- [ ] **Step 5: Run the suite and commit**

Run: `make test-redis && make lint`
Expected: PASS.

```bash
git add go.mod go.sum internal
git commit -m "Expose Prometheus metrics from both roles"
```

### Task 15: Fly configuration and launch (approval required)

Write the config first. Stop and ask the owner before Step 3, and again before Step 7. There is no CI deployment: the spec left it optional, and the owner hasn't asked for it.

**Files:**
- Create: `deploy/fly/web.toml`, `deploy/fly/worker.toml`, `deploy/fly/autoscaler.toml`, `deploy/fly/README.md`

- [ ] **Step 1: Write the three configs**

Check each key against current Fly docs. Common to web and worker: `primary_region = "fra"`, `shared-cpu-1x` with 256 MB, `[metrics]` on port 9091 at `/metrics`, and these `[env]` values:

| Setting | Value |
|---|---|
| `SVC_MAX_IMG_DIMENSION` | `2048` |
| `SVC_MAX_IMG_PIXELS` | `12000000` |
| `SVC_MAX_IMG_SOURCE_DIMENSION` | `8192` |
| `SVC_IMG_CACHE_TTL` | `15m` |

Web also sets `SVC_ROLE=web`, `SVC_TRUST_FLY_CLIENT_IP=true`, an `[http_service]` on port 4000 with `force_https`, `auto_stop_machines = "stop"`, `auto_start_machines = true`, `min_machines_running = 1`, and a `/health` check.

Worker also sets `SVC_ROLE=worker`, `GOMEMLIMIT=200MiB`, `kill_timeout = 30`, a `/health` check, and no `[http_service]`.

Autoscaler uses the `flyio/fly-autoscaler` image with:

```
FAS_APP_NAME = "img-resize-worker"
FAS_STARTED_MACHINE_COUNT = "min(5, ceil(backlog / 30))"
FAS_PROMETHEUS_METRIC_NAME = "backlog"
FAS_PROMETHEUS_ADDRESS = "https://api.fly.io/prometheus/<org-slug>"
FAS_PROMETHEUS_QUERY = <max depth × average duration, with a 1-second fallback when idle>
```

- [ ] **Step 2: Write `deploy/fly/README.md`**

List the launch commands and secrets: `AUTH_USERNAME`, a strong generated `AUTH_PASSWORD`, `SVC_REDIS_HOST` and `SVC_REDIS_PASSWORD` for both apps, and `FAS_API_TOKEN` and `FAS_PROMETHEUS_TOKEN` for the autoscaler. Note that the `scratch` image has no shell, so `fly ssh console` gives no prompt. Commit:

```bash
git add deploy
git commit -m "Add Fly.io configuration for web, worker and autoscaler"
```

- [ ] **Step 3: Deploy web and one worker (needs approval)**

Ask first. Deploy with the README's commands, keeping the Upstash database from Task 2.
Expected: `/health` returns 200 over HTTPS and a real resize works through `?sync=true`.

- [ ] **Step 4: Measure on real machines**

Send real 12 MP photos at the limits, including a tall one at 1464×8192, watching `fly_instance_*` memory and `resize_job_duration_seconds`.
Expected: no OOM kill, and memory stays under 256 MB. If not, lower the limits and tell the owner. Report jobs per second per worker.

- [ ] **Step 5: Create the worker pool and autoscaler**

Scale workers to exactly 5 machines, then deploy the autoscaler with `--ha=false`.
Expected: one worker running, four stopped.

- [ ] **Step 6: Load check**

Temporarily raise `SVC_RATE_LIMIT_PER_MINUTE` on web, then send a burst large enough for the formula to ask for more than 5 workers.
Expected: workers rise and stop at 5, the queue drains, workers fall back to 1, and Grafana shows it. Restore the setting.

- [ ] **Step 7: Report and hand over**

Report the measured limits, throughput and the Fly bill estimate. Ask the owner before merging to `main`, since that triggers the image publish.

---

## Spec Coverage

| Spec section | Tasks |
|---|---|
| Upstash check | 2 |
| Source limit, `scratch` image | 1, 3 |
| Roles | 11 |
| API, job states, generic errors, queue full | 6, 8, 9, 10 |
| Redis data, `EXISTS`, key prefix, Lua submission | 4 |
| Worker loop, reclaim, shutdown | 5, 7, 11 |
| Sync wait | 10 |
| Throttling | 12, 13 |
| Bandwidth, client caching | 2, 9, 13 |
| Metrics | 14 |
| Scaling, smaller workers | 15 |
| Testing with real Redis, compose crash check, load check | 4, 11, 15 |
