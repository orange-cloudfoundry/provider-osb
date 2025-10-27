/*
Copyright 2025 The Crossplane Authors.

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

package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/application/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
	apisv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/namespaced/util"
)

var (
	errNotApplicationCR               = errors.New("managed resource is not a Application custom resource")
	errCannotRegisterMRStateRecorder  = errors.New("cannot register MR state metrics recorder for kind")
	errCannotTrackProviderConfigUsage = errors.New("cannot track ProviderConfig usage")
	errCannotGetCredentials           = errors.New("cannot get credentials")
	errCannotCreateNewOsbClient       = errors.New("cannot create new osb client")
	errCannotMakeOriginatingIdentity  = errors.New("cannot make originating identity from value")
)

// Setup adds a controller that reconciles Application managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.ApplicationGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{
			kube:         mgr.GetClient(),
			usage:        resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			newOsbClient: util.NewOsbClient,
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithManagementPolicies(),
		managed.WithMetricRecorder(o.MetricOptions.MRMetrics),
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.ApplicationList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return fmt.Errorf("%w: %s: %v", errCannotRegisterMRStateRecorder, v1alpha1.ApplicationGroupVersionKind.Kind, err)
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.ApplicationGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.Application{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube                     client.Client
	usage                    util.ModernTracker
	newOsbClient             func(config resource.ProviderConfig, creds []byte) (osb.Client, error)
	originatingIdentityValue common.KubernetesOSBOriginatingIdentityValue
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	obj, ok := mg.(*v1alpha1.Application)
	if !ok {
		return nil, fmt.Errorf("%w: expected *v1alpha1.Application but got %T", errNotApplicationCR, mg)
	}

	if err := c.usage.Track(ctx, mg.(resource.ModernManaged)); err != nil {
		return nil, errCannotTrackProviderConfigUsage
	}

	pc, pcSpec, err := util.ResolveProviderConfig(ctx, c.kube, obj)
	if err != nil {
		return nil, err
	}

	// Extract credentials from the ProviderConfig
	creds, err := resource.CommonCredentialExtractor(ctx, pcSpec.Credentials.Source, c.kube, pcSpec.Credentials.CommonCredentialSelectors)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCannotGetCredentials, err)
	}

	// Create a new OSB client using the resolved ProviderConfig and extracted credentials
	osbClient, err := util.NewOsbClient(pc, creds)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCannotCreateNewOsbClient, err)
	}

	// Add extra data to the originating identity from the ProviderConfig
	c.originatingIdentityValue.Extra = &pcSpec.OriginatingIdentityExtraData

	// Create the originating identity object
	oid, err := util.MakeOriginatingIdentityFromValue(c.originatingIdentityValue)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCannotMakeOriginatingIdentity, err)
	}

	// Return an external client with the OSB client, Kubernetes client, and originating identity.
	return &external{
		kube:                c.kube,
		osbClient:           osbClient,
		originatingIdentity: *oid,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	osbClient           osb.Client
	kube                client.Client
	originatingIdentity osb.OriginatingIdentity
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Application)
	if !ok {
		return managed.ExternalObservation{}, fmt.Errorf("%w: expected *v1alpha1.Application but got %T", errNotApplicationCR, mg)
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
	cr, ok := mg.(*v1alpha1.Application)
	if !ok {
		return managed.ExternalCreation{}, fmt.Errorf("%w: expected *v1alpha1.Application but got %T", errNotApplicationCR, mg)
	}

	fmt.Printf("Creating: %+v", cr)

	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Application)
	if !ok {
		return managed.ExternalUpdate{}, fmt.Errorf("%w: expected *v1alpha1.Application but got %T", errNotApplicationCR, mg)
	}

	fmt.Printf("Updating: %+v", cr)

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.Application)
	if !ok {
		return managed.ExternalDelete{}, fmt.Errorf("%w: expected *v1alpha1.Application but got %T", errNotApplicationCR, mg)
	}

	fmt.Printf("Deleting: %+v", cr)

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}
