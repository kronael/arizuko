# Checklist

Twelve items. Walk through before declaring any chart done.

## Data

- [ ] Is the data sorted by the value the reader cares about (not alphabetical, not as-input)?
- [ ] Are all data points visible, or is the chart hiding outliers behind summary lines?
- [ ] If categories were grouped into "Other", is that group small enough not to mislead?

## Honesty

- [ ] Lie factor between 0.95 and 1.05? (Visual change matches numerical change.)
- [ ] Bar charts start at zero?
- [ ] Truncated line-chart axes have the baseline labeled?
- [ ] No area or volume encoding a single number?

## Ink

- [ ] Gridlines either light gray or absent?
- [ ] No chart border / frame box (range frame only where needed)?
- [ ] No redundant encoding (bar + number + label + axis tick all saying the same thing)?
- [ ] One accent color, used for focal data only?

## Labels

- [ ] Direct labels on data, not a separate legend (unless ≥ 5 categories)?
- [ ] Numbers formatted readably (4.2M not 4,200,000)?
