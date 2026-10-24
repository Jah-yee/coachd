package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

// upcomingDays is how far ahead the home page lists approved workouts
// (approved or on their way to the watch: store.DesiredStatuses).
const upcomingDays = 14

// Every date a proposal can have.
var (
	firstDate = civil.Date{Year: 1, Month: time.January, Day: 1}
	lastDate  = civil.Date{Year: 9999, Month: time.December, Day: 31}
)

// homePage is the data of home.html. Name, goal name, target and notes are
// agent-written: the template shows them escaped and labelled.
type homePage struct {
	NoPasskey bool
	Apps      bool // builtin mode: link the Connected apps page
	Pending   []proposalRow
	Upcoming  []proposalRow
	Goal      *goalView
}

type proposalRow struct {
	ID, Day, Name, Status string
	Warnings              int
}

type goalView struct {
	Name, Day, Sport, Distance, Elevation, Target, Notes string
}

// day formats a date as "Fri 23 Oct".
func day(d civil.Date) string {
	return time.Date(d.Year, d.Month, d.Day, 12, 0, 0, 0, time.UTC).Format("Mon 2 Jan")
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := func() (homePage, error) {
		p := homePage{Apps: s.d.Builtin}
		has, err := s.hasPasskey(ctx)
		if err != nil {
			return p, err
		}
		p.NoPasskey = !has
		pending, err := s.d.DB.ListProposals(ctx, firstDate, lastDate, []store.ProposalStatus{store.ProposalPending})
		if err != nil {
			return p, err
		}
		if p.Pending, err = proposalRows(pending); err != nil {
			return p, err
		}
		today := civil.Of(s.d.Now(), s.d.TZ)
		upcoming, err := s.d.DB.ListProposals(ctx, today, today.AddDays(upcomingDays), store.DesiredStatuses)
		if err != nil {
			return p, err
		}
		if p.Upcoming, err = proposalRows(upcoming); err != nil {
			return p, err
		}
		g, err := s.d.DB.ActiveGoal(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return p, nil
		}
		if err != nil {
			return p, err
		}
		p.Goal = newGoalView(g)
		return p, nil
	}()
	if err != nil {
		s.internalError(w, r, err, false)
		return
	}
	s.render(w, r, http.StatusOK, "home", page)
}

func proposalRows(ps []store.Proposal) ([]proposalRow, error) {
	out := make([]proposalRow, 0, len(ps))
	for _, p := range ps {
		var warnings []json.RawMessage
		if len(p.WarningsJSON) > 0 {
			if err := json.Unmarshal(p.WarningsJSON, &warnings); err != nil {
				return nil, fmt.Errorf("web: proposal %s warnings: %w", p.ID, err)
			}
		}
		out = append(out, proposalRow{ID: p.ID, Day: day(p.Date), Name: p.Name, Status: string(p.Status), Warnings: len(warnings)})
	}
	return out, nil
}

func newGoalView(g store.Goal) *goalView {
	v := &goalView{Name: g.Name, Day: day(g.Date) + " " + strconv.Itoa(g.Date.Year),
		Sport: strings.ReplaceAll(string(g.Sport), "_", " "), Target: g.Target, Notes: g.Notes}
	if g.DistanceKm != nil {
		v.Distance = strconv.FormatFloat(*g.DistanceKm, 'f', -1, 64) + " km"
	}
	if g.ElevationGainM != nil {
		v.Elevation = strconv.FormatFloat(*g.ElevationGainM, 'f', 0, 64) + " m climb"
	}
	return v
}
