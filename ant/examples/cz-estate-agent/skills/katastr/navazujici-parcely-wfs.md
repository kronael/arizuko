# Navazující parcely + druh pozemku — BEZ captchy (ČÚZK WFS + RUIAN ArcGIS)

Použij, když chceš u nemovitosti najít PŘESNOU parcelu, PŘÍMÉ sousedy
(navazující pozemky) a DRUH pozemku. Tyto zdroje NEMAJÍ captchu ani bot-wall.
Jen JMÉNA vlastníků jsou blokovaná (viz dole).

## 1. Adresa → bod (RUIAN)
Z inzerátu vezmi ulici + č.p. (sreality `__NEXT_DATA__`: `houseNumber`, `street`,
`latitude`, `longitude`). GPS z inzerátu může být posunuté — ověř bod přes RUIAN
adresní WFS (`inspire-ad-wfs.asp`, typ `AD:Address`, filtruj designator = č.p.).

## 2. Parcely v okolí (ČÚZK INSPIRE WFS — geometrie, výměra)
```
https://services.cuzk.cz/wfs/inspire-cp-wfs.asp?service=WFS&version=2.0.0
 &request=GetFeature&typeNames=CP:CadastralParcel&srsName=EPSG:4326
 &bbox=<miny>,<minx>,<maxy>,<maxx>,urn:ogc:def:crs:EPSG::4326&count=300
```
POZOR na osy: Marushka vrací souřadnice jako **lon lat** (x=lon ~14, y=lat ~50)
i přes urn CRS. `<cp:posList>` = polygon, `<cp:areaValue>` = výměra m²,
`<cp:label>` = číslo parcely, `<cp:nationalCadastralReference>` = `kukod-parcela`.
Bbox ~150 m kolem bodu stačí. Parsuj shapely: bod uvnitř = hostitelská parcela;
`union` parcel nemovitosti, pak `boundary.intersection(...).length > 0` = přímí
sousedé (navazující pozemky). Delší sdílená hranice = větší význam.

## 3. Druh pozemku (RUIAN ArcGIS — BEZ captchy)
```
https://ags.cuzk.gov.cz/arcgis/rest/services/RUIAN/MapServer/5/query?f=json
 &where=katastralniuzemi%3D<kukod>%20AND%20cisloparcely%20IN%20('522/8','567/3')
 &outFields=cisloparcely,vymeraparcely,druhpozemkukod,zpusobyvyuzitipozemku
 &returnGeometry=false
```
Layer 5 = `Parcela`. Pole `druhpozemkukod`. Číselník druhu:
2=orná půda · 5=zahrada · 6=ovocný sad · 7=trvalý travní porost (pastvina/louka)
· 10=lesní pozemek · 11=vodní plocha · 13=zastavěná plocha a nádvoří · 14=ostatní plocha.
`platiod` (epoch ms) = kdy parcela vznikla/změna → vodítko na čerstvou změnu vlastníka.
Pro koně hledej druh 2 (orná → zatravnit) nebo 7 (TTP přímo).

## 4. Jména vlastníků — BLOKER
Jména vlastníků jsou JEN v ČÚZK Nahlížení do KN, které chrání bot-ochrana (HTTP 403 / CAPTCHA).
Postup = `katastr.py` podle `SKILL.md`; u výzvy (CAPTCHA, login) se zastav pro operátora.
Když jméno nejde získat → vytypuj parcely k oslovení (čísla + k.ú.) a jméno nech na Kronovi/RK.

## Ověřený příběh (Přišimasy ID164, 20.10.2026)
Vila Škvorecká 11, k.ú. 736295: st.22 (1793) + 34/3 (2109 zahrada) = 3902 m² (= inzerát).
Navazující: 522/8 orná 5,5 ha (★ koňsky použitelná) + 567/3 orná 6127 m²; zbytek
voda/cesty/ostatní. Jména vlastníků blokována bot-wallem + guest down.

## 5. ÚP plocha (zastavitelnost) — DRUH POZEMKU ≠ ÚZEMNÍ PLÁN (Kron 20.10.2026)
KATASTRÁLNÍ druh pozemku (orná/TTP/les) NENÍ totéž co ÚZEMNÍ PLÁN (funkční plocha).
Parcela může být v katastru `orná půda` a přitom v ÚP `SV – smíšené obytné venkovské`
= ZASTAVITELNÁ. "Stavební pozemek" v inzerátu = tvrzení o ÚP, ne o katastru.
NIKDY neodhaduj zastavitelnost z druhu pozemku. VŽDY otevři ÚP.

Postup (bez captchy, host curl):
1. Obec web má ÚP v `file_storage`. Najdi stránku: `curl obec.cz/` → hledej href `uzemni-plan`.
2. Stáhni `?page=all`, vytáhni dvojice (název dokumentu → `/modules/file_storage/download.php?file=...`).
   Ber: HLAVNÍ VÝKRES, TEXTOVÁ ČÁST / ODŮVODNĚNÍ, a VÝKRES ZÁBORŮ ZPF.
3. `pdftotext -layout x.pdf - | grep "<č.parcely>"`. Odůvodnění/výrok změny i záborová
   tabulka uvádějí parcelu → plochu (SV/BV/ZZ/AL…) + rozlohu lokality v ha.
4. POZOR: zastavitelná plocha často pokrývá JEN ČÁST parcely (lokalita Z.xx v ha ≠ výměra
   parcely). Zapiš kolik m² je SV a kolik zůstává nezastavitelné.
5. Stav změny: hledej "nabývá účinnosti / vydává / opatření obecné povahy" + datum KÚ
   stanoviska. Úplné znění (ÚZ) na webu = změna je zpravidla vydaná. Ověř datum účinnosti.

Číselník ÚP ploch (časté): SV smíšené obytné venkovské · BV bydlení venkovské ·
ZZ zeleň zahradní a sadová · AL/TTP travní · AP orná · NL les · DS/PU dopravní.
