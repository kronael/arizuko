package chanstore_test

// `file_urls` bodies: the mapping an adapter's `/files/<id>` proxy resolves.
// The guarantee is that it OUTLIVES the process that wrote it — the adapter may
// restart between publishing an attachment and routd's enrich downloading it
// (BUGS L163).

import (
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
)

func TestFileURLRoundTrips(t *testing.T) {
	st := newStore(t)
	const url = "https://cdn.discordapp.com/attachments/1/2/cat.png?ex=abc"

	if err := st.RecordFileURL(t.Context(), "aabbccddeeff", url); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, held, err := st.FileURL(t.Context(), "aabbccddeeff")
	if err != nil || !held {
		t.Fatalf("read back = (%q, %v, %v)", got, held, err)
	}
	if got != url {
		t.Errorf("url = %q, want %q", got, url)
	}
}

// An id nothing recorded is reported as a miss rather than raised: it is the
// proxy's 404, which is a normal answer to a request that invented an id.
func TestFileURLUnknownIDIsAMiss(t *testing.T) {
	st := newStore(t)
	got, held, err := st.FileURL(t.Context(), "000000000000")
	if err != nil {
		t.Fatalf("an unrecorded id must miss, not fail: %v", err)
	}
	if held {
		t.Errorf("an unrecorded id resolved to %q", got)
	}
}

// The write is unconditional, and it has to be: the id is derived from the URL,
// so an adapter that publishes the same attachment twice records the same
// mapping twice. A refusal there would be an outcome the caller has to
// classify, for a write that changes nothing.
func TestFileURLRecordIsRepeatable(t *testing.T) {
	st := newStore(t)
	const url = "https://files.slack.com/files-pri/T1-F1/photo.jpg"
	for range 2 {
		if err := st.RecordFileURL(t.Context(), "112233445566", url); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	got, held, err := st.FileURL(t.Context(), "112233445566")
	if err != nil || !held || got != url {
		t.Fatalf("after two records the mapping reads as (%q, %v, %v)", got, held, err)
	}
}

// Two ids are two items. A coordinate that co-located them would let one
// attachment's mapping answer for another's.
func TestFileURLIDsAreIndependent(t *testing.T) {
	st := newStore(t)
	if err := st.RecordFileURL(t.Context(), "aaaaaaaaaaaa", "https://a.example/a.png"); err != nil {
		t.Fatalf("record a: %v", err)
	}
	if err := st.RecordFileURL(t.Context(), "bbbbbbbbbbbb", "https://b.example/b.png"); err != nil {
		t.Fatalf("record b: %v", err)
	}
	for id, want := range map[string]string{
		"aaaaaaaaaaaa": "https://a.example/a.png",
		"bbbbbbbbbbbb": "https://b.example/b.png",
	} {
		got, held, err := st.FileURL(t.Context(), id)
		if err != nil || !held || got != want {
			t.Errorf("%s = (%q, %v, %v), want %q", id, got, held, err, want)
		}
	}
}

// The entity set is what the opener makes before an adapter boots, so an entity
// nothing declares is a table no deployment creates and every read refuses.
func TestFileURLIsDeclared(t *testing.T) {
	for _, e := range chanstore.Entities {
		if e == chanstore.FileURL {
			return
		}
	}
	t.Fatalf("chanstore.Entities does not declare %q, so no adapter would have the table",
		chanstore.FileURL)
}
