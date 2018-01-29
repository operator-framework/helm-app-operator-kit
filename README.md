# Stateless App Operator

This repository serves as a template for easily creating managed stateless applications that run either Kubernetes Deployments or Helm charts.

## Creating and registering the sample application type

To create and register the sample application type in your Tectonic cluster, run the `generate-and-install-example.sh` script, and follow its instructions.

## Creating and registering the sample application type (manually)

To create and register the sample application type in your Tectonic cluster:

1) Replace all instances of `YOUR_NAMESPACE_HERE` in the `yaml` files found in this directory with the Kubernetes namespace in which you wish to register the new application type:

```sh
sed -i.orig 's/YOUR_NAMESPACE_HERE/mynamespace/g' *.yaml
```

2) Replace all instances of `YOUR_REPO_IMAGE_HERE` in the `yaml` files found in this directory with the container repository in which you wish to store the built operator:

```sh
sed -i.orig 's/YOUR_REPO_IMAGE_HERE/quay.io\/mynamespace\/mysampleapp/g' *.yaml
```

3) Build and push the stateless app operator:

```sh
docker build -t quay.io/mynamespace/mysampleapp:latest .
docker push quay.io/mynamespace/mysampleapp:latest
```

4) Create the CRD and CSV in-cluster:

```sh
kubectl create -f example-app.crd.yaml
kubectl create -f example-app-operator.v0.0.1.clusterserviceversion.yaml
```

5) Wait a minute or two for the application kind to register

## Creating an instance of the example application

To create an instance of the example application, create the following custom resource:

```yaml
apiVersion: example-apps.example.com/v1alpha1
kind: ExampleApp
metadata:
  name: sample-example
  namespace: YOUR_NAMESPACE_HERE
spec:
  size: 2
```

Note that the `size` is pulled into the nginx template in `example-templates/deployment.yaml.tmpl`.