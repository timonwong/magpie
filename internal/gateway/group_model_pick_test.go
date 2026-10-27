package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A model removed from a provider's picked list must not remain a live member
// of a saved routing group.
func TestGroupSkipsModelRemovedFromProviderPicks(t *testing.T) {
	fresh(t)
	a, b := &keyed{}, &keyed{}
	serveOn(t, "a", "ka", []string{"x", "y"}, a)
	serveOn(t, "b", "kb", []string{"x"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/x", "b/x"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModels("a", []string{"y"}); err != nil {
		t.Fatal(err)
	}

	code, body := postAs(t, New(), "", `{"model":"group/mine","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from kb") || len(a.tried) != 0 {
		t.Fatalf("group/mine after a/x was unpicked: %d %s, a tried %v, b tried %v", code, body, a.tried, b.tried)
	}
}
