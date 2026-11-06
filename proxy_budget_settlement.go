package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"sync"
)

// Completed calls replace their admission reservation with provider-reported cost.
// Missing usage or interrupted responses retain their reservation: billing is unknown.
type workflowCostReservation struct {
	path, rig, workflow string
	units               int64
	mu                  sync.Mutex
	settled             bool
}

func (r *workflowCostReservation) settleCost(cost float64) error {
	if r == nil {
		return nil
	}
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) || cost > float64(math.MaxInt64)/1e6 {
		return fmt.Errorf("invalid provider cost")
	}
	actual := int64(math.Ceil(cost * 1e6))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled {
		return nil
	}
	err := updateBudgetLedger(r.path, func(ledger map[string]int64) error {
		for _, key := range []string{budgetKey(r.rig, r.workflow), budgetKey(r.rig)} {
			if ledger[key] < r.units {
				return fmt.Errorf("execution budget: reservation missing during settlement")
			}
		}
		for _, key := range []string{budgetKey(r.rig, r.workflow), budgetKey(r.rig)} {
			ledger[key] = ledger[key] - r.units + actual
		}
		return nil
	})
	if err == nil {
		r.settled = true
	}
	return err
}

func (r *workflowCostReservation) settleResponse(body []byte) {
	if r == nil {
		return
	}
	var response struct {
		Usage struct {
			Cost *float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &response) != nil || response.Usage.Cost == nil {
		return
	}
	if err := r.settleCost(*response.Usage.Cost); err != nil {
		log.Printf("[BUDGET] cost settlement failed for %s/%s: %v", r.rig, r.workflow, err)
	}
}

// Observe SSE usage without changing streamed bytes or buffering the full answer.
type budgetUsageBody struct {
	io.ReadCloser
	reservation *workflowCostReservation
	line        []byte
	oversized   bool
}

func (b *budgetUsageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	for _, c := range p[:n] {
		if c == '\n' {
			if !b.oversized {
				line := bytes.TrimSpace(b.line)
				if bytes.HasPrefix(line, []byte("data:")) {
					b.reservation.settleResponse(bytes.TrimSpace(line[5:]))
				}
			}
			b.line = nil
			b.oversized = false
		} else if len(b.line) < 1<<20 && !b.oversized {
			b.line = append(b.line, c)
		} else {
			b.line = nil
			b.oversized = true
		}
	}
	if err == io.EOF && len(b.line) > 0 && !b.oversized {
		line := bytes.TrimSpace(b.line)
		if bytes.HasPrefix(line, []byte("data:")) {
			b.reservation.settleResponse(bytes.TrimSpace(line[5:]))
		}
		b.line = nil
	}
	return n, err
}
