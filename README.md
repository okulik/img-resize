# Image resizer

An HTTP-based service with a simple API for resizing images. Resizing is asynchronous by default: a request queues a job in Redis and returns at once, and `?sync=true` waits for the resized image instead. Also, it supports two types of resized images caches - a simple in-memory cache, and a Redis-based one.

## Build & Run Service

### Running it locally
```bash
make run
```

### Roles
The service runs in one of two roles, chosen with `SVC_ROLE`:
- `web` (default) serves the API. It needs `AUTH_USERNAME` and `AUTH_PASSWORD`.
- `worker` takes jobs off the queue and resizes the images. It serves only `/health`, on `HTTP_SERVER_PORT`.

Both need the same Redis. Run a worker next to `make run` with:
```bash
make run-worker
```

On `SIGTERM` a worker stops reading jobs, finishes the one it is running and exits. A job whose worker died is taken over by another worker after `SVC_JOB_CLAIM_IDLE`.

### Inside the Docker container
```bash
make docker-run
```

## Run a sample request against the server
```bash
curl -u admin:admin -H "Content-Type: application/json" \
  -d @req.json http://localhost:4000/v1/resize
```

The response lists a hash for every image and whether its job is enqueued. With `?sync=true` (one URL only) the request waits up to `SVC_SYNC_WAIT_TIMEOUT` and returns the resized image, or `202` if it isn't ready by then.

`GET /v1/image/{hash}` returns the image with `200` once it is ready. Until then it returns `202` while the job is queued or processing, `422` if it failed (`fetch failed`, `invalid image` or `image too large`), and `404` if the hash is unknown.

Now, in your browser, you can check one of the resized images using the returned hash from the call above. For example, try entering `http://localhost:4000/v1/image/3731df6b15afc23322056bf1e234b86b8cdf32f0999eec5ccd3fd6148c8065fd`. Make sure to enter `admin` / `admin` as the username and password in the basic auth form.

Alternatively, run the following from the command line to see the resized image:
```bash
curl -u admin:admin \
  http://localhost:4000/v1/image/3731df6b15afc23322056bf1e234b86b8cdf32f0999eec5ccd3fd6148c8065fd \
  --output a.jpg | open a.jpg
```

## Settings

Besides `SVC_ROLE`, the queue and workers use these `SVC_*` settings:

| Setting | Default | Meaning |
| --- | --- | --- |
| `SVC_WORKER_CONCURRENCY` | `1` | Jobs a worker runs at once |
| `SVC_JOB_CLAIM_IDLE` | `60s` | How long a job may sit unfinished before another worker takes it |
| `SVC_JOB_RECLAIM_INTERVAL` | `15s` | How often a worker looks for such jobs |
| `SVC_JOB_MAX_ATTEMPTS` | `3` | Attempts before a job fails |
| `SVC_JOB_STATE_TTL` | `1h` | How long job state is kept |
| `SVC_FAILED_JOB_TTL` | `5m` | How long a failed job is kept |
| `SVC_MAX_QUEUE_DEPTH` | `10000` | Queued jobs after which new ones are refused |
| `SVC_SYNC_WAIT_TIMEOUT` | `15s` | How long `?sync=true` waits |
| `SVC_ALLOW_INTERNAL_URLS` | `false` | Allow image URLs on private addresses |
| `SVC_METRICS_PORT` | `9091` | Port of the Prometheus `/metrics` endpoint, served by both roles and not on the public port |

## A wish list

There's a number of important features that are currently missing in the current implementation. For instance, there's no any external error tracking nor telemetry. Here's a wish-list of features that would make the service more useful, maintainable, and production-ready:
- **Error Tracking Service Integration**: Incorporate an external error tracking service like Sentry or Honeybadger for reporting service errors.
- **Telemetry Implementation**: Integrate tracing using tools like New Relic, Datadog, Jaeger, or Tempo to trace requests. Also incorporate metrics and monitoring utilizing New Relic, Datadog, Prometheus, VictoriaMetrics, or Grafana and add logging, utilizing a structured logging library like [zap](https://github.com/uber-go/zap) or slog.
- **Containerized Deployment**: Run the service in a containerized environment (AWS ECS, k8s) and behind a load balancer, for scaling purpose. Deploy the container image to a private registry like AWS ECR or GCP Artifact Registry rather than GitHub Container Registry.
- **HTTP Server Optimization**: Optionally consider using alternative HTTP server implementations like [fasthttp](https://github.com/valyala/fasthttp) to handle a high number of concurrent connections.
