# Fly.io deployment

Three apps in the `personal` org, region `fra`:

| App | Config | Role |
|---|---|---|
| `img-resize-web` | `web.toml` | Accepts requests, serves results |
| `img-resize-worker` | `worker.toml` | Fetches and resizes images, exactly 5 machines |
| `img-resize-autoscaler` | `autoscaler.toml` | Starts and stops worker machines, 1 to 5 running |

Run every command from the repo root. Never put a real secret in a file in this repo.

## 1. Prerequisites

- `flyctl` installed and logged in (`fly auth login`).
- The Upstash database `img-resize` exists. Check it with `fly redis status img-resize`.
- It must be on the fixed 250 MB plan, not pay-as-you-go. Eviction stays on and auto-upgrade stays off, so a full database drops old images instead of growing the bill.

`fly redis update --help` (flyctl 0.4.123) has no `--plan`, eviction or auto-upgrade flags. It only takes `--enable-prodpack`, `--disable-prodpack`, `--region`, `--replica-regions` and `--org`. Change the plan in the Upstash console, opened with:

```bash
fly redis dashboard personal
```

Pick "Fixed 250MB" (`fly redis plans` lists it at $10/mo), leave eviction enabled and auto-upgrade disabled, then confirm:

```bash
fly redis status img-resize
```

Expected: `Plan` is `Fixed 250MB`, `Eviction` is `Enabled`, `Auto-Upgrade` is `Disabled`.

## 2. Create the three apps

```bash
fly apps create img-resize-web --org personal
fly apps create img-resize-worker --org personal
fly apps create img-resize-autoscaler --org personal
```

## 3. Secrets

Web gets the login and Redis secrets. Generate a strong password and keep it in your password manager.

```bash
fly secrets set -a img-resize-web --stage \
  AUTH_USERNAME="<username>" \
  AUTH_PASSWORD="$(openssl rand -base64 32)" \
  SVC_REDIS_HOST="fly-img-resize.upstash.io" \
  SVC_REDIS_PASSWORD="<password from 'fly redis status img-resize'>"
```

Workers get the Redis secrets only, with no `AUTH_*`:

```bash
fly secrets set -a img-resize-worker --stage \
  SVC_REDIS_HOST="fly-img-resize.upstash.io" \
  SVC_REDIS_PASSWORD="<password from 'fly redis status img-resize'>"
```

The Redis port is the default 6379, so `SVC_REDIS_PORT` is not set.

The autoscaler needs a deploy token for the worker app and a read-only token for the org's Prometheus data:

```bash
fly secrets set -a img-resize-autoscaler --stage \
  FAS_API_TOKEN="$(fly tokens create deploy -a img-resize-worker)" \
  FAS_PROMETHEUS_TOKEN="$(fly tokens create readonly personal)"
```

`--stage` stores the secrets without restarting anything. They apply at the next deploy.

Do not set `SVC_ALLOW_INTERNAL_URLS` on the worker. Internal addresses must stay blocked in production.

## 4. Deploy web and the first worker

```bash
fly deploy -c deploy/fly/web.toml --ha=false
fly deploy -c deploy/fly/worker.toml --ha=false
```

The web app needs one machine running. `--ha=false` gives it exactly one. Then create a second web machine for availability:

```bash
fly machine clone -a img-resize-web <machine-id>
```

Find the ID with `fly machine list -a img-resize-web`. With `min_machines_running = 1`, one web machine always runs and the other is stopped and started on demand.

## 5. Create exactly 5 worker machines

The first deploy created one. Clone it four times:

```bash
fly machine list -a img-resize-worker
fly machine clone -a img-resize-worker <machine-id>   # run 4 times
fly machine list -a img-resize-worker
```

Expected: 5 machines. The autoscaler's formula, `max(1, min(5, ceil(backlog / 30)))`, sets how many run: the `max(1, ...)` floor keeps one worker running when the queue is empty, and the rest are stopped. Do not use `fly scale count` for this, because it fights the autoscaler.

## 6. Launch checklist

Finish this before section 7 and before any real traffic.

1. Move Upstash to the fixed plan (section 1), then run `fly redis reset img-resize`, as the password was exposed in a transcript. The reset changes the password, so set the new `SVC_REDIS_PASSWORD` on `img-resize-web` and `img-resize-worker` again (section 3).
2. Send a request with a forged `Fly-Client-IP` header and confirm the limiter keys on the real address.
3. Send a real 12 MP baseline file, a progressive file at `SVC_MAX_IMG_PROGRESSIVE_PIXELS`, a CMYK file and a tall 1464x8192 file, and watch worker memory. The CMYK file must fail with `invalid image`.
4. Record the p99 of `resize_job_duration_seconds`. Set `SVC_JOB_CLAIM_IDLE` to at least 2 x p99 and the worker `kill_timeout` to at least 5s + p99.
5. Confirm one scrape in Grafana. Watch the first sync resize and the worker logs for go-redis problems against Upstash, including the `NOGROUP` text.

## 7. Deploy the autoscaler

```bash
fly deploy -c deploy/fly/autoscaler.toml --ha=false
```

`--ha=false` is required. Two autoscalers would both start and stop machines.

## 8. Checks and rollback

```bash
curl -i https://img-resize-web.fly.dev/health
fly status -a img-resize-web
fly status -a img-resize-worker
fly status -a img-resize-autoscaler
fly logs -a img-resize-autoscaler
```

Expected: `/health` returns 200, and the worker app shows 5 machines with one started and four stopped when idle.

The `scratch` image has no shell, so `fly ssh console` connects but gives no prompt. Use `fly logs` and the metrics instead.

Roll back by redeploying an earlier release:

```bash
fly releases -a img-resize-web
fly deploy -c deploy/fly/web.toml --image <earlier image ref from the releases list>
```

A later `fly deploy` of the worker app updates every worker machine, stopped ones included. Fly's docs don't say whether a stopped machine stays stopped afterwards, so check `fly status -a img-resize-worker` after a deploy. The autoscaler re-applies the formula on its next run and starts or stops machines to match.

To stop autoscaling quickly, stop the autoscaler machine (`fly machine stop -a img-resize-autoscaler <machine-id>`). Workers then stay in whatever state they are in.
