package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
)

type helmOperatorReconciler struct {
	Client       client.Client
	GVK          schema.GroupVersionKind
	Installer    helm.Installer
	ResyncPeriod time.Duration

	lastResourceVersions map[types.NamespacedName]string
	mutex                sync.RWMutex
}

const (
	finalizer = "uninstall-helm-release"
)

func (r *helmOperatorReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	logrus.Infof("processing %s", request.NamespacedName)

	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(r.GVK)
	err := r.Client.Get(context.TODO(), request.NamespacedName, o)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}

	deleted := o.GetDeletionTimestamp() != nil
	pendingFinalizers := o.GetFinalizers()
	if !deleted && !contains(pendingFinalizers, finalizer) {
		logrus.Debugf("adding finalizer %s to resource", finalizer)
		finalizers := append(pendingFinalizers, finalizer)
		o.SetFinalizers(finalizers)
		err := r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}
	if deleted {
		if !contains(pendingFinalizers, finalizer) {
			logrus.Info("resouce is terminated, skipping reconciliation")
			return reconcile.Result{}, nil
		}

		_, err = r.Installer.UninstallRelease(o)
		if err != nil {
			return reconcile.Result{}, err
		}

		finalizers := []string{}
		for _, pendingFinalizer := range pendingFinalizers {
			if pendingFinalizer != finalizer {
				finalizers = append(finalizers, pendingFinalizer)
			}
		}
		o.SetFinalizers(finalizers)
		err := r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}

	lastResourceVersion, ok := r.getLastResourceVersion(request.NamespacedName)
	if ok && o.GetResourceVersion() == lastResourceVersion {
		logrus.Infof("skipping %s because resource version has not changed", request.NamespacedName)
		return reconcile.Result{RequeueAfter: r.ResyncPeriod}, nil
	}

	updatedResource, err := r.Installer.InstallRelease(o)
	if err != nil {
		logrus.Errorf(err.Error())
		return reconcile.Result{}, err
	}

	err = r.Client.Update(context.TODO(), updatedResource)
	if err != nil {
		logrus.Errorf(err.Error())
		return reconcile.Result{}, fmt.Errorf("failed to update custom resource status: %v", err)
	}
	r.setLastResourceVersion(request.NamespacedName, o.GetResourceVersion())

	return reconcile.Result{RequeueAfter: r.ResyncPeriod}, nil
}

func contains(l []string, s string) bool {
	for _, elem := range l {
		if elem == s {
			return true
		}
	}
	return false
}

func (r *helmOperatorReconciler) getLastResourceVersion(n types.NamespacedName) (string, bool) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	v, ok := r.lastResourceVersions[n]
	return v, ok
}

func (r *helmOperatorReconciler) setLastResourceVersion(n types.NamespacedName, v string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.lastResourceVersions[n] = v
}
