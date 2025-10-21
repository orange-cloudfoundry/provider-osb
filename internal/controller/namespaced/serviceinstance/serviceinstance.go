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
	"net/http"
	"reflect"

	"errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	apisbinding "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
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
	if instance.Status.AtProvider.LastOperationState == osb.StateInProgress || instance.Status.AtProvider.LastOperationState == "deleting" {
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
	// Manage errors from the GetInstance call.
	if err != nil {
		// Check if the error is an HTTP error returned by the OSB client.
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			// If the HTTP status code is 404, the resource does not exist in the external system.
			if httpErr.StatusCode == http.StatusNotFound {
				return managed.ExternalObservation{
					ResourceExists: false,
				}, nil
			}
		}
		// For all other errors, wrap and return them as unexpected errors.
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "OSB GetInstance request failed", err)
	}

	// Compare the deinstancered spec from the ServiceInstance with the actual instance returned from OSB.
	// This determines if the external resource is up to date with the deinstancered state.
	upToDate, err := compareSpecWithOSB(*instance, osbInstance)
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

	req := buildProvisionRequest(instance, params, ctxMap)

	resp, err := c.osb.ProvisionInstance(req)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("OSB ProvisionInstance request failed: %w", err)
	}

	updateInstanceStatusFromProvisionResponse(instance, resp)

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
	req, err := buildUpdateRequest(instance)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// Send the request to the OSB broker.
	resp, err := c.osb.UpdateInstance(req)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("OSB UpdateInstance request failed: %w", err)
	}

	// Update the instance status based on the response.
	updateInstanceStatusFromUpdate(instance, resp)

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
	if instance.Spec.ForProvider.InstanceId == "" {
		return managed.ExternalDelete{}, nil
	}
	// If there are active bindings, we cannot delete the ServiceInstance.
	if instance.Status.AtProvider.HasActiveBindings {
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

func (c *external) handleLastOperationInProgress(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalObservation, error) {
	// Build the LastOperationRequest using the InstanceId and LastOperationKey from the ServiceInstance status.

	req := &osb.LastOperationRequest{
		InstanceID:          instance.Spec.ForProvider.InstanceId,
		ServiceID:           &instance.Spec.ForProvider.ServiceId,
		PlanID:              &instance.Spec.ForProvider.PlanId,
		OriginatingIdentity: &c.originatingIdentity,
		OperationKey:        &instance.Status.AtProvider.LastOperationKey,
	}
	resp, err := c.osb.PollLastOperation(req)

	if err != nil {
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			if httpErr.StatusCode == http.StatusGone || httpErr.StatusCode == http.StatusNotFound {
				// The resource is gone, we consider it as not existing.
				return managed.ExternalObservation{
					ResourceExists: false,
				}, nil
			}
		}
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "OSB request failed", errors.New("PollLastOperation"))
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

// hasActiveBindingsForInstance checks if any of the given ServiceBindings
// reference the provided ServiceInstance and are not marked for deletion.
func hasActiveBindingsForInstance(instance *v1alpha1.ServiceInstance, bindings []apisbinding.ServiceBinding) bool {
	for _, b := range bindings {
		if b.Spec.ForProvider.InstanceRef == nil {
			continue
		}
		if b.Spec.ForProvider.InstanceRef.Name == instance.GetName() && b.DeletionTimestamp.IsZero() {
			return true
		}
	}
	return false
}

// UpdateActiveBindingsStatus updates the ServiceInstance status to reflect whether
// it has any active ServiceBindings in the same namespace.
func (c *external) UpdateActiveBindingsStatus(ctx context.Context, instance *v1alpha1.ServiceInstance) error {
	var bindingList apisbinding.ServiceBindingList
	if err := c.kube.List(ctx, &bindingList, client.InNamespace(instance.GetNamespace())); err != nil {
		return fmt.Errorf("cannot list ServiceBindings: %w", err)
	}

	instance.Status.AtProvider.HasActiveBindings = hasActiveBindingsForInstance(instance, bindingList.Items)
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

// compareSpecWithOSB compares a ServiceInstance spec with the corresponding OSB instance response.
// It returns true if both representations are consistent, false otherwise.
func compareSpecWithOSB(instance v1alpha1.ServiceInstance, osbInstance *osb.GetInstanceResponse) (bool, error) {
	if osbInstance == nil {
		return false, nil
	}

	// Compare PlanID
	if instance.Spec.ForProvider.PlanId != "" && instance.Spec.ForProvider.PlanId != osbInstance.PlanID {
		return false, nil
	}

	// Compare parameters
	if ok, err := compareInstanceParameters(instance, osbInstance); err != nil || !ok {
		return ok, err
	}

	// Compare context
	if !reflect.DeepEqual(instance.Spec.ForProvider.Context, instance.Status.AtProvider.Context) {
		return false, nil
	}

	return true, nil
}

// compareInstanceParameters compares parameters between a ServiceInstance spec
// and its OSB instance response.
func compareInstanceParameters(instance v1alpha1.ServiceInstance, osbInstance *osb.GetInstanceResponse) (bool, error) {
	if len(instance.Spec.ForProvider.Parameters) == 0 {
		return true, nil
	}

	params, err := instance.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return false, fmt.Errorf("failed to parse ServiceInstance parameters: %w", err)
	}

	if !reflect.DeepEqual(params, osbInstance.Parameters) {
		return false, nil
	}

	return true, nil
}

// buildProvisionRequest creates an OSB ProvisionRequest from a ServiceInstance spec.
func buildProvisionRequest(si *v1alpha1.ServiceInstance, params map[string]interface{}, ctxMap map[string]interface{}) *osb.ProvisionRequest {
	return &osb.ProvisionRequest{
		InstanceID:        si.Spec.ForProvider.InstanceId,
		ServiceID:         si.Spec.ForProvider.ServiceId,
		PlanID:            si.Spec.ForProvider.PlanId,
		OrganizationGUID:  si.Spec.ForProvider.OrganizationGuid,
		SpaceGUID:         si.Spec.ForProvider.SpaceGuid,
		Parameters:        params,
		AcceptsIncomplete: true,
		Context:           ctxMap,
	}
}

// updateInstanceStatusFromProvisionResponse updates the ServiceInstance status based on the OSB ProvisionInstance response.
func updateInstanceStatusFromProvisionResponse(si *v1alpha1.ServiceInstance, resp *osb.ProvisionResponse) {
	si.Status.AtProvider.Context = si.Spec.ForProvider.Context
	si.Status.AtProvider.DashboardURL = resp.DashboardURL

	if resp.Async {
		si.Status.SetConditions(xpv1.Creating())
		si.Status.AtProvider.LastOperationState = osb.StateInProgress
		if resp.OperationKey != nil {
			si.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		return
	}

	si.Status.SetConditions(xpv1.Available())
	si.Status.AtProvider.LastOperationState = osb.StateSucceeded
}

// buildUpdateRequest constructs an OSB UpdateInstanceRequest from the given ServiceInstance.
// It converts the ServiceInstance spec parameters and context into the format expected by the OSB client.
// Returns the prepared request or an error if the conversion fails.
func buildUpdateRequest(si *v1alpha1.ServiceInstance) (*osb.UpdateInstanceRequest, error) {
	params, err := si.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ServiceInstance parameters: %w", err)
	}

	ctxMap, err := si.Spec.ForProvider.Context.ToMap()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ServiceInstance context: %w", err)
	}

	return &osb.UpdateInstanceRequest{
		InstanceID:        si.Spec.ForProvider.InstanceId,
		ServiceID:         si.Spec.ForProvider.ServiceId,
		PlanID:            &si.Spec.ForProvider.PlanId,
		Parameters:        params,
		AcceptsIncomplete: true,
		Context:           ctxMap,
	}, nil
}

// updateInstanceStatusFromUpdate updates the status of the ServiceInstance
// based on the OSB UpdateInstanceResponse. It sets the dashboard URL, context,
// and last operation state. If the update is asynchronous, it also sets the
// last operation key and marks the operation as in progress.
func updateInstanceStatusFromUpdate(si *v1alpha1.ServiceInstance, resp *osb.UpdateInstanceResponse) {
	si.Status.AtProvider.Context = si.Spec.ForProvider.Context
	si.Status.AtProvider.DashboardURL = resp.DashboardURL

	if resp.Async {
		si.Status.AtProvider.LastOperationState = osb.StateInProgress
		if resp.OperationKey != nil {
			si.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
	} else {
		si.Status.AtProvider.LastOperationState = osb.StateSucceeded
	}
}

// deprovision handles the deprovisioning of a ServiceInstance in the external system.
// It returns an ExternalDelete indicating whether the resource still exists or not.
func (c *external) deprovision(ctx context.Context, instance *v1alpha1.ServiceInstance) (managed.ExternalDelete, error) {
	req := c.buildDeprovisionRequest(instance)

	resp, err := c.osb.DeprovisionInstance(req)
	if err != nil {
		if httpErr, isHTTP := osb.IsHTTPError(err); isHTTP && (httpErr.StatusCode == http.StatusGone || httpErr.StatusCode == http.StatusNotFound) {
			// Resource is already gone; nothing to do.
			return managed.ExternalDelete{}, nil
		}
		return managed.ExternalDelete{}, fmt.Errorf("OSB DeprovisionInstance request failed: %w", err)
	}

	if resp.Async {
		// Asynchronous deletion: update last operation status
		updateInstanceStatusForAsyncDeletion(instance, resp)
		// Persist status to Kubernetes
		if err := c.kube.Status().Update(ctx, instance); err != nil {
			return managed.ExternalDelete{}, fmt.Errorf("cannot update ServiceInstance status: %w", err)
		}
	}

	return managed.ExternalDelete{}, nil
}

// buildDeprovisionRequest constructs an OSB DeprovisionRequest from a ServiceInstance.
func (c *external) buildDeprovisionRequest(si *v1alpha1.ServiceInstance) *osb.DeprovisionRequest {
	return &osb.DeprovisionRequest{
		InstanceID:          si.Spec.ForProvider.InstanceId,
		ServiceID:           si.Spec.ForProvider.ServiceId,
		PlanID:              si.Spec.ForProvider.PlanId,
		AcceptsIncomplete:   true,
		OriginatingIdentity: &c.originatingIdentity,
	}
}

// updateInstanceStatusForAsyncDeletion updates the ServiceInstance status when
// a deletion is performed asynchronously.
func updateInstanceStatusForAsyncDeletion(si *v1alpha1.ServiceInstance, resp *osb.DeprovisionResponse) {
	si.Status.AtProvider.LastOperationState = "deleting"
	if resp.OperationKey != nil {
		si.Status.AtProvider.LastOperationKey = *resp.OperationKey
	}
}
