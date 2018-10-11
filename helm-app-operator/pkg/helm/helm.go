package helm

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	// HelmChartWatchesEnvVar is the environment variable for a YAML
	// configuration file containing mappings of GVKs to helm charts. Use of
	// this environment variable overrides the watch configuration provided
	// by API_VERSION, KIND, and HELM_CHART, and it allows users to configure
	// multiple watches, each with a different chart.
	HelmChartWatchesEnvVar = "HELM_CHART_WATCHES"

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

	operatorName                = "helm-app-operator"
	defaultHelmChartWatchesFile = "/opt/helm/watches.yaml"
)

// Installer can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Installer interface {
	InstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error)
	UninstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

type installer struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chart            *cpb.Chart
}

type watch struct {
	Group   string `yaml:"group"`
	Version string `yaml:"version"`
	Kind    string `yaml:"kind"`
	Chart   string `yaml:"chart"`
}

// NewInstaller returns a new Helm installer capable of installing and uninstalling releases.
func NewInstaller(storageBackend *storage.Storage, tillerKubeClient *kube.Client, chart *cpb.Chart) Installer {
	return installer{storageBackend, tillerKubeClient, chart}
}

// newInstallerFromEnv returns a GVK and installer based on configuration provided
// in the environment.
func newInstallerFromEnv(storageBackend *storage.Storage, tillerKubeClient *kube.Client) (schema.GroupVersionKind, Installer, error) {
	apiVersion := os.Getenv(APIVersionEnvVar)
	kind := os.Getenv(KindEnvVar)
	chartDir := os.Getenv(HelmChartEnvVar)

	var gvk schema.GroupVersionKind
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return gvk, nil, err
	}
	gvk = gv.WithKind(kind)

	if err := verifyGVK(gvk); err != nil {
		return gvk, nil, fmt.Errorf("invalid GVK: %s: %s", gvk, err)
	}

	chart, err := loadChart(chartDir)
	if err != nil {
		return gvk, nil, fmt.Errorf("invalid chart directory: failed to load chart from %s: %s", chartDir, err)
	}

	installer := NewInstaller(storageBackend, tillerKubeClient, chart)
	return gvk, installer, nil
}

// NewInstallersFromEnv returns a map of installers, keyed by GVK, based on
// configuration provided in the environment.
func NewInstallersFromEnv(storageBackend *storage.Storage, tillerKubeClient *kube.Client) (map[schema.GroupVersionKind]Installer, error) {
	if watchesFile, ok := getWatchesFile(); ok {
		return NewInstallersFromFile(storageBackend, tillerKubeClient, watchesFile)
	}
	gvk, installer, err := newInstallerFromEnv(storageBackend, tillerKubeClient)
	if err != nil {
		return nil, err
	}
	return map[schema.GroupVersionKind]Installer{gvk: installer}, nil
}

// NewInstallersFromFile reads the config file at the provided path and returns a map
// of installers, keyed by each GVK in the config.
func NewInstallersFromFile(storageBackend *storage.Storage, tillerKubeClient *kube.Client, path string) (map[schema.GroupVersionKind]Installer, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %s", err)
	}
	watches := []watch{}
	err = yaml.Unmarshal(b, &watches)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %s", err)
	}

	m := map[schema.GroupVersionKind]Installer{}
	for _, w := range watches {
		gvk := schema.GroupVersionKind{
			Group:   w.Group,
			Version: w.Version,
			Kind:    w.Kind,
		}

		if err := verifyGVK(gvk); err != nil {
			return nil, fmt.Errorf("invalid GVK: %s: %s", gvk, err)
		}

		chart, err := loadChart(w.Chart)
		if err != nil {
			return nil, fmt.Errorf("failed to load chart from %s: %s", w.Chart, err)
		}

		if _, ok := m[gvk]; ok {
			return nil, fmt.Errorf("duplicate GVK: %s", gvk)
		}
		m[gvk] = NewInstaller(storageBackend, tillerKubeClient, chart)
	}
	return m, nil
}

func verifyGVK(gvk schema.GroupVersionKind) error {
	// A GVK without a group is valid. Certain scenarios may cause a GVK
	// without a group to fail in other ways later in the initialization
	// process.
	if gvk.Version == "" {
		return errors.New("version must not be empty")
	}
	if gvk.Kind == "" {
		return errors.New("kind must not be empty")
	}
	return nil
}

func loadChart(path string) (*cpb.Chart, error) {
	if path == "" {
		return nil, errors.New("path must not be empty")
	}
	if stat, err := os.Stat(path); err != nil || !stat.IsDir() {
		return nil, errors.New("path is not a directory")
	}
	return chartutil.LoadDir(path)
}

func getWatchesFile() (string, bool) {
	// If the watches env variable is set (even if it's an empty string), use it
	// since the user explicitly set it.
	if watchesFile, ok := os.LookupEnv(HelmChartWatchesEnvVar); ok {
		return watchesFile, true
	}

	// Next, check if the default watches file is present. If so, use it.
	if _, err := os.Stat(defaultHelmChartWatchesFile); err == nil {
		return defaultHelmChartWatchesFile, true
	}
	return "", false
}

// InstallRelease accepts a custom resource, installs a Helm release using Tiller,
// and returns the custom resource with updated `status`.
func (c installer) InstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	cr, err := valuesFromResource(r)
	logrus.Infof("using values: %s", string(cr))
	if err != nil {
		return r, err
	}

	var updatedRelease *release.Release
	latestRelease, err := c.storageBackend.Last(releaseName(r))

	tiller := tillerRendererForCR(r, c.storageBackend, c.tillerKubeClient)

	status := v1alpha1.StatusFor(r)
	c.syncReleaseStatus(*status)

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

	status = v1alpha1.StatusFor(r)
	status.SetRelease(updatedRelease)
	// TODO(alecmerdler): Call `status.SetPhase()` with `NOTES.txt` of rendered Chart
	status.SetPhase(v1alpha1.PhaseApplied, v1alpha1.ReasonApplySuccessful, "")
	r.Object["status"] = status

	return r, nil
}

// UninstallRelease accepts a custom resource, uninstalls the existing Helm release
// using Tiller, and returns the custom resource with updated `status`.
func (c installer) UninstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error) {
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
func tillerRendererForCR(r *unstructured.Unstructured, storageBackend *storage.Storage, tillerKubeClient *kube.Client) *tiller.ReleaseServer {
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

func releaseName(r *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", operatorName, r.GetName())
}

func valuesFromResource(r *unstructured.Unstructured) ([]byte, error) {
	return yaml.Marshal(r.Object["spec"])
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
