package interfaces

import (
	common "github.com/orange-cloudfoundry/provider-osb/apis/common"
)

// ApplicationDataProvider defines the minimal interface required
// to retrieve application data from a resource spec.
//
// Any struct implementing these methods can be used with
// ResolveApplicationData.
type ApplicationDataProviderNamespaced interface {
	HasApplicationRef() bool
	HasApplicationData() bool
	GetApplicationRef() *common.NamespacedName
	GetApplicationData() *common.ApplicationData
}
