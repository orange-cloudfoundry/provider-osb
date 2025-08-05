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
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
)

const (
	errNotServiceBinding     = "managed resource is not a ServiceBinding custom resource"
	errTrackPCUsage          = "cannot track ProviderConfig usage"
	errGetPC                 = "cannot get ProviderConfig"
	errGetCreds              = "cannot get credentials"
	errGetReferencedResource = "cannot get referenced resource"

	errNewClient = "cannot create new Service"

	errTechnical  = "error: technical error encountered : %s"
	errNoResponse = "no errors but the response sent back was empty for request: %v"

	errAddReferenceFinalizer = "cannot add finalizer to referenced resource"

	errCannotParseCredentials = "cannot parse credentials"
	errGetDataFromBinding     = "cannot get data from service binding"
	errRequestFailed          = "OSB %s request failed"
	errParseMarshall          = "error while marshalling or parsing %s"

	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	objFinalizerName       = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"
)

var (
	// TODO: actually implement an no op client
	// The osb fake client returns errors for every function which is not explicitely implemented
	// so we don't want to use it here
	newNoOpService = func(_ []byte) (osb.Client, error) {
		return osb.NewClient(&osb.ClientConfiguration{
			Name:           "NoOp",
			TimeoutSeconds: 30,
		})
	}
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
			newServiceFn: util.NewOsbClient,
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
	newServiceFn             func(url string, creds []byte) (osb.Client, error)
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

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	cd := pc.Spec.Credentials
	creds, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	// Build osb client
	osbclient, err := c.newServiceFn(pc.Spec.BrokerURL, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	// Build originating identity for client
	c.originatingIdentityValue.Extra = &pc.Spec.OriginatingIdentityExtraData
	oid, err := util.MakeOriginatingIdentityFromValue(c.originatingIdentityValue)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{client: osbclient, kube: c.kube, originatingIdentity: *oid}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	client              osb.Client
	kube                client.Client
	originatingIdentity osb.OriginatingIdentity
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotServiceBinding)
	}

	bindingData, err := c.getDataFromServiceBinding(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetDataFromBinding)
	}

	// Manage pending async operations (poll only for "ip progress" state)
	if cr.Status.AtProvider.LastOperationState == osb.StateInProgress {
		// TODO use poll delay using resp.pollDelay

		// Poll last operation
		lastOpReq := &osb.BindingLastOperationRequest{
			InstanceID:          bindingData.instanceData.InstanceId,
			BindingID:           cr.Status.AtProvider.Uuid,
			ServiceID:           &bindingData.instanceData.ServiceId,
			PlanID:              &bindingData.instanceData.PlanId,
			OriginatingIdentity: &c.originatingIdentity,
			OperationKey:        &cr.Status.AtProvider.LastOperationKey,
		}

		resp, err := c.client.PollBindingLastOperation(lastOpReq)

		// Manage errors // TODO factorize
		if err != nil {
			// HTTP error 410 means that the resource was deleted by the broker
			// so if the resource on the cluster was effectively deleted,
			// we can remove its finalizer
			if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
				if httpErr.StatusCode == 410 && meta.WasDeleted(cr) {
					// Remove finalizer, and return resourceexists: false explicitely
					// This will trigger the removal of crossplane runtime's finalizers
					meta.RemoveFinalizer(cr, asyncDeletionFinalizer)
					return managed.ExternalObservation{
						ResourceExists: false,
					}, nil
				}
			}
			// Other errors should be considered as failures
			return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "PollBindingLastOperation"))
		}

		// Set polled operation data in resource status
		cr.Status.AtProvider.LastOperationState = resp.State
		if resp.Description != nil {
			cr.Status.AtProvider.LastOperationDescription = *resp.Description
		}
		cr.Status.AtProvider.LastOperationPolledTime = time.Now().Format(util.Iso8601dateFormat)

		// Requeue, waiting for operation treatment
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil
	}

	// get binding from broker
	req := &osb.GetBindingRequest{
		InstanceID: bindingData.instanceData.InstanceId,
		BindingID:  cr.Status.AtProvider.Uuid,
	}

	resp, err := c.client.GetBinding(req)

	// Manage errors // TODO factorize
	if err != nil {
		// Handle HTTP errors
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			// If 404, then it means that the resource does not exist
			if httpErr.StatusCode == http.StatusNotFound {
				return managed.ExternalObservation{
					ResourceExists: false,
				}, nil
			}
		}
		return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "GetBinding"))
	}

	// Set observed fields values in cr.Status.AtProvider
	if err = c.setResponseDataInStatus(cr, responseData{
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
	if resp.Metadata.RenewBefore != "" {
		// TODO put this in update function
		renewBeforeTime, err := time.Parse(util.Iso8601dateFormat, resp.Metadata.RenewBefore)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "renewBefore time"))
		}
		// TODO do it if an auto renew feature flag is enabled
		// else issue a warning on the resource if should be renewed, and different warning if expired
		if renewBeforeTime.Before(time.Now()) {
			// Trigger binding rotation.
			// We count on the next reconciliation to update renew_before and expires_at
			creds, err := c.triggerRotation(cr, bindingData)
			if err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "RotateBinding"))
			}
			// We update creds only if the function returned credentials
			// i.e. the operations was synchronous and successful
			if creds != nil {
				credentialsJson = creds
			}
		}
	}

	isEqual, err := compareToObserved(cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if !isEqual {
		return managed.ExternalObservation{}, errors.Wrap(err, "bindings cannot be updated")
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: credentialsJson,
	}, nil
}

type responseData struct {
	Parameters      map[string]any
	Endpoints       *[]osb.Endpoint
	VolumeMounts    *[]osb.VolumeMount
	RouteServiceURL *string
	SyslogDrainURL  *string
	Metadata        *osb.BindingMetadata
}

// set AtProviderGet updates the Observed status of a ServiceBinding according to
// a response we got from a GET request to an OSB broker
func (c *external) setResponseDataInStatus(cr *v1alpha1.ServiceBinding, data responseData) error {
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

	return c.setAtProvider(cr, v1alpha1.ServiceBindingObservation{
		// Update attributes from response data
		Parameters:      common.SerializableParameters(params),
		RouteServiceURL: data.RouteServiceURL,
		Endpoints:       v1alpha1.SerializableEndpoints(endpoints),
		VolumeMounts:    v1alpha1.SerializableVolumeMounts(volumeMounts),
		SyslogDrainURL:  data.SyslogDrainURL,
		Metadata:        data.Metadata,
		// Do not change these attributes
		Uuid:                     cr.Status.AtProvider.Uuid,
		LastOperationState:       cr.Status.AtProvider.LastOperationState,
		LastOperationKey:         cr.Status.AtProvider.LastOperationKey,
		LastOperationDescription: cr.Status.AtProvider.LastOperationDescription,
		LastOperationPolledTime:  cr.Status.AtProvider.LastOperationPolledTime,
	})
}

func (c *external) setAtProvider(cr *v1alpha1.ServiceBinding, observation v1alpha1.ServiceBindingObservation) error {
	cr.Status.AtProvider = observation
	// TODO add some checks here
	return nil
}

func (c *external) triggerRotation(cr *v1alpha1.ServiceBinding, data bindingData) (map[string][]byte, error) {
	resp, err := c.client.RotateBinding(&osb.RotateBindingRequest{
		InstanceID:           data.instanceData.InstanceId,
		BindingID:            string(uuid.NewUUID()),
		AcceptsIncomplete:    true, // TODO use config param for this
		PredecessorBindingID: cr.Status.AtProvider.Uuid,
		OriginatingIdentity:  &c.originatingIdentity,
	})
	if err != nil {
		return nil, err
	}

	// We only return ConnectionDetails in case the operation is synchronous.
	if !resp.Async {
		creds, err := getCredsFromResponse(resp)
		if err != nil {
			return nil, err
		}

		return creds, nil
	}
	return nil, nil
}

func compareToObserved(cr *v1alpha1.ServiceBinding) (bool, error) {
	// We do not compare credentials, as this logic is managed by creds rotation.
	// We do not compare bindingmetadata either, since the only metadata in binding objects
	// is related to binding rotation (renew_before and expires_at)

	// TODO add context and route to test if these were updated and return false
	return reflect.DeepEqual(cr.Status.AtProvider.Parameters, cr.Spec.ForProvider.Parameters), nil
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

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotServiceBinding)
	}

	// Prepare binding creation request
	bindingData, err := c.getDataFromServiceBinding(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errGetDataFromBinding)
	}

	spec := cr.Spec.ForProvider

	// Convert spec.Context of type common.KubernetesOSBContext to map[string]any
	requestContextBytes, err := json.Marshal(spec.Context)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "context from ServiceBinding"))
	}
	var requestContext map[string]any
	err = json.Unmarshal(requestContextBytes, &requestContext)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "context to bytes from ServiceBinding"))
	}

	// Convert spec.Parameters of type *apiextensions.JSON to map[string]any
	var requestParams map[string]any
	err = json.Unmarshal([]byte(spec.Parameters), &requestParams)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters to bytes from ServiceBinding"))
	}

	newUuid := string(uuid.NewUUID())

	cr.Status.AtProvider.Uuid = newUuid

	bindRequest := osb.BindRequest{
		BindingID:  newUuid,
		InstanceID: bindingData.instanceData.InstanceId,
		BindResource: &osb.BindResource{
			AppGUID: &bindingData.applicationData.Guid,
			Route:   &cr.Spec.ForProvider.Route,
		},
		AcceptsIncomplete:   true, // TODO: add a config flag to enable async requests or not
		OriginatingIdentity: &c.originatingIdentity,
		PlanID:              cr.Spec.ForProvider.InstanceData.PlanId,
		Context:             requestContext,
		Parameters:          requestParams,
		ServiceID:           cr.Spec.ForProvider.InstanceData.ServiceId,
		AppGUID:             &bindingData.applicationData.Guid,
	}

	// Request binding creation
	resp, err := c.client.Bind(&bindRequest)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "Bind"))
	}
	if resp == nil {
		return managed.ExternalCreation{}, fmt.Errorf(errNoResponse, bindRequest)
	}

	if resp.Async {
		// If the creation was asynchronous, add an annotation
		if resp.OperationKey != nil {
			cr.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		cr.Status.AtProvider.Uuid = newUuid
		cr.Status.AtProvider.LastOperationState = osb.StateInProgress
		return managed.ExternalCreation{
			// AdditionalDetails is for logs
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	creds, err := getCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	// Set values for endpoints, volumes, syslog drain urls
	// Avoid rewriting parameters since they are not included in the response
	params, err := cr.Status.AtProvider.Parameters.ToParameters()
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters from resource status"))
	}

	c.setResponseDataInStatus(cr, responseData{
		Parameters:      params,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	})

	return managed.ExternalCreation{
		ConnectionDetails: creds,
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Calling this function means that the resources on the cluster and from the broker are different.
	// Anything that is changed should trigger an error, since bindings are never updatable.
	// The only situation when calling this function is valid is when
	// the service binding's should be rotated

	_, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotServiceBinding)
	}

	// TODO rotate bindings here

	// If it is async, the Observe() function will manage the ConnectionDetails instead.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotServiceBinding)
	}

	fmt.Printf("Deleting: %+v", cr)

	spec := cr.Spec.ForProvider

	bindingData, err := c.getDataFromServiceBinding(ctx, spec)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, errGetDataFromBinding)
	}

	reqUnbind := osb.UnbindRequest{
		InstanceID:          bindingData.instanceData.InstanceId,
		BindingID:           cr.Status.AtProvider.Uuid,
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

	if resp.Async {
		// If the creation was asynchronous, add a finalizer
		if resp.OperationKey != nil {
			cr.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		cr.Status.AtProvider.LastOperationState = osb.StateInProgress
		meta.AddFinalizer(cr, asyncDeletionFinalizer)
		return managed.ExternalDelete{
			// AdditionalDetails is for logs
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	// unimplemented
	return nil
}

type refFinalizerFn func(context.Context, *unstructured.Unstructured, string) error

func (c *external) handleRefFinalizer(ctx context.Context, mg *v1alpha1.ServiceBinding, finalizerFn refFinalizerFn, ignoreNotFound bool) error {
	// Get referenced service instance, if exists

	instanceRef := mg.Spec.ForProvider.InstanceRef
	if instanceRef != nil {
		var instanceObject unstructured.Unstructured // should always be of type ServiceInstance
		err := c.kube.Get(ctx, instanceRef.ToObjectKey(), &instanceObject)

		if err != nil {
			if !ignoreNotFound || !kerrors.IsNotFound(err) {
				return errors.Wrap(err, errGetReferencedResource)
			}
		}

		finalizerName := bindingMetadataPrefix + string(mg.UID)
		if err = finalizerFn(ctx, &instanceObject, finalizerName); err != nil {
			return err
		}
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
		err := c.kube.Get(ctx, spec.ApplicationRef.ToObjectKey(), &application)
		if err != nil {
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
		err := c.kube.Get(ctx, instanceSpec.ApplicationRef.ToObjectKey(), &application)
		if err != nil {
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
		err := c.kube.Get(ctx, spec.InstanceRef.ToObjectKey(), &instance)
		if err != nil {
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

type callbackFn func(*v1alpha1.ServiceBinding, *osb.GetBindingResponse) error
