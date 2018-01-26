read -p "Enter your namespace for the example application: "  namespace
read -p "Enter the Docker repository in which to place the built operator (example: quay.io/mynamespace/example-sao): " repository

if [ -z "$namespace" ]; then
    echo "Missing namespace";
    exit 1;
fi

if [ -z "$repository" ]; then
    echo "Missing repository";
    exit 1;
fi

TEMP_DIR=`mktemp -d`

function cleanup {      
  rm -rf "$TEMP_DIR"
}

trap cleanup EXIT

# Copy the yaml files and Dockerfile into the temp directory
cp Dockerfile $TEMP_DIR
cp *.yaml $TEMP_DIR
cp -r example-templates $TEMP_DIR/
cd $TEMP_DIR

# Replace all required "variables"
sed -i.bak "s/YOUR_NAMESPACE_HERE/${namespace}/g" *.yaml
sed -i.bak "s/YOUR_REPO_IMAGE_HERE/${repository//\//\\/}/g" *.yaml

# Build and push
echo "Building and pushing stateless app operator"
docker build -t $repository .
docker push $repository

# Create in cluster
echo "Registering app"
kubectl create -f example-app.crd.yaml
kubectl create -f example-app-operator.v0.0.1.clusterserviceversion.yaml
