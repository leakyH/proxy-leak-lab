FROM golang:1.26.4-alpine3.23 AS build
WORKDIR /src
COPY go.mod ./
COPY server ./server
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/leak-server ./server

FROM alpine:3.23
RUN apk add --no-cache libmaxminddb
RUN addgroup -S app && adduser -S -G app app && mkdir -p /data && chown app:app /data
COPY --from=build /out/leak-server /usr/local/bin/leak-server
USER app
EXPOSE 8080/tcp 9001/tcp 9002/udp 3478/udp
ENTRYPOINT ["/usr/local/bin/leak-server"]
