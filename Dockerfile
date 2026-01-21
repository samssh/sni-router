FROM docker.arvancloud.ir/golang:1.24.9-alpine3.22 AS builder
WORKDIR /app

RUN apk --no-cache --update add \
  build-base \
  gcc

COPY . .

ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-D_LARGEFILE64_SOURCE"
RUN go build -ldflags "-w -s" -o build/sni-router cmd/main.go

FROM alpine:3.22.2
ENV TZ=Asia/Tehran
WORKDIR /app

COPY --from=builder /app/build/sni-router /app/

RUN chmod +x /app/sni-router

CMD [ "./sni-router" ]
