"""solve.py -- solve the CUZK "rotate the 9 map tiles" challenge offline.

Authorization: see adjacent AUTHORIZATION.md.

Input : directory with tile0.jpg .. tile8.jpg (grid order, 0 = top-left, 8 = bottom-right).
Output: CW clicks per tile (0-3), confidence margin, optional solution.png / JSON.

Per seam (12 seams x 16 rotation pairs) a vector of raw compatibility terms is computed
from the usable content after auto-trimming the degraded border (fade + confetti):
  col   fill-colour continuity of the outermost usable pixels (+-2 px tolerance)
  line  line-profile overlap (IoU, +-3 px, colour weighted)
  port  thin lines leaving the tile are extrapolated across the seam gap by a
        least-squares direction fit and matched to the neighbour's ports
  mgc   Mahalanobis gradient compatibility (Gallagher 2012) generalised to a gap:
        the colour jump across the seam is scored against the distribution of
        equal-length jumps inside the tile
Terms are combined with weights, each seam is made relative to its own best pair
(blank seams add 0) and all 4^9 assignments are summed exactly.
margin = best - second best total; tile_margin[i] = cost of flipping tile i alone.

usage: uv run --python 3.14 --with pillow --with numpy --with scipy python solve.py <dir>
          [--trim auto|N] [--save solution.png] [--json out.json] [--weights a,b,c,d]
"""

import argparse
import json
import os

import numpy as np
from PIL import Image
from scipy.ndimage import gaussian_filter, label, maximum_filter1d

S = 128  # working size of the trimmed content
BAND = 14  # depth of the band kept per edge
TAU = 14.0  # high-pass magnitude threshold for "line pixel"
NCOMP = 4  # col, line, port, mgc
VERSION = 2  # bump when seam_components changes (invalidates _tensor.npz caches)
DEFAULT_W = (1.0, 1.0, 0.5, 1.0)
H_SEAMS = [
    (r * 3 + c, r * 3 + c + 1) for r in range(3) for c in range(2)
]  # a.R <-> b.L
V_SEAMS = [
    (r * 3 + c, (r + 1) * 3 + c) for r in range(2) for c in range(3)
]  # a.B <-> b.T


# ----------------------------------------------------------------------------- loading
def load_tiles(d):
    tiles = []
    for i in range(9):
        p = os.path.join(d, f"tile{i}.jpg")
        if not os.path.exists(p):
            p = os.path.join(d, f"tile{i}.png")
        with Image.open(p) as image:
            tiles.append(np.asarray(image.convert("RGB")).astype(np.float32))
    return tiles


def auto_trim(tiles):
    """first index (from each side) where mean brightness reaches 97% of the plateau."""
    ks = []
    for a in tiles:
        g = a.mean(2)
        for prof in (g.mean(1), g[::-1].mean(1), g.mean(0), g[:, ::-1].mean(0)):
            plateau = np.median(prof[10:25])
            ks.append(int(np.argmax(prof >= 0.97 * plateau)))
    t = int(np.percentile(np.array(ks), 90)) + 1
    return int(min(max(t, 0), 12))


def prep(a, trim):
    """trim border, resize to SxS, compute colour / high-pass / soft line mask."""
    h, w, _ = a.shape
    a = a[trim : h - trim, trim : w - trim]
    im = Image.fromarray(np.clip(a, 0, 255).astype(np.uint8)).resize(
        (S, S), Image.LANCZOS
    )
    rgb = np.asarray(im).astype(np.float32)
    bg = np.stack([gaussian_filter(rgb[..., c], 10) for c in range(3)], -1)
    hp = rgb - bg
    mag = np.sqrt((hp**2).sum(-1))
    soft = np.clip((mag - TAU) / TAU, 0, 1)
    return {"rgb": rgb, "hp": hp, "soft": soft}


def rot(d, k):
    return {n: np.ascontiguousarray(np.rot90(v, -k)) for n, v in d.items()}


def bands(d):
    """4 edges -> descriptors built from band arrays [depth, pos, ...], pos in natural order."""
    out = {}
    for side in "TBLR":
        sel = {}
        for n, v in d.items():
            if side == "T":
                b = v[:BAND]
            elif side == "B":
                b = v[::-1][:BAND]
            elif side == "L":
                b = np.swapaxes(v[:, :BAND], 0, 1)
            else:
                b = np.swapaxes(v[:, ::-1][:, :BAND], 0, 1)
            sel[n] = np.ascontiguousarray(b)
        out[side] = edge_desc(sel)
    return out


def edge_desc(b):
    rgb, hp, soft = b["rgb"], b["hp"], b["soft"]
    col = rgb[0:3].mean(0)  # [pos,3]  outermost colour
    lprof = soft[0:3].max(0)  # [pos]    line presence at the edge
    lcol = (hp[0:3] * soft[0:3][..., None]).sum(0) / (soft[0:3].sum(0)[:, None] + 1e-6)
    ports = extract_ports(soft[:10], hp[:10])
    return {"col": col, "band": rgb, "lprof": lprof, "lcol": lcol, "ports": ports}


def extract_ports(soft, hp):
    """thin line components touching the edge -> (p0, slope, colour, weight, depth spread)."""
    mask = soft > 0.25
    lab, n = label(mask, structure=np.ones((3, 3)))
    ports = []
    if n == 0:
        return ports
    touch = np.unique(lab[0:2][lab[0:2] > 0]).tolist()
    for cid in touch:
        dd, pp = np.nonzero(lab == cid)
        if len(dd) < 3 or len(dd) > 90:  # tiny noise / blob or text: not a thin line
            continue
        w = soft[dd, pp]
        colour = (hp[dd, pp] * w[:, None]).sum(0) / w.sum()
        if dd.max() - dd.min() >= 2:
            A = np.stack([np.ones_like(dd, float), dd.astype(float)], 1)
            W = np.sqrt(w)
            sol, *_ = np.linalg.lstsq(A * W[:, None], pp.astype(float) * W, rcond=None)
            p0, slope = float(sol[0]), float(np.clip(sol[1], -3.0, 3.0))
            spread = float(dd.max() - dd.min())
        else:
            p0, slope, spread = float(pp[dd <= 1].mean()), 0.0, 0.0
        ports.append((p0, slope, colour, float(w.sum()), spread))
    return ports


# ----------------------------------------------------------------------------- scoring
def _mgc_one(ba, cb, L, lam=25.0):
    """jump A.edge -> B.edge scored against jumps of length L inside A. returns mean dist."""
    a0 = ba[0:2].mean(0)
    aL = ba[L : L + 2].mean(0)
    JA = a0 - aL  # internal jumps [pos,3]
    mu = JA.mean(0)
    cov = np.cov(JA.T) + lam * np.eye(3)
    inv = np.linalg.inv(cov)
    G = cb - a0 - mu
    return float(np.einsum("pi,ij,pj->p", G, inv, G).mean())


def seam_components(ea, eb, gap):
    """returns np.array([col, line, port, mgc]) -- all 'higher is better'."""
    # (1) fill colour continuity, +-2 px tolerance, robust
    ca, cb = ea["col"], eb["col"]
    best = np.full(len(ca), 1e9, np.float32)
    for s in range(-2, 3):
        best = np.minimum(best, np.abs(ca - np.roll(cb, s, axis=0)).sum(1))
    best[:3] = best[:3].min()
    best[-3:] = best[-3:].min()
    s_col = 1.0 - np.clip(best, 0, 90).mean() / 90.0

    # (2) line profile overlap (colour weighted IoU, +-3 px)
    la, lb = ea["lprof"], eb["lprof"]
    lbd = maximum_filter1d(lb, 7)
    lad = maximum_filter1d(la, 7)
    inter = (np.minimum(la, lbd) + np.minimum(lb, lad)) / 2
    union = np.maximum(la, lb).sum() + 1e-6
    both = np.minimum(la, lbd) > 0
    if both.any():
        cd = np.abs(ea["lcol"][both] - eb["lcol"][both]).sum(1)
        inter_val = (inter[both] * (0.4 + 0.6 * np.clip(1 - cd / 120.0, 0, 1))).sum()
    else:
        inter_val = 0.0
    s_line = inter_val / union if union > 1 else 0.0

    # (3) port matching across the gap
    pa, pb = ea["ports"], eb["ports"]
    s_port = 0.0
    if pa or pb:
        g = gap / 2.0
        ma = [(p0 - g * sl, sl, c, w, sp) for p0, sl, c, w, sp in pa]
        mb = [(p0 - g * sl, sl, c, w, sp) for p0, sl, c, w, sp in pb]
        pairs = []
        for i, (pma, sla, ca_, wa, spa) in enumerate(ma):
            for j, (pmb, slb, cb_, wb, spb) in enumerate(mb):
                tol = 3.0 + 0.6 * (abs(sla) + abs(slb)) * g * (
                    1.0 if min(spa, spb) >= 3 else 2.0
                )
                dpos = abs(pma - pmb) / tol
                dcolr = np.abs(ca_ - cb_).sum() / 90.0
                dslope = abs(sla + slb) / 1.2 if min(spa, spb) >= 3 else 0.0
                pairs.append((dpos + dcolr + 0.5 * dslope, i, j))
        pairs.sort()
        used_a, used_b, matched = set(), set(), 0.0
        for cost, i, j in pairs:
            if cost > 1.6:
                break
            if i in used_a or j in used_b:
                continue
            used_a.add(i)
            used_b.add(j)
            matched += (1.6 - cost) / 1.6
        unmatched = (len(ma) - len(used_a)) + (len(mb) - len(used_b))
        s_port = (matched - 0.45 * unmatched) / max(2.0, (len(ma) + len(mb)) / 2.0)

    # (4) MGC generalised to the gap
    L = int(min(BAND - 2, max(1, round(gap) + 1)))
    d = _mgc_one(ea["band"], eb["col"], L) + _mgc_one(eb["band"], ea["col"], L)
    s_mgc = -np.log1p(d)
    return np.array([s_col, s_line, s_port, s_mgc], np.float32)


def seam_tensor(E, gap):
    """[12,4,4,NCOMP] raw components for every seam / rotation pair (seam order H then V)."""
    T = np.zeros((12, 4, 4, NCOMP), np.float32)
    seams = [(a, b, "R", "L") for a, b in H_SEAMS] + [
        (a, b, "B", "T") for a, b in V_SEAMS
    ]
    for si, (a, b, sa, sb) in enumerate(seams):
        for ra in range(4):
            for rb in range(4):
                T[si, ra, rb] = seam_components(E[a][ra][sa], E[b][rb][sb], gap)
    return T


def solve_tensor(T, weights=DEFAULT_W, prior=None):
    """exact 4^9 maximisation of the weighted, seam-relative score."""
    w = np.asarray(weights, np.float32)
    seams = H_SEAMS + V_SEAMS
    total = np.zeros([4] * 9, np.float32)
    for si, (a, b) in enumerate(seams):
        t = T[si] @ w
        t = t - t.max()
        shape = [1] * 9
        shape[a] = 4
        shape[b] = 4
        total += t.reshape(shape)
    if prior is not None:
        for i in range(9):
            shape = [1] * 9
            shape[i] = 4
            total += np.asarray(prior[i], np.float32).reshape(shape)
    flat = total.ravel()
    order = np.argpartition(flat, -2)[-2:]
    order = order[np.argsort(flat[order])[::-1]]
    best = np.unravel_index(order[0], total.shape)
    second = np.unravel_index(order[1], total.shape)
    margin = float(flat[order[0]] - flat[order[1]])
    tile_margin = []
    for i in range(9):
        alt = []
        for k in range(4):
            if k == best[i]:
                continue
            sl = [slice(None)] * 9
            sl[i] = k
            alt.append(float(total[tuple(sl)].max()))
        tile_margin.append(float(flat[order[0]] - max(alt)))
    return {
        "cw": [int(x) for x in best],
        "second": [int(x) for x in second],
        "margin": margin,
        "tile_margin": tile_margin,
        "score": float(flat[order[0]]),
    }


def analyse_dir(d, trim="auto"):
    """everything up to the seam tensor (the expensive part)."""
    tiles = load_tiles(d)
    h, w, _ = tiles[0].shape
    t = auto_trim(tiles) if trim == "auto" else int(trim)
    side = min(h, w) - 2 * t
    gap = 2 * t * S / side
    P = [prep(a, t) for a in tiles]
    E = [[bands(rot(P[i], k)) for k in range(4)] for i in range(9)]
    return {"T": seam_tensor(E, gap), "trim": t, "gap": gap}


def solve_dir(d, trim="auto", weights=DEFAULT_W, prior=None):
    A = analyse_dir(d, trim)
    r = solve_tensor(A["T"], weights, prior)
    r.update({"trim": A["trim"], "gap": A["gap"]})
    return r


def save_solution(d, cw, path, trim):
    tiles = load_tiles(d)
    h, w, _ = tiles[0].shape
    n = min(h, w) - 2 * trim
    cv = Image.new("RGB", (n * 3 + 4, n * 3 + 4), (0, 0, 0))
    for i, a in enumerate(tiles):
        a = a[trim : h - trim, trim : w - trim][:n, :n]
        im = Image.fromarray(np.rot90(a, -cw[i]).astype(np.uint8))
        r, c = divmod(i, 3)
        cv.paste(im, (c * (n + 2), r * (n + 2)))
    cv.save(path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("dir")
    ap.add_argument("--trim", default="auto")
    ap.add_argument("--save", default=None)
    ap.add_argument("--json", default=None)
    ap.add_argument("--weights", default=None, help="comma list: col,line,port,mgc")
    ap.add_argument("--quiet", action="store_true")
    a = ap.parse_args()
    w = tuple(float(x) for x in a.weights.split(",")) if a.weights else DEFAULT_W
    r = solve_dir(a.dir, a.trim, w)
    lp = os.path.join(a.dir, "labels.json")
    if os.path.exists(lp):
        with open(lp) as stream:
            truth = json.load(stream)["recover_cw"]
        r["truth"] = truth
        r["correct_tiles"] = int(sum(int(x == y) for x, y in zip(r["cw"], truth)))
    if not a.quiet:
        print(
            "cw:",
            r["cw"],
            " margin={:.2f}".format(r["margin"]),
            f" trim={r['trim']:d} gap={r['gap']:.1f}",
        )
        print("tile margins:", " ".join(f"{x:.1f}" for x in r["tile_margin"]))
        if "truth" in r:
            print("truth:", r["truth"], f" correct tiles: {r['correct_tiles']:d}/9")
    if a.save:
        save_solution(a.dir, r["cw"], a.save, r["trim"])
    if a.json:
        with open(a.json, "w") as stream:
            json.dump(r, stream)
    return r


if __name__ == "__main__":
    main()
