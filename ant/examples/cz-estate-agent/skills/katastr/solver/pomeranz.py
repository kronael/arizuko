import sys
from itertools import product

import numpy as np
from PIL import Image

D = sys.argv[1] if len(sys.argv) > 1 else "."
TRIM = 12
N = 120


def load(i):
    im = Image.open(f"{D}/tile{i}.jpg").convert("RGB")
    a = np.asarray(im)
    h, w, _ = a.shape
    a = a[TRIM : h - TRIM, TRIM : w - TRIM]
    return np.asarray(Image.fromarray(a).resize((N, N))).astype(np.float32)


def rot_cw(a, k):
    return np.ascontiguousarray(np.rot90(a, -k))


# For each tile+rot keep the two outer rows/cols on each side for gradient prediction.
def bands(a):
    return {
        "T": (a[0, :, :], a[1, :, :]),  # outer,inner  (x index)
        "B": (a[-1, :, :], a[-2, :, :]),
        "L": (a[:, 0, :], a[:, 1, :]),  # (y index)
        "R": (a[:, -1, :], a[:, -2, :]),
    }


B = [[bands(rot_cw(load(i), k)) for k in range(4)] for i in range(9)]


def pdiss(out_a, in_a, out_b, in_b):
    # predict b-outer from a gradient and vice versa, L1 (Pomeranz p=1)
    pred_b = 2 * out_a - in_a
    pred_a = 2 * out_b - in_b
    return float(np.abs(pred_b - out_b).sum() + np.abs(pred_a - out_a).sum())


H = [(r * 3 + c, r * 3 + c + 1) for r in range(3) for c in range(2)]
V = [(r * 3 + c, (r + 1) * 3 + c) for r in range(2) for c in range(3)]
# horizontal: a.R meets b.L
Hpre = {
    (a, b): np.array(
        [[pdiss(*B[a][ra]["R"], *B[b][rb]["L"]) for rb in range(4)] for ra in range(4)]
    )
    for a, b in H
}
Vpre = {
    (a, b): np.array(
        [[pdiss(*B[a][ra]["B"], *B[b][rb]["T"]) for rb in range(4)] for ra in range(4)]
    )
    for a, b in V
}

best = None
bestd = 1e18
for rot in product(range(4), repeat=9):
    d = 0.0
    for a, b in H:
        d += Hpre[(a, b)][rot[a], rot[b]]
    for a, b in V:
        d += Vpre[(a, b)][rot[a], rot[b]]
    if d < bestd:
        bestd = d
        best = rot
print("BEST cw clicks:", best, f"dissim={bestd:.0f}")
tiles = [Image.fromarray(rot_cw(load(i), best[i]).astype(np.uint8)) for i in range(9)]
canvas = Image.new("RGB", (N * 3 + 8, N * 3 + 8), (0, 0, 0))
for i, t in enumerate(tiles):
    r, c = divmod(i, 3)
    canvas.paste(t, (c * (N + 2), r * (N + 2)))
canvas.save(f"{D}/solution3.png")
