ARG VERSION=dev

FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hooversion ./cmd/hooversion \
 && go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/versionhoo-app ./cmd/versionhoo-app

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk add --no-cache git openssh-client ca-certificates

COPY --from=build /out/hooversion /usr/local/bin/hooversion
COPY --from=build /out/versionhoo-app /usr/local/bin/versionhoo-app

ENV VERSIONHOO_HOST=0.0.0.0 \
    VERSIONHOO_PORT=3000

EXPOSE 3000

CMD ["versionhoo-app"]
