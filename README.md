# Field Materials Backend Interview HWA

Several improvements were made to enhance code readability and production readiness:
- The codebase was reorganized into separate files and packages, following Go best practices.
- A new `/health` endpoint was added, a common requirement for ECS, Kubernetes, and similar environments.
- Basic authentication was enforced on all endpoints (except `/health`).
- A Makefile was introduced to automate common development tasks such as running tests, refreshing dependencies, and running the service in a Docker container.
- A Dockerfile was created to easily build and test a production-ready image of the service.
- Several environment variables were introduced to simplify service configuration. Refer to `internal/settings/settings.go` for a comprehensive and (hopefully) self-explanatory list.

## Build & Run Server

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
