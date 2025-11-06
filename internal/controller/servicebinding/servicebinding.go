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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
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

var (
	errNotServiceBindingCR           = errors.New("managed resource is not a ServiceBinding custom resource")
	errCannotRegisterMRStateRecorder = errors.New("cannot register MR state metrics recorder for kind")
	errCannotTrackProviderConfig     = errors.New("cannot track ProviderConfig usage")
	errCannotGetCredentials          = errors.New("cannot get credentials")
	errCannotCreateNewOsbClient      = errors.New("cannot create new OSB client")
	errCannotMakeOriginatingIdentity = errors.New("cannot make originating identity from value")
	errOSBRotatingRequestFailed      = errors.New("OSB RotateBinding request failed")
	errExtractCredsFailed            = errors.New("failed to extract credentials from OSB response")
	errCreateRespDataFailed          = errors.New("failed to create response data with binding parameters")
	errUpdateStatusFailed            = errors.New("failed to update binding status")
	errFailedToBindServiceBinding    = errors.New("failed to bind service binding")
	errFailedToUnBindServiceBinding  = errors.New("failed to unbind service binding")
	errFailedToObserveState          = errors.New("failed to observe state of service binding")
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

	action, credentials, err := binding.ObserveState(ctx, c.kube, c.osb, c.rotateBinding, c.originatingIdentity)
	if err != nil {
		return managed.ExternalObservation{}, fmt.Errorf("%w: %s", errFailedToObserveState, fmt.Sprint(err))
	}

	switch action {
	case common.NeedToCreate:
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	case common.NeedToUpdate:
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	case common.NothingToDo:
		return managed.ExternalObservation{
			ResourceExists:    true,
			ResourceUpToDate:  true,
			ConnectionDetails: credentials,
		}, nil
	default:
		return managed.ExternalObservation{}, nil
	}
}

// Create provisions a new ServiceBinding through the OSB client
// and updates its status and connection details accordingly.
func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	binding, ok := mg.(*v1alpha1.ServiceBinding)
	if !ok {
		return managed.ExternalCreation{}, fmt.Errorf("%w: expected *v1alpha1.ServiceBinding but got %T", errNotServiceBindingCR, mg)

	}

	bindResponse, async, err := binding.Bind(ctx, c.kube, c.osb, c.originatingIdentity)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w, %s", errFailedToBindServiceBinding, fmt.Sprint(err))
	}

	if async {
		return managed.ExternalCreation{
			AdditionalDetails: managed.AdditionalDetails{
				"async": "true",
			},
		}, nil
	}

	// Extract connection credentials from the OSB response.
	creds, err := util.GetCredsFromResponse(bindResponse)
	if err != nil {
		return managed.ExternalCreation{}, fmt.Errorf("%w: %s", errExtractCredsFailed, fmt.Sprint(err))
	}

	data, err := binding.CreateResponseDataWithBindingParameters(*bindResponse)
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
	creds, err := binding.TriggerRotation(ctx, c.kube, c.osb, c.originatingIdentity)
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

	isAsync, err := binding.UnBind(ctx, c.kube, c.osb, c.originatingIdentity)
	if err != nil {
		return managed.ExternalDelete{}, fmt.Errorf("%w: %s", errFailedToUnBindServiceBinding, fmt.Sprint(err))
	}

	if isAsync {
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
