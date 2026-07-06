# Build stage
FROM golang:1.21-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o collections-service .

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata curl

WORKDIR /root/

COPY --from=builder /app/collections-service .

EXPOSE 8306

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8306/health || exit 1

CMD ["./collections-service"]

