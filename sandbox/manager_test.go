package sandbox

import "testing"

func TestCreateOpts_BrowserField(t *testing.T) {
	yes := true
	no := false
	if (CreateOpts{}).Browser != nil {
		t.Fatal("zero-value CreateOpts.Browser should be nil (manager default)")
	}
	if got := (CreateOpts{Browser: &yes}).Browser; got == nil || !*got {
		t.Fatalf("Browser=&true not preserved, got %v", got)
	}
	if got := (CreateOpts{Browser: &no}).Browser; got == nil || *got {
		t.Fatalf("Browser=&false not preserved, got %v", got)
	}
}
