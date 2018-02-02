FROM quay.io/coreos/sao:latest
ADD example-chart /chart
ADD example-config.yaml config.yaml
CMD ["start", "--config", "/config.yaml"]
