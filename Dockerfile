FROM golang:1.26.6-alpine3.22 AS builder
WORKDIR /app

COPY . .

ENV CGO_ENABLED=0
RUN go build -ldflags "-w -s" -o build/sni-router cmd/main.go

FROM alpine:3.22.2
ENV TZ=Asia/Tehran
WORKDIR /app

COPY --from=builder /app/build/sni-router /app/

RUN chmod +x /app/sni-router

CMD [ "./sni-router" ]
