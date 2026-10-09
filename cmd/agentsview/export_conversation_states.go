package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/db"
)

func newConversationStatesCommand() *cobra.Command {
	var references string
	command := &cobra.Command{
		Use: "states", Short: "Read current metadata for a bounded list of saved conversation references",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if len(references) > 8192 {
				return errors.New("conversation references exceed 8192 bytes")
			}
			var refs []db.ConversationReference
			if err := json.Unmarshal([]byte(references), &refs, json.RejectUnknownMembers(true)); err != nil {
				return errors.New("expected a JSON array of conversation references")
			}
			database, err := openConversationExportDB(command)
			if err != nil {
				return err
			}
			defer database.Close()
			result, err := database.ExportConversationStates(command.Context(), refs)
			if err != nil {
				return writeConversationExportError(command, err)
			}
			return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), result)
		},
	}
	command.Flags().StringVar(&references, "references", "", "JSON array of up to 32 session_id/message_id references")
	return command
}
