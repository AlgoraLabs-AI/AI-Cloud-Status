package autostart

import "testing"

// fakeRunKey is an in-memory runKey for testing the reconcile logic without a
// real registry.
type fakeRunKey struct {
	values  map[string]string
	sets    int
	deletes int
	getErr  error
}

func newFakeRunKey() *fakeRunKey { return &fakeRunKey{values: map[string]string{}} }

func (f *fakeRunKey) get(name string) (string, bool, error) {
	if f.getErr != nil {
		return "", false, f.getErr
	}
	v, ok := f.values[name]
	return v, ok, nil
}

func (f *fakeRunKey) set(name, value string) error {
	f.values[name] = value
	f.sets++
	return nil
}

func (f *fakeRunKey) delete(name string) error {
	delete(f.values, name)
	f.deletes++
	return nil
}

func (f *fakeRunKey) close() error { return nil }

func TestReconcileEnableRegisters(t *testing.T) {
	rk := newFakeRunKey()
	if err := reconcile(rk, "app", `"C:\app.exe"`, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rk.values["app"] != `"C:\app.exe"` {
		t.Errorf("value = %q, want registered", rk.values["app"])
	}
	if en, _ := isEnabled(rk, "app"); !en {
		t.Error("should be enabled after registering")
	}
}

func TestReconcileEnableIsIdempotent(t *testing.T) {
	rk := newFakeRunKey()
	rk.values["app"] = `"C:\app.exe"`
	if err := reconcile(rk, "app", `"C:\app.exe"`, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rk.sets != 0 {
		t.Errorf("unchanged registration should not re-write (sets=%d)", rk.sets)
	}
}

func TestReconcileEnableUpdatesChangedPath(t *testing.T) {
	rk := newFakeRunKey()
	rk.values["app"] = `"C:\old.exe"`
	if err := reconcile(rk, "app", `"C:\new.exe"`, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rk.values["app"] != `"C:\new.exe"` {
		t.Errorf("value = %q, want updated path", rk.values["app"])
	}
	if rk.sets != 1 {
		t.Errorf("changed path should write once (sets=%d)", rk.sets)
	}
}

func TestReconcileDisableRemoves(t *testing.T) {
	rk := newFakeRunKey()
	rk.values["app"] = `"C:\app.exe"`
	if err := reconcile(rk, "app", `"C:\app.exe"`, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, ok := rk.values["app"]; ok {
		t.Error("entry should be removed when disabled")
	}
	if rk.deletes != 1 {
		t.Errorf("deletes = %d, want 1", rk.deletes)
	}
}

func TestReconcileDisableAbsentIsNoop(t *testing.T) {
	rk := newFakeRunKey()
	if err := reconcile(rk, "app", `"C:\app.exe"`, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rk.deletes != 0 {
		t.Errorf("disabling an absent entry should not delete (deletes=%d)", rk.deletes)
	}
}
