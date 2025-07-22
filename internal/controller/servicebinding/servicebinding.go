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
	"slices"
	"time"

	"github.com/pkg/errors"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

	errCannotParseCredentials  = "cannot parse credentials"
	errGetBindingRequestFailed = "GetBinding request failed"
	errGetDataFromBinding      = "cannot get data from service binding"

	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	objFinalizerName       = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"

	endpointsAnnotation      = bindingMetadataPrefix + "/endpoints"
	syslogDrainURLAnnotation = bindingMetadataPrefix + "/syslog-drain-url"
	volumeMountsAnnotation   = bindingMetadataPrefix + "/volume-mounts"
	expiresAtAnnotation      = bindingMetadataPrefix + "/expires-at"
	renewBeforeAnnotation    = bindingMetadataPrefix + "/renew-before"
	bindingIdAnnotation      = bindingMetadataPrefix + "/binding-id"
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

	isAsync, ok := cr.GetAnnotations()[util.AsyncAnnotation]

	// Handle async resources
	if ok && isAsync == "true" {
		// ASYNC IS NOT SUPPORTED YET
		return managed.ExternalObservation{}, errors.New("Async requests are not supported yet.")
		// get last operation
		// lastOpRequest := osb.BindingLastOperationRequest{
		// 	InstanceID: bindingData.InstanceData.InstanceId,
		// 	BindingID:  cr.GetName(),
		// 	ServiceID:  &bindingData.InstanceData.ServiceId,
		// 	PlanID:     &bindingData.InstanceData.PlanId,
		// 	// TODO - should be a uuid and used as annotation to track last operation
		// 	// or not becase sometimes the broker doesnt send back the operation key and it
		// 	// means that 1 async task should be ongoing at a time, fml
		// 	// OperationKey: "toto",
		// }
		// lastOp, err := c.client.PollBindingLastOperation(&lastOpRequest)

		// // Manage error cases
		// if err != nil {
		// 	// Handle http errors
		// 	if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
		// 		switch httpErr.StatusCode {
		// 		case 404:
		// 			// Resource was not found, so return resourceexists: false to trigger creation
		// 			// (even though a resource can only be marked as async once creation request has been sent)
		// 			// This means that a resource that was deleted without using this provider
		// 			// will be re-created
		// 			return managed.ExternalObservation{
		// 				ResourceExists:   false,
		// 				ResourceUpToDate: false,
		// 			}, nil
		// 		case 410:
		// 			// 410 means the resource was previously deleted by the platform, and is now gone
		// 			// The only thing we can do is assume the resource was deleted,
		// 			// then remove the async finalizer to proceed with deletion
		// 			if !meta.WasDeleted(mg) {
		// 				return managed.ExternalObservation{}, errors.New("Resource is gone from the broker (HTTP 410 code)")
		// 			}
		// 			meta.RemoveFinalizer(mg, asyncDeletionFinalizer)
		// 			break
		// 		default:
		// 			break
		// 		}
		// 	}
		// 	return managed.ExternalObservation{}, errors.Wrap(err, "PollBindingLastOperation request failed")
		// }

		// // Check last operation state
		// switch lastOp.State {
		// case osb.StateFailed:
		// 	// If the last operation has failed, return an error
		// 	return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf("Last operation has failed : %s", *lastOp.Description))
		// case osb.StateSucceeded:
		// 	// If the last operation has succeeded, it means that nothing is currrently ongoing
		// 	// and we can proceed as normal
		// 	// We still have to check if it was a deletion, in which case we remove
		// 	// the resource's finalizers to finish is deletion on the cluster
		// 	// TODO: we should also remove the async annotation here in any case
		// 	if meta.WasDeleted(mg) {
		// 		meta.RemoveFinalizer(mg, asyncDeletionFinalizer)
		// 	}
		// case osb.StateInProgress:
		// 	// TODO: check if it has been pending for too long (?)
		// 	// an operation is in progress, don't touch anything
		// 	// return exists: true, uptodate: true to avoid triggering anything, and wait for the next observe (pollInterval)
		// 	return managed.ExternalObservation{
		// 		ResourceExists:   true,
		// 		ResourceUpToDate: true,
		// 	}, nil
		// }
	}

	// get binding from broker
	req := &osb.GetBindingRequest{
		InstanceID: bindingData.InstanceId,
		BindingID:  cr.ObjectMeta.Annotations[bindingIdAnnotation],
	}

	resp, err := c.client.GetBinding(req)

	// Manage errors
	if err != nil {
		// Handle HTTP errors
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			if httpErr.StatusCode == http.StatusNotFound {
				if ok && isAsync == "true" {
					// return error because it means that there is a successful last operation on the binding but it does not exist, wtf
					return managed.ExternalObservation{}, errors.Wrap(err, "GetBinding returned 404, but LastOperation did not, should be impossible")
				} else {
					// resource doesn't exist yet, return exists: false to trigger creation
					return managed.ExternalObservation{
						ResourceExists:   false,
						ResourceUpToDate: false,
					}, nil
				}
			}
		}
		return managed.ExternalObservation{}, errors.Wrap(err, errGetBindingRequestFailed)
	}

	credentialsJson := map[string][]byte{}

	for k, v := range resp.Credentials {
		marshaled, err := json.Marshal(v)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errCannotParseCredentials)
		}
		credentialsJson[k] = marshaled
	}

	if resp.Metadata.RenewBefore != "" {
		renewBeforeTime, err := time.Parse(util.Iso8601dateFormat, resp.Metadata.RenewBefore)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, "error while parsing renewBefore time")
		}
		// TODO do it if an auto renew feature flag is enabled (TODO add an autorenew feature flag ;) )
		// else issue a warning on the resource if should be renewed, and different warning if expired
		if renewBeforeTime.Before(time.Now()) {
			// Trigger binding rotation.
			// We count on the next reconciliation to update renew_before and expires_at
			creds, err := c.triggerRotation(cr, bindingData)
			if err != nil {
				return managed.ExternalObservation{}, errors.Wrap(err, "error while rotating binding credentials")
			}
			// We update creds only if the function returned credentials
			// i.e. the operations was synchronous and successful
			if creds != nil {
				credentialsJson = creds
			}
		}
	}

	isEqual, err := compareBindingResponseToCR(resp, cr)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetBindingRequestFailed)
	}

	// If local CR is different from external resource, trigger update
	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  isEqual,
		ConnectionDetails: credentialsJson,
	}, nil
}

func (c *external) triggerRotation(cr *v1alpha1.ServiceBinding, data bindingData) (map[string][]byte, error) {
	resp, err := c.client.RotateBinding(&osb.RotateBindingRequest{
		InstanceID:           data.InstanceId,
		BindingID:            string(uuid.NewUUID()),
		AcceptsIncomplete:    false, // TODO async is not supported yet
		PredecessorBindingID: cr.ObjectMeta.Annotations[bindingIdAnnotation],
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

func compareBindingResponseToCR(resp *osb.GetBindingResponse, cr *v1alpha1.ServiceBinding) (bool, error) {
	metadata := cr.ObjectMeta.Annotations

	// We do not compare credentials, as this logic is managed by creds rotation.
	// We do not compare bindingmetadata either, since the only metadata in binding objects
	// is related to binding rotation (renew_before and expires_at)

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
	var parametersFromCR map[string]any
	err = json.Unmarshal(parametersFromCRRaw, &parametersFromCR)
	if err != nil {
		return false, fmt.Errorf(errTechnical, err.Error())
	}

	if !reflect.DeepEqual(resp.Parameters, parametersFromCR) {
		return false, nil
	}

	return cr.Spec.ForProvider.Route == *resp.RouteServiceURL && metadata[syslogDrainURLAnnotation] == *resp.SyslogDrainURL, nil
}

func getCredsFromResponse(resp *osb.BindResponse) (map[string][]byte, error) {
	// Serialize credentials
	creds := make(map[string][]byte, len(resp.Credentials))

	for k, v := range resp.Credentials {
		credsBytes, err := json.Marshal(v)
		if err != nil {
			return map[string][]byte{}, errors.Wrap(err, "error while Marshalling credentials")
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

	newUuid := string(uuid.NewUUID())

	meta.AddAnnotations(cr, map[string]string{
		bindingIdAnnotation: newUuid,
	})

	fmt.Printf("\n\n\n%+v\n\n\n", c.originatingIdentity)

	bindRequest := osb.BindRequest{
		BindingID:  newUuid,
		InstanceID: bindingData.InstanceData.InstanceId,
		BindResource: &osb.BindResource{
			AppGUID: &bindingData.ApplicationData.Guid,
			Route:   &cr.Spec.ForProvider.Route,
		},
		AcceptsIncomplete:   false, // TODO async is not supported yet TODO: add a config flag to enable async requests or not
		OriginatingIdentity: &c.originatingIdentity,
		PlanID:              cr.Spec.ForProvider.InstanceData.PlanId,
		Context:             requestContext,
		Parameters:          requestParams,
		ServiceID:           cr.Spec.ForProvider.InstanceData.ServiceId,
		AppGUID:             &bindingData.ApplicationData.Guid,
	}

	// Request binding creation
	resp, err := c.client.Bind(&bindRequest)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "Bind request failed")
	}
	if resp == nil {
		return managed.ExternalCreation{}, fmt.Errorf(errNoResponse, bindRequest)
	}

	if resp.Async {
		// TODO async is not supported yet
		return managed.ExternalCreation{}, errors.New("Async requests are not supported yet.")
		// If the creation was asynchronous, add an annotation
		// meta.AddAnnotations(cr, map[string]string{
		// 	util.AsyncAnnotation: "true",
		// })
		// return managed.ExternalCreation{
		// 	// AdditionalDetails is for logs
		// 	AdditionalDetails: managed.AdditionalDetails{
		// 		"async": "true",
		// 	},
		// }, nil
	}

	// Set annotations for endpoints, volumes, syslog drain urls
	endpointsBytes, err := json.Marshal(resp.Endpoints)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	volumeMountsBytes, err := json.Marshal(resp.VolumeMounts)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	syslogDrainURLBytes, err := json.Marshal(resp.SyslogDrainURL)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	meta.AddAnnotations(cr, map[string]string{
		endpointsAnnotation:      string(endpointsBytes),
		volumeMountsAnnotation:   string(volumeMountsBytes),
		syslogDrainURLAnnotation: string(syslogDrainURLBytes),
	})

	creds, err := getCredsFromResponse(resp)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "could not parse credentials from response")
	}

	return managed.ExternalCreation{
		ConnectionDetails: creds,
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Calling this function means that the resources on the cluster and from the broker are different.
	// Anything that is changed should trigger a resync by pulling info from the broker and
	// updating the MR accordingly. Updates to bindings are not possible in the OSB spec, so we
	// always assume that the difference comes from the broker and not from the user, and anything
	// that was changed from within the cluster should be overridden.
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotServiceBinding)
	}

	bindingData, err := c.getDataFromServiceBinding(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetDataFromBinding)
	}

	// get binding from broker
	req := &osb.GetBindingRequest{
		InstanceID: bindingData.InstanceId,
		BindingID:  cr.ObjectMeta.Annotations[bindingIdAnnotation],
	}

	resp, err := c.client.GetBinding(req)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetBindingRequestFailed)
	}

	paramsJson, err := json.Marshal(resp.Parameters)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "Error marshalling binding parameters")
	}

	cr.Spec.ForProvider.Parameters = &apiextensions.JSON{Raw: paramsJson}
	cr.Spec.ForProvider.Route = *resp.RouteServiceURL

	endpointsBytes, err := json.Marshal(resp.Endpoints)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "Error marshalling binding endpoints")
	}

	volumeMountsBytes, err := json.Marshal(resp.VolumeMounts)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	meta.AddAnnotations(cr, map[string]string{
		endpointsAnnotation:      string(endpointsBytes),
		volumeMountsAnnotation:   string(volumeMountsBytes),
		syslogDrainURLAnnotation: *resp.SyslogDrainURL,
	})

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
		InstanceID:          bindingData.InstanceData.InstanceId,
		BindingID:           cr.ObjectMeta.Annotations[bindingIdAnnotation],
		AcceptsIncomplete:   false, // TODO async is not supported yet TODO based on async annotation
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
		// TODO don't do anything for async, as the finalizer should be removed in the observe
		// function, but for synchronous responses
		// TODO async is not supported yet
		return managed.ExternalDelete{}, errors.New("Async requests are not supported yet.")
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
