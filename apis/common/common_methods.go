package common

// Set copies the contents of the given InstanceData into the receiver.
// All fields of the receiver will be overwritten with the values from instanceData.
func (instanceData *InstanceData) Set(newInstanceData InstanceData) {
	*instanceData = newInstanceData
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
