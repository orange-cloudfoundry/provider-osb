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

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/connection"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	"github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/features"
)

const (
	errNotServiceInstance = "managed resource is not a ServiceInstance custom resource"
	errTrackPCUsage       = "cannot track ProviderConfig usage"
	errGetPC              = "cannot get ProviderConfig"
	errGetCreds           = "cannot get credentials"

	errNewClient     = "cannot create new Service"
	errRequestFailed = "OSB %s request failed"
	errParseMarshall = "error while marshalling or parsing %s"
)

// Setup adds a controller that reconciles ServiceInstance managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceInstanceGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.ServiceInstanceGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newOsbClient: util.NewOsbClient}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...))

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
	kube                     client.Client
	usage                    resource.Tracker
	newOsbClient             func(conf apisv1alpha1.ProviderConfig, creds []byte) (osb.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {

	// Assert that the managed resource is of type ServiceInstance.
	cr, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return nil, errors.New(errNotServiceInstance)
	}

	// Track usage of the ProviderConfig by this managed resource.
	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// Retrieve the ProviderConfig referenced by the managed resource.
	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// Extract credentials from the ProviderConfig.
	cd := pc.Spec.Credentials
	creds, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	// Create a new OSB client using the broker URL and credentials.
	osbclient, err := c.newOsbClient(*pc, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	// Build originating identity for the OSB client.
	c.originatingIdentityValue.Extra = &pc.Spec.OriginatingIdentityExtraData
	oid, err := util.MakeOriginatingIdentityFromValue(c.originatingIdentityValue)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	// Return an external client with the OSB client, Kubernetes client, and originating identity.
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

// Observe checks the current state of the external ServiceInstance resource and determines
// whether it exists and is up to date with the desired managed resource state. It returns
// an ExternalObservation indicating the existence and up-to-date status of the resource,
// along with any connection details required to connect to the external resource.
// If the provided managed resource is not a ServiceInstance, an error is returned.
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	// Assert that the managed resource is of type ServiceInstance.
	si, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotServiceInstance)
	}

	// Ensure the InstanceId is set in the ServiceInstance spec before proceeding.
	if si.Spec.ForProvider.InstanceId == "" {
		return managed.ExternalObservation{}, errors.New("InstanceId must be set in ServiceInstance spec")
	}

	// Build the GetInstanceRequest using the InstanceId from the ServiceInstance spec.
	req := &osb.GetInstanceRequest{
		InstanceID: si.Spec.ForProvider.InstanceId,
	}

	// Call the OSB client's GetInstance method to retrieve the current state of the instance.
	instance, err := c.client.GetInstance(req)
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
		return managed.ExternalObservation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "GetInstance"))
	}

	// Compare the desired spec from the ServiceInstance with the actual instance returned from OSB.
	// This determines if the external resource is up to date with the desired state.
	upToDate, err := compareSpecWithOsb(*si, instance)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "cannot compare ServiceInstance spec with OSB instance")
	}
	// These fmt statements should be removed in the real implementation.
	return managed.ExternalObservation{
		// Return false when the external resource does not exist. This lets
		// the managed resource reconciler know that it needs to call Create to
		// (re)create the resource, or that it has successfully been deleted.
		ResourceExists: true,

		// Return false when the external resource exists, but it not up to date
		// with the desired managed resource state. This lets the managed
		// resource reconciler know that it needs to call Update.
		ResourceUpToDate: upToDate,

		// Return any details that may be required to connect to the external
		// resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{
			"dashboardURL": []byte(instance.DashboardURL),
		},
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {

	// Assert that the managed resource is of type ServiceInstance.
	si, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotServiceInstance)
	}

	// Convert parameters from the ServiceInstance spec to the format required by OSB.
	params, err := si.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters to bytes from ServiceInstance"))
	}

	// Convert context from the ServiceInstance spec to a map for OSB.
	context, err := si.Spec.ForProvider.Context.ToMap()
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "context to map from ServiceInstance"))
	}

	// Build the ProvisionRequest for the OSB client using the ServiceInstance spec.
	req := &osb.ProvisionRequest{
		InstanceID:        si.Spec.ForProvider.InstanceId,
		ServiceID:         si.Spec.ForProvider.ServiceId,
		PlanID:            si.Spec.ForProvider.PlanId,
		OrganizationGUID:  si.Spec.ForProvider.OrganizationGuid,
		SpaceGUID:         si.Spec.ForProvider.SpaceGuid,
		Parameters:        params,
		AcceptsIncomplete: true,
		Context:           context,
	}
	// Call the OSB client's ProvisionInstance method to create the external resource.
	resp, err := c.client.ProvisionInstance(req)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "ProvisionInstance"))
	}

	// Always update the context, DashboardURL in the status to reflect the spec.
	si.Status.AtProvider.Context = si.Spec.ForProvider.Context
	si.Status.AtProvider.DashboardURL = resp.DashboardURL
	// Update the ServiceInstance status based on the response from the OSB client.
	if resp.Async {
		si.Status.SetConditions(xpv1.Creating())
		si.Status.AtProvider.LastOperationState = osb.StateInProgress
		if resp.OperationKey != nil {
			si.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
	} else {
		si.Status.SetConditions(xpv1.Available())
		si.Status.AtProvider.LastOperationState = osb.StateSucceeded
	}
	// Update the status of the ServiceInstance resource in Kubernetes.
	if err := c.kube.Status().Update(ctx, si); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "cannot update ServiceInstance status")
	}
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotServiceInstance)
	}

	fmt.Printf("Updating: %+v", cr)

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	si, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotServiceInstance)
	}

	// If the InstanceId is not set, there is nothing to delete.
	// We consider the resource as already deleted.
	if si.Spec.ForProvider.InstanceId == "" {
		return managed.ExternalDelete{}, nil
	}
	// Build the DeprovisionRequest using the InstanceId from the ServiceInstance spec.
	req := &osb.DeprovisionRequest{
		InstanceID:          si.Spec.ForProvider.InstanceId,
		AcceptsIncomplete:   true,
		ServiceID:           si.Spec.ForProvider.ServiceId,
		PlanID:              si.Spec.ForProvider.PlanId,
		OriginatingIdentity: &c.originatingIdentity,
	}
	// Call the OSB client's DeprovisionInstance method to delete the external resource.
	resp, err := c.client.DeprovisionInstance(req)
	if err != nil {
		if httpErr, isHttpErr := osb.IsHTTPError(err); isHttpErr {
			if httpErr.StatusCode == http.StatusGone || httpErr.StatusCode == http.StatusNotFound {
				return managed.ExternalDelete{}, nil
			}
		}
		return managed.ExternalDelete{}, errors.Wrap(err, fmt.Sprintf(errRequestFailed, "DeprovisionInstance"))
	}
	// Update the ServiceInstance status based on the response from the OSB client.
	// If the operation is asynchronous, set the condition to Deleting and update the last operation state.
	if resp.Async {
		si.Status.AtProvider.LastOperationState = osb.StateInProgress
		if resp.OperationKey != nil {
			si.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
	}
	// This indicates that the deletion process has been initiated.
	si.Status.SetConditions(xpv1.Deleting())
	if err := c.kube.Status().Update(ctx, si); err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "cannot update ServiceInstance status")
	}
	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func compareSpecWithOsb(si v1alpha1.ServiceInstance, instance *osb.GetInstanceResponse) (bool, error) {
	if instance == nil {
		return false, nil
	}

	if si.Spec.ForProvider.PlanId != "" && si.Spec.ForProvider.PlanId != instance.PlanID {
		return false, nil
	}

	if len(si.Spec.ForProvider.Parameters) > 0 {
		// Parse parameters from the ServiceInstance spec for comparison.
		params, err := si.Spec.ForProvider.Parameters.ToParameters()
		if err != nil {
			return false, errors.Wrap(err, fmt.Sprintf(errParseMarshall, "parameters to map from ServiceInstance"))
		}

		if !reflect.DeepEqual(params, instance.Parameters) {
			return false, nil
		}
	}

	if !reflect.DeepEqual(si.Spec.ForProvider.Context, si.Status.AtProvider.Context) {
		return false, nil
	}

	return true, nil
}
