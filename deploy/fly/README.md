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

Expected: 5 machines. The autoscaler keeps at least one running and stops the rest when the queue is empty. Do not use `fly scale count` for this, because it fights the autoscaler.

## 6. Deploy the autoscaler

```bash
fly deploy -c deploy/fly/autoscaler.toml --ha=false
```

`--ha=false` is required. Two autoscalers would both start and stop machines.

## 7. Checks and rollback

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

To stop autoscaling quickly, stop the autoscaler machine (`fly machine stop -a img-resize-autoscaler <machine-id>`). Workers then stay in whatever state they are in.
