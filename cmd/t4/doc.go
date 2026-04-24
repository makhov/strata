package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/t4db/t4/t4doc"
)

type docFlags struct {
	endpoint string
	token    string
}

func docCmd() *cobra.Command {
	flags := &docFlags{}
	cmd := &cobra.Command{
		Use:   "doc",
		Short: "Use the T4 document API",
	}
	cmd.PersistentFlags().StringVar(&flags.endpoint, "endpoint", "localhost:3379", "document gRPC endpoint (env: T4_DOC_ENDPOINT)")
	cmd.PersistentFlags().StringVar(&flags.token, "token", "", "auth token for document RPCs (env: T4_TOKEN)")
	prependPreRunE(cmd, func(cmd *cobra.Command, _ []string) error {
		return applyEnvVars(cmd, map[string]string{
			"endpoint": "T4_DOC_ENDPOINT",
			"token":    "T4_TOKEN",
		})
	})
	cmd.AddCommand(docPutCmd(flags))
	cmd.AddCommand(docGetCmd(flags))
	cmd.AddCommand(docDeleteCmd(flags))
	cmd.AddCommand(docPatchCmd(flags))
	cmd.AddCommand(docFindCmd(flags))
	cmd.AddCommand(docWatchCmd(flags))
	cmd.AddCommand(docIndexCmd(flags))
	return cmd
}

func docPutCmd(flags *docFlags) *cobra.Command {
	var raw string
	cmd := &cobra.Command{
		Use:   "put <collection> <id>",
		Short: "Create or replace a JSON document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var doc map[string]any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				return err
			}
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			coll := t4doc.NewCollection[map[string]any](client, args[0])
			rev, err := coll.Put(cmd.Context(), args[1], doc)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d\n", rev)
			return nil
		},
	}
	cmd.Flags().StringVar(&raw, "json", "", "JSON document")
	_ = cmd.MarkFlagRequired("json")
	return cmd
}

func docGetCmd(flags *docFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <collection> <id>",
		Short: "Get one document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			doc, err := t4doc.NewCollection[map[string]any](client, args[0]).FindByID(cmd.Context(), args[1])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(doc)
		},
	}
	return cmd
}

func docDeleteCmd(flags *docFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <collection> <id>",
		Short: "Delete one document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			rev, err := t4doc.NewCollection[map[string]any](client, args[0]).Delete(cmd.Context(), args[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d\n", rev)
			return nil
		},
	}
	return cmd
}

func docPatchCmd(flags *docFlags) *cobra.Command {
	var raw string
	cmd := &cobra.Command{
		Use:   "patch <collection> <id>",
		Short: "Apply a JSON merge patch",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !json.Valid([]byte(raw)) {
				return fmt.Errorf("invalid JSON merge patch")
			}
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			rev, err := t4doc.NewCollection[map[string]any](client, args[0]).Patch(cmd.Context(), args[1], t4doc.MergePatch(raw))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d\n", rev)
			return nil
		},
	}
	cmd.Flags().StringVar(&raw, "json", "", "JSON merge patch")
	_ = cmd.MarkFlagRequired("json")
	return cmd
}

func docFindCmd(flags *docFlags) *cobra.Command {
	var eq, typ string
	var limit int
	var allowScan bool
	var maxScan int64
	cmd := &cobra.Command{
		Use:   "find <collection>",
		Short: "Find documents by equality filter",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, err := cliFilter(eq, typ)
			if err != nil {
				return err
			}
			opts := []t4doc.FindOption{t4doc.Limit(limit)}
			if allowScan {
				opts = append(opts, t4doc.AllowScan())
			}
			if maxScan > 0 {
				opts = append(opts, t4doc.MaxScan(maxScan))
			}
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			docs, err := t4doc.NewCollection[map[string]any](client, args[0]).Find(cmd.Context(), filter, opts...)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(docs)
		},
	}
	addFindFlags(cmd, &eq, &typ, &limit, &allowScan, &maxScan)
	return cmd
}

func docWatchCmd(flags *docFlags) *cobra.Command {
	var eq, typ string
	cmd := &cobra.Command{
		Use:   "watch <collection>",
		Short: "Watch matching document changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter, err := cliFilter(eq, typ)
			if err != nil {
				return err
			}
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			changes, err := t4doc.NewCollection[map[string]any](client, args[0]).Watch(cmd.Context(), filter)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			for ch := range changes {
				if err := enc.Encode(ch); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&eq, "eq", "", "equality filter field=value")
	cmd.Flags().StringVar(&typ, "type", "string", "filter value type: string, int64, bool")
	return cmd
}

func docIndexCmd(flags *docFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Manage document indexes",
	}
	cmd.AddCommand(docIndexCreateCmd(flags))
	return cmd
}

func docIndexCreateCmd(flags *docFlags) *cobra.Command {
	var field, typ string
	cmd := &cobra.Command{
		Use:   "create <collection> <name>",
		Short: "Create and backfill an equality index",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ft, err := cliFieldType(typ)
			if err != nil {
				return err
			}
			client, err := newDocClient(cmd.Context(), flags)
			if err != nil {
				return err
			}
			defer client.Close()
			return t4doc.NewCollection[map[string]any](client, args[0]).CreateIndex(cmd.Context(), t4doc.IndexSpec{
				Name:  args[1],
				Field: field,
				Type:  ft,
			})
		},
	}
	cmd.Flags().StringVar(&field, "field", "", "JSON field path")
	cmd.Flags().StringVar(&typ, "type", "string", "index field type: string, int64, bool")
	_ = cmd.MarkFlagRequired("field")
	return cmd
}

func addFindFlags(cmd *cobra.Command, eq, typ *string, limit *int, allowScan *bool, maxScan *int64) {
	cmd.Flags().StringVar(eq, "eq", "", "equality filter field=value")
	cmd.Flags().StringVar(typ, "type", "string", "filter value type: string, int64, bool")
	cmd.Flags().IntVar(limit, "limit", 0, "maximum number of documents to return")
	cmd.Flags().BoolVar(allowScan, "allow-scan", false, "allow a bounded collection scan when no index exists")
	cmd.Flags().Int64Var(maxScan, "max-scan", 0, "maximum documents to scan with --allow-scan")
}

func newDocClient(ctx context.Context, flags *docFlags) (*t4doc.Client, error) {
	client, err := t4doc.Dial(ctx, flags.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	token := flags.token
	if token == "" {
		token = os.Getenv("T4_TOKEN")
	}
	client.SetToken(token)
	return client, nil
}

func cliFilter(eq, typ string) (t4doc.Filter, error) {
	if eq == "" {
		return t4doc.All(), nil
	}
	parts := strings.SplitN(eq, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return t4doc.Filter{}, fmt.Errorf("--eq must be field=value")
	}
	value, err := cliValue(parts[1], typ)
	if err != nil {
		return t4doc.Filter{}, err
	}
	return t4doc.Eq(parts[0], value), nil
}

func cliValue(raw, typ string) (any, error) {
	switch typ {
	case "string":
		return raw, nil
	case "int64":
		return strconv.ParseInt(raw, 10, 64)
	case "bool":
		return strconv.ParseBool(raw)
	default:
		return nil, fmt.Errorf("unknown type %q", typ)
	}
}

func cliFieldType(typ string) (t4doc.FieldType, error) {
	switch typ {
	case "string":
		return t4doc.String, nil
	case "int64":
		return t4doc.Int64, nil
	case "bool":
		return t4doc.Bool, nil
	default:
		return "", fmt.Errorf("unknown type %q", typ)
	}
}
