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
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// String returns the general purpose string representation
func (n *NamespacedName) String() string {
	return n.Namespace + "/" + n.Name
}

func (n *NamespacedName) ToObjectKey() client.ObjectKey {
	return client.ObjectKey{
		Name:      n.Name,
		Namespace: n.Namespace,
	}
}

// ApplicationData represents the schema for an Application MR
// TODO: replace brokerUrl and credentials to a ref to ProviderConfig
// It should be only a ref and never re-specified in ApplicationData
type ApplicationData struct {
	BrokerURL string `json:"brokerURL"`
	Name      string `json:"name"`
	Guid      string `json:"guid"`
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
type SerializableParameters string

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
	NamespaceAnnotations map[string]string `json:"namespace_annotations,omitempty"`
	InstanceAnnotations  map[string]string `json:"instance_annotations,omitempty"`
	ClusterId            string            `json:"cluster_id"`
	InstanceName         string            `json:"instance_name"`
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

// ToMap converts the KubernetesOSBContext into a map[string]interface{}.
func (c *KubernetesOSBContext) ToMap() (map[string]interface{}, error) {
	// Convert struct - > json
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	// Convert json -> map[string]interface{}
	var res map[string]interface{}
	err = json.Unmarshal(b, &res)
	return res, err
}
