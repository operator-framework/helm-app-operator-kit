package helm

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
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
	chartDir         string
}

// NewInstaller returns a new Helm installer capable of installing and uninstalling releases.
func NewInstaller(storageBackend *storage.Storage, tillerKubeClient *kube.Client, chartDir string) Installer {
	return installer{storageBackend, tillerKubeClient, chartDir}
}

// InstallRelease accepts a custom resource, installs a Helm release using Tiller,
// and returns the custom resource with updated `status`.
func (c installer) InstallRelease(r *v1alpha1.HelmApp) (*v1alpha1.HelmApp, error) {
	cr, err := valuesFromResource(r)
	logrus.Infof("using values: %s", string(cr))
	if err != nil {
		return r, err
	}

	chart, err := chartutil.LoadDir(c.chartDir)
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
			Chart:     chart,
			Values:    &cpb.Config{Raw: string(cr)},
		}
		releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
		if err != nil {
			return r, err
		}
		updatedRelease = releaseResponse.GetRelease()
	} else {
		updateReq := &services.UpdateReleaseRequest{
			Name:   releaseName(r),
			Chart:  chart,
			Values: &cpb.Config{Raw: string(cr)},
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
	tiller := tillerRendererForCR(r, c.storageBackend, c.tillerKubeClient)
	_, err := tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
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
	kubeconfig, _ := rest.InClusterConfig()
	internalClientSet, _ := internalclientset.NewForConfig(kubeconfig)

	return tiller.NewReleaseServer(env, internalClientSet, false)
}

func releaseName(r *v1alpha1.HelmApp) string {
	return fmt.Sprintf("%s-%s", operatorName, r.GetName())
}

func valuesFromResource(r *v1alpha1.HelmApp) ([]byte, error) {
	return yaml.Marshal(r.Spec)
}
