// Package dynstore talks to a DynamoDB endpoint.
package dynstore

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/kronael/arizuko/objstore"
)

const (
	partAttr = "part"
	sortAttr = "sort"

	// openAttr is the name an open takes away. It is not an attribute of
	// anything: no writer sets it and no reader asks for it, which is what
	// makes removing it a write that changes nothing.
	openAttr = "__open"

	// DevKeyID and DevSecret are the documented example pair a local endpoint
	// seeds, so a test can sign without a credential store. A served endpoint
	// mints its own pair and the caller passes that one.
	DevKeyID  = "AKIAIOSFODNN7EXAMPLE"
	DevSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

type Store struct {
	api    *dynamodb.Client
	prefix string
}

func New(api *dynamodb.Client, prefix string) *Store {
	return &Store{api: api, prefix: prefix}
}

// Config names the endpoint a caller talks to, the credential it signs with and
// the table namespace it reads. Endpoint empty means Amazon's own service,
// which takes the same static key an IAM user holds anywhere else — so this
// driver reaches the service and a self-hosted server through one door.
type Config struct {
	Endpoint string
	Region   string
	// Prefix goes in front of every entity name, so one server holds several
	// owners and several test runs without a name collision.
	Prefix string
	KeyID  string
	Secret string
	// CA is the PEM the endpoint's certificate chains to. A server that
	// generates its own certificate at init needs it; the public service chains
	// to the host's trust store and leaves it empty. It is the bytes and not a
	// path because a daemon in a container has the value in its environment and
	// no file of its own to read.
	CA []byte
}

// Dial builds a client for cfg. A missing credential is an error rather than an
// anonymous client: an unsigned request fails later, at the first write, with a
// message about the request instead of the configuration.
func Dial(cfg Config) (*Store, error) {
	if cfg.KeyID == "" || cfg.Secret == "" {
		return nil, fmt.Errorf("dynstore: endpoint %q takes an access key and a secret", cfg.Endpoint)
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*dynamodb.Options){func(o *dynamodb.Options) {
		o.Credentials = credentials.NewStaticCredentialsProvider(cfg.KeyID, cfg.Secret, "")
	}}
	if cfg.Endpoint != "" {
		opts = append(opts, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint) })
	}
	client, err := httpClient(cfg.CA)
	if err != nil {
		return nil, err
	}
	opts = append(opts, func(o *dynamodb.Options) { o.HTTPClient = client })
	return New(dynamodb.NewFromConfig(aws.Config{Region: region}, opts...), cfg.Prefix), nil
}

// requestTimeout bounds one attempt at the store. The SDK's own client bounds
// the dial and the TLS handshake and nothing after them, so a server that
// accepts a connection and then goes silent holds its caller forever — and a
// daemon reaches the store mostly from contexts that never cancel. The SDK
// retries, so a transient timeout still completes the call. It is a var so a
// test can lower it and prove the bound holds.
var requestTimeout = 30 * time.Second

// httpClient bounds every attempt and, when the endpoint carries a certificate
// of its own, trusts it in ADDITION to the host's roots — so an instance with a
// self-signed store keeps verifying every other name it talks to.
func httpClient(ca []byte) (*awshttp.BuildableClient, error) {
	client := awshttp.NewBuildableClient().WithTimeout(requestTimeout)
	if len(ca) == 0 {
		return client, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("dynstore: the configured CA holds no certificate")
	}
	return client.WithTransportOptions(func(tr *http.Transport) {
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		tr.TLSClientConfig.RootCAs = pool
	}), nil
}

func (s *Store) table(entity string) *string {
	return aws.String(s.prefix + entity)
}

func str(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func itemOf(key objstore.Key, attrs map[string]string) map[string]types.AttributeValue {
	out := map[string]types.AttributeValue{
		partAttr: str(key.Partition),
		sortAttr: str(key.Sort),
	}
	for k, v := range attrs {
		out[k] = str(v)
	}
	return out
}

func keyOf(key objstore.Key) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		partAttr: str(key.Partition),
		sortAttr: str(key.Sort),
	}
}

func readItem(raw map[string]types.AttributeValue) (objstore.Item, error) {
	var it objstore.Item
	it.Attrs = map[string]string{}
	for k, v := range raw {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return it, fmt.Errorf("attribute %q is not a string", k)
		}
		switch k {
		case partAttr:
			it.Key.Partition = s.Value
		case sortAttr:
			it.Key.Sort = s.Value
		default:
			it.Attrs[k] = s.Value
		}
	}
	return it, nil
}
