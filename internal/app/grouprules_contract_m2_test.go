package app

import (
	"testing"

	"github.com/inipew/goultroid/internal/assistant/grouprules"
	"github.com/inipew/goultroid/plugins/blacklist"
	"github.com/inipew/goultroid/plugins/filters"
)

var (
	_ grouprules.Source = (*blacklist.Plugin)(nil)
	_ grouprules.Source = (*filters.Plugin)(nil)
)

func TestP7IGroupRulePluginsExposeCanonicalSource(t *testing.T) {
	t.Parallel()
}
