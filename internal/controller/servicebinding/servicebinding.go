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

	"github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/features"

	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
)

const (
	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	referenceFinalizerName = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"
)

var (
	errNotServiceBindingCR                      = errors.New("managed resource is not a ServiceBinding custom resource")
	errCannotRegisterMRStateRecorder            = errors.New("cannot register MR state metrics recorder for kind")
	errCannotTrackProviderConfig                = errors.New("cannot track ProviderConfig usage")
	errCannotGetCredentials                     = errors.New("cannot get credentials")
	errCannotCreateNewOsbClient                 = errors.New("cannot create new OSB client")
	errCannotMakeOriginatingIdentity            = errors.New("cannot make originating identity from value")
	errCannotGetBindingData                     = errors.New("cannot get data from ServiceBinding")
	errCannotAddFinalizer                       = errors.New("cannot add finalizer to referenced resource")
	errCannotRemoveFinalizer                    = errors.New("cannot remove finalizer from referenced resource")
	errOSBBindRequestFailed                     = errors.New("OSB Bind request failed")
	errOSBBindongIdIsNil                        = errors.New("OSB Bind request returned nil response for bindingID")
	errOSBRotatingRequestFailed                 = errors.New("OSB RotateBinding request failed")
	errOSBUnbindRequestFailed                   = errors.New("OSB Unbind request failed")
	errOSBUnbindNilResponse                     = errors.New("OSB Unbind returned nil response for request")
	errOSBPollBindingLastOperationRequestFailed = errors.New("OSB PollBindingLastOperation request failed")
	errStatusSpecMismatch                       = errors.New("status and spec provider parameters differ")
	errStatusSpecCompareFailed                  = errors.New("failed to compare status and spec provider")
	errRetrieveBindingDataFailed                = errors.New("failed to retrieve binding data")
	errConvertBindingSpecFailed                 = errors.New("failed to convert binding spec data")
	errExtractCredsFailed                       = errors.New("failed to extract credentials from OSB response")
	errCreateRespDataFailed                     = errors.New("failed to create response data with binding parameters")
	errUpdateStatusFailed                       = errors.New("failed to update binding status")
	errAddAsyncDeletionFinalizer                = errors.New("failed to add async deletion finalizer")
	errTechnicalEncountered                     = errors.New("technical error encountered")
	errNilBindingResponse                       = errors.New("GetBindingResponse is nil for binding")
	errNilBindingMetadataResponse               = errors.New("GetBindingResponse.Metadata is nil for binding")
	errUpdateBindingStatus                      = errors.New("failed to update binding status")
	errMarshalCredentials                       = errors.New("failed to marshal credentials from response")
	errFailedToBuildBindRequest                 = errors.New("failed to build bind request")
	errFailedToBuildUnbindRequest               = errors.New("failed to build unbind request")
	errFailedToBuildBindingLastOperationRequest = errors.New("failed to build binding last operation request")
	errFailedToBuildGetBindingRequest           = errors.New("failed to build get binding request")
)

// Setup adds a controller that reconciles ServiceBinding managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceBindingGroupKind)
	log := o.Logger.WithValues("controller", name)

	extra := &common.KubernetesOSBOriginatingIdentityExtra{}
	if err := extra.FromMap(mgr.GetConfig().Impersonate.Extra); err != nil {
		log.Info("Failed to parse KubernetesOSBOriginatingIdentityExtra from Impersonate.Extra")
	} else {
		log.Info("Successfully parsed KubernetesOSBOriginatingIdentityExtra")
		log.Debug("Successfully parsed KubernetesOSBOriginatingIdentityExtra", "extra", extra)
	}

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			originatingIdentityValue: common.KubernetesOSBOriginatingIdentityValue{
				Username: mgr.GetConfig().Impersonate.UserName,
				UID:      mgr.GetConfig().Impersonate.UID,
				Groups:   mgr.GetConfig().Impersonate.Groups,
				Extra:    extra,
			},
			newOsbClient:  util.NewOsbClient,
			rotateBinding: o.Features.Enabled(features.EnableAlphaRotateBindings),
		}),
		managed.WithLogger(log),
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
			return fmt.Errorf("%w: %s: %v", errCannotRegisterMRStateRecorder, v1alpha1.ServiceBindingGroupVersionKind.Kind, fmt.Sprint(err))
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
	usage                    util.ModernTracker
	newOsbClient             func(config resource.ProviderConfig, creds []byte) (osbClient.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
	rotateBinding            bool
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	obj, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return nil, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)

	}

	if err := c.usage.Track(ctx, mg.(resource.ModernManaged)); err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotTrackProviderConfig, fmt.Sprint(err))
	}

	pc, pcSpec, err := util.ResolveProviderConfig(ctx, c.kube, obj)
	if err != nil {
		return nil, err
	}

	// Extract credentials from the ProviderConfig
	creds, err := resource.CommonCredentialExtractor(ctx, pcSpec.Credentials.Source, c.kube, pcSpec.Credentials.CommonCredentialSelectors)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotGetCredentials, fmt.Sprint(err))
	}

	// Create a new OSB client using the resolved ProviderConfig and extracted credentials
	osbClient, err := util.NewOsbClient(pc, creds)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotCreateNewOsbClient, fmt.Sprint(err))
	}

	// Create the originating identity object
	oid, err := util.MakeOriginatingIdentityFromValue(c.originatingIdentityValue)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotMakeOriginatingIdentity, fmt.Sprint(err))
	}

	return &external{
		osb:                 osbClient,
		kube:                c.kube,
		originatingIdentity: *oid,
		rotateBinding:       c.rotateBinding,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	osb                 osbClient.Client
	kube                client.Client
	originatingIdentity osbClient.OriginatingIdentity
	rotateBinding       bool
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) { //nolint:gocyclo // See note below.
	// NOTE: This method is over our cyclomatic complexity goal.
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)
	}

	// Manage pending async operations (poll only for "in progress" state)
	if binding.IsStateInProgress() {
		return c.handleLastOperationInProgress(ctx, binding, binding.Spec.ForProvider)
	}

	// If the resource has no external name, it does not exist
	if binding.IsExternalNameEmpty() {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	// get binding from broker
	req, err := binding.BuildGetBindingRequest(binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errFailedToBuildGetBindingRequest, fmt.Sprint(err))
	}

	resp, err := c.osb.GetBinding(req)

	// Manage errors, if it's http error and 404 , then it means that the resource does not exist
	if err != nil {
		if util.IsResourceGone(err) {
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil
		}
		// Other errors are unexpected
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errOSBBindRequestFailed, fmt.Sprint(err))
	}

	data := binding.CreateResponseData(*resp)
	// Set observed fields values in cr.Status.AtProvider
	if err = binding.SetResponseDataInStatus(data); err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errUpdateBindingStatus, fmt.Sprint(err))
	}

	var credentialsJson map[string][]byte = nil
	if resp.Credentials != nil {
		credentialsJson, err = util.MarshalMapValues(resp.Credentials)
		if err != nil {
			return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errMarshalCredentials, fmt.Sprint(err))
		}
	}

	// Manage binding rotation
	eo, err1, shouldReturn := handleRenewalBindings(resp, binding, c)
	if shouldReturn {
		return eo, err1
	}

	// Compare observed to response
	// If there is a diff, return an error, since bindings are not updatable
	isStatusParametersNotLikeSpecParameters, err := binding.IsStatusParametersNotLikeSpecParameters()
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errStatusSpecCompareFailed, fmt.Sprint(err))
	}

	if isStatusParametersNotLikeSpecParameters {
		return managed.ExternalObservation{}, errStatusSpecMismatch
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
		return managed.ExternalCreation{}, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)

	}

	// Convert OSB context and parameters from the spec.
	requestContext, requestParams, err := binding.ConvertSpecsData()
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errConvertBindingSpecFailed, fmt.Sprint(err))
	}

	// Build OSB BindRequest.
	req, err := binding.BuildBindRequest(binding.Spec.ForProvider, c.originatingIdentity, requestContext, requestParams)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w, %s", errFailedToBuildBindRequest, fmt.Sprint(err))
	}

	// Execute the OSB bind request and handle async responses.
	resp, creation, err, shouldReturn := handleBindRequest(c, req, binding)
	if shouldReturn {
		return creation, err
	}

	// Extract connection credentials from the OSB response.
	creds, err := util.GetCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errExtractCredsFailed, fmt.Sprint(err))
	}

	data, err := binding.CreateResponseDataWithBindingParameters(*resp)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errCreateRespDataFailed, fmt.Sprint(err))
	}

	// Update binding status based on OSB response data.
	if err := binding.SetResponseDataInStatus(data); err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errUpdateStatusFailed, fmt.Sprint(err))
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
		return managed.ExternalUpdate{}, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)
	}

	// Trigger binding rotation.
	// We count on the next reconciliation to update renew_before and expires_at (Observe)
	creds, err := binding.TriggerRotation(c.osb, binding.Spec.ForProvider, c.originatingIdentity)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("%w: %s", errOSBRotatingRequestFailed, fmt.Sprint(err))
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
		return managed.ExternalDelete{}, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)
	}

	req, err := binding.BuildUnbindRequest(binding.Spec.ForProvider, c.originatingIdentity)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("%w, %s", errFailedToBuildUnbindRequest, fmt.Sprint(err))
	}

	resp, err := c.osb.Unbind(req)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("%w: %s", errOSBUnbindRequestFailed, fmt.Sprint(err))
	}
	if resp == nil {
		return managed.ExternalDelete{}, fmt.Errorf("%w: %s", errOSBUnbindNilResponse, fmt.Sprint(err))
	}

	if resp.Async {
		util.HandleAsyncStatus(binding, resp.OperationKey)

		if err := c.handleFinalizer(ctx, binding, asyncDeletionFinalizer, util.AddFinalizerIfNotExists); err != nil {
			return managed.ExternalDelete{}, fmt.Errorf("%w: %s", errAddAsyncDeletionFinalizer, fmt.Sprint(err))
		}

		return managed.ExternalDelete{
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	// not implemented
	return nil
}

// handleLastOperationInProgress polls the last operation for a ServiceBinding that is currently in progress.
// It updates the binding status based on the OSB response, handles async finalizers, and returns
// a managed.ExternalObservation indicating whether the resource still exists.
func (c *external) handleLastOperationInProgress(ctx context.Context, binding *v1alpha1.ServiceBinding, bindingData v1alpha1.ServiceBindingParameters) (managed.ExternalObservation, error) {
	req, err := binding.BuildBindingLastOperationRequest(bindingData, c.originatingIdentity)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errFailedToBuildBindingLastOperationRequest, fmt.Sprint(err))
	}

	resp, err := c.osb.PollBindingLastOperation(req)
	if err != nil {
		if handled, obs, err := c.handlePollError(ctx, binding, err); handled {
			return obs, err
		}
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errOSBPollBindingLastOperationRequestFailed, fmt.Sprint(err))
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
			return true, managed.ExternalObservation{}, fmt.Errorf("%w: %s", errTechnicalEncountered, fmt.Sprint(err))
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
func handleRenewalBindings(resp *osbClient.GetBindingResponse, binding *v1alpha1.ServiceBinding, c *external) (managed.ExternalObservation, error, bool) {
	if resp == nil {
		// Defensive: should never happen, but prevents future panics
		return managed.ExternalObservation{}, fmt.Errorf("%w:  %s/%s", errNilBindingResponse, binding.GetNamespace(), binding.GetName()), false
	}

	if resp.Metadata == nil {
		// Broker response is valid but missing metadata; cannot process renewal
		return managed.ExternalObservation{}, fmt.Errorf("%w:  %s/%s", errNilBindingMetadataResponse, binding.GetNamespace(), binding.GetName()), false
	}

	if resp.Metadata.RenewBefore == "" {
		// Metadata present, but no renewal data
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
	bindRequest osbClient.BindRequest,
	binding *v1alpha1.ServiceBinding,
) (*osbClient.BindResponse, managed.ExternalCreation, error, bool) {

	resp, err := c.osb.Bind(&bindRequest)
	if err != nil {
		return nil, managed.ExternalCreation{}, fmt.Errorf("%w: %s", errOSBBindRequestFailed, fmt.Sprint(err)), true
	}

	if resp == nil {
		return nil, managed.ExternalCreation{}, fmt.Errorf("%w: %s", errOSBBindongIdIsNil, fmt.Sprint(err)), true
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

type finalizerModFn func(metav1.Object, string) bool

// handleFinalizer runs kube.Update(ctx, obj) if the finalizerMod function returns true with finalizerName
func (c *external) handleFinalizer(ctx context.Context, obj client.Object, finalizerName string, finalizerMod finalizerModFn) error {
	// Get referenced service instance, if exists
	if finalizerMod(obj, finalizerName) {
		return c.kube.Update(ctx, obj)
	}
	return nil
}
