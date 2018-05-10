# Installing a custom Helm-based app

While the [Operator Lifecycle Manager][olm-repo] can only manage Operators, not all applications require developers to write a custom Operator.
The [Helm App Operator Kit][helm-sdk] makes it possible to leverage a pre-existing Helm chart to deploy Kubernetes resources as a unified application.

```sh
git clone https://github.com/coreos/helm-app-operator-kit
cd helm-app-operator-kit
```

To create a new application, the Helm App Operator Kit provides both a shell script and manual instructions.

## Prerequisites

- Kubernetes 1.9+ cluster
- `docker` client
- `kubectl` client

## Scripted

To create and register the sample application type in your Kubernetes cluster, run the `generate-and-install-example.sh` script, and follow its instructions:

```
./generate-and-install-example.sh
Enter your namespace for the example application: mytestnamespace
Enter the Docker repository in which to place the built operator (example: quay.io/mynamespace/example-sao): quay.io/myquayuser/myrepo
...
```

## Manual

To manually create and register the sample application type in your Kubernetes cluster:

1) Replace all instances of `YOUR_NAMESPACE_HERE` in the `yaml` files found in this directory with the Kubernetes namespace in which you wish to register the new application type:

```sh
sed -i.orig 's/YOUR_NAMESPACE_HERE/mynamespace/g' *.yaml
```

2) Replace all instances of `YOUR_REPO_IMAGE_HERE` in the `yaml` files found in this directory with the container repository in which you wish to store the built operator:

```sh
sed -i.orig 's#YOUR_REPO_IMAGE_HERE#quay.io/mynamespace/mysampleapp#g' *.yaml
```

3) Build and push an image of the operator that contains the example Helm chart:

```sh
docker build -t quay.io/mynamespace/mysampleapp:latest .
docker push quay.io/mynamespace/mysampleapp:latest
```

4) Create the Kubernetes resource (CustomResourceDefinition) for new instances of your application and install the operator (ClusterServiceVersion-v1) into your namespace:

```sh
kubectl create -f example-app.crd.yaml
kubectl create -f example-app-operator.v0.0.1.clusterserviceversion.yaml
```

## Creating an instance of the example application

After the CustomResourceDefinition and ClusterServiceVersion-v1 resources for the new application have been applied, new instances of that app can be created:

```yaml
cat <<EOF | kubectl create -f -
apiVersion: example.com/v1alpha1
kind: ExampleApp
metadata:
  name: sample-example
  namespace: YOUR_NAMESPACE_HERE
spec:
  size: 2
EOF
```

Note that the contents of the `spec` block is the contents used in the chart in `example-chart/values.yaml`.


[helm-sdk]: https://github.com/coreos/helm-app-operator-kit
[olm-repo]: https://github.com/operator-framework/operator-lifecycle-manager
