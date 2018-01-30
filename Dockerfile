FROM quay.io/coreos/sao:latest
ADD example-templates /templates
ADD example-config.yaml config.yaml
CMD ["start", "--config", "/config.yaml"]
