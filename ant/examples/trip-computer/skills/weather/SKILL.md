---
name: weather
description: Forecast for a place and date from official meteorological sources. NOT for building a trip plan (use itinerary).
when_to_use: 'weather, forecast, will it rain, rain timing, wind, temperature, sea temperature, waves, mountain conditions, snow, počasí, předpověď, bude pršet'
---

## Weather Sources — Best by Region

**Any country:** start with the national meteorological institute and
cross-check it with a second source. A new region needs fresh research:
find its institute first. For Portugal, start with IPMA; use a marine
forecast for coastal wind and waves.

**Europe (general):**
- **yr.no** — free, ECMWF model
- **meteoblue.com** — multi-model ensemble, hourly detail

**Coastal/Marine:**
- **Windy.com** — visualize multiple models (ECMWF, GFS, ICON)
- **Windguru** — surf/kite/sail forecasts

**Mountain/Ski:**
- **bergfex.com** — Alps, ski resorts
- **mountain-forecast.com** — worldwide peaks

## Process

1. **Identify location** — city/beach/coordinates and its timezone
2. **Launch agent-browser** — navigate to appropriate source
3. **Extract forecast:**
   - Issue time and the period it is valid for, in the destination's timezone
   - Temperature (high/low)
   - Precipitation (timing, amount in mm)
   - Wind (speed, direction)
   - Cloud cover
   - Sea temp (coastal), wave height (if relevant)
   - Warnings that affect the planned activity (storm, wind, ice, heat)
4. **Cross-check** — verify with second source if critical
5. **Report concisely** — temps, rain timing, special conditions

## Freshness

- ALWAYS check that the date asked for lies inside the provider's
  forecast range. Beyond it, give climate averages labelled as such —
  NEVER hourly conditions for that date.
- When every source is refused or down, ALWAYS reuse only a forecast
  still valid for the date, labelled with its issue time; otherwise
  report the forecast as unavailable and give the exact egress ask
  (`network_allow` for the refused host). NEVER present yesterday's
  expired forecast as today's.

## Output Format

```
Location: [Name]
Date: [YYYY-MM-DD]

Temperature: XX-YY°C (current: ZZ°C)
Conditions: [clear/cloudy/overcast/rain]
Rain: [none / XX:00-YY:00 (Zmm)]
Wind: [speed] km/h [direction]
Sea: [temp]°C [if coastal]
Wave: [height]m [if coastal]
Warnings: [none / what and when]

Source: [national institute / yr.no / etc.], issued [YYYY-MM-DD HH:MM], valid to [YYYY-MM-DD HH:MM]
```

## NEVER

- Use WebSearch for weather (outdated, wrong year)
- Trust single source for critical decisions
- Report weather without verifying current year
- Guess temperature ranges
- Use consumer weather apps (Weather.com, AccuWeather) — prefer official met institutes
