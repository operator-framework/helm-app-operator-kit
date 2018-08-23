FROM golang:1.10 as builder
ENV API_VERSION 'test.com/v1'
WORKDIR /go/src/github.com/operator-framework/helm-app-operator-kit/helm-app-operator
COPY helm-app-operator .
RUN dep ensure
RUN go test ./...
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/operator cmd/helm-app-operator/main.go
RUN chmod +x bin/operator
