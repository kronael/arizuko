package dynstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/kronael/arizuko/objstore"
)

// nonEmpty answers nil for an empty map, which the endpoint refuses.
func nonEmpty(v map[string]types.AttributeValue) map[string]types.AttributeValue {
	if len(v) == 0 {
		return nil
	}
	return v
}

func condExpr(b *names, c objstore.Cond) (string, error) {
	var parts []string
	switch c.Item {
	case objstore.ItemPresent:
		parts = append(parts, "attribute_exists("+b.name(partAttr)+")")
	case objstore.ItemAbsent:
		parts = append(parts, "attribute_not_exists("+b.name(partAttr)+")")
	}
	for _, cm := range c.Where {
		e, err := cmpExpr(b, cm)
		if err != nil {
			return "", err
		}
		parts = append(parts, e)
	}
	return strings.Join(parts, " AND "), nil
}

func setExpr(b *names, attrs map[string]string, remove []string) string {
	var sets, removes []string
	for k, v := range attrs {
		sets = append(sets, b.name(k)+" = "+b.value(v))
	}
	for _, k := range remove {
		removes = append(removes, b.name(k))
	}
	var out []string
	if len(sets) > 0 {
		out = append(out, "SET "+strings.Join(sets, ", "))
	}
	if len(removes) > 0 {
		out = append(out, "REMOVE "+strings.Join(removes, ", "))
	}
	return strings.Join(out, " ")
}

func (s *Store) writeAction(w objstore.Write) (types.TransactWriteItem, error) {
	b := newNames()
	cond, err := condExpr(b, w.Cond)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	switch w.Op {
	case objstore.Put:
		put := &types.Put{TableName: s.table(w.Entity), Item: itemOf(w.Key, w.Attrs)}
		if cond != "" {
			put.ConditionExpression = aws.String(cond)
			put.ExpressionAttributeNames = b.n
			put.ExpressionAttributeValues = nonEmpty(b.v)
		}
		return types.TransactWriteItem{Put: put}, nil

	case objstore.Delete:
		del := &types.Delete{TableName: s.table(w.Entity), Key: keyOf(w.Key)}
		if cond != "" {
			del.ConditionExpression = aws.String(cond)
			del.ExpressionAttributeNames = b.n
			del.ExpressionAttributeValues = nonEmpty(b.v)
		}
		return types.TransactWriteItem{Delete: del}, nil

	case objstore.Update, objstore.Merge:
		if w.Op == objstore.Update {
			exists := "attribute_exists(" + b.name(partAttr) + ")"
			if cond == "" {
				cond = exists
			} else {
				cond = exists + " AND " + cond
			}
		}
		expr := setExpr(b, w.Attrs, w.Remove)
		if expr == "" {
			// An OPEN: the item's existence is the whole write (a chat with no
			// real inbound, store/messages.go). There is no empty update here
			// and a key attribute may not be assigned, so the expression takes
			// away a name the model never writes: the item is created when it
			// is missing and no attribute of it changes when it is there.
			expr = "REMOVE " + b.name(openAttr)
		}
		upd := &types.Update{
			TableName:        s.table(w.Entity),
			Key:              keyOf(w.Key),
			UpdateExpression: aws.String(expr),
		}
		if cond != "" {
			upd.ConditionExpression = aws.String(cond)
		}
		upd.ExpressionAttributeNames = b.n
		upd.ExpressionAttributeValues = nonEmpty(b.v)
		return types.TransactWriteItem{Update: upd}, nil
	}
	return types.TransactWriteItem{}, fmt.Errorf("unknown write op %d", w.Op)
}

func (s *Store) checkAction(c objstore.Check) (types.TransactWriteItem, error) {
	b := newNames()
	cond, err := condExpr(b, c.Cond)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	if cond == "" {
		return types.TransactWriteItem{}, errors.New("a check with no condition checks nothing")
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName:                 s.table(c.Entity),
		Key:                       keyOf(c.Key),
		ConditionExpression:       aws.String(cond),
		ExpressionAttributeNames:  b.n,
		ExpressionAttributeValues: nonEmpty(b.v),
	}}, nil
}

func (s *Store) GuardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, error) {
	if err := objstore.ValidateBatch(checks, writes); err != nil {
		return false, err
	}
	for range 16 {
		committed, retry, err := s.guardedBatch(ctx, checks, writes)
		if !retry {
			return committed, err
		}
	}
	return false, fmt.Errorf("batch of %d writes: merge presence changed repeatedly", len(writes))
}

func (s *Store) guardedBatch(ctx context.Context, checks []objstore.Check,
	writes []objstore.Write) (bool, bool, error) {
	items := make([]types.TransactWriteItem, 0, len(checks)+len(writes))
	internal := make(map[int]bool)
	for _, c := range checks {
		a, err := s.checkAction(c)
		if err != nil {
			return false, false, err
		}
		items = append(items, a)
	}
	for _, w := range writes {
		if w.Op == objstore.Merge && len(w.Init) > 0 {
			found, err := s.Query(ctx, objstore.Query{Entity: w.Entity, Partition: w.Key.Partition,
				Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: w.Key.Sort}})
			if err != nil {
				return false, false, err
			}
			internal[len(items)] = true
			if len(found) > 0 {
				w.Op, w.Init = objstore.Update, nil
			} else {
				attrs := maps.Clone(w.Init)
				maps.Copy(attrs, w.Attrs)
				w.Op, w.Attrs, w.Init, w.Remove = objstore.Put, attrs, nil, nil
				w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
			}
		}
		a, err := s.writeAction(w)
		if err != nil {
			return false, false, err
		}
		items = append(items, a)
	}
	_, err := s.api.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err == nil {
		return true, false, nil
	}
	if len(internal) > 0 {
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) && len(canceled.CancellationReasons) == len(items) {
			retry, external := false, false
			for i, reason := range canceled.CancellationReasons {
				if reason.Code != nil && *reason.Code == "None" {
					continue
				}
				if reason.Code == nil || *reason.Code != "ConditionalCheckFailed" {
					return false, false, serverFault(fmt.Sprintf("batch of %d writes", len(writes)), err)
				}
				if internal[i] {
					retry = true
				} else {
					external = true
				}
			}
			if external {
				return false, false, nil
			}
			if retry {
				return false, true, nil
			}
		}
		return false, false, serverFault(fmt.Sprintf("batch of %d writes", len(writes)), err)
	}
	if refused(err) {
		return false, false, nil
	}
	// The operation's name rides the error because a batch that fails names
	// nothing on its own; a caller reading a log line needs to know which
	// write, and whether the server or the request is at fault (BUGS L293).
	return false, false, serverFault(fmt.Sprintf("batch of %d writes", len(writes)), err)
}

// refused reports whether the endpoint turned the batch down because a
// condition did not hold, which is an answer rather than a fault.
func refused(err error) bool {
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) {
		for _, r := range canceled.CancellationReasons {
			if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
				return true
			}
		}
		return false
	}
	var failed *types.ConditionalCheckFailedException
	return errors.As(err, &failed)
}
