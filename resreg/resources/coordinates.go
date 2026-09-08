package resources

// coordinates.go — the byte that joins a composite coordinate, spelled once.
//
// Six of routd's thirteen resources have no single-field identity: acl is the
// six-field grant, acl_membership is (child, parent), network_rules is
// (folder, target), installed_packages is (folder, name), secrets is
// (scope_kind, scope_id, key) and routes is (seq, target, match). Each joins
// its fields into one sort key, and no value of any of them may carry the
// separator — a write that does is refused rather than splitting the key
// somewhere the read would not put it back together (resreg.EntityKeySep).
//
// WHY THIS BYTE. The obvious ones are all in the data. "/" separates a folder
// from its children and a topic from its folder; ":" separates a principal's
// kind from its name and a jid's platform from its address; "=" and ","
// separate the pairs inside an acl row's `params`; "#" opens a topic inside a
// route target. Measured over every one of those fields on all three live
// instances 2026-09-08, the whole character set is space, `#*,-./0-9:=@_`, the
// letters, and one emoji. ASCII 0x1F is the unit separator — the byte whose
// only job is this one — and it is what store/pane_sessions and the engine's
// own composite fixtures already use.
//
// Measuring today's rows is not the guarantee, and it is not meant to be. The
// guarantee is the refusal: a value carrying this byte fails its write.
const KeySep = "\x1f"
