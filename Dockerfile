FROM golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473 AS test
WORKDIR /src
ENV GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=readonly
COPY . .
RUN test -z "$(gofmt -l cmd internal)" && go vet ./... && CGO_ENABLED=1 go test -race -count=1 ./...

FROM test AS build
ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
    -o /out/gateway ./cmd/gateway

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/gateway /gateway
USER 65532:65532
ENTRYPOINT ["/gateway"]
