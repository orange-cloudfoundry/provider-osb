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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	common "github.com/orange-cloudfoundry/provider-osb/apis/common"
)

type ServiceInstanceParameters struct {
	ApplicationRef   *common.NamespacedName        `json:"application,omitempty"`
	ApplicationData  *common.ApplicationData       `json:"applicationData,omitempty"`
	InstanceId       string                        `json:"instanceId"`
	PlanId           string                        `json:"planId"`
	ServiceId        string                        `json:"serviceId"`
	Context          common.KubernetesOSBContext   `json:"context,omitempty"`
	Parameters       common.SerializableParameters `json:"parameters,omitempty"`
	OrganizationGuid string                        `json:"organizationGuid"`
	SpaceGuid        string                        `json:"spaceGuid"`
}

type ServiceInstanceSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`

	ForProvider ServiceInstanceParameters `json:"forProvider"`
}

type ServiceInstanceStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ServiceInstanceObservation `json:"atProvider,omitempty"`
}

type ServiceInstanceObservation struct {
	ApplicationRef           *common.NamespacedName       `json:"application,omitempty"`
	ApplicationData          *common.ApplicationData      `json:"applicationData,omitempty"`
	InstanceId               string                       `json:"instanceId"`
	ServiceId                string                       `json:"serviceId"`
	PlanId                   string                       `json:"planId"`
	Context                  common.KubernetesOSBContext  `json:"context,omitempty"`
	DashboardURL             *string                      `json:"dashboardURL,omitempty"`
	LastOperationState       osbClient.LastOperationState `json:"lastOperationState,omitempty"`
	LastOperationKey         osbClient.OperationKey       `json:"lastOperationKey,omitempty"`
	LastOperationDescription string                       `json:"lastOperationDescription,omitempty"`
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

	Spec   ServiceInstanceSpec   `json:"spec"`
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
