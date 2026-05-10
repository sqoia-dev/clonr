package server

// reactive_push.go — Sprint 36 Day 2
//
// Server-side helper that renders a reactive-config plugin and pushes the
// result to the affected node via the clientd WebSocket hub. This is the
// "inline push" half of the Day 2 dual-write pattern described in
// docs/design/reactive-config.md §9.
//
// The observer goroutine (observer.go) handles hash tracking and alert
// surfacing; this file handles the actual WS delivery. Day 3 will unify
// both into the observer goroutine once all plugins are converted.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/config/plugins"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// pushHostnamePlugin renders the hostname plugin for nodeID+cfg and sends a
// targeted config_push WS message to the node. Non-blocking on the WS send
// (the hub buffers outbound messages). Best-effort: errors are logged but do
// not fail the caller's HTTP handler — the node will pick up the correct
// hostname on next `clustr deploy` if the push fails.
func (s *Server) pushHostnamePlugin(nodeID string, cfg api.NodeConfig) {
	state := config.ClusterState{
		NodeID:     nodeID,
		NodeConfig: cfg,
	}

	instrs, err := plugins.HostnamePlugin{}.Render(state)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "hostname").
			Msg("reactive push: hostname plugin Render failed")
		return
	}
	if len(instrs) == 0 {
		// Empty hostname — nothing to push.
		return
	}

	// Only one instruction expected from the hostname plugin.
	instr := instrs[0]

	// Compute content hash for integrity verification and config_render_state tracking.
	renderedHash, err := config.HashInstructions(instrs)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "hostname").
			Msg("reactive push: HashInstructions failed")
		return
	}

	content := instr.Payload
	sum := sha256.Sum256([]byte(content))
	checksum := fmt.Sprintf("sha256:%x", sum)

	pushPayload := clientd.ConfigPushPayload{
		Target:       "hostname", // maps to configTargets["hostname"] in configapply.go
		Content:      content,
		Checksum:     checksum,
		Plugin:       "hostname",
		RenderedHash: renderedHash,
	}

	payloadBytes, err := json.Marshal(pushPayload)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "hostname").
			Msg("reactive push: marshal ConfigPushPayload failed")
		return
	}

	msgID := uuid.New().String()
	msg := clientd.ServerMessage{
		Type:    "config_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payloadBytes),
	}

	if err := s.clientdHub.Send(nodeID, msg); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Str("plugin", "hostname").
			Msg("reactive push: node not connected or send failed — will apply on next deploy")
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("plugin", "hostname").
		Str("hostname", cfg.Hostname).
		Str("rendered_hash", renderedHash).
		Str("msg_id", msgID).
		Msg("reactive push: hostname config_push sent")
}

// configPushPayloadFromInstruction converts a single InstallInstruction from a
// plugin's Render output into a ConfigPushPayload for the legacy WS wire format.
// Only "overwrite" instructions are supported (hostname plugin always returns overwrite).
func configPushPayloadFromInstruction(instr api.InstallInstruction, pluginName, renderedHash string) (clientd.ConfigPushPayload, error) {
	if instr.Opcode != "overwrite" {
		return clientd.ConfigPushPayload{}, fmt.Errorf("reactive push: only overwrite instructions are supported, got %q", instr.Opcode)
	}
	sum := sha256.Sum256([]byte(instr.Payload))
	checksum := fmt.Sprintf("sha256:%x", sum)
	return clientd.ConfigPushPayload{
		Target:       pluginName,
		Content:      instr.Payload,
		Checksum:     checksum,
		Plugin:       pluginName,
		RenderedHash: renderedHash,
	}, nil
}
