/*
Copyright 2022 The Crossplane Authors.

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

package serviceinstance

import (
	"context"
	"fmt"

	"errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	apisbinding "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
	apishelpers "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/helpers/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/instance/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/namespaced/util"
)

// Setup adds a controller that reconciles ServiceInstance managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceInstanceGroupKind)

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.ServiceInstanceGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kubeClient:   mgr.GetClient(),
			newOsbClient: util.NewOsbClient}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.ServiceInstance{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kubeClient               client.Client
	newOsbClient             func(conf apisv1alpha1.ProviderConfig, creds []byte) (osb.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	osb, kube, originatingIdentity, err := util.Connect(ctx, c.kubeClient, c.newOsbClient, mg, c.originatingIdentityValue)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", "cannot connect", err)
	}

	// Return an external client with the OSB client, Kubernetes client, and originating identity.
	return &external{
		osb:                 osb,
		kube:                kube,
		originatingIdentity: *originatingIdentity,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	osb                 osb.Client
	kube                client.Client
	originatingIdentity osb.OriginatingIdentity
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	// Assert that the managed resource is of type ServiceInstance.
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalObservation{}, errors.New("managed resource is not a ServiceInstance custom resource")
	}

	if err := validateInstanceID(instance); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "validation failed", err)
	}

	// Manage pending async operations (poll only for "in progress" state)
	if instance.IsStateInProgress() || instance.IsStateDeleting() {
		return c.handleLastOperationInProgress(ctx, instance)
	}

	// If the resource is being deleted, check for active bindings before allowing deletion.
	if !instance.GetDeletionTimestamp().IsZero() {
		return c.handleDeletionWithActiveBindings(ctx, instance)
	}

	// Build the GetInstanceRequest uinstanceng the InstanceId from the ServiceInstance spec.
	req := &osb.GetInstanceRequest{
		InstanceID: instance.Spec.ForProvider.InstanceId,
		//ServiceID:  instance.Spec.ForProvider.ServiceId,
		//PlanID:     instance.Spec.ForProvider.PlanId,
	}

	// Call the OSB client's GetInstance method to retrieve the current state of the instance.
	osbInstance, err := c.osb.GetInstance(req)

	if err != nil {
		if util.IsResourceGone(err) {
			// The resource does not exist externally — treat as non-existent
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil
		}
		// Other errors are unexpected
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "OSB GetInstance request failed", err)
	}

	// Compare the deinstancered spec from the ServiceInstance with the actual instance returned from OSB.
	// This determines if the external resource is up to date with the deinstancered state.
	upToDate, err := instance.CompareSpecWithOSB(osbInstance)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot compare ServiceInstance spec with OSB instance", err)
	}
	// These fmt statements should be removed in the real implementation.
	return managed.ExternalObservation{
		// Return false when the external resource does not exist. This lets
		// the managed resource reconciler know that it needs to call Create to
		// (re)create the resource, or that it has successfully been deleted.
		ResourceExists: true,

		// Return false when the external resource exists, but it not up to date
		// with the deinstancered managed resource state. This lets the managed
		// resource reconciler know that it needs to call Update.
		ResourceUpToDate: upToDate,

		// Return any details that may be required to connect to the external
		// resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{
			"dashboardURL": []byte(osbInstance.DashboardURL),
		},
	}, nil
}

// Create provisions a new ServiceInstance through the OSB client
// and updates its status accordingly.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalCreation{}, errors.New("managed resource is not a ServiceInstance custom resource")
	}

	params, err := instance.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to parse ServiceInstance parameters: %w", err)
	}

	ctxMap, err := instance.Spec.ForProvider.Context.ToMap()
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to parse ServiceInstance context: %w", err)
	}

	req := instance.BuildOSBProvisionRequest(params, ctxMap)

	resp, err := c.osb.ProvisionInstance(req)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("OSB ProvisionInstance request failed: %w", err)
	}

	instance.UpdateStatusFromOSB(resp)

	if err := c.kube.Status().Update(ctx, instance); err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("cannot update ServiceInstance status: %w", err)
	}

	return managed.ExternalCreation{}, nil
}

// Update sends an update request to the OSB broker for the given ServiceInstance
// and updates its status in Kubernetes accordingly.
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalUpdate{}, fmt.Errorf("managed resource is not a ServiceInstance custom resource")
	}

	// Build the OSB update request.
	req, err := instance.BuildOSBUpdateRequest()
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// Send the request to the OSB broker.
	resp, err := c.osb.UpdateInstance(req)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("OSB UpdateInstance request failed: %w", err)
	}

	// Update the instance status based on the response.
	instance.UpdateStatusFromOSB(resp)

	// Persist the updated status in Kubernetes.
	if err := c.kube.Status().Update(ctx, instance); err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("cannot update ServiceInstance status: %w", err)
	}

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalDelete{}, errors.New("managed resource is not a ServiceInstance custom resource")
	}

	// If the InstanceId is not set, there is nothing to delete.
	// We consider the resource as already deleted.
	if instance.IsInstanceIDEmpty() {
		return managed.ExternalDelete{}, nil
	}
	if instance.IsAlreadyDeleted() {
		return managed.ExternalDelete{}, nil
	}
	// If there are active bindings, we cannot delete the ServiceInstance.
	if instance.HasActiveBindings() {
		return managed.ExternalDelete{}, errors.New("cannot delete ServiceInstance, it has active bindings---")
	}

	return c.deprovision(ctx, instance)
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

// removeFinalizer removes the specified finalizer from the ServiceInstance if it exists.
func (c *external) removeFinalizer(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalObservation, error) {
	latest, err := util.GetLatestKubeObject(ctx, c.kube, instance)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	// Remove the specified finalizer if it exists.
	for _, f := range latest.GetFinalizers() {
		controllerutil.RemoveFinalizer(latest, f)
	}

	// Update the status of the ServiceInstance resource in Kubernetes.
	if err := c.kube.Status().Update(ctx, latest); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot update ServiceInstance status", err)
	}

	// If the last operation was a deletion and it has succeeded, we consider the resource as deleted.
	return managed.ExternalObservation{
		ResourceExists: false,
	}, nil
}

// Ensure the InstanceId is set in the ServiceInstance spec before proceeding.
func validateInstanceID(si resource.Managed) error {
	if siSpec, ok := si.(interface {
		GetForProvider() interface{ InstanceId() string }
	}); ok {
		if siSpec.GetForProvider().InstanceId() == "" {
			return errors.New("InstanceId must be set in the ServiceInstance spec")
		}
		return nil
	}
	return errors.New("unable to access InstanceId in the ServiceInstance spec")
}

// Build the LastOperationRequest using the InstanceId and LastOperationKey from the ServiceInstance status.
func (c *external) handleLastOperationInProgress(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalObservation, error) {
	req := instance.CreateRequestPollLastOperation(c.originatingIdentity)

	resp, err := c.osb.PollLastOperation(req)

	if err != nil {
		if util.IsResourceGone(err) {
			// The resource is gone — treat as non-existent
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil
		}
		// Other errors are unexpected
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "OSB PollLastOperation request failed", err)
	}

	latest, err := util.GetLatestKubeObject(ctx, c.kube, instance)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	util.UpdateStatusFromLastOp(latest, resp)

	if instance.IsDeletable() {
		return c.removeFinalizer(ctx, instance)
	}

	// Update the status of the ServiceInstance resource in Kubernetes.
	if err := c.kube.Status().Update(ctx, latest); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot update ServiceInstance status", err)
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// UpdateActiveBindingsStatus updates the ServiceInstance status to reflect whether
// it has any active ServiceBindings in the same namespace.
func (c *external) UpdateActiveBindingsStatus(ctx context.Context, instance *v1alpha1.ServiceInstance) error {
	var bindingList apisbinding.ServiceBindingList
	if err := c.kube.List(ctx, &bindingList, client.InNamespace(instance.GetNamespace())); err != nil {
		return fmt.Errorf("cannot list ServiceBindings: %w", err)
	}

	apishelpers.SetActiveBindingsForInstance(instance, bindingList.Items)

	return nil
}

// handleActiveBindings checks for active ServiceBindings before allowing deletion of the ServiceInstance.
// If active bindings are found, it updates the ServiceInstance status to reflect this and prevents deletion.
// If no active bindings are found, it allows the deletion process to proceed.
// It returns an ExternalObservation indicating that the resource exists and is up to date,
// along with any error encountered during the process.
func (c *external) handleDeletionWithActiveBindings(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalObservation, error) {
	if err := c.UpdateActiveBindingsStatus(ctx, instance); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot update active bindings status", err)
	}

	// Update the status of the ServiceInstance resource in Kubernetes.
	if err := c.kube.Status().Update(ctx, instance); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot update ServiceInstance ressource status", err)
	}

	// If the resource is being deleted, we consider it as existing and up to date.
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// deprovision handles the deprovisioning of a ServiceInstance in the external system.
// It returns an ExternalDelete indicating whether the resource still exists or not.
func (c *external) deprovision(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalDelete, error) {
	req := instance.BuildDeprovisionRequest(c.originatingIdentity)

	resp, err := c.osb.DeprovisionInstance(req)
	if err != nil {
		if util.IsResourceGone(err) {
			// Resource is already gone; nothing to do.
			return managed.ExternalDelete{}, nil
		}
		return managed.ExternalDelete{}, fmt.Errorf("OSB DeprovisionInstance request failed: %w", err)
	}

	if resp.Async {
		// Asynchronous deletion: update last operation status
		instance.UpdateStatusForAsyncDeletion(resp)
		// Persist status to Kubernetes
		if err := c.kube.Status().Update(ctx, instance); err != nil {
			return managed.ExternalDelete{}, fmt.Errorf("cannot update ServiceInstance status: %w", err)
		}
	}

	return managed.ExternalDelete{}, nil
}
