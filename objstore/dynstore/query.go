package dynstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/kronael/arizuko/objstore"
)

type names struct {
	n map[string]string
	v map[string]types.AttributeValue
}

func newNames() *names {
	return &names{n: map[string]string{}, v: map[string]types.AttributeValue{}}
}

func (b *names) name(attr string) string {
	k := fmt.Sprintf("#n%d", len(b.n))
	b.n[k] = attr
	return k
}

func (b *names) value(val string) string {
	k := fmt.Sprintf(":v%d", len(b.v))
	b.v[k] = str(val)
	return k
}

func keyCond(b *names, q objstore.Query) string {
	cond := b.name(partAttr) + " = " + b.value(q.Partition)
	k := q.Key
	if k.Op == objstore.AnyKey {
		return cond
	}
	sk := b.name(sortAttr)
	switch k.Op {
	case objstore.ExactKey:
		return cond + " AND " + sk + " = " + b.value(k.Value)
	case objstore.LtKey:
		return cond + " AND " + sk + " < " + b.value(k.Value)
	case objstore.LeKey:
		return cond + " AND " + sk + " <= " + b.value(k.Value)
	case objstore.GtKey:
		return cond + " AND " + sk + " > " + b.value(k.Value)
	case objstore.GeKey:
		return cond + " AND " + sk + " >= " + b.value(k.Value)
	case objstore.PrefixKey:
		return cond + " AND begins_with(" + sk + ", " + b.value(k.Value) + ")"
	}
	return cond
}

func cmpExpr(b *names, c objstore.Cmp) (string, error) {
	n := b.name(c.Attr)
	switch c.Op {
	case objstore.Eq:
		return n + " = " + b.value(c.Value), nil
	case objstore.Ne:
		return n + " <> " + b.value(c.Value), nil
	case objstore.Lt:
		return n + " < " + b.value(c.Value), nil
	case objstore.Le:
		return n + " <= " + b.value(c.Value), nil
	case objstore.Gt:
		return n + " > " + b.value(c.Value), nil
	case objstore.Ge:
		return n + " >= " + b.value(c.Value), nil
	case objstore.Absent:
		return "attribute_not_exists(" + n + ")", nil
	case objstore.Present:
		return "attribute_exists(" + n + ")", nil
	}
	return "", fmt.Errorf("unknown comparison %d on %q", c.Op, c.Attr)
}

func filterExpr(b *names, where []objstore.Cmp) (string, error) {
	if len(where) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(where))
	for _, c := range where {
		e, err := cmpExpr(b, c)
		if err != nil {
			return "", err
		}
		parts = append(parts, e)
	}
	return strings.Join(parts, " AND "), nil
}

// inSubtree keeps a prefix read to the node itself and what sits under it, so a
// sibling whose name merely starts with the same bytes drops out.
func inSubtree(k objstore.KeyPredicate, sort string) bool {
	if k.Op != objstore.PrefixKey || k.Sep == "" {
		return true
	}
	return sort == k.Value || strings.HasPrefix(sort, k.Value+k.Sep)
}

func (s *Store) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if err := objstore.ValidateQuery(q); err != nil {
		return nil, err
	}
	b := newNames()
	in := &dynamodb.QueryInput{
		TableName:              s.table(q.Entity),
		KeyConditionExpression: aws.String(keyCond(b, q)),
		ScanIndexForward:       aws.Bool(!q.Descending),
		ConsistentRead:         aws.Bool(true),
	}
	filter, err := filterExpr(b, q.Where)
	if err != nil {
		return nil, err
	}
	if filter != "" {
		in.FilterExpression = aws.String(filter)
	}
	if len(q.Project) > 0 {
		proj := make([]string, 0, len(q.Project)+2)
		proj = append(proj, b.name(partAttr), b.name(sortAttr))
		for _, p := range q.Project {
			proj = append(proj, b.name(p))
		}
		in.ProjectionExpression = aws.String(strings.Join(proj, ", "))
	}
	if q.Limit > 0 {
		in.Limit = aws.Int32(int32(q.Limit))
	}
	in.ExpressionAttributeNames = b.n
	in.ExpressionAttributeValues = b.v

	var out []objstore.Item
	for {
		res, err := s.api.Query(ctx, in)
		if err != nil {
			return nil, serverFault(fmt.Sprintf("query %s", q.Entity), err)
		}
		for _, raw := range res.Items {
			it, err := readItem(raw)
			if err != nil {
				return nil, err
			}
			if !inSubtree(q.Key, it.Key.Sort) {
				continue
			}
			out = append(out, it)
			if q.Limit > 0 && len(out) >= q.Limit {
				return out, nil
			}
		}
		if len(res.LastEvaluatedKey) == 0 {
			return out, nil
		}
		in.ExclusiveStartKey = res.LastEvaluatedKey
	}
}
