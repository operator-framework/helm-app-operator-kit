package v1alpha1

import (
	"os"
	"strings"

	sdkK8sutil "github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	sdkK8sutil.AddToSDKScheme(AddToScheme)
}

// addKnownTypes adds the set of types defined in this package to the supplied scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	apiVersion := os.Getenv("API_VERSION")
	kind := os.Getenv("KIND")

	groupVersion := schema.GroupVersion{
		Group:   strings.Split(apiVersion, "/")[0],
		Version: strings.Split(apiVersion, "/")[1],
	}
	scheme.AddKnownTypeWithName(groupVersion.WithKind(kind), &HelmApp{})
	scheme.AddKnownTypeWithName(groupVersion.WithKind(kind+"List"), &HelmAppList{})
	metav1.AddToGroupVersion(scheme, groupVersion)

	return nil
}
