package dynstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// CreateEntity makes the table an entity is served from, or leaves it alone.
func (s *Store) CreateEntity(ctx context.Context, entity string) error {
	_, err := s.api.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   s.table(entity),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(partAttr), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(sortAttr), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(partAttr), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String(sortAttr), KeyType: types.KeyTypeRange},
		},
	})
	var exists *types.ResourceInUseException
	if err != nil && !errors.As(err, &exists) {
		return err
	}
	// Creation is asynchronous, so the table is not servable the moment the call
	// returns. A write before it is active fails as a missing resource.
	w := dynamodb.NewTableExistsWaiter(s.api, func(o *dynamodb.TableExistsWaiterOptions) {
		o.MinDelay = 10 * time.Millisecond
		o.MaxDelay = 200 * time.Millisecond
	})
	return w.Wait(ctx, &dynamodb.DescribeTableInput{TableName: s.table(entity)}, 30*time.Second)
}

// DropEntity removes a table. Tests use it; nothing in production does.
func (s *Store) DropEntity(ctx context.Context, entity string) error {
	_, err := s.api.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: s.table(entity)})
	var missing *types.ResourceNotFoundException
	if err != nil && !errors.As(err, &missing) {
		return err
	}
	return nil
}

// Entities answers every entity this store's prefix holds. A caller that did
// not create them needs it: the copy verifies a table it was told about, and a
// test fixture drops what its prefix left behind on a server that outlives it.
func (s *Store) Entities(ctx context.Context) ([]string, error) {
	var out []string
	var start *string
	for {
		page, err := s.api.ListTables(ctx, &dynamodb.ListTablesInput{ExclusiveStartTableName: start})
		if err != nil {
			return nil, err
		}
		for _, name := range page.TableNames {
			if strings.HasPrefix(name, s.prefix) {
				out = append(out, strings.TrimPrefix(name, s.prefix))
			}
		}
		if page.LastEvaluatedTableName == nil {
			return out, nil
		}
		start = page.LastEvaluatedTableName
	}
}
