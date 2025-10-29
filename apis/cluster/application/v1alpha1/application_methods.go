package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"github.com/orange-cloudfoundry/provider-osb/apis/cluster/common"
	"github.com/orange-cloudfoundry/provider-osb/internal/interfaces"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errWhileRetrievingReferencedApplication = errors.New("error while retrieving referenced application")
	errBindingReferencedNotExist            = errors.New("binding referenced an application which does not exist")
)

// ResolveApplicationData returns the ApplicationData associated with a ServiceBindingParameters.
// It first checks if an ApplicationRef is set and retrieves the referenced Application from the Kubernetes API.
// If the reference does not exist, it returns an error. If no reference is set but ApplicationData exists
// directly in the binding parameters, it returns that data instead.
// Returns nil if neither a reference nor inline data is provided.
func ResolveApplicationData(ctx context.Context, kube client.Client, spec interfaces.ApplicationDataProviderCluster) (*common.ApplicationData, error) {
	var appData *common.ApplicationData

	if spec.HasApplicationRef() {
		application := Application{}

		if err := kube.Get(ctx, spec.GetApplicationRef().ToObjectKey(), &application); err != nil {
			if kerrors.IsNotFound(err) {
				return nil, errBindingReferencedNotExist
			}
			return nil, fmt.Errorf("%w: %s", errWhileRetrievingReferencedApplication, fmt.Sprint(err))
		}

		appCopy := application.Spec.ForProvider
		appData = &appCopy

	} else if spec.HasApplicationData() {
		appData = spec.GetApplicationData()
	}

	return appData, nil
}
