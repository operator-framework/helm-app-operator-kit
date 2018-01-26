FROM quay.io/coreos/sao:ALM-411
ADD example-templates /templates
ADD example-config.yaml config.yaml
CMD ["start", "--config", "/config.yaml"]
