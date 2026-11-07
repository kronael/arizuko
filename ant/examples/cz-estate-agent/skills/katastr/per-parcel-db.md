# Workflow: věrná per-parcel databáze (Kron 20.10.2026, AUTORITATIVNÍ)

Cíl: pro nemovitost/lokalitu vytvořit VĚRNOU databázi po JEDNOTLIVÝCH parcelách —
co parcela je podle katastru A podle územního plánu, kdo ji vlastní, vlastnosti.
NIKDY netvrdit vlastnost parcely z jednoho zdroje (= „náhodné věci").

## ŽELEZNÉ pravidlo
**Katastrální DRUH pozemku ≠ ÚZEMNÍ PLÁN.** Parcela může být v katastru „orná půda",
ale v ÚP „plocha zastavitelná" (→ reálně stavební), nebo naopak. Druh z katastru
SÁM O SOBĚ nestačí k závěru „je/není stavební". VŽDY srovnat s ÚP.

## Postup (přes všechny vrstvy)
1. **Katastrální mapa** — najdi parcely v lokalitě (ČÚZK RUIAN ArcGIS, viz
   `sousedni-parcely.md`; nebo klik v katastrnemovitosti.cz). Pozor: pin inzerátu
   často sedí na cizí parcele (hřiště/cesta) — NEBER ho jako „naši" parcelu.
2. **Územní plán** — otevři ÚP té obce a SROVNEJ ho s katastrální mapou PŘES VRSTVY
   (katastr + ÚP + vlastnictví + sítě ve stejné mapě). Pochop, který pozemek je který.
   ÚP zdroje: katastrnemovitosti.cz vrstva „územní plán"; geoportál obce; krajský
   ÚP viewer; Portál územního plánování. Zapiš ÚP plochu KAŽDÉ parcely (zastavitelná
   SV/BV/…, nezastavitelná, ÚSES, les-LHP…).
3. **Proklikání** — parcelu po parcele si udělej SEZNAM (číslo, výměra, druh katastr, ÚP plocha).
4. **Vlastníci + vlastnosti** — k parcelám dohledej vlastníky (jméno z KN/LV přes
   cuzk tunel; typ z free mapy) + vlastnosti (BPEJ, úřední cena, sítě, záplavy).
5. **Věrná DB** — ulož per-parcel do `~/nemo/parcely/<lokalita>.md`: řádek na parcelu
   `parcela | výměra | druh(KN) | ÚP plocha | vlastník | sítě | pozn.`. Poznač zdroj+datum.
6. **Update → skill** — když se postup změní/vylepší, zapiš to SEM (aby to příště šlo rychleji).

## Anti-vzor (co NEDĚLAT)
- Tvrdit „je to orná / stavební / cizí" jen z katastru bez ÚP vrstvy.
- Brát GPS pin inzerátu jako parcelu.
- Házet do chatu dílčí „náhodné" nálezy místo věrné DB.
