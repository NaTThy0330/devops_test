# Builder stage: compile the API binary with Go.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Copy module files first so dependency downloads can be cached.
COPY services/api/go.mod services/api/go.sum ./services/api/
COPY third_party/prometheus/client_golang ./third_party/prometheus/client_golang
WORKDIR /src/services/api
RUN go mod download

# Copy the remaining source after dependency resolution.
COPY services/api/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/api .

# Runtime stage: minimal non-root image with only the compiled binary.
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /out/api /api

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/api"]
