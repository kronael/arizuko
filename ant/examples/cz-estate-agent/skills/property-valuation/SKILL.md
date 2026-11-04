---
name: property-valuation
description: Value a Czech property by components (land, buildings, infrastructure) with cited comparables. NOT for a bank appraisal or legal advice.
when_to_use: 'ocenění, odhad ceny, valuation, comparables, Kč/m2, cena pozemku, nabídková cena'
---

# Property valuation

ALWAYS give every load-bearing number a source: listing URL, sale record or build cost, with its date.
ALWAYS check that the asking price in the listing belongs to this property.
ALWAYS give one headline price band. A second band is allowed only for a really different buyer type, with its premium as an explicit percentage.
ALWAYS widen the band and show sensitivity when the technical state is unknown.

## Method

1. Land: split by parcel and land type, using `parcel-open-data`. Table: type (parcel numbers), m2, Kč/m2, total, rate source.
   The parcel areas must sum to the area in the land register.
2. Land rate: use the micro-market, not the district average. Take at least three comparables from the same market and distance band.
3. NEVER derive a land rate from asking price divided by area for a plot with a house. Subtract the house reproduction value first.
4. Agricultural land (ZPF) takes an agricultural rate, not a building rate.
5. Split by zoning: buildable part, garden and yard, and surplus. Reduce only the surplus that cannot be built.
6. Buildings: reproduction cost minus wear, state from photos, legal status (in the cadastre, permitted, on which land type).
   An unpermitted building takes a deduction. NEVER value one building as both use and demolition.
7. Prefer sale records (realised prices) as anchors. Label asking prices "asking".
8. Total: the component sum, then a fair band below it with named reasons (location, access, networks, liquidity, time on market).
9. List every missing document and its effect on the number in CZK.
10. Quote other appraisals verbatim: author, date, client, and each figure.
