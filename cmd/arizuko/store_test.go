package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
)

const closedPort = "http://127.0.0.1:1"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func uniq(prefix string) string { return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()) }

// newInstance makes a data directory whose operator.env holds a loopback line
// to a closed port, the line an earlier generate wrote.
func newInstance(t *testing.T) (instance, dir string) {
	t.Helper()
	cfg := dyntest.Config(t)
	for _, k := range []string{dynstore.EnvURL, dynstore.EnvRegion, dynstore.EnvKeyID, dynstore.EnvSecret, dynstore.EnvCA} {
		t.Setenv(k, os.Getenv(k))
	}
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")
	instance = uniq("enr")
	dir = filepath.Join(base, "arizuko_"+instance)
	writeFile(t, filepath.Join(dir, ".env"), dynstore.EnvURL+"="+cfg.Endpoint+"\n"+dynstore.EnvRegion+"=us-east-1\n")
	writeFile(t, filepath.Join(dir, "env", "operator.env"), dynstore.EnvURL+"="+closedPort+"\n")
	return instance, dir
}

func enrollRun(t *testing.T, instance string, extra ...string) (string, error) {
	t.Helper()
	args, err := instanceArgs(append([]string{"-i", instance, "enroll"}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = storeEnroll(args, &out)
	return out.String(), err
}

func testManager(t *testing.T) *manager {
	t.Helper()
	m, err := newManager(map[string]string{dynstore.EnvURL: dyntest.Config(t).Endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func pairOf(t *testing.T, dir, file string) map[string]string {
	t.Helper()
	pair, err := godotenv.Read(filepath.Join(dir, "env", file))
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func rowCount() int {
	n := 0
	for _, prefixes := range store.StoreReach {
		if len(prefixes) > 0 {
			n++
		}
	}
	return n
}

func TestEnrollMintsEveryRowAndConverges(t *testing.T) {
	instance, dir := newInstance(t)
	m := testManager(t)

	out, err := enrollRun(t, instance)
	if err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	if got := strings.Count(out, " minted"); got != rowCount() || strings.Contains(out, " kept") {
		t.Fatalf("first run minted %d of %d rows:\n%s", got, rowCount(), out)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "env", "operator.env")); strings.Contains(string(body), closedPort) {
		t.Errorf("operator.env still holds the closed port:\n%s", body)
	}
	for _, f := range []string{"timed.env", "ttsd.env"} {
		if _, err := os.Stat(filepath.Join(dir, "env", f)); err == nil {
			t.Errorf("%s was written for an empty row", f)
		}
	}
	acct, err := m.accounts()
	if err != nil {
		t.Fatal(err)
	}
	id := acct["arizuko_"+instance]
	for daemon, prefixes := range store.StoreReach {
		if len(prefixes) == 0 {
			continue
		}
		user, file := enrollee(daemon)
		if pair := pairOf(t, dir, file); pair[dynstore.EnvKeyID] == "" || pair[dynstore.EnvSecret] == "" {
			t.Errorf("%s holds no pair", file)
		}
		var docs []struct {
			Name string          `json:"policy_name"`
			Doc  json.RawMessage `json:"policy_document"`
		}
		if err := m.do("GET", "/accounts/"+id+"/users/"+user+"/policies", nil, &docs, 0); err != nil {
			t.Fatal(err)
		}
		var got, want any
		for _, d := range docs {
			if d.Name == "tables" {
				_ = json.Unmarshal(d.Doc, &got)
			}
		}
		_ = json.Unmarshal([]byte(policy(prefixes)), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s policy = %v, want %v", user, got, want)
		}
	}

	again, err := enrollRun(t, instance)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, again)
	}
	if got := strings.Count(again, " kept"); got != rowCount() || strings.Contains(again, " minted") {
		t.Fatalf("second run kept %d of %d rows:\n%s", got, rowCount(), again)
	}
	keys, err := m.keys(id, "routd")
	if err != nil || len(keys) != 1 {
		t.Errorf("routd holds %d keys after two runs (%v)", len(keys), err)
	}
}

func TestEnrollHalfPairStopsBeforeAnyWrite(t *testing.T) {
	instance, dir := newInstance(t)
	writeFile(t, filepath.Join(dir, "env", "routd.env"), dynstore.EnvKeyID+"=AKIAHALF\n")
	out, err := enrollRun(t, instance)
	if err == nil || !strings.Contains(err.Error(), "routd.env") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	assertNoAccount(t, instance)
}

func assertNoAccount(t *testing.T, instance string) {
	t.Helper()
	acct, err := testManager(t).accounts()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := acct["arizuko_"+instance]; ok {
		t.Errorf("account arizuko_%s exists after a run that had to stop first", instance)
	}
}

func TestEnrollKeyOfAnotherAccountStopsBeforeAnyWrite(t *testing.T) {
	first, firstDir := newInstance(t)
	if out, err := enrollRun(t, first); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	foreign := pairOf(t, firstDir, "routd.env")

	second, secondDir := newInstance(t)
	writeFile(t, filepath.Join(secondDir, "env", "routd.env"),
		dynstore.EnvKeyID+"="+foreign[dynstore.EnvKeyID]+"\n"+dynstore.EnvSecret+"="+foreign[dynstore.EnvSecret]+"\n")
	_, err := enrollRun(t, second)
	if err == nil || !strings.Contains(err.Error(), foreign[dynstore.EnvKeyID]) ||
		!strings.Contains(err.Error(), "arizuko_"+first) {
		t.Fatalf("err = %v, want the key id and the owner account", err)
	}
	assertNoAccount(t, second)
}

func TestEnrollKeyOfAnotherUserStops(t *testing.T) {
	instance, dir := newInstance(t)
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	authd := pairOf(t, dir, "authd.env")
	writeFile(t, filepath.Join(dir, "env", "routd.env"),
		dynstore.EnvKeyID+"="+authd[dynstore.EnvKeyID]+"\n"+dynstore.EnvSecret+"="+authd[dynstore.EnvSecret]+"\n")
	_, err := enrollRun(t, instance)
	if err == nil || !strings.Contains(err.Error(), authd[dynstore.EnvKeyID]) || !strings.Contains(err.Error(), "user authd") {
		t.Fatalf("err = %v, want the key id and its user", err)
	}
}

func TestEnrollUnknownKeyIsReplaced(t *testing.T) {
	instance, dir := newInstance(t)
	writeFile(t, filepath.Join(dir, "env", "routd.env"),
		dynstore.EnvKeyID+"=AKIANOONEHOLDSTHIS\n"+dynstore.EnvSecret+"=x\nKEEP=1\n")
	out, err := enrollRun(t, instance)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "replaced AKIANOONEHOLDSTHIS") {
		t.Errorf("output does not name the replaced key:\n%s", out)
	}
	pair := pairOf(t, dir, "routd.env")
	if pair[dynstore.EnvKeyID] == "AKIANOONEHOLDSTHIS" || pair["KEEP"] != "1" {
		t.Errorf("routd.env = %v", pair)
	}
}

func TestEnrollWrongSecretEndsWithoutKept(t *testing.T) {
	instance, dir := newInstance(t)
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pair := pairOf(t, dir, "authd.env")
	writeFile(t, filepath.Join(dir, "env", "authd.env"),
		dynstore.EnvKeyID+"="+pair[dynstore.EnvKeyID]+"\n"+dynstore.EnvSecret+"=wrongsecretwrongsecretwrongsecretwrong0\n")
	out, err := enrollRun(t, instance)
	if err == nil || !strings.Contains(err.Error(), "authd:") || strings.Contains(out, "kept") {
		t.Fatalf("err = %v, output %q", err, out)
	}
}

func TestPolicyDocument(t *testing.T) {
	want := `{"Statement":[{"Action":"dynamodb:*","Effect":"Allow","Resource":["arn:aws:dynamodb:*:*:table/onbod.*","arn:aws:dynamodb:*:*:table/routd.*"]}],"Version":"2012-10-17"}`
	if got := policy([]string{"onbod", "routd"}); got != want {
		t.Errorf("policy = %s, want %s", got, want)
	}
}

// account makes a source account holding one operator user and returns its client.
func account(t *testing.T, name string) *dynamodb.Client {
	t.Helper()
	m := testManager(t)
	id, err := m.account(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.user(id, "operator"); err != nil {
		t.Fatal(err)
	}
	keyID, secret, err := m.mint(id, "operator")
	if err != nil {
		t.Fatal(err)
	}
	c, err := dynstore.Client(dynstore.Config{Endpoint: dyntest.Config(t).Endpoint, KeyID: keyID, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// snapshot writes the .env and env/ of an old instance whose operator pair
// belongs to source.
func snapshot(t *testing.T, source string) string {
	t.Helper()
	m := testManager(t)
	id, err := m.account(source)
	if err != nil {
		t.Fatal(err)
	}
	keyID, secret, err := m.mint(id, "operator")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), dynstore.EnvURL+"="+dyntest.Config(t).Endpoint+"\n")
	writeFile(t, filepath.Join(dir, "env", "operator.env"),
		dynstore.EnvKeyID+"="+keyID+"\n"+dynstore.EnvSecret+"="+secret+"\n")
	return dir
}

var s = func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func makeTable(t *testing.T, c *dynamodb.Client, name string, in ...func(*dynamodb.CreateTableInput)) {
	t.Helper()
	input := &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
	}
	for _, f := range in {
		f(input)
	}
	if _, err := c.CreateTable(context.Background(), input); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func put(t *testing.T, c *dynamodb.Client, name string, item map[string]types.AttributeValue) {
	t.Helper()
	if _, err := c.PutItem(context.Background(), &dynamodb.PutItemInput{TableName: aws.String(name), Item: item}); err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

func scan(t *testing.T, c *dynamodb.Client, name string) []map[string]types.AttributeValue {
	t.Helper()
	var items []map[string]types.AttributeValue
	pages := dynamodb.NewScanPaginator(c, &dynamodb.ScanInput{TableName: aws.String(name), ConsistentRead: aws.Bool(true)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(context.Background())
		if err != nil {
			t.Fatalf("scan %s: %v", name, err)
		}
		items = append(items, page.Items...)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["pk"].(*types.AttributeValueMemberS).Value < items[j]["pk"].(*types.AttributeValueMemberS).Value
	})
	return items
}

func dstClient(t *testing.T, dir string) *dynamodb.Client {
	t.Helper()
	pair := pairOf(t, dir, "operator.env")
	c, err := dynstore.Client(dynstore.Config{Endpoint: dyntest.Config(t).Endpoint, KeyID: pair[dynstore.EnvKeyID], Secret: pair[dynstore.EnvSecret]})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEnrollCopiesEveryTableAndCounts(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	every := uniq("teled.every")
	makeTable(t, src, every)
	put(t, src, every, map[string]types.AttributeValue{
		"pk": s("a"), "sk": s("1"),
		"n":    &types.AttributeValueMemberN{Value: "42.5"},
		"b":    &types.AttributeValueMemberB{Value: []byte{1, 2, 3}},
		"t":    &types.AttributeValueMemberBOOL{Value: true},
		"z":    &types.AttributeValueMemberNULL{Value: true},
		"ss":   &types.AttributeValueMemberSS{Value: []string{"x", "y"}},
		"ns":   &types.AttributeValueMemberNS{Value: []string{"1", "2"}},
		"bs":   &types.AttributeValueMemberBS{Value: [][]byte{{1}, {2}}},
		"l":    &types.AttributeValueMemberL{Value: []types.AttributeValue{s("x"), &types.AttributeValueMemberN{Value: "7"}}},
		"m":    &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"k": s("v")}},
		"utf8": s("héllo"),
	})
	put(t, src, every, map[string]types.AttributeValue{"pk": s("b"), "sk": s("1")})
	big := uniq("routd.big")
	makeTable(t, src, big)
	const items = 5
	for i := range items {
		put(t, src, big, map[string]types.AttributeValue{"pk": s(fmt.Sprint(i)), "sk": s("1"), "blob": s(strings.Repeat("x", 300_000))})
	}

	instance, dir := newInstance(t)
	out, err := enrollRun(t, instance, snapshot(t, source))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{every + " 2 equal", big + fmt.Sprintf(" %d equal", items)} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	dst := dstClient(t, dir)
	for _, name := range []string{every, big} {
		if got, want := scan(t, dst, name), scan(t, src, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s differs from the source", name)
		}
	}
}

func TestEnrollCopyKeepsADestinationItem(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	name := uniq("teled.keep")
	makeTable(t, src, name)
	put(t, src, name, map[string]types.AttributeValue{"pk": s("a"), "sk": s("1"), "v": s("old")})
	snap := snapshot(t, source)

	instance, dir := newInstance(t)
	if out, err := enrollRun(t, instance, snap); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dst := dstClient(t, dir)
	put(t, dst, name, map[string]types.AttributeValue{"pk": s("a"), "sk": s("1"), "v": s("new")})
	out, err := enrollRun(t, instance, snap)
	if err != nil || !strings.Contains(out, name+" 1 equal") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if got := scan(t, dst, name)[0]["v"].(*types.AttributeValueMemberS).Value; got != "new" {
		t.Errorf("the copy replaced a destination item: v = %q", got)
	}
}

func TestEnrollCopyFailsOnAnExtraDestinationItem(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	name := uniq("teled.extra")
	makeTable(t, src, name)
	put(t, src, name, map[string]types.AttributeValue{"pk": s("a"), "sk": s("1")})
	snap := snapshot(t, source)

	instance, dir := newInstance(t)
	if out, err := enrollRun(t, instance, snap); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	put(t, dstClient(t, dir), name, map[string]types.AttributeValue{"pk": s("z"), "sk": s("1")})
	out, err := enrollRun(t, instance, snap)
	if err == nil || !strings.Contains(err.Error(), "destination 2") || strings.Contains(out, name+" ") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestEnrollCopyStopsBeforeAnyWrite(t *testing.T) {
	cases := map[string]func(*testing.T, *dynamodb.Client) string{
		"a table whose prefix has no row": func(t *testing.T, c *dynamodb.Client) string {
			name := uniq("nosuchowner.t")
			makeTable(t, c, name)
			return name
		},
		"a table with an index": func(t *testing.T, c *dynamodb.Client) string {
			name := uniq("teled.indexed")
			makeTable(t, c, name, func(in *dynamodb.CreateTableInput) {
				in.AttributeDefinitions = append(in.AttributeDefinitions, types.AttributeDefinition{AttributeName: aws.String("gsi"), AttributeType: types.ScalarAttributeTypeS})
				in.GlobalSecondaryIndexes = []types.GlobalSecondaryIndex{{
					IndexName:  aws.String("gsi"),
					KeySchema:  []types.KeySchemaElement{{AttributeName: aws.String("gsi"), KeyType: types.KeyTypeHash}},
					Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
				}}
			})
			return name
		},
	}
	for what, seed := range cases {
		t.Run(what, func(t *testing.T) {
			source := uniq("src")
			name := seed(t, account(t, source))
			instance, _ := newInstance(t)
			_, err := enrollRun(t, instance, snapshot(t, source))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want it to name %s", err, name)
			}
			assertNoAccount(t, instance)
		})
	}
}

func TestEnrollCopyFailsOnAnotherKeySchema(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	name := uniq("teled.shape")
	makeTable(t, src, name)
	snap := snapshot(t, source)

	instance, dir := newInstance(t)
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	makeTable(t, dstClient(t, dir), name, func(in *dynamodb.CreateTableInput) {
		in.AttributeDefinitions = in.AttributeDefinitions[:1]
		in.KeySchema = in.KeySchema[:1]
	})
	_, err := enrollRun(t, instance, snap)
	if err == nil || !strings.Contains(err.Error(), "key schema") {
		t.Fatalf("err = %v", err)
	}
}
