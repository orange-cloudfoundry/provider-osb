/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

// ApplicationData represents the schema for an Application MR
type ApplicationData struct {
	BrokerURL   *string      `json:"brokerURL"`
	Credentials *Credentials `json:"credentials,omitempty"`
	Name        *string      `json:"Name"`
}

// Instance Data represents the schema for a ServiceInstance MR
type InstanceData struct {
	InstanceId *string `json:"instanceId"`
	PlanId     *string `json:"planId"`
	ServiceId  *string `json:"serviceId"`
}

// Credentials is a struct to hold credentials used to contact the service brokers
type Credentials struct {
	SecretName     *string         `json:"secretName,omitempty"`
	HardcodedCreds *HardcodedCreds `json:"hardcodedCreds,omitempty"`
}

// HardcodedCreds represents the info needed for credentials that are hardcoded
type HardcodedCreds struct {
	User     *string `json:"user"`
	Password *string `json:"password"`
}

// KubernetesContextObject represents the context object for kubernetes in the OSB spec
// cf https://github.com/cloudfoundry/servicebroker/blob/master/profile.md#kubernetes-context-object
type KubernetesContextObject struct {
	Platform             *string           `json:"platform"`
	Namespace            *string           `json:"namespace"`
	NamespaceAnnotations map[string]string `json:"namespace_annotations,omitempty"`
	InstanceAnnotations  map[string]string `json:"instance_annotations,omitempty"`
	ClusterId            *string           `json:"cluster_id"`
	InstanceName         *string           `json:"instance_name"`
}
