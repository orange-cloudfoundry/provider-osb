package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/application/v1alpha1"
	bindingv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	instancev1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
)

var (
	errAppDataFetchFailed                    = errors.New("failed to fetch application data from binding")
	errInstanceNotFound                      = errors.New("referenced service instance not found")
	errInstanceFetchFailed                   = errors.New("failed to retrieve referenced service instance")
	errAppDataFetchFromInstance              = errors.New("failed to fetch application data from instance")
	errMissingInstanceData                   = errors.New("missing instance data: no reference or inlined data provided")
	errMissingApplicationData                = errors.New("missing application data: no reference or inlined data provided")
	errInstanceRefEmpty                      = errors.New("instance ref is empty")
	errServiceBindingEmpty                   = errors.New("service binding is empty")
	errApplicationDataAndApplicationRefEmpty = errors.New("applciation data and application ref are empty, need one of them")
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

	appData, err := resolveApplicationData(ctx, kube, spec)
	if err != nil {
		return bindingv1alpha1.BindingData{}, err
	}

	instanceData, appData, err := resolveInstanceData(ctx, kube, binding, spec, appData)
	if err != nil {
		return bindingv1alpha1.BindingData{}, err
	}

	return bindingv1alpha1.BindingData{
		InstanceData:    *instanceData,
		ApplicationData: *appData,
	}, nil
}

// resolveApplicationData attempts to fetch application data from the binding spec.
func resolveApplicationData(
	ctx context.Context,
	kube client.Client,
	spec bindingv1alpha1.ServiceBindingParameters,
) (*common.ApplicationData, error) {
	if spec.ApplicationData == nil && spec.ApplicationRef == nil {
		return nil, errApplicationDataAndApplicationRefEmpty
	}

	appData, err := applicationv1alpha1.ResolveApplicationData(ctx, kube, &spec)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errAppDataFetchFailed, fmt.Sprint(err))
	}

	if appData == nil {
		return nil, errMissingApplicationData
	}

	return appData, nil
}

// resolveInstanceData handles fetching instance data either from reference or inlined.
func resolveInstanceData(
	ctx context.Context,
	kube client.Client,
	binding *bindingv1alpha1.ServiceBinding,
	spec bindingv1alpha1.ServiceBindingParameters,
	appData *common.ApplicationData,
) (*common.InstanceData, *common.ApplicationData, error) {
	if spec.HasInstanceRef() {
		instance, err := ResolveServiceInstance(ctx, kube, spec)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return nil, nil, fmt.Errorf("%w: %s/%s", errInstanceNotFound, binding.Namespace, binding.Name)
			}
			return nil, nil, fmt.Errorf("%w: %s", errInstanceFetchFailed, fmt.Sprint(err))
		}

		instanceData := &common.InstanceData{}
		instanceData.Set(instance.GetSpecForProvider())

		if appData == nil {
			appDataResolved, err := applicationv1alpha1.ResolveApplicationData(ctx, kube, instance.GetSpecForProviderPtr())
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %s", errAppDataFetchFromInstance, fmt.Sprint(err))
			}
			appData = appDataResolved
		}

		return instanceData, appData, nil
	}

	if spec.HasInstanceData() {
		return spec.InstanceData, appData, nil
	}

	return nil, nil, errMissingInstanceData
}
