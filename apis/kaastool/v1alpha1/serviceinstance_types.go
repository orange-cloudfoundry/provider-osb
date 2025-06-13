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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// ServiceInstanceParameters are the configurable fields of a ServiceInstance.
type ServiceInstanceParameters struct {
	Kind     string       `json:"kind"`
	Metadata Metadata     `json:"metadata"`
	Spec     InstanceSpec `json:"spec"`
	Status   string       `json:"status"`
}

type InstanceSpec struct {
	Application     string          `json:"application,omitempty"`
	ApplicationData ApplicationData `json:"applicationData,omitempty"`
	PlanId          int             `json:"planId"`
	Context         map[string]any  `json:"context,omitempty"`
	Parameters      map[string]any  `json:"parameters,omitempty"`
}

// ServiceInstanceObservation are the observable fields of a ServiceInstance.
type ServiceInstanceObservation struct {
	ObservableField string `json:"observableField,omitempty"`
}

// A ServiceInstanceSpec defines the desired state of a ServiceInstance.
type ServiceInstanceSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       ServiceInstanceParameters `json:"forProvider"`
}

// A ServiceInstanceStatus represents the observed state of a ServiceInstance.
type ServiceInstanceStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ServiceInstanceObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A ServiceInstance is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,osbprovider}
type ServiceInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceInstanceSpec   `json:"spec"`
	Status ServiceInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceInstanceList contains a list of ServiceInstance
type ServiceInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceInstance `json:"items"`
}

// ServiceInstance type metadata.
var (
	ServiceInstanceKind             = reflect.TypeOf(ServiceInstance{}).Name()
	ServiceInstanceGroupKind        = schema.GroupKind{Group: Group, Kind: ServiceInstanceKind}.String()
	ServiceInstanceKindAPIVersion   = ServiceInstanceKind + "." + SchemeGroupVersion.String()
	ServiceInstanceGroupVersionKind = SchemeGroupVersion.WithKind(ServiceInstanceKind)
)

func init() {
	SchemeBuilder.Register(&ServiceInstance{}, &ServiceInstanceList{})
}
