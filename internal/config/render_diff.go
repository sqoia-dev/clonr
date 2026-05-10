package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// canonicalInstruction is the wire shape used when hashing an
// InstallInstruction. Fields are sorted alphabetically so the JSON is
// deterministic regardless of struct field order in future Go versions.
type canonicalInstruction struct {
	Anchors *api.AnchorPair `json:"anchors,omitempty"`
	Opcode  string          `json:"opcode"`
	Payload string          `json:"payload"`
	Target  string          `json:"target"`
}

// HashInstructions returns the SHA-256 hex digest of the canonical JSON
// serialisation of instrs. The serialisation is:
//
//  1. Sort instructions by (target, opcode) so two equal sets with different
//     ordering produce the same hash.
//  2. Marshal each instruction as canonicalInstruction (alphabetical fields).
//  3. SHA-256 the resulting JSON array.
//
// An empty (nil) slice hashes to the SHA-256 of "[]".
func HashInstructions(instrs []api.InstallInstruction) (string, error) {
	sorted := make([]api.InstallInstruction, len(instrs))
	copy(sorted, instrs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Target != sorted[j].Target {
			return sorted[i].Target < sorted[j].Target
		}
		return sorted[i].Opcode < sorted[j].Opcode
	})

	canonical := make([]canonicalInstruction, len(sorted))
	for i, instr := range sorted {
		canonical[i] = canonicalInstruction{
			Anchors: instr.Anchors,
			Opcode:  instr.Opcode,
			Payload: instr.Payload,
			Target:  instr.Target,
		}
	}

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("config.HashInstructions: marshal: %w", err)
	}

	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}
