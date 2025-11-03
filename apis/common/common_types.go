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

import (
	"encoding/json"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NamespacedName is a re-implementation from k8s.io/apimachinery/pkg/types since it does not have json tags
// And it makes `make generate` fail
type NamespacedName struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
}

// String returns the general purpose string representation
func (n *NamespacedName) String() string {
	return n.Namespace + "/" + n.Name
}

// ToObjectKey converts a NamespacedName into a Kubernetes client.ObjectKey,
// which can be used to retrieve or manipulate Kubernetes objects.
func (n *NamespacedName) ToObjectKey() client.ObjectKey {
	return client.ObjectKey{
		Name:      n.Name,
		Namespace: n.Namespace,
	}
}

// Instance Data represents the schema for a ServiceInstance MR
type InstanceData struct {
	// +kubebuilder:validation:Optional
	AppGuid string `json:"appGuid,omitempty"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	InstanceId string `json:"instanceId"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	PlanId string `json:"planId"`
	// +kubebuilder:validation:Required
	ServiceId string `json:"serviceId"`
	// +kubebuilder:validation:Optional
	Context KubernetesOSBContext `json:"context,omitempty"`
	// +kubebuilder:validation:Optional
	Parameters SerializableParameters `json:"parameters,omitempty"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	OrganizationGuid string `json:"organizationGuid"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	SpaceGuid string `json:"spaceGuid"`
}

// SerializableParameters represents a JSON-encoded map of arbitrary parameters.
// Stored as a string because slices and maps are not directly serializable in Go
// It is stored as a string but can be converted back to a Go map.
type SerializableParameters string

// ToParameters deserializes the JSON string into a map[string]any.
// Returns an empty map if the string is nil or empty.
func (v *SerializableParameters) ToParameters() (map[string]any, error) {
	if v == nil || len([]byte(*v)) == 0 {
		return map[string]any{}, nil
	}
	res := map[string]any{}
	err := json.Unmarshal([]byte(*v), &res)
	return res, err
}

// KubernetesOSBContext represents the context object for kubernetes in the OSB spec
// cf https://github.com/cloudfoundry/servicebroker/blob/master/profile.md#kubernetes-context-object
type KubernetesOSBContext struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=kubernetes
	Platform string `json:"platform"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:Optional
	NamespaceAnnotations map[string]string `json:"namespaceAnnotations,omitempty"`
	// +kubebuilder:validation:Optional
	InstanceAnnotations map[string]string `json:"instanceAnnotations,omitempty"`
	// +kubebuilder:validation:Required
	ClusterId string `json:"clusterId"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	InstanceName string `json:"instanceName"`
}

// KubernetesOSBOriginatingIdentityExtra represents the "extra" attribute in the
// Originating-Identity header structure from the OSB spec
// cf https://github.com/cloudfoundry/servicebroker/blob/master/profile.md#kubernetes-originating-identity-header
// The underlying type encoded in Json should always be map[string][]string
type KubernetesOSBOriginatingIdentityExtra apiextensions.JSON

// FromMap helps construct a KubernetesOSBOriginatingIdentityExtra object's raw bytes from a map[string][]string variable
func (e *KubernetesOSBOriginatingIdentityExtra) FromMap(m map[string][]string) error {
	raw, err := json.Marshal(m)
	if err == nil {
		e.Raw = raw
	}
	return err
}

// ToMap returns a map[string][]string by unmarshalling the KubernetesOSBOriginatingIdentityExtra object's raw bytes
func (e *KubernetesOSBOriginatingIdentityExtra) ToMap() (map[string][]string, error) {
	res := map[string][]string{}
	err := json.Unmarshal(e.Raw, &res)
	return res, err
}

// KubernetesOSBOriginatingIdentityValue represents the Originating-Identity header structure from the OSB spec
// cf https://github.com/cloudfoundry/servicebroker/blob/master/profile.md#kubernetes-originating-identity-header
type KubernetesOSBOriginatingIdentityValue struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	UID string `json:"uid"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:UniqueItems=true
	Groups []string `json:"groups"`
	// +kubebuilder:validation:Optional
	Extra *KubernetesOSBOriginatingIdentityExtra `json:"extra,omitempty"`
}

// ToMap converts the KubernetesOSBContext into a map[string]any.
func (c *KubernetesOSBContext) ToMap() (map[string]any, error) {
	// Convert struct - > json
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	// Convert json -> map[string]any
	var res map[string]any
	err = json.Unmarshal(b, &res)
	return res, err
}

type Action int

const (
	NothingToDo Action = iota
	NeedToCreate
	NeedToUpdate
)
