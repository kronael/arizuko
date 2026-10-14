package compose

import "testing"

func TestEngagementTTLReachesRoutd(t *testing.T) {
	for _, value := range []string{"0", "90s"} {
		t.Run(value, func(t *testing.T) {
			dir := seed(t, "ENGAGEMENT_TTL="+value+"\n")
			gen(t, dir)
			got := readEnvFileKey(dir+"/env/routd.env", "ENGAGEMENT_TTL")
			if got != value {
				t.Fatalf("generated routd ENGAGEMENT_TTL=%q want %q", got, value)
			}
			if other := readEnvFileKey(dir+"/env/runed.env", "ENGAGEMENT_TTL"); other != "" {
				t.Fatalf("engagement TTL leaked into runed: %q", other)
			}
		})
	}
}
