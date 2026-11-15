package apicompat

import "fmt"

// chatToolSlots binds upstream indices, IDs and unambiguous chunk positions to
// internal slots in first-seen order. Slots never change when an index arrives
// after the ID.
type chatToolSlots struct {
	indices   map[int]int
	ids       map[string]int
	positions map[int]int
	count     int
}

// ResolveChatToolSlot is shared by the two Chat output bridges. Positions are
// fallback evidence only: an explicit new index or ID announces a new call.
func ResolveChatToolSlot(choice, position int, index *int, callID string, slots *chatToolSlots) (int, error) {
	if choice != 0 {
		return 0, fmt.Errorf("unsupported Chat choice index %d", choice)
	}
	if index != nil && *index < 0 {
		return 0, fmt.Errorf("negative Chat tool index %d", *index)
	}
	if slots.indices == nil {
		slots.indices = make(map[int]int)
		slots.ids = make(map[string]int)
		slots.positions = make(map[int]int)
	}
	slot, found := 0, false
	if index != nil {
		slot, found = slots.indices[*index]
	}
	if callID != "" {
		if byID, ok := slots.ids[callID]; ok {
			if found && slot != byID {
				return 0, fmt.Errorf("chat tool index and call ID identify different calls")
			}
			slot, found = byID, true
		}
	}
	if !found {
		if candidate, ok := slots.positions[position]; ok && candidate >= 0 {
			// An unbound index/ID may complete the sole positional identity,
			// but must not overwrite an already bound index/ID.
			boundIndex, boundID := false, false
			for _, v := range slots.indices {
				boundIndex = boundIndex || v == candidate
			}
			for _, v := range slots.ids {
				boundID = boundID || v == candidate
			}
			if (index == nil || !boundIndex) && (callID == "" || !boundID) {
				slot, found = candidate, true
			}
		}
	}
	if !found && index == nil && callID == "" && slots.count > 0 {
		if _, exists := slots.positions[position]; exists {
			return 0, fmt.Errorf("ambiguous Chat tool position %d", position)
		}
	}
	if !found {
		slot = slots.count
		slots.count++
	}
	if index != nil {
		slots.indices[*index] = slot
	}
	if callID != "" {
		for id, existing := range slots.ids {
			if existing == slot && id != callID {
				return 0, fmt.Errorf("chat tool call ID changed")
			}
		}
		slots.ids[callID] = slot
	}
	if old, exists := slots.positions[position]; exists && old != slot {
		slots.positions[position] = -1
	} else if !exists {
		slots.positions[position] = slot
	}
	return slot, nil
}
