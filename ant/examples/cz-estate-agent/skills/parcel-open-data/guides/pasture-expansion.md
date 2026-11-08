# Pasture expansion

Identify every subject parcel and its cadastral territory from a reliable source. Do not infer the whole property from one point. Search adjacent parcels and nearby land across roads. Check land type, ownership type, access and zoning separately; none proves availability for purchase or lease. Check LPIS and state offers, then verify current rights with the responsible source. An absent or failed response is unknown.

ALWAYS distinguish neighbouring land from land offered for purchase or lease. Different LV numbers do not prove different owners. An LPIS user is not necessarily the legal owner. Verify owner identity through `katastr`; record willingness to sell or lease only from an offer or an owner's response.

```sh
uv run --python 3.14 --with requests --with shapely --with pillow python ~/.claude/skills/parcel-open-data/scripts/pasture_expand.py --id LISTING --gps LAT,LON --ku TERRITORY --subject PARCELS --out OUTPUT_BASE
```

The command writes Markdown and JSON at the explicit output base. It stops if any subject parcel is missing. It does not identify owners or establish that land is available.
