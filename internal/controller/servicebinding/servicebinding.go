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
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane/provider-osbprovider/apis/kaastool/v1alpha1"
	apisv1alpha1 "github.com/crossplane/provider-osbprovider/apis/v1alpha1"
	"github.com/crossplane/provider-osbprovider/internal/features"

	osb "sigs.k8s.io/go-open-service-broker-client/v2"
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

	objFinalizerName         = "service-binding"
	refFinalizerNamePrefix   = "osb.provider.crossplane.io"
	errAddFinalizer          = "cannot add finalizer to Service Binding"
	errAddReferenceFinalizer = "cannot add finalizer to referenced resource"
)

// A NoOpService does nothing.
type NoOpService struct{}

var (
	newNoOpService = func(_ []byte) (interface{}, error) { return &NoOpService{}, nil }
)

// Setup adds a controller that reconciles ServiceBinding managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceBindingGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.ServiceBindingGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newServiceFn: newNoOpService}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...))

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
	newServiceFn func(creds []byte) (interface{}, error)
}

func decodeB64StringRespondBasicAuthConfig(s string) (osb.BasicAuthConfig, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return osb.BasicAuthConfig{}, err
	}
	jsonDecode := struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}{}
	err = json.Unmarshal(data, &jsonDecode)
	if err != nil {
		return osb.BasicAuthConfig{}, err
	}
	return osb.BasicAuthConfig{
		Username: jsonDecode.User,
		Password: jsonDecode.Password,
	}, err
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
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	svc, err := c.newServiceFn(data)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	config := osb.DefaultClientConfiguration()
	config.URL = ""
	basicAuth, err := decodeB64StringRespondBasicAuthConfig()
	if err != nil {
		return nil, errors.Wrap(err, "error : can't decode string into basic auth struct")
	}
	authConfig := osb.AuthConfig{
		BasicAuthConfig: &basicAuth,
	}
	config.AuthConfig = &authConfig

	client, err := osb.NewClient(config)
	if err != nil {
		return nil, err
	}

	return &external{client: client, kube: c.kube}, nil
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
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotServiceBinding)
	}

	// These fmt statements should be removed in the real implementation.
	fmt.Printf("Observing: %+v", cr)

	return managed.ExternalObservation{
		// Return false when the external resource does not exist. This lets
		// the managed resource reconciler know that it needs to call Create to
		// (re)create the resource, or that it has successfully been deleted.
		ResourceExists: true,

		// Return false when the external resource exists, but it not up to date
		// with the desired managed resource state. This lets the managed
		// resource reconciler know that it needs to call Update.
		ResourceUpToDate: true,

		// Return any details that may be required to connect to the external
		// resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotServiceBinding)
	}

	fmt.Printf("Creating: %+v", cr)

	spec := cr.Spec.ForProvider.Spec

	instanceId, appGuid, identity, err := c.getDataFromServiceBinding(spec, ctx)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	bindRequest := osb.BindRequest{
		BindingID:  cr.Spec.ForProvider.Metadata.Name,
		InstanceID: instanceId,
		BindResource: &osb.BindResource{
			AppGUID: &appGuid,
			Route:   cr.Spec.ForProvider.Spec.Route,
		},
		AcceptsIncomplete:   true,
		OriginatingIdentity: &identity,
		PlanID:              *spec.InstanceData.PlanId,
		Context:             spec.Context,
		Parameters:          spec.Parameters,
		ServiceID:           *spec.InstanceData.ServiceId,
		AppGUID:             &appGuid,
	}

	resp, err := c.client.Bind(&bindRequest)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	if resp != nil && resp.Async {
		// Get the operation and manage the creation of the secret
		go c.getLastOperation(0, 0, instanceId, cr.Spec.ForProvider.Metadata.Name, *spec.InstanceData.ServiceId, *spec.InstanceData.PlanId, resp.OperationKey, cr, c.ManageFollowingActions)
	} else if resp != nil {
		c.ManageFollowingActions(cr)
	} else {
		return managed.ExternalCreation{}, fmt.Errorf(errNoResponse, bindRequest)
	}

	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, errors.Errorf("update isn't supported for service binding type")
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotServiceBinding)
	}

	fmt.Printf("Deleting: %+v", cr)

	spec := cr.Spec.ForProvider.Spec

	instanceId, _, identity, err := c.getDataFromServiceBinding(spec, ctx)
	if err != nil {
		return managed.ExternalDelete{}, err
	}

	reqUnbind := osb.UnbindRequest{
		InstanceID:          instanceId,
		BindingID:           cr.Spec.ForProvider.Metadata.Name,
		AcceptsIncomplete:   true,
		ServiceID:           *spec.InstanceData.ServiceId,
		PlanID:              *spec.InstanceData.PlanId,
		OriginatingIdentity: &identity,
	}
	resp, err := c.client.Unbind(&reqUnbind)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	if resp != nil && resp.Async {
		go c.getLastOperation(0, 0, instanceId, cr.Spec.ForProvider.Metadata.Name, *spec.InstanceData.ServiceId, *spec.InstanceData.PlanId, resp.OperationKey, cr, c.ManageFollowingActions)
	} else if resp != nil {
		c.ManageFollowingActions(cr)
	} else {
		return managed.ExternalDelete{}, fmt.Errorf(errNoResponse, reqUnbind)
	}

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

type refFinalizerFn func(context.Context, *unstructured.Unstructured, string) error

func (c *external) handleRefFinalizer(ctx context.Context, mg *v1alpha1.ServiceBinding, finalizerFn refFinalizerFn, ignoreNotFound bool) error {
	// Loop through references to resolve each referenced resource
	for _, ref := range mg.Spec.References {
		if ref.DependsOn == nil && ref.PatchesFrom == nil {
			continue
		}

		refAPIVersion, refKind, refNamespace, refName := getReferenceInfo(ref)
		res := &unstructured.Unstructured{}
		res.SetAPIVersion(refAPIVersion)
		res.SetKind(refKind)
		// Try to get referenced resource
		err := c.kube.Get(ctx, client.ObjectKey{
			Namespace: refNamespace,
			Name:      refName,
		}, res)
		if err != nil {
			if ignoreNotFound && kerrors.IsNotFound(err) {
				continue
			}

			return errors.Wrap(err, errGetReferencedResource)
		}

		finalizerName := refFinalizerNamePrefix + string(mg.UID)
		if err = finalizerFn(ctx, res, finalizerName); err != nil {
			return err
		}
	}

	return nil
}

func (c *external) AddFinalizer(ctx context.Context, mg resource.Managed) error {
	obj, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return errors.New(errNotKubernetesServiceBinding)
	}

	if meta.FinalizerExists(obj, objFinalizerName) {
		return nil
	}
	meta.AddFinalizer(obj, objFinalizerName)

	err := c.kube.Update(ctx, obj)
	if err != nil {
		return errors.Wrap(err, errAddFinalizer)
	}

	// Add finalizer to referenced resources if not exists
	err = c.handleRefFinalizer(ctx, obj, func(
		ctx context.Context, res *unstructured.Unstructured, finalizer string,
	) error {
		if !meta.FinalizerExists(res, finalizer) {
			meta.AddFinalizer(res, finalizer)
			if err := c.kube.Update(ctx, res); err != nil {
				return errors.Wrap(err, errAddReferenceFinalizer)
			}
		}
		return nil
	}, false)
	return errors.Wrap(err, errAddFinalizer)
}

func (c *external) getDataFromServiceBinding(spec v1alpha1.BindingSpec, ctx context.Context) (string, string, osb.OriginatingIdentity, error) {
	var instanceId string
	var appGuid string
	if spec.Application != nil {
		appGuid = *spec.Application
	} else if spec.ApplicationData != nil {
		appGuid = spec.ApplicationData.Name
	}
	if spec.Instance != nil {
		instanceId = *spec.Instance
	} else if spec.InstanceData.InstanceId != nil {
		instanceId = *spec.InstanceData.InstanceId
	}
	// Checking if we miss instance data, triggering an issue for binding usage.
	if instanceId == "" {
		return "", "", osb.OriginatingIdentity{}, errors.New("error: missing either instance data, binding handling impossible.")
	}
	// Getting app data from instance.
	if appGuid == "" {
		instance := v1alpha1.ServiceInstance{}
		err := c.kube.Get(ctx, client.ObjectKey{Name: instanceId}, &instance)
		if err != nil {
			return "", "", osb.OriginatingIdentity{}, fmt.Errorf("error: instance couldn't be found, binding handling impossible. Values : %s", err.Error())
		}
		specInstance := instance.Spec.ForProvider.Spec
		if specInstance.Application != "" {
			appGuid = specInstance.Application
		} else if specInstance.ApplicationData.Name != "" {
			appGuid = specInstance.ApplicationData.Name
		} else {
			return "", "", osb.OriginatingIdentity{}, errors.New("error: missing either application data in fetched instance, binding handling impossible.")
		}
	}

	identity := osb.OriginatingIdentity{ //TODO: Config parameter
		Platform: "",
		Value:    "",
	}

	return instanceId, appGuid, identity, nil
}

func (c *external) getLastOperation(
	numberOfRepetition, delay int, instanceId, bindingId, serviceId, planId string,
	operationId *osb.OperationKey, cr *v1alpha1.ServiceBinding, callbakcFunc func(*v1alpha1.ServiceBinding, *osb.GetBindingResponse),
) (*osb.GetBindingResponse, error) {
	for i := 0; i < numberOfRepetition; i++ {
		req := osb.BindingLastOperationRequest{
			InstanceID:   instanceId,
			BindingID:    bindingId,
			ServiceID:    &serviceId,
			PlanID:       &planId,
			OperationKey: operationId,
		}
		resp, err := c.client.PollBindingLastOperation(&req)
		if err != nil {
			return nil, fmt.Errorf(errTechnical, err.Error())
		}
		switch resp.State {
		case osb.StateInProgress:
			time.Sleep(time.Duration(delay))
		case osb.StateFailed:
			return nil, fmt.Errorf("error: binding operation failed for binding id : %s, instance id : %s, after %d repetition. Ffailure error : %s", bindingId, instanceId, i, resp.Description)
		case osb.StateSucceeded:
			req := osb.GetBindingRequest{
				InstanceID: instanceId,
				BindingID:  bindingId,
			}
			resp, err := c.client.GetBinding(&req)
			if err != nil {

			}
			callbakcFunc(cr, resp)
			return resp, err
		}
	}
	return nil, errors.Errorf("error: max calls reached for last operation, please try again later")
}

func (c *external) ManageFollowingActions(bind *v1alpha1.ServiceBinding, bindingFromOsb *osb.GetBindingResponse) {
	if bind != nil {

	}
}
