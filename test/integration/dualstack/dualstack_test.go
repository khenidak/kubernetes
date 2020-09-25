/*
Copyright 2020 The Kubernetes Authors.

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

package dualstack

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/test/integration/framework"
	netutils "k8s.io/utils/net"
)

// TestCreateServiceSingleStackIPv4 test the Service dualstackness in an IPv4 SingleStack cluster
func TestCreateServiceSingleStackIPv4(t *testing.T) {
	// Create an IPv4 single stack control-plane
	serviceCIDR := "10.0.0.0/16"
	defaultIPFamily := v1.IPv4Protocol
	dualStack := false
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr
	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create the service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}
			// if no IPFamily specified it must use the default (first cidr in service subnet)
			ipFamilies := tc.ipFamilies
			if len(ipFamilies) == 0 {
				ipFamilies = []v1.IPFamily{defaultIPFamily}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// TestCreateServiceSingleStackIPv6 test the Service dualstackness in an IPv6 SingleStack cluster
func TestCreateServiceSingleStackIPv6(t *testing.T) {
	// Create an IPv6 single stack control-plane
	serviceCIDR := "2001:db8:1::/48"
	defaultIPFamily := v1.IPv6Protocol
	dualStack := false
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr
	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create the service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}
			// if no IPFamily specified it must use the default (first cidr in service subnet)
			ipFamilies := tc.ipFamilies
			if len(ipFamilies) == 0 {
				ipFamilies = []v1.IPFamily{defaultIPFamily}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// TestCreateServiceDualStackIPv4Only test the Service dualstackness in an IPv4 only DualStack cluster
func TestCreateServiceDualStackIPv4Only(t *testing.T) {
	// Create an IPv4 only dual stack control-plane
	serviceCIDR := "10.0.0.0/16"
	defaultIPFamily := v1.IPv4Protocol
	dualStack := true
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr
	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create the service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}
			// if no IPFamily specified it must use the default (first cidr in service subnet)
			ipFamilies := tc.ipFamilies
			if len(ipFamilies) == 0 {
				ipFamilies = []v1.IPFamily{defaultIPFamily}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// TestCreateServiceDualStackIPv6Only test the Service dualstackness in an IPv6 only DualStack cluster
func TestCreateServiceDualStackIPv6Only(t *testing.T) {
	// Create an IPv6 only dual stack control-plane
	serviceCIDR := "2001:db8:1::/48"
	defaultIPFamily := v1.IPv6Protocol
	dualStack := false
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr
	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    true,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create the service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}
			// if no IPFamily specified it must use the default (first cidr in service subnet)
			ipFamilies := tc.ipFamilies
			if len(ipFamilies) == 0 {
				ipFamilies = []v1.IPFamily{defaultIPFamily}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// TestCreateServiceDualStackIPv4IPv6 test the Service dualstackness in a IPv4IPv6 DualStack cluster
func TestCreateServiceDualStackIPv4IPv6(t *testing.T) {
	// Create an IPv4IPv6 dual stack control-plane
	serviceCIDR := "10.0.0.0/16"
	secondaryServiceCIDR := "2001:db8:1::/48"
	defaultIPFamily := []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol}
	dualStack := true
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr

	_, secCidr, err := net.ParseCIDR(secondaryServiceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.SecondaryServiceIPRange = *secCidr

	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create a service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}

			ipFamilies := tc.ipFamilies
			// if no IPFamily specified it must use the defaults (first cidrs in service subnet)
			if (tc.ipFamilyPolicy == v1.SingleStack) && (len(ipFamilies) == 0) {
				ipFamilies = defaultIPFamily[:1]
				// if no IPFamily specified it must use the defaults
			} else if (tc.ipFamilyPolicy != v1.SingleStack) && (len(ipFamilies) == 0) {
				ipFamilies = defaultIPFamily
				// if only one IPFamily specified but dual stack add the missing one
			} else if (tc.ipFamilyPolicy != v1.SingleStack) && (len(ipFamilies) == 1) {
				if ipFamilies[0] == v1.IPv4Protocol {
					ipFamilies = append(ipFamilies, v1.IPv6Protocol)
				} else {
					ipFamilies = append(ipFamilies, v1.IPv4Protocol)
				}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// TestCreateServiceDualStackIPv6IPv4 test the Service dualstackness in a IPv6IPv4 DualStack cluster
func TestCreateServiceDualStackIPv6IPv4(t *testing.T) {
	// Create an IPv6IPv4 dual stack control-plane
	serviceCIDR := "2001:db8:1::/48"
	secondaryServiceCIDR := "10.0.0.0/16"
	defaultIPFamily := []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol}
	dualStack := true
	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.IPv6DualStack, dualStack)()

	cfg := framework.NewIntegrationTestMasterConfig()
	_, cidr, err := net.ParseCIDR(serviceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.ServiceIPRange = *cidr

	_, secCidr, err := net.ParseCIDR(secondaryServiceCIDR)
	if err != nil {
		t.Fatalf("bad cidr: %v", err)
	}
	cfg.ExtraConfig.SecondaryServiceIPRange = *secCidr

	_, s, closeFn := framework.RunAMaster(cfg)
	defer closeFn()

	client := clientset.NewForConfigOrDie(&restclient.Config{Host: s.URL})

	// Wait until the default "kubernetes" service is created.
	if err = wait.Poll(250*time.Millisecond, time.Minute, func() (bool, error) {
		_, err := client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		return !apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatalf("creating kubernetes service timed out")
	}

	// verify client is working
	if err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		_, err = client.CoreV1().Endpoints("default").Get(context.TODO(), "kubernetes", metav1.GetOptions{})
		if err != nil {
			t.Logf("error fetching endpoints: %v", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("server without enabled endpoints failed to register: %v", err)
	}

	var testcases = []struct {
		name           string
		serviceType    v1.ServiceType
		clusterIPs     []string
		ipFamilies     []v1.IPFamily
		ipFamilyPolicy v1.IPFamilyPolicyType
		expectError    bool
	}{
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - Default IP Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv4 IPv6 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Single Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.SingleStack,
			expectError:    true,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Prefer Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.PreferDualStack,
			expectError:    false,
		},
		{
			name:           "Type ClusterIP - Server Allocated IP - IPv6 IPv4 Family - Policy Required Dual Stack",
			serviceType:    v1.ServiceTypeClusterIP,
			clusterIPs:     []string{},
			ipFamilies:     []v1.IPFamily{v1.IPv6Protocol, v1.IPv4Protocol},
			ipFamilyPolicy: v1.RequireDualStack,
			expectError:    false,
		},
	}

	for i, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {

			svc := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("svc-test-%d", i), // use different services for each test
				},
				Spec: v1.ServiceSpec{
					Type:           tc.serviceType,
					ClusterIPs:     tc.clusterIPs,
					IPFamilies:     tc.ipFamilies,
					IPFamilyPolicy: &tc.ipFamilyPolicy,
					Ports: []v1.ServicePort{
						{
							Name:       fmt.Sprintf("port-test-%d", i),
							Port:       443,
							TargetPort: intstr.IntOrString{IntVal: 443},
							Protocol:   "TCP",
						},
					},
				},
			}

			// create a service
			_, err = client.CoreV1().Services(metav1.NamespaceDefault).Create(context.TODO(), svc, metav1.CreateOptions{})
			if (err != nil) != tc.expectError {
				t.Errorf("Test failed expected result: %v received %v ", tc.expectError, err)
			}
			// if no error was expected validate the service otherwise return
			if err != nil {
				return
			}
			// validate the service was created correctly if it was not expected to fail
			svc, err = client.CoreV1().Services(metav1.NamespaceDefault).Get(context.TODO(), svc.Name, metav1.GetOptions{})
			if err != nil {
				t.Errorf("Unexpected error to get the service %s %v", svc.Name, err)
			}

			ipFamilies := tc.ipFamilies
			// if no IPFamily specified it must use the defaults (first cidrs in service subnet)
			if (tc.ipFamilyPolicy == v1.SingleStack) && (len(ipFamilies) == 0) {
				ipFamilies = defaultIPFamily[:1]
				// if no IPFamily specified it must use the defaults
			} else if (tc.ipFamilyPolicy != v1.SingleStack) && (len(ipFamilies) == 0) {
				ipFamilies = defaultIPFamily
				// if only one IPFamily specified but dual stack add the missing one
			} else if (tc.ipFamilyPolicy != v1.SingleStack) && (len(ipFamilies) == 1) {
				if ipFamilies[0] == v1.IPv4Protocol {
					ipFamilies = append(ipFamilies, v1.IPv6Protocol)
				} else {
					ipFamilies = append(ipFamilies, v1.IPv4Protocol)
				}
			}
			if err := validateServiceAndClusterIPFamily(svc, ipFamilies); err != nil {
				t.Errorf("Unexpected error validating the service %s %v", svc.Name, err)
			}
		})
	}
}

// helper functions obtained from test/e2e/network/dual_stack.go

// validateServiceAndClusterIPFamily checks that the service has the expected IPFamilies
func validateServiceAndClusterIPFamily(svc *v1.Service, expectedIPFamilies []v1.IPFamily) error {
	// create a slice for the errors
	var errstrings []string

	if svc.Spec.IPFamilies == nil {
		return fmt.Errorf("service ip family nil for service %s/%s", svc.Namespace, svc.Name)
	}
	if !reflect.DeepEqual(svc.Spec.IPFamilies, expectedIPFamilies) {
		return fmt.Errorf("ip families mismatch for service: %s/%s, expected: %s, actual: %s", svc.Namespace, svc.Name, expectedIPFamilies, svc.Spec.IPFamilies)
	}

	for j, ip := range svc.Spec.ClusterIPs {
		if ip == v1.ClusterIPNone {
			// ???
		}

		// the clusterIP assigned should have the same IPFamily requested
		if netutils.IsIPv6String(ip) != (expectedIPFamilies[j] == v1.IPv6Protocol) {
			errstrings = append(errstrings, fmt.Sprintf("got unexpected service ip %s, should belong to %s ip family", ip, expectedIPFamilies[j]))
		}
	}

	if len(errstrings) > 0 {
		errstrings = append(errstrings, fmt.Sprintf("Error validating Service: %s, ClusterIPs: %v Expected IPFamilies %v", svc.Name, svc.Spec.ClusterIPs, expectedIPFamilies))
		return fmt.Errorf(strings.Join(errstrings, "\n"))
	}

	return nil
}
