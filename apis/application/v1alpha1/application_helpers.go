package v1alpha1

func (cr *Application) GetConfigChecksum() string {
	return cr.Status.AtProvider.ConfigChecksum
}

func (cr *Application) SetConfigChecksum(cs string) {
	cr.Status.AtProvider.ConfigChecksum = cs
}
