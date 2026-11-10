import sys
from itertools import product

import numpy as np
from PIL import Image
from scipy.ndimage import maximum_filter1d

D = sys.argv[1] if len(sys.argv) > 1 else "."
TRIM = 12
N = 120
TOL = 5


def load(i):
    im = Image.open(f"{D}/tile{i}.jpg").convert("RGB")
    a = np.asarray(im)
    h, w, _ = a.shape
    a = a[TRIM : h - TRIM, TRIM : w - TRIM]
    return np.asarray(Image.fromarray(a).resize((N, N))).astype(np.float32)


def rot_cw(a, k):
    return np.ascontiguousarray(np.rot90(a, -k))


def linemask(a):
    r, g, b = a[..., 0], a[..., 1], a[..., 2]
    mx = a.max(2)
    mn = a.min(2)
    sat = mx - mn
    val = mx
    green = (g > r + 10) & (g > b + 10)
    (sat < 25) & (val > 150)
    line = (
        (sat > 38) & (~green) & (val < 235)
    )  # colored thin lines (pink/purple/yellow/red), not green fields, not pale
    dark = (val < 120) & (~green)
    return (line | dark).astype(np.float32)


def edgesig(m):
    S = 6
    top = m[:S, :].max(0)
    bot = m[-S:, :].max(0)
    left = m[:, :S].max(1)
    right = m[:, -S:].max(1)
    # tolerance dilation
    f = lambda v: maximum_filter1d(v, TOL)
    return {"T": f(top), "B": f(bot), "L": f(left), "R": f(right)}


E = [[edgesig(linemask(rot_cw(load(i), k))) for k in range(4)] for i in range(9)]


def mscore(u, v):
    inter = np.minimum(u, v).sum()
    union = np.maximum(u, v).sum() + 1e-6
    return inter / union  # IoU of line presence along edge


H = [(r * 3 + c, r * 3 + c + 1) for r in range(3) for c in range(2)]
V = [(r * 3 + c, (r + 1) * 3 + c) for r in range(2) for c in range(3)]
Hpre = {
    (a, b): np.array(
        [[mscore(E[a][ra]["R"], E[b][rb]["L"]) for rb in range(4)] for ra in range(4)]
    )
    for a, b in H
}
Vpre = {
    (a, b): np.array(
        [[mscore(E[a][ra]["B"], E[b][rb]["T"]) for rb in range(4)] for ra in range(4)]
    )
    for a, b in V
}
best = None
bs = -1
for rot in product(range(4), repeat=9):
    s = 0.0
    for a, b in H:
        s += Hpre[(a, b)][rot[a], rot[b]]
    for a, b in V:
        s += Vpre[(a, b)][rot[a], rot[b]]
    if s > bs:
        bs = s
        best = rot
print("BEST cw:", best, f"IoU-sum={bs:.3f}")
tiles = [Image.fromarray(rot_cw(load(i), best[i]).astype(np.uint8)) for i in range(9)]
cv = Image.new("RGB", (N * 3 + 8, N * 3 + 8), (0, 0, 0))
for i, t in enumerate(tiles):
    r, c = divmod(i, 3)
    cv.paste(t, (c * (N + 2), r * (N + 2)))
cv.save(f"{D}/solution4.png")
