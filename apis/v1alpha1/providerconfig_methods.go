package v1alpha1

func (pc *ProviderConfig) GetBrokerURL() string {
	return pc.Spec.BrokerURL
}

func (pc *ProviderConfig) GetOSBVersion() string {
	return pc.Spec.OSBVersion
}

func (pc *ProviderConfig) GetTimeout() int {
	return pc.Spec.Timeout
}

func (cpc *ClusterProviderConfig) GetBrokerURL() string {
	return cpc.Spec.BrokerURL
}

func (cpc *ClusterProviderConfig) GetOSBVersion() string {
	return cpc.Spec.OSBVersion
}

func (cpc *ClusterProviderConfig) GetTimeout() int {
	return cpc.Spec.Timeout
}
