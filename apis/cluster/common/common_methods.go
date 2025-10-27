package common

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

// GetApplicationRef returns the reference to the associated application
// from the InstanceData specification. It may return nil if no reference
// is defined.
func (spec *InstanceData) GetApplicationRef() *NamespacedName {
	return spec.ApplicationRef
}
