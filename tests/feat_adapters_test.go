package tests

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/tests/testutils"
)

func TestFeature_ChannelAdapters(t *testing.T) {
	t.Run("capability-surface", func(t *testing.T) {
		fc := testutils.NewFakeChannel("tg", "telegram:")
		if !fc.Owns("telegram:42") || fc.Owns("slack:1") {
			t.Fatalf("Owns prefix routing wrong")
		}
		if _, err := fc.Send("telegram:42", "hi", "", "", "", "turn-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := fc.SendFile("telegram:42", "/p", "n", "cap", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := fc.SendVoice("telegram:42", "/v.ogg", "cap", ""); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := fc.Like(ctx, "telegram:42", "m1", "👍"); err != nil {
			t.Fatal(err)
		}
		if err := fc.Delete(ctx, "telegram:42", "m1"); err != nil {
			t.Fatal(err)
		}
		if _, err := fc.Post(ctx, "telegram:42", "post body", nil); err != nil {
			t.Fatal(err)
		}
		_ = fc.Typing("telegram:42", true)
		if len(fc.Sent()) != 1 {
			t.Fatalf("sent = %d, want 1", len(fc.Sent()))
		}
		if len(fc.SentFiles) != 1 || len(fc.SentVoices) != 1 {
			t.Fatalf("file=%d voice=%d, want 1,1", len(fc.SentFiles), len(fc.SentVoices))
		}
		if len(fc.Reactions) != 1 || len(fc.Deletes) != 1 || len(fc.Posts) != 1 {
			t.Fatalf("reactions=%d deletes=%d posts=%d, want 1,1,1",
				len(fc.Reactions), len(fc.Deletes), len(fc.Posts))
		}
		if fc.TypingCalls != 1 {
			t.Fatalf("typing calls = %d, want 1", fc.TypingCalls)
		}
	})
}
