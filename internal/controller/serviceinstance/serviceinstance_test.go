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

package serviceinstance

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	osbfake "github.com/orange-cloudfoundry/go-open-service-broker-client/v2/fake"
	apisbinding "github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	"github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
	mock "github.com/orange-cloudfoundry/provider-osb/internal/mymock"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

var (
	fakeServiceInstance = &v1alpha1.ServiceInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "example-instance",
			Namespace:  "default",
			Finalizers: []string{"finalizer.osb.crossplane.io"},
		},
		Spec: v1alpha1.ServiceInstanceSpec{
			ForProvider: common.InstanceData{
				InstanceId: "test-id",
				ServiceId:  "service-id-xyz",
				PlanId:     "plan-id-789",
			},
		},
	}
	stateInProgress = v1alpha1.ServiceInstanceStatus{
		AtProvider: v1alpha1.ServiceInstanceObservation{
			LastOperationState: osb.StateInProgress,
			HasActiveBindings:  false,
			LastOperationKey:   "op-key",
		},
	}
	stateDeletingNoBindings = v1alpha1.ServiceInstanceStatus{
		AtProvider: v1alpha1.ServiceInstanceObservation{
			LastOperationState: "deleting",
			HasActiveBindings:  false,
			LastOperationKey:   "op-key",
		},
	}
	deletionWithActiveBindings = &v1alpha1.ServiceInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "example-instance",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: v1alpha1.ServiceInstanceSpec{
			ForProvider: common.InstanceData{
				InstanceId: "test-id",
			},
		},
	}
)

func AddServiceInstanceStatus(si *v1alpha1.ServiceInstance, status v1alpha1.ServiceInstanceStatus) *v1alpha1.ServiceInstance {
	cpy := si.DeepCopy()
	cpy.Status = status
	return cpy
}

// newMockKubeClientForServiceInstance crée un client Kubernetes simulé pour un ServiceInstance donné.
func newMockKubeClientForServiceInstance(ctrl *gomock.Controller, si *v1alpha1.ServiceInstance) client.Client {
	// Crée le mock
	mockClient := mock.NewMockClient(ctrl) // NewMockClient généré par mockgen

	// Mock le Get() pour retourner l'objet ServiceInstance attendu
	mockClient.
		EXPECT().
		Get(gomock.Any(), client.ObjectKey{Name: "example-instance", Namespace: "default"}, gomock.Any()).
		DoAndReturn(func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			*obj.(*v1alpha1.ServiceInstance) = *si
			return nil
		}).
		AnyTimes()
	mockClient.
		EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			bindings := list.(*apisbinding.ServiceBindingList)
			bindings.Items = append(bindings.Items, apisbinding.ServiceBinding{
				Spec: apisbinding.ServiceBindingSpec{
					ForProvider: apisbinding.ServiceBindingParameters{
						InstanceRef: &common.NamespacedName{Name: "example-instance"},
					},
				},
			})
			return nil
		}).
		AnyTimes()
	// Mock le Status().Update()
	mockStatus := mock.NewMockSubResourceWriter(ctrl)
	mockStatus.
		EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return nil
		}).
		AnyTimes()

	mockClient.
		EXPECT().
		Status().
		Return(mockStatus).
		AnyTimes()

	return mockClient
}

func TestObserve(t *testing.T) {

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	type fields struct {
		client osb.Client
		kube   client.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	// notServiceInstance is a test double that does not implement ServiceInstance.
	type notServiceInstance struct {
		resource.Managed
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotServiceInstance": {
			reason: "An error should be returned if the managed resource is not a ServiceInstance.",
			fields: fields{},
			args: args{
				ctx: context.TODO(),
				mg:  &notServiceInstance{},
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.New(errNotServiceInstance),
			},
		},
		"InstanceIdMissing": {
			reason: "Should return error if InstanceId is not set in ServiceInstance spec",
			fields: fields{},
			args: args{
				ctx: context.TODO(),
				mg: &v1alpha1.ServiceInstance{
					Spec: v1alpha1.ServiceInstanceSpec{
						ForProvider: common.InstanceData{
							InstanceId: "",
						},
					},
				},
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.New("InstanceId must be set in ServiceInstance spec"),
			},
		},
		"LastOperationInProgress": {
			reason: "Should call handleLastOperationInProgress if LastOperationState is in progress",
			fields: fields{
				client: &osbfake.FakeClient{
					PollLastOperationReaction: &osbfake.PollLastOperationReaction{
						Response: &osb.LastOperationResponse{
							State: osb.StateInProgress,
						},
					},
				},
				kube: newMockKubeClientForServiceInstance(ctrl, AddServiceInstanceStatus(fakeServiceInstance, stateInProgress)),
			},
			args: args{
				ctx: context.TODO(),
				mg:  AddServiceInstanceStatus(fakeServiceInstance, stateInProgress),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"DeletingNoActiveBindings": {
			reason: "Should remove finalizer when deleting and no active bindings",
			fields: fields{
				client: &osbfake.FakeClient{
					PollLastOperationReaction: &osbfake.PollLastOperationReaction{
						Response: &osb.LastOperationResponse{
							State: osb.StateInProgress,
						},
					},
				},
				kube: newMockKubeClientForServiceInstance(ctrl, AddServiceInstanceStatus(fakeServiceInstance, stateDeletingNoBindings)),
			},
			args: args{
				ctx: context.TODO(),
				mg:  AddServiceInstanceStatus(fakeServiceInstance, stateDeletingNoBindings),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists: false,
				},
				err: nil,
			},
		},
		"DeletionWithActiveBindings": {
			reason: "Should check for active bindings when resource is being deleted",
			fields: fields{
				client: &osbfake.FakeClient{}, // Pas utilisé ici
				kube:   newMockKubeClientForServiceInstance(ctrl, deletionWithActiveBindings),
			},
			args: args{
				ctx: context.TODO(),
				mg:  deletionWithActiveBindings,
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"GetInstanceSuccessUpToDate": {
			reason: "Should return ResourceExists=true and ResourceUpToDate=true when GetInstance succeeds and spec is up to date",
			fields: fields{
				client: &osbfake.FakeClient{
					GetInstanceReaction: &osbfake.GetInstanceReaction{
						Response: &osb.GetInstanceResponse{
							DashboardURL: "https://dashboard.example.com",
							PlanID:       "plan-id-789",
							Parameters:   map[string]interface{}{},
						},
					},
				},
				kube: newMockKubeClientForServiceInstance(ctrl, fakeServiceInstance),
			},
			args: args{
				ctx: context.TODO(),
				mg:  fakeServiceInstance,
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
					ConnectionDetails: managed.ConnectionDetails{
						"dashboardURL": []byte("https://dashboard.example.com"),
					},
				},
				err: nil,
			},
		},
		"GetInstanceNotFound": {
			reason: "Should return ResourceExists=false when GetInstance returns 404",
			fields: fields{
				client: &osbfake.FakeClient{
					GetInstanceReaction: &osbfake.GetInstanceReaction{
						Error: &osb.HTTPStatusCodeError{StatusCode: http.StatusNotFound},
					},
				},
				kube: newMockKubeClientForServiceInstance(ctrl, fakeServiceInstance),
			},
			args: args{
				ctx: context.TODO(),
				mg:  fakeServiceInstance,
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists: false,
				},
				err: nil,
			},
		},
		"GetInstanceOtherError": {
			reason: "Should return error when GetInstance returns unexpected error",
			fields: fields{
				client: &osbfake.FakeClient{
					GetInstanceReaction: &osbfake.GetInstanceReaction{
						Error: errors.New("unexpected error"),
					},
				},
				kube: newMockKubeClientForServiceInstance(ctrl, fakeServiceInstance),
			},
			args: args{
				ctx: context.TODO(),
				mg:  fakeServiceInstance,
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.Wrap(errors.New("unexpected error"), "OSB GetInstance request failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				client: tc.fields.client,
				kube:   tc.fields.kube,
			}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
