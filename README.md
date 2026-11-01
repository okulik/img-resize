# Image resizer

An HTTP service with a simple API for resizing JPEG images. A web frontend accepts requests and puts one job per image on a queue in Redis. A separate fleet of workers fetches and resizes the images, and stores the results back in Redis for the web frontend to serve.

Resizing is asynchronous by default: a request returns at once with an ID for each image. With `?sync=true` it waits and returns the resized image instead.

- Jobs are at-least-once. If a worker dies, another one takes over its job.
- Requests and served bytes are throttled per network prefix rather than per address, as one IPv6 client usually holds many addresses.
- Image URLs on private and internal addresses are blocked.
- Both roles export Prometheus metrics, and the queue backlog drives autoscaling on Fly.io.

## How it works

```
client ──> web ──> Redis Stream ──> worker ──> source image
            ^                         │
            └──── resized image ──────┘
                  stored in Redis
```

The service runs in one of two roles, chosen with `SVC_ROLE`:

- `web` (default) serves the API. It needs `AUTH_USERNAME` and `AUTH_PASSWORD`.
- `worker` takes jobs off the queue and resizes the images. It serves only `/health`, on `HTTP_SERVER_PORT`.

Both need the same Redis, which holds the queue, job state, rate limits and resized images.

On `SIGTERM` a worker stops reading jobs, finishes the one it is running and exits. A job whose worker died is taken over by another worker after `SVC_JOB_CLAIM_IDLE`, up to `SVC_JOB_MAX_ATTEMPTS` attempts.

## Run it

### With Docker Compose

This starts web on port 4000, one worker and Redis:

```bash
docker compose up -d --build
```

### From source

You need Go and a Redis. In development the service reads its login and Redis password from `.env.dev`. Start Redis with:

```bash
docker compose up -d redis
```

Then run web and a worker in two terminals:

```bash
make run
make run-worker
```

## Use it

Send the sample request from `req.json`. Its third URL doesn't exist, to show a failed job:

```bash
curl -u admin:admin -H "Content-Type: application/json" \
  -d @req.json http://localhost:4000/v1/resize
```

```json
[
  {"result":"enqueued","id":"<image id>","cached":false},
  {"result":"enqueued","id":"<image id>","cached":false},
  {"result":"enqueued","id":"<image id>","cached":false}
]
```

Fetch a resized image with its ID, once the job is done:

```bash
curl -u admin:admin http://localhost:4000/v1/image/<image id> --output a.jpg
```

Or wait for the image in one request. Sync mode takes exactly one URL:

```bash
curl -u admin:admin -H "Content-Type: application/json" \
  -d '{"urls":["https://raw.githubusercontent.com/golang/go/master/src/image/testdata/video-001.jpeg"],"width":200,"height":0}' \
  "http://localhost:4000/v1/resize?sync=true" --output a.jpg
```

## API

Everything under `/v1` uses HTTP Basic authentication. `GET /health` doesn't.

### `POST /v1/resize`

| Field | Meaning |
| --- | --- |
| `urls` | Up to 100 image URLs, in a body of at most 8 KB |
| `width`, `height` | Output size, each up to `SVC_MAX_IMG_DIMENSION`. Set one to `0` to keep the aspect ratio |

The response lists a result for every URL. An image that is already cached comes back with `"result":"success"` and `"cached":true`, and no job is queued. With `?sync=true` the request waits up to `SVC_SYNC_WAIT_TIMEOUT`.

### `GET /v1/image/{id}`

Returns the image as `image/jpeg`, or else the state of its job.

### Status codes

| Status | `POST /v1/resize` | `?sync=true` | `GET /v1/image/{id}` |
| --- | --- | --- | --- |
| `200` | Jobs accepted | The image | The image |
| `202` | | Not ready in time, keep polling by ID | Queued or processing |
| `400` | Invalid request | Invalid request, or not exactly one URL | |
| `401` | Bad login | Bad login | Bad login |
| `404` | | | Unknown ID, or the image expired |
| `422` | | Job failed | Job failed |
| `429` | Throttled, see `Retry-After` | Throttled | Throttled |
| `503` | Queue full or Redis unavailable | Queue full or Redis unavailable | Redis unavailable |

A failed job reports one of `fetch failed`, `invalid image` or `image too large` in `error`. Network errors and temporary statuses such as `429` are retried. Every other failure is final.

## Image limits

Input and output are JPEG only. CMYK and RGB JPEGs are rejected as `invalid image`, to keep memory use predictable. Progressive JPEGs have their own pixel limit, as decoding them takes more memory.

Pixel count alone doesn't bound memory: the resize buffer grows with source height × output width, so `SVC_MAX_IMG_SOURCE_DIMENSION` limits tall images too. The defaults suit a large machine, and the limits for small ones are in [`deploy/fly`](deploy/fly).

## Settings

Web and workers read these from the environment.

### Images and cache

| Setting | Default | Meaning |
| --- | --- | --- |
| `SVC_MAX_IMG_SIZE` | `15728640` | Largest source file, in bytes |
| `SVC_MAX_IMG_DIMENSION` | `4096` | Largest output width or height |
| `SVC_MAX_IMG_PIXELS` | `50000000` | Largest source image, in pixels |
| `SVC_MAX_IMG_PROGRESSIVE_PIXELS` | `50000000` | Largest progressive source image, in pixels |
| `SVC_MAX_IMG_SOURCE_DIMENSION` | `8192` | Longest source side |
| `SVC_IMG_CACHE_TTL` | `1h` | How long resized images are kept |
| `SVC_ALLOW_INTERNAL_URLS` | `false` | Allow image URLs on private addresses |

### Redis

| Setting | Default | Meaning |
| --- | --- | --- |
| `SVC_REDIS_HOST` | `0.0.0.0` | Redis host |
| `SVC_REDIS_PORT` | `6379` | Redis port |
| `SVC_REDIS_PASSWORD` | | Redis password |

### Queue and workers

| Setting | Default | Meaning |
| --- | --- | --- |
| `SVC_ROLE` | `web` | `web` or `worker` |
| `SVC_WORKER_CONCURRENCY` | `1` | Jobs a worker runs at once |
| `SVC_JOB_CLAIM_IDLE` | `60s` | How long a job may sit unfinished before another worker takes it |
| `SVC_JOB_RECLAIM_INTERVAL` | `15s` | How often a worker looks for such jobs |
| `SVC_JOB_MAX_ATTEMPTS` | `3` | Attempts before a job fails |
| `SVC_JOB_STATE_TTL` | `1h` | How long job state is kept |
| `SVC_FAILED_JOB_TTL` | `5m` | How long a failed job is kept |
| `SVC_MAX_QUEUE_DEPTH` | `10000` | Queued jobs after which new ones are refused |
| `SVC_SYNC_WAIT_TIMEOUT` | `15s` | How long `?sync=true` waits |

### Throttling

Limits use a fixed one-minute window, shared by all web machines through Redis.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SVC_RATE_LIMIT_PER_MINUTE` | `120` | Images a network may submit per minute |
| `SVC_RATE_LIMIT_BYTES_PER_MINUTE` | `104857600` | Bytes of images a network may receive per minute |
| `SVC_RATE_LIMIT_IPV4_PREFIX` | `32` | IPv4 prefix that counts as one network |
| `SVC_RATE_LIMIT_IPV6_PREFIX` | `64` | IPv6 prefix that counts as one network |
| `SVC_TRUST_FLY_CLIENT_IP` | `false` | Take the client address from `Fly-Client-IP`. Enable it only behind Fly's proxy |

### Login and HTTP

| Setting | Default | Meaning |
| --- | --- | --- |
| `AUTH_USERNAME`, `AUTH_PASSWORD` | | Basic auth login, required for the web role |
| `AUTH_REALM` | `localhost` | Basic auth realm |
| `HTTP_SERVER_PORT` | `4000` | HTTP port |
| `HTTP_SERVER_GRACEFUL_SHUTDOWN_TIMEOUT` | `10s` | How long a shutdown waits for running work to finish |
| `HTTP_SERVER_IDLE_TIMEOUT` | `60s` | Idle connection timeout |
| `HTTP_SERVER_READ_TIMEOUT` | `10s` | Request read timeout |
| `HTTP_SERVER_WRITE_TIMEOUT` | `20s` | Response write timeout |
| `HTTP_CLIENT_READ_TIMEOUT` | `10s` | Timeout for fetching a source image |
| `HTTP_CLIENT_USER_AGENT` | `img-resize` | User agent for fetching source images |

## Metrics

Both roles serve Prometheus metrics at `/metrics` on `SVC_METRICS_PORT` (default `9091`), which is separate from the public port.

| Metric | Meaning |
| --- | --- |
| `resize_queue_depth` | Jobs on the queue, sampled by web |
| `resize_job_duration_seconds` | How long a job takes |
| `resize_jobs_total{result}` | Jobs by `success`, `failure`, `retried` or `abandoned` |
| `resize_throttled_total{limit}` | Refused requests by `requests` or `bytes` |
| `resize_rejected_total{reason}` | Other refused requests, such as `queue_full` |
| `resize_bandwidth_bytes_total{direction}` | Image bytes `served` or `stored` |

Each role exports all of them, so filter by app when you query.

## Development

```bash
make test           # Unit tests. Tests that need Redis are skipped
make test-redis     # All tests, against a throwaway Redis container
make test-coverage  # The same, with coverage
make lint           # Runs golangci-lint
```

`make help` lists the other targets. Pushes to `main` that pass the tests publish a Docker image to the GitHub Container Registry. The image is built `FROM scratch`, so it has no shell.

## Deployment

[`deploy/fly`](deploy/fly) holds the configuration and step-by-step guide for Fly.io: three apps in `fra` with Upstash Redis, 256 MB machines, and an autoscaler that runs 1 to 5 workers based on queue backlog. There's no CI deployment. `make fly-up` builds the whole demo from nothing, `make fly-stop` and `make fly-start` pause it, and `make fly-down` deletes it.

## A wish list

Here are some features that would make the service more useful and production-ready:

- **Error tracking**: Report service errors to something like Sentry or Honeybadger.
- **Tracing and structured logs**: Trace requests with Jaeger or Tempo, and move to a structured logger like `slog`.
- **Login throttling**: Failed logins aren't limited, and a real service would want per-client API keys rather than one shared password.
- **A leaner resizer**: [nfnt/resize](https://github.com/nfnt/resize) is archived, and its buffers are what limit image sizes on small machines.
- **More formats**: Accept PNG and WebP, and return more than JPEG.
- **Object storage**: Keep resized images out of Redis, which would suit heavier traffic.
