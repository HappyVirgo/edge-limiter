# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/edge-limiter ./cmd/edge-limiter

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/edge-limiter /edge-limiter
COPY configs/local.yaml /etc/edge-limiter/config.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/edge-limiter"]
CMD ["-config", "/etc/edge-limiter/config.yaml"]
