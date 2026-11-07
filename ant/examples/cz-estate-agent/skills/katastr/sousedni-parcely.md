# Workflow: sousední parcely + druh/ÚP/úřední cena (free mapa)

Zdroj: `katastrnemovitosti.cz` — bezplatná SPA mapa nad daty ČÚZK. BEZ captchy,
BEZ placení. Dává vše kromě JMÉNA vlastníka (jen typ vlastníka). Detail viz
`facts/katastrnemovitosti-cz.md`.

Použij pro: druh pozemku, výměra, ÚP (zastavitelnost), BPEJ + **úřední cena půdy**
(= kotva pro land rate v ocenění), záplavy, vzdálenost k sítím, budovy, a hlavně
mapování SOUSEDNÍCH parcel pro pacht/odkup.

## Kroky (přes agent-browser skill)

1. GPS nemovitosti vezmi z inzerátu (ne z geokodéru středu obce).
2. Otevři `https://katastrnemovitosti.cz/mapa/#18/<lat>/<lon>` → klik do mapy vybere parcelu.
3. Pro cílovou parcelu zapiš: výměra, druh, využití, ÚP plocha, BPEJ, úřední cena, záplavy, sítě.
4. **Sousední parcely**: klikej okolní parcely kolem cílové. Pro každou zapiš
   druh (pole/TTP/les/zahrada/stavební), výměru, typ vlastníka (soukromý/podílový/obec/stát).
5. Vrstvy: `?vrstva=vlastnictvi`, Záplavy (MŽP), Inženýrské sítě (DMVS), LPIS.

## Kategorizace k pachtu/odkupu (Kron 15.10.)

Pro koňský areál — jsou okolní pozemky dostupné? Každému sousedovi přiřaď:

| Kategorie | Kdy |
| --- | --- |
| **ano** | soukromý vlastník, zemědělský druh (TTP/orná/pastvina), přímo navazuje, bez zástavy |
| **možná** | soukromý, ale les/zahrada nebo oddělený cestou; nutno ověřit ochotu |
| **spíš ne** | obec/stát (SPÚ) — jde to, ale úřední proces; nebo stavební pozemek (drahý) |
| **ne** | cizí areál, zástavba, ÚP chrání (ÚSES, les-LHP) |

Jméno vlastníka tady NEDOSTANEŠ (jen typ). Pro jméno z LV → `SKILL.md` §Vlastníci (guest `katastr.py`).

## Výstup

Ulož do reportu nemovitosti (`~/nemo/valuations/<ID>-...md`): tabulka cílová +
sousední parcely se sloupci parcela | druh | výměra | typ vlastníka | ÚP | kategorie | poznámka.
Úřední cenu předej do ocenění jako land-rate kotvu (ne jako tržní cenu).

## RYCHLÁ CESTA: ČÚZK RUIAN ArcGIS REST (parcely+druh+výměra, BEZ captchy)

Ověřeno 20.10.2026. Programový zdroj parcel — číslo, výměra, druh, k.ú. — bez captchy,
JSON. NEDÁ jméno vlastníka (to jen přes guest `katastr.py` — viz `SKILL.md` §Vlastníci).

Služba: `https://ags.cuzk.gov.cz/arcgis/rest/services/RUIAN/MapServer`
vrstva **5 = Parcela** (polygony), 0 = ParcelaDefinicniBod.

Parcela pod bodem (GPS inzerátu — POZOR, pin často sedí na obecním hřišti/cestě):
```
.../RUIAN/MapServer/5/query?geometry=<lon>,<lat>&geometryType=esriGeometryPoint&inSR=4326&spatialRel=esriSpatialRelIntersects&outFields=*&returnGeometry=false&f=json
```
Sousedi v okolí (envelope xmin,ymin,xmax,ymax ve WGS84):
```
.../5/query?geometry=<xmin>,<ymin>,<xmax>,<ymax>&geometryType=esriGeometryEnvelope&inSR=4326&spatialRel=esriSpatialRelIntersects&outFields=*&returnGeometry=false&f=json
```
Najít KANDIDÁTA parcely dle výměry (jen když pin selže) — POTVRĎ proti LV, NIKDY neber parcelu jen dle shody plochy:
```
.../5/query?where=katastralniuzemi=<kod>+AND+vymeraparcely>=X+AND+vymeraparcely<=Y&outFields=cisloparcely,vymeraparcely,druhpozemkukod&returnGeometry=true&outSR=4326&f=json
```
Služby celé ČR: `https://ags.cuzk.gov.cz/arcgis/rest/services?f=json`.

**druhpozemkukod**: 2=orná · 5=zahrada · 6=ovocný sad · 7=TTP(louka/pastvina) ·
10=les · 11=vodní · 13=zastavěná plocha · 14=ostatní plocha. `zpusobyvyuzitipozemku`
dodá podtyp (sportoviště, atd.).

POZN.: `katastralniuzemi` kód je v odpovědi — ověř že odpovídá cílovému k.ú.
(Senešnice 747424 ≠ Bojanovice 609722 — různá k.ú. ve stejné obci!).
