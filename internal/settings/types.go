package settings

// SettingType denotes the data type of a setting value.
type SettingType string

const (
	TypeBool     SettingType = "bool"
	TypeInt      SettingType = "int"
	TypeString   SettingType = "string"
	TypeDuration SettingType = "duration"
	TypeEnum     SettingType = "enum"
)

// SettingScope defines the target context hierarchy.
type SettingScope string

const (
	ScopeGlobal SettingScope = "global"
	ScopeChat   SettingScope = "chat"
	ScopeUser   SettingScope = "user"
)

// Standard category names for UI grouping.
const (
	CategoryGeneral    = "general"
	CategorySecurity   = "security"
	CategoryModeration = "moderation"
	CategoryAutomation = "automation"
	CategoryUI         = "ui"
	CategoryAdvanced   = "advanced"
)

// WidgetType hints how UI should render a setting.
type WidgetType string

const (
	WidgetToggle   WidgetType = "toggle"
	WidgetStepper  WidgetType = "stepper"
	WidgetDuration WidgetType = "duration"
	WidgetSelector WidgetType = "selector"
	WidgetText     WidgetType = "text"
)

// UIHint describes how a setting should be presented in Telegram UI.
type UIHint struct {
	Widget     WidgetType
	Step       int64
	Presets    []string
	Confirm    bool
	Searchable bool
}

// SettingDefinition holds the schema, validation rules, and metadata for a setting.
type SettingDefinition struct {
	Namespace     string
	Key           string
	Type          SettingType
	DefaultValue  string
	AllowedValues []string // For TypeEnum
	MinVal        *int64   // For TypeInt or TypeDuration (seconds)
	MaxVal        *int64   // For TypeInt or TypeDuration (seconds)
	Title         string
	Description   string
	Category      string
	Validator     func(val string) error
	UI            UIHint
	Order         int
	Sensitive     bool
}
