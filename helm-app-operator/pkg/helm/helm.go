package helm

import (
	"context"
	"fmt"
	"os"

	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/engine"
	"k8s.io/helm/pkg/kube"
	cpb "k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/apis/app/v1alpha1"
)

const (
	// APIVersionEnvVar is the environment variable for the group and version
	// to be watched using the format `<group>/<version>`
	// (e.g. "example.com/v1alpha1").
	APIVersionEnvVar = "API_VERSION"

	// KindEnvVar is the environment variable for the kind to be watched. The
	// value is typically singular and should be CamelCased (e.g. "MyApp").
	KindEnvVar = "KIND"

	// HelmChartEnvVar is the environment variable for the directory location
	// of the helm chart to be installed for CRs that match the values for the
	// API_VERSION and KIND environment variables.
	HelmChartEnvVar = "HELM_CHART"

	operatorName = "helm-app-operator"
)

// Installer can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Installer interface {
	InstallRelease(r *v1alpha1.HelmApp) (*v1alpha1.HelmApp, error)
	UninstallRelease(r *v1alpha1.HelmApp) (*v1alpha1.HelmApp, error)
}

type installer struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chart            *cpb.Chart
}

// NewInstaller returns a new Helm installer capable of installing and uninstalling releases.
func NewInstaller(storageBackend *storage.Storage, tillerKubeClient *kube.Client, chart *cpb.Chart) Installer {
	return installer{storageBackend, tillerKubeClient, chart}
}

// NewInstallerFromEnv returns a GVK and installer based on configuration provided
// in the environment.
func NewInstallerFromEnv(storageBackend *storage.Storage, tillerKubeClient *kube.Client) (schema.GroupVersionKind, Installer, error) {
	apiVersion := os.Getenv(APIVersionEnvVar)
	kind := os.Getenv(KindEnvVar)
	chartDir := os.Getenv(HelmChartEnvVar)

	var gvk schema.GroupVersionKind
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return gvk, nil, err
	}
	gvk = gv.WithKind(kind)

	// Verify the GVK. In general, GVKs without groups are valid. However,
	// a GVK without a group will most likely fail with a more descriptive
	// error later in the initialization process.
	if gvk.Version == "" {
		return gvk, nil, fmt.Errorf("invalid %s: version must not be empty", APIVersionEnvVar)
	}
	if gvk.Kind == "" {
		return gvk, nil, fmt.Errorf("invalid %s: kind must not be empty", KindEnvVar)
	}

	// Verify that the Helm chart directory is valid.
	if chartDir == "" {
		return gvk, nil, fmt.Errorf("invalid %s: must not be empty", HelmChartEnvVar)
	}
	if stat, err := os.Stat(chartDir); err != nil || !stat.IsDir() {
		return gvk, nil, fmt.Errorf("invalid %s: %s is not a directory", HelmChartEnvVar, chartDir)
	}
	chart, err := chartutil.LoadDir(chartDir)
	if err != nil {
		return gvk, nil, fmt.Errorf("invalid %s: failed loading chart from %s: %s", HelmChartEnvVar, chartDir, err)
	}

	installer := NewInstaller(storageBackend, tillerKubeClient, chart)
	return gvk, installer, nil
}

// InstallRelease accepts a custom resource, installs a Helm release using Tiller,
// and returns the custom resource with updated `status`.
func (c installer) InstallRelease(r *v1alpha1.HelmApp) (*v1alpha1.HelmApp, error) {
	cr, err := valuesFromResource(r)
	logrus.Infof("using values: %s", string(cr))
	if err != nil {
		return r, err
	}

	var updatedRelease *release.Release
	latestRelease, err := c.storageBackend.Last(releaseName(r))

	tiller := tillerRendererForCR(r, c.storageBackend, c.tillerKubeClient)
	c.syncReleaseStatus(r.Status)

	if err != nil || latestRelease == nil {
		installReq := &services.InstallReleaseRequest{
			Namespace: r.GetNamespace(),
			Name:      releaseName(r),
			Chart:     c.chart,
			Values:    &cpb.Config{Raw: string(cr)},
		}

		err := processRequirements(installReq.Chart, installReq.Values)
		if err != nil {
			return nil, err
		}

		releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
		if err != nil {
			return r, err
		}
		updatedRelease = releaseResponse.GetRelease()
	} else {
		updateReq := &services.UpdateReleaseRequest{
			Name:   releaseName(r),
			Chart:  c.chart,
			Values: &cpb.Config{Raw: string(cr)},
		}

		err := processRequirements(updateReq.Chart, updateReq.Values)
		if err != nil {
			return r, err
		}

		releaseResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
		if err != nil {
			return r, err
		}
		updatedRelease = releaseResponse.GetRelease()
	}

	r.Status = *r.Status.SetRelease(updatedRelease)
	// TODO(alecmerdler): Call `r.Status.SetPhase()` with `NOTES.txt` of rendered Chart
	r.Status = *r.Status.SetPhase(v1alpha1.PhaseApplied, v1alpha1.ReasonApplySuccessful, "")

	return r, nil
}

// UninstallRelease accepts a custom resource, uninstalls the existing Helm release
// using Tiller, and returns the custom resource with updated `status`.
func (c installer) UninstallRelease(r *v1alpha1.HelmApp) (*v1alpha1.HelmApp, error) {
	// Get history of this release
	h, err := c.storageBackend.History(releaseName(r))
	if err != nil {
		return r, err
	}

	// If there is no history, the release has already been uninstalled,
	// so there's nothing to do.
	if len(h) == 0 {
		return r, nil
	}

	tiller := tillerRendererForCR(r, c.storageBackend, c.tillerKubeClient)
	_, err = tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  releaseName(r),
		Purge: true,
	})
	if err != nil {
		return r, err
	}

	return r, nil
}

func (c installer) syncReleaseStatus(status v1alpha1.HelmAppStatus) {
	if status.Release == nil {
		return
	}
	if _, err := c.storageBackend.Get(status.Release.GetName(), status.Release.GetVersion()); err == nil {
		return
	}

	c.storageBackend.Create(status.Release)
}

// tillerRendererForCR creates a ReleaseServer configured with a rendering engine that adds ownerrefs to rendered assets
// based on the CR.
func tillerRendererForCR(r *v1alpha1.HelmApp, storageBackend *storage.Storage, tillerKubeClient *kube.Client) *tiller.ReleaseServer {
	controllerRef := metav1.NewControllerRef(r, r.GroupVersionKind())
	ownerRefs := []metav1.OwnerReference{
		*controllerRef,
	}
	baseEngine := engine.New()
	e := NewOwnerRefEngine(baseEngine, ownerRefs)
	var ey environment.EngineYard = map[string]environment.Engine{
		environment.GoTplEngine: e,
	}
	env := &environment.Environment{
		EngineYard: ey,
		Releases:   storageBackend,
		KubeClient: tillerKubeClient,
	}
	// Can't use `k8sclient.GetKubeClient()` because it implements the wrong interface
	kubeconfig, _ := tillerKubeClient.ToRESTConfig()
	internalClientSet, _ := internalclientset.NewForConfig(kubeconfig)

	return tiller.NewReleaseServer(env, internalClientSet, false)
}

func releaseName(r *v1alpha1.HelmApp) string {
	return fmt.Sprintf("%s-%s", operatorName, r.GetName())
}

func valuesFromResource(r *v1alpha1.HelmApp) ([]byte, error) {
	return yaml.Marshal(r.Spec)
}

// processRequirements will process the requirements file
// It will disable/enable the charts based on condition in requirements file
// Also imports the specified chart values from child to parent.
func processRequirements(chart *cpb.Chart, values *cpb.Config) error {
	err := chartutil.ProcessRequirementsEnabled(chart, values)
	if err != nil {
		return err
	}
	err = chartutil.ProcessRequirementsImportValues(chart)
	if err != nil {
		return err
	}
	return nil
}
