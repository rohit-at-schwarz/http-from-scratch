FROM golang:1.27-alpine AS builder
WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o server ./cmd/httpserver

FROM alpine:3.20
RUN apk --no-cache add ca-certificates
WORKDIR /root/

COPY --from=builder /app/server .
EXPOSE 8080

ENTRYPOINT ["./server"]