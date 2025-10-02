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
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/connection"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/application/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/features"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

const (
	errNotServiceBinding     = "managed resource is not a ServiceBinding custom resource"
	errTrackPCUsage          = "cannot track ProviderConfig usage"
	errGetPC                 = "cannot get ProviderConfig"
	errGetCreds              = "cannot get credentials"
	errGetReferencedResource = "cannot get referenced resource"

	errNewClient = "cannot create new Service"

	errTechnical    = "error: technical error encountered : %s"
	errNoResponse   = "no errors but the response sent back was empty for request: %v"
	errStatusUpdate = "error: Cannot update status of service bindings resources: %v"

	errAddReferenceFinalizer    = "cannot add finalizer to referenced resource"
	errRemoveReferenceFinalizer = "cannot remove finalizer from referenced resource"

	errCannotParseCredentials = "cannot parse credentials"
	errGetDataFromBinding     = "cannot get data from service binding"
	errRequestFailed          = "OSB %s request failed"
	errParseMarshall          = "error while marshalling or parsing %s"
	errBindingExpired         = "binding has expired at %s"

	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	referenceFinalizerName = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"
)

// Setup adds a controller that reconciles ServiceBinding managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceBindingGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			originatingIdentityValue: common.KubernetesOSBOriginatingIdentityValue{
				Username: mgr.GetConfig().Impersonate.UserName,
				UID:      mgr.GetConfig().Impersonate.UID,
				Groups:   mgr.GetConfig().Impersonate.Groups,
			},
			newServiceFn:  util.NewOsbClient,
			rotateBinding: o.Features.Enabled(features.EnableAlphaRotateBindings),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...),
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
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.TestKindList")
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
	usage                    resource.Tracker
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
	newServiceFn             func(config apisv1alpha1.ProviderConfig, creds []byte) (osb.Client, error)
	rotateBinding            bool
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return nil, errors.New(errNotServiceBinding)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc, err := util.GetProviderConfig(ctx, c.kube, cr.Spec.ForProvider.ApplicationData.ProviderConfig)
	if err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	cd := pc.Spec.Credentials
	creds, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	// Build osb client
	osbclient, err := c.newServiceFn(*pc, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	// Build originating identity for client
	c.originatingIdentityValue.Extra = &pc.Spec.OriginatingIdentityExtraData
	oid, err := util.MakeOriginatingIdentityFromValue(c.originatingIdentityValue)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{client: osbclient, kube: c.kube, originatingIdentity: *oid, rotateBinding: c.rotateBinding}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	client              osb.Client
	kube                client.Client
	originatingIdentity osb.OriginatingIdentity
	rotateBinding       bool
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) { //nolint:gocyclo // See note below.
	// NOTE: This method is over our cyclomatic complexity goal.
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotServiceBinding)
	}

	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetDataFromBinding)
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

	resp, err := c.client.GetBinding(req)

	// Manage errors, if it's http error and 404 , then it means that the resource does not exist
	eo, err, shouldReturn := util.HandleHttpError(err, "GetBinding")
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
			return managed.ExternalObservation{}, errors.Wrap(err, errCannotParseCredentials)
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
		return managed.ExternalObservation{}, errors.Wrap(err, "bindings cannot be updated")
	}

	// Handle the ServiceInstance referenced by this binding, if there is one.
	// We add a finalizer to prevent deletion of the ServiceInstance while the current
	// ServiceBinding still exists.
	// Doing so in the Observe() function enable adding a ServiceInstance
	// resource after the creation of the ServiceBinding.
	if err = c.addRefFinalizer(ctx, binding); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errAddReferenceFinalizer)
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: credentialsJson,
	}, nil
}

func handleRenewalBindings(resp *osb.GetBindingResponse, binding *v1alpha1.ServiceBinding, c *external) (managed.ExternalObservation, error, bool) {
	if resp.Metadata != nil && resp.Metadata.RenewBefore != "" {
		renewBeforeTime, err := time.Parse(util.Iso8601dateFormat, resp.Metadata.RenewBefore)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "renewBefore time")), true
		}

		// If the binding should be rotated, set ResourceUpToDate as false to trigger update
		if renewBeforeTime.Before(time.Now()) {
			expireAtTime, err := time.Parse(util.Iso8601dateFormat, resp.Metadata.ExpiresAt)
			if err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "expireAt time")), true
			}
			expired := false
			if expireAtTime.Before(time.Now()) {
				cond := xpv1.Condition{
					Type:    xpv1.TypeHealthy,
					Status:  v1.ConditionFalse,
					Message: fmt.Sprintf("warning : the binding has expired %s", expireAtTime.Format(util.Iso8601dateFormat)),
				}
				binding.SetConditions(cond)
				expired = true
			}
			if c.rotateBinding {
				return managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				}, nil, true
			} else if !expired {
				cond := xpv1.Condition{
					Type:    xpv1.TypeHealthy,
					Status:  v1.ConditionFalse,
					Message: fmt.Sprintf("warning : the binding will expire soon %s", expireAtTime.Format(util.Iso8601dateFormat)),
				}
				binding.SetConditions(cond)
			}
		}
	}
	return managed.ExternalObservation{}, nil, false
}

func (c *external) handleLastOperationInProgress(ctx context.Context, binding *v1alpha1.ServiceBinding, bindingData bindingData) (managed.ExternalObservation, error) {
	// TODO use poll delay using resp.pollDelay

	// Poll last operation
	lastOpReq := &osb.BindingLastOperationRequest{
		InstanceID:          bindingData.instanceData.InstanceId,
		BindingID:           meta.GetExternalName(binding),
		ServiceID:           &bindingData.instanceData.ServiceId,
		PlanID:              &bindingData.instanceData.PlanId,
		OriginatingIdentity: &c.originatingIdentity,
		OperationKey:        &binding.Status.AtProvider.LastOperationKey,
	}

	resp, err := c.client.PollBindingLastOperation(lastOpReq)

	// Manage errors
	if err != nil {
		// HTTP error 410 means that the resource was deleted by the broker
		// so if the resource on the cluster was effectively deleted,
		// we can remove its finalizer
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			if httpErr.StatusCode == http.StatusGone && meta.WasDeleted(binding) {
				// Remove async finalizer from binding
				if err = c.handleFinalizer(ctx, binding, asyncDeletionFinalizer, util.RemoveFinalizerIfExists); err != nil {
					return managed.ExternalObservation{}, errors.Wrap(err, errTechnical)
				}
				// Remove reference finalizer from referenced ServiceInstance
				if err = c.removeRefFinalizer(ctx, binding); err != nil {
					return managed.ExternalObservation{}, errors.Wrap(err, errRemoveReferenceFinalizer)
				}
				// return resourceexists: false explicitly
				// This will trigger the removal of crossplane runtime's finalizers
				return managed.ExternalObservation{
					ResourceExists: false,
				}, nil
			}
		}
		// Other errors should be considered as failures
		return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "PollBindingLastOperation"))
	}

	// Set polled operation data in resource status
	binding.Status.AtProvider.LastOperationState = resp.State
	if resp.Description != nil {
		binding.Status.AtProvider.LastOperationDescription = *resp.Description
	} else {
		binding.Status.AtProvider.LastOperationDescription = ""
	}
	binding.Status.AtProvider.LastOperationPolledTime = *util.TimeNow()

	if err = c.kube.Status().Update(ctx, binding); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errStatusUpdate)
	}

	// Requeue, waiting for operation treatment
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotServiceBinding)
	}

	// Prepare binding creation request
	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errGetDataFromBinding)
	}

	spec := binding.Spec.ForProvider

	// Convert spec.Context and spec.Parameters of type common.KubernetesOSBContext to map[string]any
	requestContext, requestParams, err := convertSpecsData(spec)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	bindingUuid := meta.GetExternalName(binding)
	// Immediately set the binding's external name with a new UUID if it did not have one,
	// even before creation is a success
	if meta.GetExternalName(binding) == "" {
		bindingUuid := string(uuid.NewUUID())
		meta.SetExternalName(binding, bindingUuid)
	}

	bindRequest := osb.BindRequest{
		BindingID:  bindingUuid,
		InstanceID: bindingData.instanceData.InstanceId,
		BindResource: &osb.BindResource{
			AppGUID: &bindingData.applicationData.Guid,
			Route:   &binding.Spec.ForProvider.Route,
		},
		AcceptsIncomplete:   true, // TODO: add a config flag to enable async requests or not
		OriginatingIdentity: &c.originatingIdentity,
		PlanID:              binding.Spec.ForProvider.InstanceData.PlanId,
		Context:             requestContext,
		Parameters:          requestParams,
		ServiceID:           binding.Spec.ForProvider.InstanceData.ServiceId,
		AppGUID:             &bindingData.applicationData.Guid,
	}

	// Request binding creation
	resp, ec, err, shouldReturn := handleBindRequest(c, bindRequest, binding)
	if shouldReturn {
		return ec, err
	}

	creds, err := getCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	// Set values for endpoints, volumes, syslog drain urls
	// Avoid rewriting parameters since they are not included in the response
	params, err := binding.Status.AtProvider.Parameters.ToParameters()
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters from resource status"))
	}

	err = c.setResponseDataInStatus(binding, responseData{
		Parameters:      params,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	})

	if err != nil {
		return managed.ExternalCreation{}, err
	}

	return managed.ExternalCreation{
		ConnectionDetails: creds,
	}, nil
}

func handleBindRequest(c *external, bindRequest osb.BindRequest, binding *v1alpha1.ServiceBinding) (*osb.BindResponse, managed.ExternalCreation, error, bool) {
	resp, err := c.client.Bind(&bindRequest)

	if err != nil {
		return nil, managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "Bind")), true
	}
	if resp == nil {
		return nil, managed.ExternalCreation{}, fmt.Errorf(errNoResponse, bindRequest), true
	}

	if resp.Async {
		// If the creation was asynchronous, add an annotation
		if resp.OperationKey != nil {
			binding.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		binding.Status.AtProvider.LastOperationState = osb.StateInProgress
		return nil, managed.ExternalCreation{
			// AdditionalDetails is for logs
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil, true
	}
	return resp, managed.ExternalCreation{}, nil, false
}

func convertSpecsData(spec v1alpha1.ServiceBindingParameters) (map[string]any, map[string]any, error) {
	requestContextBytes, err := json.Marshal(spec.Context)
	if err != nil {
		return nil, nil, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "context from ServiceBinding"))
	}
	var requestContext map[string]any
	if err = json.Unmarshal(requestContextBytes, &requestContext); err != nil {
		return nil, nil, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "context to bytes from ServiceBinding"))
	}

	// Convert spec.Parameters of type *apiextensions.JSON to map[string]any
	var requestParams map[string]any
	if err = json.Unmarshal([]byte(spec.Parameters), &requestParams); err != nil {
		return nil, nil, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters to bytes from ServiceBinding"))
	}
	return requestContext, requestParams, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Calling this function means that the resources on the cluster and from the broker are different.
	// Anything that is changed should trigger an error, since bindings are never updatable.
	// The only situation when calling this function is valid is when
	// the service binding's should be rotated

	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotServiceBinding)
	}

	// Prepare binding rotation request
	bindingData, err := c.getDataFromServiceBinding(ctx, binding.Spec.ForProvider)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetDataFromBinding)
	}

	// Trigger binding rotation.
	// We count on the next reconciliation to update renew_before and expires_at (Observe)
	creds, err := c.triggerRotation(binding, bindingData)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "RotateBinding"))
	}

	// nil credentials will not erase the pre-existing ones.
	// If it is async, the Observe() function will manage the ConnectionDetails instead.
	return managed.ExternalUpdate{
		ConnectionDetails: creds,
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotServiceBinding)
	}

	fmt.Printf("Deleting: %+v", binding)

	// Make the unbind request

	spec := binding.Spec.ForProvider

	bindingData, err := c.getDataFromServiceBinding(ctx, spec)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, errGetDataFromBinding)
	}

	reqUnbind := osb.UnbindRequest{
		InstanceID:          bindingData.instanceData.InstanceId,
		BindingID:           meta.GetExternalName(binding),
		AcceptsIncomplete:   true, // TODO based on async configuration
		ServiceID:           spec.InstanceData.ServiceId,
		PlanID:              spec.InstanceData.PlanId,
		OriginatingIdentity: &c.originatingIdentity,
	}

	resp, err := c.client.Unbind(&reqUnbind)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	if resp == nil {
		return managed.ExternalDelete{}, fmt.Errorf(errNoResponse, reqUnbind)
	}

	// If the deletion is asynchronous, simply update the resource status and add a finalizer
	// The finalizer will be removed (triggering the actual deletion of the resource)
	// in the Observe function
	if resp.Async {
		if resp.OperationKey != nil {
			binding.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		binding.Status.AtProvider.LastOperationState = osb.StateInProgress
		if err = c.handleFinalizer(ctx, binding, asyncDeletionFinalizer, util.AddFinalizerIfNotExists); err != nil {
			return managed.ExternalDelete{}, errors.Wrap(err, errTechnical)
		}
		return managed.ExternalDelete{
			// AdditionalDetails is for logs
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	// TODO delete referenced instance finalizer

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	// not implemented
	return nil
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
			return nil, errors.Wrap(err, "error while retrieving referenced application")
		}
		appData = &application.Spec.ForProvider
	} else if spec.ApplicationData != nil {
		// Fetch from within the service binding
		appData = spec.ApplicationData
	}
	return appData, nil
}

func (c *external) fetchApplicationDataFromInstance(ctx context.Context, instanceSpec common.InstanceData) (*common.ApplicationData, error) {
	var appData *common.ApplicationData

	if instanceSpec.ApplicationRef != nil {
		// Fetch from referenced resource
		application := applicationv1alpha1.Application{}
		if err := c.kube.Get(ctx, instanceSpec.ApplicationRef.ToObjectKey(), &application); err != nil {
			if kerrors.IsNotFound(err) {
				return nil, errors.New("binding referenced an instance which referenced an application which does not exist")
			}
			return nil, errors.Wrap(err, "error while retrieving referenced application from referenced instance")
		}
		appData = &application.Spec.ForProvider
	} else if instanceSpec.ApplicationData != nil {
		// Fetch from within the service binding
		appData = instanceSpec.ApplicationData
	}

	return appData, nil
}

func (c *external) getDataFromServiceBinding(ctx context.Context, spec v1alpha1.ServiceBindingParameters) (bindingData, error) {
	// Fetch application data
	appData, err := c.fetchApplicationDataFromBinding(ctx, spec)
	if err != nil {
		return bindingData{}, err
	}

	// Fetch instance data
	var instanceData *common.InstanceData

	if spec.InstanceRef != nil {
		// Fetch from referenced resource
		instance := instancev1alpha1.ServiceInstance{}
		if err := c.kube.Get(ctx, spec.InstanceRef.ToObjectKey(), &instance); err != nil {
			if kerrors.IsNotFound(err) {
				return bindingData{}, errors.New("binding referenced a service instance which does not exist")
			}
			return bindingData{}, errors.Wrap(err, "error while retrieving referenced service instance")
		}

		instanceSpec := instance.Spec.ForProvider
		instanceData = &instanceSpec

		// If there was not application data found from the binding directly, fetch it from the instance instead

		if appData == nil {
			appData, err = c.fetchApplicationDataFromInstance(ctx, instanceSpec)
			if err != nil {
				return bindingData{}, err
			}
		}
	} else if spec.InstanceData != nil {
		instanceData = spec.InstanceData
	}

	// Checking if we miss instance data, triggering an issue for binding usage.
	if instanceData == nil {
		return bindingData{}, errors.New("error: missing either instance data or reference, binding handling impossible.")
	}

	// Checking if we miss application data, triggering an issue for binding usage.
	if appData == nil {
		return bindingData{}, errors.New("error: missing either application data or reference, binding handling impossible.")
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
		return errors.New(fmt.Sprintf(errParseMarshall, "parameters from response"))
	}

	endpoints, err := json.Marshal(data.Endpoints)
	if err != nil {
		return errors.New(fmt.Sprintf(errParseMarshall, "endpoints from response"))
	}

	volumeMounts, err := json.Marshal(data.VolumeMounts)
	if err != nil {
		return errors.New(fmt.Sprintf(errParseMarshall, "volume mounts from response"))
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

func (c *external) triggerRotation(binding *v1alpha1.ServiceBinding, data bindingData) (map[string][]byte, error) {
	newUuid := string(uuid.NewUUID())

	resp, err := c.client.RotateBinding(&osb.RotateBindingRequest{
		InstanceID:           data.instanceData.InstanceId,
		BindingID:            newUuid,
		AcceptsIncomplete:    true, // TODO use config param for this
		PredecessorBindingID: meta.GetExternalName(binding),
		OriginatingIdentity:  &c.originatingIdentity,
	})
	if err != nil {
		return nil, err
	}

	// If the rotate request had no error, we can safely update the external name
	meta.SetExternalName(binding, newUuid)

	// We only return ConnectionDetails in case the operation is synchronous.
	if !resp.Async {
		creds, err := getCredsFromResponse(resp)
		if err != nil {
			return nil, err
		}
		return creds, nil
	} else {
		// In asynchronous workflow, just update LastOperation status
		if resp.OperationKey != nil {
			binding.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		binding.Status.AtProvider.LastOperationState = osb.StateInProgress
		binding.Status.AtProvider.LastOperationPolledTime = *util.TimeNow()
		binding.Status.AtProvider.LastOperationDescription = ""
	}

	return nil, nil
}

func compareToObserved(cr *v1alpha1.ServiceBinding) bool {
	// We do not compare credentials, as this logic is managed by creds rotation.
	// We do not compare bindingmetadata either, since the only metadata in binding objects
	// is related to binding rotation (renew_before and expires_at)

	// TODO add context and route to test if these were updated and return false
	return reflect.DeepEqual(cr.Status.AtProvider.Parameters, cr.Spec.ForProvider.Parameters)
}

func getCredsFromResponse(resp *osb.BindResponse) (map[string][]byte, error) {
	// Serialize credentials
	creds := make(map[string][]byte, len(resp.Credentials))

	for k, v := range resp.Credentials {
		credsBytes, err := json.Marshal(v)
		if err != nil {
			return map[string][]byte{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "credentials from response"))
		}
		creds[k] = credsBytes
	}

	return creds, nil
}
