package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/db"
)

func newExportUsageDaysCommand() *cobra.Command {
	var options db.UsageDaysOptions
	command := &cobra.Command{
		Use: "usage-days", Short: "Export daily source totals and native credits from the local archive",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			database, err := openConversationExportDB(command)
			if err != nil {
				return err
			}
			defer database.Close()
			result, err := database.ExportUsageDays(command.Context(), options)
			if err != nil {
				return err
			}
			return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), result)
		},
	}
	command.Flags().StringVar(&options.From, "from", "", "First calendar date (YYYY-MM-DD, required)")
	command.Flags().StringVar(&options.To, "to", "", "Last calendar date (YYYY-MM-DD, required)")
	command.Flags().StringVar(&options.Timezone, "timezone", "", "IANA calendar timezone (required)")
	command.Flags().StringVar(&options.Source, "source", "", "Exact source, such as codex, qoder-cn, kiro-cli, or kiro-crew")
	return command
}
