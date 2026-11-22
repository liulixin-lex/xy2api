package resilience

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type CollabLocalEndingMode string

const (
	CollabLocalEndingOff    CollabLocalEndingMode = "off"
	CollabLocalEndingGroups CollabLocalEndingMode = "groups"
	CollabLocalEndingAll    CollabLocalEndingMode = "all"
)

type CollabSettings struct {
	IncrementalDeclarations bool                      `json:"incremental_declarations"`
	Records                 bool                      `json:"records"`
	TreeAffinity            bool                      `json:"tree_affinity"`
	LosslessFirst           bool                      `json:"lossless_first"`
	Ladder                  bool                      `json:"ladder"`
	LocalEnding             CollabLocalEndingSettings `json:"local_ending"`
	RecordRouting           bool                      `json:"record_routing"`
	StrictMarkerVeto        bool                      `json:"strict_marker_veto"`
}

type CollabLocalEndingSettings struct {
	Mode   CollabLocalEndingMode `json:"mode"`
	Groups []int64               `json:"groups"`
}

func DefaultCollabSettings() CollabSettings {
	return CollabSettings{Records: true, LocalEnding: CollabLocalEndingSettings{Mode: CollabLocalEndingOff, Groups: []int64{}}}
}

// Stored documents may come from a newer version. Invalid collaboration values
// reset only that field, never the rest of the resilience document. Admin PUT
// validates separately before decoding over the current settings.
func (s *CollabSettings) UnmarshalJSON(data []byte) error {
	defaults := DefaultCollabSettings()
	if !collabStoredObject(data, func(name string, raw json.RawMessage) {
		var target *bool
		var fallback bool
		switch strings.ToLower(name) {
		case "incremental_declarations":
			target = &s.IncrementalDeclarations
		case "records":
			target, fallback = &s.Records, defaults.Records
		case "tree_affinity":
			target = &s.TreeAffinity
		case "ladder":
			target = &s.Ladder
		case "lossless_first":
			target = &s.LosslessFirst
		case "record_routing":
			target = &s.RecordRouting
		case "strict_marker_veto":
			target = &s.StrictMarkerVeto
		case "local_ending":
			_ = s.LocalEnding.UnmarshalJSON(raw)
			return
		default:
			collabSafety.settingsCorrections.Add(1)
			return
		}
		var value bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			value = fallback
			collabSafety.settingsCorrections.Add(1)
		}
		*target = value
	}) {
		*s = defaults
		collabSafety.settingsCorrections.Add(1)
	}
	return nil
}

func (s *CollabLocalEndingSettings) UnmarshalJSON(data []byte) error {
	if !collabStoredObject(data, func(name string, raw json.RawMessage) {
		switch strings.ToLower(name) {
		case "mode":
			var mode CollabLocalEndingMode
			if json.Unmarshal(raw, &mode) != nil || !ValidCollabLocalEndingMode(mode) {
				mode = CollabLocalEndingOff
				collabSafety.settingsCorrections.Add(1)
			}
			s.Mode = mode
		case "groups":
			var groups []int64
			valid := collabRejectNull(raw) == nil && json.Unmarshal(raw, &groups) == nil
			for _, group := range groups {
				valid = valid && group > 0
			}
			if !valid {
				groups = []int64{}
				collabSafety.settingsCorrections.Add(1)
			}
			s.Groups = groups
		default:
			collabSafety.settingsCorrections.Add(1)
		}
	}) {
		*s = DefaultCollabSettings().LocalEnding
		collabSafety.settingsCorrections.Add(1)
	}
	return nil
}

// Iterate in document order, including repeated/case-insensitive fields.
func collabStoredObject(data []byte, field func(string, json.RawMessage)) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return false
		}
		name, ok := token.(string)
		if !ok {
			return false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return false
		}
		field(name, raw)
	}
	return true
}

// CheckCollabSettingsJSON is for explicit admin input only. The plain types
// intentionally bypass tolerant stored-document unmarshalling.
func CheckCollabSettingsJSON(data []byte) error {
	if err := collabRejectNull(data); err != nil {
		return err
	}
	type local struct {
		Mode   CollabLocalEndingMode `json:"mode"`
		Groups []int64               `json:"groups"`
	}
	type strict struct {
		IncrementalDeclarations bool  `json:"incremental_declarations"`
		Records                 bool  `json:"records"`
		TreeAffinity            bool  `json:"tree_affinity"`
		Ladder                  bool  `json:"ladder"`
		LosslessFirst           bool  `json:"lossless_first"`
		LocalEnding             local `json:"local_ending"`
		RecordRouting           bool  `json:"record_routing"`
		StrictMarkerVeto        bool  `json:"strict_marker_veto"`
	}
	var value strict
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var problem error
	if !collabStoredObject(data, func(name string, raw json.RawMessage) {
		if !strings.EqualFold(name, "local_ending") {
			return
		}
		if !collabStoredObject(raw, func(name string, raw json.RawMessage) {
			if strings.EqualFold(name, "mode") {
				var mode CollabLocalEndingMode
				if json.Unmarshal(raw, &mode) != nil || !ValidCollabLocalEndingMode(mode) {
					problem = fmt.Errorf("collab.local_ending.mode must be off, groups or all")
				}
			}
			if strings.EqualFold(name, "groups") {
				var groups []int64
				if json.Unmarshal(raw, &groups) != nil {
					problem = fmt.Errorf("collab.local_ending.groups must be integers")
				}
				for _, group := range groups {
					if group <= 0 {
						problem = fmt.Errorf("collab.local_ending.groups must be positive")
					}
				}
			}
		}) {
			problem = fmt.Errorf("collab.local_ending must be an object")
		}
	}) {
		return fmt.Errorf("collab must be an object")
	}
	return problem
}

func collabRejectNull(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("collab fields must not be null")
		}
	}
}

func ValidCollabLocalEndingMode(mode CollabLocalEndingMode) bool {
	return mode == CollabLocalEndingOff || mode == CollabLocalEndingGroups || mode == CollabLocalEndingAll
}

func (s *CollabSettings) normalize() {
	if s.LocalEnding.Mode == "" {
		s.LocalEnding.Mode = CollabLocalEndingOff
	}
	s.LocalEnding.Groups = append([]int64{}, s.LocalEnding.Groups...)
	slices.Sort(s.LocalEnding.Groups)
	s.LocalEnding.Groups = slices.Compact(s.LocalEnding.Groups)
}

func (s *Settings) CollabIncrementalDeclarationsOn() bool {
	return s.master() && s.Collab.IncrementalDeclarations
}
func (s *Settings) CollabRecordsOn() bool          { return s.master() && s.Collab.Records }
func (s *Settings) CollabTreeAffinityOn() bool     { return s.master() && s.Collab.TreeAffinity }
func (s *Settings) CollabLadderOn() bool           { return s.master() && s.Collab.Ladder }
func (s *Settings) CollabLosslessFirstOn() bool    { return s.CollabLadderOn() && s.Collab.LosslessFirst }
func (s *Settings) CollabRecordRoutingOn() bool    { return s.CollabLadderOn() && s.Collab.RecordRouting }
func (s *Settings) CollabStrictMarkerVetoOn() bool { return s.master() && s.Collab.StrictMarkerVeto }
func (s *Settings) CollabLocalEndingOn(groupID int64) bool {
	if !s.CollabLadderOn() {
		return false
	}
	switch s.Collab.LocalEnding.Mode {
	case CollabLocalEndingAll:
		return true
	case CollabLocalEndingGroups:
		return slices.Contains(s.Collab.LocalEnding.Groups, groupID)
	default:
		return false
	}
}

// Only these two pre-failure routing changes require lossless-first. Ordering
// failover candidates retains the existing record-routing gate.
func (s *Settings) CollabLeaseMigrationSuppressionOn() bool {
	return s.CollabTreeAffinityOn() && s.CollabLosslessFirstOn()
}
func (s *Settings) CollabRedispatchBorrowOn() bool {
	return s.CollabRecordRoutingOn() && s.CollabLosslessFirstOn()
}
