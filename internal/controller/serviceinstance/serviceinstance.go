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

	"errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	"github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
)

var (
	errNotServiceInstanceCR           = errors.New("managed resource is not a ServiceInstance custom resource")
	errCannotTrackProviderConfig      = errors.New("cannot track ProviderConfig usage")
	errCannotCreateNewOsbClient       = errors.New("cannot create new OSB client")
	errCannotMakeOriginatingIdentity  = errors.New("cannot make originating identity from value")
	errInstanceIDNotSet               = errors.New("InstanceId must be set in the ServiceInstance spec")
	errCannotTrackProviderConfigUsage = errors.New("cannot track ProviderConfig usage")
	errFailedToDeprovision            = errors.New("failed to deprovisionning service instance")
	errFailedToProvision              = errors.New("failed to provison service instance")
	errFailedToUpdate                 = errors.New("failed to update service instance")
	errFailedToObserveState           = errors.New("failed to observe state of service instance")
)

// Setup adds a controller that reconciles ServiceInstance managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ServiceInstanceGroupKind)
	log := o.Logger.WithValues("controller", name)

	extra := &common.KubernetesOSBOriginatingIdentityExtra{}
	if err := extra.FromMap(mgr.GetConfig().Impersonate.Extra); err != nil {
		log.Info("Failed to parse KubernetesOSBOriginatingIdentityExtra from Impersonate.Extra")
	} else {
		log.Info("Successfully parsed KubernetesOSBOriginatingIdentityExtra")
		log.Debug("Successfully parsed KubernetesOSBOriginatingIdentityExtra", "extra", extra)
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.ServiceInstanceGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			originatingIdentityValue: common.KubernetesOSBOriginatingIdentityValue{
				Username: mgr.GetConfig().Impersonate.UserName,
				UID:      mgr.GetConfig().Impersonate.UID,
				Groups:   mgr.GetConfig().Impersonate.Groups,
				Extra:    extra,
			},
			newOsbClient: util.NewOsbClient}),
		managed.WithLogger(log),
		managed.WithPollInterval(o.PollInterval),
		managed.WithManagementPolicies(),
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
	kube                     client.Client
	usage                    util.ModernTracker
	newOsbClient             func(config resource.ProviderConfig, creds []byte) (osbClient.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	obj, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return nil, fmt.Errorf("%w: expected *v1alpha1.ServiceInstance but got %T", errNotServiceInstanceCR, mg)
	}

	if err := c.usage.Track(ctx, mg.(resource.ModernManaged)); err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotTrackProviderConfigUsage, fmt.Sprint(err))
	}

	pc, pcSpec, err := util.ResolveProviderConfig(ctx, c.kube, obj)
	if err != nil {
		return nil, err
	}

	// Extract credentials from the ProviderConfig
	creds, err := resource.CommonCredentialExtractor(ctx, pcSpec.Credentials.Source, c.kube, pcSpec.Credentials.CommonCredentialSelectors)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotTrackProviderConfig, fmt.Sprint(err))
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

	// Return an external client with the OSB client, Kubernetes client, and originating identity.
	return &external{
		osb:                 osbClient,
		kube:                c.kube,
		originatingIdentity: *oid,
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
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalObservation{}, fmt.Errorf("%w: expected *v1alpha1.ServiceInstance but got %T", errNotServiceInstanceCR, mg)
	}

	if instance.IsInstanceIDEmpty() {
		return managed.ExternalObservation{}, errInstanceIDNotSet
	}

	action, osbInstance, err := instance.ObserveState(ctx, c.kube, c.osb, c.originatingIdentity)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w, %s", errFailedToObserveState, fmt.Sprint(err))
	}

	switch action {
	case common.NeedToCreate:
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil

	case common.NothingToDo:
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil

	case common.NeedToUpdate:
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
			ConnectionDetails: managed.ConnectionDetails{
				"dashboardURL": []byte(osbInstance.DashboardURL),
			},
		}, nil

	default:
		return managed.ExternalObservation{}, nil
	}
}

// Create provisions a new ServiceInstance through the OSB client
// and updates its status accordingly.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalCreation{}, fmt.Errorf("%w: expected *v1alpha1.ServiceInstance but got %T", errNotServiceInstanceCR, mg)
	}

	if err := instance.Provision(ctx, c.kube, c.osb); err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errFailedToProvision, fmt.Sprint(err))
	}

	return managed.ExternalCreation{}, nil
}

// Update sends an update request to the OSB broker for the given ServiceInstance
// and updates its status in Kubernetes accordingly.
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalUpdate{}, fmt.Errorf("%w: expected *v1alpha1.ServiceInstance but got %T", errNotServiceInstanceCR, mg)
	}

	err := instance.Update(ctx, c.kube, c.osb, c.originatingIdentity)
	if err != nil {
		return managed.ExternalUpdate{}, fmt.Errorf("%w, %s", errFailedToUpdate, fmt.Sprint(err))
	}

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	instance, ok := mg.(*v1alpha1.ServiceInstance)
	if !ok {
		return managed.ExternalDelete{}, fmt.Errorf("%w: expected *v1alpha1.ServiceInstance but got %T", errNotServiceInstanceCR, mg)
	}

	err := instance.Deprovision(ctx, c.kube, c.osb, c.originatingIdentity)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("%w, %s", errFailedToDeprovision, fmt.Sprint(err))
	}

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}
