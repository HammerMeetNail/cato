package v1

import (
	"fmt"
	"net/http"
	"testing"
)

func TestImportEndpoints(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "imp")
	st := c.json("GET", "/api/v1/import", nil, http.StatusOK)
	if st["steam_configured"] != false || len(st["presets"].([]any)) == 0 {
		t.Fatalf("status %v", st)
	}
	c.json("POST", "/api/v1/import/steam/preview", map[string]any{"profile": "gaben"}, http.StatusServiceUnavailable)

	csv := "Title,Status,Rating,Hours\nHades,Beaten,5,20\nCeleste,Want to play,,\nNo Such Game At All,Playing,,\n"
	prev := c.json("POST", "/api/v1/import/csv/preview", map[string]any{"csv": csv}, http.StatusOK)
	items := prev["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("preview %v", prev)
	}
	first := items[0].(map[string]any)
	if first["state"] != "matched" || first["match"].(map[string]any)["id"] != float64(1) {
		t.Fatalf("first row %v", first)
	}
	var entries []map[string]any
	for _, it := range items {
		m := it.(map[string]any)
		if m["match"] != nil {
			entries = append(entries, map[string]any{"game_id": m["match"].(map[string]any)["id"], "row": m["row"]})
		}
	}
	res := c.json("POST", "/api/v1/import/commit", map[string]any{"source": "csv", "entries": entries}, http.StatusOK)
	if res["added"] != float64(2) {
		t.Fatalf("commit %v", res)
	}
	item := c.json("GET", "/api/v1/library/1", nil, http.StatusOK)
	if item["status"] != "completed" || item["rating"] != float64(100) || item["minutes"] != float64(1200) {
		t.Fatalf("imported item %v", item)
	}
	c.json("POST", "/api/v1/import/commit", map[string]any{"source": "nope", "entries": entries}, http.StatusBadRequest)
	batches := c.json("GET", "/api/v1/import", nil, http.StatusOK)["batches"].([]any)
	id := batches[0].(map[string]any)["id"].(string)
	c.json("POST", fmt.Sprintf("/api/v1/import/batches/%s/undo", id), nil, http.StatusOK)
	c.json("GET", "/api/v1/library/1", nil, http.StatusNotFound)
	c.json("POST", fmt.Sprintf("/api/v1/import/batches/%s/undo", id), nil, http.StatusNotFound)

	// Someone else's batch is not found.
	other := f.user(t, "other")
	res = c.json("POST", "/api/v1/import/commit", map[string]any{"source": "csv", "entries": entries}, http.StatusOK)
	other.json("POST", fmt.Sprintf("/api/v1/import/batches/%s/undo", res["batch_id"]), nil, http.StatusNotFound)

	// JSON export → import into another account.
	rec := c.do("GET", "/api/v1/export.json", nil)
	prevJSON := other.json("POST", "/api/v1/import/json/preview", map[string]any{"data": rec.Body.String()}, http.StatusOK)
	if n := len(prevJSON["items"].([]any)); n != 2 {
		t.Fatalf("json preview items %d", n)
	}
	other.json("POST", "/api/v1/import/json/preview", map[string]any{"data": `{"hello":1}`}, http.StatusBadRequest)
}

func TestPushAndPasskeyEndpoints(t *testing.T) {
	f := newFixture(t)
	c := f.user(t, "pk")
	st := c.json("GET", "/api/v1/push", nil, http.StatusOK)
	if k, _ := st["public_key"].(string); len(k) != 87 {
		t.Fatalf("VAPID public key %q", k)
	}
	c.json("POST", "/api/v1/push/subscriptions", map[string]any{"endpoint": "http://insecure/x", "keys": map[string]string{"p256dh": "a", "auth": "b"}}, http.StatusBadRequest)
	// httptest requests have Host "example.com": passkeys are available.
	pk := c.json("GET", "/api/v1/passkeys", nil, http.StatusOK)
	if pk["available"] != true {
		t.Fatalf("passkeys %v", pk)
	}
	opts := c.json("POST", "/api/v1/passkeys/begin", nil, http.StatusOK)["publicKey"].(map[string]any)
	if opts["rp"].(map[string]any)["id"] != "example.com" || opts["attestation"] != "none" {
		t.Fatalf("options %v", opts)
	}
	c.json("POST", "/api/v1/passkeys", map[string]any{"response": map[string]any{"clientDataJSON": "e30", "attestationObject": "oA"}}, http.StatusBadRequest)
	c.json("GET", "/api/v1/barcode/not-a-code", nil, http.StatusBadRequest)
}
