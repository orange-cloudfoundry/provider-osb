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

	"github.com/crossplane/crossplane-runtime/v2/apis/common"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NamespacedName is a re-implementation from k8s.io/apimachinery/pkg/types since it does not have json tags
// And it makes `make generate` fail
type NamespacedName struct {
	Name      string `json:"name"`
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

// ApplicationData represents the schema for an Application MR
type ApplicationData struct {
	Name                    string                          `json:"name"`
	Guid                    string                          `json:"guid"`
	ProviderConfigReference *common.ProviderConfigReference `json:"providerConfigRef,omitempty"`
}

// Instance Data represents the schema for a ServiceInstance MR
type InstanceData struct {
	ApplicationRef   *NamespacedName        `json:"application,omitempty"`
	ApplicationData  *ApplicationData       `json:"applicationData,omitempty"`
	InstanceId       string                 `json:"instanceId"`
	PlanId           string                 `json:"planId"`
	ServiceId        string                 `json:"serviceId"`
	Context          KubernetesOSBContext   `json:"context,omitempty"`
	Parameters       SerializableParameters `json:"parameters,omitempty"`
	OrganizationGuid string                 `json:"organizationGuid"`
	SpaceGuid        string                 `json:"spaceGuid"`
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
	Platform             string            `json:"platform"`
	Namespace            string            `json:"namespace"`
	NamespaceAnnotations map[string]string `json:"namespaceAnnotations,omitempty"`
	InstanceAnnotations  map[string]string `json:"instanceAnnotations,omitempty"`
	ClusterId            string            `json:"clusterId"`
	InstanceName         string            `json:"instanceName"`
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
	Username string                                 `json:"username"`
	UID      string                                 `json:"uid"`
	Groups   []string                               `json:"groups"`
	Extra    *KubernetesOSBOriginatingIdentityExtra `json:"extra"`
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
