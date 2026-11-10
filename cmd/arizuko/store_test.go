package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/googd/googstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

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

// newInstance makes a data directory for enrollment.
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
	writeFile(t, filepath.Join(dir, ".env"), storeConfigEnv(cfg))
	return instance, dir
}

func storeConfigEnv(cfg dynstore.Config) string {
	return dynstore.EnvURL + "=" + cfg.Endpoint + "\n" +
		dynstore.EnvRegion + "=" + os.Getenv(dynstore.EnvRegion) + "\n" +
		dynstore.EnvCA + "=" + base64.StdEncoding.EncodeToString(cfg.CA) + "\n"
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
	cfg := dyntest.Config(t)
	m, err := newManager(map[string]string{dynstore.EnvURL: cfg.Endpoint, dynstore.EnvCA: base64.StdEncoding.EncodeToString(cfg.CA)})
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
	if err := m.putPolicy(id, "operator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:*","Resource":"*"}]}`); err != nil {
		t.Fatal(err)
	}
	keyID, secret, err := m.mint(id, "operator")
	if err != nil {
		t.Fatal(err)
	}
	cfg := dyntest.Config(t)
	cfg.KeyID, cfg.Secret = keyID, secret
	c, err := dynstore.Client(cfg)
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
	if err := m.putPolicy(id, "operator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["dynamodb:ListTables","dynamodb:DescribeTable","dynamodb:Scan"],"Resource":"arn:aws:dynamodb:*:*:table/*"}]}`); err != nil {
		t.Fatal(err)
	}
	keyID, secret, err := m.mint(id, "operator")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), storeConfigEnv(dyntest.Config(t)))
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

func dstClient(t *testing.T, dir, daemon string) *dynamodb.Client {
	t.Helper()
	pair := pairOf(t, dir, daemon+".env")
	cfg := dyntest.Config(t)
	cfg.KeyID, cfg.Secret = pair[dynstore.EnvKeyID], pair[dynstore.EnvSecret]
	c, err := dynstore.Client(cfg)
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
	for _, name := range []string{every, big} {
		owner, _, _ := strings.Cut(name, ".")
		dst := dstClient(t, dir, owner)
		if got, want := scan(t, dst, name), scan(t, src, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s differs from the source", name)
		}
	}
}

func TestEnrollCopyScanLimits(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	name := uniq("teled.scanlimit")
	makeTable(t, src, name)
	for i := range 501 {
		put(t, src, name, map[string]types.AttributeValue{"pk": s(fmt.Sprint(i)), "sk": s("1")})
	}
	snap := snapshot(t, source)
	instance, dir := newInstance(t)
	target, err := url.Parse(dyntest.Config(t).Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport, err := dynstore.HTTPClient(dyntest.Config(t).CA)
	if err != nil {
		t.Fatal(err)
	}
	proxy.Transport = transport.GetTransport()
	var mu sync.Mutex
	var scans [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") == "DynamoDB_20120810.Scan" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			mu.Lock()
			scans = append(scans, body)
			mu.Unlock()
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	for _, path := range []string{filepath.Join(snap, ".env"), filepath.Join(dir, ".env")} {
		writeFile(t, path, dynstore.EnvURL+"="+server.URL+"\n"+dynstore.EnvRegion+"=us-east-1\n")
	}
	if out, err := enrollRun(t, instance, snap); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	counts := map[string]int{}
	for _, body := range scans {
		var request struct {
			Limit  *int   `json:"Limit"`
			Select string `json:"Select"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.Limit == nil || *request.Limit <= 0 || *request.Limit > 500 {
			t.Errorf("Scan has invalid Limit: %s", body)
		}
		counts[request.Select]++
	}
	if counts[""] < 2 || counts["COUNT"] < 2 {
		t.Errorf("Scan pages by Select = %v, want at least two of each", counts)
	}
}

func TestEnrollCopyInventoriesEveryPage(t *testing.T) {
	source := uniq("src")
	src := account(t, source)
	for i := range 101 {
		makeTable(t, src, fmt.Sprintf("teled.page%03d", i))
	}
	instance, _ := newInstance(t)
	out, err := enrollRun(t, instance, snapshot(t, source))
	if err != nil {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	if strings.Count(out, " 0 equal\n") != 101 {
		t.Fatalf("copy omitted tables from the paged inventory:\n%s", out)
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
	dst := dstClient(t, dir, "teled")
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
	put(t, dstClient(t, dir, "teled"), name, map[string]types.AttributeValue{"pk": s("z"), "sk": s("1")})
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
		"a table with a stream": func(t *testing.T, c *dynamodb.Client) string {
			name := uniq("teled.stream")
			makeTable(t, c, name, func(in *dynamodb.CreateTableInput) {
				in.StreamSpecification = &types.StreamSpecification{
					StreamEnabled:  aws.Bool(true),
					StreamViewType: types.StreamViewTypeNewAndOldImages,
				}
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
	makeTable(t, dstClient(t, dir, "teled"), name, func(in *dynamodb.CreateTableInput) {
		in.AttributeDefinitions = in.AttributeDefinitions[:1]
		in.KeySchema = in.KeySchema[:1]
	})
	_, err := enrollRun(t, instance, snap)
	if err == nil || !strings.Contains(err.Error(), "key schema") {
		t.Fatalf("err = %v", err)
	}
}

func TestViewRejectsStoreOverride(t *testing.T) {
	for _, key := range []string{dynstore.EnvURL, dynstore.EnvCA} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".env"), key+"=same\n")
			path := filepath.Join(dir, "env", "operator.env")
			writeFile(t, path, key+"=same\n"+dynstore.EnvKeyID+"=operator\n")
			got, err := view(dir)
			if err != nil || got[dynstore.EnvKeyID] != "operator" {
				t.Fatalf("matching values: %v %v", got, err)
			}
			writeFile(t, path, key+"=different\n")
			if _, err := view(dir); err == nil {
				t.Fatal("accepted override")
			}
		})
	}
}

func TestInstanceDirRejectsStoreOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	writeFile(t, filepath.Join(dir, ".env"), dynstore.EnvURL+"=https://store\n")
	writeFile(t, filepath.Join(dir, "env", "operator.env"), dynstore.EnvURL+"=https://other\n")
	if _, err := instanceDir(""); err == nil {
		t.Fatal("accepted override")
	}
}

func TestGenerateRefusesBeforeRelink(t *testing.T) {
	if dir := os.Getenv("ARIZUKO_REFUSAL_CHILD"); dir != "" {
		generateCompose(dir, "test")
		return
	}
	dir := seedSyncInstance(t, "refusal", map[string]string{"sample.yml": "services: {}\n"}, map[string]string{"sample.yml": "services: {}\n"})
	writeFile(t, filepath.Join(dir, ".env"), "AWS_REGION=us-east-1\nHOST_APP_DIR="+os.Getenv("HOST_APP_DIR")+"\n")
	path := filepath.Join(dir, "services", "sample.yml")
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGenerateRefusesBeforeRelink$")
	cmd.Env = append(os.Environ(), "ARIZUKO_REFUSAL_CHILD="+dir)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("accepted invalid store: %s", output)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || after.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fragment changed before refusal")
	}
	if _, err := os.Stat(filepath.Join(dir, "web")); !os.IsNotExist(err) {
		t.Fatal("web output created")
	}
}

func TestEnrolledPoliciesEnforceStoreReach(t *testing.T) {
	if os.Getenv(dyntest.EnvEndpoint) == "" {
		t.Skip("set ARIZUKO_TEST_DYNAMO_ENDPOINT to an isolated policy-enforcing ExtendDB server")
	}
	instance, dir := newInstance(t)
	m := testManager(t)
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	accounts, err := m.accounts()
	if err != nil {
		t.Fatal(err)
	}
	accountID := accounts["arizuko_"+instance]
	if accountID == "" {
		t.Fatal("enrolled account is missing")
	}
	t.Cleanup(func() {
		if err := m.deleteAccount("arizuko_" + instance); err != nil {
			t.Errorf("delete account: %v", err)
		}
	})
	const fullPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:*","Resource":"*"}]}`
	const fixtureUser = "policy_fixture"
	if err := m.user(accountID, fixtureUser); err != nil {
		t.Fatal(err)
	}
	if err := m.putPolicy(accountID, fixtureUser, fullPolicy); err != nil {
		t.Fatal(err)
	}
	id, secret, err := m.mint(accountID, fixtureUser)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dyntest.Config(t)
	cfg.KeyID, cfg.Secret = id, secret
	admin, err := dynstore.Client(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entity := uniq("policy")
	prefixes := map[string]bool{"unrelated": true}
	for _, owners := range store.StoreReach {
		for _, owner := range owners {
			prefixes[owner] = true
		}
	}
	for owner := range prefixes {
		table := dynstore.Prefix(owner) + entity
		ownerCfg := cfg
		if owner != "unrelated" {
			for daemon, owners := range store.StoreReach {
				if slices.Contains(owners, owner) {
					_, file := enrollee(daemon)
					pair := pairOf(t, dir, file)
					ownerCfg.KeyID, ownerCfg.Secret = pair[dynstore.EnvKeyID], pair[dynstore.EnvSecret]
					break
				}
			}
		}
		ownerCfg.Prefix = dynstore.Prefix(owner)
		ownerStore, err := dynstore.Dial(ownerCfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := ownerStore.CreateEntity(context.Background(), entity); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		t.Cleanup(func() {
			if _, err := admin.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
				t.Errorf("delete %s: %v", table, err)
			}
		})
	}
	check := func(t *testing.T, err error, allowed bool) {
		t.Helper()
		if allowed {
			if err != nil {
				t.Fatalf("allowed operation failed: %v", err)
			}
			return
		}
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDeniedException" {
			t.Fatalf("expected AccessDeniedException, got %v", err)
		}
	}
	checkReach := func(t *testing.T) {
		for daemon, owners := range store.StoreReach {
			if len(owners) == 0 {
				continue
			}
			_, file := enrollee(daemon)
			pair := pairOf(t, dir, file)
			userCfg := dyntest.Config(t)
			userCfg.KeyID, userCfg.Secret = pair[dynstore.EnvKeyID], pair[dynstore.EnvSecret]
			client, err := dynstore.Client(userCfg)
			if err != nil {
				t.Fatal(err)
			}
			for owner := range prefixes {
				t.Run(daemon+"/"+owner, func(t *testing.T) {
					table := dynstore.Prefix(owner) + entity
					key := map[string]types.AttributeValue{"part": s(daemon), "sort": s("one")}
					_, err := client.PutItem(context.Background(), &dynamodb.PutItemInput{TableName: aws.String(table), Item: key})
					check(t, err, slices.Contains(owners, owner))
					got, err := client.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: key, ConsistentRead: aws.Bool(true)})
					check(t, err, slices.Contains(owners, owner))
					if slices.Contains(owners, owner) && !reflect.DeepEqual(got.Item, key) {
						t.Fatalf("read = %v, want %v", got.Item, key)
					}
				})
			}
		}
	}
	t.Run("minted", checkReach)
	before := map[string]string{}
	for daemon, owners := range store.StoreReach {
		if len(owners) > 0 {
			user, file := enrollee(daemon)
			before[daemon] = pairOf(t, dir, file)[dynstore.EnvKeyID]
			if err := m.putPolicy(accountID, user, fullPolicy); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("re-enroll: %v\n%s", err, out)
	}
	for daemon, id := range before {
		_, file := enrollee(daemon)
		if pairOf(t, dir, file)[dynstore.EnvKeyID] != id {
			t.Errorf("%s key changed", daemon)
		}
	}
	t.Run("kept", checkReach)
}

func TestRemoveInstanceEnforcingStore(t *testing.T) {
	if os.Getenv(dyntest.EnvEndpoint) == "" {
		t.Skip("set ARIZUKO_TEST_DYNAMO_ENDPOINT to an isolated policy-enforcing ExtendDB server")
	}
	instance, dir := newInstance(t)
	m := testManager(t)
	if out, err := enrollRun(t, instance); err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	accounts, err := m.accounts()
	if err != nil {
		t.Fatal(err)
	}
	account := "arizuko_" + instance
	id := accounts[account]
	const fixture = "removal_fixture"
	if err := m.user(id, fixture); err != nil {
		t.Fatal(err)
	}
	if err := m.putPolicy(id, fixture, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:*","Resource":"*"}]}`); err != nil {
		t.Fatal(err)
	}
	key, secret, err := m.mint(id, fixture)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dyntest.Config(t)
	cfg.KeyID, cfg.Secret = key, secret
	admin, err := dynstore.Client(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	t.Cleanup(func() {
		for _, name := range names {
			_, _ = admin.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
		}
		if err := m.deleteAccount(account); err != nil {
			t.Errorf("cleanup account: %v", err)
		}
	})

	create := func(owner string, entities ...string) {
		t.Helper()
		c := cfg
		c.Prefix = dynstore.Prefix(owner)
		d, err := dynstore.Dial(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, entity := range entities {
			if err := d.CreateEntity(context.Background(), entity); err != nil {
				t.Fatalf("create %s.%s: %v", owner, entity, err)
			}
			names = append(names, c.Prefix+entity)
		}
	}
	create("authd", authstore.Entities...)
	create("authd", audit.Entity)
	for _, owner := range []string{store.OwnerRoutd, store.OwnerOnbod, store.OwnerRuned} {
		create(owner, store.EntitiesByOwner[owner]...)
		if owner == store.OwnerRuned {
			create(owner, runstore.Entities...)
		}
		create(owner, store.ScopeEntity, audit.Entity)
	}
	for _, owner := range []string{"gitd", "googd", "slakd", "teled", "discd", "emaid"} {
		create(owner, chanstore.Entities...)
	}
	create("emaid", emaistore.Entities...)
	create("googd", googstore.PollEntity)
	page, err := admin.ListTables(context.Background(), &dynamodb.ListTablesInput{})
	if err != nil || len(page.TableNames) != len(names) {
		t.Fatalf("created %d tables, listed %d: %v", len(names), len(page.TableNames), err)
	}

	bin := t.TempDir()
	script := filepath.Join(bin, "systemctl")
	writeFile(t, script, "#!/bin/sh\nexit 3\n")
	if err := os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := removeInstance(instance); err != nil {
		t.Fatalf("remove: %v", err)
	}
	accounts, err = m.accounts()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := accounts[account]; ok {
		t.Fatal("account remains")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("instance directory remains: %v", err)
	}
	names = nil // ExtendDB refuses account deletion while any table remains.
}
