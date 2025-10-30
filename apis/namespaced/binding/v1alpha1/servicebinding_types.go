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

package v1alpha1

import (
	"encoding/json"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"

	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
)

// ServiceBindingParameters are the configurable fields of a ServiceBinding.
type ServiceBindingParameters struct {
	ApplicationRef  *common.NamespacedName        `json:"application,omitempty"`
	ApplicationData *common.ApplicationData       `json:"applicationData,omitempty"`
	InstanceRef     *common.NamespacedName        `json:"instance,omitempty"`
	InstanceData    *common.InstanceData          `json:"instanceData,omitempty"`
	Context         common.KubernetesOSBContext   `json:"context,omitempty"`
	Parameters      common.SerializableParameters `json:"parameters,omitempty"`
	Route           string                        `json:"route,omitempty"`
	// TODO manage additional attributes
}

// ServiceBindingObservation are the observable fields of a ServiceBinding.
type ServiceBindingObservation struct {
	// TODO add context and route to test if these were updated and return error if so
	Parameters               common.SerializableParameters `json:"parameters,omitempty"`
	RouteServiceURL          *string                       `json:"routeServiceUrl,omitempty"`
	Endpoints                SerializableEndpoints         `json:"endpoints,omitempty"`
	VolumeMounts             SerializableVolumeMounts      `json:"volumeMounts,omitempty"`
	SyslogDrainURL           *string                       `json:"syslogDrainUrl,omitempty"`
	Metadata                 *osbClient.BindingMetadata    `json:"metadata,omitempty"`
	LastOperationState       osbClient.LastOperationState  `json:"lastOperationState,omitempty"`
	LastOperationKey         osbClient.OperationKey        `json:"lastOperationKey,omitempty"`
	LastOperationDescription string                        `json:"lastOperationDescription,omitempty"`
	LastOperationPolledTime  string                        `json:"lastOperationPolled_time,omitempty"`
}

// SerializableVolumeMounts represents a JSON-encoded slice of osb.VolumeMount.
// Stored as a string because slices are not directly serializable in Go.
// It is stored as a string but can be converted back to a slice of VolumeMounts.
type SerializableVolumeMounts string

// ToVolumeMounts deserializes the JSON string into a slice of osb.VolumeMount.
// Returns an empty slice if the string is nil or empty.
func (v *SerializableVolumeMounts) ToVolumeMounts() (*[]osbClient.VolumeMount, error) {
	if v == nil || len([]byte(*v)) == 0 {
		empty := []osbClient.VolumeMount{}
		return &empty, nil
	}

	res := []osbClient.VolumeMount{}
	err := json.Unmarshal([]byte(*v), &res)
	return &res, err
}

// SerializableEndpoints represents a JSON-encoded slice of endpoints.
// Stored as a string because slices are not directly serializable in Go.
// It is stored as a string but can be converted back to a slice of Endpoints.
type SerializableEndpoints string

// ToEndpoints deserializes the JSON string into a slice of osb.Endpoint.
// Returns an empty slice if the string is nil or empty.
func (v *SerializableEndpoints) ToEndpoints() (*[]osbClient.Endpoint, error) {
	if v == nil || len([]byte(*v)) == 0 {
		empty := []osbClient.Endpoint{}
		return &empty, nil
	}

	res := []osbClient.Endpoint{}
	err := json.Unmarshal([]byte(*v), &res)
	return &res, err
}

// String returns the underlying string representation of SerializableEndpoints.
// Returns an empty string if the receiver is nil.
func (v *SerializableEndpoints) String() string {
	if v == nil {
		return ""
	}
	return string(*v)
}

// A ServiceBindingSpec defines the desired state of a ServiceBinding.
type ServiceBindingSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              ServiceBindingParameters `json:"forProvider"`
}

// A ServiceBindingStatus represents the observed state of a ServiceBinding.
type ServiceBindingStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ServiceBindingObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status

// A ServiceBinding is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,osb}
type ServiceBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceBindingSpec   `json:"spec"`
	Status ServiceBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceBindingList contains a list of ServiceBinding
type ServiceBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceBinding `json:"items"`
}

// ServiceBinding type metadata.
var (
	ServiceBindingKind             = reflect.TypeOf(ServiceBinding{}).Name()
	ServiceBindingGroupKind        = schema.GroupKind{Group: Group, Kind: ServiceBindingKind}.String()
	ServiceBindingKindAPIVersion   = ServiceBindingKind + "." + SchemeGroupVersion.String()
	ServiceBindingGroupVersionKind = SchemeGroupVersion.WithKind(ServiceBindingKind)
)

func init() {
	SchemeBuilder.Register(&ServiceBinding{}, &ServiceBindingList{})
}

// TODO add GetName() function returning the observed uuid

// Custom Response Type for OSB
type responseData struct {
	Parameters      map[string]any
	Endpoints       *[]osbClient.Endpoint
	VolumeMounts    *[]osbClient.VolumeMount
	RouteServiceURL *string
	SyslogDrainURL  *string
	Metadata        *osbClient.BindingMetadata
}

type BindingData struct {
	InstanceData    common.InstanceData
	ApplicationData common.ApplicationData
}
