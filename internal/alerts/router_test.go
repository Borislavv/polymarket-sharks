package alerts

import (
	"testing"
)

// AlertingEnabled switch behaviour is covered end-to-end in the integration
// test TestWorker_AlertingDisabledStillPersists below (build tag integration).
// This unit-level placeholder ensures the field default is set by NewRouter
// so a missing wiring does not silently kill alerts in prod.
func TestRouter_AlertingEnabledByDefault(t *testing.T) {
	r := NewRouter(nil, nil, DefaultLinks(), nil, "a", "b", "c", "d")
	if !r.AlertingEnabled {
		t.Fatalf("NewRouter must set AlertingEnabled=true by default")
	}
}
