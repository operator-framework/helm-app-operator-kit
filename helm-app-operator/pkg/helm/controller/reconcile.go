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

	updatedResource, needsUpdate, err := r.Installer.ReconcileRelease(o)
	if err != nil {
		logrus.Errorf(err.Error())
		return reconcile.Result{}, err
	}

	if needsUpdate {
		err = r.Client.Update(context.TODO(), updatedResource)
		if err != nil {
			logrus.Errorf(err.Error())
			return reconcile.Result{}, fmt.Errorf("failed to update custom resource status: %v", err)
		}
	}

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
