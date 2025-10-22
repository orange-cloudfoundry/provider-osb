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

package servicebinding

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
	helpersv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/helpers/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/namespaced/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/features"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
)

const (
	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	referenceFinalizerName = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"
)

// Setup adds a controller that reconciles ServiceBinding managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceBindingGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube: mgr.GetClient(),
			originatingIdentityValue: common.KubernetesOSBOriginatingIdentityValue{
				Username: mgr.GetConfig().Impersonate.UserName,
				UID:      mgr.GetConfig().Impersonate.UID,
				Groups:   mgr.GetConfig().Impersonate.Groups,
			},
			newOsbClient:  util.NewOsbClient,
			rotateBinding: o.Features.Enabled(features.EnableAlphaRotateBindings),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithManagementPolicies(),
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.ServiceBindingList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return fmt.Errorf("%s: %w", "cannot register MR state metrics recorder for kind v1alpha1.TestKindList", err)
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.ServiceBindingGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.ServiceBinding{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube                     client.Client
	newOsbClient             func(config apisv1alpha1.ProviderConfig, creds []byte) (osb.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
	rotateBinding            bool
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	client, kube, originatingIdentity, err := util.Connect(ctx, c.kube, c.newOsbClient, mg, c.originatingIdentityValue)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", "cannot connect", err)
	}

	return &external{
		osb:                 client,
		kube:                kube,
		originatingIdentity: *originatingIdentity,
		rotateBinding:       c.rotateBinding,
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
	rotateBinding       bool
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) { //nolint:gocyclo // See note below.
	// NOTE: This method is over our cyclomatic complexity goal.
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, errors.New("managed resource is not a ServiceBinding custom resource")
	}

	bindingData, err := helpersv1alpha1.GetDataFromServiceBinding(ctx, c.kube, binding)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot get data from service binding", err)
	}

	// Manage pending async operations (poll only for "in progress" state)
	if binding.IsStateInProgress() {
		return c.handleLastOperationInProgress(ctx, binding, bindingData)
	}

	// If the resource has no external name, it does not exist
	if binding.IsExternalNameEmpty() {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	// get binding from broker
	req := binding.CreateGetBindingRequest(bindingData)

	resp, err := c.osb.GetBinding(req)

	// Manage errors, if it's http error and 404 , then it means that the resource does not exist
	if err != nil {
		if util.IsResourceGone(err) {
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil
		}
		// Other errors are unexpected
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "OSB GetBinfind request failed", err)
	}

	data := binding.CreateResponseData(*resp)
	// Set observed fields values in cr.Status.AtProvider
	if err = binding.SetResponseDataInStatus(data); err != nil {
		return managed.ExternalObservation{}, err
	}

	credentialsJson, err := util.MarshalMapValues(resp.Credentials)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	// Manage binding rotation
	eo, err1, shouldReturn := handleRenewalBindings(resp, binding, c)
	if shouldReturn {
		return eo, err1
	}
	// Compare observed to response
	// If there is a diff, return an error, since bindings are not updatable
	if binding.IsStatusParametersNotLikeProviderParameters() {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "bindings cannot be updated", err)
	}

	// Handle the ServiceInstance referenced by this binding, if there is one.
	// We add a finalizer to prevent deletion of the ServiceInstance while the current
	// ServiceBinding still exists.
	// Doing so in the Observe() function enable adding a ServiceInstance
	// resource after the creation of the ServiceBinding.
	if err = c.addRefFinalizer(ctx, binding); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot add finalizer to referenced resource", err)
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: credentialsJson,
	}, nil
}

// Create provisions a new ServiceBinding through the OSB client
// and updates its status and connection details accordingly.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, fmt.Errorf("managed resource is not a ServiceBinding custom resource")
	}

	// Retrieve instance and application data for the binding.
	bindingData, err := helpersv1alpha1.GetDataFromServiceBinding(ctx, c.kube, binding)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to retrieve binding data: %w", err)
	}

	// Convert OSB context and parameters from the spec.
	requestContext, requestParams, err := binding.ConvertSpecsData()
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to convert binding spec data: %w", err)
	}

	// Ensure the binding has a valid external name (UUID).
	bindingUUID := binding.EnsureBindingUUID()

	// Build OSB BindRequest.
	req := binding.BuildBindRequest(bindingData, bindingUUID, c.originatingIdentity, requestContext, requestParams)

	// Execute the OSB bind request and handle async responses.
	resp, creation, err, shouldReturn := handleBindRequest(c, req, binding)
	if shouldReturn {
		return creation, err
	}

	// Extract connection credentials from the OSB response.
	creds, err := util.GetCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to extract credentials from OSB response: %w", err)
	}

	data, err := binding.CreateResponseDataWithBindingParameters(*resp)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to create response data with binding parameters: %w", err)
	}

	// Update binding status based on OSB response data.
	if err := binding.SetResponseDataInStatus(data); err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to update binding status: %w", err)
	}

	return managed.ExternalCreation{
		ConnectionDetails: creds,
	}, nil
}

// Calling this function means that the resources on the cluster and from the broker are different.
// Anything that is changed should trigger an error, since bindings are never updatable.
// The only situation when calling this function is valid is when
// the service binding's should be rotated
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalUpdate{}, errors.New("managed resource is not a ServiceBinding custom resource")
	}

	// Prepare binding rotation request
	bindingData, err := helpersv1alpha1.GetDataFromServiceBinding(ctx, c.kube, binding)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("%s: %w", "cannot get data from service binding", err)
	}

	// Trigger binding rotation.
	// We count on the next reconciliation to update renew_before and expires_at (Observe)
	creds, err := binding.TriggerRotation(c.osb, bindingData, c.originatingIdentity)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("%s: %w", "OSB RotateBinding request failed", err)
	}

	// nil credentials will not erase the pre-existing ones.
	// If it is async, the Observe() function will manage the ConnectionDetails instead.
	return managed.ExternalUpdate{
		ConnectionDetails: creds,
	}, nil
}

// Delete handles the deletion of a ServiceBinding in the external system.
// If the unbind operation is asynchronous, it updates the status and sets a finalizer.
// Returns managed.ExternalDelete with additional details for async operations.
func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalDelete{}, errors.New("managed resource is not a ServiceBinding")
	}

	// Fetch binding data (instance & application)
	bindingData, err := helpersv1alpha1.GetDataFromServiceBinding(ctx, c.kube, binding)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("failed to get data from binding: %w", err)
	}

	req := binding.BuildUnbindRequest(bindingData, c.originatingIdentity)

	resp, err := c.osb.Unbind(req)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("OSB Unbind request failed: %w", err)
	}
	if resp == nil {
		return managed.ExternalDelete{}, fmt.Errorf("OSB Unbind returned nil response for request: %+v", req)
	}

	if resp.Async {
		util.HandleAsyncStatus(binding, resp.OperationKey)

		if err := c.handleFinalizer(ctx, binding, asyncDeletionFinalizer, util.AddFinalizerIfNotExists); err != nil {
			return managed.ExternalDelete{}, fmt.Errorf("failed to add async deletion finalizer: %w", err)
		}

		return managed.ExternalDelete{
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	// TODO: delete referenced instance finalizer if necessary

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	// not implemented
	return nil
}

// handleLastOperationInProgress polls the last operation for a ServiceBinding that is currently in progress.
// It updates the binding status based on the OSB response, handles async finalizers, and returns
// a managed.ExternalObservation indicating whether the resource still exists.
func (c *external) handleLastOperationInProgress(ctx context.Context, binding *v1alpha1.ServiceBinding, bindingData v1alpha1.BindingData) (managed.ExternalObservation, error) {
	lastOpReq := binding.BuildBindingLastOperationRequest(bindingData, c.originatingIdentity)

	resp, err := c.osb.PollBindingLastOperation(lastOpReq)
	if err != nil {
		if handled, obs, err := c.handlePollError(ctx, binding, err); handled {
			return obs, err
		}
		return managed.ExternalObservation{}, fmt.Errorf("OSB PollBindingLastOperation request failed: %w", err)
	}

	latest, err := util.GetLatestKubeObject(ctx, c.kube, binding)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	util.UpdateStatusFromLastOp(latest, resp)

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// handlePollError handles errors returned by PollBindingLastOperation.
// Returns (handled bool, observation, error) indicating whether the error was handled.
func (c *external) handlePollError(ctx context.Context, binding *v1alpha1.ServiceBinding, err error) (bool, managed.ExternalObservation, error) {
	if util.IsResourceGone(err) && meta.WasDeleted(binding) {
		// Remove async finalizer from binding
		if err := c.handleFinalizer(ctx, binding, asyncDeletionFinalizer, util.RemoveFinalizerIfExists); err != nil {
			return true, managed.ExternalObservation{}, fmt.Errorf("technical error encountered: %w", err)
		}
		// Remove reference finalizer from the referenced ServiceInstance
		if err := c.removeRefFinalizer(ctx, binding); err != nil {
			return true, managed.ExternalObservation{}, fmt.Errorf("cannot remove finalizer from referenced resource: %w", err)
		}
		// Return that resource no longer exists
		return true, managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}
	return false, managed.ExternalObservation{}, nil
}

// handleRenewalBindings checks whether a binding should be renewed based on its metadata.
// It sets conditions on the binding and returns a managed.ExternalObservation indicating
// whether the resource is up-to-date. Returns a bool to signal whether the operation was handled.
func handleRenewalBindings(resp *osb.GetBindingResponse, binding *v1alpha1.ServiceBinding, c *external) (managed.ExternalObservation, error, bool) {
	if resp.Metadata.RenewBefore == "" {
		return managed.ExternalObservation{}, nil, false
	}

	renewBeforeTime, err := util.ParseISO8601Time(resp.Metadata.RenewBefore, "renewBefore")
	if err != nil {
		return managed.ExternalObservation{}, err, true
	}

	if renewBeforeTime.Before(time.Now()) {
		expireAtTime, err := util.ParseISO8601Time(resp.Metadata.ExpiresAt, "expiresAt")
		if err != nil {
			return managed.ExternalObservation{}, err, true
		}

		expired := binding.MarkBindingIfExpired(expireAtTime)

		if c.rotateBinding {
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil, true
		} else if !expired {
			binding.MarkBindingAsExpiringSoon(expireAtTime)
		}
	}

	return managed.ExternalObservation{}, nil, false
}

// handleBindRequest executes an OSB BindRequest and handles asynchronous responses.
// Returns:
//   - *osb.BindResponse: the OSB response if synchronous,
//   - managed.ExternalCreation: creation details for Crossplane,
//   - error: any occurred error,
//   - bool: whether the caller should return immediately (true if async or error).
func handleBindRequest(
	c *external,
	bindRequest osb.BindRequest,
	binding *v1alpha1.ServiceBinding,
) (*osb.BindResponse, managed.ExternalCreation, error, bool) {

	resp, err := c.osb.Bind(&bindRequest)
	if err != nil {
		return nil, managed.ExternalCreation{}, fmt.Errorf("OSB Bind request failed: %w", err), true
	}

	if resp == nil {
		return nil, managed.ExternalCreation{}, fmt.Errorf("OSB Bind request returned nil response for bindingID %s", bindRequest.BindingID), true
	}

	if resp.Async {
		util.HandleAsyncStatus(binding, resp.OperationKey)

		return nil, managed.ExternalCreation{
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil, true
	}

	// Synchronous response: return the OSB response for further processing
	return resp, managed.ExternalCreation{}, nil, false
}

// addRefFinalizer adds removes the reference finalizer to the ServiceInstance object referenced by the binding
// (if there is one)
func (c *external) removeRefFinalizer(ctx context.Context, binding *v1alpha1.ServiceBinding) error {
	// Remove finalizer from referenced ServiceInstance
	instanceRef := binding.Spec.ForProvider.InstanceRef
	if instanceRef != nil {
		finalizerName := fmt.Sprintf("%s-%s", referenceFinalizerName, binding.Name)
		return c.handleFinalizer(ctx, binding, finalizerName, util.RemoveFinalizerIfExists)
	}
	return nil
}

// addRefFinalizer adds the reference finalizer to the ServiceInstance object referenced by the binding
// (if there is one)
func (c *external) addRefFinalizer(ctx context.Context, binding *v1alpha1.ServiceBinding) error {
	// Add finalizer to referenced ServiceInstance
	instanceRef := binding.Spec.ForProvider.InstanceRef
	if instanceRef != nil {
		finalizerName := fmt.Sprintf("%s-%s", referenceFinalizerName, binding.Name)
		return c.handleFinalizer(ctx, binding, finalizerName, util.AddFinalizerIfNotExists)
	}
	return nil
}

type finalizerModFn func(metav1.Object, string) bool

// handleFinalizer runs kube.Update(ctx, obj) if the finalizerMod function returns true with finalizerName
func (c *external) handleFinalizer(ctx context.Context, obj client.Object, finalizerName string, finalizerMod finalizerModFn) error {
	// Get referenced service instance, if exists
	if finalizerMod(obj, finalizerName) {
		return c.kube.Update(ctx, obj)
	}
	return nil
}
