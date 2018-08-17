# Example: Tomcat Operator

Simple Operator using the official [Tomcat Helm Chart](https://github.com/kubernetes/charts/tree/master/stable/tomcat).

Run the following
```sh
$ docker build \
  --build-arg HELM_CHART=https://storage.googleapis.com/kubernetes-charts/tomcat-0.1.0.tgz \
  --build-arg API_VERSION=apache.org/v1alpha1 \
  --build-arg KIND=Tomcat \
  -t quay.io/<namespace>/tomcat-operator .
$ kubectl create -f crd.yaml
$ kubectl create -n <operator-namespace> -f csv.yaml
```
