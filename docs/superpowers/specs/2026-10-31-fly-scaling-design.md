# Scaling img-resize on Fly.io

Status: draft, awaiting approval. Items marked **(my call)** were not decided by the project owner and can be changed.

## Why

The service should run on Fly.io on small, cheap instances and handle bursts by adding resizing capacity automatically. An attack or a traffic spike must not produce an unbounded bill or take the service down.

Today one process does everything. It accepts requests, keeps the job queue and progress tracking in memory, and resizes images. That rules out running several machines: a poll can land on a machine that doesn't know the job, queued jobs die with their machine, and every machine needs enough memory for the largest resize.

## Goals

- A web service that accepts requests and returns immediately, and a separate fleet of workers that resize images.
- A queue that lives outside both, in Redis, and survives any machine dying.
- Worker count that follows the backlog automatically, with a hard upper limit.
- Workers small enough to run on small Fly machines.
- Per-client throttling that holds across all web machines.
- Metrics that show what the service is doing and drive scaling.

## Non-goals

- Per-user API keys or replacing Basic Auth. All callers keep sharing one login.
- Multiple regions.
- Webhooks or other completion callbacks. Clients poll, or use sync mode.
- Image formats other than JPEG.
- Removing the in-memory LRU cache code. It stays as is, unused in production.

## Anti-goals

The design has failed, even if it works, if any of these happen:

- A deploy, a scale-down or a worker crash silently loses queued jobs.
- Spend can grow without limit under load.
- One bad image can kill workers forever.
- Web machines need to hold resize-sized amounts of memory.

## Constraints

- Go 1.27.3, chi, go-redis v9, a `scratch` image (no shell).
- Everything runs in Fly's `fra` region, the owner's choice. That includes Upstash, whose primary region can't be changed later.
- Managed Redis is Upstash on Fly, on pay-as-you-go with eviction on, the owner's choice for a demo. The private URL has the form `redis://password@fly-<name>.upstash.io`.
- Fly's proxy starts and stops machines but never creates them, and scales on connections, not queue length.
- fly-autoscaler targets a whole app, reconciles every 15 seconds, and never goes below one machine.
- Fly scrapes app metrics every 15 seconds.
- Pay-as-you-go costs $0.20 per 100K commands, holds up to 10 GB and has no monthly cap. The queue and the image cache share that storage, and every image crosses Redis twice: stored by a worker, read by web.
- Commits: one fix per commit, a single-line message, no co-author line. Work happens on a branch, with tests written first.

## Approaches

| | Approach | Trade-off | Outcome |
|---|---|---|---|
| A | Redis Streams plus a small amount of our own code | No new dependency and full control of job state, but we write and test retries and crash recovery | **Chosen by the owner** |
| B | The asynq job library | Least queue code, but pre-1.0, unconfirmed on Upstash, and it doesn't cover sync mode's "job finished" signal | Rejected: one job type means most of it would go unused |
| C | A separate queue service (Postgres, SQS) | Adds a second stateful system | Dropped: Redis is required for the cache anyway |

The owner also decided that the web and worker roles are separate, that async is the default, and that sync mode takes exactly one URL.

## Design

### Roles

One binary and one image, with the role chosen by `SVC_ROLE=web|worker`, defaulting to `web` **(my call)**. They deploy as two Fly apps, `img-resize-web` and `img-resize-worker`, because the autoscaler targets a whole app.

- **Web:** authentication, validation, throttling, adding jobs, serving results. It holds a finished image in memory only while serving it, and never decodes or resizes.
- **Worker:** reads jobs, fetches, resizes and stores. It serves only health and metrics.

The fetching and resizing code in `internal/image` moves behind the worker. The in-process channel, the worker pool and `ResizingProgress` are removed.

### API

`POST /v1/resize`

Async is the default. The body and the 100-URL limit stay the same, and existing dimension validation still applies. The response is per URL, as today, with status 200 **(my call: not worth changing to 202)**. `?async=true` is still accepted and ignored, so existing async callers keep working. Callers relying on the old synchronous default will break; this is intended.

`POST /v1/resize?sync=true`

- Exactly one URL, otherwise 400.
- Already cached: returns the image at once.
- Finished within the wait: 200 with `image/jpeg` bytes.
- Wait timed out: 202 with the JSON result, including the ID. The job keeps running.
- Failed: 422 with a JSON error **(my call)**.
- `SVC_SYNC_WAIT_TIMEOUT` defaults to 15s, below the server's 20s write timeout.

`GET /v1/image/{id}`

- 200 with the image.
- 202 while queued or processing.
- 422 with a JSON error if the job failed.
- 404 if the ID is unknown or expired.

This call no longer blocks, because sync mode is the way to wait **(my call)**.

Image responses carry `Cache-Control: private, max-age=900`, so a client that fetches the same image again doesn't use bandwidth **(my call)**. The ID identifies the exact URL and size, and the image doesn't change during its 15-minute lifetime.

Failure messages returned to clients stay generic: `fetch failed`, `invalid image`, `image too large`. Detail goes only to logs, so that error text can't be used to probe internal networks.

If the queue holds `SVC_MAX_QUEUE_DEPTH` jobs (default 10000, the current limit), `POST /v1/resize` returns 503 with `Retry-After` **(my call: whole request, not per URL)**.

### Redis data

| Key | Type | TTL | Purpose |
|---|---|---|---|
| `resize:jobs` | stream, consumer group `resize-workers` | none | The queue |
| `resize:job:<id>` | hash: status, error | 1h; failed jobs 5m | Job state |
| `resize:image:<id>` | string | `SVC_IMG_CACHE_TTL`, 15m on Fly | Finished images |
| `resize:done` | pub/sub channel | n/a | Job-finished notices for sync waiters |
| `resize:rate:<prefix>:<minute>` | counter | 2m | Throttling image requests |
| `resize:bytes:<prefix>:<minute>` | counter | 2m | Throttling downloads |

Redis is a short-lived handoff, not image storage. Clients are expected to fetch a finished image within 15 minutes.

Eviction is on, so a full Redis drops old images instead of rejecting new jobs. It removes keys with a TTL first, and the stream has none, so images go before the queue while any TTL keys remain. A client can still get a 404 for a finished image under heavy load, in which case it resubmits and the image is rebuilt. Evicting a job state has the same result.

The image key gains a prefix, which drops existing cached images. That's harmless for a cache.

`RedisImageCache.Contains` must use `EXISTS`. It currently downloads the whole image, and the web service calls it on every submission.

### Adding a job

Image IDs already identify identical requests. For each URL:

1. If the image exists, return `success` with `cached: true`.
2. Run a Lua script atomically: if no job state exists, create it as `queued` and `XADD` the job. A crash can't leave state without a job, or the reverse.
3. Return `enqueued` with the ID, whether the job was new or already running.

A failed job's state expires after 5 minutes, after which the same request can try again.

### Worker loop

Each worker reads with `XREADGROUP`, one job at a time, blocking for 5 seconds. The consumer name is `FLY_MACHINE_ID`, or the hostname locally. The group is created at startup, ignoring "already exists".

For each job:

1. If the image is already cached, finish the job.
2. Set the state to `processing`, then fetch, resize and store it.
3. On success, `XACK`, `XDEL`, delete the state, then publish to `resize:done`.
4. On a permanent failure (invalid JPEG, over a limit, blocked address, a 4xx from the source), set the state to `failed`, acknowledge, and publish.
5. On a transient failure (timeout, a 5xx from the source, a Redis error), leave it unacknowledged.

Every 15 seconds each worker runs `XAUTOCLAIM` for jobs idle longer than `SVC_JOB_CLAIM_IDLE` (default 60s, which must exceed the longest possible job). Jobs delivered `SVC_JOB_MAX_ATTEMPTS` times (default 3) become `failed`, whatever the reason. This covers crashes and images that kill a worker: the worker dies, the job is reclaimed, and after three attempts it stops.

`SVC_WORKER_CONCURRENCY` defaults to 1.

On SIGTERM a worker stops reading and finishes its current job. The `kill_timeout` must exceed the longest job. Anything cut off is reclaimed.

### Sync wait

Each web machine keeps one `resize:done` subscription and passes notices to the requests waiting on them.

A request registers its wait first, then checks job state, so a job that finishes in between isn't missed. It also rechecks state once a second as a safety net, because pub/sub messages can be lost.

### Throttling

Limits apply per CIDR prefix rather than per address, the owner's choice, because one IPv6 client usually controls many addresses. They use a fixed one-minute window in Redis, so all web machines share them.

- The prefix lengths are configurable. `SVC_RATE_LIMIT_IPV6_PREFIX` defaults to 64 and `SVC_RATE_LIMIT_IPV4_PREFIX` to 32 **(my call: a shorter IPv4 prefix would penalise unrelated neighbours)**.
- `POST /v1/resize` counts one per URL, so a 100-URL batch counts as 100. `SVC_RATE_LIMIT_PER_MINUTE` defaults to 120.
- Image bytes served, by `GET /v1/image/{id}` and sync mode, count against `SVC_RATE_LIMIT_BYTES_PER_MINUTE`, default 100 MB **(my call: bytes rather than requests, because bandwidth is what's being limited)**.
- Limits run after authentication. Over a limit returns 429 with `Retry-After`.
- `Fly-Client-IP` is trusted only when `SVC_TRUST_FLY_CLIENT_IP=true`, set in `fly.toml`. Otherwise the header could be forged, so `RemoteAddr` is used.
- If Redis can't be reached, the request fails with 503, since it couldn't be queued anyway.

The limits assume many separate clients. A single backend would need a higher limit, set through configuration.

### Bandwidth

There's no global bandwidth budget, the owner's choice after seeing costs. Pay-as-you-go has no plan limit on bandwidth, so only per-prefix throttling and the login limit what web can serve.

The worker cap limits machine spend. `SVC_MAX_QUEUE_DEPTH` limits the backlog. Throttling stops one client from using up either. Redis commands have no cap, but all of them sit behind the login, so only someone with the password can add to that bill.

### Metrics

Both roles expose Prometheus metrics at `/metrics` on port 9091, through `prometheus/client_golang`, a new dependency. The port is listed under `[metrics]` in `fly.toml` and is not in `[http_service]`, so it isn't public.

- `resize_queue_depth`: gauge from `XLEN`, sampled by web every 5 seconds.
- `resize_job_duration_seconds`: histogram from workers.
- `resize_jobs_total{result}`: `success`, `failure`, `retried`, `abandoned`.
- `resize_throttled_total{limit}`: counter from web, for `requests` and `bytes`.
- `resize_rejected_total{reason}`: such as `queue_full`.
- `resize_bandwidth_bytes_total{direction}`: stored and served bytes.

Fly's built-in metrics cover concurrency, response times, CPU and out-of-memory kills.

### Scaling

Workers use fly-autoscaler, deployed as its own always-on app with `FAS_STARTED_MACHINE_COUNT` and a pool of 5 pre-created worker machines **(my call on the pool; the cap of 5 is the owner's)**. That enforces the limit twice: the formula can't ask for more than 5, and no more than 5 exist. Starting a stopped machine is also faster than creating one.

The signal is backlog time, in seconds:

```
backlog = max(resize_queue_depth) * average job duration
workers = min(5, ceil(backlog / 30))
```

The average comes from `resize_job_duration_seconds`, with a fallback value when idle so the query never returns nothing. The autoscaler keeps at least one worker. The 30-second target is a starting value.

Reaction time is roughly 30 to 60 seconds, from the 15-second scrape, the 15-second reconcile and machine start. Short bursts wait in the queue.

Web uses Fly Proxy autostart and autostop with two machines and `min_machines_running = 1`, which is enough because it does little work.

### Smaller workers

Workers run on 256 MB machines, the owner's choice. The image limits are set to fit, in `fly.toml`:

| Setting | Fly value |
|---|---|
| `SVC_MAX_IMG_DIMENSION` | 2048 |
| `SVC_MAX_IMG_PIXELS` | 12000000 |
| `SVC_MAX_IMG_SOURCE_DIMENSION` | 8192 |
| `GOMEMLIMIT` | 200MiB |

Pixel count alone doesn't bound memory. The resize library's temporary buffer grows with source height × output width, so a 1-megapixel image of 16×65000 resized to 4096×4096 was OOM-killed at 256 MB. `SVC_MAX_IMG_SOURCE_DIMENSION` limits the longer source side and closes this. Its local default is 8192.

Measured in containers capped at 256 MB, with one CPU:

| Source | Output | Result |
|---|---|---|
| 7071×7071 (50MP, current limit) | 4096×4096 | Killed |
| 16×65000 (1MP) | 4096×4096 | Killed |
| 3464×3464 (12MP) | 2048×2048 | 112 MB |
| 1464×8192 (12MP, worst shape allowed) | 2048×2048 | 140 MB, and also passes at a 180 MB cap |
| 2930×8192 (24MP, would need a higher limit) | 2048×2048 | 192 MB, too little margin |

These used solid-colour images. Decoded memory doesn't depend on content, but a real file also holds up to `SVC_MAX_IMG_SIZE` (15 MB) of compressed data, and real photos take longer to decode. Step 4 repeats this on a real Fly machine with real photos, and measures throughput on a shared CPU.

## Delivery order

0. **Upstash check.** Against a real Upstash database in `fra`, confirm `XREADGROUP` with `BLOCK`, `XAUTOCLAIM`, `EVAL`, and pub/sub over the Redis protocol. This needs `fly auth login` and the owner's approval to create a database. If blocking reads aren't available, workers poll with a short sleep instead.
1. **Queue and split.** Roles, Streams, job state, the API changes, `compose.yml` with a worker service, README.
2. **Throttling.**
3. **Metrics.**
4. **Fly deployment.** `fly.toml` for both apps, the autoscaler app, Upstash, measured limits, secrets, CI deployment if wanted.

Everything through step 3 works locally with `docker compose`. Nothing deploys without the owner's go-ahead.

Before step 1, two things come first: the uncommitted change to a `scratch` image needs committing, and `SVC_MAX_IMG_SOURCE_DIMENSION` needs adding. The second is a bug fix for `main` that doesn't depend on Fly, because the current limits still let one request exhaust memory.

## Testing

- Queue and throttling behaviour is tested against a real Redis, because `redismock` can't exercise consumer groups or Lua. Tests read `SVC_TEST_REDIS_ADDR` and skip if it isn't set. CI runs a Redis service container and sets it, so these tests never skip there **(my call)**.
- Must be covered: a crashed worker's job is reclaimed; a job that always crashes stops after three attempts; duplicate submissions create one job; sync mode returns on finish, on failure and on timeout; a limit is shared across two web instances; different addresses in one prefix share a limit.
- An end-to-end check in `docker compose` with one web and two workers, killing a worker mid-job.
- Load check before launch: a burst that makes the worker count rise and stop at the limit.

## Rough cost

These are estimates. Fly prices changed in October 2026 and sources disagree, so check before relying on them.

- Normal month: one web, one worker and the autoscaler at 256 MB, about $2.50 each in `fra`, plus about $2.50 for idle Upstash polling (1.2M commands), comes to about $10.
- Machines at maximum: two web machines and all 5 workers running all month comes to about $18 before Redis. Redis then depends on traffic and has no ceiling.

## Risks

- **Shared login.** A public service with one Basic Auth password, and no limit on failed logins, can be brute-forced. Set a strong secret. Throttling runs after authentication, so it doesn't help here.
- **Shared-CPU throttling.** Fly's shared CPUs may slow sustained resizing. Step 4 measures throughput.
- **Single Redis.** If Upstash is down, the service can't accept work or serve images.
- **Scale-up lag.** If a burst outruns the 30 to 60 second reaction time, jobs wait longer and sync requests return 202.
- **Uncapped Redis bill.** Pay-as-you-go suits a demo. Real traffic should move to a fixed plan, or object storage for images.
- **Queue eviction.** If Redis ever holds no keys with a TTL while full, the queue itself could be evicted. Workers and metrics would show a sudden drop in queue length.

## Left to the builder

Package layout, the hash field names, Lua details, histogram buckets, and exact log messages.
