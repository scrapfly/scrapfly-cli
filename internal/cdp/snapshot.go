package cdp

import (
	"context"
	"encoding/json"
	"fmt"
)

// AXNode is a compact view over a single Accessibility node. Fields mirror
// the CDP `AXNode` shape but only what the model needs to reason.
//
// AXNodeID is the raw CDP accessibility-tree node id. Pass it directly as
// {type:"axNodeId", query: ax_node_id} to Antibot commands to skip DOM
// resolution.
type AXNode struct {
	Ref           string   `json:"ref"`        // e1, e2, ...
	AXNodeID      string   `json:"ax_node_id"` // raw CDP Accessibility.AXNodeId
	Role          string   `json:"role,omitempty"`
	Name          string   `json:"name,omitempty"`
	Value         string   `json:"value,omitempty"`
	Description   string   `json:"description,omitempty"`
	BackendNodeID int64    `json:"backend_node_id,omitempty"`
	ChildRefs     []string `json:"children,omitempty"`
}

// Snapshot captures the current page's full accessibility tree, builds a
// compact ref table (e1, e2, ...), and returns the flat list in document
// order. The Session's RefTable is replaced.
func (s *Session) Snapshot(ctx context.Context) ([]AXNode, error) {
	res, err := s.Call(ctx, "Accessibility.getFullAXTree", map[string]any{})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Nodes []struct {
			NodeID           string   `json:"nodeId"`
			Ignored          bool     `json:"ignored"`
			Role             *AXVal   `json:"role,omitempty"`
			Name             *AXVal   `json:"name,omitempty"`
			Value            *AXVal   `json:"value,omitempty"`
			Description      *AXVal   `json:"description,omitempty"`
			BackendDOMNodeID int64    `json:"backendDOMNodeId,omitempty"`
			ChildIDs         []string `json:"childIds,omitempty"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res, &raw); err != nil {
		return nil, fmt.Errorf("parse AXTree: %w", err)
	}

	// First pass: assign refs to non-ignored nodes with a role.
	refByNodeID := map[string]string{}
	out := make([]AXNode, 0, len(raw.Nodes))
	counter := 0
	s.RefTable = map[string]int64{}
	for _, n := range raw.Nodes {
		if n.Ignored {
			continue
		}
		role := axString(n.Role)
		if role == "" || role == "none" || role == "presentation" {
			continue
		}
		counter++
		ref := fmt.Sprintf("e%d", counter)
		refByNodeID[n.NodeID] = ref
		s.RefTable[ref] = n.BackendDOMNodeID
		out = append(out, AXNode{
			Ref:           ref,
			AXNodeID:      n.NodeID,
			Role:          role,
			Name:          axString(n.Name),
			Value:         axString(n.Value),
			Description:   axString(n.Description),
			BackendNodeID: n.BackendDOMNodeID,
		})
	}
	// Second pass: resolve child refs.
	byRef := map[string]int{}
	for i, n := range out {
		byRef[n.Ref] = i
	}
	for _, n := range raw.Nodes {
		ref, ok := refByNodeID[n.NodeID]
		if !ok {
			continue
		}
		idx := byRef[ref]
		for _, cid := range n.ChildIDs {
			if cref, ok := refByNodeID[cid]; ok {
				out[idx].ChildRefs = append(out[idx].ChildRefs, cref)
			}
		}
	}
	return out, nil
}

// AXVal mirrors CDP's AXValue — a typed wrapper around a string/number/bool.
type AXVal struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

func axString(v *AXVal) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	// AXValue.value may be a string, a number, or a bool depending on type.
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	return string(v.Value)
}

// BackendIDForRef looks up the CDP backendNodeId previously captured for a
// snapshot ref. Returns (0, false) if the ref is unknown.
func (s *Session) BackendIDForRef(ref string) (int64, bool) {
	id, ok := s.RefTable[ref]
	return id, ok && id != 0
}
