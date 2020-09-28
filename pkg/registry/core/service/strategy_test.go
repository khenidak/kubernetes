/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package service

import (
	"net"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/intstr"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	api "k8s.io/kubernetes/pkg/apis/core"
	_ "k8s.io/kubernetes/pkg/apis/core/install"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/kubernetes/pkg/features"
)

func newStrategy(cidr string, hasSecondary bool) (testStrategy Strategy, testStatusStrategy Strategy) {
	_, testCIDR, err := net.ParseCIDR(cidr)
	if err != nil {
		panic("invalid CIDR")
	}
	testStrategy, _ = StrategyForServiceCIDRs(*testCIDR, hasSecondary)
	testStatusStrategy = NewServiceStatusStrategy(testStrategy)
	return
}

func TestExportService(t *testing.T) {
	testStrategy, _ := newStrategy("10.0.0.0/16", false)
	tests := []struct {
		objIn     runtime.Object
		objOut    runtime.Object
		exact     bool
		expectErr bool
	}{
		{
			objIn: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Status: api.ServiceStatus{
					LoadBalancer: api.LoadBalancerStatus{
						Ingress: []api.LoadBalancerIngress{
							{IP: "1.2.3.4"},
						},
					},
				},
			},
			objOut: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
			},
			exact: true,
		},
		{
			objIn: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Spec: api.ServiceSpec{
					ClusterIPs: []string{"10.0.0.1"},
				},
				Status: api.ServiceStatus{
					LoadBalancer: api.LoadBalancerStatus{
						Ingress: []api.LoadBalancerIngress{
							{IP: "1.2.3.4"},
						},
					},
				},
			},
			objOut: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Spec: api.ServiceSpec{
					ClusterIPs: nil,
				},
			},
		},
		{
			objIn: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Spec: api.ServiceSpec{
					ClusterIPs: []string{"10.0.0.1", "2001::1"},
				},
				Status: api.ServiceStatus{
					LoadBalancer: api.LoadBalancerStatus{
						Ingress: []api.LoadBalancerIngress{
							{IP: "1.2.3.4"},
						},
					},
				},
			},
			objOut: &api.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "bar",
				},
				Spec: api.ServiceSpec{
					ClusterIPs: nil,
				},
			},
		},

		{
			objIn:     &api.Pod{},
			expectErr: true,
		},
	}

	for _, test := range tests {
		err := testStrategy.Export(genericapirequest.NewContext(), test.objIn, test.exact)
		if err != nil {
			if !test.expectErr {
				t.Errorf("unexpected error: %v", err)
			}
			continue
		}
		if test.expectErr {
			t.Error("unexpected non-error")
			continue
		}
		if !reflect.DeepEqual(test.objIn, test.objOut) {
			t.Errorf("expected:\n%v\nsaw:\n%v\n", test.objOut, test.objIn)
		}
	}
}

func TestCheckGeneratedNameError(t *testing.T) {
	testStrategy, _ := newStrategy("10.0.0.0/16", false)
	expect := errors.NewNotFound(api.Resource("foos"), "bar")
	if err := rest.CheckGeneratedNameError(testStrategy, expect, &api.Service{}); err != expect {
		t.Errorf("NotFoundError should be ignored: %v", err)
	}

	expect = errors.NewAlreadyExists(api.Resource("foos"), "bar")
	if err := rest.CheckGeneratedNameError(testStrategy, expect, &api.Service{}); err != expect {
		t.Errorf("AlreadyExists should be returned when no GenerateName field: %v", err)
	}

	expect = errors.NewAlreadyExists(api.Resource("foos"), "bar")
	if err := rest.CheckGeneratedNameError(testStrategy, expect, &api.Service{ObjectMeta: metav1.ObjectMeta{GenerateName: "foo"}}); err == nil || !errors.IsServerTimeout(err) {
		t.Errorf("expected try again later error: %v", err)
	}
}

func makeValidService() api.Service {
	return api.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "valid",
			Namespace:       "default",
			Labels:          map[string]string{},
			Annotations:     map[string]string{},
			ResourceVersion: "1",
		},
		Spec: api.ServiceSpec{
			Selector:        map[string]string{"key": "val"},
			SessionAffinity: "None",
			Type:            api.ServiceTypeClusterIP,
			Ports:           []api.ServicePort{{Name: "p", Protocol: "TCP", Port: 8675, TargetPort: intstr.FromInt(8675)}},
		},
	}
}

// TODO: This should be done on types that are not part of our API
func TestBeforeUpdate(t *testing.T) {
	testCases := []struct {
		name      string
		tweakSvc  func(oldSvc, newSvc *api.Service) // given basic valid services, each test case can customize them
		expectErr bool
	}{
		{
			name: "no change",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				// nothing
			},
			expectErr: false,
		},
		{
			name: "change port",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				newSvc.Spec.Ports[0].Port++
			},
			expectErr: false,
		},
		{
			name: "bad namespace",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				newSvc.Namespace = "#$%%invalid"
			},
			expectErr: true,
		},
		{
			name: "change name",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				newSvc.Name += "2"
			},
			expectErr: true,
		},
		{
			name: "change ClusterIP",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				oldSvc.Spec.ClusterIPs = []string{"1.2.3.4"}
				newSvc.Spec.ClusterIPs = []string{"4.3.2.1"}
			},
			expectErr: true,
		},
		{
			name: "change selector",
			tweakSvc: func(oldSvc, newSvc *api.Service) {
				newSvc.Spec.Selector = map[string]string{"newkey": "newvalue"}
			},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		strategy, _ := newStrategy("172.30.0.0/16", false)

		oldSvc := makeValidService()
		newSvc := makeValidService()
		tc.tweakSvc(&oldSvc, &newSvc)
		ctx := genericapirequest.NewDefaultContext()
		err := rest.BeforeUpdate(strategy, ctx, runtime.Object(&oldSvc), runtime.Object(&newSvc))
		if tc.expectErr && err == nil {
			t.Errorf("unexpected non-error for %q", tc.name)
		}
		if !tc.expectErr && err != nil {
			t.Errorf("unexpected error for %q: %v", tc.name, err)
		}
	}
}

func TestServiceStatusStrategy(t *testing.T) {
	_, testStatusStrategy := newStrategy("10.0.0.0/16", false)
	ctx := genericapirequest.NewDefaultContext()
	if !testStatusStrategy.NamespaceScoped() {
		t.Errorf("Service must be namespace scoped")
	}
	oldService := makeValidService()
	newService := makeValidService()
	oldService.ResourceVersion = "4"
	newService.ResourceVersion = "4"
	newService.Spec.SessionAffinity = "ClientIP"
	newService.Status = api.ServiceStatus{
		LoadBalancer: api.LoadBalancerStatus{
			Ingress: []api.LoadBalancerIngress{
				{IP: "127.0.0.2"},
			},
		},
	}
	testStatusStrategy.PrepareForUpdate(ctx, &newService, &oldService)
	if newService.Status.LoadBalancer.Ingress[0].IP != "127.0.0.2" {
		t.Errorf("Service status updates should allow change of status fields")
	}
	if newService.Spec.SessionAffinity != "None" {
		t.Errorf("PrepareForUpdate should have preserved old spec")
	}
	errs := testStatusStrategy.ValidateUpdate(ctx, &newService, &oldService)
	if len(errs) != 0 {
		t.Errorf("Unexpected error %v", errs)
	}
}

func makeServiceWithIPFamilies(ipfamilies []api.IPFamily, ipFamilyPolicy *api.IPFamilyPolicyType) *api.Service {
	return &api.Service{
		Spec: api.ServiceSpec{
			IPFamilies:     ipfamilies,
			IPFamilyPolicy: ipFamilyPolicy,
		},
	}
}

func TestDropDisabledField(t *testing.T) {
	requireDualStack := api.RequireDualStack
	preferDualStack := api.PreferDualStack
	singleStack := api.SingleStack

	testCases := []struct {
		name            string
		enableDualStack bool
		svc             *api.Service
		oldSvc          *api.Service
		compareSvc      *api.Service
	}{
		{
			name:            "not dual stack, field not used",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, nil),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, nil),
		},
		{
			name:            "not dual stack, field used in new, not in old",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies([]api.IPFamily{api.IPv4Protocol}, nil),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies([]api.IPFamily{api.IPv4Protocol}, nil),
		},
		{
			name:            "not dual stack, field used in old and new",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies([]api.IPFamily{api.IPv4Protocol}, nil),
			oldSvc:          makeServiceWithIPFamilies([]api.IPFamily{api.IPv4Protocol}, nil),
			compareSvc:      makeServiceWithIPFamilies([]api.IPFamily{api.IPv4Protocol}, nil),
		},
		{
			name:            "dualstack, field used",
			enableDualStack: true,
			svc:             makeServiceWithIPFamilies([]api.IPFamily{api.IPv6Protocol}, nil),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies([]api.IPFamily{api.IPv6Protocol}, nil),
		},
		/* preferDualStack field */
		{
			name:            "not dual stack, fields is not use",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, nil),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, nil),
		},
		{
			name:            "not dual stack, fields used in new, not in old",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, &singleStack),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, &singleStack),
		},
		{
			name:            "not dual stack, fields used in new, not in old",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, &preferDualStack),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, &preferDualStack),
		},
		{
			name:            "not dual stack, fields used in new, not in old",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, &requireDualStack),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, &requireDualStack),
		},

		{
			name:            "not dual stack, fields used in old and new",
			enableDualStack: false,
			svc:             makeServiceWithIPFamilies(nil, &singleStack),
			oldSvc:          makeServiceWithIPFamilies(nil, &singleStack),
			compareSvc:      makeServiceWithIPFamilies(nil, &singleStack),
		},
		{
			name:            "dualstack, field used",
			enableDualStack: true,
			svc:             makeServiceWithIPFamilies(nil, &singleStack),
			oldSvc:          nil,
			compareSvc:      makeServiceWithIPFamilies(nil, &singleStack),
		},

		/* add more tests for other dropped fields as needed */
	}
	for _, tc := range testCases {
		func() {
			defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, tc.enableDualStack)()
			old := tc.oldSvc.DeepCopy()
			dropServiceDisabledFields(tc.svc, tc.oldSvc)

			// old node  should never be changed
			if !reflect.DeepEqual(tc.oldSvc, old) {
				t.Errorf("%v: old svc changed: %v", tc.name, diff.ObjectReflectDiff(tc.oldSvc, old))
			}

			if !reflect.DeepEqual(tc.svc, tc.compareSvc) {
				t.Errorf("%v: unexpected svc spec: %v", tc.name, diff.ObjectReflectDiff(tc.svc, tc.compareSvc))
			}
		}()
	}

}

func TestClearClusterIPRelatedFields(t *testing.T) {
	singleStack := api.SingleStack
	requireDualStack := api.RequireDualStack
	testCases := []struct {
		name        string
		oldService  *api.Service
		newService  *api.Service
		shouldClear bool
	}{
		{
			name:        "should clear, single stack converting to external name",
			shouldClear: true,

			oldService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{"10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
			newService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeExternalName,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{""},
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
		},

		{
			name:        "should clear, dual stack converting to external name(user correctly removed all ips)",
			shouldClear: true,

			oldService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					IPFamilyPolicy: &requireDualStack,
					ClusterIPs:     []string{"2000::1", "10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv6Protocol, api.IPv4Protocol},
				},
			},
			newService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeExternalName,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     nil,
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
		},

		{
			name:        "should NOT clear, single stack converting to external name ClusterIPs was not cleared",
			shouldClear: false,

			oldService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{"2000::1"},
					IPFamilies:     []api.IPFamily{api.IPv6Protocol},
				},
			},
			newService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeExternalName,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{"2000::1"},
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
		},

		{
			name:        "should NOT clear, dual stack converting to external name but user just removed 1st IP",
			shouldClear: false,

			oldService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					IPFamilyPolicy: &requireDualStack,
					ClusterIPs:     []string{"2000::1", "10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv6Protocol, api.IPv4Protocol},
				},
			},
			newService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeExternalName,
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{"", "10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
		},

		{
			name:        "should NOT clear, dualstack service changing selector",
			shouldClear: false,

			oldService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					Selector:       map[string]string{"foo": "bar"},
					IPFamilyPolicy: &requireDualStack,
					ClusterIPs:     []string{"2000::1", "10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv6Protocol, api.IPv4Protocol},
				},
			},
			newService: &api.Service{
				Spec: api.ServiceSpec{
					Type:           api.ServiceTypeClusterIP,
					Selector:       map[string]string{"foo": "baz"},
					IPFamilyPolicy: &singleStack,
					ClusterIPs:     []string{"2000::1", "10.0.0.4"},
					IPFamilies:     []api.IPFamily{api.IPv4Protocol},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clearClusterIPRelatedFields(testCase.newService, testCase.oldService)

			if testCase.shouldClear && len(testCase.newService.Spec.ClusterIPs) != 0 {
				t.Fatalf("expected clusterIPs to be cleared")
			}

			if testCase.shouldClear && len(testCase.newService.Spec.IPFamilies) != 0 {
				t.Fatalf("expected ipfamilies to be cleared")
			}

			if testCase.shouldClear && testCase.newService.Spec.IPFamilyPolicy != nil {
				t.Fatalf("expected ipfamilypolicy to be cleared")
			}

			if !testCase.shouldClear && len(testCase.newService.Spec.ClusterIPs) == 0 {
				t.Fatalf("expected clusterIPs NOT to be cleared")
			}

			if !testCase.shouldClear && len(testCase.newService.Spec.IPFamilies) == 0 {
				t.Fatalf("expected ipfamilies NOT to be cleared")
			}

			if !testCase.shouldClear && testCase.newService.Spec.IPFamilyPolicy == nil {
				t.Fatalf("expected ipfamilypolicy NOT to be cleared")
			}

		})
	}

}
