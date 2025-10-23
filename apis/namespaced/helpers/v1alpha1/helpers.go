package v1alpha1

import (
	"context"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/application/v1alpha1"
	bindingv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/instance/v1alpha1"
)

// hasActiveBindingsForInstance checks if any of the given ServiceBindings
// reference the provided ServiceInstance and are not marked for deletion.
func hasActiveBindingsForInstance(instance *instancev1alpha1.ServiceInstance, bindings []bindingv1alpha1.ServiceBinding) bool {
	for _, b := range bindings {
		if b.HasNoProviderInstanceRef() {
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

// NewServiceInstanceFromRef retrieves a ServiceInstance object from Kubernetes
// based on the InstanceRef provided in the ServiceBindingParameters spec.
// It returns an error if the referenced ServiceInstance does not exist
// or if any other Kubernetes client error occurs.
func NewServiceInstanceFromRef(ctx context.Context, kube client.Client, spec bindingv1alpha1.ServiceBindingParameters) (instancev1alpha1.ServiceInstance, error) {
	instance := instancev1alpha1.ServiceInstance{}
	if err := kube.Get(ctx, spec.InstanceRef.ToObjectKey(), &instance); err != nil {
		if kerrors.IsNotFound(err) {
			return instancev1alpha1.ServiceInstance{}, fmt.Errorf("binding references a service instance which does not exist")
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
	appData, err := applicationv1alpha1.NewApplicationDataFromRef(ctx, kube, &spec)
	if err != nil {
		return bindingv1alpha1.BindingData{}, fmt.Errorf("failed to fetch application data from binding: %w", err)
	}

	// Initialize instance data pointer.
	var instanceData *common.InstanceData

	if spec.HasInstanceRef() {
		// Fetch instance data from the referenced ServiceInstance resource.
		instance, err := NewServiceInstanceFromRef(ctx, kube, spec)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return bindingv1alpha1.BindingData{}, fmt.Errorf("referenced service instance not found")
			}
			return bindingv1alpha1.BindingData{}, fmt.Errorf("failed to retrieve referenced service instance: %w", err)
		}

		instanceData.Set(instance.GetSpecForProvider())

		// If no application data was found on the binding, fetch it from the instance.
		if appData.IsNil() {
			appData, err = applicationv1alpha1.NewApplicationDataFromRef(ctx, kube, instance.GetSpecForProviderPtr())
			if err != nil {
				return bindingv1alpha1.BindingData{}, fmt.Errorf("failed to fetch application data from instance: %w", err)
			}
		}

	} else if spec.HasInstanceData() {
		// Use inlined instance data if available.
		instanceData = spec.InstanceData
	}

	// Validate presence of both instance and application data.
	if instanceData.IsNil() {
		return bindingv1alpha1.BindingData{}, fmt.Errorf("missing instance data: no reference or inlined data provided")
	}
	if appData.IsNil() {
		return bindingv1alpha1.BindingData{}, fmt.Errorf("missing application data: no reference or inlined data provided")
	}

	return bindingv1alpha1.BindingData{
		InstanceData:    *instanceData,
		ApplicationData: *appData,
	}, nil
}
