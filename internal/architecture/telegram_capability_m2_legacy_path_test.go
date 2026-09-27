package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM2AppDoesNotUseLegacyTelegramAggregates(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "app", "app.go"),
		filepath.Join("internal", "app", "wiring_services.go"),
		filepath.Join("internal", "app", "selfinline.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			".client.Service()",
			".dispatcher.Service()",
			".dispatcher.CommandService()",
			"client.Service()",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s still uses legacy Telegram aggregate path %q", rel, forbidden)
			}
		}
	}
}
