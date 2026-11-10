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
    return np.rot90(a, -k)


# boundary strips (depth 2 averaged) in RGB, fixed orientation
def strips(a):
    top = a[:2, :, :].mean(0)
    bot = a[-2:, :, :].mean(0)  # indexed by x (left->right)
    left = a[:, :2, :].mean(1)
    right = a[:, -2:, :].mean(1)  # indexed by y (top->bottom)
    return {"T": top, "B": bot, "L": left, "R": right}


S = [[strips(rot_cw(load(i), k)) for k in range(4)] for i in range(9)]


def ssd(u, v):
    return float(((u - v) ** 2).sum())


H = [(r * 3 + c, r * 3 + c + 1) for r in range(3) for c in range(2)]
V = [(r * 3 + c, (r + 1) * 3 + c) for r in range(2) for c in range(3)]
Hpre = {
    (a, b): np.array(
        [[ssd(S[a][ra]["R"], S[b][rb]["L"]) for rb in range(4)] for ra in range(4)]
    )
    for a, b in H
}
Vpre = {
    (a, b): np.array(
        [[ssd(S[a][ra]["B"], S[b][rb]["T"]) for rb in range(4)] for ra in range(4)]
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
canvas.save(f"{D}/solution2.png")
