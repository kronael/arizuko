---
name: obec-profile
description: Profile a Czech municipality for a zoning-change decision from public sources. NOT for personal data of private people.
when_to_use: 'obec, starosta, zastupitelstvo, změna ÚP, volby, zastavitelnost, rezoning'
---

# Municipality profile

Run it for a shortlisted property only, and only when a zoning change or a buildable surplus drives the decision.

1. Identify the municipality and its code from the listing breadcrumb or the land register. NEVER pick one of several same-name municipalities from a geocoder: take the district from the listing.
2. Council: read the council page of the municipality website. Note the mayor, the deputy and the committee chairs.
3. Elections: read the last municipal result on `volby.gov.cz` by municipality code. Note the winning list and its vote share. Verify current officials against council minutes; mark post-election gaps as unknown.
4. Zoning: read the zoning page and the notice board. A running zoning-plan change is an open window. Search the council minutes for "změna ÚP" and "zastavitelná".
5. Local press: note disputes, building stops and blocked projects.

Write the result to the group's research record. Source every number with a URL. Mark gaps as gaps. NEVER invent an election number.
Assess zoning feasibility against the actual plan, its changes and council decisions. Use `uredni-deska` for the full notice-board sweep and `zdroje-review` before delivery. Use `estate-site` for publication.
A host that returns 403 on every path is the egress allowlist, not proof that the data is absent.
