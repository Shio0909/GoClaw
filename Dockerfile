FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /goclaw ./cmd/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /goclaw /usr/local/bin/goclaw

RUN mkdir -p /data/memory_data
WORKDIR /data

EXPOSE 8080

ENTRYPOINT ["goclaw"]
CMD ["serve"]
