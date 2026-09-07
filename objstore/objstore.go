// Package objstore is the object store arizuko addresses instead of per-daemon
// SQL: a daemon names an entity, a coordinate and some attributes, and a driver
// answers. A second engine is a second driver and no daemon changes (spec
// 6/37).
//
// The surface is TWO primitives and nothing else (spec 6/40 §"The seam is TWO
// primitives"). Query reads; GuardedBatch writes under conditions. Everything
// arizuko needs beyond them — the versioned replace, the reachability guard,
// the claim-and-return, the counter — is a PROTOCOL written once over the two
// and shared by every driver, never a third method some engine cannot carry.
//
// The interface is deliberately narrower than SQL, because the narrow shape is
// the one every engine has. What a driver must never do is offer more: a
// Postgres driver that quietly gave set-wide atomicity over an unbounded
// predicate would let a caller depend on something a key-value driver cannot
// honour, and the caller would break on the engine nobody tested. objstore's
// conformance suite is what catches that — every driver runs the identical test
// bodies (objstore/conformance).
//
// "Narrower than SQL" is not the same as "narrower than the weakest engine".
// Every shape on this interface is one DynamoDB documents, and the AWS page is
// cited beside it. A shape the weakest engine HAS and this interface lacks is
// not portability, it is a hole a caller has to work around in nine daemons —
// which is what the first cut of the sort-key predicate was, offering 2 of
// DynamoDB's 7 operators.
//
// An attribute value is a string. That is the one modelling rule this package
// imposes, and it is the same discipline core.TS already imposes on time: a
// value that must ORDER is written fixed-width by its writer, so string order
// is the order the caller means, on every engine, with no cast in any dialect.
// A typed attribute model buys ordered numbers and costs a comparison grammar
// per driver — the vendor surface 6/37 §"No surrogate database" refuses. The
// rule is not left to discipline: a writer DECLARES an ordered attribute's
// width with OrderedAttr and every driver refuses a value of another width
// (ordered.go).
package objstore

import "context"

// Store is the whole storage surface. A driver implements these two methods and
// nothing else.
type Store interface {
	// Query returns one PAGE of the items q selects, in sort-key order.
	//
	// A page, not the selection: a read whose selection exceeds MaxPageBytes
	// comes back partial with a cursor, because that is what a key-value engine
	// does and a driver that quietly returned everything would let a caller
	// depend on completeness it loses on the other engine. QueryAll walks the
	// pages when a caller genuinely wants the whole selection.
	Query(ctx context.Context, q Query) (Page, error)

	// GuardedBatch applies every write, or none of them, and only if every
	// check holds and every write's own condition holds.
	//
	// An Outcome with Committed=false and err=nil is a REFUSED batch — a
	// condition did not hold. That is a normal outcome, not a fault: "someone
	// else claimed it" is what a claim reports when it loses, and authd's
	// ClaimRefreshToken returns exactly that today (won=false). A driver that
	// reported it as an error would make every caller unwrap a sentinel to find
	// out whether its own logic worked, and the ones that forgot would log a
	// fault per contended write.
	//
	// The refusal is ATTRIBUTED: Outcome.Refused names the action that did not
	// hold and carries the item as the engine held it. A bare false cannot tell
	// "someone else won the race" from "the row was already in that state", and
	// callers that only had the row count read it as a semantic verdict.
	GuardedBatch(ctx context.Context, checks []Check, writes []Write) (Outcome, error)
}

// Key is an item's coordinate: the partition it lives in, and its position
// inside that partition.
//
// Partition is the coordinate a caller FILTERS on, not the identifier it looks
// up by (6/37 §"How a daemon's tables become documents", rule 1) — for arizuko
// that is almost always the tenant root, because a folder subtree has to sit
// inside ONE partition for a prefix to cover it (6/40 §1). Choose the folder
// itself and every subtree read becomes a scan.
//
// Sort carries the rest: the path below the tenant, or a fixed-width timestamp
// where the caller reads in time order. Both a prefix predicate and an ordered
// read run on it, so it is the only part of the coordinate that admits a range.
type Key struct {
	Partition string
	Sort      string
}

// Item is one object as a driver returns it. Attrs holds only what the query
// projected; a query with no projection returns every attribute the item has.
//
// An attribute the item does not carry is ABSENT from the map, and absent is
// the only way an attribute is missing — there is no stored null. That is what
// makes Absent and Present exact on every engine, and it is the shape a
// tombstone already has: `used_at IS NULL` in SQL is "the used_at attribute is
// not there".
type Item struct {
	Key   Key
	Attrs map[string]string
}

// MaxPageBytes is how much stored item data one Query may examine.
//
// It is DynamoDB's page size: "A single Query operation can retrieve a maximum
// of 1 MB of data. This limit applies before any FilterExpression or
// ProjectionExpression is applied to the results."
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html
//
// The bound is declared here, with the interface, so a SQL driver cannot hold
// its own opinion of it. A driver that returned the whole selection would be
// offering completeness DynamoDB never gives, and the divergence is invisible
// at any fixture small enough for a unit test — which is the failure this
// constant exists to make impossible.
const MaxPageBytes = 1 << 20

// Page is one Query's answer: the items it returned, and where to resume.
//
// More is DynamoDB's LastEvaluatedKey, and it means exactly what that means:
// "A non-empty LastEvaluatedKey only means that the previous Query stopped at a
// page boundary — the 1 MB page-size limit or a Limit value — not that more
// matching items necessarily remain."
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.Pagination.html
//
// So an empty More is the ONLY end-of-selection signal, and a page with zero
// Items and a non-empty More is normal: the cap applies to the items the read
// EXAMINED, before Where narrowed them. Feed More back as Query.After.
type Page struct {
	Items []Item
	More  string

	// Examined is how many items the read looked at before Where narrowed them.
	// It is DynamoDB's ScannedCount, and it is what makes the cap observable: a
	// caller (or a conformance body) can see that a page ended on the bound
	// rather than on the selection running out.
	Examined int
}

// RefusalKind says which half of a batch refused it.
type RefusalKind uint8

const (
	// CheckRefused is a Check on an item the batch does not modify.
	CheckRefused RefusalKind = iota
	// WriteRefused is a Write whose own Cond did not hold.
	WriteRefused
)

// Refusal names the one action whose condition did not hold, and shows the item
// as the engine held it when the condition was evaluated.
//
// It is DynamoDB's answer to the same question: TransactWriteItems reports
// CancellationReasons "in the order of requested items" with the code
// `ConditionalCheckFailed`, and each action carries
// ReturnValuesOnConditionCheckFailure — "a field indicating whether to retrieve
// the item's attributes if the condition is not met".
// https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html
//
// Item is the pre-image and it is what closes the hole. A claim that lost a
// race and a claim on a row that was already spent both return Committed=false;
// only the pre-image tells them apart — `used_at` written by somebody else this
// instant, versus `used_at` that was there before the caller looked. Item is
// nil when the item was not there at all, which is a third answer again.
type Refusal struct {
	Kind   RefusalKind
	Index  int
	Entity string
	Key    Key
	Item   *Item
}

// Outcome is what a GuardedBatch did.
//
// Committed is the answer callers had before; Refused is the attribution they
// did not. Exactly one of them is set: a committed batch refused nothing, and a
// refused batch names what refused it.
type Outcome struct {
	Committed bool
	Refused   *Refusal
}

// Committed is the outcome of a batch that applied every write.
var Committed = Outcome{Committed: true}

// Refused is the outcome of a batch a condition turned away.
func Refused(kind RefusalKind, index int, entity string, key Key, item *Item) Outcome {
	return Outcome{Refused: &Refusal{
		Kind: kind, Index: index, Entity: entity, Key: key, Item: item,
	}}
}
