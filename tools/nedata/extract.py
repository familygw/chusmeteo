"""Genera internal/geo/data/sudamerica.json a partir de Natural Earth 1:10m.

Natural Earth es de dominio público: https://www.naturalearthdata.com
Uso (desde la raíz del repo, con un clon de nvkelso/natural-earth-vector):
    python3 tools/nedata/extract.py <ruta>/natural-earth-vector/geojson

Recorta a la ventana del mapa, simplifica (Douglas-Peucker) y descarta islotes.
Las Malvinas se suman a Argentina y a la provincia de Tierra del Fuego,
como en la cartografía oficial argentina.
"""
import json, sys, os

W, E, S, N = -77.0, -50.0, -57.0, -19.5   # ventana (un poco más grande que el mapa)
TOL = 0.02        # tolerancia de simplificación en grados (~2 km)
MIN_AREA = 0.004  # descartar anillos más chicos (grados²)
NEIGHBORS = {'CHL': 'Chile', 'URY': 'Uruguay', 'PRY': 'Paraguay', 'BOL': 'Bolivia', 'BRA': 'Brasil', 'PER': 'Perú'}

def clip(ring):
    """Sutherland-Hodgman contra el rectángulo de la ventana."""
    def cut(pts, inside, inter):
        out = []
        for i in range(len(pts)):
            a, b = pts[i - 1], pts[i]
            ia, ib = inside(a), inside(b)
            if ib:
                if not ia: out.append(inter(a, b))
                out.append(b)
            elif ia:
                out.append(inter(a, b))
        return out
    def ix(x):
        return lambda a, b: (x, a[1] + (b[1] - a[1]) * (x - a[0]) / (b[0] - a[0]))
    def iy(y):
        return lambda a, b: (a[0] + (b[0] - a[0]) * (y - a[1]) / (b[1] - a[1]), y)
    pts = [tuple(p[:2]) for p in ring]
    for inside, inter in ((lambda p: p[0] >= W, ix(W)), (lambda p: p[0] <= E, ix(E)),
                          (lambda p: p[1] >= S, iy(S)), (lambda p: p[1] <= N, iy(N))):
        if not pts: break
        pts = cut(pts, inside, inter)
    return pts

def dp(pts, tol):
    if len(pts) < 3: return pts
    a, b = pts[0], pts[-1]
    dx, dy = b[0] - a[0], b[1] - a[1]
    norm = (dx * dx + dy * dy) ** 0.5
    best, idx = -1, 0
    for i in range(1, len(pts) - 1):
        p = pts[i]
        d = abs(dy * (p[0] - a[0]) - dx * (p[1] - a[1])) / norm if norm else ((p[0]-a[0])**2 + (p[1]-a[1])**2) ** 0.5
        if d > best: best, idx = d, i
    if best <= tol: return [a, b]
    return dp(pts[:idx + 1], tol)[:-1] + dp(pts[idx:], tol)

def simplify(ring):
    if len(ring) < 4: return ring
    # partimos el anillo en dos mitades para que DP no colapse anillos cerrados
    m = len(ring) // 2
    return dp(ring[:m + 1], TOL)[:-1] + dp(ring[m:] + [ring[0]], TOL)[:-1]

def area(r):
    return abs(sum(r[i - 1][0] * r[i][1] - r[i][0] * r[i - 1][1] for i in range(len(r)))) / 2

def rings_of(geom):
    polys = geom['coordinates'] if geom['type'] == 'MultiPolygon' else [geom['coordinates']]
    out = []
    for poly in polys:
        for ring in poly:  # exterior + agujeros: con par/impar se resuelven solos
            r = clip(ring)
            if len(r) < 3 or area(r) < MIN_AREA: continue
            r = simplify(r)
            if len(r) >= 3:
                out.append([round(v, 3) for p in r for v in p])  # plano: lon,lat,lon,lat...
    return out

src = sys.argv[1]
countries = json.load(open(os.path.join(src, 'ne_10m_admin_0_countries.geojson')))['features']
provinces = json.load(open(os.path.join(src, 'ne_10m_admin_1_states_provinces.geojson')))['features']

by = {f['properties']['ADM0_A3']: f for f in countries}
malvinas = rings_of(by['FLK']['geometry'])
out = {
    'source': 'Natural Earth 1:10m (dominio público), recortado y simplificado por tools/nedata/extract.py',
    'countries': [{'name': 'Argentina', 'code': 'ARG', 'rings': rings_of(by['ARG']['geometry']) + malvinas}],
    'provinces': [],
}
for code, name in NEIGHBORS.items():
    rs = rings_of(by[code]['geometry'])
    if rs: out['countries'].append({'name': name, 'code': code, 'rings': rs})
for f in sorted((f for f in provinces if f['properties'].get('adm0_a3') == 'ARG'), key=lambda f: f['properties']['name']):
    name = f['properties']['name']
    rs = rings_of(f['geometry'])
    if name == 'Tierra del Fuego':
        rs += malvinas
    out['provinces'].append({'name': name, 'rings': rs})

dst = 'internal/geo/data/sudamerica.json'
json.dump(out, open(dst, 'w'), separators=(',', ':'), ensure_ascii=False)
pts = lambda items: sum(len(r) // 2 for i in items for r in i['rings'])
print(f"{dst}: {os.path.getsize(dst)//1024} KB, países {pts(out['countries'])} vértices, provincias {pts(out['provinces'])} vértices")
for c in out['countries']: print('  ', c['name'], len(c['rings']), 'anillos')
