package memory

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Invariant struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	Rule      string    `json:"rule"`
	Reason    string    `json:"reason"`
	Status    string    `json:"status"` // active, disabled, proposed, rejected
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type InvariantEvent struct {
	Action string    `json:"action"`
	Rule   Invariant `json:"rule"`
	At     time.Time `json:"at"`
}
type InvariantSet struct {
	Version int              `json:"version"`
	Items   []Invariant      `json:"items"`
	Events  []InvariantEvent `json:"events"`
}
type InvariantCommand struct {
	TaskID    string    `json:"taskId"`
	ProfileID string    `json:"profileId"`
	Version   int       `json:"version"`
	Action    string    `json:"action"`
	Rule      Invariant `json:"rule"`
}

func emptyInvariants() InvariantSet {
	return InvariantSet{Version: 1, Items: []Invariant{}, Events: []InvariantEvent{}}
}
func (s InvariantSet) Active() []Invariant {
	result := []Invariant{}
	for _, r := range s.Items {
		if r.Status == "active" {
			result = append(result, r)
		}
	}
	return result
}
func validInvariant(r *Invariant) error {
	r.Title = strings.TrimSpace(r.Title)
	r.Rule = strings.TrimSpace(r.Rule)
	r.Reason = strings.TrimSpace(r.Reason)
	if r.Category != "architecture" && r.Category != "decision" && r.Category != "stack" && r.Category != "business" && r.Category != "other" {
		return fmt.Errorf("%w: выберите категорию", ErrInvalid)
	}
	if r.Title == "" || r.Rule == "" || utf8.RuneCountInString(r.Title) > 100 || utf8.RuneCountInString(r.Rule) > 2000 || utf8.RuneCountInString(r.Reason) > 1000 {
		return fmt.Errorf("%w: название до 100, правило до 2000, обоснование до 1000 символов", ErrInvalid)
	}
	return nil
}
func (s *Store) UpdateInvariants(owner string, cmd InvariantCommand) (State, error) {
	return s.update(owner, func(state *State) error {
		set := &state.Invariants
		if state.Task.ID != cmd.TaskID || set.Version != cmd.Version {
			return ErrConflict
		}
		index := -1
		for i, r := range set.Items {
			if r.ID == cmd.Rule.ID {
				index = i
				break
			}
		}
		r := cmd.Rule
		switch cmd.Action {
		case "create":
			if cmd.Rule.ID != "" {
				return ErrInvalid
			}
			if len(set.Items) >= 50 {
				return fmt.Errorf("%w: не более 50 правил на задачу", ErrInvalid)
			}
			if err := validInvariant(&r); err != nil {
				return err
			}
			id, err := newID()
			if err != nil {
				return err
			}
			r.ID = id
			r.Status = "active"
			r.Version = 1
		case "edit", "activate", "disable", "reject":
			if index < 0 {
				return ErrConflict
			}
			old := set.Items[index]
			if cmd.Action == "edit" {
				if err := validInvariant(&r); err != nil {
					return err
				}
				r.Status = old.Status
			} else {
				r = old
				switch cmd.Action {
				case "activate":
					if old.Status != "disabled" && old.Status != "proposed" {
						return ErrConflict
					}
					r.Status = "active"
				case "disable":
					if old.Status != "active" {
						return ErrConflict
					}
					r.Status = "disabled"
				case "reject":
					if old.Status != "proposed" {
						return ErrConflict
					}
					r.Status = "rejected"
				}
			}
			r.Version = old.Version + 1
		default:
			return ErrInvalid
		}
		r.UpdatedAt = time.Now().UTC()
		if index < 0 {
			set.Items = append(set.Items, r)
		} else {
			set.Items[index] = r
		}
		set.Version++
		set.Events = append(set.Events, InvariantEvent{Action: cmd.Action, Rule: r, At: r.UpdatedAt})
		if len(set.Events) > 200 {
			set.Events = set.Events[len(set.Events)-200:]
		}
		// A changed rule set invalidates old chat requests and workflow suggestions.
		state.Task.Workflow.Version++
		state.Task.Workflow.Proposal = nil
		return nil
	})
}
func (s *Store) ProposeInvariants(owner, taskID string, proposals []Invariant) (State, error) {
	return s.update(owner, func(state *State) error {
		if state.Task.ID != taskID {
			return ErrConflict
		}
		if len(proposals) > 3 {
			return ErrInvalid
		}
		for _, r := range proposals {
			if err := validInvariant(&r); err != nil {
				return err
			}
			duplicate := false
			for _, old := range state.Invariants.Items {
				if old.Rule == r.Rule || old.Title == r.Title {
					duplicate = true
				}
			}
			if duplicate {
				continue
			}
			if len(state.Invariants.Items) >= 50 {
				break
			}
			id, err := newID()
			if err != nil {
				return err
			}
			r.ID = id
			r.Status = "proposed"
			r.Version = 1
			r.UpdatedAt = time.Now().UTC()
			state.Invariants.Items = append(state.Invariants.Items, r)
			state.Invariants.Version++
		}
		return nil
	})
}
