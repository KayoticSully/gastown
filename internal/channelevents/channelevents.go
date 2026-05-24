// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/<rig>/<channel>/*.event
// and consumed by await-event subscribers (e.g., the refinery watching for
// MERGE_READY events). This is distinct from the activity feed events in
// the events package (~/gt/.events.jsonl).
//
// Channels are rig-scoped (gt-gyc): each rig's events live under their own
// directory so a refinery (or any await-event --cleanup consumer) in one rig
// cannot consume — and delete — events addressed to another rig. Callers that
// genuinely cannot resolve a rig may pass an empty rig, which falls back to the
// legacy unscoped ~/gt/events/<channel>/ path.
package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/workspace"
)

// ValidChannelName restricts channel names to safe characters (no path traversal).
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// EventDir returns the directory that holds events for a (rig, channel) pair
// under a town root. Events are rig-scoped to ~/gt/events/<rig>/<channel>/ so
// that an await-event consumer in one rig cannot read or --cleanup events
// addressed to another (gt-gyc). An empty rig falls back to the legacy
// unscoped ~/gt/events/<channel>/ path for callers that cannot resolve a rig.
//
// Producers and consumers MUST agree on the rig string for events to be
// delivered; in practice the rig comes from GT_RIG (set on refinery/witness
// sessions) on both sides.
func EventDir(townRoot, rig, channel string) string {
	if rig == "" {
		return filepath.Join(townRoot, "events", channel)
	}
	return filepath.Join(townRoot, "events", rig, channel)
}

// validateNames checks the channel (required) and rig (optional) names against
// the safe-character pattern to prevent path traversal.
func validateNames(rig, channel string) error {
	if !ValidChannelName.MatchString(channel) {
		return fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}
	if rig != "" && !ValidChannelName.MatchString(rig) {
		return fmt.Errorf("invalid rig name %q: must match [a-zA-Z0-9_-]", rig)
	}
	return nil
}

// Emit creates an event file in the rig-scoped channel directory, resolving the
// town root from the current working directory.
func Emit(rig, channel, eventType string, payloadPairs []string) (string, error) {
	if err := validateNames(rig, channel); err != nil {
		return "", err
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		home, _ := os.UserHomeDir()
		townRoot = filepath.Join(home, "gt")
	}
	eventDir := EventDir(townRoot, rig, channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}

	return emitToDir(eventDir, rig, channel, eventType, payloadPairs)
}

// EmitToTown creates an event file using an explicit town root.
// Used by internal callers that already know the town root.
func EmitToTown(townRoot, rig, channel, eventType string, payloadPairs []string) (string, error) {
	if err := validateNames(rig, channel); err != nil {
		return "", err
	}

	eventDir := EventDir(townRoot, rig, channel)
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}
	return emitToDir(eventDir, rig, channel, eventType, payloadPairs)
}

// emitToDir writes an event file to the given directory.
func emitToDir(eventDir, rig, channel, eventType string, payloadPairs []string) (string, error) {
	if err := validateNames(rig, channel); err != nil {
		return "", err
	}

	payload := make(map[string]string)
	for _, pair := range payloadPairs {
		key, val, found := strings.Cut(pair, "=")
		if found {
			payload[key] = val
		}
	}

	now := time.Now()
	event := map[string]interface{}{
		"type":      eventType,
		"channel":   channel,
		"rig":       rig,
		"timestamp": now.Format(time.RFC3339),
		"payload":   payload,
	}

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	seq := emitSeq.Add(1)
	eventFile := filepath.Join(eventDir, fmt.Sprintf("%d-%d-%d.event", now.UnixNano(), seq, os.Getpid()))
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		return "", fmt.Errorf("writing event file: %w", err)
	}

	return eventFile, nil
}
