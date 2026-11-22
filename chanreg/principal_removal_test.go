package chanreg

import "testing"

func TestPrincipalSurvivesAccountRemoval(t *testing.T) {
	for _, removed := range []string{"teled-a", "teled-b"} {
		t.Run(removed, func(t *testing.T) {
			reg := New()
			for _, name := range []string{"teled-a", "teled-b"} {
				_, err := reg.RegisterWithOrigin(name, "http://127.0.0.1:8080", []string{"telegram:"}, nil, "127.0.0.1", "service:teled")
				if err != nil {
					t.Fatal(err)
				}
			}
			reg.Deregister(removed)
			got := reg.ByPrincipal("service:teled")
			if got == nil || got.Name == removed {
				t.Fatalf("principal lookup = %v", got)
			}
			reg.Deregister(got.Name)
			if got := reg.ByPrincipal("service:teled"); got != nil {
				t.Fatalf("stale principal = %v", got)
			}
		})
	}
}
