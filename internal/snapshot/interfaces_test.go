package snapshot

import (
	"reflect"
	"testing"
)

// TestInterfaceMethodSets pins the method set of each orchestration interface.
// These interfaces are consumed by stories 005–007; an accidental signature
// change here ripples downstream (risk R-foundation-churn), so the doc test
// fails loudly if a method is renamed, added, or removed.
func TestInterfaceMethodSets(t *testing.T) {
	cases := []struct {
		name    string
		typ     reflect.Type
		methods []string
	}{
		{"Engine", reflect.TypeOf((*Engine)(nil)).Elem(), []string{"Create", "List", "Name", "Prune"}},
		{"Shipper", reflect.TypeOf((*Shipper)(nil)).Elem(), []string{"Name", "Send"}},
		{"Notifier", reflect.TypeOf((*Notifier)(nil)).Elem(), []string{"Notify"}},
		{"Scheduler", reflect.TypeOf((*Scheduler)(nil)).Elem(), []string{"Apply", "Remove"}},
		{"Runner", reflect.TypeOf((*Runner)(nil)).Elem(), []string{"Run"}},
	}

	for _, tc := range cases {
		got := make([]string, tc.typ.NumMethod())
		for i := range got {
			got[i] = tc.typ.Method(i).Name
		}
		// reflect returns methods sorted by name, matching the expected lists.
		if !reflect.DeepEqual(got, tc.methods) {
			t.Errorf("%s method set = %v, want %v", tc.name, got, tc.methods)
		}
	}
}

// TestNoopNotifier confirms the default Notifier accepts a result and reports no
// error (it is the no-op default until story 005).
func TestNoopNotifier(t *testing.T) {
	if err := (noopNotifier{}).Notify(t.Context(), RunResult{}); err != nil {
		t.Errorf("noopNotifier.Notify = %v, want nil", err)
	}
}
