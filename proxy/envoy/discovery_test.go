// Copyright 2017 Istio Authors
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

package envoy

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	restful "github.com/emicklei/go-restful"

	proxyconfig "istio.io/api/proxy/v1/config"
	"istio.io/pilot/adapter/config/memory"
	"istio.io/pilot/model"
	"istio.io/pilot/proxy"
	"istio.io/pilot/test/mock"
	"istio.io/pilot/test/util"
)

// Implement minimal methods to satisfy model.Controller interface for
// creating a new discovery service instance.
type mockController struct {
	handlers int
}

func (ctl *mockController) AppendServiceHandler(_ func(*model.Service, model.Event)) error {
	ctl.handlers++
	return nil
}
func (ctl *mockController) AppendInstanceHandler(_ func(*model.ServiceInstance, model.Event)) error {
	ctl.handlers++
	return nil
}
func (ctl *mockController) Run(_ <-chan struct{}) {}

func makeDiscoveryService(t *testing.T, r model.ConfigStore, mesh *proxyconfig.MeshConfig) *DiscoveryService {
	out, err := NewDiscoveryService(
		&mockController{},
		nil,
		proxy.Environment{
			ServiceDiscovery: mock.Discovery,
			ServiceAccounts:  mock.Discovery,
			IstioConfigStore: model.MakeIstioStore(r),
			Mesh:             mesh,
		},
		DiscoveryServiceOptions{
			EnableCaching:   true,
			EnableProfiling: true, // increase code coverage stats
		})
	if err != nil {
		t.Fatalf("NewDiscoveryService failed: %v", err)
	}
	return out
}

func makeDiscoveryRequest(ds *DiscoveryService, method, url string, t *testing.T) []byte {
	httpRequest, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpWriter := httptest.NewRecorder()
	container := restful.NewContainer()
	ds.Register(container)
	container.ServeHTTP(httpWriter, httpRequest)
	body, err := ioutil.ReadAll(httpWriter.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func commonSetup(t *testing.T) (*proxyconfig.MeshConfig, model.ConfigStore, *DiscoveryService) {
	mesh := makeMeshConfig()
	registry := memory.Make(model.IstioConfigTypes)
	ds := makeDiscoveryService(t, registry, &mesh)
	return &mesh, registry, ds
}

func compareResponse(body []byte, file string, t *testing.T) {
	err := ioutil.WriteFile(file, body, 0644)
	if err != nil {
		t.Fatalf(err.Error())
	}
	util.CompareYAML(file, t)
}

func TestServiceDiscovery(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/registration/" + mock.HelloService.Key(mock.HelloService.Ports[0], nil)
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/sds.json", t)
}

// Can we list Services?
func TestServiceDiscoveryListAllServices(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/registration/"
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/all-sds.json", t)
}

func TestServiceDiscoveryVersion(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/registration/" + mock.HelloService.Key(mock.HelloService.Ports[0],
		map[string]string{"version": "v1"})
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/sds-v1.json", t)
}

func TestServiceDiscoveryEmpty(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/registration/nonexistent"
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/sds-empty.json", t)
}

// Test listing all clusters
func TestClusterDiscoveryAllClusters(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/clusters/"
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/all-cds.json", t)
}

func TestClusterDiscovery(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds.json", t)
}

func TestClusterDiscoveryCircuitBreaker(t *testing.T) {
	_, registry, ds := commonSetup(t)
	// add weighted rule to split into two clusters
	addConfig(registry, weightedRouteRule, t)
	addConfig(registry, cbPolicy, t)
	// add egress rule and a circuit breaker for external service (*.google.com)
	addConfig(registry, egressRule, t)
	addConfig(registry, egressRuleCBPolicy, t)

	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds-circuit-breaker.json", t)
}

func TestClusterDiscoveryWithSSLContext(t *testing.T) {
	mesh := makeMeshConfig()
	mesh.AuthPolicy = proxyconfig.MeshConfig_MUTUAL_TLS
	registry := memory.Make(model.IstioConfigTypes)
	addConfig(registry, egressRule, t) // original dst cluster should not have auth
	ds := makeDiscoveryService(t, registry, &mesh)
	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds-ssl-context.json", t)
}

func TestClusterDiscoveryIngress(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addIngressRoutes(registry, t)
	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.Ingress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds-ingress.json", t)
}

func TestClusterDiscoveryIstioEgress(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.Egress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds-istio-egress.json", t)
}

func TestClusterDiscoveryRouter(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.Router.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/cds-router.json", t)
}

// Test listing all routes
func TestRouteDiscoveryAllRoutes(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := "/v1/routes/"
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/all-rds.json", t)
}

func TestRouteDiscoveryV0(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-v0.json", t)
}

func TestRouteDiscoveryV0Mixerless(t *testing.T) {
	mesh := makeMeshConfig()
	mesh.MixerAddress = ""
	registry := memory.Make(model.IstioConfigTypes)
	addConfig(registry, egressRule, t) //expect *.google.com and *.yahoo.com
	ds := makeDiscoveryService(t, registry, &mesh)
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-v0-nomixer.json", t)
}

func TestRouteDiscoveryV0Status(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/routes/81/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-v0-status.json", t)
}

func TestRouteDiscoveryV1(t *testing.T) {
	_, _, ds := commonSetup(t)
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV1.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-v1.json", t)
}

func TestRouteDiscoveryTimeout(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, egressRule, t)
	addConfig(registry, timeoutRouteRule, t)
	addConfig(registry, egressRuleTimeoutRule, t)
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-timeout.json", t)
}

func TestRouteDiscoveryWeighted(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, weightedRouteRule, t)
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-weighted.json", t)
}

func TestRouteDiscoveryFault(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, faultRouteRule, t)

	// fault rule is source based: we check that the rule only affect v0 and not v1
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-fault.json", t)

	url = fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV1.ServiceNode())
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-v1.json", t)
}

func TestRouteDiscoveryRedirect(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, redirectRouteRule, t)

	// fault rule is source based: we check that the rule only affect v0 and not v1
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-redirect.json", t)
}

func TestRouteDiscoveryRewrite(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, rewriteRouteRule, t)

	// fault rule is source based: we check that the rule only affect v0 and not v1
	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-rewrite.json", t)
}

func TestRouteDiscoveryWebsocket(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, websocketRouteRule, t)

	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-websocket.json", t)

	url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-websocket.json", t)
}

func TestRouteDiscoveryIngress(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addIngressRoutes(registry, t)

	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.Ingress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-ingress.json", t)

	url = fmt.Sprintf("/v1/routes/443/%s/%s", "istio-proxy", mock.Ingress.ServiceNode())
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-ingress-ssl.json", t)
}

func TestRouteDiscoveryIngressWeighted(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addIngressRoutes(registry, t)
	addConfig(registry, weightedRouteRule, t)

	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.Ingress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-ingress-weighted.json", t)
}

func TestRouteDiscoveryRouterWeighted(t *testing.T) {
	_, registry, ds := commonSetup(t)
	addConfig(registry, weightedRouteRule, t)

	url := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.Router.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-router-weighted.json", t)
}

func TestRouteDiscoveryIstioEgress(t *testing.T) {
	_, _, ds := commonSetup(t)

	url := fmt.Sprintf("/v1/routes/8888/%s/%s", "istio-proxy", mock.Egress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-istio-egress.json", t)
}

func TestListenerDiscoverySidecar(t *testing.T) {
	testCases := []struct {
		name string
		file fileConfig
	}{
		{name: "none"},
		/* these configs do not affect listeners
		{
			name: "cb",
			file: cbPolicy,
		},
		{
			name: "redirect",
			file: redirectRouteRule,
		},
		{
			name: "rewrite",
			file: rewriteRouteRule,
		},
		{
			name: "websocket",
			file: websocketRouteRule,
		},
		{
			name: "timeout",
			file: timeoutRouteRule,
		},
		*/
		{
			name: "weighted",
			file: weightedRouteRule,
		},
		{
			name: "fault",
			file: faultRouteRule,
		},
		{
			name: "egressrule",
			file: egressRule,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, registry, ds := commonSetup(t)

			if testCase.name != "none" {
				addConfig(registry, testCase.file, t)
			}

			// test with no auth
			url := fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
			response := makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v0-%s.json", testCase.name), t)

			url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV1.ServiceNode())
			response = makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v1-%s.json", testCase.name), t)

			// test with no mixer
			mesh := makeMeshConfig()
			mesh.MixerAddress = ""
			ds = makeDiscoveryService(t, registry, &mesh)
			url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
			response = makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v0-%s-nomixer.json", testCase.name), t)

			url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV1.ServiceNode())
			response = makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v1-%s-nomixer.json", testCase.name), t)

			// test with auth
			mesh = makeMeshConfig()
			mesh.AuthPolicy = proxyconfig.MeshConfig_MUTUAL_TLS
			ds = makeDiscoveryService(t, registry, &mesh)
			url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
			response = makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v0-%s-auth.json", testCase.name), t)

			url = fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV1.ServiceNode())
			response = makeDiscoveryRequest(ds, "GET", url, t)
			compareResponse(response, fmt.Sprintf("testdata/lds-v1-%s-auth.json", testCase.name), t)
		})
	}
}

func TestListenerDiscoveryIngress(t *testing.T) {
	mesh := makeMeshConfig()
	registry := memory.Make(model.IstioConfigTypes)
	addConfig(registry, egressRule, t)
	addIngressRoutes(registry, t)
	ds := makeDiscoveryService(t, registry, &mesh)
	url := fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.Ingress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-ingress.json", t)

	mesh.AuthPolicy = proxyconfig.MeshConfig_MUTUAL_TLS
	ds = makeDiscoveryService(t, registry, &mesh)
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-ingress.json", t)
}

func TestListenerDiscoveryHttpProxy(t *testing.T) {
	mesh := makeMeshConfig()
	mesh.ProxyListenPort = 0
	mesh.ProxyHttpPort = 15002
	registry := memory.Make(model.IstioConfigTypes)
	ds := makeDiscoveryService(t, registry, &mesh)
	addConfig(registry, egressRule, t)

	url := fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-httpproxy.json", t)
	url = fmt.Sprintf("/v1/routes/%s/%s/%s", RDSAll, "istio-proxy", mock.HelloProxyV0.ServiceNode())
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/rds-httpproxy.json", t)
}

func TestListenerDiscoveryIstioEgress(t *testing.T) {
	mesh := makeMeshConfig()
	registry := memory.Make(model.IstioConfigTypes)
	ds := makeDiscoveryService(t, registry, &mesh)
	addConfig(registry, egressRule, t)
	url := fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.Egress.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-istio-egress.json", t)

	mesh.AuthPolicy = proxyconfig.MeshConfig_MUTUAL_TLS
	ds = makeDiscoveryService(t, registry, &mesh)
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-istio-egress-auth.json", t)
}

func TestListenerDiscoveryRouter(t *testing.T) {
	mesh := makeMeshConfig()
	registry := memory.Make(model.IstioConfigTypes)
	ds := makeDiscoveryService(t, registry, &mesh)
	addConfig(registry, egressRule, t)
	url := fmt.Sprintf("/v1/listeners/%s/%s", "istio-proxy", mock.Router.ServiceNode())
	response := makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-router.json", t)

	mesh.AuthPolicy = proxyconfig.MeshConfig_MUTUAL_TLS
	ds = makeDiscoveryService(t, registry, &mesh)
	response = makeDiscoveryRequest(ds, "GET", url, t)
	compareResponse(response, "testdata/lds-router-auth.json", t)
}

func TestDiscoveryCache(t *testing.T) {
	_, _, ds := commonSetup(t)

	sds := "/v1/registration/" + mock.HelloService.Key(mock.HelloService.Ports[0], nil)
	cds := fmt.Sprintf("/v1/clusters/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	rds := fmt.Sprintf("/v1/routes/80/%s/%s", "istio-proxy", mock.HelloProxyV0.ServiceNode())
	responseByPath := map[string]string{
		sds: "testdata/sds.json",
		cds: "testdata/cds.json",
		rds: "testdata/rds-v1.json",
	}

	cases := []struct {
		wantCache  string
		query      bool
		clearCache bool
		clearStats bool
	}{
		{
			wantCache: "testdata/cache-empty.json",
		},
		{
			wantCache: "testdata/cache-cold.json",
			query:     true,
		},
		{
			wantCache: "testdata/cache-warm-one.json",
			query:     true,
		},
		{
			wantCache: "testdata/cache-warm-two.json",
			query:     true,
		},
		{
			wantCache:  "testdata/cache-cleared.json",
			clearCache: true,
			query:      true,
		},
		{
			wantCache:  "testdata/cache-cold.json",
			clearCache: true,
			clearStats: true,
			query:      true,
		},
	}
	for _, c := range cases {
		if c.clearCache {
			ds.clearCache()
		}
		if c.clearStats {
			_ = makeDiscoveryRequest(ds, "POST", "/cache_stats_delete", t)
		}
		if c.query {
			for path, want := range responseByPath {
				got := makeDiscoveryRequest(ds, "GET", path, t)
				compareResponse(got, want, t)
			}
		}
		got := makeDiscoveryRequest(ds, "GET", "/cache_stats", t)
		compareResponse(got, c.wantCache, t)
	}
}
