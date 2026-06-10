package auth

import (
	"net/http"
)

// ProxydTransitSub is the JWT subject of the service token proxyd attaches to
// every request it forwards (spec 5/1). proxyd is the sole web ingress: it
// strips client-supplied identity headers, authenticates the user, then
// re-stamps X-User-* / X-Chat-* and proves the channel with this token. A
// backend trusts those stamped headers ONLY on this proof.
const ProxydTransitSub = "service:proxyd"

// ProxydTransit reports whether r carries a valid authd-minted ES256 bearer
// minted FOR proxyd (typ=service, sub=service:proxyd). VerifyHTTP checks
// signature/expiry/issuer; this adds the subject pin that makes the bearer a
// genuine "came through proxyd" transit proof. The pin is load-bearing: without
// it ANY holder of a valid authd token reaching a backend directly could forge
// X-User-* and be treated as that end-user. ks==nil (AUTHD_URL unset, local dev)
// always returns false — callers gate the open-for-local-dev path on ks==nil
// separately, never on a missing secret.
func ProxydTransit(r *http.Request, ks *KeySet) bool {
	if ks == nil {
		return false
	}
	sub, err := VerifyHTTP(r, ks)
	return err == nil && sub.Typ == "service" && sub.Sub == ProxydTransitSub
}
