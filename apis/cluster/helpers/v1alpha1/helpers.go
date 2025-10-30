package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/cluster/application/v1alpha1"
	bindingv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/cluster/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/cluster/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/cluster/instance/v1alpha1"
)

var (
	errAppDataFetchFailed       = errors.New("failed to fetch application data from binding")
	errInstanceNotFound         = errors.New("referenced service instance not found")
	errInstanceFetchFailed      = errors.New("failed to retrieve referenced service instance")
	errAppDataFetchFromInstance = errors.New("failed to fetch application data from instance")
	errMissingInstanceData      = errors.New("missing instance data: no reference or inlined data provided")
	errMissingApplicationData   = errors.New("missing application data: no reference or inlined data provided")
)

// hasActiveBindingsForInstance checks if any of the given ServiceBindings
// reference the provided ServiceInstance and are not marked for deletion.
func hasActiveBindingsForInstance(instance *instancev1alpha1.ServiceInstance, bindings []bindingv1alpha1.ServiceBinding) bool {
	if instance == nil {
		return false
	}
	for _, b := range bindings {
		if b.HasNoInstanceRef() {
			continue
		}
		if b.IsBoundToInstance(instance.GetName()) && b.IsNotBeingDeleted() {
			return true
		}
	}
	return false
}

// SetActiveBindingsForInstance updates the HasActiveBindings status field
// of the given ServiceInstance based on the provided list of ServiceBindings.
// It determines whether any of the bindings reference the instance and are not
// marked for deletion.
func SetActiveBindingsForInstance(
	instance *instancev1alpha1.ServiceInstance,
	bindings []bindingv1alpha1.ServiceBinding,
) {
	instance.Status.AtProvider.HasActiveBindings = hasActiveBindingsForInstance(instance, bindings)
}

// ResolveServiceInstance retrieves a ServiceInstance object from Kubernetes
// based on the InstanceRef provided in the ServiceBindingParameters spec.
// It returns an error if the referenced ServiceInstance does not exist
// or if any other Kubernetes client error occurs.
func ResolveServiceInstance(ctx context.Context, kube client.Client, spec bindingv1alpha1.ServiceBindingParameters) (instancev1alpha1.ServiceInstance, error) {
	instance := instancev1alpha1.ServiceInstance{}
	if err := kube.Get(ctx, spec.InstanceRef.ToObjectKey(), &instance); err != nil {
		if kerrors.IsNotFound(err) {
			return instancev1alpha1.ServiceInstance{}, err
		}
		return instancev1alpha1.ServiceInstance{}, err
	}
	return instance, nil
}

// GetDataFromServiceBinding retrieves both instance and application data required
// for handling a ServiceBinding. It supports fetching from either a direct reference
// (InstanceRef) or inlined InstanceData in the spec, and similarly for application data.
// Returns a BindingData struct containing both instance and application data,
// or an error if any required data is missing or cannot be retrieved.
func GetDataFromServiceBinding(
	ctx context.Context,
	kube client.Client,
	binding *bindingv1alpha1.ServiceBinding,
) (bindingv1alpha1.BindingData, error) {

	spec := binding.Spec.ForProvider

	// Try to fetch application data directly from the binding spec.
	appData, err := applicationv1alpha1.ResolveApplicationData(ctx, kube, &spec)
	if err != nil {
		return bindingv1alpha1.BindingData{}, fmt.Errorf("%w: %s", errAppDataFetchFailed, fmt.Sprint(err))
	}

	// Initialize instance data pointer.
	var instanceData *common.InstanceData

	if spec.HasInstanceRef() {

		// Fetch instance data from the referenced ServiceInstance resource.
		instance, err := ResolveServiceInstance(ctx, kube, spec)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return bindingv1alpha1.BindingData{}, fmt.Errorf("%w: %s/%s", errInstanceNotFound, binding.Namespace, binding.Name)
			}
			return bindingv1alpha1.BindingData{}, fmt.Errorf("%w: %s", errInstanceFetchFailed, fmt.Sprint(err))
		}

		instanceData = &common.InstanceData{}
		instanceData.Set(instance.GetSpecForProvider())

		// If no application data was found on the binding, fetch it from the instance.
		if appData == nil {
			appData, err = applicationv1alpha1.ResolveApplicationData(ctx, kube, instance.GetSpecForProviderPtr())
			if err != nil {
				return bindingv1alpha1.BindingData{}, fmt.Errorf("%w: %s", errAppDataFetchFromInstance, fmt.Sprint(err))
			}
		}

	} else if spec.HasInstanceData() {
		// Use inlined instance data if available.
		instanceData = spec.InstanceData
	}

	// Validate presence of both instance and application data.
	if instanceData == nil {
		return bindingv1alpha1.BindingData{}, errMissingInstanceData
	}
	if appData == nil {
		return bindingv1alpha1.BindingData{}, errMissingApplicationData
	}

	return bindingv1alpha1.BindingData{
		InstanceData:    *instanceData,
		ApplicationData: *appData,
	}, nil
}
