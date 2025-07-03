package util

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"

	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/go-open-service-broker-client/v2/fake"
	"github.com/pkg/errors"
)

const (
	MetadataPrefix  = "osb.provider.crossplane.io"
	AsyncAnnotation = MetadataPrefix + "/async"
)

// func AddFinalizer(ctx context.Context, mg resource.Managed) error {
// 	obj, ok := mg.(*v1alpha1.ServiceBinding)
// 	if !ok {
// 		return errors.New(errNotKubernetesServiceBinding)
// 	}

// 	if meta.FinalizerExists(obj, objFinalizerName) {
// 		return nil
// 	}
// 	meta.AddFinalizer(obj, objFinalizerName)

// 	err := c.kube.Update(ctx, obj)
// 	if err != nil {
// 		return errors.Wrap(err, errAddFinalizer)
// 	}

// 	// Add finalizer to referenced resources if not exists
// 	err = c.handleRefFinalizer(ctx, obj, func(
// 		ctx context.Context, res *unstructured.Unstructured, finalizer string,
// 	) error {
// 		if !meta.FinalizerExists(res, finalizer) {
// 			meta.AddFinalizer(res, finalizer)
// 		}
// 		return nil
// 	}, false)
// 	return errors.Wrap(err, errAddFinalizer)
// }

func EndpointEqual(e1, e2 osb.Endpoint) bool {
	// Use sorted copies of the endpoint's ports slices
	// to ignore elements order
	sortedPorts1 := slices.Clone(e1.Ports)
	sortedPorts2 := slices.Clone(e2.Ports)

	slices.Sort(sortedPorts1)
	slices.Sort(sortedPorts2)

	return e1.Host == e2.Host && e1.Protocol == e2.Protocol && slices.Equal(sortedPorts1, sortedPorts2)
}

func VolumeMountEqual(e1, e2 osb.VolumeMount) bool {
	return reflect.DeepEqual(e1, e2)
}

func decodeB64StringToBasicAuthConfig(s string) (osb.BasicAuthConfig, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return osb.BasicAuthConfig{}, err
	}
	jsonDecode := struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}{}
	err = json.Unmarshal(data, &jsonDecode)
	if err != nil {
		return osb.BasicAuthConfig{}, err
	}
	return osb.BasicAuthConfig{
		Username: jsonDecode.User,
		Password: jsonDecode.Password,
	}, err
}

// TODO: take into account controller.options:
// - to enable OSB client alpha features
// - to override timeout
// - to override OSB spec version (?)
// -> Use ProviderConfig type parameter instead of brokerUrl and creds
func NewOsbClient(brokerUrl string, creds []byte) (osb.Client, error) {
	config := osb.DefaultClientConfiguration()
	config.URL = brokerUrl

	if len(creds) > 0 {
		credsString := string(creds)
		basicAuth, err := decodeB64StringToBasicAuthConfig(credsString)
		if err != nil {
			return nil, errors.Wrap(err, "error : can't decode string into basic auth struct")
		}
		authConfig := osb.AuthConfig{
			BasicAuthConfig: &basicAuth,
		}
		config.AuthConfig = &authConfig
	}

	return osb.NewClient(config)
}

// TODO: actually implement an no op client, since the osb.fake client
// returns an error in every function (except if redefined)
type NoOpOsbClient fake.FakeClient

// func (c *NoOpOsbClient) GetCatalog() (*osb.CatalogResponse, error) {
// 	return nil, nil
// }
