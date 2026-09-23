package settings

import "testing"

func TestDefaultAssistantLocaleDefinitionIsCanonicalEnum(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	def, version, ok := reg.GetVersioned("ui", "locale")
	if !ok || def == nil || version == 0 {
		t.Fatal("ui:locale definition is missing")
	}
	if def.Type != TypeEnum || def.DefaultValue != "en" {
		t.Fatalf("ui:locale type/default=%q/%q", def.Type, def.DefaultValue)
	}
	if len(def.AllowedValues) != 2 || def.AllowedValues[0] != "en" || def.AllowedValues[1] != "id" {
		t.Fatalf("ui:locale allowed values=%v", def.AllowedValues)
	}
	if got, err := def.Canonicalize("ID"); err != nil || got != "id" {
		t.Fatalf("canonicalize ID=%q err=%v", got, err)
	}
	if _, err := def.Canonicalize("fr"); err == nil {
		t.Fatal("unsupported Assistant locale unexpectedly accepted")
	}
}
