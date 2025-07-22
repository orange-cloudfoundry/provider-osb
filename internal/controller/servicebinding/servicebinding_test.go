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
	"encoding/json"
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
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

var basicBinding = &v1alpha1.ServiceBinding{
	TypeMeta: metav1.TypeMeta{
		APIVersion: v1alpha1.SchemeGroupVersion.String(),
		Kind:       v1alpha1.ServiceBindingKind,
	},
	ObjectMeta: metav1.ObjectMeta{
		Name:      "basic-binding",
		Namespace: "basic-namespace",
		Annotations: map[string]string{
			syslogDrainURLAnnotation: "basic-syslog-drain-url",
			volumeMountsAnnotation:   "[{\"driver\":\"basic-driver\",\"container_dir\":\"basic-dir\",\"mode\":\"basic-mode\",\"device_type\":\"basic-device-type\",\"device\":{\"volume_id\":\"basic-volume\"}}]",
			endpointsAnnotation:      "[{\"host\":\"basic-host-1\",\"ports\":[443,8080],\"protocol\":\"tcp\"},{\"host\":\"basic-host-2\",\"ports\":[443],\"protocol\":\"udp\"}]",
			expiresAtAnnotation:      time.Now().AddDate(0, 0, 7).Format(util.Iso8601dateFormat), // expires in 7 days
			renewBeforeAnnotation:    time.Now().AddDate(0, 0, 6).Format(util.Iso8601dateFormat), // renew before 6 days
		},
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
			Parameters: &v1.JSON{Raw: []byte("{\"param1\":\"value1\"}")},
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

func withAnnotations(binding v1alpha1.ServiceBinding, annotations map[string]string) *v1alpha1.ServiceBinding {
	binding.ObjectMeta.Annotations = annotations
	return &binding
}

func encodeCredentials(creds map[string][]byte) map[string][]byte {
	res := make(map[string][]byte, len(creds))
	for k, v := range creds {
		res[k] = []byte("\"" + b64.StdEncoding.EncodeToString(v) + "\"")
	}
	return res
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
				mg: basicBinding,
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
				err: errors.Wrap(panicError, "GetBinding request failed"), // TODO variable
			},
		},
		"NotResourceUpToDate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					// TODO refacto all this
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						syslogDrainUrl := basicBinding.ObjectMeta.Annotations[syslogDrainURLAnnotation] + "-new" // changed from broker

						volumeMountsFromAnnotation := basicBinding.ObjectMeta.Annotations[volumeMountsAnnotation]
						var volumeMounts []osb.VolumeMount
						err := json.Unmarshal([]byte(volumeMountsFromAnnotation), &volumeMounts)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing volume mounts")
						}

						endpointsFromAnnotation := basicBinding.ObjectMeta.Annotations[endpointsAnnotation]
						var endpoints []osb.Endpoint
						err = json.Unmarshal([]byte(endpointsFromAnnotation), &endpoints)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing endpoints")
						}

						parametersBytes := basicBinding.Spec.ForProvider.Parameters.Raw
						var parameters map[string]any
						err = json.Unmarshal(parametersBytes, &parameters)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing parameters ")
						}

						credentials := make(map[string]any, len(basicCredentials))
						for k, v := range basicCredentials {
							credentials[k] = v
						}

						res := &osb.GetBindingResponse{
							Credentials:     credentials,
							SyslogDrainURL:  &syslogDrainUrl,
							VolumeMounts:    volumeMounts,
							Endpoints:       &endpoints,
							RouteServiceURL: &basicBinding.Spec.ForProvider.Route,
							Parameters:      parameters,
							Metadata: &osb.BindingMetadata{
								ExpiresAt:   basicBinding.ObjectMeta.Annotations[expiresAtAnnotation],
								RenewBefore: basicBinding.ObjectMeta.Annotations[renewBeforeAnnotation],
							},
						}
						return res, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  false,
					ConnectionDetails: encodeCredentials(basicCredentials),
				},
			},
		},
		"ResourceUpToDateCredentialsChanged": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						syslogDrainUrl := basicBinding.ObjectMeta.Annotations[syslogDrainURLAnnotation]

						volumeMountsFromAnnotation := basicBinding.ObjectMeta.Annotations[volumeMountsAnnotation]
						var volumeMounts []osb.VolumeMount
						err := json.Unmarshal([]byte(volumeMountsFromAnnotation), &volumeMounts)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing volume mounts")
						}

						endpointsFromAnnotation := basicBinding.ObjectMeta.Annotations[endpointsAnnotation]
						var endpoints []osb.Endpoint
						err = json.Unmarshal([]byte(endpointsFromAnnotation), &endpoints)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing endpoints")
						}

						parametersBytes := basicBinding.Spec.ForProvider.Parameters.Raw
						var parameters map[string]any
						err = json.Unmarshal(parametersBytes, &parameters)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing parameters ")
						}

						credentials := make(map[string]any, len(updatedCredentials)) // update credentials
						for k, v := range updatedCredentials {
							credentials[k] = v
						}

						res := &osb.GetBindingResponse{
							Credentials:     credentials,
							SyslogDrainURL:  &syslogDrainUrl,
							VolumeMounts:    volumeMounts,
							Endpoints:       &endpoints,
							RouteServiceURL: &basicBinding.Spec.ForProvider.Route,
							Parameters:      parameters,
							Metadata: &osb.BindingMetadata{
								ExpiresAt:   basicBinding.ObjectMeta.Annotations[expiresAtAnnotation],
								RenewBefore: basicBinding.ObjectMeta.Annotations[renewBeforeAnnotation],
							},
						}
						return res, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentials(updatedCredentials),
				},
			},
		},
		"ResourceUpToDate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						syslogDrainUrl := basicBinding.ObjectMeta.Annotations[syslogDrainURLAnnotation]

						volumeMountsFromAnnotation := basicBinding.ObjectMeta.Annotations[volumeMountsAnnotation]
						var volumeMounts []osb.VolumeMount
						err := json.Unmarshal([]byte(volumeMountsFromAnnotation), &volumeMounts)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing volume mounts")
						}

						endpointsFromAnnotation := basicBinding.ObjectMeta.Annotations[endpointsAnnotation]
						var endpoints []osb.Endpoint
						err = json.Unmarshal([]byte(endpointsFromAnnotation), &endpoints)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing endpoints")
						}

						parametersBytes := basicBinding.Spec.ForProvider.Parameters.Raw
						var parameters map[string]any
						err = json.Unmarshal(parametersBytes, &parameters)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing parameters ")
						}

						// TODO refacto this specific part
						credentials := make(map[string]any, len(basicCredentials))
						for k, v := range basicCredentials {
							credentials[k] = v
						}

						res := &osb.GetBindingResponse{
							Credentials:     credentials,
							SyslogDrainURL:  &syslogDrainUrl,
							VolumeMounts:    volumeMounts,
							Endpoints:       &endpoints,
							RouteServiceURL: &basicBinding.Spec.ForProvider.Route,
							Parameters:      parameters,
							Metadata: &osb.BindingMetadata{
								ExpiresAt:   basicBinding.ObjectMeta.Annotations[expiresAtAnnotation],
								RenewBefore: basicBinding.ObjectMeta.Annotations[renewBeforeAnnotation],
							},
						}
						return res, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: encodeCredentials(basicCredentials),
				},
			},
		},
		"ResourceExistsAsyncNotSupported": {
			args: args{
				mg: withAnnotations(*basicBinding, map[string]string{
					util.AsyncAnnotation: "true",
				}),
			},
			want: want{
				o:   managed.ExternalObservation{},
				err: errors.New("Async requests are not supported yet."),
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
		"AsyncNotSupported": {
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
				o:   managed.ExternalCreation{},
				err: errors.New("Async requests are not supported yet."),
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
				err: errors.Wrap(panicError, "Bind request failed"), // TODO variable
			},
		},
		"SuccessCreate": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					BindReaction: osbfake.DynamicBindReaction(func(req *osb.BindRequest) (*osb.BindResponse, error) {
						// TODO refacto this code
						credentials := make(map[string]any, len(basicCredentials))
						for k, v := range basicCredentials {
							credentials[k] = v
						}

						return &osb.BindResponse{
							Async:       false,
							Credentials: credentials,
						}, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalCreation{
					ConnectionDetails: encodeCredentials(basicCredentials),
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
				o:   managed.ExternalUpdate{},
				err: errors.Wrap(panicError, "GetBinding request failed"), // TODO variable
			},
		},
		"Success": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					GetBindingReaction: osbfake.DynamicGetBindingReaction(func() (*osb.GetBindingResponse, error) {
						syslogDrainUrl := basicBinding.ObjectMeta.Annotations[syslogDrainURLAnnotation]

						volumeMountsFromAnnotation := basicBinding.ObjectMeta.Annotations[volumeMountsAnnotation]
						var volumeMounts []osb.VolumeMount
						err := json.Unmarshal([]byte(volumeMountsFromAnnotation), &volumeMounts)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing volume mounts")
						}

						endpointsFromAnnotation := basicBinding.ObjectMeta.Annotations[endpointsAnnotation]
						var endpoints []osb.Endpoint
						err = json.Unmarshal([]byte(endpointsFromAnnotation), &endpoints)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing endpoints")
						}

						parametersBytes := basicBinding.Spec.ForProvider.Parameters.Raw
						var parameters map[string]any
						err = json.Unmarshal(parametersBytes, &parameters)
						if err != nil {
							return nil, errors.Wrap(err, "Failed parsing parameters")
						}

						credentials := make(map[string]any, len(basicCredentials))
						for k, v := range basicCredentials {
							credentials[k] = v
						}

						res := &osb.GetBindingResponse{
							Credentials:     credentials,
							SyslogDrainURL:  &syslogDrainUrl,
							VolumeMounts:    volumeMounts,
							Endpoints:       &endpoints,
							RouteServiceURL: &basicBinding.Spec.ForProvider.Route,
							Parameters:      parameters,
							Metadata: &osb.BindingMetadata{
								ExpiresAt:   basicBinding.ObjectMeta.Annotations[expiresAtAnnotation],
								RenewBefore: basicBinding.ObjectMeta.Annotations[renewBeforeAnnotation],
							},
						}
						return res, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalUpdate{},
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
		"AsyncNotSupported": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					UnbindReaction: osbfake.DynamicUnbindReaction(func(*osb.UnbindRequest) (*osb.UnbindResponse, error) {
						return &osb.UnbindResponse{
							Async: true,
						}, nil
					}),
				},
			},
			want: want{
				o:   managed.ExternalDelete{},
				err: errors.New("Async requests are not supported yet."),
			},
		},
		"Success": {
			args: args{
				mg: basicBinding,
			},
			fields: fields{
				client: &osbfake.FakeClient{
					UnbindReaction: osbfake.DynamicUnbindReaction(func(*osb.UnbindRequest) (*osb.UnbindResponse, error) {
						return &osb.UnbindResponse{
							Async: false,
						}, nil
					}),
				},
			},
			want: want{
				o: managed.ExternalDelete{},
			},
		},
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
