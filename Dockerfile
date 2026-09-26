# One image for both halves of Tropis:
#   tropis            the analyser (CronJob, `tropis analyze`)
#   tropis-collector  the host collector (DaemonSet), with smartctl
#
# Built with -trimpath and no cgo, so the binaries are reproducible from the
# same source and toolchain.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/00Webbo/tropis/pkg/reason.AgentVersion=${VERSION}" \
      -o /out/tropis ./cmd/tropis && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
      -o /out/tropis-collector ./cmd/tropis-collector

FROM alpine:3.20
RUN apk add --no-cache smartmontools
COPY --from=build /out/tropis /out/tropis-collector /usr/local/bin/
# The analyser runs as this user. The collector overrides it: reading SMART
# requires root and device access, which the chart grants only to it.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/tropis"]
