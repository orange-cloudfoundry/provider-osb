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
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	osbfake "github.com/orange-cloudfoundry/go-open-service-broker-client/v2/fake"
	"github.com/orange-cloudfoundry/provider-osb/apis/binding/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	panicError       = errors.New("unknown error, panic error")
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
		},
		Spec: v1alpha1.ServiceBindingSpec{
			ForProvider: v1alpha1.ServiceBindingParameters{
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
				ApplicationData: &common.ApplicationData{
					Name: "basic-application",
				},
			},
			ResourceSpec: xpv1.ResourceSpec{
				ProviderConfigReference: &xpv1.Reference{
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
		Metadata: &osb.BindingMetadata{
			ExpiresAt:   time.Now().AddDate(0, 0, 7).Format(util.Iso8601dateFormat), // expires in 7 days
			RenewBefore: time.Now().AddDate(0, 0, 6).Format(util.Iso8601dateFormat), // renew before 6 days
		},
		Uuid: "basicUuid",
	}
)

func withAnnotations(binding v1alpha1.ServiceBinding, annotations map[string]string) *v1alpha1.ServiceBinding {
	binding.ObjectMeta.Annotations = annotations
	return &binding
}

func withLastOperationState(binding v1alpha1.ServiceBinding, state osb.LastOperationState) *v1alpha1.ServiceBinding {
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

func generateResponse[T osb.GetBindingResponse | osb.BindResponse](resp *T, creds map[string][]byte) error {
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

	if getBindingResp, ok := any(resp).(*osb.GetBindingResponse); ok {
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
	if bindResp, ok := any(resp).(*osb.BindResponse); ok {
		bindResp.Credentials = credentials
		bindResp.Endpoints = endpoints
		bindResp.VolumeMounts = volumeMounts
		bindResp.Metadata = basicObservation.Metadata
		bindResp.RouteServiceURL = &basicRouteServiceURL
		bindResp.SyslogDrainURL = &basicSyslogDrainURL
	}
	return nil
}

func TestObserve(t *testing.T) {
	type fields struct {
		client osb.Client
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
				err: errors.New(errNotServiceBinding),
			},
		},
		"NotResourceExists": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*basicBinding, osb.StateSucceeded),
			},

			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: &osbfake.GetBindingReaction{
						Error: osb.HTTPStatusCodeError{
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
		"GetBindingFailed": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: &osbfake.GetBindingReaction{
						Error: panicError,
					},
				},
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.Wrap(panicError, fmt.Sprintf(errRequestFailed, "GetBinding")),
			},
		},
		"ResourceUpToDateCredentialsChanged": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osb.StateSucceeded),
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						resp := &osb.GetBindingResponse{}
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
		"ResourceUpToDate": {
			args: args{
				// We are testing the asynchronous way, since a succeeded or failed last operation
				// should not impact the workflow in any way
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osb.StateSucceeded),
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						resp := &osb.GetBindingResponse{}
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
		"ResourceHasPendingLastOperationStillInProgress": {
			args: args{
				mg: withLastOperationState(*withObservation(*basicBinding, basicObservation), osb.StateInProgress),
			},
			fields: fields{
				client: &osbfake.FakeClient{
					PollBindingLastOperationReaction: &osbfake.PollBindingLastOperationReaction{
						Response: &osb.LastOperationResponse{
							State: osb.StateInProgress,
						},
					},
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
		// TODO add test cases for specific errors
		// TODO add test case for binding rotation
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
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

func TestCreate(t *testing.T) {
	type fields struct {
		client osb.Client
		oid    osb.OriginatingIdentity
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
				err: errors.New(errNotServiceBinding),
			},
		},
		"ErrorCreate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					BindReaction: &osbfake.BindReaction{
						Error: panicError,
					},
				},
			},
			want: want{
				o:   managed.ExternalCreation{},
				err: errors.Wrap(panicError, fmt.Sprintf(errRequestFailed, "Bind")),
			},
		},
		"SuccessCreate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					BindReaction: osbfake.DynamicBindReaction(func(req *osb.BindRequest) (*osb.BindResponse, error) {
						resp := &osb.BindResponse{}
						err := generateResponse(resp, basicCredentials)
						return resp, err
					}),
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
				client: &osbfake.FakeClient{
					BindReaction: &osbfake.BindReaction{
						Response: &osb.BindResponse{
							Async: true,
						},
					},
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
		// TODO add test cases for originating identity header
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client, originatingIdentity: tc.fields.oid}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	type fields struct {
		client osb.Client
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
				err: errors.New(errNotServiceBinding),
			},
		},
		"SuccessUpdate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					RotateBindingReaction: osbfake.DynamicRotateBindingReaction(func(_ *osb.RotateBindingRequest) (*osb.BindResponse, error) {
						resp := &osb.BindResponse{}
						err := generateResponse(resp, updatedCredentials)
						return resp, err
					}),
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
			e := external{client: tc.fields.client}
			got, err := e.Update(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Update(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Update(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	type fields struct {
		client osb.Client
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
				err: errors.New(errNotServiceBinding),
			},
		},
		"SuccessDelete": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					UnbindReaction: &osbfake.UnbindReaction{
						Response: &osb.UnbindResponse{
							Async: false,
						},
					},
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
				client: &osbfake.FakeClient{
					UnbindReaction: &osbfake.UnbindReaction{
						Response: &osb.UnbindResponse{
							Async: true,
						},
					},
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
		// TODO add more test cases
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
			got, err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
