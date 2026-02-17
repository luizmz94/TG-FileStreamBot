FROM golang:1.24-alpine AS builder
RUN apk update && apk upgrade --available && sync
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o /app/fsb -ldflags="-w -s" ./cmd/fsb

FROM scratch
COPY --from=builder /app/fsb /app/fsb
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
EXPOSE ${PORT}
EXPOSE ${STATUS_PORT}
ENTRYPOINT ["/app/fsb", "run"]
