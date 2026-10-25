// Package outcome links an activity to the delivered proposal it carried out.
// The link is structural: the workout the activity recorded must have the
// same steps as the FIT workout coachd encodes for the proposal. It is a pure
// function of stored rows, recomputed on every read, and holds no coaching
// logic.
package outcome

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/fitdata"
	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

// WindowDays is how far, in days either way, a proposal's date may be from
// the date of an activity that carried it out: the runner may move a run.
const WindowDays = 3

// Fingerprint is the fitdata.Fingerprint of the FIT workout coachd delivers
// for p. The steps are decoded back from the encoded bytes, so they are read
// exactly as an activity's recorded steps are.
func Fingerprint(p store.Proposal) (string, error) {
	var w workout.Workout
	if err := json.Unmarshal(p.ContentJSON, &w); err != nil {
		return "", fmt.Errorf("outcome: proposal content: %w", err)
	}
	b, err := fitworkout.Encode(w, p.CreatedAt)
	if err != nil {
		return "", err
	}
	steps, err := fitdata.DecodeWorkout(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	return fitdata.Fingerprint(steps), nil
}

// Linker links activities and delivered proposals through the store.
type Linker struct {
	DB *store.DB
}

// candidate is a delivered proposal with its fingerprint.
type candidate struct {
	p  store.Proposal
	fp string
}

// ProposalFor returns the ID of the delivered proposal activity a carried
// out, or nil.
func (l Linker) ProposalFor(ctx context.Context, a store.ActivityWorkout) (*string, error) {
	cs, err := l.candidates(ctx, a.Date.AddDays(-WindowDays), a.Date.AddDays(WindowDays))
	if err != nil {
		return nil, err
	}
	if p := pick(a, cs); p != nil {
		return &p.ID, nil
	}
	return nil, nil
}

// ActivitiesFor returns, for each delivered proposal in ps, the IDs of the
// activities that carried it out, oldest first. Proposals with none are
// absent. It agrees with ProposalFor.
func (l Linker) ActivitiesFor(ctx context.Context, ps []store.Proposal) (map[string][]int64, error) {
	out := map[string][]int64{}
	want := map[string]bool{}
	var from, to civil.Date
	for _, p := range ps {
		if p.Status != store.ProposalDelivered {
			continue
		}
		if len(want) == 0 || p.Date.Before(from) {
			from = p.Date
		}
		if len(want) == 0 || to.Before(p.Date) {
			to = p.Date
		}
		want[p.ID] = true
	}
	if len(want) == 0 {
		return out, nil
	}
	acts, err := l.DB.ActivityWorkouts(ctx, from.AddDays(-WindowDays), to.AddDays(WindowDays))
	if err != nil {
		return nil, err
	}
	// Every proposal an activity in that range could link to.
	cs, err := l.candidates(ctx, from.AddDays(-2*WindowDays), to.AddDays(2*WindowDays))
	if err != nil {
		return nil, err
	}
	for _, a := range acts {
		if p := pick(a, cs); p != nil && want[p.ID] {
			out[p.ID] = append(out[p.ID], a.ID)
		}
	}
	return out, nil
}

// candidates returns the delivered proposals dated in [from, to]. A proposal
// whose workout no longer encodes links to nothing.
func (l Linker) candidates(ctx context.Context, from, to civil.Date) ([]candidate, error) {
	ps, err := l.DB.ListProposals(ctx, from, to, []store.ProposalStatus{store.ProposalDelivered})
	if err != nil {
		return nil, err
	}
	cs := make([]candidate, 0, len(ps))
	for _, p := range ps {
		if fp, err := Fingerprint(p); err == nil {
			cs = append(cs, candidate{p: p, fp: fp})
		}
	}
	return cs, nil
}

// pick returns the proposal a carried out: among the candidates with a's
// fingerprint dated within WindowDays of a, the nearest date, then the
// earliest creation, then the lowest ID.
func pick(a store.ActivityWorkout, cs []candidate) *store.Proposal {
	var best *store.Proposal
	bestDist := 0
	for i := range cs {
		p := &cs[i].p
		dist := abs(p.Date.Sub(a.Date))
		if cs[i].fp != a.Fingerprint || dist > WindowDays {
			continue
		}
		if best == nil || dist < bestDist ||
			dist == bestDist && (p.CreatedAt.Before(best.CreatedAt) || p.CreatedAt.Equal(best.CreatedAt) && p.ID < best.ID) {
			best, bestDist = p, dist
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
