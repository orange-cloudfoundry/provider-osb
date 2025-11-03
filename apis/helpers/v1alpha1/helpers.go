package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bindingv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
)

var (
	errInstanceNotFound    = errors.New("referenced service instance not found")
	errInstanceFetchFailed = errors.New("failed to retrieve referenced service instance")
	errMissingInstanceData = errors.New("missing instance data: no reference or inlined data provided")
	errInstanceRefEmpty    = errors.New("instance ref is empty")
	errServiceBindingEmpty = errors.New("service binding is empty")
)

// hasActiveBindingsForInstance checks if any of the given ServiceBindings
// reference the provided ServiceInstance and are not marked for deletion.
func hasActiveBindingsForInstance(instance *instancev1alpha1.ServiceInstance, bindings []bindingv1alpha1.ServiceBinding) bool {
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
	if spec.InstanceRef == nil {
		return instancev1alpha1.ServiceInstance{}, errInstanceRefEmpty
	}

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

	if binding == nil {
		return bindingv1alpha1.BindingData{}, errServiceBindingEmpty
	}

	spec := binding.Spec.ForProvider

	instanceData, err := resolveInstanceData(ctx, kube, binding, spec)
	if err != nil {
		return bindingv1alpha1.BindingData{}, err
	}

	return bindingv1alpha1.BindingData{
		InstanceData: *instanceData,
	}, nil
}

// resolveInstanceData handles fetching instance data either from reference or inlined.
func resolveInstanceData(
	ctx context.Context,
	kube client.Client,
	binding *bindingv1alpha1.ServiceBinding,
	spec bindingv1alpha1.ServiceBindingParameters,
) (*common.InstanceData, error) {
	if spec.HasInstanceRef() {
		instance, err := ResolveServiceInstance(ctx, kube, spec)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return nil, fmt.Errorf("%w: %s/%s", errInstanceNotFound, binding.Namespace, binding.Name)
			}
			return nil, fmt.Errorf("%w: %s", errInstanceFetchFailed, fmt.Sprint(err))
		}

		instanceData := &common.InstanceData{}
		instanceData.Set(instance.GetSpecForProvider())

		return instanceData, nil
	}

	if spec.HasInstanceData() {
		return spec.InstanceData, nil
	}

	return nil, errMissingInstanceData
}
