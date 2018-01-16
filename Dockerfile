FROM golang:alpine AS build-env

RUN apk update && \
    apk add git

WORKDIR        /go/src/github.com/wpengine/lostromos
COPY lostromos /go/src/github.com/wpengine/lostromos

RUN go get -u github.com/golang/dep/...
RUN dep ensure
RUN CGO_ENABLED=0 go install github.com/wpengine/lostromos

FROM alpine:latest
COPY --from=build-env /go/bin/lostromos /lostromos

RUN adduser -D lostromos
USER lostromos

ENTRYPOINT ["/lostromos"]
