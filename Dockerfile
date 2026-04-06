FROM golang:1.25.8 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sidecar-agent ./cmd/sidecar-agent

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/sidecar-agent /usr/local/bin/sidecar-agent

EXPOSE 15010 15011 15353

ENTRYPOINT ["/usr/local/bin/sidecar-agent"]
