package interfaces

import (
	commonCluster "github.com/orange-cloudfoundry/provider-osb/apis/cluster/common"
	commonNamespaced "github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
)

// ApplicationDataProvider defines the minimal interface required
// to retrieve application data from a resource spec.
//
// Any struct implementing these methods can be used with
// ResolveApplicationData.
type ApplicationDataProviderCluster interface {
	HasApplicationRef() bool
	HasApplicationData() bool
	GetApplicationRef() *commonCluster.NamespacedName
	GetApplicationData() *commonCluster.ApplicationData
}

// ApplicationDataProvider defines the minimal interface required
// to retrieve application data from a resource spec.
//
// Any struct implementing these methods can be used with
// ResolveApplicationData.
type ApplicationDataProviderNamespaced interface {
	HasApplicationRef() bool
	HasApplicationData() bool
	GetApplicationRef() *commonNamespaced.NamespacedName
	GetApplicationData() *commonNamespaced.ApplicationData
}
