package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/db"
)

func newExportRetentionCommand() *cobra.Command {
	var options db.RetentionOptions
	command := &cobra.Command{Use: "retention-sources", Short: "Export bounded source coverage evidence; never removes originals", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			database, err := openConversationExportDB(command)
			if err != nil {
				return err
			}
			defer database.Close()
			result, err := database.ExportRetentionSources(command.Context(), options)
			if err != nil {
				return err
			}
			return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), result)
		}}
	command.Flags().StringVar(&options.Machine, "machine", "", "Exact configured archive machine (required)")
	command.Flags().StringVar(&options.After, "after", "", "Continue after a previously listed session")
	command.Flags().StringVar(&options.SessionID, "session", "", "Recheck one exact session")
	command.Flags().IntVar(&options.Limit, "limit", 1, "Maximum sources, between 1 and 8")
	return command
}
