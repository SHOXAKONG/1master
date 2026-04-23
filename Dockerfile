# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY protocol/ ./protocol/
COPY server/ ./server/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o mytunnel-server ./server

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

RUN adduser -D -u 1000 tunnel
USER tunnel

COPY --from=builder /app/mytunnel-server /usr/local/bin/mytunnel-server

EXPOSE 8080 9000

ENV TUNNEL_DOMAIN=localhost
ENV TUNNEL_PORT=9000
ENV HTTP_PORT=8080

ENTRYPOINT ["mytunnel-server"]