package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/joho/godotenv"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

const storeUsage = "arizuko store [-i instance] enroll [snapshot-dir]"

type holder struct {
	acct, user string
	active     bool
}

const scanPage = 500

func cmdStore(args []string) {
	if verb(args, storeUsage) != "enroll" {
		usage(nil, storeUsage)
	}
	if err := storeEnroll(args, os.Stdout); err != nil {
		die("Failed: %v", err)
	}
}

func storeEnroll(args []string, w io.Writer) error {
	if len(args) < 2 || len(args) > 3 {
		return errors.New("usage: " + storeUsage)
	}
	instance := args[0]
	if instance == "" {
		return errors.New("enroll needs an instance: -i NAME")
	}
	dataDir, err := instanceDir(instance)
	if err != nil {
		return err
	}
	if err := compose.WriteOperatorEnv(dataDir); err != nil {
		return err
	}
	dst, err := view(dataDir)
	if err != nil {
		return err
	}
	m, err := newManager(dst)
	if err != nil {
		return err
	}
	var src *dynamodb.Client
	var tables []types.TableDescription
	if len(args) == 3 {
		old, err := view(args[2])
		if err != nil {
			return err
		}
		if src, err = clientOf(old, old[dynstore.EnvKeyID], old[dynstore.EnvSecret]); err != nil {
			return err
		}
		if tables, err = inventory(src); err != nil {
			return err
		}
	}
	clients, err := enroll(dataDir, "arizuko_"+instance, dst, m, w)
	if err != nil {
		return err
	}
	if src == nil {
		return nil
	}
	return copyTables(src, clients, tables, w)
}

func view(dir string) (map[string]string, error) {
	env, err := godotenv.Read(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}
	operator, err := godotenv.Read(filepath.Join(dir, "env", "operator.env"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	maps.Copy(env, operator)
	return env, nil
}

func clientOf(view map[string]string, keyID, secret string) (*dynamodb.Client, error) {
	ca, err := dynstore.DecodeCA(view[dynstore.EnvCA])
	if err != nil {
		return nil, err
	}
	return dynstore.Client(dynstore.Config{
		Endpoint: view[dynstore.EnvURL],
		Region:   view[dynstore.EnvRegion],
		KeyID:    keyID,
		Secret:   secret,
		CA:       ca,
	})
}

func enrollee(daemon string) (user, file string) {
	if daemon == "cmd/arizuko" {
		daemon = "operator"
	}
	return daemon, daemon + ".env"
}

func policy(prefixes []string) string {
	resources := make([]string, len(prefixes))
	for i, p := range prefixes {
		resources[i] = "arn:aws:dynamodb:*:*:table/" + dynstore.Prefix(p) + "*"
	}
	doc, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Effect": "Allow", "Action": "dynamodb:*", "Resource": resources},
		},
	})
	return string(doc)
}

func holders(m *manager) (map[string]holder, error) {
	accounts, err := m.accounts()
	if err != nil {
		return nil, err
	}
	held := map[string]holder{}
	for name, id := range accounts {
		users, err := m.users(id)
		if err != nil {
			return nil, err
		}
		for _, user := range users {
			keys, err := m.keys(id, user)
			if err != nil {
				return nil, err
			}
			for _, k := range keys {
				held[k.ID] = holder{name, user, k.Active}
			}
		}
	}
	return held, nil
}

type row struct {
	user, path string
	prefixes   []string
	pair       map[string]string
	replaced   string
}

func enroll(dataDir, acct string, dst map[string]string, m *manager, w io.Writer) (map[string]*dynamodb.Client, error) {
	held, err := holders(m)
	if err != nil {
		return nil, err
	}
	var rows []row
	for _, daemon := range slices.Sorted(maps.Keys(store.StoreReach)) {
		prefixes := store.StoreReach[daemon]
		if len(prefixes) == 0 {
			continue
		}
		user, file := enrollee(daemon)
		r := row{user: user, path: filepath.Join(dataDir, "env", file), prefixes: prefixes}
		pair, err := godotenv.Read(r.path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		id, secret := pair[dynstore.EnvKeyID], pair[dynstore.EnvSecret]
		h, known := held[id]
		switch {
		case (id == "") != (secret == ""):
			return nil, fmt.Errorf("%s holds half a key pair", r.path)
		case id != "" && known && (h.acct != acct || h.user != user):
			return nil, fmt.Errorf("%s holds key %s of user %s in account %s", r.path, id, h.user, h.acct)
		case id != "" && known && !h.active:
			return nil, fmt.Errorf("%s holds the inactive key %s", r.path, id)
		case id != "" && known:
			r.pair = pair
		case id != "":
			r.replaced = id
		}
		rows = append(rows, r)
	}

	id, err := m.account(acct)
	if err != nil {
		return nil, err
	}
	clients := map[string]*dynamodb.Client{}
	for _, r := range rows {
		if err := m.user(id, r.user); err != nil {
			return nil, err
		}
		if err := m.putPolicy(id, r.user, policy(r.prefixes)); err != nil {
			return nil, err
		}
		minted := r.pair == nil
		how := "kept"
		if minted {
			how = "minted"
			keyID, secret, err := m.mint(id, r.user)
			if err != nil {
				return nil, err
			}
			if err := compose.WriteStoreKey(dataDir, r.path, keyID, secret); err != nil {
				return nil, err
			}
			r.pair = map[string]string{dynstore.EnvKeyID: keyID, dynstore.EnvSecret: secret}
		}
		c, err := clientOf(dst, r.pair[dynstore.EnvKeyID], r.pair[dynstore.EnvSecret])
		if err != nil {
			return nil, err
		}
		if !minted {
			if err := signedCheck(c, r.prefixes[0]); err != nil {
				return nil, fmt.Errorf("%s: %w", r.user, err)
			}
		}
		for _, p := range r.prefixes {
			if clients[p] == nil {
				clients[p] = c
			}
		}
		line := fmt.Sprintf("%s %s %s %s", r.user, strings.Join(r.prefixes, ","), r.pair[dynstore.EnvKeyID], how)
		if r.replaced != "" {
			line += " replaced " + r.replaced
		}
		_, _ = fmt.Fprintln(w, line)
	}
	return clients, nil
}

func signedCheck(c *dynamodb.Client, prefix string) error {
	_, err := c.DescribeTable(context.Background(), &dynamodb.DescribeTableInput{
		TableName: aws.String(dynstore.Prefix(prefix) + "enroll_check"),
	})
	var missing *types.ResourceNotFoundException
	if err != nil && !errors.As(err, &missing) {
		return err
	}
	return nil
}

func inventory(src *dynamodb.Client) ([]types.TableDescription, error) {
	ctx := context.Background()
	owners := map[string]bool{}
	for _, prefixes := range store.StoreReach {
		for _, p := range prefixes {
			owners[p] = true
		}
	}
	var tables []types.TableDescription
	pages := dynamodb.NewListTablesPaginator(src, &dynamodb.ListTablesInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range page.TableNames {
			if owner, _, ok := strings.Cut(name, "."); !ok || !owners[owner] {
				return nil, fmt.Errorf("table %s has no prefix of a StoreReach row", name)
			}
			out, err := src.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)})
			if err != nil {
				return nil, err
			}
			td := *out.Table
			if len(td.GlobalSecondaryIndexes)+len(td.LocalSecondaryIndexes) > 0 ||
				(td.StreamSpecification != nil && aws.ToBool(td.StreamSpecification.StreamEnabled)) {
				return nil, fmt.Errorf("table %s has an index or a stream, which the copy does not carry", name)
			}
			tables = append(tables, td)
		}
	}
	return tables, nil
}

func copyTables(src *dynamodb.Client, dst map[string]*dynamodb.Client, tables []types.TableDescription, w io.Writer) error {
	for _, td := range tables {
		owner, _, _ := strings.Cut(aws.ToString(td.TableName), ".")
		n, err := copyTable(src, dst[owner], td)
		if err != nil {
			return fmt.Errorf("%s: %w", aws.ToString(td.TableName), err)
		}
		_, _ = fmt.Fprintf(w, "%s %d equal\n", aws.ToString(td.TableName), n)
	}
	return nil
}

func shape(td types.TableDescription) string {
	var keys, defs []string
	for _, k := range td.KeySchema {
		keys = append(keys, aws.ToString(k.AttributeName)+"/"+string(k.KeyType))
	}
	for _, d := range td.AttributeDefinitions {
		defs = append(defs, aws.ToString(d.AttributeName)+"/"+string(d.AttributeType))
	}
	slices.Sort(defs)
	return strings.Join(keys, ",") + ";" + strings.Join(defs, ",")
}

func copyTable(src, dst *dynamodb.Client, td types.TableDescription) (int, error) {
	ctx := context.Background()
	table := &dynamodb.DescribeTableInput{TableName: td.TableName}
	if _, err := dst.DescribeTable(ctx, table); err != nil {
		var missing *types.ResourceNotFoundException
		if !errors.As(err, &missing) {
			return 0, err
		}
		if _, err := dst.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:            td.TableName,
			BillingMode:          types.BillingModePayPerRequest,
			KeySchema:            td.KeySchema,
			AttributeDefinitions: td.AttributeDefinitions,
		}); err != nil {
			return 0, err
		}
	}
	if err := dynstore.WaitTable(ctx, dst, td.TableName, 60*time.Second); err != nil {
		return 0, err
	}
	have, err := dst.DescribeTable(ctx, table)
	if err != nil {
		return 0, err
	}
	if shape(*have.Table) != shape(td) {
		return 0, fmt.Errorf("key schema %s, source %s", shape(*have.Table), shape(td))
	}

	var hash string
	for _, k := range td.KeySchema {
		if k.KeyType == types.KeyTypeHash {
			hash = aws.ToString(k.AttributeName)
		}
	}
	scanned := 0
	pages := dynamodb.NewScanPaginator(src, &dynamodb.ScanInput{
		TableName:      td.TableName,
		ConsistentRead: aws.Bool(true),
		Limit:          aws.Int32(scanPage),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		for _, item := range page.Items {
			scanned++
			_, err := dst.PutItem(ctx, &dynamodb.PutItemInput{
				TableName:                td.TableName,
				Item:                     item,
				ConditionExpression:      aws.String("attribute_not_exists(#k)"),
				ExpressionAttributeNames: map[string]string{"#k": hash},
			})
			var kept *types.ConditionalCheckFailedException
			if err != nil && !errors.As(err, &kept) {
				return 0, err
			}
		}
	}

	counted := 0
	pages = dynamodb.NewScanPaginator(dst, &dynamodb.ScanInput{
		TableName:      td.TableName,
		ConsistentRead: aws.Bool(true),
		Select:         types.SelectCount,
		Limit:          aws.Int32(scanPage),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		counted += int(page.Count)
	}
	if counted != scanned {
		return 0, fmt.Errorf("source %d items, destination %d", scanned, counted)
	}
	return scanned, nil
}
