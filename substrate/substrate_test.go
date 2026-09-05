package substrate

import (
	"reflect"
	"testing"
)

// The declaration must not be able to carry policy: every Profile field is a
// bool or an enum this package declares. A bare string could hold a folder, a
// func an allowlist resolver, a slice a rule set — all arizuko's, none the
// substrate's. A comment would ask for this; the test refuses it.
func TestProfileCarriesNoPolicy(t *testing.T) {
	typ := reflect.TypeOf(Profile{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		enum := f.Type.Kind() == reflect.String && f.Type.PkgPath() == typ.PkgPath()
		if f.Type.Kind() != reflect.Bool && !enum {
			t.Errorf("Profile.%s is %s — a declaration field must be a bool or an enum declared in package substrate",
				f.Name, f.Type)
		}
	}
}
