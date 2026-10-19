package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ServedHere holds only for a magpie gateway that says it keeps this
// process's folder: a CLI leaves its plugins to that one (plugin.
// LeaveToServer), never to a magpie keeping another folder (a sandbox, a
// container, another user's) or to one that doesn't say, whose plugins'
// providers aren't the ones on this folder's disk.
func TestServedHereOnlyForThisFolder(t *testing.T) {
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(gw.URL, "http://"))
	if !ServedHere() {
		t.Fatal("ServedHere = false with this folder's gateway answering")
	}

	for name, body := range map[string]string{
		"another folder's":     `{"name":"magpie","version":"dev","folder":"0123456789ab"}`,
		"one that doesn't say": `{"name":"magpie","version":"0.1.1290"}`,
		"not magpie":           `{"name":"nginx"}`,
	} {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(other.URL, "http://"))
		if ServedHere() {
			t.Errorf("ServedHere = true with %s gateway answering", name)
		}
		other.Close()
	}

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:1") // nothing answers
	if ServedHere() {
		t.Error("ServedHere = true with nothing answering")
	}
}

// The folder a gateway keeps is told to this machine only: a request
// from the network learns nothing of it.
func TestInfoTellsTheFolderOnlyHere(t *testing.T) {
	h := New().Handler()
	for _, c := range []struct {
		remote string
		want   bool
	}{{"127.0.0.1:50000", true}, {"192.0.2.7:50000", false}} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatalf("%s: %d %s", c.remote, rec.Code, rec.Body)
		}
		if _, told := info["folder"]; told != c.want {
			t.Errorf("a request from %s was told the folder: %v, want %v (%s)", c.remote, told, c.want, rec.Body)
		}
	}
}
