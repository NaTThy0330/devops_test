# Builder stage: compile the worker binary with Go.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Copy module files first so dependency downloads can be cached.
COPY services/worker/go.mod services/worker/go.sum ./services/worker/
COPY third_party/prometheus/client_golang ./third_party/prometheus/client_golang
WORKDIR /src/services/worker
RUN go mod download

# Copy the remaining source after dependency resolution.
COPY services/worker/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/worker .

# Runtime stage: minimal non-root image with only the compiled binary.
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /out/worker /worker

USER nonroot:nonroot
ENTRYPOINT ["/worker"]
