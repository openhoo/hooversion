ARG VERSION=dev

FROM golang:1.25-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hooversion ./cmd/hooversion \
 && go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/versionhoo-app ./cmd/versionhoo-app

FROM alpine:3.22

RUN apk add --no-cache git openssh-client ca-certificates

COPY --from=build /out/hooversion /usr/local/bin/hooversion
COPY --from=build /out/versionhoo-app /usr/local/bin/versionhoo-app

ENV VERSIONHOO_HOST=0.0.0.0 \
    VERSIONHOO_PORT=3000

EXPOSE 3000

CMD ["versionhoo-app"]
