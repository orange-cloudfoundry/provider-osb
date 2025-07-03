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
	"reflect"
	"slices"
	"time"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
	errNotServiceBinding           = "managed resource is not a ServiceBinding custom resource"
	errTrackPCUsage                = "cannot track ProviderConfig usage"
	errGetPC                       = "cannot get ProviderConfig"
	errGetCreds                    = "cannot get credentials"
	errNotKubernetesServiceBinding = "managed resource is not a Service Binding custom resource"
	errGetReferencedResource       = "cannot get referenced resource"

	errNewClient = "cannot create new Service"

	errTechnical  = "error: technical error enountered : %s"
	errNoResponse = "no errors but the response sent back was empty for request: %v"

	errAddReferenceFinalizer = "cannot add finalizer to referenced resource"

	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	objFinalizerName = bindingMetadataPrefix + "/service-binding"

	endpointsAnnotation      = bindingMetadataPrefix + "/endpoints"
	syslogDrainURLAnnotation = bindingMetadataPrefix + "/syslog-drain-url"
	volumeMountsAnnotation   = bindingMetadataPrefix + "/volume-mounts"
)

var (
	// TODO: actually implement an no op client
	// Using the osb.fake client is not possible since it does not
	// implement the osb.Client interface (pointer receivers prevent this)
	// Also it returns errors for every function which is not explicitely implemented
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
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
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
	kube         client.Client
	usage        resource.Tracker
	newServiceFn func(url string, creds []byte) (osb.Client, error)
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

	osbclient, err := c.newServiceFn(pc.Spec.BrokerURL, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{client: osbclient, kube: c.kube}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	client osb.Client
	kube   client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	fmt.Printf("mg: %+v", mg)
	// TODO: manage cases when credentials secret is changed
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotServiceBinding)
	}

	// These fmt statements should be removed in the real implementation.
	fmt.Printf("Observing: %+v", cr)

	bindingData, err := c.getDataFromServiceBinding(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalObservation{}, err // TODO add error for this
	}

	isAsync, ok := cr.GetAnnotations()[util.AsyncAnnotation]

	// Handle async resources
	if ok && isAsync == "true" {
		// get last operation
		lastOpRequest := osb.BindingLastOperationRequest{
			InstanceID: bindingData.InstanceData.InstanceId,
			BindingID:  cr.GetName(),
			ServiceID:  &bindingData.InstanceData.ServiceId,
			PlanID:     &bindingData.InstanceData.PlanId,
			// OperationKey: "toto", //TODO - should be a uuid ?
		}
		lastOp, err := c.client.PollBindingLastOperation(&lastOpRequest)

		// Manage error cases
		if err != nil {
			// Handle http errors
			if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
				switch httpErr.StatusCode {
				case 404:
					// Resource was not found, so return resourceexists: false to trigger creation
					// (even though a resource can only be marked as async once creation request has been sent)
					// This means that a resource that was deleted without using this provider
					// will be re-created
					return managed.ExternalObservation{
						ResourceExists:   false,
						ResourceUpToDate: false,
					}, nil
				case 410:
					// TODO: manage this case: 410 means the resource was previously deleted by the platform, and is now gone
					// The only thing we can do is assume the resource was deleted (check with meta.WasDeleted, else return error)
					// We should then remove the async finalizer here
					// TODO add an async finalizer ;)
					break
				default:
					break
				}
			}
			return managed.ExternalObservation{}, errors.Wrap(err, "PollBindingLastOperation request failed")
		}

		// Check last operation state
		switch lastOp.State {
		case osb.StateFailed:
			// If the last operation has failed, return an error
			return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf("Last operation has failed : %s", *lastOp.Description))
		case osb.StateSucceeded:
			// If the last operation has succeeded, it means that nothing is currrently ongoing
			// and we can proceed as normal
			break
		case osb.StateInProgress:
			// TODO: check if it has been pending for too long (?)
			// an operation is in progress, don't touch anything
			// return exists: true, uptodate: true to avoid triggering anything, and wait for the next observe (pollInterval)
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: true,
			}, nil
		}
	}

	// get binding from broker
	req := &osb.GetBindingRequest{
		InstanceID: bindingData.InstanceId,
		BindingID:  cr.Name,
	}

	resp, err := c.client.GetBinding(req)

	// Manage errors
	if err != nil {
		// Handle HTTP errors
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			if httpErr.StatusCode == 404 {
				if ok && isAsync == "true" {
					// return error because it means that there is a successful last operation on the binding but it does not exist, wtf
					return managed.ExternalObservation{}, errors.Wrap(err, "GetBinding returned 404, but LastOperation did not, should be impossible")
				} else {
					// Resource doesn't exist yet, return exists: false to trigger creation
					return managed.ExternalObservation{
						ResourceExists:   false,
						ResourceUpToDate: false,
					}, nil
				}
			}
		}
		return managed.ExternalObservation{}, errors.Wrap(err, "GetBinding request failed")
	}

	isDifferent, err := compareBindingResponseToCR(resp, cr)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "GetBinding request failed")
	}

	// TODO: add code to update connection credentials. Since this property is updated by the broker
	// and not by this provider, we can update the secret in this function (it should not trigger a requeue
	// since we don't have to update the CR)

	// TODO: check if renew_before is too close
	// if so, return uptodate: false anyway, and let the Update function
	// detect that the binding should be rotated
	// Also do it if an auto renew feature flag is enabled (TODO add an autorenew feature flag ;) )

	// If local CR is different from external resource, trigger update
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: !isDifferent,
		// TODO: manage connectiondetails
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func compareBindingResponseToCR(resp *osb.GetBindingResponse, cr *v1alpha1.ServiceBinding) (bool, error) {
	metadata := cr.ObjectMeta.Annotations

	// We do not compare credentials, as this logic is managed by creds rotation.
	// We do not compare metadata either, since the only metadata in binding objects
	// are related to binding rotation (renew_before and expires_at)

	// Compare endpoints
	endpointsFromCRRaw := []byte(metadata[endpointsAnnotation])
	var endpointsFromCR []osb.Endpoint
	err := json.Unmarshal(endpointsFromCRRaw, &endpointsFromCR)
	if err != nil {
		return false, fmt.Errorf(errTechnical, err.Error())
	}

	// Structs with slices are not comparable, so we have to use a custom comparison function
	// DeepEqual couldve worked too, but we don't want to care about the order of the elements
	if !slices.EqualFunc(endpointsFromCR, *resp.Endpoints, util.EndpointEqual) {
		return false, nil
	}

	// Compare volume mounts
	volumeMountsFromCRRaw := []byte(metadata[volumeMountsAnnotation])
	var volumeMountsFromCR []osb.VolumeMount
	err = json.Unmarshal(volumeMountsFromCRRaw, &volumeMountsFromCR)
	if err != nil {
		return false, fmt.Errorf(errTechnical, err.Error())
	}

	// Structs with slices are not comparable, so we have to use a custom comparison function
	// Though it only calls reflect.DeepEqual
	if !slices.EqualFunc(volumeMountsFromCR, resp.VolumeMounts, util.VolumeMountEqual) {
		return false, nil
	}

	// Compare parameters
	parametersFromCRRaw := cr.Spec.ForProvider.Parameters.Raw
	var parametersFromCR *map[string]any
	err = json.Unmarshal(parametersFromCRRaw, parametersFromCR)
	if err != nil {
		return false, fmt.Errorf(errTechnical, err.Error())
	}

	// Note @frigaut: This might not work because of interface{}, we'll see
	if !reflect.DeepEqual(resp.Parameters, cr.Spec.ForProvider.Parameters) {
		return false, nil
	}

	return cr.Spec.ForProvider.Route == *resp.RouteServiceURL && metadata[syslogDrainURLAnnotation] == *resp.SyslogDrainURL, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotServiceBinding)
	}

	fmt.Printf("Creating: %+v", cr)

	// Prepare binding creation request
	bindingData, err := c.getDataFromServiceBinding(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	spec := cr.Spec.ForProvider

	requestContextBytes, err := json.Marshal(spec.Context)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	var requestContext map[string]any
	err = json.Unmarshal(requestContextBytes, &requestContext)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	requestParamsBytes, err := json.Marshal(spec.Parameters)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	var requestParams map[string]any
	err = json.Unmarshal(requestParamsBytes, &requestParams)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	bindRequest := osb.BindRequest{
		BindingID:  cr.GetName(),
		InstanceID: bindingData.InstanceData.InstanceId,
		BindResource: &osb.BindResource{
			AppGUID: &bindingData.ApplicationData.Guid,
			Route:   &cr.Spec.ForProvider.Route,
		},
		AcceptsIncomplete:   true, // TODO: add a config flag to enable async requests or not
		OriginatingIdentity: &bindingData.originatingIdentity,
		PlanID:              cr.Spec.ForProvider.InstanceData.PlanId,
		Context:             requestContext,
		Parameters:          requestParams,
		ServiceID:           cr.Spec.ForProvider.InstanceData.ServiceId,
		AppGUID:             &bindingData.ApplicationData.Guid,
	}

	// Request binding creation
	resp, err := c.client.Bind(&bindRequest)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	if resp == nil {
		return managed.ExternalCreation{}, fmt.Errorf(errNoResponse, bindRequest)
	}

	if resp.Async {
		// Get the operation
		meta.AddAnnotations(cr, map[string]string{
			util.AsyncAnnotation: "true",
		})
		return managed.ExternalCreation{
			// AdditionalDetails is for logs
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	// Serialize credentials
	creds := make(map[string][]byte, len(resp.Credentials))

	for k, v := range resp.Credentials {
		credsBytes, err := json.Marshal(v)
		if err != nil {
			return managed.ExternalCreation{}, errors.New(errTechnical) // TODO add an error for this
		}
		creds[k] = credsBytes
	}

	return managed.ExternalCreation{
		ConnectionDetails: creds,
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// TODO implem update (binding rotation)
	// TODO if spec is different, crash because update should not work for bindings
	return managed.ExternalUpdate{}, errors.Errorf("update isn't supported for service binding type")
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
		return managed.ExternalDelete{}, err
	}

	reqUnbind := osb.UnbindRequest{
		InstanceID:          bindingData.InstanceData.InstanceId,
		BindingID:           cr.GetName(),
		AcceptsIncomplete:   true,
		ServiceID:           spec.InstanceData.ServiceId,
		PlanID:              spec.InstanceData.PlanId,
		OriginatingIdentity: &bindingData.originatingIdentity,
	}
	resp, err := c.client.Unbind(&reqUnbind)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	if resp == nil {
		return managed.ExternalDelete{}, fmt.Errorf(errNoResponse, reqUnbind)
	}

	if resp.Async {
		// Get the operation and manage the creation of the secret
		meta.AddAnnotations(cr, map[string]string{
			util.AsyncAnnotation: "true",
		})
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
	// TODO implem disconnect
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
	common.InstanceData
	common.ApplicationData
	originatingIdentity osb.OriginatingIdentity
}

func (c *external) getDataFromServiceBinding(ctx context.Context, spec v1alpha1.ServiceBindingParameters) (bindingData, error) {
	// Fetch application data
	var appData *common.ApplicationData

	if spec.ApplicationRef != nil {
		// Fetch from referenced resource
		application := applicationv1alpha1.Application{}
		err := c.kube.Get(ctx, spec.ApplicationRef.ToObjectKey(), &application)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return bindingData{}, errors.New("binding referenced an application which does not exist")
			}
			return bindingData{}, errors.Wrap(err, "error while retrieving referenced application")
		}
		appData = &application.Spec.ForProvider
	} else if spec.ApplicationData != nil {
		// Fetch from within the service binding
		appData = spec.ApplicationData
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
			if instanceSpec.ApplicationRef != nil {
				// Fetch from referenced resource
				application := applicationv1alpha1.Application{}
				err := c.kube.Get(ctx, instanceSpec.ApplicationRef.ToObjectKey(), &application)
				if err != nil {
					if kerrors.IsNotFound(err) {
						return bindingData{}, errors.New("binding referenced an instance which referenced an application which does not exist")
					}
					return bindingData{}, errors.Wrap(err, "error while retrieving referenced application from referenced instance")
				}
				appData = &application.Spec.ForProvider
			} else if instanceSpec.ApplicationData != nil {
				// Fetch from within the service binding
				appData = instanceSpec.ApplicationData
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

	originatingIdentity := osb.OriginatingIdentity{ // TODO: put originating identity in config parameter
		Platform: "",
		Value:    "",
	}

	return bindingData{*instanceData, *appData, originatingIdentity}, nil
}

type callbackFn func(*v1alpha1.ServiceBinding, *osb.GetBindingResponse) error

func (c *external) pollLastOperation(
	numberOfRepetition,
	delay int,
	request osb.BindingLastOperationRequest,
	cr *v1alpha1.ServiceBinding,
	callbackFunc callbackFn,
) error {
	for i := 0; i < numberOfRepetition; i++ {
		resp, err := c.client.PollBindingLastOperation(&request)
		if err != nil {
			return fmt.Errorf(errTechnical, err.Error())
		}
		switch resp.State {
		case osb.StateInProgress:
			time.Sleep(time.Duration(delay))
		case osb.StateFailed:
			return fmt.Errorf("error: binding operation failed for binding id : %s, instance id : %s, after %d repetition. Failure error : %s", request.BindingID, request.InstanceID, i, *resp.Description)
		case osb.StateSucceeded:
			req := osb.GetBindingRequest{
				InstanceID: request.InstanceID,
				BindingID:  request.BindingID,
			}
			resp, err := c.client.GetBinding(&req)
			if err != nil {

			}
			return callbackFunc(cr, resp)
		}
	}
	return errors.Errorf("error: max calls reached for last operation, please try again later")
}

func (c *external) ManageFollowingActions(bind *v1alpha1.ServiceBinding, bindingFromOsb *osb.GetBindingResponse) error {
	if bind != nil {
		// TODO this function should :
		//  - idk
	}
	return nil
}
