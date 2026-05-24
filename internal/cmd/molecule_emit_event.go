package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/channelevents"
)

var (
	emitEventChannel string
	emitEventType    string
	emitEventPayload []string
	emitEventRig     string
)

// resolveEventRig determines the rig used to scope an event channel directory.
// An explicit value (from --rig) wins; otherwise it is detected from the
// environment (GT_RIG) or the current working directory. An empty result means
// the legacy unscoped ~/gt/events/<channel>/ path is used (gt-gyc).
func resolveEventRig(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return detectCurrentRig()
}

var moleculeEmitEventCmd = &cobra.Command{
	Use:   "emit-event",
	Short: "Emit a file-based event on a named channel",
	Long: `Emit an event file to ~/gt/events/<rig>/<channel>/ for subscribers to pick up.

This is the Go counterpart to emit-event.sh. Events are JSON files consumed
by await-event subscribers (e.g., the refinery watching for MERGE_READY events).

Channels are rig-scoped (gt-gyc): the rig comes from --rig, else GT_RIG, else
the current working directory. This keeps each rig's events isolated so a
consumer in one rig cannot read or --cleanup another rig's events.

EVENT FORMAT:
Creates a JSON file at ~/gt/events/<rig>/<channel>/<timestamp>.event:
  {"type": "...", "channel": "...", "rig": "...", "timestamp": "...", "payload": {...}}

EXAMPLES:
  # Emit a MERGE_READY event for the refinery
  gt mol step emit-event --channel refinery --type MERGE_READY \
    --payload polecat=nux --payload branch=polecat/nux/gt-iw7m

  # Emit a PATROL_WAKE event
  gt mol step emit-event --channel refinery --type PATROL_WAKE \
    --payload source=witness --payload queue_depth=3

  # Emit an MQ_SUBMIT event
  gt mol step emit-event --channel refinery --type MQ_SUBMIT \
    --payload branch=feat/new-feature --payload mr_id=bd-42`,
	RunE: runMoleculeEmitEvent,
}

// EmitEventResult is returned when an event is emitted.
type EmitEventResult struct {
	Path    string `json:"path"`
	Channel string `json:"channel"`
	Type    string `json:"type"`
}

func init() {
	moleculeEmitEventCmd.Flags().StringVar(&emitEventChannel, "channel", "",
		"Event channel name (required, e.g., 'refinery')")
	moleculeEmitEventCmd.Flags().StringVar(&emitEventType, "type", "",
		"Event type (required, e.g., 'MERGE_READY')")
	moleculeEmitEventCmd.Flags().StringArrayVar(&emitEventPayload, "payload", nil,
		"Payload key=value pairs (repeatable)")
	moleculeEmitEventCmd.Flags().StringVar(&emitEventRig, "rig", "",
		"Rig to scope the event to (default: GT_RIG env or detected from cwd). "+
			"Events are isolated per rig so consumers in other rigs cannot read or delete them.")
	moleculeEmitEventCmd.Flags().BoolVar(&moleculeJSON, "json", false,
		"Output as JSON")
	_ = moleculeEmitEventCmd.MarkFlagRequired("channel")
	_ = moleculeEmitEventCmd.MarkFlagRequired("type")

	moleculeStepCmd.AddCommand(moleculeEmitEventCmd)
}

func runMoleculeEmitEvent(cmd *cobra.Command, args []string) error {
	rig := resolveEventRig(emitEventRig)
	path, err := channelevents.Emit(rig, emitEventChannel, emitEventType, emitEventPayload)
	if err != nil {
		return err
	}

	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(EmitEventResult{
			Path:    path,
			Channel: emitEventChannel,
			Type:    emitEventType,
		})
	}

	fmt.Println(path)
	return nil
}
