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

//lint:ignore U1000 wired into the node-PUT handler once reactive config push is enabled (REACTIVE-PUSH-SSSD, Sprint 38)
// pushSSSDPlugin renders the sssd plugin for nodeID+cfg and sends a targeted
// config_push WS message to the node. Best-effort: errors are logged but do
// not fail the caller's HTTP handler. The node will pick up the correct
// sssd.conf on next deploy if the push fails.
func (s *Server) pushSSSDPlugin(nodeID string, cfg api.NodeConfig) {
	state := config.ClusterState{
		NodeID:     nodeID,
		NodeConfig: cfg,
	}

	instrs, err := plugins.SSSDPlugin{}.Render(state)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "sssd").
			Msg("reactive push: sssd plugin Render failed")
		return
	}
	if len(instrs) == 0 {
		// LDAPConfig is nil — nothing to push.
		return
	}

	renderedHash, err := config.HashInstructions(instrs)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "sssd").
			Msg("reactive push: HashInstructions failed")
		return
	}

	s.sendPluginPush(nodeID, "sssd", instrs[0], renderedHash)
}

// pushHostsPlugin renders the hosts plugin for the given node (using allNodes
// for the full cluster roster) and sends a targeted config_push WS message.
// Best-effort: errors are logged; the node picks up the correct /etc/hosts
// on next deploy if the push fails.
func (s *Server) pushHostsPlugin(nodeID string, cfg api.NodeConfig, allNodes []api.NodeConfig) {
	state := config.ClusterState{
		NodeID:     nodeID,
		NodeConfig: cfg,
		AllNodes:   allNodes,
	}

	instrs, err := plugins.HostsPlugin{}.Render(state)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "hosts").
			Msg("reactive push: hosts plugin Render failed")
		return
	}
	if len(instrs) == 0 {
		return
	}

	renderedHash, err := config.HashInstructions(instrs)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "hosts").
			Msg("reactive push: HashInstructions failed")
		return
	}

	s.sendPluginPush(nodeID, "hosts", instrs[0], renderedHash)
}

// pushLimitsPlugin renders the limits plugin for nodeID+cfg and sends a
// targeted config_push WS message to the node. Best-effort: errors logged;
// node picks up correct limits.conf on next deploy if push fails.
func (s *Server) pushLimitsPlugin(nodeID string, cfg api.NodeConfig) {
	state := config.ClusterState{
		NodeID:     nodeID,
		NodeConfig: cfg,
	}

	instrs, err := plugins.LimitsPlugin{}.Render(state)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "limits").
			Msg("reactive push: limits plugin Render failed")
		return
	}
	if len(instrs) == 0 {
		return
	}

	renderedHash, err := config.HashInstructions(instrs)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", "limits").
			Msg("reactive push: HashInstructions failed")
		return
	}

	s.sendPluginPush(nodeID, "limits", instrs[0], renderedHash)
}

// sendPluginPush is the shared WS delivery path for single-instruction plugins.
// It marshals the ConfigPushPayload, wraps it in a ServerMessage, and sends it
// to nodeID via the clientdHub. Non-blocking on the WS send. Best-effort.
func (s *Server) sendPluginPush(nodeID, pluginName string, instr api.InstallInstruction, renderedHash string) {
	payload, err := configPushPayloadFromInstruction(instr, pluginName, renderedHash)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", pluginName).
			Msg("reactive push: configPushPayloadFromInstruction failed")
		return
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Str("plugin", pluginName).
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
		log.Warn().Err(err).Str("node_id", nodeID).Str("plugin", pluginName).
			Msg("reactive push: node not connected or send failed — will apply on next deploy")
		return
	}

	log.Info().
		Str("node_id", nodeID).
		Str("plugin", pluginName).
		Str("rendered_hash", renderedHash).
		Str("msg_id", msgID).
		Msg("reactive push: config_push sent")
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
