package interfaces

// ServiceBindingAccessor defines the subset of ServiceBinding methods
// needed by ServiceInstance to check active bindings.
type ServiceBindingAccessor interface {
	// HasNoProviderInstanceRef returns true if the binding has no associated ServiceInstance reference.
	HasNoProviderInstanceRef() bool

	// IsBoundToInstance returns true if the binding references the ServiceInstance with the given name.
	IsBoundToInstance(instanceName string) bool

	// IsNotBeingDeleted returns true if the binding has not been marked for deletion.
	IsNotBeingDeleted() bool
}
