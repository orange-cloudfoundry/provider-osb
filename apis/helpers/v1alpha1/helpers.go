package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	applicationv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/application/v1alpha1"
	bindingv1alpha1 "github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
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

	return bindingv1alpha1.BindingData{
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
