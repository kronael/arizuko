---
name: weather
description: Advanced weather forecasts using official meteorological sources via browser automation. Use when asked for weather, forecast, or conditions for a specific location/date.
---

## Weather Sources — Best by Region

**Any country:** the national meteorological institute is the most accurate
source. A new region needs fresh research: find its institute first.
For Portugal, start with IPMA; use a marine forecast for coastal wind and waves.

**Europe (general):**
- **yr.no** — free, accurate, ECMWF model
- **meteoblue.com** — multi-model ensemble, hourly detail

**Coastal/Marine:**
- **Windy.com** — visualize multiple models (ECMWF, GFS, ICON)
- **Windguru** — surf/kite/sail forecasts

**Mountain/Ski:**
- **bergfex.com** — Alps, ski resorts
- **mountain-forecast.com** — worldwide peaks

## Process

1. **Identify location** — city/beach/coordinates
2. **Launch agent-browser** — navigate to appropriate source
3. **Extract forecast:**
   - Temperature (high/low)
   - Precipitation (timing, amount in mm)
   - Wind (speed, direction)
   - Cloud cover
   - Sea temp (coastal), wave height (if relevant)
4. **Cross-check** — verify with second source if critical
5. **Report concisely** — temps, rain timing, special conditions

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

Source: [national institute / yr.no / etc.]
```

## NEVER

- Use WebSearch for weather (outdated, wrong year)
- Trust single source for critical decisions
- Report weather without verifying current year
- Guess temperature ranges
- Use consumer weather apps (Weather.com, AccuWeather) — prefer official met institutes
