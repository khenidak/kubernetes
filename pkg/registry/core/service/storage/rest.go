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

package storage

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/util/dryrun"
	"k8s.io/klog/v2"

	apiservice "k8s.io/kubernetes/pkg/api/service"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	registry "k8s.io/kubernetes/pkg/registry/core/service"
	"k8s.io/kubernetes/pkg/registry/core/service/ipallocator"
	"k8s.io/kubernetes/pkg/registry/core/service/portallocator"
	netutil "k8s.io/utils/net"
)

// REST adapts a service registry into apiserver's RESTStorage model.
type REST struct {
	strategy                    rest.RESTCreateUpdateStrategy
	services                    ServiceStorage
	endpoints                   EndpointsStorage
	serviceIPAllocatorsByFamily map[api.IPFamily]ipallocator.Interface
	defaultServiceIPFamily      api.IPFamily // --service-cluster-ip-range[0]
	serviceNodePorts            portallocator.Interface
	proxyTransport              http.RoundTripper
	pods                        rest.Getter
}

// ServiceNodePort includes protocol and port number of a service NodePort.
type ServiceNodePort struct {
	// The IP protocol for this port. Supports "TCP" and "UDP".
	Protocol api.Protocol

	// The port on each node on which this service is exposed.
	// Default is to auto-allocate a port if the ServiceType of this Service requires one.
	NodePort int32
}

type ServiceStorage interface {
	rest.Scoper
	rest.Getter
	rest.Lister
	rest.CreaterUpdater
	rest.GracefulDeleter
	rest.Watcher
	rest.Exporter
	rest.StorageVersionProvider
}

type EndpointsStorage interface {
	rest.Getter
	rest.GracefulDeleter
}

// NewREST returns a wrapper around the underlying generic storage and performs
// allocations and deallocations of various service related resources like ports.
// TODO: all transactional behavior should be supported from within generic storage
//   or the strategy.
func NewREST(
	services ServiceStorage,
	endpoints EndpointsStorage,
	pods rest.Getter,
	serviceIPs ipallocator.Interface,
	secondaryServiceIPs ipallocator.Interface,
	serviceNodePorts portallocator.Interface,
	proxyTransport http.RoundTripper,
) (*REST, *registry.ProxyREST) {

	strategy, _ := registry.StrategyForServiceCIDRs(serviceIPs.CIDR(), secondaryServiceIPs != nil)

	byIPFamily := make(map[api.IPFamily]ipallocator.Interface)

	// detect this cluster default Service IPFamily (ipfamily of --service-cluster-ip-range[0])
	serviceIPFamily := api.IPv4Protocol
	cidr := serviceIPs.CIDR()
	if netutil.IsIPv6CIDR(&cidr) {
		serviceIPFamily = api.IPv6Protocol
	}

	// add primary family
	byIPFamily[serviceIPFamily] = serviceIPs

	if secondaryServiceIPs != nil {
		// process secondary family
		secondaryServiceIPFamily := api.IPv6Protocol

		// get family of secondary
		if serviceIPFamily == api.IPv6Protocol {
			secondaryServiceIPFamily = api.IPv4Protocol
		}
		// add it
		byIPFamily[secondaryServiceIPFamily] = secondaryServiceIPs
	}

	klog.V(0).Infof("the default service ipfamily for this cluster is: %s", string(serviceIPFamily))

	rest := &REST{
		strategy:                    strategy,
		services:                    services,
		endpoints:                   endpoints,
		serviceIPAllocatorsByFamily: byIPFamily,
		serviceNodePorts:            serviceNodePorts,
		defaultServiceIPFamily:      serviceIPFamily,
		proxyTransport:              proxyTransport,
		pods:                        pods,
	}

	return rest, &registry.ProxyREST{Redirector: rest, ProxyTransport: proxyTransport}
}

var (
	_ ServiceStorage              = &REST{}
	_ rest.CategoriesProvider     = &REST{}
	_ rest.ShortNamesProvider     = &REST{}
	_ rest.StorageVersionProvider = &REST{}
)

func (rs *REST) StorageVersion() runtime.GroupVersioner {
	return rs.services.StorageVersion()
}

// ShortNames implements the ShortNamesProvider interface. Returns a list of short names for a resource.
func (rs *REST) ShortNames() []string {
	return []string{"svc"}
}

// Categories implements the CategoriesProvider interface. Returns a list of categories a resource is part of.
func (rs *REST) Categories() []string {
	return []string{"all"}
}

func (rs *REST) NamespaceScoped() bool {
	return rs.services.NamespaceScoped()
}

func (rs *REST) New() runtime.Object {
	return rs.services.New()
}

func (rs *REST) NewList() runtime.Object {
	return rs.services.NewList()
}

func (rs *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	obj, err := rs.services.Get(ctx, name, options)
	if err != nil {
		return obj, err
	}
	// default on read for pre-existing services
	svc := obj.(*api.Service)

	return svc, nil
}

func (rs *REST) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	return rs.services.List(ctx, options)
}

func (rs *REST) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	return rs.services.Watch(ctx, options)
}

func (rs *REST) Export(ctx context.Context, name string, opts metav1.ExportOptions) (runtime.Object, error) {
	return rs.services.Export(ctx, name, opts)
}

func (rs *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	service := obj.(*api.Service)

	// bag of clusterIPs allocated in the process of creation
	// failed allocation will automatically trigger release
	var toReleaseclusterIPs map[api.IPFamily]string

	if err := rest.BeforeCreate(rs.strategy, ctx, obj); err != nil {
		return nil, err
	}

	// TODO: this should probably move to strategy.PrepareForCreate()
	defer func() {
		released, err := rs.releaseClusterIPs(toReleaseclusterIPs)
		if err != nil {
			klog.Infof("failed to release clusterIPs for failed new service:%v allocated:%v released:%v error:%v",
				service.Name, toReleaseclusterIPs, released, err)
		}
	}()

	// try set ip families (for missing ip families)
	// we do it here, since we want this to be visible
	// even when dryRun == true
	if err := rs.tryDefaultValidateServiceClusterIPFields(service); err != nil {
		return nil, err
	}

	var err error
	if !dryrun.IsDryRun(options.DryRun) {
		toReleaseclusterIPs, err = rs.allocServiceClusterIPs(service)
		if err != nil {
			return nil, err
		}
	}

	nodePortOp := portallocator.StartOperation(rs.serviceNodePorts, dryrun.IsDryRun(options.DryRun))
	defer nodePortOp.Finish()

	if service.Spec.Type == api.ServiceTypeNodePort || service.Spec.Type == api.ServiceTypeLoadBalancer {
		if err := initNodePorts(service, nodePortOp); err != nil {
			return nil, err
		}
	}

	// Handle ExternalTraffic related fields during service creation.
	if apiservice.NeedsHealthCheck(service) {
		if err := allocateHealthCheckNodePort(service, nodePortOp); err != nil {
			return nil, errors.NewInternalError(err)
		}
	}
	if errs := validation.ValidateServiceExternalTrafficFieldsCombination(service); len(errs) > 0 {
		return nil, errors.NewInvalid(api.Kind("Service"), service.Name, errs)
	}

	out, err := rs.services.Create(ctx, service, createValidation, options)
	if err != nil {
		err = rest.CheckGeneratedNameError(rs.strategy, err, service)
	}

	if err == nil {
		el := nodePortOp.Commit()
		if el != nil {
			// these should be caught by an eventual reconciliation / restart
			utilruntime.HandleError(fmt.Errorf("error(s) committing service node-ports changes: %v", el))
		}

		// no clusterips to release
		toReleaseclusterIPs = nil
	}

	return out, err
}

func (rs *REST) Delete(ctx context.Context, id string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	// TODO: handle graceful
	obj, _, err := rs.services.Delete(ctx, id, deleteValidation, options)
	if err != nil {
		return nil, false, err
	}

	svc := obj.(*api.Service)

	// Only perform the cleanup if this is a non-dryrun deletion
	if !dryrun.IsDryRun(options.DryRun) {
		// TODO: can leave dangling endpoints, and potentially return incorrect
		// endpoints if a new service is created with the same name
		_, _, err = rs.endpoints.Delete(ctx, id, rest.ValidateAllObjectFunc, &metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return nil, false, err
		}

		rs.releaseAllocatedResources(svc)
	}

	// TODO: this is duplicated from the generic storage, when this wrapper is fully removed we can drop this
	details := &metav1.StatusDetails{
		Name: svc.Name,
		UID:  svc.UID,
	}
	if info, ok := genericapirequest.RequestInfoFrom(ctx); ok {
		details.Group = info.APIGroup
		details.Kind = info.Resource // legacy behavior
	}
	status := &metav1.Status{Status: metav1.StatusSuccess, Details: details}
	return status, true, nil
}

func (rs *REST) releaseAllocatedResources(svc *api.Service) {
	rs.releaseServiceClusterIPs(svc)

	for _, nodePort := range collectServiceNodePorts(svc) {
		err := rs.serviceNodePorts.Release(nodePort)
		if err != nil {
			// these should be caught by an eventual reconciliation / restart
			utilruntime.HandleError(fmt.Errorf("Error releasing service %s node port %d: %v", svc.Name, nodePort, err))
		}
	}

	if apiservice.NeedsHealthCheck(svc) {
		nodePort := svc.Spec.HealthCheckNodePort
		if nodePort > 0 {
			err := rs.serviceNodePorts.Release(int(nodePort))
			if err != nil {
				// these should be caught by an eventual reconciliation / restart
				utilruntime.HandleError(fmt.Errorf("Error releasing service %s health check node port %d: %v", svc.Name, nodePort, err))
			}
		}
	}
}

// externalTrafficPolicyUpdate adjusts ExternalTrafficPolicy during service update if needed.
// It is necessary because we default ExternalTrafficPolicy field to different values.
// (NodePort / LoadBalancer: default is Global; Other types: default is empty.)
func externalTrafficPolicyUpdate(oldService, service *api.Service) {
	var neededExternalTraffic, needsExternalTraffic bool
	if oldService.Spec.Type == api.ServiceTypeNodePort ||
		oldService.Spec.Type == api.ServiceTypeLoadBalancer {
		neededExternalTraffic = true
	}
	if service.Spec.Type == api.ServiceTypeNodePort ||
		service.Spec.Type == api.ServiceTypeLoadBalancer {
		needsExternalTraffic = true
	}
	if neededExternalTraffic && !needsExternalTraffic {
		// Clear ExternalTrafficPolicy to prevent confusion from ineffective field.
		service.Spec.ExternalTrafficPolicy = api.ServiceExternalTrafficPolicyType("")
	}
}

// healthCheckNodePortUpdate handles HealthCheckNodePort allocation/release
// and adjusts HealthCheckNodePort during service update if needed.
func (rs *REST) healthCheckNodePortUpdate(oldService, service *api.Service, nodePortOp *portallocator.PortAllocationOperation) (bool, error) {
	neededHealthCheckNodePort := apiservice.NeedsHealthCheck(oldService)
	oldHealthCheckNodePort := oldService.Spec.HealthCheckNodePort

	needsHealthCheckNodePort := apiservice.NeedsHealthCheck(service)
	newHealthCheckNodePort := service.Spec.HealthCheckNodePort

	switch {
	// Case 1: Transition from don't need HealthCheckNodePort to needs HealthCheckNodePort.
	// Allocate a health check node port or attempt to reserve the user-specified one if provided.
	// Insert health check node port into the service's HealthCheckNodePort field if needed.
	case !neededHealthCheckNodePort && needsHealthCheckNodePort:
		klog.Infof("Transition to LoadBalancer type service with ExternalTrafficPolicy=Local")
		if err := allocateHealthCheckNodePort(service, nodePortOp); err != nil {
			return false, errors.NewInternalError(err)
		}

	// Case 2: Transition from needs HealthCheckNodePort to don't need HealthCheckNodePort.
	// Free the existing healthCheckNodePort and clear the HealthCheckNodePort field.
	case neededHealthCheckNodePort && !needsHealthCheckNodePort:
		klog.Infof("Transition to non LoadBalancer type service or LoadBalancer type service with ExternalTrafficPolicy=Global")
		klog.V(4).Infof("Releasing healthCheckNodePort: %d", oldHealthCheckNodePort)
		nodePortOp.ReleaseDeferred(int(oldHealthCheckNodePort))
		// Clear the HealthCheckNodePort field.
		service.Spec.HealthCheckNodePort = 0

	// Case 3: Remain in needs HealthCheckNodePort.
	// Reject changing the value of the HealthCheckNodePort field.
	case neededHealthCheckNodePort && needsHealthCheckNodePort:
		if oldHealthCheckNodePort != newHealthCheckNodePort {
			klog.Warningf("Attempt to change value of health check node port DENIED")
			fldPath := field.NewPath("spec", "healthCheckNodePort")
			el := field.ErrorList{field.Invalid(fldPath, newHealthCheckNodePort,
				"cannot change healthCheckNodePort on loadBalancer service with externalTraffic=Local during update")}
			return false, errors.NewInvalid(api.Kind("Service"), service.Name, el)
		}
	}
	return true, nil
}

func (rs *REST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	oldObj, err := rs.services.Get(ctx, name, &metav1.GetOptions{})
	if err != nil {
		// Support create on update, if forced to.
		if forceAllowCreate {
			obj, err := objInfo.UpdatedObject(ctx, nil)
			if err != nil {
				return nil, false, err
			}
			createdObj, err := rs.Create(ctx, obj, createValidation, &metav1.CreateOptions{DryRun: options.DryRun})
			if err != nil {
				return nil, false, err
			}
			return createdObj, true, nil
		}
		return nil, false, err
	}
	oldService := oldObj.(*api.Service)
	obj, err := objInfo.UpdatedObject(ctx, oldService)
	if err != nil {
		return nil, false, err
	}

	service := obj.(*api.Service)

	if !rest.ValidNamespace(ctx, &service.ObjectMeta) {
		return nil, false, errors.NewConflict(api.Resource("services"), service.Namespace, fmt.Errorf("Service.Namespace does not match the provided context"))
	}

	// Copy over non-user fields
	if err := rest.BeforeUpdate(rs.strategy, ctx, service, oldService); err != nil {
		return nil, false, err
	}

	// TODO: this should probably move to strategy.PrepareForCreate()
	var toReleaseClusterIPs map[api.IPFamily]string
	var toAllocClusterIPs map[api.IPFamily]string

	// failed allocation release
	defer func() {
		// release the allocate .. allocate the released
		released, errRelease := rs.releaseClusterIPs(toReleaseClusterIPs)
		allocated, errAllocate := rs.allocClusterIPs(oldService, toAllocClusterIPs)
		if errRelease != nil || errAllocate != nil {
			klog.V(4).Infof("service %v/%v failed to clean up after failed service update error:%v. Allocated/NotReleased:%v/%v Released/NotReallocated:%v/%v",
				service.Namespace, service.Name, err, toReleaseClusterIPs, released, toAllocClusterIPs, allocated)
		}
	}()

	nodePortOp := portallocator.StartOperation(rs.serviceNodePorts, dryrun.IsDryRun(options.DryRun))
	defer nodePortOp.Finish()

	// try set ip families (for missing ip families)
	if err := rs.tryDefaultValidateServiceClusterIPFields(service); err != nil {
		return nil, false, err
	}

	if !dryrun.IsDryRun(options.DryRun) {
		toReleaseClusterIPs, toAllocClusterIPs, err = rs.handleClusterIPsForUpdatedService(oldService, service)
		if err != nil {
			return nil, false, err
		}
	}
	// Update service from NodePort or LoadBalancer to ExternalName or ClusterIP, should release NodePort if exists.
	if (oldService.Spec.Type == api.ServiceTypeNodePort || oldService.Spec.Type == api.ServiceTypeLoadBalancer) &&
		(service.Spec.Type == api.ServiceTypeExternalName || service.Spec.Type == api.ServiceTypeClusterIP) {
		releaseNodePorts(oldService, nodePortOp)
	}
	// Update service from any type to NodePort or LoadBalancer, should update NodePort.
	if service.Spec.Type == api.ServiceTypeNodePort || service.Spec.Type == api.ServiceTypeLoadBalancer {
		if err := updateNodePorts(oldService, service, nodePortOp); err != nil {
			return nil, false, err
		}
	}
	// Update service from LoadBalancer to non-LoadBalancer, should remove any LoadBalancerStatus.
	if service.Spec.Type != api.ServiceTypeLoadBalancer {
		// Although loadbalancer delete is actually asynchronous, we don't need to expose the user to that complexity.
		service.Status.LoadBalancer = api.LoadBalancerStatus{}
	}

	// Handle ExternalTraffic related updates.
	success, err := rs.healthCheckNodePortUpdate(oldService, service, nodePortOp)
	if !success || err != nil {
		return nil, false, err
	}
	externalTrafficPolicyUpdate(oldService, service)
	if errs := validation.ValidateServiceExternalTrafficFieldsCombination(service); len(errs) > 0 {
		return nil, false, errors.NewInvalid(api.Kind("Service"), service.Name, errs)
	}

	out, created, err := rs.services.Update(ctx, service.Name, rest.DefaultUpdatedObjectInfo(service), createValidation, updateValidation, forceAllowCreate, options)
	if err == nil {
		el := nodePortOp.Commit()
		if el != nil {
			// problems should be fixed by an eventual reconciliation / restart
			utilruntime.HandleError(fmt.Errorf("error(s) committing NodePorts changes: %v", el))
		}
		// all good, nothing to realloc, or release
		toReleaseClusterIPs = nil
		toAllocClusterIPs = nil
	}

	return out, created, err
}

// Implement Redirector.
var _ = rest.Redirector(&REST{})

// ResourceLocation returns a URL to which one can send traffic for the specified service.
func (rs *REST) ResourceLocation(ctx context.Context, id string) (*url.URL, http.RoundTripper, error) {
	// Allow ID as "svcname", "svcname:port", or "scheme:svcname:port".
	svcScheme, svcName, portStr, valid := utilnet.SplitSchemeNamePort(id)
	if !valid {
		return nil, nil, errors.NewBadRequest(fmt.Sprintf("invalid service request %q", id))
	}

	// If a port *number* was specified, find the corresponding service port name
	if portNum, err := strconv.ParseInt(portStr, 10, 64); err == nil {
		obj, err := rs.services.Get(ctx, svcName, &metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		svc := obj.(*api.Service)
		found := false
		for _, svcPort := range svc.Spec.Ports {
			if int64(svcPort.Port) == portNum {
				// use the declared port's name
				portStr = svcPort.Name
				found = true
				break
			}
		}
		if !found {
			return nil, nil, errors.NewServiceUnavailable(fmt.Sprintf("no service port %d found for service %q", portNum, svcName))
		}
	}

	obj, err := rs.endpoints.Get(ctx, svcName, &metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	eps := obj.(*api.Endpoints)
	if len(eps.Subsets) == 0 {
		return nil, nil, errors.NewServiceUnavailable(fmt.Sprintf("no endpoints available for service %q", svcName))
	}
	// Pick a random Subset to start searching from.
	ssSeed := rand.Intn(len(eps.Subsets))
	// Find a Subset that has the port.
	for ssi := 0; ssi < len(eps.Subsets); ssi++ {
		ss := &eps.Subsets[(ssSeed+ssi)%len(eps.Subsets)]
		if len(ss.Addresses) == 0 {
			continue
		}
		for i := range ss.Ports {
			if ss.Ports[i].Name == portStr {
				addrSeed := rand.Intn(len(ss.Addresses))
				// This is a little wonky, but it's expensive to test for the presence of a Pod
				// So we repeatedly try at random and validate it, this means that for an invalid
				// service with a lot of endpoints we're going to potentially make a lot of calls,
				// but in the expected case we'll only make one.
				for try := 0; try < len(ss.Addresses); try++ {
					addr := ss.Addresses[(addrSeed+try)%len(ss.Addresses)]
					if err := isValidAddress(ctx, &addr, rs.pods); err != nil {
						utilruntime.HandleError(fmt.Errorf("Address %v isn't valid (%v)", addr, err))
						continue
					}
					ip := addr.IP
					port := int(ss.Ports[i].Port)
					return &url.URL{
						Scheme: svcScheme,
						Host:   net.JoinHostPort(ip, strconv.Itoa(port)),
					}, rs.proxyTransport, nil
				}
				utilruntime.HandleError(fmt.Errorf("Failed to find a valid address, skipping subset: %v", ss))
			}
		}
	}
	return nil, nil, errors.NewServiceUnavailable(fmt.Sprintf("no endpoints available for service %q", id))
}

func (r *REST) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return r.services.ConvertToTable(ctx, object, tableOptions)
}

func (rs *REST) allocClusterIPs(service *api.Service, toAlloc map[api.IPFamily]string) (map[api.IPFamily]string, error) {
	allocated := make(map[api.IPFamily]string)

	for family, ip := range toAlloc {
		allocator := rs.serviceIPAllocatorsByFamily[family] // should always be there, as we pre validate
		if ip == "" {
			allocatedIP, err := allocator.AllocateNext()
			if err != nil {
				return allocated, errors.NewInternalError(fmt.Errorf("failed to allocate a serviceIP: %v", err))
			}
			allocated[family] = allocatedIP.String()
		} else {
			parsedIP := net.ParseIP(ip)
			if err := allocator.Allocate(parsedIP); err != nil {
				// TODO: when validation becomes versioned, this gets more complicated.
				el := field.ErrorList{field.Invalid(field.NewPath("spec", "clusterIPs"), service.Spec.ClusterIPs, fmt.Sprintf("failed to allocated ip:%v with error:%v", ip, err))}
				return allocated, errors.NewInvalid(api.Kind("Service"), service.Name, el)
			}
			allocated[family] = ip
		}
	}
	return allocated, nil
}

// releases clusterIPs per family
func (rs *REST) releaseClusterIPs(toRelease map[api.IPFamily]string) (map[api.IPFamily]string, error) {
	if toRelease == nil {
		return nil, nil
	}

	released := make(map[api.IPFamily]string)
	for family, ip := range toRelease {
		allocator, ok := rs.serviceIPAllocatorsByFamily[family]
		if !ok {
			// cluster was configured for dual stack, then single stack
			continue
		}

		parsedIP := net.ParseIP(ip)
		if err := allocator.Release(parsedIP); err != nil {
			return released, err
		}
		released[family] = ip
	}

	return released, nil
}

// allocates ClusterIPs for a service
func (rs *REST) allocServiceClusterIPs(service *api.Service) (map[api.IPFamily]string, error) {
	// external name don't get ClusterIPs
	if service.Spec.Type == api.ServiceTypeExternalName {
		return nil, nil
	}

	// headless don't get ClusterIPs
	if len(service.Spec.ClusterIPs) > 0 && service.Spec.ClusterIPs[0] == api.ClusterIPNone {
		return nil, nil
	}

	toAlloc := make(map[api.IPFamily]string)
	// at this stage, the only fact we know is that service has correct ip families
	// assigned to it. It may have partial assigned ClusterIPs (Upgrade to dual stack)
	// may have no ips at all. The below loop is meant to fix this
	// (we also know that this cluster has these families)

	// if there is no slice to work with
	if service.Spec.ClusterIPs == nil {
		service.Spec.ClusterIPs = make([]string, 0, len(service.Spec.IPFamilies))
	}

	for i, ipFamily := range service.Spec.IPFamilies {
		if i > (len(service.Spec.ClusterIPs) - 1) {
			service.Spec.ClusterIPs = append(service.Spec.ClusterIPs, "" /* just a marker */)
		}

		toAlloc[ipFamily] = service.Spec.ClusterIPs[i]
	}

	// allocate
	allocated, err := rs.allocClusterIPs(service, toAlloc)

	// set if successful
	if err == nil {
		for family, ip := range allocated {
			for i, check := range service.Spec.IPFamilies {
				if family == check {
					service.Spec.ClusterIPs[i] = ip
				}
			}
		}
	}

	return allocated, err
}

// handles upgrade/downgrade change type for an update service
func (rs *REST) handleClusterIPsForUpdatedService(oldService *api.Service, service *api.Service) (allocated map[api.IPFamily]string, released map[api.IPFamily]string, err error) {
	// use cases:
	// A: service changing types from ExternalName TO ClusterIP types ==> allocate all new
	// B: service changing types from ClusterIP types TO ExternalName ==> release all allocated
	// C: Service upgrading to dual stack  ==> partial allocation
	// D: service downgrading from dual stack ==> partial release

	// CASE A:
	// Update service from ExternalName to non-ExternalName, should initialize ClusterIP.
	if oldService.Spec.Type == api.ServiceTypeExternalName && service.Spec.Type != api.ServiceTypeExternalName {
		allocated, err := rs.allocServiceClusterIPs(service)
		return allocated, nil, err
	}

	// CASE B:
	// Update service from non-ExternalName to ExternalName, should release ClusterIP if exists.
	if oldService.Spec.Type != api.ServiceTypeExternalName && service.Spec.Type == api.ServiceTypeExternalName {
		released, err := rs.releaseServiceClusterIPs(oldService)
		return nil, released, err
	}

	// if headless service then we bail out early (no clusterIPs management needed)
	if len(oldService.Spec.ClusterIPs) > 0 && oldService.Spec.ClusterIPs[0] == api.ClusterIPNone {
		return nil, nil, nil
	}

	upgraded := len(oldService.Spec.IPFamilies) == 1 && len(service.Spec.IPFamilies) == 2
	downgraded := len(oldService.Spec.IPFamilies) == 2 && len(service.Spec.IPFamilies) == 1

	// CASE C:
	if upgraded {
		toAllocate := make(map[api.IPFamily]string)
		// if secondary ip was named, just get it. if not add a marker
		if len(service.Spec.ClusterIPs) < 2 {
			service.Spec.ClusterIPs = append(service.Spec.ClusterIPs, "" /* marker */)
		}

		toAllocate[service.Spec.IPFamilies[1]] = service.Spec.ClusterIPs[1]

		// allocate
		allocated, err := rs.allocClusterIPs(service, toAllocate)
		// set if successful
		if err == nil {
			service.Spec.ClusterIPs[1] = allocated[service.Spec.IPFamilies[1]]
		}

		return allocated, nil, err
	}

	// CASE D:
	if downgraded {
		toRelease := make(map[api.IPFamily]string)
		toRelease[oldService.Spec.IPFamilies[1]] = oldService.Spec.ClusterIPs[1]
		released, err := rs.releaseClusterIPs(toRelease)
		return nil, released, err
	}
	// it was not an upgrade nor downgrade
	return nil, nil, nil
}

// releases allocated ClusterIPs for service that is about to be deleted
func (rs *REST) releaseServiceClusterIPs(service *api.Service) (released map[api.IPFamily]string, err error) {
	// external name don't get ClusterIPs
	if service.Spec.Type == api.ServiceTypeExternalName {
		return nil, nil
	}

	// headless don't get ClusterIPs
	if len(service.Spec.ClusterIPs) > 0 && service.Spec.ClusterIPs[0] == api.ClusterIPNone {
		return nil, nil
	}

	toRelease := make(map[api.IPFamily]string)
	for i, ipFamily := range service.Spec.IPFamilies {
		// during release we depend on the fact that on create || update path
		// families + clusterIPs were set correctly
		toRelease[ipFamily] = service.Spec.ClusterIPs[i]
	}
	return rs.releaseClusterIPs(toRelease)
}

// attempts to default service ip families according to cluster configuration
// while ensuring that provided families are configured on cluster.
func (rs *REST) tryDefaultValidateServiceClusterIPFields(service *api.Service) error {
	// can not do anything here
	if service.Spec.Type == api.ServiceTypeExternalName {
		return nil
	}
	// two families or two IPs with Single or PreferDualStack is not allowed
	if service.Spec.IPFamilyPolicy != nil &&
		(len(service.Spec.ClusterIPs) == 2 || len(service.Spec.IPFamilies) == 2) &&
		*(service.Spec.IPFamilyPolicy) != api.RequireDualStack {
		el := field.ErrorList{field.Invalid(field.NewPath("spec", "ipFamilyPolicy"), service.Spec.IPFamilyPolicy, "if provided then it must be set to RequirdDualStack for multi families or multi ClusterIPs")}
		return errors.NewInvalid(api.Kind("Service"), service.Name, el)
	}

	// default families according to cluster IPs
	for i, ip := range service.Spec.ClusterIPs {
		if ip == "" || ip == api.ClusterIPNone {
			break
		}

		// we have previously validated for ip correctness and if family exist it will match ip family
		// so the following is safe to do
		isIPv6 := netutil.IsIPv6String(ip)

		// family is not there.
		if i > len(service.Spec.IPFamilies)-1 {
			if isIPv6 {
				// first make sure that family(ip) is configured
				if _, found := rs.serviceIPAllocatorsByFamily[api.IPv6Protocol]; !found {
					el := field.ErrorList{field.Invalid(field.NewPath("spec", "clusterIPs").Index(i), service.Spec.ClusterIPs, "ClusterIP of IPv6 can not be used, IPv6 is not configuerd on cluster")}
					return errors.NewInvalid(api.Kind("Service"), service.Name, el)
				}
				service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv6Protocol)
			} else {
				// first make sure that family(ip) is configured
				if _, found := rs.serviceIPAllocatorsByFamily[api.IPv4Protocol]; !found {
					el := field.ErrorList{field.Invalid(field.NewPath("spec", "clusterIPs").Index(i), service.Spec.ClusterIPs, "ClusterIP of IPv4 can not be used, IPv4 is not configuerd on cluster")}
					return errors.NewInvalid(api.Kind("Service"), service.Name, el)
				}
				service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv4Protocol)
			}
		}
	}

	// default headless+selectorless
	if len(service.Spec.ClusterIPs) > 0 && service.Spec.ClusterIPs[0] == api.ClusterIPNone && service.Spec.Selector == nil {
		if service.Spec.IPFamilyPolicy == nil {
			requireDualStack := api.RequireDualStack
			service.Spec.IPFamilyPolicy = &requireDualStack
		}

		// if not set by user
		if len(service.Spec.IPFamilies) == 0 {
			service.Spec.IPFamilies = []api.IPFamily{rs.defaultServiceIPFamily}
		}

		// this follows headful services. With one exception on a single stack
		// cluster the user is allowed to create headless services that has multi families
		// the validation allows it
		if len(service.Spec.IPFamilies) < 2 {
			if *(service.Spec.IPFamilyPolicy) == api.RequireDualStack ||
				(*(service.Spec.IPFamilyPolicy) == api.PreferDualStack && len(rs.serviceIPAllocatorsByFamily) == 2) {
				// add the alt ipfamily
				if service.Spec.IPFamilies[0] == api.IPv4Protocol {
					service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv6Protocol)
				} else {
					service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv4Protocol)
				}
			}
		}

		// nothing more needed here
		return nil
	}

	// ipfamily check
	if len(service.Spec.ClusterIPs) > 0 && service.Spec.ClusterIPs[0] == api.ClusterIPNone && service.Spec.Selector == nil {
		klog.V(4).Infof("service %v/%v is a headless service. no validation will be perfomed on its ipfamilies:%v", service.Namespace, service.Name, service.Spec.IPFamilies)
	} else {
		// the following applies on all type of services including headless w/ selector

		// asking for dual stack on a none dual stack cluster
		// should fail without assigning any family
		if service.Spec.IPFamilyPolicy != nil && *(service.Spec.IPFamilyPolicy) == api.RequireDualStack && len(rs.serviceIPAllocatorsByFamily) < 2 {
			el := field.ErrorList{field.Invalid(field.NewPath("spec", "ipFamilyPolicy"), service.Spec.IPFamilyPolicy, "Cluster is not configured for dual stack services")}
			return errors.NewInvalid(api.Kind("Service"), service.Name, el)
		}

		//TODO(khenidak) this code, will become dead, because the above check will trigger before this one
		// we need to make a choice which error we want to return (family or general "no dual stack for you"
		// OR find a way to return multi validation error similar to validation package

		// if there is a family requested then it has to be configured on cluster
		// that means if the service is dual stack and cluster isn't  user won't get "sorry service is dual stack, but cluster isn't"
		// they will get specific error on which ip family not configured on cluster
		for i, ipFamily := range service.Spec.IPFamilies {
			if _, found := rs.serviceIPAllocatorsByFamily[ipFamily]; !found {
				el := field.ErrorList{field.Invalid(field.NewPath("spec", "ipFamilies").Index(i), service.Spec.ClusterIPs, fmt.Sprintf("ipfamily %v is not configured on cluster", ipFamily))}
				return errors.NewInvalid(api.Kind("Service"), service.Name, el)
			}
		}
	}

	// default ipFamilyPolicy to SingleStack. if there are
	// web hooks, they must have already ran by now
	if service.Spec.IPFamilyPolicy == nil {
		singleStack := api.SingleStack
		service.Spec.IPFamilyPolicy = &singleStack
	}

	// nil families, gets cluster default (if feature flag is not in effect, the strategy will take care of removing it)
	if len(service.Spec.IPFamilies) == 0 {
		service.Spec.IPFamilies = []api.IPFamily{rs.defaultServiceIPFamily}
	}

	// is this service looking for dual stack, and this cluster does have two families?
	// if so, then append the missing family
	if *(service.Spec.IPFamilyPolicy) != api.SingleStack &&
		len(service.Spec.IPFamilies) == 1 &&
		len(rs.serviceIPAllocatorsByFamily) == 2 {

		if service.Spec.IPFamilies[0] == api.IPv4Protocol {
			service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv6Protocol)
		}

		if service.Spec.IPFamilies[0] == api.IPv6Protocol {
			service.Spec.IPFamilies = append(service.Spec.IPFamilies, api.IPv4Protocol)
		}
	}

	return nil
}

func isValidAddress(ctx context.Context, addr *api.EndpointAddress, pods rest.Getter) error {
	if addr.TargetRef == nil {
		return fmt.Errorf("Address has no target ref, skipping: %v", addr)
	}
	if genericapirequest.NamespaceValue(ctx) != addr.TargetRef.Namespace {
		return fmt.Errorf("Address namespace doesn't match context namespace")
	}
	obj, err := pods.Get(ctx, addr.TargetRef.Name, &metav1.GetOptions{})
	if err != nil {
		return err
	}
	pod, ok := obj.(*api.Pod)
	if !ok {
		return fmt.Errorf("failed to cast to pod: %v", obj)
	}
	if pod == nil {
		return fmt.Errorf("pod is missing, skipping (%s/%s)", addr.TargetRef.Namespace, addr.TargetRef.Name)
	}
	for _, podIP := range pod.Status.PodIPs {
		if podIP.IP == addr.IP {
			return nil
		}
	}
	return fmt.Errorf("pod ip(s) doesn't match endpoint ip, skipping: %v vs %s (%s/%s)", pod.Status.PodIPs, addr.IP, addr.TargetRef.Namespace, addr.TargetRef.Name)
}

// This is O(N), but we expect haystack to be small;
// so small that we expect a linear search to be faster
func containsNumber(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// This is O(N), but we expect serviceNodePorts to be small;
// so small that we expect a linear search to be faster
func containsNodePort(serviceNodePorts []ServiceNodePort, serviceNodePort ServiceNodePort) bool {
	for _, snp := range serviceNodePorts {
		if snp == serviceNodePort {
			return true
		}
	}
	return false
}

// Loop through the service ports list, find one with the same port number and
// NodePort specified, return this NodePort otherwise return 0.
func findRequestedNodePort(port int, servicePorts []api.ServicePort) int {
	for i := range servicePorts {
		servicePort := servicePorts[i]
		if port == int(servicePort.Port) && servicePort.NodePort != 0 {
			return int(servicePort.NodePort)
		}
	}
	return 0
}

// allocateHealthCheckNodePort allocates health check node port to service.
func allocateHealthCheckNodePort(service *api.Service, nodePortOp *portallocator.PortAllocationOperation) error {
	healthCheckNodePort := service.Spec.HealthCheckNodePort
	if healthCheckNodePort != 0 {
		// If the request has a health check nodePort in mind, attempt to reserve it.
		err := nodePortOp.Allocate(int(healthCheckNodePort))
		if err != nil {
			return fmt.Errorf("failed to allocate requested HealthCheck NodePort %v: %v",
				healthCheckNodePort, err)
		}
		klog.V(4).Infof("Reserved user requested healthCheckNodePort: %d", healthCheckNodePort)
	} else {
		// If the request has no health check nodePort specified, allocate any.
		healthCheckNodePort, err := nodePortOp.AllocateNext()
		if err != nil {
			return fmt.Errorf("failed to allocate a HealthCheck NodePort %v: %v", healthCheckNodePort, err)
		}
		service.Spec.HealthCheckNodePort = int32(healthCheckNodePort)
		klog.V(4).Infof("Reserved allocated healthCheckNodePort: %d", healthCheckNodePort)
	}
	return nil
}

func initNodePorts(service *api.Service, nodePortOp *portallocator.PortAllocationOperation) error {
	svcPortToNodePort := map[int]int{}
	for i := range service.Spec.Ports {
		servicePort := &service.Spec.Ports[i]
		allocatedNodePort := svcPortToNodePort[int(servicePort.Port)]
		if allocatedNodePort == 0 {
			// This will only scan forward in the service.Spec.Ports list because any matches
			// before the current port would have been found in svcPortToNodePort. This is really
			// looking for any user provided values.
			np := findRequestedNodePort(int(servicePort.Port), service.Spec.Ports)
			if np != 0 {
				err := nodePortOp.Allocate(np)
				if err != nil {
					// TODO: when validation becomes versioned, this gets more complicated.
					el := field.ErrorList{field.Invalid(field.NewPath("spec", "ports").Index(i).Child("nodePort"), np, err.Error())}
					return errors.NewInvalid(api.Kind("Service"), service.Name, el)
				}
				servicePort.NodePort = int32(np)
				svcPortToNodePort[int(servicePort.Port)] = np
			} else {
				nodePort, err := nodePortOp.AllocateNext()
				if err != nil {
					// TODO: what error should be returned here?  It's not a
					// field-level validation failure (the field is valid), and it's
					// not really an internal error.
					return errors.NewInternalError(fmt.Errorf("failed to allocate a nodePort: %v", err))
				}
				servicePort.NodePort = int32(nodePort)
				svcPortToNodePort[int(servicePort.Port)] = nodePort
			}
		} else if int(servicePort.NodePort) != allocatedNodePort {
			// TODO(xiangpengzhao): do we need to allocate a new NodePort in this case?
			// Note: the current implementation is better, because it saves a NodePort.
			if servicePort.NodePort == 0 {
				servicePort.NodePort = int32(allocatedNodePort)
			} else {
				err := nodePortOp.Allocate(int(servicePort.NodePort))
				if err != nil {
					// TODO: when validation becomes versioned, this gets more complicated.
					el := field.ErrorList{field.Invalid(field.NewPath("spec", "ports").Index(i).Child("nodePort"), servicePort.NodePort, err.Error())}
					return errors.NewInvalid(api.Kind("Service"), service.Name, el)
				}
			}
		}
	}

	return nil
}

func updateNodePorts(oldService, newService *api.Service, nodePortOp *portallocator.PortAllocationOperation) error {
	oldNodePortsNumbers := collectServiceNodePorts(oldService)
	newNodePorts := []ServiceNodePort{}
	portAllocated := map[int]bool{}

	for i := range newService.Spec.Ports {
		servicePort := &newService.Spec.Ports[i]
		nodePort := ServiceNodePort{Protocol: servicePort.Protocol, NodePort: servicePort.NodePort}
		if nodePort.NodePort != 0 {
			if !containsNumber(oldNodePortsNumbers, int(nodePort.NodePort)) && !portAllocated[int(nodePort.NodePort)] {
				err := nodePortOp.Allocate(int(nodePort.NodePort))
				if err != nil {
					el := field.ErrorList{field.Invalid(field.NewPath("spec", "ports").Index(i).Child("nodePort"), nodePort.NodePort, err.Error())}
					return errors.NewInvalid(api.Kind("Service"), newService.Name, el)
				}
				portAllocated[int(nodePort.NodePort)] = true
			}
		} else {
			nodePortNumber, err := nodePortOp.AllocateNext()
			if err != nil {
				// TODO: what error should be returned here?  It's not a
				// field-level validation failure (the field is valid), and it's
				// not really an internal error.
				return errors.NewInternalError(fmt.Errorf("failed to allocate a nodePort: %v", err))
			}
			servicePort.NodePort = int32(nodePortNumber)
			nodePort.NodePort = servicePort.NodePort
		}
		if containsNodePort(newNodePorts, nodePort) {
			return fmt.Errorf("duplicate nodePort: %v", nodePort)
		}
		newNodePorts = append(newNodePorts, nodePort)
	}

	newNodePortsNumbers := collectServiceNodePorts(newService)

	// The comparison loops are O(N^2), but we don't expect N to be huge
	// (there's a hard-limit at 2^16, because they're ports; and even 4 ports would be a lot)
	for _, oldNodePortNumber := range oldNodePortsNumbers {
		if containsNumber(newNodePortsNumbers, oldNodePortNumber) {
			continue
		}
		nodePortOp.ReleaseDeferred(int(oldNodePortNumber))
	}

	return nil
}

func releaseNodePorts(service *api.Service, nodePortOp *portallocator.PortAllocationOperation) {
	nodePorts := collectServiceNodePorts(service)

	for _, nodePort := range nodePorts {
		nodePortOp.ReleaseDeferred(nodePort)
	}
}

func collectServiceNodePorts(service *api.Service) []int {
	servicePorts := []int{}
	for i := range service.Spec.Ports {
		servicePort := &service.Spec.Ports[i]
		if servicePort.NodePort != 0 {
			servicePorts = append(servicePorts, int(servicePort.NodePort))
		}
	}
	return servicePorts
}
