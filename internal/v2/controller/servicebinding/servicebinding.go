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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
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

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v2/application/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/v2/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/v2/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v2/instance/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v2/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/v2/controller/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/v2/features"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
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

	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot get data from service binding", err)
	}

	// Manage pending async operations (poll only for "in progress" state)
	if binding.Status.AtProvider.LastOperationState == osb.StateInProgress {
		return c.handleLastOperationInProgress(ctx, binding, bindingData)
	}

	// If the resource has no external name, it does not exist
	externalName := meta.GetExternalName(binding)
	if externalName == "" {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	// get binding from broker
	req := &osb.GetBindingRequest{
		InstanceID: bindingData.instanceData.InstanceId,
		BindingID:  externalName,
	}

	resp, err := c.osb.GetBinding(req)

	// Manage errors, if it's http error and 404 , then it means that the resource does not exist
	eo, err, shouldReturn := util.HandleHttpErrorForBindingObserver(err)
	if shouldReturn {
		return eo, err
	}

	// Set observed fields values in cr.Status.AtProvider
	if err = c.setResponseDataInStatus(binding, responseData{
		Parameters:      resp.Parameters,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	}); err != nil {
		return managed.ExternalObservation{}, err
	}

	// Marshall credentials from response
	credentialsJson := map[string][]byte{}

	for k, v := range resp.Credentials {
		marshaled, err := json.Marshal(v)
		if err != nil {
			return managed.ExternalObservation{}, fmt.Errorf("%s: %w", "cannot parse credentials", err)
		}
		credentialsJson[k] = marshaled
	}

	// Manage binding rotation
	eo, err1, shouldReturn := handleRenewalBindings(resp, binding, c)
	if shouldReturn {
		return eo, err1
	}
	// Compare observed to response
	// If there is a diff, return an error, since bindings are not updatable
	isEqual := compareToObserved(binding)
	if !isEqual {
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
	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to retrieve binding data: %w", err)
	}

	// Convert OSB context and parameters from the spec.
	requestContext, requestParams, err := convertSpecsData(binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to convert binding spec data: %w", err)
	}

	// Ensure the binding has a valid external name (UUID).
	bindingUUID := ensureBindingUUID(binding)

	// Build OSB BindRequest.
	req := buildBindRequest(binding, bindingData, bindingUUID, c.originatingIdentity, requestContext, requestParams)

	// Execute the OSB bind request and handle async responses.
	resp, creation, err, shouldReturn := handleBindRequest(c, req, binding)
	if shouldReturn {
		return creation, err
	}

	// Extract connection credentials from the OSB response.
	creds, err := getCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("failed to extract credentials from OSB response: %w", err)
	}

	// Update binding status based on OSB response data.
	if err := c.updateBindingStatusFromResponse(binding, resp); err != nil {
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
	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("%s: %w", "cannot get data from service binding", err)
	}

	// Trigger binding rotation.
	// We count on the next reconciliation to update renew_before and expires_at (Observe)
	creds, err := c.triggerRotation(binding, bindingData)
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
	data, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("failed to get data from binding: %w", err)
	}

	req := buildUnbindRequest(binding, data, &c.originatingIdentity)

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
func (c *external) handleLastOperationInProgress(ctx context.Context, binding *v1alpha1.ServiceBinding, bindingData bindingData) (managed.ExternalObservation, error) {
	lastOpReq := c.buildBindingLastOperationRequest(binding, bindingData)

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

// buildBindingLastOperationRequest constructs an OSB BindingLastOperationRequest for polling.
func (c external) buildBindingLastOperationRequest(binding *v1alpha1.ServiceBinding, data bindingData) *osb.BindingLastOperationRequest {
	return &osb.BindingLastOperationRequest{
		InstanceID:          data.instanceData.InstanceId,
		BindingID:           meta.GetExternalName(binding),
		ServiceID:           &data.instanceData.ServiceId,
		PlanID:              &data.instanceData.PlanId,
		OriginatingIdentity: &c.originatingIdentity,
		OperationKey:        &binding.Status.AtProvider.LastOperationKey,
	}
}

// handlePollError handles errors returned by PollBindingLastOperation.
// Returns (handled bool, observation, error) indicating whether the error was handled.
func (c *external) handlePollError(ctx context.Context, binding *v1alpha1.ServiceBinding, err error) (bool, managed.ExternalObservation, error) {
	if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr && httpErr.StatusCode == http.StatusGone && meta.WasDeleted(binding) {
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

	renewBeforeTime, err := parseISO8601Time(resp.Metadata.RenewBefore, "renewBefore")
	if err != nil {
		return managed.ExternalObservation{}, err, true
	}

	if renewBeforeTime.Before(time.Now()) {
		expireAtTime, err := parseISO8601Time(resp.Metadata.ExpiresAt, "expiresAt")
		if err != nil {
			return managed.ExternalObservation{}, err, true
		}

		expired := markBindingIfExpired(binding, expireAtTime)

		if c.rotateBinding {
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil, true
		} else if !expired {
			markBindingAsExpiringSoon(binding, expireAtTime)
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

// parseISO8601Time parses a string in ISO8601 format into a time.Time.
// Returns a wrapped error if parsing fails.
func parseISO8601Time(value, field string) (time.Time, error) {
	t, err := time.Parse(util.Iso8601dateFormat, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("error parsing %s time: %w", field, err)
	}
	return t, nil
}

// markBindingIfExpired sets a false healthy condition if the binding has expired.
// Returns true if expired, false otherwise.
func markBindingIfExpired(binding *v1alpha1.ServiceBinding, expireAt time.Time) bool {
	if expireAt.Before(time.Now()) {
		cond := xpv1.Condition{
			Type:    xpv1.TypeHealthy,
			Status:  v1.ConditionFalse,
			Message: fmt.Sprintf("warning: the binding has expired %s", expireAt.Format(util.Iso8601dateFormat)),
		}
		binding.SetConditions(cond)
		return true
	}
	return false
}

// markBindingAsExpiringSoon sets a false healthy condition if the binding is about to expire.
func markBindingAsExpiringSoon(binding *v1alpha1.ServiceBinding, expireAt time.Time) {
	cond := xpv1.Condition{
		Type:    xpv1.TypeHealthy,
		Status:  v1.ConditionFalse,
		Message: fmt.Sprintf("warning: the binding will expire soon %s", expireAt.Format(util.Iso8601dateFormat)),
	}
	binding.SetConditions(cond)
}

// convertSpecsData converts the ServiceBinding spec's Context and Parameters
// from their Kubernetes types into plain map[string]any structures suitable for OSB requests.
// - Context is marshaled to JSON and then unmarshaled into a map.
// - Parameters, stored as raw JSON bytes, are unmarshaled into a map.
// Returns the converted context and parameters maps, or an error if conversion fails.
func convertSpecsData(spec v1alpha1.ServiceBindingParameters) (map[string]any, map[string]any, error) {
	requestContextBytes, err := json.Marshal(spec.Context)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", "error while marshalling or parsing context from ServiceBinding", err)
	}
	var requestContext map[string]any
	if err = json.Unmarshal(requestContextBytes, &requestContext); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", "error while marshalling or parsing context to bytes from ServiceBinding", err)
	}

	// Convert spec.Parameters of type *apiextensions.JSON to map[string]any
	var requestParams map[string]any
	if err = json.Unmarshal([]byte(spec.Parameters), &requestParams); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", "error while marshalling or parsing paramaters to bytes from ServiceBinding", err)
	}
	return requestContext, requestParams, nil
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

type bindingData struct {
	instanceData    common.InstanceData
	applicationData common.ApplicationData
}

func (c *external) fetchApplicationDataFromBinding(ctx context.Context, spec v1alpha1.ServiceBindingParameters) (*common.ApplicationData, error) {
	var appData *common.ApplicationData

	if spec.ApplicationRef != nil {
		// Fetch from referenced resource
		application := applicationv1alpha1.Application{}
		if err := c.kube.Get(ctx, spec.ApplicationRef.ToObjectKey(), &application); err != nil {
			if kerrors.IsNotFound(err) {
				return nil, errors.New("binding referenced an application which does not exist")
			}
			return nil, fmt.Errorf("%s: %w", "error while retrieving referenced application", err)
		}
		appData = &application.Spec.ForProvider
	} else if spec.ApplicationData != nil {
		// Fetch from within the service binding
		appData = spec.ApplicationData
	}
	return appData, nil
}

// fetchApplicationDataFromInstance retrieves application data associated with a given service instance.
//
// The application data can come from two possible sources:
// 1. A referenced Application resource (via ApplicationRef)
// 2. Inline data provided directly within the instance specification
//
// Parameters:
// - ctx: the context for request lifetime and cancellation
// - instanceSpec: the specification of the instance containing either a reference or inline application data
//
// Returns:
// - *common.ApplicationData: the retrieved application data, or nil if not found
// - error: any error encountered during the fetch process
func (c *external) fetchApplicationDataFromInstance(ctx context.Context, instanceSpec common.InstanceData) (*common.ApplicationData, error) {
	var appData *common.ApplicationData

	// Case 1: The instance references an external Application resource
	if instanceSpec.ApplicationRef != nil {
		application := applicationv1alpha1.Application{}

		// Try to retrieve the referenced Application from the cluster
		if err := c.kube.Get(ctx, instanceSpec.ApplicationRef.ToObjectKey(), &application); err != nil {
			// Handle the case where the Application resource doesn't exist
			if kerrors.IsNotFound(err) {
				return nil, errors.New("binding referenced an instance which referenced an application which does not exist")
			}
			// Wrap and return any other error that occurred while fetching
			return nil, fmt.Errorf("%s: %w", "error while retrieving referenced application from referenced instance", err)
		}

		// Store the retrieved Application's provider data
		appData = &application.Spec.ForProvider

		// Case 2: The application data is provided inline within the instance spec
	} else if instanceSpec.ApplicationData != nil {
		appData = instanceSpec.ApplicationData
	}

	// Return the resolved application data (or nil if neither source was found)
	return appData, nil
}

// getDataFromServiceBinding retrieves both instance and application data required for
// handling a ServiceBinding. It supports fetching from either a direct reference
// (InstanceRef) or inlined InstanceData in the spec, and similarly for application data.
// Returns a bindingData struct containing both instance and application data,
// or an error if any required data is missing or cannot be retrieved.
func (c *external) getDataFromServiceBinding(ctx context.Context, spec v1alpha1.ServiceBindingParameters) (bindingData, error) {
	// Fetch application data directly from the binding spec if available.
	appData, err := c.fetchApplicationDataFromBinding(ctx, spec)
	if err != nil {
		return bindingData{}, fmt.Errorf("failed to fetch application data from binding: %w", err)
	}

	// Initialize instance data pointer.
	var instanceData *common.InstanceData

	if spec.InstanceRef != nil {
		// Fetch instance data from the referenced ServiceInstance resource.
		instance := instancev1alpha1.ServiceInstance{}
		if err := c.kube.Get(ctx, spec.InstanceRef.ToObjectKey(), &instance); err != nil {
			if kerrors.IsNotFound(err) {
				return bindingData{}, fmt.Errorf("binding references a service instance which does not exist")
			}
			return bindingData{}, fmt.Errorf("failed to retrieve referenced service instance: %w", err)
		}

		instanceSpec := instance.Spec.ForProvider
		instanceData = &instanceSpec

		// If no application data was found on the binding, fetch it from the instance.
		if appData == nil {
			appData, err = c.fetchApplicationDataFromInstance(ctx, instanceSpec)
			if err != nil {
				return bindingData{}, fmt.Errorf("failed to fetch application data from instance: %w", err)
			}
		}
	} else if spec.InstanceData != nil {
		// Use inlined instance data if available.
		instanceData = spec.InstanceData
	}

	// Validate that both instance data and application data are present.
	if instanceData == nil {
		return bindingData{}, fmt.Errorf("missing instance data: cannot handle binding without instance reference or inlined data")
	}

	if appData == nil {
		return bindingData{}, fmt.Errorf("missing application data: cannot handle binding without application info")
	}

	return bindingData{*instanceData, *appData}, nil
}

type responseData struct {
	Parameters      map[string]any
	Endpoints       *[]osb.Endpoint
	VolumeMounts    *[]osb.VolumeMount
	RouteServiceURL *string
	SyslogDrainURL  *string
	Metadata        *osb.BindingMetadata
}

func (c *external) setResponseDataInStatus(binding *v1alpha1.ServiceBinding, data responseData) error {
	params, err := json.Marshal(data.Parameters)
	if err != nil {
		return errors.New("error while marshalling or parsing parameters from response")
	}

	endpoints, err := json.Marshal(data.Endpoints)
	if err != nil {
		return errors.New("error while marshalling or parsing endpoints from response")
	}

	volumeMounts, err := json.Marshal(data.VolumeMounts)
	if err != nil {
		return errors.New("error while marshalling or parsing volume mounts from response")
	}

	return c.setAtProvider(binding, v1alpha1.ServiceBindingObservation{
		// Update attributes from response data
		Parameters:      common.SerializableParameters(params),
		RouteServiceURL: data.RouteServiceURL,
		Endpoints:       v1alpha1.SerializableEndpoints(endpoints),
		VolumeMounts:    v1alpha1.SerializableVolumeMounts(volumeMounts),
		SyslogDrainURL:  data.SyslogDrainURL,
		Metadata:        data.Metadata,
		// Do not change these attributes
		LastOperationState:       binding.Status.AtProvider.LastOperationState,
		LastOperationKey:         binding.Status.AtProvider.LastOperationKey,
		LastOperationDescription: binding.Status.AtProvider.LastOperationDescription,
		LastOperationPolledTime:  binding.Status.AtProvider.LastOperationPolledTime,
	})
}

func (c *external) setAtProvider(cr *v1alpha1.ServiceBinding, observation v1alpha1.ServiceBindingObservation) error {
	cr.Status.AtProvider = observation
	// checks can be added here
	return nil
}

// triggerRotation triggers a credentials rotation for the given ServiceBinding.
// It creates a new binding ID (UUID) and calls the OSB RotateBinding API.
// Returns the new connection credentials if the operation is synchronous, or nil otherwise.
// Updates the ServiceBinding status with LastOperation info in case of async rotation.
// triggerRotation triggers a credentials rotation for the given ServiceBinding.
// It returns the new credentials if the rotation is synchronous, or nil otherwise.
func (c *external) triggerRotation(binding *v1alpha1.ServiceBinding, data bindingData) (map[string][]byte, error) {
	newUUID := string(uuid.NewUUID())

	req := c.buildRotateBindingRequest(binding, data, newUUID)
	resp, err := c.osb.RotateBinding(req)
	if err != nil {
		return nil, fmt.Errorf("OSB RotateBinding request failed: %w", err)
	}

	// Update the binding's external name only if the request succeeded
	meta.SetExternalName(binding, newUUID)

	if !resp.Async {
		creds, err := extractCredentials(resp)
		if err != nil {
			return nil, err
		}
		return creds, nil
	}

	util.HandleAsyncStatus(binding, resp.OperationKey)

	binding.Status.AtProvider.LastOperationPolledTime = *util.TimeNow()
	binding.Status.AtProvider.LastOperationDescription = ""
	return nil, nil
}

// buildRotateBindingRequest constructs an OSB RotateBindingRequest for a binding.
func (c *external) buildRotateBindingRequest(binding *v1alpha1.ServiceBinding, data bindingData, newUUID string) *osb.RotateBindingRequest {
	return &osb.RotateBindingRequest{
		InstanceID:           data.instanceData.InstanceId,
		BindingID:            newUUID,
		AcceptsIncomplete:    true, // TODO: make configurable
		PredecessorBindingID: meta.GetExternalName(binding),
		OriginatingIdentity:  &c.originatingIdentity,
	}
}

// extractCredentials marshals OSB BindResponse credentials into map[string][]byte.
func extractCredentials(resp *osb.BindResponse) (map[string][]byte, error) {
	creds := make(map[string][]byte, len(resp.Credentials))
	for key, value := range resp.Credentials {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal credential '%s' from response: %w", key, err)
		}
		creds[key] = data
	}
	return creds, nil
}

func compareToObserved(cr *v1alpha1.ServiceBinding) bool {
	// We do not compare credentials, as this logic is managed by creds rotation.
	// We do not compare bindingmetadata either, since the only metadata in binding objects
	// is related to binding rotation (renew_before and expires_at)

	// TODO add context and route to test if these were updated and return false
	return reflect.DeepEqual(cr.Status.AtProvider.Parameters, cr.Spec.ForProvider.Parameters)
}

// getCredsFromResponse serializes the credentials from an OSB BindResponse
// into a map[string][]byte suitable for Crossplane connection details.
// Returns an error if any credential cannot be marshaled to JSON.
func getCredsFromResponse(resp *osb.BindResponse) (map[string][]byte, error) {
	creds := make(map[string][]byte, len(resp.Credentials))

	for key, value := range resp.Credentials {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal credential '%s' from response: %w", key, err)
		}
		creds[key] = data
	}

	return creds, nil
}

// ensureBindingUUID returns the existing external name or generates a new UUID if missing.
func ensureBindingUUID(binding *v1alpha1.ServiceBinding) string {
	if id := meta.GetExternalName(binding); id != "" {
		return id
	}
	newID := string(uuid.NewUUID())
	meta.SetExternalName(binding, newID)
	return newID
}

// buildBindRequest creates an OSB BindRequest from the ServiceBinding spec and related data.
func buildBindRequest(
	binding *v1alpha1.ServiceBinding,
	data bindingData,
	bindingID string,
	originatingIdentity osb.OriginatingIdentity,
	ctxMap, params map[string]any,
) osb.BindRequest {
	return osb.BindRequest{
		BindingID:           bindingID,
		InstanceID:          data.instanceData.InstanceId,
		BindResource:        &osb.BindResource{AppGUID: &data.applicationData.Guid, Route: &binding.Spec.ForProvider.Route},
		AcceptsIncomplete:   true, // TODO: make configurable
		OriginatingIdentity: &originatingIdentity,
		PlanID:              data.instanceData.PlanId,
		Context:             ctxMap,
		Parameters:          params,
		ServiceID:           data.instanceData.ServiceId,
		AppGUID:             &data.applicationData.Guid,
	}
}

// updateBindingStatusFromResponse updates the ServiceBinding status with data from the OSB BindResponse.
func (c *external) updateBindingStatusFromResponse(binding *v1alpha1.ServiceBinding, resp *osb.BindResponse) error {
	params, err := binding.Status.AtProvider.Parameters.ToParameters()
	if err != nil {
		return fmt.Errorf("failed to parse parameters from binding status: %w", err)
	}

	if err := c.setResponseDataInStatus(binding, responseData{
		Parameters:      params,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	}); err != nil {
		return fmt.Errorf("failed to set response data in status: %w", err)
	}

	return nil
}

// buildUnbindRequest constructs an OSB UnbindRequest for the given ServiceBinding.
func buildUnbindRequest(binding *v1alpha1.ServiceBinding, data bindingData, oid *osb.OriginatingIdentity) *osb.UnbindRequest {
	return &osb.UnbindRequest{
		InstanceID:          data.instanceData.InstanceId,
		BindingID:           meta.GetExternalName(binding),
		AcceptsIncomplete:   true, // TODO: make configurable
		ServiceID:           data.instanceData.ServiceId,
		PlanID:              data.instanceData.PlanId,
		OriginatingIdentity: oid,
	}
}
