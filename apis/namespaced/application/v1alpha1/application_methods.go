package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplicationDataProvider defines the minimal interface required
// to retrieve application data from a resource spec.
//
// Any struct implementing these methods can be used with
// NewApplicationDataFromRef.
type ApplicationDataProvider interface {
	HasApplicationRef() bool
	HasApplicationData() bool
	GetApplicationRef() *common.NamespacedName
	GetApplicationData() *common.ApplicationData
}

// NewApplicationDataFromRef returns the ApplicationData associated with a ServiceBindingParameters.
// It first checks if an ApplicationRef is set and retrieves the referenced Application from the Kubernetes API.
// If the reference does not exist, it returns an error. If no reference is set but ApplicationData exists
// directly in the binding parameters, it returns that data instead.
// Returns nil if neither a reference nor inline data is provided.
func NewApplicationDataFromRef(ctx context.Context, kube client.Client, spec ApplicationDataProvider) (*common.ApplicationData, error) {
	var appData *common.ApplicationData

	if spec.HasApplicationRef() {
		application := Application{}

		if err := kube.Get(ctx, spec.GetApplicationRef().ToObjectKey(), &application); err != nil {
			if kerrors.IsNotFound(err) {
				return nil, errors.New("binding referenced an application which does not exist")
			}
			return nil, fmt.Errorf("%s: %w", "error while retrieving referenced application", err)
		}
		appData = &application.Spec.ForProvider
	} else if spec.HasApplicationData() {
		// Fetch from within the service binding
		appData = spec.GetApplicationData()
	}
	return appData, nil
}
