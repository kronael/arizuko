# Map Links Guide — mapy.com Format

## CRITICAL FORMAT (USER-TESTED)

```
https://mapy.com/en/turisticka?q=Location%20Name&x=LONGITUDE&y=LATITUDE&z=17
```

**Key Components (ALL MANDATORY):**
- `mapy.com` domain (NOT mapy.cz, NOT en.mapy.cz)
- `/en/turisticka` path
- `q=` location name (URL encoded)
- `&x=` LONGITUDE (negative for West, e.g. -9.14)
- `&y=` LATITUDE (positive for North, e.g. 38.72)
- `&z=17` zoom level

**Working Examples:**
```
https://mapy.com/en/turisticka?q=Restaurant%20Name&x=14.4208&y=50.0875&z=17
https://mapy.com/en/turisticka?q=Attraction%20Name&x=14.4005&y=50.0903&z=17
https://mapy.com/en/turisticka?q=Parking%20Lot&x=14.4170&y=50.0810&z=17
```

## How to Get Coordinates

**Method 1: agent-browser (REQUIRED)**
1. Navigate to https://mapy.com
2. Search: `"Name" + "Address" + "City"`
3. CLICK exact result in search
4. Extract URL from address bar
5. Verify `&x=...&y=...&z=17` present
6. If missing: click location marker to refresh URL

**Method 2: Nominatim (fallback)**
```bash
curl "https://nominatim.openstreetmap.org/search?q=Name+Address+City&format=json&limit=1"
```
Extract `lon` → `x=`, `lat` → `y=`

## Validation Checklist

1. ✅ Domain is `mapy.com`
2. ✅ Path is `/en/turisticka`
3. ✅ Has `&x=` longitude (negative West, positive East)
4. ✅ Has `&y=` latitude (negative South, positive North)
5. ✅ Has `&z=17` zoom
6. ✅ Coordinates 4+ decimal places
7. ✅ Name URL-encoded (spaces → %20)

## What Breaks

- ❌ Search-only: `mapy.com/en/turisticka?q=Name` (no coords)
- ❌ Wrong domain: `en.mapy.cz` or `mapy.cz`
- ❌ Short links: `mapy.cz/s/XXX`
- ❌ Swapped x/y: `x=LAT&y=LONG`

## Google Maps Fallback

**For restaurants/cafes ONLY** if not found on mapy.com:
```
https://www.google.com/maps/search/?api=1&query=LAT,LONG&query_place_id=PLACE_ID
```

Get place_id from Google Maps search result.

NEVER use Google Maps fallback for attractions, parking, viewpoints.

## Service Selection

**Sightseeing/Walking → mapy.com**
- Individual waypoint links
- Opens in mapy.cz mobile app
- Works offline

**Car Routes → Google Maps**
```
https://www.google.com/maps/dir/Start/Waypoint/End
```
Superior traffic data, drive times

**Public Transport → Google Maps**
Real-time schedules, transfers, pricing
