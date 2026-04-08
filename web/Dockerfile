FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o mailreaper ./cmd/mailreaper/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/mailreaper /usr/local/bin/mailreaper
COPY --from=builder /build/ui /app/ui
WORKDIR /app
EXPOSE 8025
ENTRYPOINT ["mailreaper"]
CMD ["--config", "/app/config.yaml", "--db", "/app/data/mailreaper.db"]
