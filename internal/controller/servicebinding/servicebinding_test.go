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

package servicebinding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	osbfake "github.com/orange-cloudfoundry/go-open-service-broker-client/v2/fake"
	"github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"github.com/orange-cloudfoundry/provider-osb/internal/mymock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	apiscommonv2 "github.com/crossplane/crossplane-runtime/v2/apis/common"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	b64 "encoding/base64"
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

type notServiceBinding struct {
	resource.Managed
}

var (
	errPanic         = errors.New("unknown error, panic error")
	basicCredentials = map[string][]byte{
		"user":     []byte("basic-user"),
		"password": []byte("basic-password"),
	}
	updatedCredentials = map[string][]byte{
		"user":     []byte("basic-user"),
		"password": []byte("another-basic-password"),
	}
)

var (
	basicParameters      common.SerializableParameters = common.SerializableParameters("{\"param1\":\"value1\"}")
	basicRouteServiceURL                               = "basic-route-service-url"
	basicSyslogDrainURL                                = "basic-syslog-drain-url"
	basicBinding                                       = &v1alpha1.ServiceBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       v1alpha1.ServiceBindingKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "basic-binding",
			Namespace: "basic-namespace",
			Annotations: map[string]string{
				meta.AnnotationKeyExternalName: "basicUuid",
			},
		},
		Spec: v1alpha1.ServiceBindingSpec{
			ForProvider: v1alpha1.ServiceBindingParameters{
				AppGuid: "basic-application",
				Context: common.KubernetesOSBContext{
					Platform:             "basic-platform",
					Namespace:            "basic-namespace",
					NamespaceAnnotations: map[string]string{},
					InstanceAnnotations:  map[string]string{},
					ClusterId:            "basic-cluster",
					InstanceName:         "basic-instance",
				},
				Parameters: basicParameters,
				Route:      "basic-route",
				InstanceData: &common.InstanceData{
					PlanId:     "basic-plan",
					ServiceId:  "basic-service",
					InstanceId: "basic-instance",
				},
			},
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &apiscommonv2.ProviderConfigReference{
					Name: "basic-providerconfig",
				},
				ManagementPolicies: xpv1.ManagementPolicies{xpv1.ManagementActionAll},
			},
		},
	}
	basicObservation = v1alpha1.ServiceBindingObservation{
		Parameters:      common.SerializableParameters("{\"param1\":\"value1\"}"),
		RouteServiceURL: &basicRouteServiceURL,
		Endpoints:       v1alpha1.SerializableEndpoints("[{\"host\":\"basic-host-1\",\"ports\":[443,8080],\"protocol\":\"tcp\"},{\"host\":\"basic-host-2\",\"ports\":[443],\"protocol\":\"udp\"}]"),
		VolumeMounts:    v1alpha1.SerializableVolumeMounts("[{\"driver\":\"basic-driver\",\"container_dir\":\"basic-dir\",\"mode\":\"basic-mode\",\"device_type\":\"basic-device-type\",\"device\":{\"volume_id\":\"basic-volume\"}}]"),
		SyslogDrainURL:  &basicSyslogDrainURL,
		Metadata: &osbClient.BindingMetadata{
			ExpiresAt:   time.Now().AddDate(0, 0, 7).Format(util.Iso8601dateFormat), // expires in 7 days
			RenewBefore: time.Now().AddDate(0, 0, 6).Format(util.Iso8601dateFormat), // renew before 6 days
		},
	}
)

/*
func withMetadata(binding v1alpha1.ServiceBinding, metadata *osb.BindingMetadata) *v1alpha1.ServiceBinding {
	binding.Status.AtProvider.Metadata = metadata
	return &binding
}
*/

func withLastOperationState(binding v1alpha1.ServiceBinding, state osbClient.LastOperationState) *v1alpha1.ServiceBinding {
	binding.Status.AtProvider.LastOperationState = state
	return &binding
}

func withObservation(binding v1alpha1.ServiceBinding, observation v1alpha1.ServiceBindingObservation) *v1alpha1.ServiceBinding {
	binding.Status.AtProvider = observation
	return &binding
}

func encodeCredentialsBase64(creds map[string][]byte) map[string][]byte {
	res := make(map[string][]byte, len(creds))
	for k, v := range creds {
		res[k] = []byte("\"" + b64.StdEncoding.EncodeToString(v) + "\"")
	}
	return res
}

func generateResponse[T osbClient.GetBindingResponse | osbClient.BindResponse](resp *T, creds map[string][]byte) error {
	credentials := make(map[string]any, len(creds))
	for k, v := range creds {
		credentials[k] = v
	}

	endpoints, err := basicObservation.Endpoints.ToEndpoints()
	if err != nil {
		return err
	}

	volumeMounts, err := basicObservation.VolumeMounts.ToVolumeMounts()
	if err != nil {
		return err
	}

	if getBindingResp, ok := any(resp).(*osbClient.GetBindingResponse); ok {
		parameters, err := basicParameters.ToParameters()
		if err != nil {
			return err
		}
		getBindingResp.Credentials = credentials
		getBindingResp.Endpoints = endpoints
		getBindingResp.Parameters = parameters
		getBindingResp.VolumeMounts = volumeMounts
		getBindingResp.Metadata = basicObservation.Metadata
		getBindingResp.RouteServiceURL = &basicRouteServiceURL
		getBindingResp.SyslogDrainURL = &basicSyslogDrainURL
	}
	if bindResp, ok := any(resp).(*osbClient.BindResponse); ok {
		bindResp.Credentials = credentials
		bindResp.Endpoints = endpoints
		bindResp.VolumeMounts = volumeMounts
		bindResp.Metadata = basicObservation.Metadata
		bindResp.RouteServiceURL = &basicRouteServiceURL
		bindResp.SyslogDrainURL = &basicSyslogDrainURL
	}
	return nil
}

func newFakeKubeClient(t *testing.T) *mymock.MockClient {
	ctrl := gomock.NewController(t)
	mock := mymock.NewMockClient(ctrl)

	mock.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
			sb, ok := obj.(*v1alpha1.ServiceBinding)
			if !ok {
				return fmt.Errorf("unexpected type %T", obj)
			}
			sb.Status = v1alpha1.ServiceBindingStatus{
				AtProvider: v1alpha1.ServiceBindingObservation{
					LastOperationState: osbClient.StateInProgress,
				},
			}
			sb.Spec = v1alpha1.ServiceBindingSpec{}
			sb.SetName(key.Name)
			sb.SetNamespace(key.Namespace)
			return nil
		},
	)

	mock.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	mock.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	mock.EXPECT().Delete(gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

	return mock
}

func TestObserve(t *testing.T) {
	type fields struct {
		kube          client.Client
		osb           osbClient.Client
		oid           osbClient.OriginatingIdentity
		rotateBinding bool
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotServiceBinding": {
			args: args{
				mg: notServiceBinding{},
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.New("managed resource is not a ServiceBinding custom resource"),
			},
		},
		"NotResourceExists": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*basicBinding, osbClient.StateSucceeded),
			},

			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: &osbfake.GetBindingReaction{
						Error: osbClient.HTTPStatusCodeError{
							StatusCode: http.StatusNotFound,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   false,
					ResourceUpToDate: false,
				},
			},
		},
		"ObserveStateFailed": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: &osbfake.GetBindingReaction{
						Error: errPanic,
					},
				},
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errFailedToObserveState,
			},
		},
		"ResourceUpToDateCredentialsChanged": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osbClient.StateSucceeded),
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osbClient.GetBindingResponse, error) {
						resp := &osbClient.GetBindingResponse{}
						err := generateResponse(resp, updatedCredentials)
						return resp, err
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentialsBase64(updatedCredentials),
				},
			},
		},
		"ResourceHasPendingLastOperationStillInProgress": {
			args: args{
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osbClient.StateInProgress),
			},
			fields: fields{
				kube: newFakeKubeClient(t),
				osb: &osbfake.FakeClient{
					PollBindingLastOperationReaction: &osbfake.PollBindingLastOperationReaction{
						Response: &osbClient.LastOperationResponse{
							State: osbClient.StateInProgress,
						},
					},
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				// Requeue without doing anything, since an operation is still in progress
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"ResourceExistsCredentialsShouldBeRotated": {
			args: args{
				mg: withObservation(*basicBinding, basicObservation),
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osbClient.GetBindingResponse, error) {
						resp := &osbClient.GetBindingResponse{}
						err := generateResponse(resp, basicCredentials)
						// Set renewbefore date to yesterday
						resp.Metadata = &osbClient.BindingMetadata{
							RenewBefore: time.Now().AddDate(0, 0, -1).Format(util.Iso8601dateFormat),
							ExpiresAt:   time.Now().AddDate(0, 0, 7).Format(util.Iso8601dateFormat),
						}
						return resp, err
					}),
				},
				rotateBinding: true,
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		"ResourceExistsCredentialsExpireSoonAndNoRotate": {
			args: args{
				mg: withObservation(*basicBinding, basicObservation),
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osbClient.GetBindingResponse, error) {
						resp := &osbClient.GetBindingResponse{}
						err := generateResponse(resp, basicCredentials)
						// Set renewbefore date to 2 days ago, expires to yesterday
						resp.Metadata = &osbClient.BindingMetadata{
							RenewBefore: time.Now().AddDate(0, 0, -1).Format(util.Iso8601dateFormat),
							ExpiresAt:   time.Now().AddDate(0, 0, 7).Format(util.Iso8601dateFormat),
						}
						return resp, err
					}),
				},
				rotateBinding: false,
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentialsBase64(basicCredentials),
				},
				err: nil,
			},
		},
		"ResourceExistsCredentialsAreExpired": {
			args: args{
				mg: withObservation(*basicBinding, basicObservation),
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osbClient.GetBindingResponse, error) {
						resp := &osbClient.GetBindingResponse{}
						err := generateResponse(resp, basicCredentials)
						// Set renewbefore date to 2 days ago, expires to yesterday
						resp.Metadata = &osbClient.BindingMetadata{
							RenewBefore: time.Now().AddDate(0, 0, -2).Format(util.Iso8601dateFormat),
							ExpiresAt:   time.Now().AddDate(0, 0, -1).Format(util.Iso8601dateFormat),
						}
						return resp, err
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentialsBase64(basicCredentials),
				},
				err: nil,
			},
		},
		"ResourceUpToDate": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osbClient.StateSucceeded),
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osbClient.GetBindingResponse, error) {
						resp := &osbClient.GetBindingResponse{}
						err := generateResponse(resp, basicCredentials)
						return resp, err
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentialsBase64(basicCredentials),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				kube:                tc.fields.kube,
				osb:                 tc.fields.osb,
				originatingIdentity: tc.fields.oid,
				rotateBinding:       tc.fields.rotateBinding,
			}

			got, err := e.Observe(tc.args.ctx, tc.args.mg)

			if err != nil {
				t.Logf("Original error: %+v", err)
			}

			if diff := cmp.Diff(tc.want.err, errors.Unwrap(err), test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	type fields struct {
		osb osbClient.Client
		oid osbClient.OriginatingIdentity
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotServiceBinding": {
			args: args{
				mg: notServiceBinding{},
			},
			want: want{
				o:   managed.ExternalCreation{},
				err: errors.New("managed resource is not a ServiceBinding custom resource"),
			},
		},
		"BindFailed": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					BindReaction: &osbfake.BindReaction{
						Error: errPanic,
					},
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				o:   managed.ExternalCreation{},
				err: errFailedToBindServiceBinding,
			},
		},
		"SuccessCreate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					BindReaction: osbfake.DynamicBindReaction(func(req *osbClient.BindRequest) (*osbClient.BindResponse, error) {
						resp := &osbClient.BindResponse{}
						err := generateResponse(resp, basicCredentials)
						return resp, err
					}),
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				o: managed.ExternalCreation{
					ConnectionDetails: encodeCredentialsBase64(basicCredentials),
				},
			},
		},
		"AsyncCreate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					BindReaction: &osbfake.BindReaction{
						Response: &osbClient.BindResponse{
							Async: true,
						},
					},
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				// no credentials, because async
				o: managed.ExternalCreation{
					AdditionalDetails: managed.AdditionalDetails{
						"async": "true",
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				osb:                 tc.fields.osb,
				originatingIdentity: tc.fields.oid,
			}

			got, err := e.Create(tc.args.ctx, tc.args.mg)

			if err != nil {
				t.Logf("Original error: %+v", err)
			}

			if diff := cmp.Diff(tc.want.err, errors.Unwrap(err), test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	type fields struct {
		osb osbClient.Client
		oid osbClient.OriginatingIdentity
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalUpdate
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotServiceBinding": {
			args: args{
				mg: notServiceBinding{},
			},
			want: want{
				o:   managed.ExternalUpdate{},
				err: errors.New("managed resource is not a ServiceBinding custom resource"),
			},
		},
		"SuccessUpdateBindingRotation": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					RotateBindingReaction: osbfake.DynamicRotateBindingReaction(func(_ *osbClient.RotateBindingRequest) (*osbClient.BindResponse, error) {
						resp := &osbClient.BindResponse{}
						err := generateResponse(resp, updatedCredentials)
						return resp, err
					}),
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				o: managed.ExternalUpdate{
					ConnectionDetails: encodeCredentialsBase64(updatedCredentials),
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				osb:                 tc.fields.osb,
				originatingIdentity: tc.fields.oid,
			}

			got, err := e.Update(tc.args.ctx, tc.args.mg)

			if err != nil {
				t.Logf("Original error: %+v", err)
			}

			if diff := cmp.Diff(tc.want.err, errors.Unwrap(err), test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	type fields struct {
		osb osbClient.Client
		oid osbClient.OriginatingIdentity
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalDelete
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotServiceBinding": {
			args: args{
				mg: notServiceBinding{},
			},
			want: want{
				o:   managed.ExternalDelete{},
				err: errors.New("managed resource is not a ServiceBinding custom resource"),
			},
		},
		"SuccessDelete": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					UnbindReaction: &osbfake.UnbindReaction{
						Response: &osbClient.UnbindResponse{
							Async: false,
						},
					},
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				o: managed.ExternalDelete{},
			},
		},
		"AsyncDelete": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				osb: &osbfake.FakeClient{
					UnbindReaction: &osbfake.UnbindReaction{
						Response: &osbClient.UnbindResponse{
							Async: true,
						},
					},
				},
				oid: osbClient.OriginatingIdentity{
					Platform: "kubernetes",
					Value:    "some value",
				},
			},
			want: want{
				// no credentials, because async
				o: managed.ExternalDelete{
					AdditionalDetails: managed.AdditionalDetails{
						"async": "true",
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				osb: tc.fields.osb,
				kube: &test.MockClient{
					MockUpdate: test.NewMockUpdateFn(nil),
				},
				originatingIdentity: tc.fields.oid,
			}

			got, err := e.Delete(tc.args.ctx, tc.args.mg)

			if err != nil {
				t.Logf("Original error: %+v", err)
			}

			if diff := cmp.Diff(tc.want.err, errors.Unwrap(err), test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
