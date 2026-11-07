import sys
from itertools import product

import numpy as np
from PIL import Image

TILES_DIR = sys.argv[1] if len(sys.argv) > 1 else "."
TRIM = 10  # trim speckled border px
STRIP = 6  # edge strip thickness to sample
N = 120  # working size after trim/resize


def load(i):
    im = Image.open(f"{TILES_DIR}/tile{i}.jpg").convert("RGB")
    a = np.asarray(im).astype(np.float32)
    h, w, _ = a.shape
    a = a[TRIM : h - TRIM, TRIM : w - TRIM]  # trim border
    im2 = Image.fromarray(a.astype(np.uint8)).resize((N, N))
    return np.asarray(im2).astype(np.float32)


def linemask(rgb):
    # roads/boundaries = saturated OR dark; background pastel/light/low-sat green
    mx = rgb.max(2)
    mn = rgb.min(2)
    sat = mx - mn  # chroma
    val = mx
    dark = val < 150
    colored = sat > 55  # strong color (pink/purple/yellow/red roads)
    mask = (colored | dark).astype(np.float32)
    return mask


def rot_cw(a, k):  # k clockwise 90° turns = what k clicks produce
    return np.rot90(a, -k)


def edges(mask):
    # return dict of 1D signatures, fixed orientation:
    # top/bottom indexed left->right ; left/right indexed top->bottom
    top = mask[:STRIP, :].max(0)
    bottom = mask[-STRIP:, :].max(0)
    left = mask[:, :STRIP].max(1)
    right = mask[:, -STRIP:].max(1)
    return {"T": top, "B": bottom, "L": left, "R": right}


def match(a, b):
    # overlap of two edge signatures (same indexing/direction)
    inter = np.minimum(a, b).sum()
    denom = (a.sum() + b.sum()) / 2 + 1e-6
    return inter / denom


# precompute edges for each tile, each cw rotation
E = [[edges(linemask(rot_cw(load(i), k))) for k in range(4)] for i in range(9)]


# grid positions: idx = r*3+c
def pos(i):
    return divmod(i, 3)


# adjacencies: horizontal (i, i+1 same row); vertical (i, i+3)
H = [(r * 3 + c, r * 3 + c + 1) for r in range(3) for c in range(2)]
V = [(r * 3 + c, (r + 1) * 3 + c) for r in range(2) for c in range(3)]


# pair score tables
def hscore(a, b, ra, rb):  # a left of b: a.Right vs b.Left (top->bottom)
    return match(E[a][ra]["R"], E[b][rb]["L"])


def vscore(a, b, ra, rb):  # a above b: a.Bottom vs b.Top (left->right)
    return match(E[a][ra]["B"], E[b][rb]["T"])


Hpre = {
    (a, b): np.array([[hscore(a, b, ra, rb) for rb in range(4)] for ra in range(4)])
    for a, b in H
}
Vpre = {
    (a, b): np.array([[vscore(a, b, ra, rb) for rb in range(4)] for ra in range(4)])
    for a, b in V
}

best = None
bestscore = -1
for rot in product(range(4), repeat=9):
    s = 0.0
    for a, b in H:
        s += Hpre[(a, b)][rot[a], rot[b]]
    for a, b in V:
        s += Vpre[(a, b)][rot[a], rot[b]]
    if s > bestscore:
        bestscore = s
        best = rot

print("BEST rotations (cw clicks per tile 0..8):", best, f"score={bestscore:.3f}")
# save montage of solution
tiles = [Image.fromarray(rot_cw(load(i), best[i]).astype(np.uint8)) for i in range(9)]
canvas = Image.new("RGB", (N * 3 + 8, N * 3 + 8), (0, 0, 0))
for i, t in enumerate(tiles):
    r, c = divmod(i, 3)
    canvas.paste(t, (c * (N + 2), r * (N + 2)))
canvas.save(f"{TILES_DIR}/solution.png")
print("saved solution.png")
