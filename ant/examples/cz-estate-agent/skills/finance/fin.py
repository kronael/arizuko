#!/usr/bin/env python3
"""Financial calculator — hypotéky, LTV, DSTI, výnosy, čas.hodnota peněz.
Spuštění: uv run --python 3.14 --with numpy-financial python3 ~/.claude/skills/finance/fin.py <příkaz> ...
Sazby zadávej v % (4.19 = 4,19 %).
"""
import sys, numpy_financial as npf

def anu(P, r, n): return -npf.pmt(r/12, n, P)

def cmd_splatka(a):
    "splatka <jistina> <roky> <sazba%> [další sazby%] — anuitní splátka + přeplacení"
    P=float(a[0]); n=int(float(a[1])*12); rates=[float(x)/100 for x in a[2:]]
    if not rates: raise ValueError("sazba je povinná")
    print(f"jistina {P/1e6:.2f}M, {n//12} let ({n} splátek)")
    for r in rates:
        pmt=anu(P,r,n); tot=pmt*n
        print(f"{r*100:5.2f}%  {pmt:10,.0f} Kč/měs   celkem {tot/1e6:.2f}M  úrok {(tot-P)/1e6:.2f}M")

def cmd_zbyva(a):
    "zbyva <jistina> <roky> <sazba%> <splaceno_mesicu> — zůstatek jistiny + kolik splaceno"
    P=float(a[0]); n=int(float(a[1])*12); r=float(a[2])/100; k=int(a[3]); i=r/12
    bal=P*((1+i)**n-(1+i)**k)/((1+i)**n-1)
    print(f"po {k} splátkách: zůstatek jistiny {bal:,.0f} Kč, splaceno {P-bal:,.0f} Kč")

def cmd_ltv(a):
    "ltv <uver> <hodnota_nemovitosti> — loan-to-value %"
    u,h=float(a[0]),float(a[1]); print(f"LTV = {u/h*100:.1f} %  (úvěr {u/1e6:.2f}M / hodnota {h/1e6:.2f}M)")

def cmd_dsti(a):
    "dsti <mesicni_splatka> <cisty_mesicni_prijem> — debt-service-to-income %"
    s,p=float(a[0]),float(a[1]); print(f"DSTI = {s/p*100:.1f} %  (splátka {s:,.0f} / příjem {p:,.0f})")

def cmd_vynos(a):
    "vynos <rocni_najem> <cena> — hrubý nájemní výnos %"
    n,c=float(a[0]),float(a[1]); print(f"hrubý výnos = {n/c*100:.2f} % / rok  (návratnost {c/n:.1f} let)")

def cmd_fv(a):
    "fv <castka> <sazba%> <roky> — budoucí hodnota (složené úročení)"
    c,r,y=float(a[0]),float(a[1])/100,float(a[2]); print(f"FV = {c*(1+r)**y:,.0f} Kč za {y:g} let při {r*100:.2f} %")

def cmd_pv(a):
    "pv <castka> <sazba%> <roky> — současná hodnota (diskont)"
    c,r,y=float(a[0]),float(a[1])/100,float(a[2]); print(f"PV = {c/(1+r)**y:,.0f} Kč (diskont {y:g} let při {r*100:.2f} %)")

CMDS={k[4:]:v for k,v in globals().items() if k.startswith("cmd_")}

if __name__=="__main__":
    if len(sys.argv)<2 or sys.argv[1] not in CMDS:
        print("příkazy:")
        for k,v in CMDS.items(): print(f"  {k:9} {v.__doc__}")
        sys.exit(0)
    CMDS[sys.argv[1]](sys.argv[2:])
