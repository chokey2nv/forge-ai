package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/api"
	mongo_store "github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/store/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const adminUsage = `usage:
  forge users add <name> [--admin] [--email <address>]   password on stdin (first line)
  forge users list
  forge tokens create <user> <token name>                prints the token once`

// runAdmin manages users and tokens directly in the database (Phase 13.2),
// for the first user and for scripts; it needs no running control plane.
func runAdmin(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, adminUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mc, err := mongo.Connect(options.Client().ApplyURI(envOr("MONGODB_URI", "mongodb://127.0.0.1:27017")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "mongo:", err)
		return 1
	}
	defer func() { _ = mc.Disconnect(context.Background()) }()
	db := mc.Database(envOr("MONGODB_DATABASE", "forge_ai"))
	h := &api.Handlers{Users: mongo_store.NewUserRepo(db), Tokens: mongo_store.NewTokenRepo(db)}

	switch args[0] + " " + args[1] {
	case "users add":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, adminUsage)
			return 2
		}
		admin, email := false, ""
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "--admin":
				admin = true
			case "--email":
				if i+1 < len(args) {
					email = args[i+1]
					i++
				}
			}
		}
		fmt.Fprint(os.Stderr, "Password (first line of stdin): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		u, err := h.AddUser(ctx, args[2], strings.TrimRight(line, "\r\n"), email, admin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+err.Error())
			return 1
		}
		fmt.Fprintf(os.Stderr, "\ncreated %s (%s)%s\n", u.Name, u.ID, map[bool]string{true: ", instance admin"}[u.InstanceAdmin])
		return 0
	case "users list":
		us, err := h.Users.List(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, u := range us {
			fmt.Printf("%s\t%s\tadmin=%v\tdisabled=%v\n", u.ID, u.Name, u.InstanceAdmin, u.DisabledAt != nil)
		}
		return 0
	case "tokens create":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, adminUsage)
			return 2
		}
		u, err := h.Users.GetByName(ctx, args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "no user %q\n", args[2])
			return 1
		}
		_, secret, err := h.IssueToken(ctx, u.ID, strings.Join(args[3:], " "))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(secret)
		return 0
	}
	fmt.Fprintln(os.Stderr, adminUsage)
	return 2
}
