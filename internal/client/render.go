package client

import (
	"agent_romm/internal/protocol"
	"agent_romm/internal/room"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

func SafeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) && r != '\n' && r != '\t' {
			fmt.Fprintf(&b, "\\u{%04x}", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type itemKey struct{ thread, turn, item string }
type projection struct {
	active          string
	activeWatermark uint64
	revision        uint64
	partial         map[itemKey]string
	used            int
	truncated       bool
}

func (p *projection) snapshot(s protocol.RuntimeSnapshot) {
	p.active = s.ActiveTurnID
	p.activeWatermark = s.DurableWatermark
	p.revision = s.ProjectionRevision
	p.partial = make(map[itemKey]string)
	p.used = 0
	p.truncated = s.ProjectionTruncated
	for _, v := range s.LiveItems {
		cost := room.LiveItemCost(v.ThreadID, v.TurnID, v.ItemID, v.Partial)
		if cost > room.LiveProjectionBudget-p.used {
			p.truncated = true
			break
		}
		p.partial[itemKey{v.ThreadID, v.TurnID, v.ItemID}] = v.Partial
		p.used += cost
	}
}
func (p *projection) remove(k itemKey) {
	if value, ok := p.partial[k]; ok {
		p.used -= room.LiveItemCost(k.thread, k.turn, k.item, value)
		delete(p.partial, k)
	}
}
func (p *projection) transient(e room.TransientEvent, out io.Writer) error {
	if e.Revision <= p.revision {
		return nil
	}
	p.revision = e.Revision
	k := itemKey{string(e.ThreadID), string(e.TurnID), string(e.ItemID)}
	if e.Kind == "projection-truncated" {
		p.truncated = true
		_, err := fmt.Fprintln(out, "[stream truncated; completed output follows]")
		return err
	}
	if e.Kind == "projection-reset" {
		p.truncated = false
		return nil
	}
	if e.Kind != "item-delta" {
		p.remove(k)
		return nil
	}
	if p.truncated {
		return nil
	}
	cost := 6 * len(e.Delta)
	if _, ok := p.partial[k]; !ok {
		cost += room.LiveItemCost(k.thread, k.turn, k.item, "")
	}
	if cost > room.LiveProjectionBudget-p.used {
		p.truncated = true
		_, err := fmt.Fprintln(out, "[stream truncated; completed output follows]")
		return err
	}
	p.used += cost
	p.partial[k] += e.Delta
	_, err := fmt.Fprintf(out, "[partial %s] %s\n", SafeText(k.item), SafeText(e.Delta))
	return err
}
func (p *projection) durable(e room.DurableEvent, out io.Writer) error {
	var payload struct {
		TurnID   string `json:"turn_id"`
		ThreadID string `json:"thread_id"`
		ItemID   string `json:"item_id"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return err
	}
	switch e.Kind {
	case "turn/running":
		if uint64(e.Seq) > p.activeWatermark {
			p.active = payload.TurnID
		}
	case "turn/completed", "turn/failed", "turn/interrupted", "turn/needs-review":
		if uint64(e.Seq) > p.activeWatermark {
			for k := range p.partial {
				if k.turn == payload.TurnID {
					p.remove(k)
				}
			}
			p.truncated = false
		}
		if uint64(e.Seq) > p.activeWatermark && p.active == payload.TurnID {
			p.active = ""
		}
	case "item/completed":
		p.remove(itemKey{payload.ThreadID, payload.TurnID, payload.ItemID})
	}
	_, err := fmt.Fprintf(out, "[%d %s uid=%d] %s\n", e.Seq, SafeText(e.Kind), e.ActorUID, SafeText(string(e.Payload)))
	return err
}
