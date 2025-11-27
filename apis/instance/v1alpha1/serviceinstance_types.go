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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/orange-cloudfoundry/provider-osb/apis/common"
)

type ServiceInstanceSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`

	// +kubebuilder:validation:Required
	ForProvider common.InstanceData `json:"forProvider"`
}

type ServiceInstanceStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	// +kubebuilder:validation:Optional
	AtProvider ServiceInstanceObservation `json:"atProvider,omitempty"`
}

type ServiceInstanceObservation struct {
	// +kubebuilder:validation:Optional
	AppGuid string `json:"appGuid,omitempty"`
	// +kubebuilder:validation:Optional
	InstanceId string `json:"instanceId"`
	// +kubebuilder:validation:Optional
	PlanId string `json:"planId"`
	// +kubebuilder:validation:Optional
	ServiceId string `json:"serviceId"`
	// +kubebuilder:validation:Optional
	Context common.KubernetesOSBContext `json:"context,omitempty"`
	// +kubebuilder:validation:Optional
	Parameters common.SerializableParameters `json:"parameters,omitempty"`
	// +kubebuilder:validation:Optional
	OrganizationGuid string `json:"organizationGuid"`
	// +kubebuilder:validation:Optional
	SpaceGuid string `json:"spaceGuid"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^https?://.+`
	DashboardURL *string `json:"dashboardURL,omitempty"`
	// +kubebuilder:validation:Optional
	LastOperationState osbClient.LastOperationState `json:"lastOperationState,omitempty"`
	// +kubebuilder:validation:Optional
	LastOperationKey osbClient.OperationKey `json:"lastOperationKey,omitempty"`
	// +kubebuilder:validation:Optional
	LastOperationDescription string `json:"lastOperationDescription,omitempty"`
	// +kubebuilder:validation:Optional
	HasActiveBindings bool `json:"hasActiveBindings,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion

// ServiceInstance is the managed OSB resource
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,osb}
type ServiceInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec ServiceInstanceSpec `json:"spec"`
	// +kubebuilder:validation:Optional
	Status ServiceInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceInstanceList contains a list of ServiceInstances
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
