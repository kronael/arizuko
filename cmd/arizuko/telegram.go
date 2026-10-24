package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/store"
)

func cmdTelegram(args []string) {
	need(args, 2, "arizuko telegram <instance> rekey-dms ...")
	if args[1] != "rekey-dms" {
		die("unknown telegram subcommand: %s", args[1])
	}
	cmdTelegramRekey(args[0], mustInstanceDir(args[0]), args[2:])
}

func telegramAccounts(values []string) (map[string]string, error) {
	accounts := map[string]string{}
	ids := map[string]bool{}
	for _, value := range values {
		name, id, ok := strings.Cut(value, "=")
		n, err := strconv.ParseInt(id, 10, 64)
		if !ok || name == "" || strings.ContainsAny(name, " /:=\t\n") ||
			err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
			return nil, fmt.Errorf("account %q requires CHANNEL_NAME=positive_bot_id", value)
		}
		if accounts[name] != "" || ids[id] {
			return nil, fmt.Errorf("duplicate account or bot id in %q", value)
		}
		accounts[name], ids[id] = id, true
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("at least one --account CHANNEL_NAME=bot_id is required")
	}
	return accounts, nil
}

func cmdTelegramRekey(instance, dataDir string, args []string) {
	fs := flag.NewFlagSet("telegram rekey-dms", flag.ContinueOnError)
	var values renameList
	fs.Var(&values, "account", "repeat CHANNEL_NAME=bot_id for each Telegram account")
	inventoryPath := fs.String("inventory", "", "private complete stopped owner partition counts JSON")
	endpoint := fs.String("endpoint", "", "operator endpoint matching the inventory")
	journal := fs.String("journal", "", "private operation directory outside the instance")
	dryRun := fs.Bool("dry-run", false, "preflight and print counts without writing")
	stopped := fs.Bool("stopped", false, "assert all instance writers and guests are stopped")
	yes := fs.Bool("yes", false, "execute without a prompt")
	resume := fs.Bool("resume", false, "finish the operation in --journal")
	rollback := fs.Bool("rollback", false, "reverse the operation in --journal")
	if err := flexParse(fs, args); err != nil {
		die("Failed: %v", err)
	}
	if fs.NArg() != 0 || *inventoryPath == "" || *endpoint == "" || (*resume && *rollback) {
		die("usage: arizuko telegram <instance> rekey-dms --account CHANNEL_NAME=bot_id ... " +
			"--inventory <file> --endpoint <url> --dry-run | " +
			"--stopped --yes --journal <dir> [--resume|--rollback]")
	}
	if !*dryRun && (!*stopped || !*yes || *journal == "") {
		die("Failed: execution requires --stopped --yes --journal; keep all writers stopped through verification")
	}
	accounts, err := telegramAccounts(values)
	if err != nil {
		die("Failed: %v", err)
	}
	var inventory renameInventory
	if err = renameReadJSON(*inventoryPath, &inventory); err != nil {
		die("Failed: inventory: %v", err)
	}
	drivers, err := renameOpenOwners(instance, dataDir, *endpoint, inventory)
	if err != nil {
		die("Failed: target preflight: %v", err)
	}
	ctx := context.Background()
	var plan renamePlan
	if *resume || *rollback {
		err = renameReadJSON(filepath.Join(*journal, "plan.json"), &plan)
		if err == nil && (plan.Instance != instance || plan.DataDir != dataDir ||
			!reflect.DeepEqual(plan.Inventory, inventory) || !maps.Equal(plan.Accounts, accounts) ||
			len(plan.Mapping) != 0 || len(plan.Files) != 0) {
			err = fmt.Errorf("journal target, inventory or accounts differ from this command")
		}
	} else {
		plan, err = telegramRekeyPreflight(ctx, instance, dataDir, accounts, inventory, drivers)
	}
	if err != nil {
		die("Failed: rekey preflight: %v", err)
	}
	counts := map[string]int{}
	for _, row := range plan.Rows {
		if row.Entity == store.Messages && row.Before.Attrs["source"] == "" {
			counts[row.Before.Key.Partition]++
		}
	}
	for _, jid := range slices.Sorted(maps.Keys(counts)) {
		fmt.Printf("%s: %d message rows without source\n", jid, counts[jid])
	}
	fmt.Printf("rekey-dms: %d items\n", len(plan.Rows))
	if *dryRun {
		return
	}
	if err = renameExecute(ctx, *journal, plan, drivers, *resume, *rollback); err != nil {
		die("Failed: %v; keep writers stopped and resume or roll back using %s", err, *journal)
	}
	fmt.Println("rekey-dms complete; keep writers stopped through verification")
}
