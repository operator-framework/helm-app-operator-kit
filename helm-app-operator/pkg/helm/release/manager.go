// Copyright 2018 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/martinlindhe/base36"
	"github.com/pborman/uuid"
	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/helm/pkg/chartutil"
	helmengine "k8s.io/helm/pkg/engine"
	"k8s.io/helm/pkg/kube"
	cpb "k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/proto/hapi/release"
	rpb "k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/engine"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/types"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/util"
)

// ManagerFactory creates Managers that are specific to custom resources. It is
// used by the HelmOperatorReconciler during resource reconciliation, and it
// improves decoupling between reconciliation logic and the Helm backend
// components used to manage releases.
type ManagerFactory interface {
	NewManager(r *unstructured.Unstructured) Manager
}

type managerFactory struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string
}

func (f managerFactory) NewManager(r *unstructured.Unstructured) Manager {
	return f.newManagerForCR(r)
}

func (f managerFactory) newManagerForCR(r *unstructured.Unstructured) Manager {
	return &manager{
		storageBackend:   f.storageBackend,
		tillerKubeClient: f.tillerKubeClient,
		chartDir:         f.chartDir,

		tiller:      f.tillerRendererForCR(r),
		releaseName: getReleaseName(r),
		namespace:   r.GetNamespace(),

		resource: r,
		spec:     r.Object["spec"],
		status:   types.StatusFor(r),
	}
}

// Manager manages a Helm release. It can install, update, reconcile,
// and uninstall a release.
type Manager interface {
	ReconcileRelease() (*rpb.Release, bool, error)
	UninstallRelease() (*rpb.Release, error)
}

type manager struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string

	tiller      *tiller.ReleaseServer
	releaseName string
	namespace   string

	resource *unstructured.Unstructured
	spec     interface{}
	status   *types.HelmAppStatus
}

// ReconcileRelease ensures the managed release is reconciled,
// and returns the updated release if successful (or an error otherwise).
// - If the release is not already installed, a new release will be installed.
// - If the release has changed, the release will be updated.
// - If the release has no changes, the underlying resources will be reconciled.
func (m manager) ReconcileRelease() (*rpb.Release, bool, error) {
	needsUpdate := false

	// chart is mutated by the call to processRequirements,
	// so we need to reload it from disk every time.
	chart, err := chartutil.LoadDir(m.chartDir)
	if err != nil {
		return nil, needsUpdate, fmt.Errorf("failed to load chart: %s", err)
	}

	cr, err := yaml.Marshal(m.spec)
	if err != nil {
		return nil, needsUpdate, fmt.Errorf("failed to parse values: %s", err)
	}
	config := &cpb.Config{Raw: string(cr)}
	logrus.Debugf("Using values: %s", config.GetRaw())

	err = processRequirements(chart, config)
	if err != nil {
		return nil, needsUpdate, fmt.Errorf("failed to process chart requirements: %s", err)
	}

	tiller := m.tiller

	status := m.status
	if err := m.syncReleaseStatus(*status); err != nil {
		return nil, needsUpdate, fmt.Errorf("failed to sync release status: %s", err)
	}

	releaseName := m.releaseName

	// Get release history for this release name
	releases, err := m.storageBackend.History(releaseName)
	if err != nil && !notFoundErr(err) {
		return nil, needsUpdate, fmt.Errorf("failed to retrieve release history: %s", err)
	}

	// Cleanup non-deployed release versions. If all release versions are
	// non-deployed, this will ensure that failed installations are correctly
	// retried.
	for _, rel := range releases {
		if rel.GetInfo().GetStatus().GetCode() != release.Status_DEPLOYED {
			_, err := m.storageBackend.Delete(rel.GetName(), rel.GetVersion())
			if err != nil && !notFoundErr(err) {
				return nil, needsUpdate, fmt.Errorf("failed to delete stale release version: %s", err)
			}
		}
	}

	var updatedRelease *release.Release
	latestRelease, err := m.storageBackend.Deployed(releaseName)
	if err != nil || latestRelease == nil {
		// If there's no deployed release, attempt a tiller install.
		updatedRelease, err = m.installRelease(tiller, m.namespace, releaseName, chart, config)
		if err != nil {
			return nil, needsUpdate, fmt.Errorf("install error: %s", err)
		}
		needsUpdate = true
		diffStr := util.Diff("", updatedRelease.GetManifest())
		logrus.Infof("Installed release for %s release=%s; diff:\n%s", util.ResourceString(m.resource), updatedRelease.GetName(), diffStr)
	} else {
		candidateRelease, err := m.getCandidateRelease(tiller, releaseName, chart, config)
		if err != nil {
			return nil, needsUpdate, fmt.Errorf("failed to generate candidate release: %s", err)
		}

		latestManifest := latestRelease.GetManifest()
		if latestManifest == candidateRelease.GetManifest() {
			err = m.reconcileRelease(m.namespace, latestManifest)
			if err != nil {
				return nil, needsUpdate, fmt.Errorf("reconcile error: %s", err)
			}
			updatedRelease = latestRelease
			logrus.Infof("Reconciled release for %s release=%s", util.ResourceString(m.resource), updatedRelease.GetName())
		} else {
			updatedRelease, err = m.updateRelease(tiller, releaseName, chart, config)
			if err != nil {
				return nil, needsUpdate, fmt.Errorf("update error: %s", err)
			}
			needsUpdate = true
			diffStr := util.Diff(latestManifest, updatedRelease.GetManifest())
			logrus.Infof("Updated release for %s release=%s; diff:\n%s", util.ResourceString(m.resource), updatedRelease.GetName(), diffStr)
		}
	}
	return updatedRelease, needsUpdate, nil
}

// UninstallRelease uninstalls the managed release.
func (m manager) UninstallRelease() (*rpb.Release, error) {
	releaseName := m.releaseName

	// Get history of this release
	h, err := m.storageBackend.History(releaseName)
	if err != nil {
		return nil, fmt.Errorf("failed to get release history: %s", err)
	}

	// If there is no history, the release has already been uninstalled,
	// so there's nothing to do.
	if len(h) == 0 {
		return nil, nil
	}

	tiller := m.tiller
	uninstallResponse, err := tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  releaseName,
		Purge: true,
	})
	if err != nil {
		return nil, err
	}
	uninstalledRelease := uninstallResponse.GetRelease()
	diffStr := util.Diff(uninstalledRelease.GetManifest(), "")
	logrus.Infof("Uninstalled release for %s release=%s; diff:\n%s", util.ResourceString(m.resource), releaseName, diffStr)
	return uninstalledRelease, nil
}

func (m manager) installRelease(tiller *tiller.ReleaseServer, namespace, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	installReq := &services.InstallReleaseRequest{
		Namespace: namespace,
		Name:      name,
		Chart:     chart,
		Values:    config,
	}

	releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			uninstallReq := &services.UninstallReleaseRequest{
				Name:  releaseResponse.GetRelease().GetName(),
				Purge: true,
			}
			_, uninstallErr := tiller.UninstallRelease(context.TODO(), uninstallReq)
			if uninstallErr != nil {
				return nil, fmt.Errorf("failed to roll back failed installation: %s: %s", uninstallErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (m manager) updateRelease(tiller *tiller.ReleaseServer, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	updateReq := &services.UpdateReleaseRequest{
		Name:   name,
		Chart:  chart,
		Values: config,
	}

	releaseResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			rollbackReq := &services.RollbackReleaseRequest{
				Name:  name,
				Force: true,
			}
			_, rollbackErr := tiller.RollbackRelease(context.TODO(), rollbackReq)
			if rollbackErr != nil {
				return nil, fmt.Errorf("failed to roll back failed update: %s: %s", rollbackErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (m manager) reconcileRelease(namespace string, expectedManifest string) error {
	expectedInfos, err := m.tillerKubeClient.BuildUnstructured(namespace, bytes.NewBufferString(expectedManifest))
	if err != nil {
		return err
	}
	return expectedInfos.Visit(func(expected *resource.Info, err error) error {
		if err != nil {
			return err
		}
		helper := resource.NewHelper(expected.Client, expected.Mapping)
		_, err = helper.Create(expected.Namespace, true, expected.Object)
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create error: %s", err)
		}

		patch, err := json.Marshal(expected.Object)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %s", err)
		}

		_, err = helper.Patch(expected.Namespace, expected.Name, apitypes.MergePatchType, patch)
		if err != nil {
			return fmt.Errorf("patch error: %s", err)
		}
		return nil
	})
}

func (m manager) getCandidateRelease(tiller *tiller.ReleaseServer, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   name,
		Chart:  chart,
		Values: config,
		DryRun: true,
	}
	dryRunResponse, err := tiller.UpdateRelease(context.TODO(), dryRunReq)
	if err != nil {
		return nil, err
	}
	return dryRunResponse.GetRelease(), nil
}

func (m manager) syncReleaseStatus(status types.HelmAppStatus) error {
	if status.Release == nil {
		return nil
	}

	name := status.Release.GetName()
	version := status.Release.GetVersion()
	_, err := m.storageBackend.Get(name, version)
	if err == nil {
		return nil
	}

	if !notFoundErr(err) {
		return err
	}
	return m.storageBackend.Create(status.Release)
}

// tillerRendererForCR creates a ReleaseServer configured with a rendering engine that adds ownerrefs to rendered assets
// based on the CR.
func (f managerFactory) tillerRendererForCR(r *unstructured.Unstructured) *tiller.ReleaseServer {
	controllerRef := metav1.NewControllerRef(r, r.GroupVersionKind())
	ownerRefs := []metav1.OwnerReference{
		*controllerRef,
	}
	baseEngine := helmengine.New()
	e := engine.NewOwnerRefEngine(baseEngine, ownerRefs)
	var ey environment.EngineYard = map[string]environment.Engine{
		environment.GoTplEngine: e,
	}
	env := &environment.Environment{
		EngineYard: ey,
		Releases:   f.storageBackend,
		KubeClient: f.tillerKubeClient,
	}
	kubeconfig, _ := f.tillerKubeClient.ToRESTConfig()
	internalClientSet, _ := internalclientset.NewForConfig(kubeconfig)

	return tiller.NewReleaseServer(env, internalClientSet, false)
}

func getReleaseName(r *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", r.GetName(), shortenUID(r.GetUID()))
}

func notFoundErr(err error) bool {
	return strings.Contains(err.Error(), "not found")
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

func shortenUID(uid apitypes.UID) (shortUID string) {
	u := uuid.Parse(string(uid))
	uidBytes, err := u.MarshalBinary()
	if err != nil {
		shortUID = strings.Replace(string(uid), "-", "", -1)
	}
	shortUID = strings.ToLower(base36.EncodeBytes(uidBytes))
	return
}
