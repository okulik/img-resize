# Image resizer

An HTTP-based service with a simple API for resizing images. It supports blocking and non-blocking modes of downloading and resizing images. Also, it supports two types of resized images caches - a simple in-memory cache, and a Redis-based one.

## Build & Run Service

### Running it locally
```bash
make run
```

### Inside the Docker container
```bash
make docker-run
```

## Run a sample request against the server
```bash
curl -u admin:admin -H "Content-Type: application/json" \
  -d @req.json http://localhost:4000/v1/resize?async=true
```

Now, in your browser, you can check one of the resized images using the returned hash from the call above. For example, try entering `http://localhost:4000/v1/image/3731df6b15afc23322056bf1e234b86b8cdf32f0999eec5ccd3fd6148c8065fd`. Make sure to enter `admin` / `admin` as the username and password in the basic auth form.

Alternatively, run the following from the command line to see the resized image:
```bash
curl -u admin:admin \
  http://localhost:4000/v1/image/3731df6b15afc23322056bf1e234b86b8cdf32f0999eec5ccd3fd6148c8065fd \
  --output a.jpg | open a.jpg
```

## A wish list

There's a number of important features that are currently missing in the current implementation. For instance, there's no any external error tracking nor telemetry. Here's a wish-list of features that would make the service more useful, maintainable, and production-ready:
- **Error Tracking Service Integration**: Incorporate an external error tracking service like Sentry or Honeybadger for reporting service errors.
- **Telemetry Implementation**: Integrate tracing using tools like New Relic, Datadog, Jaeger, or Tempo to trace requests. Also incorporate metrics and monitoring utilizing New Relic, Datadog, Prometheus, VictoriaMetrics, or Grafana and add logging, utilizing a structured logging library like [zap](https://github.com/uber-go/zap) or slog.
- **Containerized Deployment**: Run the service in a containerized environment (AWS ECS, k8s) and behind a load balancer, for scaling purpose. Deploy the container image to a private registry like AWS ECR or GCP Artifact Registry rather than GitHub Container Registry.
- **HTTP Server Optimization**: Optionally consider using alternative HTTP server implementations like [fasthttp](https://github.com/valyala/fasthttp) to handle a high number of concurrent connections.
