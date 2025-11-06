package common

// HasApplicationRef returns true if the InstanceData has an ApplicationRef set.
// This indicates that the instance is linked to a separate Application resource.
func (instanceData *InstanceData) HasApplicationRef() bool {
	return instanceData.ApplicationRef != nil
}

// HasApplicationData returns true if the InstanceData contains inline ApplicationData.
// This indicates that the instance stores application information directly.
func (instanceData *InstanceData) HasApplicationData() bool {
	return instanceData.ApplicationData != nil
}

// GetApplicationData returns the inline ApplicationData stored in the InstanceData.
// Returns nil if no inline data exists.
func (instanceData *InstanceData) GetApplicationData() *ApplicationData {
	return instanceData.ApplicationData
}

// Set copies the contents of the given InstanceData into the receiver.
// All fields of the receiver will be overwritten with the values from instanceData.
func (instanceData *InstanceData) Set(newInstanceData InstanceData) {
	*instanceData = newInstanceData
}

// GetApplicationRef returns the reference to the associated application
// from the InstanceData specification. It may return nil if no reference
// is defined.
func (instanceData *InstanceData) GetApplicationRef() *NamespacedName {
	return instanceData.ApplicationRef
}

func (a Action) String() string {
	switch a {
	case NothingToDo:
		return "NothingToDo"
	case NeedToCreate:
		return "NeedToCreate"
	case NeedToUpdate:
		return "NeedToUpdate"
	default:
		return "Unknown"
	}
}
