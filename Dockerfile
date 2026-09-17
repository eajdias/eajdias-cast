FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod ./
COPY *.go ./
COPY static ./static
RUN go mod tidy \
 && go build -trimpath -ldflags="-s -w" -o /out/cast .

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && addgroup -g 2000 cast \
 && adduser -D -H -u 20000 cast \
 && addgroup cast cast \
 && mkdir -p /data/cinema /data/manga /data/music /data/cache \
 && chown -R cast:cast /data

COPY --from=build /out/cast /usr/local/bin/cast

USER cast
EXPOSE 8080
ENTRYPOINT ["cast"]
