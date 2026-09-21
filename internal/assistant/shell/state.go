package shell

import "encoding/binary"

type Screen uint8

const (
	ScreenHome Screen = iota
	ScreenStatus
	ScreenHelp
	ScreenSettings
	ScreenSettingsCategory
	ScreenSettingDetail
)

const (
	legacyStateBytes = 8
	stateBytes       = 16
	stateVersion     = 1
)

type State struct {
	Screen        Screen
	CategoryIndex uint16
	SettingIndex  uint16
	Refreshes     uint64
}

func InitialState() []byte {
	return EncodeState(State{Screen: ScreenHome})
}

func DecodeState(raw []byte) State {
	if len(raw) == legacyStateBytes {
		return State{Screen: ScreenHome, Refreshes: binary.BigEndian.Uint64(raw)}
	}
	if len(raw) != stateBytes || raw[0] != stateVersion {
		return State{Screen: ScreenHome}
	}
	screen := Screen(raw[1])
	if screen > ScreenSettingDetail {
		screen = ScreenHome
	}
	return State{
		Screen:        screen,
		CategoryIndex: binary.BigEndian.Uint16(raw[2:4]),
		SettingIndex:  binary.BigEndian.Uint16(raw[4:6]),
		Refreshes:     binary.BigEndian.Uint64(raw[8:16]),
	}
}

func EncodeState(state State) []byte {
	raw := make([]byte, stateBytes)
	raw[0] = stateVersion
	raw[1] = byte(state.Screen)
	binary.BigEndian.PutUint16(raw[2:4], state.CategoryIndex)
	binary.BigEndian.PutUint16(raw[4:6], state.SettingIndex)
	binary.BigEndian.PutUint64(raw[8:16], state.Refreshes)
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
	state.Screen = screen
	return EncodeState(state)
}

func StepCategoryState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettings
	state.CategoryIndex = uint16(stepIndex(int(state.CategoryIndex), total, delta))
	state.SettingIndex = 0
	return EncodeState(state)
}

func OpenCategoryState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingsCategory
	state.CategoryIndex = uint16(clampIndex(int(state.CategoryIndex), total))
	state.SettingIndex = 0
	return EncodeState(state)
}

func StepSettingState(raw []byte, total, delta int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingsCategory
	state.SettingIndex = uint16(stepIndex(int(state.SettingIndex), total, delta))
	return EncodeState(state)
}

func OpenSettingState(raw []byte, total int) []byte {
	state := DecodeState(raw)
	state.Screen = ScreenSettingDetail
	state.SettingIndex = uint16(clampIndex(int(state.SettingIndex), total))
	return EncodeState(state)
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
