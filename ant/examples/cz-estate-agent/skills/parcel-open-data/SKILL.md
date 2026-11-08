---
name: parcel-open-data
description: Find a Czech parcel, its neighbours and land type from open CUZK services without a captcha. NOT for owner names or liens (use katastr).
when_to_use: 'parcela, sousední parcely, druh pozemku, RUIAN, WFS, územní plán, zastavitelnost, k.ú.'
---

# Parcel open data

These sources need no browser and meet no challenge. They never give owner names.
Owners, liens and easements come only from `katastr`.

For parcel evidence use [parcels.md](guides/parcels.md). For a pasture expansion study use [pasture-expansion.md](guides/pasture-expansion.md). Record missing results as unknown.

ALWAYS take the parcel from the source document or the map, never from a listing pin or an area match.
ALWAYS check the cadastral territory code (`katastralniuzemi`) in the answer. One municipality can hold several territories.
NEVER state that land is buildable from its land type. Land type is not zoning: ALWAYS read the municipality's zoning plan.

## Parcel and land type (RUIAN ArcGIS, layer 5 = Parcela)

`https://ags.cuzk.gov.cz/arcgis/rest/services/RUIAN/MapServer/5/query?f=json&returnGeometry=false`

- Point: `geometry=<lon>,<lat>&geometryType=esriGeometryPoint&inSR=4326&spatialRel=esriSpatialRelIntersects&outFields=*`
- Neighbourhood: the same with `geometryType=esriGeometryEnvelope` and `geometry=<xmin>,<ymin>,<xmax>,<ymax>`.
- Known numbers: `where=katastralniuzemi=<code> AND cisloparcely IN ('<n>/<m>')&outFields=cisloparcely,vymeraparcely,druhpozemkukod,zpusobyvyuzitipozemku`.

`druhpozemkukod`: 2 arable, 5 garden, 6 orchard, 7 permanent grass, 10 forest, 11 water, 13 built-up, 14 other.

## Boundaries and direct neighbours (INSPIRE WFS)

`https://services.cuzk.cz/wfs/inspire-cp-wfs.asp?service=WFS&version=2.0.0&request=GetFeature&typeNames=CP:CadastralParcel&srsName=EPSG:4326&bbox=<miny>,<minx>,<maxy>,<maxx>,urn:ogc:def:crs:EPSG::4326&count=300`

The service returns coordinates as lon, lat despite the CRS urn. A box of about 150 m around the point is enough.
Parcels whose polygons share a boundary with the target are its direct neighbours.
`cp:areaValue` is the area in m2, `cp:label` the parcel number.

## Zoning plan

1. Find the zoning-plan page of the municipality website (`uzemni-plan`). Download the main drawing, the text part with its justification and the farmland-loss drawing.
2. Convert the PDFs with `pdftotext -layout` and search for the parcel number. The justification and the farmland table name the zone per parcel.
3. A buildable zone often covers only part of a parcel. Record how many m2 are buildable and how many are not.
4. Read the date the change took effect. A consolidated text on the site usually means the change is in force.

Common zones: SV mixed rural residential, BV rural housing, ZZ garden and orchard green, NL forest, DS transport.

## Free map

`https://katastrnemovitosti.cz/mapa/#18/<lat>/<lon>` selects a parcel on click and shows land type, area, BPEJ, official price and owner type (never the name).
