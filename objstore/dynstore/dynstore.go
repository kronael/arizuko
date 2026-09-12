// Package dynstore talks to a DynamoDB endpoint.
package dynstore

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/kronael/arizuko/objstore"
)

const (
	partAttr = "part"
	sortAttr = "sort"

	// The documented example pair, which a local endpoint seeds so a client can
	// sign without a credential store. A real endpoint takes its own.
	devKeyID  = "AKIAIOSFODNN7EXAMPLE"
	devSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

type Store struct {
	api    *dynamodb.Client
	prefix string
}

func New(api *dynamodb.Client, prefix string) *Store {
	return &Store{api: api, prefix: prefix}
}

// Dial builds a client for an endpoint. Empty endpoint means the real service.
func Dial(ctx context.Context, endpoint, region, prefix string) (*Store, error) {
	opts := []func(*dynamodb.Options){}
	if endpoint != "" {
		opts = append(opts, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.Credentials = credentials.NewStaticCredentialsProvider(devKeyID, devSecret, "")
		})
	}
	if region == "" {
		region = "us-east-1"
	}
	cfg := aws.Config{Region: region}
	return New(dynamodb.NewFromConfig(cfg, opts...), prefix), nil
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
