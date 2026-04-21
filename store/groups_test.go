package store

import (
	"reflect"
	"testing"
)

func TestRouteSourceJIDs_RoomOnly(t *testing.T) {
	got := routeSourceJIDs("room=123")
	want := []string{"123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routeSourceJIDs(%q) = %v, want %v", "room=123", got, want)
	}
}

func TestRouteSourceJIDs_PlatformAndRoom(t *testing.T) {
	got := routeSourceJIDs("platform=telegram room=123")
	want := []string{"telegram:123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRouteSourceJIDs_ChatJID(t *testing.T) {
	got := routeSourceJIDs("chat_jid=telegram:123")
	want := []string{"telegram:123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRouteSourceJIDs_GlobSkipped(t *testing.T) {
	got := routeSourceJIDs("platform=telegram room=*")
	if len(got) != 0 {
		t.Fatalf("glob room should be skipped, got %v", got)
	}
}
