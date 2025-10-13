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

	"github.com/orange-cloudfoundry/provider-osb/apis/common"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
)

// ServiceBindingParameters are the configurable fields of a ServiceBinding.
// +kubebuilder:validation:XValidation:rule="has(self.application) || has(self.applicationData) || has(self.instance) || has(self.instanceData)",message="At least one of application, applicationData, instance, instanceData must be defined"
type ServiceBindingParameters struct {
	// +kubebuilder:validation:Optional
	ApplicationRef *common.NamespacedName `json:"application,omitempty"`

	// +kubebuilder:validation:Optional
	ApplicationData *common.ApplicationData `json:"applicationData,omitempty"`

	// +kubebuilder:validation:Optional
	InstanceRef *common.NamespacedName `json:"instance,omitempty"`

	// +kubebuilder:validation:Optional
	InstanceData *common.InstanceData `json:"instanceData,omitempty"`

	// +kubebuilder:validation:Optional
	Context common.KubernetesOSBContext `json:"context,omitempty"`

	// +kubebuilder:validation:Optional
	Parameters common.SerializableParameters `json:"parameters,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https?://.+`
	Route string `json:"route,omitempty"`
	// TODO manage additional attributes
}

// ServiceBindingObservation are the observable fields of a ServiceBinding.
type ServiceBindingObservation struct {
	// TODO add context and route to test if these were updated and return error if so
	// +kubebuilder:validation:Optional
	Parameters common.SerializableParameters `json:"parameters,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https?://.+`
	RouteServiceURL *string `json:"route_service_url,omitempty"`

	// +kubebuilder:validation:Optional
	Endpoints SerializableEndpoints `json:"endpoints,omitempty"`

	// +kubebuilder:validation:Optional
	VolumeMounts SerializableVolumeMounts `json:"volume_mounts,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https?://.+`
	SyslogDrainURL *string `json:"syslog_drain_url,omitempty"`

	// +kubebuilder:validation:Optional
	Metadata *osb.BindingMetadata `json:"metadata,omitempty"`

	// +kubebuilder:validation:Optional
	LastOperationState osb.LastOperationState `json:"last_operation_state,omitempty"`

	// +kubebuilder:validation:Optional
	LastOperationKey osb.OperationKey `json:"last_operation_key,omitempty"`

	// +kubebuilder:validation:Optional
	LastOperationDescription string `json:"last_operation_description,omitempty"`

	// +kubebuilder:validation:Optional
	LastOperationPolledTime string `json:"last_operation_polled_time,omitempty"`
}

type SerializableVolumeMounts string

func (v *SerializableVolumeMounts) ToVolumeMounts() (*[]osb.VolumeMount, error) {
	if v == nil || len([]byte(*v)) == 0 {
		return &[]osb.VolumeMount{}, nil
	}
	res := &[]osb.VolumeMount{}
	err := json.Unmarshal([]byte(*v), &res)
	return res, err
}

// +kubebuilder:validation:Type=string
type SerializableEndpoints string

func (v *SerializableEndpoints) ToEndpoints() (*[]osb.Endpoint, error) {
	if v == nil || len([]byte(*v)) == 0 {
		return &[]osb.Endpoint{}, nil
	}
	res := &[]osb.Endpoint{}
	err := json.Unmarshal([]byte(*v), &res)
	return res, err
}

func (v *SerializableEndpoints) String() string {
	if v == nil {
		return ""
	}
	return string(*v)
}

// A ServiceBindingSpec defines the desired state of a ServiceBinding.
type ServiceBindingSpec struct {
	xpv1.ResourceSpec `json:",inline"`

	// +kubebuilder:validation:Required
	ForProvider ServiceBindingParameters `json:"forProvider"`
}

// A ServiceBindingStatus represents the observed state of a ServiceBinding.
type ServiceBindingStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// +kubebuilder:validation:Optional
	AtProvider ServiceBindingObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A ServiceBinding is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,osb}
type ServiceBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec ServiceBindingSpec `json:"spec"`

	// +kubebuilder:validation:Optional
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
