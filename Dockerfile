FROM golang:1.12 as builder
WORKDIR /go/src/github.com/gerald1248/verify-deployment
ADD . ./
ENV CGO_ENABLED 0
ENV GOOS linux
ENV GO111MODULE on
RUN \
  go mod download && \
  go get && \
  go vet && \
  go test -v && \
  go build

FROM ubuntu:18.10
WORKDIR /app/
RUN groupadd app && useradd -g app app
COPY --from=builder /go/src/github.com/gerald1248/verify-deployment /usr/local/bin/verify-deployment
USER app
CMD ["while true; do sleep 60; done"]]
