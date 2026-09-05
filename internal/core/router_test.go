package core

import (
	"reflect"
	"testing"
)

func TestRouter_Parse(t *testing.T) {
	r := NewRouter(".")

	tests := []struct {
		input       string
		wantOK      bool
		wantName    string
		wantArgs    []string
		wantRawArgs string
	}{
		{
			input:       ".ping",
			wantOK:      true,
			wantName:    "ping",
			wantArgs:    []string{},
			wantRawArgs: "",
		},
		{
			input:       ".PING",
			wantOK:      true,
			wantName:    "ping",
			wantArgs:    []string{},
			wantRawArgs: "",
		},
		{
			input:       "  .ping   hello   world  ",
			wantOK:      true,
			wantName:    "ping",
			wantArgs:    []string{"hello", "world"},
			wantRawArgs: "hello   world",
		},
		{
			input:       `.cmd "hello world" foo 'bar baz'`,
			wantOK:      true,
			wantName:    "cmd",
			wantArgs:    []string{"hello world", "foo", "bar baz"},
			wantRawArgs: `"hello world" foo 'bar baz'`,
		},
		{
			input:       `.echo "escaped \"quotes\"" test`,
			wantOK:      true,
			wantName:    "echo",
			wantArgs:    []string{`escaped "quotes"`, "test"},
			wantRawArgs: `"escaped \"quotes\"" test`,
		},
		{
			input:  "hello world",
			wantOK: false,
		},
		{
			input:  ".",
			wantOK: false,
		},
		{
			input:  " . ",
			wantOK: false,
		},
		{
			input:  "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		parsed, ok, err := r.Parse(tt.input)
		if err != nil {
			t.Errorf("unexpected error for %q: %v", tt.input, err)
			continue
		}
		if ok != tt.wantOK {
			t.Errorf("Parse(%q) ok = %v, wantOK = %v", tt.input, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if parsed.Name != tt.wantName {
			t.Errorf("Parse(%q) Name = %s, wantName = %s", tt.input, parsed.Name, tt.wantName)
		}
		if !reflect.DeepEqual(parsed.Args, tt.wantArgs) {
			t.Errorf("Parse(%q) Args = %v, wantArgs = %v", tt.input, parsed.Args, tt.wantArgs)
		}
		if parsed.RawArgs != tt.wantRawArgs {
			t.Errorf("Parse(%q) RawArgs = %q, wantRawArgs = %q", tt.input, parsed.RawArgs, tt.wantRawArgs)
		}
	}
}

func TestRouter_SyntaxErrors(t *testing.T) {
	r := NewRouter(".")

	// Unclosed quotes
	_, ok, err := r.Parse(`.exec "echo hello`)
	if ok || err != ErrUnclosedQuote {
		t.Errorf("expected ErrUnclosedQuote, got ok=%v, err=%v", ok, err)
	}

	_, ok, err = r.Parse(`.cmd 'single quote unclosed`)
	if ok || err != ErrUnclosedQuote {
		t.Errorf("expected ErrUnclosedQuote, got ok=%v, err=%v", ok, err)
	}

	// Trailing backslash escape
	_, ok, err = r.Parse(`.cmd hello\`)
	if ok || err != ErrTrailingEscape {
		t.Errorf("expected ErrTrailingEscape, got ok=%v, err=%v", ok, err)
	}
}

func TestRouter_CustomPrefix(t *testing.T) {
	r := NewRouter("!")
	if r.Prefix() != "!" {
		t.Errorf("expected prefix !, got %s", r.Prefix())
	}

	if _, ok, _ := r.Parse(".ping"); ok {
		t.Errorf("did not expect .ping to match with ! prefix")
	}

	parsed, ok, err := r.Parse("!ping")
	if err != nil || !ok || parsed.Name != "ping" {
		t.Errorf("expected !ping to match, got ok=%v, parsed=%v, err=%v", ok, parsed, err)
	}

	// Default prefix when empty string passed
	rDefault := NewRouter("")
	if rDefault.Prefix() != "." {
		t.Errorf("expected default prefix ., got %s", rDefault.Prefix())
	}
}

func TestRouter_RegisterAndFind(t *testing.T) {
	r := NewRouter(".")

	pingCmd := Command{
		Name:        "ping",
		Aliases:     []string{"p", "latency"},
		Description: "Check latency",
		Category:    "Utility",
	}

	if err := r.Register(pingCmd); err != nil {
		t.Fatalf("unexpected error registering ping: %v", err)
	}

	// Find by canonical name
	cmd, exists := r.Find("ping")
	if !exists || cmd.Name != "ping" {
		t.Errorf("failed to find ping by name")
	}

	// Find by uppercase name
	cmd, exists = r.Find("PING")
	if !exists || cmd.Name != "ping" {
		t.Errorf("failed to find ping by uppercase name")
	}

	// Find by alias
	cmd, exists = r.Find("p")
	if !exists || cmd.Name != "ping" {
		t.Errorf("failed to find ping by alias 'p'")
	}

	cmd, exists = r.Find("LATENCY")
	if !exists || cmd.Name != "ping" {
		t.Errorf("failed to find ping by uppercase alias 'LATENCY'")
	}

	// Non-existent command
	_, exists = r.Find("unknown")
	if exists {
		t.Errorf("expected unknown command to not exist")
	}

	// Check All()
	all := r.All()
	if len(all) != 1 {
		t.Errorf("expected 1 command in All(), got %d", len(all))
	}
}

func TestRouter_RegisterConflicts(t *testing.T) {
	r := NewRouter(".")

	cmd1 := Command{
		Name:    "test",
		Aliases: []string{"t"},
	}
	if err := r.Register(cmd1); err != nil {
		t.Fatalf("failed to register cmd1: %v", err)
	}

	// Empty name
	if err := r.Register(Command{Name: ""}); err == nil {
		t.Errorf("expected error registering empty command name")
	}

	// Duplicate canonical name
	if err := r.Register(Command{Name: "TEST"}); err == nil {
		t.Errorf("expected error registering duplicate command name")
	}

	// Duplicate alias matching existing name
	if err := r.Register(Command{Name: "other", Aliases: []string{"test"}}); err == nil {
		t.Errorf("expected error registering alias conflicting with existing name")
	}

	// Duplicate alias matching existing alias
	if err := r.Register(Command{Name: "other", Aliases: []string{"t"}}); err == nil {
		t.Errorf("expected error registering alias conflicting with existing alias")
	}
}
