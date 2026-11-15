# Development rules

- This is an independent Go gateway, not a fork of New API.
- Go is pinned to 1.26.10; use the digest-pinned Dockerfile. Do not follow latest.
- Current scope is a publishing bootstrap, not a working model proxy. Keep this explicit.
- Do not implement automatic model retries, continuations, or fallback requests.
- Never commit credentials, production data, local tool authentication, or upstream keys.
- Run `go test -race ./...`, `go vet ./...`, and the container smoke check for relevant changes.
- PRs and ordinary main commits build only. Tags and explicit manual publication may push images.
- Publication must not deploy to a production server. Record the image digest for deployment.
- Health/liveness does not imply readiness to accept model requests.
