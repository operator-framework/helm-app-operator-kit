package stub

import (
	"context"
	"fmt"
	"strings"

	"github.com/operator-framework/operator-sdk/pkg/sdk"
	"github.com/sirupsen/logrus"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/apis/app/v1alpha1"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
)

func NewHandler(controller helm.Installer) sdk.Handler {
	return &Handler{controller}
}

type Handler struct {
	controller helm.Installer
}

var lastResourceVersion string

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	switch o := event.Object.(type) {
	case *v1alpha1.HelmApp:
		logrus.Infof("processing %s", strings.Join([]string{o.GetNamespace(), o.GetName()}, "/"))

		if event.Deleted {
			_, err := h.controller.UninstallRelease(o)
			return err
		}
		if o.GetResourceVersion() == lastResourceVersion {
			logrus.Infof("skipping %s because resource version has not changed", strings.Join([]string{o.GetNamespace(), o.GetName()}, "/"))
			return nil
		}

		updatedResource, err := h.controller.InstallRelease(o)
		if err != nil {
			logrus.Errorf(err.Error())
			return err
		}

		err = sdk.Update(updatedResource)
		if err != nil {
			logrus.Errorf(err.Error())
			return fmt.Errorf("failed to update custom resource status: %v", err)
		}
		lastResourceVersion = o.GetResourceVersion()
	}
	return nil
}
