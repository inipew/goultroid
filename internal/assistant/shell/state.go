package shell

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

type Screen uint8

const (
	ScreenHome Screen = iota
	ScreenStatus
	ScreenHelp
	ScreenSettings
	ScreenSettingsCategory
	ScreenSettingDetail
	ScreenSettingInput
	ScreenLanguage
)

const (
	v0StateBytes = 8
	v1StateBytes = 16
	v2StateBytes = 32
	stateBytes   = 40
	stateVersion = 3
	bindingBytes = 16
)

type State struct {
	Screen         Screen
	CategoryIndex  uint16
	SettingIndex   uint16
	Refreshes      uint64
	SettingBinding [bindingBytes]byte
	SchemaVersion  uint64
}

func InitialState() []byte {
	return EncodeState(State{Screen: ScreenHome})
}

func DecodeState(raw []byte) State {
	if len(raw) == v0StateBytes {
		return State{Screen: ScreenHome, Refreshes: binary.BigEndian.Uint64(raw)}
	}
	if len(raw) == v1StateBytes && raw[0] == 1 {
		return State{
			Screen:        normalizeScreen(Screen(raw[1])),
			CategoryIndex: binary.BigEndian.Uint16(raw[2:4]),
			SettingIndex:  binary.BigEndian.Uint16(raw[4:6]),
			Refreshes:     binary.BigEndian.Uint64(raw[8:16]),
		}
	}
	if len(raw) == v2StateBytes && raw[0] == 2 {
		state := State{
			Screen:        normalizeScreen(Screen(raw[1])),
			CategoryIndex: binary.BigEndian.Uint16(raw[2:4]),
			SettingIndex:  binary.BigEndian.Uint16(raw[4:6]),
			Refreshes:     binary.BigEndian.Uint64(raw[8:16]),
		}
		copy(state.SettingBinding[:], raw[16:32])
		return state
	}
	if len(raw) != stateBytes || raw[0] != stateVersion {
		return State{Screen: ScreenHome}
	}
	state := State{
		Screen:        normalizeScreen(Screen(raw[1])),
		CategoryIndex: binary.BigEndian.Uint16(raw[2:4]),
		SettingIndex:  binary.BigEndian.Uint16(raw[4:6]),
		Refreshes:     binary.BigEndian.Uint64(raw[8:16]),
		SchemaVersion: binary.BigEndian.Uint64(raw[32:40]),
	}
	copy(state.SettingBinding[:], raw[16:32])
	return state
}

func normalizeScreen(screen Screen) Screen {
	if screen > ScreenLanguage {
		return ScreenHome
	}
	return screen
}

func EncodeState(state State) []byte {
	raw := make([]byte, stateBytes)
	raw[0] = stateVersion
	raw[1] = byte(normalizeScreen(state.Screen))
	binary.BigEndian.PutUint16(raw[2:4], state.CategoryIndex)
	binary.BigEndian.PutUint16(raw[4:6], state.SettingIndex)
	binary.BigEndian.PutUint64(raw[8:16], state.Refreshes)
	copy(raw[16:32], state.SettingBinding[:])
	binary.BigEndian.PutUint64(raw[32:40], state.SchemaVersion)
	return raw
}

func RefreshCount(raw []byte) uint64 {
	return DecodeState(raw).Refreshes
}

func NextRefreshState(raw []byte) []byte {
	state := DecodeState(raw)
	state.Refreshes++
	return EncodeState(state)
}

func ScreenState(raw []byte, screen Screen) []byte {
	state := DecodeState(raw)
	state.Screen = normalizeScreen(screen)
	if state.Screen != ScreenSettingDetail && state.Screen != ScreenSettingInput {
		clearSettingBinding(&state)
	}
	return EncodeState(state)
}

func StepCategoryState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettings
	state.CategoryIndex = uint16(stepIndex(int(state.CategoryIndex), total, delta))
	state.SettingIndex = 0
	clearSettingBinding(&state)
	return EncodeState(state)
}

func OpenCategoryState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingsCategory
	state.CategoryIndex = uint16(clampIndex(int(state.CategoryIndex), total))
	state.SettingIndex = 0
	clearSettingBinding(&state)
	return EncodeState(state)
}

func StepSettingState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingsCategory
	state.SettingIndex = uint16(stepIndex(int(state.SettingIndex), total, delta))
	clearSettingBinding(&state)
	return EncodeState(state)
}

func OpenSettingState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingDetail
	state.SettingIndex = uint16(clampIndex(int(state.SettingIndex), total))
	clearSettingBinding(&state)
	return EncodeState(state)
}

func BeginSettingInputState(raw []byte) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingInput
	return EncodeState(state)
}

func CompleteSettingInputState(raw []byte) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingDetail
	return EncodeState(state)
}

func BindSettingState(raw []byte, namespace, key string, schemaVersion uint64) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingDetail
	state.SettingBinding = SettingBinding(namespace, key)
	state.SchemaVersion = schemaVersion
	return EncodeState(state)
}

func SettingBinding(namespace, key string) [bindingBytes]byte {
	identity := strings.ToLower(strings.TrimSpace(namespace)) + "\x00" + strings.ToLower(strings.TrimSpace(key))
	sum := sha256.Sum256([]byte(identity))
	var binding [bindingBytes]byte
	copy(binding[:], sum[:bindingBytes])
	return binding
}

func SettingBindingMatches(raw []byte, namespace, key string) bool {
	state := DecodeState(raw)
	if state.SettingBinding == ([bindingBytes]byte{}) {
		return false
	}
	return state.SettingBinding == SettingBinding(namespace, key)
}

func clearSettingBinding(state *State) {
	if state == nil {
		return
	}
	state.SettingBinding = [bindingBytes]byte{}
	state.SchemaVersion = 0
}

func clampIndex(index, total int) int {
	if total <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= total {
		return total - 1
	}
	return index
}

func stepIndex(index, total, delta int) int {
	if total <= 0 {
		return 0
	}
	index = clampIndex(index, total)
	next := (index + delta) % total
	if next < 0 {
		next += total
	}
	return next
}
