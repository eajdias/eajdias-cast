FROM golang:alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY static ./static
RUN go build -trimpath -ldflags="-s -w" -o /out/cast .

FROM alpine:3.20
RUN apk add --no-cache ffmpeg=6.1.1-r8 ca-certificates=20260413-r0 tzdata=2026b-r0 mesa-va-gallium=24.0.9-r1 \
 && adduser -D -H -u 20000 cast \
 && mkdir -p /data/cinema /data/manga /data/music /data/cache \
 && chown -R cast:cast /data

COPY --from=build /out/cast /usr/local/bin/cast

USER 20000
EXPOSE 8080
ENTRYPOINT ["cast"]
