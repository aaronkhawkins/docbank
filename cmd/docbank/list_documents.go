package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"go.kenn.io/docbank/internal/client"
	"go.kenn.io/docbank/internal/store"
)

var (
	listDocumentsLimit  int
	listDocumentsOffset int
	listDocumentsJSON   bool
)

var listDocumentsCmd = &cobra.Command{
	Use:   "list-documents [path-or-id]",
	Short: "List live documents recursively",
	Long: "List live files recursively at vault root or below one directory. " +
		"Use ls for immediate children and search for lexical queries; '*' and paths are not search wildcards.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if listDocumentsLimit < 1 || listDocumentsLimit > 5000 {
			return usageError(errors.New("--limit must be between 1 and 5000"))
		}
		if listDocumentsOffset < 0 {
			return usageError(errors.New("--offset must not be negative"))
		}
		raw := "/"
		if len(args) == 1 {
			raw = args[0]
		}
		selector, err := parseNodeSelector(raw)
		if err != nil {
			return err
		}
		c, err := client.Ensure(cmd.Context())
		if err != nil {
			return err
		}
		directory, err := selector.resolve(cmd.Context(), c)
		if err != nil {
			return err
		}
		if directory.Kind != "dir" {
			return fmt.Errorf("document scope %q: %w", raw, store.ErrNotDir)
		}
		info, err := c.Info(cmd.Context())
		if err != nil {
			return err
		}
		page, err := c.Documents(cmd.Context(), info.VaultID, directory.ID,
			listDocumentsLimit, listDocumentsOffset)
		if err != nil {
			return err
		}
		if listDocumentsJSON {
			return writeCLIJSON(cmd.OutOrStdout(), page)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "SELECTOR\tVERSION\tSIZE\tMEDIA TYPE\tPATH")
		for _, item := range page.Items {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", formatNodeSelector(item.ID),
				item.CurrentVersionID, item.Size, item.MimeType, item.Path)
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("writing document inventory: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "showing %d-%d of %d documents\n",
			page.Offset, page.NextOffset, page.Total)
		if page.Truncated {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"more documents available; use --offset %d to continue\n", page.NextOffset)
		}
		return nil
	},
}

func init() {
	listDocumentsCmd.Flags().IntVar(&listDocumentsLimit, "limit", 500, "page size (1-5000)")
	listDocumentsCmd.Flags().IntVar(&listDocumentsOffset, "offset", 0, "zero-based page offset")
	listDocumentsCmd.Flags().BoolVar(&listDocumentsJSON, "json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(listDocumentsCmd)
}
