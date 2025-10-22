package common

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HasApplicationRef returns true if the InstanceData has an ApplicationRef set.
// This indicates that the instance is linked to a separate Application resource.
func (spec *InstanceData) HasApplicationRef() bool {
	return spec.ApplicationRef != nil
}

// HasApplicationData returns true if the InstanceData contains inline ApplicationData.
// This indicates that the instance stores application information directly.
func (spec *InstanceData) HasApplicationData() bool {
	return spec.ApplicationData != nil
}

// GetObjectKeyFromApplicationRef returns the Kubernetes ObjectKey corresponding
// to the ApplicationRef in the InstanceData. This can be used to fetch the referenced
// Application from the cluster.
func (spec *InstanceData) GetObjectKeyFromApplicationRef() client.ObjectKey {
	return spec.ApplicationRef.ToObjectKey()
}

// GetApplicationData returns the inline ApplicationData stored in the InstanceData.
// Returns nil if no inline data exists.
func (spec *InstanceData) GetApplicationData() *ApplicationData {
	return spec.ApplicationData
}

// Set copies the contents of the given InstanceData into the receiver.
// All fields of the receiver will be overwritten with the values from instanceData.
func (spec *InstanceData) Set(instanceData InstanceData) {
	*spec = instanceData
}

// IsNil returns true if the Instance data pointer is nil.
// This can be used to check whether any application data has been set.
func (spec *InstanceData) IsNil() bool {
	return spec == nil
}

// IsNil returns true if the ApplicationData pointer is nil.
// This can be used to check whether any application data has been set.
func (spec *ApplicationData) IsNil() bool {
	return spec == nil
}

func (spec *InstanceData) GetApplicationRef() *NamespacedName {
	return spec.ApplicationRef
}
