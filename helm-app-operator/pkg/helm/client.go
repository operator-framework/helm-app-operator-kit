package helm

import (
	"time"

	"github.com/operator-framework/operator-sdk/pkg/k8sclient"

	"k8s.io/client-go/restmapper"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/helm/pkg/kube"
)

// NewTillerClientFromManager returns a Kubernetes client that can be used with
// a Tiller server.
func NewTillerClient() *kube.Client {
	return kube.New(newClientGetter())

}

type clientGetter struct {
	restConfig      *rest.Config
	discoveryClient discovery.CachedDiscoveryInterface
	restMapper      meta.RESTMapper
}

func (c *clientGetter) ToRESTConfig() (*rest.Config, error) {
	return c.restConfig, nil
}

func (c *clientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	return c.discoveryClient, nil
}

func (c *clientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	return c.restMapper, nil
}

func (c *clientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return nil
}

func newClientGetter() *clientGetter {
	client := k8sclient.GetKubeClient()
	cfg := k8sclient.GetKubeConfig()
	dc := cached.NewMemCacheClient(client.Discovery())
	rm := restmapper.NewDeferredDiscoveryRESTMapper(dc)

	rm.Reset()
	ticker := time.NewTicker(time.Minute)
	go func() {
		for range ticker.C {
			rm.Reset()
		}
	}()

	return &clientGetter{
		restConfig:      cfg,
		discoveryClient: dc,
		restMapper:      rm,
	}
}
