# Workflow: okolní pozemky k prodeji + pachtu (okolo jedné nemovitosti)

Cíl: pro JEDNU nemovitost zjistit, co je kolem ní k PRODEJI (trh) a k PACHTU/prodeji
od státu (SPÚ). Pro koňský areál = hledej pastviny (TTP), louky, orná, zemědělské pozemky.

**NAVAZUJÍCÍ NAPŘED (Kron 20.10.):** priorita = parcely, co PŘÍMO sousedí s cílovou
(sdílí hranici) → rozšíření areálu. Teprve pak blízké v koridoru. Zjisti nejdřív
PŘESNOU parcelu cílové nemovitosti (parc. č. z LV/katastru, ne jen GPS pin inzerátu —
ten často sedí na obecním rybníku/komunikaci), pak její přímé sousedy a jejich druh
+ typ vlastníka (soukromý = oslovitelný; obec/stát = jinou cestou).

Vstup: GPS nemovitosti + obec + okres + k.ú. (z inzerátu/LV). PSČ nejbližšího města.

## 1) Trh — Bazoš reality (curl, rychlé)

Endpoint (SSR, dá se číst curlem):
```
https://reality.bazos.cz/prodam/pozemek/?hledat=<dotaz>&hlokalita=<PSC>&humkreis=<km>
```
- `hlokalita` = PSČ středu hledání (např. Štěchovice `25207`), `humkreis` = poloměr km (20).
- `hledat` = klíčové slovo: `pastvina`, `louka`, `zemědělský pozemek`, `pole`.
- Parsuj: `curl -s -A Mozilla/5.0 "<url>" | sed 's/<[^>]*>/ /g' | grep -iE 'm2|Kč|pastvin|louka|BPEJ'`.
- POZOR na směr: 20 km poloměr chytí i druhou stranu (Velké Popovice vs Mníšek). Ber jen
  obce ve stejném koridoru jako cílová nemovitost; zbytek vyřaď.
- Sreality je JS (SSR `__NEXT_DATA__`), pro rychlý sken stačí Bazoš.

## 2) Stát — SPÚ „pachtovní seznam" + prodej (agent-browser, JS formulář)

Web: `https://spu.gov.cz/nabidky` (dřív spucr.cz → 307 redirect). Je to kaskádový
formulář (jQuery), NE curl. Řídí se přes `agent-browser` + `eval` (dispatch change):

Selecty (id): `typ_nabidky` → `datum` → `okres` → `ku` (plní se kaskádově).

```bash
agent-browser open "https://spu.gov.cz/nabidky"
# typ: N = pacht/pronájem zeměd.věcí (pachtovní seznam); paragraf7 = prodej §12/503-2012
agent-browser eval "(()=>{const t=document.getElementById('typ_nabidky');t.value='N';t.dispatchEvent(new Event('change',{bubbles:true}));return 1})()"
# datum: vyber aktuální termín (value je číslo, text '... - aktuální')
agent-browser eval "(()=>{const d=document.getElementById('datum');d.value='<ID>';d.dispatchEvent(new Event('change',{bubbles:true}));return 1})()"
# okres: hodnota = text, např 'Praha západ'
agent-browser eval "(()=>{const o=document.getElementById('okres');o.value='Praha západ';o.dispatchEvent(new Event('change',{bubbles:true}));return 1})()"
# ku: seznam obsahuje JEN k.ú. s živou nabídkou; vyber relevantní
agent-browser eval "(()=>{const k=document.getElementById('ku');const x=[...k.options].find(o=>o.text==='Masečín');k.value=x.value;k.dispatchEvent(new Event('change',{bubbles:true}));return 1})()"
# řádky: tabulka se sloupci Parcela | Výměra | Cena | Nájem | Druh | Využití
agent-browser eval "(()=>{let o=[];document.querySelectorAll('tr').forEach(r=>{const t=r.innerText.replace(/\s+/g,' ').trim();if(t&&/\d/.test(t)&&t.length>8&&!/Vyberte/.test(t))o.push(t)});return o.join('\n')})()"
```

- Vždy mezi kroky `sleep 3` (AJAX). Pokud `okres`/`ku` prázdný → datum ještě nedoběhl.
- `ku` dropdown = seznam katastrů, kde SPÚ REÁLNĚ něco nabízí. Chybí-li cílové k.ú.,
  stát tam teď nic nenabízí → ber sousední k.ú. ve stejném koridoru.
- `Druh = trvalý travní porost` = PASTVINA → pro koně ideál. `orná půda` = pole.
- `paragraf7` (prodej) bývá jen „neaktuální" staré termíny = žádný aktivní prodej státní půdy.

## 3) Katastr souseda (jen když potřeba vlastník/typ)

Druh + typ vlastníka sousedních parcel → `sousedni-parcely.md` (free mapa).
Jméno vlastníka pro oslovení → `SKILL.md` (guest `katastr.py`, Nahlížení; u výzvy stop pro operátora).

## Výstup

Report `~/nemo/<ID>-okolni-pozemky-<datum>.md`: trh (bazos, jen koridor) + SPÚ pacht
(k.ú. | parcela | výměra | druh) + prodej stav. Pastviny/TTP zvýrazni.
