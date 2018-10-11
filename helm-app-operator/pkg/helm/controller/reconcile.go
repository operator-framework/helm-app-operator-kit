package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
)

type helmOperatorReconciler struct {
	Client       client.Client
	GVK          schema.GroupVersionKind
	Installer    helm.Installer
	ResyncPeriod time.Duration
}

var lastResourceVersion string

func (r helmOperatorReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	logrus.Infof("processing %s", request.NamespacedName)

	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(r.GVK)
	err := r.Client.Get(context.TODO(), request.NamespacedName, o)
	o.SetName(request.Name)
	o.SetNamespace(request.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err = r.Installer.UninstallRelease(o)
		}
		return reconcile.Result{}, err
	}

	if o.GetResourceVersion() == lastResourceVersion {
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
	lastResourceVersion = o.GetResourceVersion()

	return reconcile.Result{RequeueAfter: r.ResyncPeriod}, nil
}
