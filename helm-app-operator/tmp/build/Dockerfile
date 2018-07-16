FROM alpine:3.6

RUN adduser -D helm-app-operator
USER helm-app-operator

ADD tmp/_output/bin/helm-app-operator /usr/local/bin/operator
